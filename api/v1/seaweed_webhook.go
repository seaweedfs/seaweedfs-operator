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
	"errors"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var seaweedlog = logf.Log.WithName("seaweed-resource")

func (r *Seaweed) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-seaweed-seaweedfs-com-v1-seaweed,mutating=true,failurePolicy=fail,sideEffects=None,groups=seaweed.seaweedfs.com,resources=seaweeds,verbs=create;update,versions=v1,name=mseaweed.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &Seaweed{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *Seaweed) Default() {
	seaweedlog.Info("default", "name", r.Name)

	// TODO(user): fill in your defaulting logic.
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// +kubebuilder:webhook:verbs=create;update,path=/validate-seaweed-seaweedfs-com-v1-seaweed,mutating=false,failurePolicy=fail,sideEffects=None,groups=seaweed.seaweedfs.com,resources=seaweeds,versions=v1,name=vseaweed.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &Seaweed{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Seaweed) ValidateCreate() (admission.Warnings, error) {
	seaweedlog.Info("validate create", "name", r.Name)
	errs := []error{}

	if r.Spec.Master == nil {
		errs = append(errs, errors.New("missing master spec"))
	}

	if r.Spec.Volume == nil {
		errs = append(errs, errors.New("missing volume spec"))
	} else {
		if r.Spec.Volume.Requests[corev1.ResourceStorage].Equal(resource.MustParse("0")) {
			errs = append(errs, errors.New("volume storage request cannot be zero"))
		}
	}

	if r.Spec.Worker != nil && r.Spec.Admin == nil {
		errs = append(errs, errors.New("spec.worker requires spec.admin to be configured"))
	}

	if err := r.validateS3Exclusivity(); err != nil {
		errs = append(errs, err)
	}
	if err := r.validateSFTP(); err != nil {
		errs = append(errs, err)
	}

	return r.s3DeprecationWarnings(), utilerrors.NewAggregate(errs)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Seaweed) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	seaweedlog.Info("validate update", "name", r.Name)
	errs := []error{}

	if r.Spec.Worker != nil && r.Spec.Admin == nil {
		errs = append(errs, errors.New("spec.worker requires spec.admin to be configured"))
	}
	if err := r.validateS3Exclusivity(); err != nil {
		errs = append(errs, err)
	}
	if err := r.validateSFTP(); err != nil {
		errs = append(errs, err)
	}

	return r.s3DeprecationWarnings(), utilerrors.NewAggregate(errs)
}

// validateS3Exclusivity forbids setting both the standalone S3 gateway
// (SeaweedSpec.S3) and the embedded filer S3 (FilerSpec.S3) on the same
// CR. The two paths cannot safely share port 8333 between filer and a
// standalone gateway on the same name, and supporting both in one CR
// leads to ambiguous semantics for clients.
func (r *Seaweed) validateS3Exclusivity() error {
	standalone := r.Spec.S3 != nil
	embedded := r.Spec.Filer != nil && r.Spec.Filer.S3 != nil && r.Spec.Filer.S3.Enabled
	if standalone && embedded {
		return errors.New("spec.s3 and spec.filer.s3.enabled cannot both be set; spec.filer.s3 is deprecated — migrate to the top-level spec.s3 standalone gateway")
	}
	return nil
}

// validateSFTP enforces that the standalone SFTP gateway has a filer in
// the same CR to connect to. Without a filer the gateway would start but
// fail every client request, so we reject at admission time.
func (r *Seaweed) validateSFTP() error {
	if r.Spec.SFTP != nil && r.Spec.Filer == nil {
		return errors.New("spec.sftp requires spec.filer to be configured")
	}
	return nil
}

// s3DeprecationWarnings surfaces admission.Warnings for the deprecated
// embedded S3 path so users see them in `kubectl apply` output and in
// `kubectl describe` without needing to dig through operator logs.
func (r *Seaweed) s3DeprecationWarnings() admission.Warnings {
	if r.Spec.Filer != nil && r.Spec.Filer.S3 != nil && r.Spec.Filer.S3.Enabled {
		return admission.Warnings{
			"spec.filer.s3 is deprecated and will be removed in a future release. Migrate to the top-level spec.s3 standalone gateway for independent scaling.",
		}
	}
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Seaweed) ValidateDelete() (admission.Warnings, error) {
	seaweedlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
