package maintenance

import (
	"testing"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

func TestGetDrainConfig_NilDrain(t *testing.T) {
	if getDrainConfig(&v1alpha1.NodeMaintenancePlan{}) != nil {
		t.Error("expected nil when Drain is nil")
	}
}

func TestGetDrainConfig_DrainDisabled(t *testing.T) {
	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Drain = &v1alpha1.DrainSpec{Enabled: false}
	if getDrainConfig(plan) != nil {
		t.Error("expected nil when Drain.Enabled=false")
	}
}

func TestGetDrainConfig_Defaults(t *testing.T) {
	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Drain = &v1alpha1.DrainSpec{Enabled: true} // Options is nil

	cfg := getDrainConfig(plan)
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if !cfg.IgnoreDaemonSets {
		t.Error("IgnoreDaemonSets should default to true")
	}
	if !cfg.RespectPodDisruptionBudgets {
		t.Error("RespectPodDisruptionBudgets should default to true")
	}
	if cfg.Force {
		t.Error("Force should default to false")
	}
	if cfg.DeleteEmptyDirData {
		t.Error("DeleteEmptyDirData should default to false")
	}
	if cfg.PodTerminationGracePeriodSeconds != nil {
		t.Error("PodTerminationGracePeriodSeconds should default to nil")
	}
}

func TestGetDrainConfig_ExplicitOptions(t *testing.T) {
	grace := int64(10)
	plan := &v1alpha1.NodeMaintenancePlan{}
	plan.Spec.Drain = &v1alpha1.DrainSpec{
		Enabled: true,
		Options: &v1alpha1.DrainOptions{
			MaxParallel:                      3, // plan-level; not in drainConfig
			IgnoreDaemonSets:                 false,
			DeleteEmptyDirData:               true,
			Force:                            true,
			PodTerminationGracePeriodSeconds: &grace,
			RespectPodDisruptionBudgets:      false,
		},
	}

	cfg := getDrainConfig(plan)
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.IgnoreDaemonSets {
		t.Error("IgnoreDaemonSets should be false")
	}
	if !cfg.DeleteEmptyDirData {
		t.Error("DeleteEmptyDirData should be true")
	}
	if !cfg.Force {
		t.Error("Force should be true")
	}
	if cfg.PodTerminationGracePeriodSeconds == nil || *cfg.PodTerminationGracePeriodSeconds != 10 {
		t.Errorf("PodTerminationGracePeriodSeconds = %v, want 10", cfg.PodTerminationGracePeriodSeconds)
	}
	if cfg.RespectPodDisruptionBudgets {
		t.Error("RespectPodDisruptionBudgets should be false")
	}
}
