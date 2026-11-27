/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// adminscriptlog is for logging in this file.
var adminscriptlog = logf.Log.WithName("adminscript-resource")

// +kubebuilder:webhook:verbs=create;update,path=/validate-seaweed-seaweedfs-com-v1-adminscript,mutating=false,failurePolicy=fail,sideEffects=None,groups=seaweed.seaweedfs.com,resources=adminscripts,versions=v1,name=vadminscript.kb.io,admissionReviewVersions=v1

// AdminScriptCustomValidator enforces the workload-identity rules at admission
// time: spec.image may only change the tag or digest of the referenced
// cluster's image, and spec.serviceAccountName must name an existing
// ServiceAccount that carries the seaweedfs.com/allow-admin-script annotation.
//
// Admission is a second line of defence, not the only one: the controller
// applies the same checks at reconcile time (status condition + event + a
// suspended CronJob) so a refused script is still visible where webhooks are
// disabled — the default for `make deploy`. Admission is worth having because
// it rejects the write outright and can see cluster state, and because a
// rejected update is the clearest possible signal to whoever wrote the CR.
//
// +kubebuilder:object:generate=false
type AdminScriptCustomValidator struct {
	// Reader reads the referenced Seaweed and the named ServiceAccount. It is
	// the manager's uncached reader: the operator then needs only get on
	// ServiceAccounts instead of the list/watch a cache-backed informer would
	// require, and the answer reflects the live object rather than a stale
	// cache — the same trade the controller makes when it rechecks a named
	// identity every 30 seconds.
	Reader client.Reader
}

var _ admission.Validator[*AdminScript] = &AdminScriptCustomValidator{}

// SetupWebhookWithManager registers the AdminScript validating webhook.
func (r *AdminScript) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &AdminScript{}).
		WithValidator(&AdminScriptCustomValidator{Reader: mgr.GetAPIReader()}).
		Complete()
}

// ValidateCreate implements admission.Validator.
func (v *AdminScriptCustomValidator) ValidateCreate(ctx context.Context, obj *AdminScript) (admission.Warnings, error) {
	adminscriptlog.Info("validate create", "name", obj.Name)
	return nil, v.validate(ctx, obj)
}

// ValidateUpdate implements admission.Validator. An update that keeps the
// image, ServiceAccount and cluster is not rechecked: the controller suspends a
// script whose identity was revoked, and rechecking here would also block the
// finalizer removal that lets such a script be deleted.
func (v *AdminScriptCustomValidator) ValidateUpdate(ctx context.Context, oldObj, obj *AdminScript) (admission.Warnings, error) {
	adminscriptlog.Info("validate update", "name", obj.Name)
	if ptr.Deref(oldObj.Spec.Image, "") == ptr.Deref(obj.Spec.Image, "") &&
		oldObj.Spec.ServiceAccountName == obj.Spec.ServiceAccountName &&
		oldObj.Spec.ClusterRef.Name == obj.Spec.ClusterRef.Name {
		return nil, nil
	}
	return nil, v.validate(ctx, obj)
}

// ValidateDelete implements admission.Validator. Deletion is always allowed:
// removing a script cannot widen what already runs.
func (v *AdminScriptCustomValidator) ValidateDelete(_ context.Context, _ *AdminScript) (admission.Warnings, error) {
	return nil, nil
}

// validate returns every workload-identity violation in one aggregated error,
// so a user fixes all of them in one pass instead of one per apply.
//
// An AdminScript may legitimately be created before its cluster (the controller
// keeps it Pending until the cluster appears), so a missing Seaweed only skips
// the image check; the same rule is applied at reconcile time once the cluster
// exists.
func (v *AdminScriptCustomValidator) validate(ctx context.Context, script *AdminScript) error {
	var errs []error

	if image := script.Spec.Image; image != nil && *image != "" {
		var cluster Seaweed
		key := client.ObjectKey{Namespace: script.Namespace, Name: script.Spec.ClusterRef.Name}
		switch err := v.Reader.Get(ctx, key, &cluster); {
		case apierrors.IsNotFound(err):
			// Cluster not created yet; nothing to compare against.
		case err != nil:
			errs = append(errs, fmt.Errorf("looking up Seaweed %q: %w", key, err))
		default:
			if want := ImageRepository(cluster.ClusterImage()); ImageRepository(*image) != want {
				errs = append(errs, fmt.Errorf("spec.image %q must use the referenced cluster's image repository %q; only the tag or digest may differ", *image, want))
			}
		}
	}

	if name := script.Spec.ServiceAccountName; name != "" {
		if problems := validation.IsDNS1123Subdomain(name); len(problems) > 0 {
			errs = append(errs, fmt.Errorf("spec.serviceAccountName %q is not a valid Kubernetes ServiceAccount name: %s", name, problems[0]))
		} else {
			sa := &corev1.ServiceAccount{}
			switch err := v.Reader.Get(ctx, client.ObjectKey{Namespace: script.Namespace, Name: name}, sa); {
			case apierrors.IsNotFound(err):
				errs = append(errs, fmt.Errorf("spec.serviceAccountName %q does not exist in namespace %q", name, script.Namespace))
			case err != nil:
				errs = append(errs, fmt.Errorf("looking up spec.serviceAccountName %q: %w", name, err))
			default:
				if err := ServiceAccountAllowed(sa); err != nil {
					errs = append(errs, fmt.Errorf("spec.serviceAccountName: %w", err))
				}
			}
		}
	}

	return utilerrors.NewAggregate(errs)
}
