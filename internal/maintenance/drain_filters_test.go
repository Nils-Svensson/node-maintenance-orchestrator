package maintenance

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestClassifyPods(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	defaultCfg := &drainConfig{IgnoreDaemonSets: true, RespectPodDisruptionBudgets: true}

	mirrorPod := func() corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "p",
				Annotations: map[string]string{corev1.MirrorPodAnnotationKey: "mirror"},
			},
		}
	}

	daemonSetPod := func() corev1.Pod {
		ctrl := true
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "p",
				OwnerReferences: []metav1.OwnerReference{{Kind: "DaemonSet", Controller: &ctrl}},
			},
		}
	}

	barePod := func() corev1.Pod {
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
	}

	controlledPod := func() corev1.Pod {
		ctrl := true
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "p",
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Controller: &ctrl}},
			},
		}
	}

	withEmptyDir := func(p corev1.Pod) corev1.Pod {
		p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{
			Name:         "tmp",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		return p
	}

	// withDeletionTimestamp sets DeletionTimestamp to (now - ago) and an optional grace period.
	withDeletionTimestamp := func(p corev1.Pod, ago time.Duration, graceSecs *int64) corev1.Pod {
		ts := metav1.NewTime(now.Add(-ago))
		p.DeletionTimestamp = &ts
		p.Spec.TerminationGracePeriodSeconds = graceSecs
		return p
	}

	grace := func(s int64) *int64 { return &s }

	tests := []struct {
		name       string
		pod        corev1.Pod
		cfg        *drainConfig
		wantBucket string // evictable | blocked | skipped | terminating | stuckTerminating
	}{
		// Always-skipped regardless of config
		{"mirror pod", mirrorPod(), defaultCfg, "skipped"},
		{
			name: "succeeded pod",
			pod:  corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
			cfg:  defaultCfg, wantBucket: "skipped",
		},
		{
			name: "failed pod",
			pod:  corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}, Status: corev1.PodStatus{Phase: corev1.PodFailed}},
			cfg:  defaultCfg, wantBucket: "skipped",
		},

		// DaemonSet pods
		{"daemonset / ignoreDaemonSets=true", daemonSetPod(), defaultCfg, "skipped"},
		{"daemonset / ignoreDaemonSets=false", daemonSetPod(), &drainConfig{IgnoreDaemonSets: false}, "blocked"},

		// Uncontrolled pods
		{"bare pod / force=false", barePod(), defaultCfg, "blocked"},
		{"bare pod / force=true", barePod(), &drainConfig{Force: true, IgnoreDaemonSets: true}, "evictable"},

		// EmptyDir
		{"emptyDir / deleteEmptyDirData=false", withEmptyDir(controlledPod()), defaultCfg, "blocked"},
		{"emptyDir / deleteEmptyDirData=true", withEmptyDir(controlledPod()), &drainConfig{DeleteEmptyDirData: true, IgnoreDaemonSets: true}, "evictable"},

		// Normal evictable
		{"controlled pod, no emptyDir", controlledPod(), defaultCfg, "evictable"},

		// Terminating — DeletionTimestamp set but within grace + buffer window.
		// With grace=30s and buffer=60s, the deadline is deletionTimestamp+90s.
		// The pod stays Terminating as long as now <= deadline.
		{"terminating: well within window (grace=30s)", withDeletionTimestamp(barePod(), 10*time.Second, grace(30)), defaultCfg, "terminating"},
		{"terminating: within custom grace period (grace=120s)", withDeletionTimestamp(barePod(), 50*time.Second, grace(120)), defaultCfg, "terminating"},
		{"terminating: nil grace falls back to 30s default", withDeletionTimestamp(barePod(), 10*time.Second, nil), defaultCfg, "terminating"},
		// At the exact boundary (deleted exactly grace+buffer ago) now.After(deadline)==false -> still Terminating.
		{"terminating: at exact grace+buffer boundary (grace=30s, ago=90s)", withDeletionTimestamp(barePod(), 90*time.Second, grace(30)), defaultCfg, "terminating"},

		// StuckTerminating — past grace + buffer.
		{"stuck: 1s past grace+buffer (grace=30s, ago=91s)", withDeletionTimestamp(barePod(), 91*time.Second, grace(30)), defaultCfg, "stuckTerminating"},
		{"stuck: nil grace uses 30s default, past window", withDeletionTimestamp(barePod(), 91*time.Second, nil), defaultCfg, "stuckTerminating"},
		{"stuck: past custom grace+buffer (grace=120s, ago=200s)", withDeletionTimestamp(barePod(), 200*time.Second, grace(120)), defaultCfg, "stuckTerminating"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyPods([]corev1.Pod{tt.pod}, tt.cfg, now)

			counts := map[string]int{
				"evictable":        len(result.Evictable),
				"blocked":          len(result.Blocked),
				"skipped":          len(result.Skipped),
				"terminating":      len(result.Terminating),
				"stuckTerminating": len(result.StuckTerminating),
			}

			if counts[tt.wantBucket] != 1 {
				t.Errorf("want bucket %q; got counts %v", tt.wantBucket, counts)
			}
			total := counts["evictable"] + counts["blocked"] + counts["skipped"] + counts["terminating"] + counts["stuckTerminating"]
			if total != 1 {
				t.Errorf("expected exactly 1 pod classified total, got %d", total)
			}
		})
	}
}
