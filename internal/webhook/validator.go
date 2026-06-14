package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

// NodeMaintenancePlanValidator validates NodeMaintenancePlan resources at admission time.
//
// For plans with explicit spec.nodes (CREATE and UPDATE):
//   - all listed nodes must exist in the cluster
//   - no listed node may be owned by a different plan
//
// For plans with spec.nodeSelector (CREATE only):
//   - the selector must match at least one existing node
//
// The nodeSelector check is CREATE-only because the node set is frozen into a
// snapshot on first reconcile. Once the snapshot exists, changing the selector
// has no effect, so rejecting an UPDATE that would match nothing is misleading.
type NodeMaintenancePlanValidator struct {
	Client client.Client
}

func (v *NodeMaintenancePlanValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation == admissionv1.Delete {
		return admission.Allowed("ok")
	}

	var plan v1alpha1.NodeMaintenancePlan
	if err := json.Unmarshal(req.Object.Raw, &plan); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if len(plan.Spec.Nodes) == 0 {
		if req.Operation == admissionv1.Create && plan.Spec.NodeSelector != nil {
			return v.validateNodeSelector(ctx, plan.Spec.NodeSelector)
		}
		return admission.Allowed("ok")
	}

	var denied []string
	for _, nodeName := range plan.Spec.Nodes {
		var node corev1.Node
		err := v.Client.Get(ctx, types.NamespacedName{Name: nodeName}, &node)
		if apierrors.IsNotFound(err) {
			denied = append(denied, fmt.Sprintf("node %q does not exist", nodeName))
			continue
		}
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}
		if owner := node.Annotations[v1alpha1.ManagedByAnnotation]; owner != "" && owner != plan.Name {
			denied = append(denied, fmt.Sprintf("node %q is already owned by plan %q", nodeName, owner))
		}
	}

	if len(denied) > 0 {
		return admission.Denied(strings.Join(denied, "; "))
	}
	return admission.Allowed("ok")
}

func (v *NodeMaintenancePlanValidator) validateNodeSelector(ctx context.Context, selector *metav1.LabelSelector) admission.Response {
	s, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("invalid nodeSelector: %w", err))
	}
	var nodeList corev1.NodeList
	if err := v.Client.List(ctx, &nodeList, &client.ListOptions{LabelSelector: s, Limit: 1}); err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if len(nodeList.Items) == 0 {
		return admission.Denied("nodeSelector matches no existing nodes")
	}
	return admission.Allowed("ok")
}
