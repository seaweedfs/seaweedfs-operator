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

// +kubebuilder:webhook:path=/mutate-seaweed-seaweedfs-com-v1-seaweed,mutating=true,failurePolicy=fail,groups=seaweed.seaweedfs.com,resources=seaweeds,verbs=create;update,versions=v1,name=mseaweed.kb.io

var _ webhook.Defaulter = &Seaweed{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *Seaweed) Default() {
	seaweedlog.Info("default", "name", r.Name)

	// TODO(user): fill in your defaulting logic.
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// +kubebuilder:webhook:verbs=create;update,path=/validate-seaweed-seaweedfs-com-v1-seaweed,mutating=false,failurePolicy=fail,groups=seaweed.seaweedfs.com,resources=seaweeds,versions=v1,name=vseaweed.kb.io

var _ webhook.Validator = &Seaweed{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Seaweed) ValidateCreate() error {
	seaweedlog.Info("validate create", "name", r.Name)
	errs := []error{}

	// TODO(user): fill in your validation logic upon object creation.
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

	return utilerrors.NewAggregate(errs)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Seaweed) ValidateUpdate(old runtime.Object) error {
	seaweedlog.Info("validate update", "name", r.Name)

	// TODO(user): fill in your validation logic upon object update.
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Seaweed) ValidateDelete() error {
	seaweedlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil
}
