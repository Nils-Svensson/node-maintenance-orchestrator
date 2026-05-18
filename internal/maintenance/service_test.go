package maintenance_test

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
	"github.com/Nils-Svensson/node-maintenance-orchestrator/internal/maintenance"
)

var testScheme *runtime.Scheme

func init() {
	testScheme = runtime.NewScheme()
	if err := corev1.AddToScheme(testScheme); err != nil {
		panic(err)
	}
	if err := v1alpha1.AddToScheme(testScheme); err != nil {
		panic(err)
	}
}

func newService(objects ...client.Object) (*maintenance.MaintenanceService, *record.FakeRecorder, client.Client) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objects...).
		Build()
	recorder := record.NewFakeRecorder(10)
	svc := maintenance.NewMaintenanceService(fakeClient, logr.Discard(), recorder, nil)
	return svc, recorder, fakeClient
}

func makeNode(name string, unschedulable bool, annotations map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: annotations},
		Spec:       corev1.NodeSpec{Unschedulable: unschedulable},
	}
}

func makePlan(name string, cordonEnabled bool) *v1alpha1.NodeMaintenancePlan {
	p := &v1alpha1.NodeMaintenancePlan{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if cordonEnabled {
		p.Spec.Cordon = &v1alpha1.CordonSpec{Enabled: true}
	}
	return p
}

func requireEvent(t *testing.T, recorder *record.FakeRecorder, contains string) {
	t.Helper()
	select {
	case event := <-recorder.Events:
		if !strings.Contains(event, contains) {
			t.Errorf("event %q does not contain %q", event, contains)
		}
	default:
		t.Errorf("expected event containing %q but none was fired", contains)
	}
}

func requireNoEvent(t *testing.T, recorder *record.FakeRecorder) {
	t.Helper()
	select {
	case event := <-recorder.Events:
		t.Errorf("unexpected event: %q", event)
	default:
	}
}

// ReconcileDrift tests

func TestReconcileDrift_ManualUncordon_ReleasesOwnership(t *testing.T) {
	const planName = "test-plan"
	n := makeNode("node-1", false, map[string]string{
		maintenance.ManagedByAnnotation: planName,
		maintenance.CordonedAnnotation:  "true",
	})
	p := makePlan(planName, true)
	svc, recorder, fakeClient := newService(n, p)

	err := svc.ReconcileDrift(context.Background(), p, &maintenance.OwnershipResolution{Stable: []*corev1.Node{n}})
	if err != nil {
		t.Fatalf("ReconcileDrift: %v", err)
	}

	var got corev1.Node
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "node-1"}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Annotations[maintenance.ManagedByAnnotation] != "" {
		t.Error("ManagedByAnnotation should be removed after ManualUncordon drift")
	}
	requireEvent(t, recorder, "DriftDetected")
}

func TestReconcileDrift_ExternalCordon_RetainsOwnership(t *testing.T) {
	const planName = "test-plan"
	n := makeNode("node-1", true, map[string]string{
		maintenance.ManagedByAnnotation: planName,
		// no CordonedAnnotation — externally cordoned
	})
	p := makePlan(planName, false)
	svc, recorder, fakeClient := newService(n, p)

	err := svc.ReconcileDrift(context.Background(), p, &maintenance.OwnershipResolution{Stable: []*corev1.Node{n}})
	if err != nil {
		t.Fatalf("ReconcileDrift: %v", err)
	}

	var got corev1.Node
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "node-1"}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Annotations[maintenance.ManagedByAnnotation] != planName {
		t.Errorf("ManagedByAnnotation should be retained after ExternalCordon drift, got %q", got.Annotations[maintenance.ManagedByAnnotation])
	}
	requireEvent(t, recorder, "DriftDetected")
}

func TestReconcileDrift_NoDrift_NoAction(t *testing.T) {
	const planName = "test-plan"
	n := makeNode("node-1", true, map[string]string{
		maintenance.ManagedByAnnotation: planName,
		maintenance.CordonedAnnotation:  "true",
	})
	p := makePlan(planName, true)
	svc, recorder, fakeClient := newService(n, p)

	err := svc.ReconcileDrift(context.Background(), p, &maintenance.OwnershipResolution{Stable: []*corev1.Node{n}})
	if err != nil {
		t.Fatalf("ReconcileDrift: %v", err)
	}

	var got corev1.Node
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "node-1"}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Annotations[maintenance.ManagedByAnnotation] != planName {
		t.Error("ManagedByAnnotation should not change when there is no drift")
	}
	requireNoEvent(t, recorder)
}

// ReconcileCordon tests

func TestReconcileCordon_ExternalCordon_NotUncordoned(t *testing.T) {
	const planName = "test-plan"
	n := makeNode("node-1", true, map[string]string{
		maintenance.ManagedByAnnotation: planName,
		// no CordonedAnnotation — externally cordoned
	})
	p := makePlan(planName, false)
	svc, _, fakeClient := newService(n, p)

	err := svc.ReconcileCordon(context.Background(), p, &maintenance.OwnershipResolution{Stable: []*corev1.Node{n}})
	if err != nil {
		t.Fatalf("ReconcileCordon: %v", err)
	}

	var got corev1.Node
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "node-1"}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Spec.Unschedulable {
		t.Error("externally cordoned node should not be uncordoned when cordon is disabled")
	}
}

func TestReconcileCordon_OperatorCordoned_Uncordons(t *testing.T) {
	const planName = "test-plan"
	n := makeNode("node-1", true, map[string]string{
		maintenance.ManagedByAnnotation: planName,
		maintenance.CordonedAnnotation:  "true",
	})
	p := makePlan(planName, false) // cordon disabled — operator should clean up its own cordon
	svc, _, fakeClient := newService(n, p)

	err := svc.ReconcileCordon(context.Background(), p, &maintenance.OwnershipResolution{Stable: []*corev1.Node{n}})
	if err != nil {
		t.Fatalf("ReconcileCordon: %v", err)
	}

	var got corev1.Node
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "node-1"}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Spec.Unschedulable {
		t.Error("operator-cordoned node should be uncordoned when cordon is disabled")
	}
}

func TestReconcileCordon_CordonEnabled_CordonsNode(t *testing.T) {
	const planName = "test-plan"
	n := makeNode("node-1", false, map[string]string{
		maintenance.ManagedByAnnotation: planName,
	})
	p := makePlan(planName, true)
	svc, _, fakeClient := newService(n, p)

	err := svc.ReconcileCordon(context.Background(), p, &maintenance.OwnershipResolution{Stable: []*corev1.Node{n}})
	if err != nil {
		t.Fatalf("ReconcileCordon: %v", err)
	}

	var got corev1.Node
	if err := fakeClient.Get(context.Background(), types.NamespacedName{Name: "node-1"}, &got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Spec.Unschedulable {
		t.Error("node should be cordoned when cordon is enabled")
	}
	if got.Annotations[maintenance.CordonedAnnotation] != "true" {
		t.Error("CordonedAnnotation should be set when operator cordons a node")
	}
}
