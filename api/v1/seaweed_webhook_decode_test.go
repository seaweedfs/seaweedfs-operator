package v1

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// TestWebhooksRejectSchemalessFieldsTheOperatorCannotDecode: volumes, sidecars and initContainers carry no
// CRD schema, so the API server stores any value for them. One the typed client cannot decode would stop the
// operator from listing Seaweeds at all; both webhooks decode the typed object and must refuse it first.
func TestWebhooksRejectSchemalessFieldsTheOperatorCannotDecode(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	webhooks := map[string]*admission.Webhook{
		"defaulter": admission.WithDefaulter[*Seaweed](scheme, &SeaweedCustomDefaulter{}),
		"validator": admission.WithValidator[*Seaweed](scheme, &SeaweedCustomValidator{}),
	}

	valid := baseValid()
	valid.APIVersion = GroupVersion.String()
	valid.Kind = "Seaweed"
	valid.Spec.Master.Volumes = []corev1.Volume{{Name: "extra", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	validRaw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}

	request := func(op admissionv1.Operation, raw []byte) admission.Request {
		req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
			UID:       "uid",
			Operation: op,
			Kind:      metav1.GroupVersionKind{Group: GroupVersion.Group, Version: GroupVersion.Version, Kind: "Seaweed"},
			Object:    runtime.RawExtension{Raw: raw},
		}}
		if op == admissionv1.Update {
			req.OldObject = runtime.RawExtension{Raw: validRaw}
		}
		return req
	}
	for name, wh := range webhooks {
		for _, op := range []admissionv1.Operation{admissionv1.Create, admissionv1.Update} {
			if resp := wh.Handle(context.Background(), request(op, validRaw)); !resp.Allowed {
				t.Fatalf("%s %s refused a valid Seaweed: %v", name, op, resp.Result)
			}
		}
	}

	for _, field := range []string{"volumes", "sidecars", "initContainers"} {
		for _, bad := range []any{"oops", []any{"oops"}, []any{map[string]any{"name": 5}}} {
			var obj map[string]any
			if err := json.Unmarshal(validRaw, &obj); err != nil {
				t.Fatal(err)
			}
			obj["spec"].(map[string]any)["master"].(map[string]any)[field] = bad
			raw, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			for name, wh := range webhooks {
				for _, op := range []admissionv1.Operation{admissionv1.Create, admissionv1.Update} {
					if resp := wh.Handle(context.Background(), request(op, raw)); resp.Allowed {
						t.Errorf("%s %s allowed master.%s = %v", name, op, field, bad)
					}
				}
			}
		}
	}
}
