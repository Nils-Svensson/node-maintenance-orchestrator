package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/Nils-Svensson/node-maintenance-orchestrator/api/v1alpha1"
)

func newValidator(objects ...client.Object) *NodeMaintenancePlanValidator {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	fc := fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).Build()
	return &NodeMaintenancePlanValidator{Client: fc}
}

func validatorRequest(t *testing.T, op admissionv1.Operation, plan v1alpha1.NodeMaintenancePlan) admission.Request {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: op,
			Name:      plan.Name,
			Object:    runtime.RawExtension{Raw: raw},
		},
	}
}

func nodeWithAnnotation(name, annotation string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{v1alpha1.ManagedByAnnotation: annotation},
		},
	}
}

func labeledNode(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func plan(name string, nodes []string, selector *metav1.LabelSelector) v1alpha1.NodeMaintenancePlan {
	return v1alpha1.NodeMaintenancePlan{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.NodeMaintenancePlanSpec{Nodes: nodes, NodeSelector: selector},
	}
}

func TestHandle(t *testing.T) {
	cases := []struct {
		name        string
		op          admissionv1.Operation
		plan        v1alpha1.NodeMaintenancePlan
		clusterObjs []client.Object
		wantAllowed bool
		wantCode    int32
		wantMsg     string
	}{
		{
			name:        "DELETE is always allowed",
			op:          admissionv1.Delete,
			plan:        plan("p", nil, nil),
			wantAllowed: true,
		},
		{
			name:        "CREATE with no nodes or selector is allowed",
			op:          admissionv1.Create,
			plan:        plan("p", nil, nil),
			wantAllowed: true,
		},
		{
			name: "CREATE with existing unowned nodes is allowed",
			op:   admissionv1.Create,
			plan: plan("p", []string{"node-a", "node-b"}, nil),
			clusterObjs: []client.Object{
				&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}},
				&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-b"}},
			},
			wantAllowed: true,
		},
		{
			name:        "CREATE denied when node does not exist",
			op:          admissionv1.Create,
			plan:        plan("p", []string{"missing"}, nil),
			wantAllowed: false,
			wantMsg:     `node "missing" does not exist`,
		},
		{
			name:        "CREATE denied when node owned by a different plan",
			op:          admissionv1.Create,
			plan:        plan("p", []string{"node-a"}, nil),
			clusterObjs: []client.Object{nodeWithAnnotation("node-a", "other-plan")},
			wantAllowed: false,
			wantMsg:     `node "node-a" is already owned by plan "other-plan"`,
		},
		{
			name: "CREATE allowed when node is owned by this plan",
			op:   admissionv1.Create,
			plan: plan("p", []string{"node-a"}, nil),
			// annotation matches the plan name "p"
			clusterObjs: []client.Object{nodeWithAnnotation("node-a", "p")},
			wantAllowed: true,
		},
		{
			name:        "CREATE with nodeSelector matching at least one node is allowed",
			op:          admissionv1.Create,
			plan:        plan("p", nil, &metav1.LabelSelector{MatchLabels: map[string]string{"role": "worker"}}),
			clusterObjs: []client.Object{labeledNode("node-a", map[string]string{"role": "worker"})},
			wantAllowed: true,
		},
		{
			name:        "CREATE with nodeSelector matching no nodes is denied",
			op:          admissionv1.Create,
			plan:        plan("p", nil, &metav1.LabelSelector{MatchLabels: map[string]string{"role": "worker"}}),
			wantAllowed: false,
			wantMsg:     "nodeSelector matches no existing nodes",
		},
		{
			name: "CREATE with invalid nodeSelector returns bad request",
			op:   admissionv1.Create,
			plan: plan("p", nil, &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "k", Operator: "NotAnOperator", Values: []string{}},
				},
			}),
			wantAllowed: false,
			wantCode:    http.StatusBadRequest,
		},
		{
			name: "UPDATE with nodeSelector matching no nodes is still allowed",
			op:   admissionv1.Update,
			plan: plan("p", nil, &metav1.LabelSelector{MatchLabels: map[string]string{"role": "worker"}}),
			// no nodes in cluster — but UPDATE skips the nodeSelector check
			wantAllowed: true,
		},
		{
			name:        "UPDATE denied when explicit node does not exist",
			op:          admissionv1.Update,
			plan:        plan("p", []string{"missing"}, nil),
			wantAllowed: false,
			wantMsg:     `node "missing" does not exist`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newValidator(tc.clusterObjs...)

			var resp admission.Response
			if tc.op == admissionv1.Delete {
				resp = v.Handle(context.Background(), admission.Request{
					AdmissionRequest: admissionv1.AdmissionRequest{Operation: admissionv1.Delete},
				})
			} else {
				resp = v.Handle(context.Background(), validatorRequest(t, tc.op, tc.plan))
			}

			if resp.Allowed != tc.wantAllowed {
				t.Errorf("Allowed=%v, want %v; result=%+v", resp.Allowed, tc.wantAllowed, resp.Result)
			}
			if tc.wantMsg != "" && resp.Result != nil {
				if resp.Result.Message != tc.wantMsg {
					t.Errorf("message=%q, want %q", resp.Result.Message, tc.wantMsg)
				}
			}
			if tc.wantCode != 0 && resp.Result != nil {
				if resp.Result.Code != tc.wantCode {
					t.Errorf("code=%d, want %d", resp.Result.Code, tc.wantCode)
				}
			}
		})
	}
}
