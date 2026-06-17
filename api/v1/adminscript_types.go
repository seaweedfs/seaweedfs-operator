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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AdminScriptClusterRef identifies the Seaweed cluster whose masters and
// filer the scheduled `weed shell` script runs against. The reference is
// always resolved in the AdminScript's own namespace — cross-namespace
// references are intentionally not supported, so the generated CronJob lives
// next to the cluster it administers.
type AdminScriptClusterRef struct {
	// Name of the Seaweed CR in the same namespace as this AdminScript.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AdminScriptSpec defines a `weed shell` script to run on a cron schedule.
//
// The controller renders a native batch/v1 CronJob whose pod runs
// `weed shell -master=<cluster masters> -filer=<cluster filer>` with Script
// piped to stdin. The pod is built like the cluster's admin/worker pods: it
// reuses the cluster image and, when the cluster has mTLS enabled, mounts the
// same security.toml/TLS material so the shell can authenticate to the
// masters over gRPC.
type AdminScriptSpec struct {
	// ClusterRef points at the Seaweed CR (same namespace) whose masters and
	// filer the script administers.
	// +kubebuilder:validation:Required
	ClusterRef AdminScriptClusterRef `json:"clusterRef"`

	// Schedule is the cron schedule in standard Cron format, e.g.
	// "0 2 * * *" for daily at 02:00. Validated by the Kubernetes CronJob
	// controller; an invalid expression surfaces on the AdminScript's Ready
	// condition.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Schedule string `json:"schedule"`

	// Script is the set of `weed shell` commands to run, one command per
	// line (commands may also be separated by ';'). The full text is piped
	// to `weed shell` over stdin, e.g.:
	//
	//   lock
	//   volume.balance -force
	//   volume.fix.replication
	//   unlock
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Script string `json:"script"`

	// TimeZone is the IANA time zone name (e.g. "Etc/UTC", "America/New_York")
	// the Schedule is interpreted in. Omit to use the kube-controller-manager
	// default (typically UTC). Requires Kubernetes 1.27+ for the CronJob
	// time-zone feature to be stable.
	// +optional
	TimeZone *string `json:"timeZone,omitempty"`

	// Suspend pauses scheduling of new runs without deleting the resource.
	// In-flight runs are not affected. Defaults to false.
	// +optional
	Suspend *bool `json:"suspend,omitempty"`

	// ConcurrencyPolicy controls what happens to a scheduled run when the
	// previous run is still going. Defaults to Forbid — administrative
	// scripts such as volume.balance should not overlap.
	// +optional
	// +kubebuilder:default:=Forbid
	// +kubebuilder:validation:Enum=Allow;Forbid;Replace
	ConcurrencyPolicy batchv1.ConcurrencyPolicy `json:"concurrencyPolicy,omitempty"`

	// StartingDeadlineSeconds is the deadline in seconds for starting a run
	// if it misses its scheduled time for any reason. Missed runs past the
	// deadline are counted as failed.
	// +optional
	// +kubebuilder:validation:Minimum=0
	StartingDeadlineSeconds *int64 `json:"startingDeadlineSeconds,omitempty"`

	// SuccessfulJobsHistoryLimit is the number of successful finished Jobs to
	// retain. Defaults to 3.
	// +optional
	// +kubebuilder:default:=3
	// +kubebuilder:validation:Minimum=0
	SuccessfulJobsHistoryLimit *int32 `json:"successfulJobsHistoryLimit,omitempty"`

	// FailedJobsHistoryLimit is the number of failed finished Jobs to retain.
	// Defaults to 1.
	// +optional
	// +kubebuilder:default:=1
	// +kubebuilder:validation:Minimum=0
	FailedJobsHistoryLimit *int32 `json:"failedJobsHistoryLimit,omitempty"`

	// BackoffLimit is the number of retries before a single run is marked
	// failed. Defaults to 0 — a failed administrative script is surfaced
	// immediately rather than retried.
	// +optional
	// +kubebuilder:default:=0
	// +kubebuilder:validation:Minimum=0
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	// ActiveDeadlineSeconds caps the wall-clock duration of a single run.
	// The run is terminated and marked failed once exceeded. Omit for no cap.
	// +optional
	// +kubebuilder:validation:Minimum=1
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// RestartPolicy for the run's pod. Only Never and OnFailure are valid
	// for Jobs. Defaults to Never.
	// +optional
	// +kubebuilder:default:=Never
	// +kubebuilder:validation:Enum=Never;OnFailure
	RestartPolicy corev1.RestartPolicy `json:"restartPolicy,omitempty"`

	// CredentialsSecret optionally projects every key of the referenced
	// Secret (same namespace) into the run's pod as environment variables,
	// for scripts that need credentials. When unset, the controller falls
	// back to the referenced cluster's admin CredentialsSecret if one is
	// configured. The reference is optional at pod start, so a missing Secret
	// does not block the run.
	// +optional
	CredentialsSecret *corev1.LocalObjectReference `json:"credentialsSecret,omitempty"`

	// Image overrides the container image for the run. Defaults to the
	// referenced cluster's image, so the shell version always matches the
	// cluster it administers.
	// +optional
	Image *string `json:"image,omitempty"`

	// ImagePullSecrets to use for pulling the image. Defaults to the
	// referenced cluster's pull secrets when unset.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Resources are the compute resources for the run's container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// ServiceAccountName runs the pod under a specific ServiceAccount. The
	// operator does not create it. Defaults to the namespace default SA.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// NodeSelector constrains the run's pod to nodes with matching labels.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Tolerations applied to the run's pod.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// Affinity applied to the run's pod.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
}

// AdminScriptPhase summarises the AdminScript's lifecycle.
// +kubebuilder:validation:Enum=Pending;Active;Suspended
type AdminScriptPhase string

const (
	// AdminScriptPhasePending means the referenced cluster does not exist yet,
	// so no CronJob has been created.
	AdminScriptPhasePending AdminScriptPhase = "Pending"
	// AdminScriptPhaseActive means the CronJob is created and scheduling runs.
	AdminScriptPhaseActive AdminScriptPhase = "Active"
	// AdminScriptPhaseSuspended means the CronJob exists but scheduling is
	// suspended via spec.suspend.
	AdminScriptPhaseSuspended AdminScriptPhase = "Suspended"
)

// Condition types emitted by the AdminScript controller.
const (
	// AdminScriptConditionReady summarises whether the CronJob is reconciled
	// and scheduling as configured.
	AdminScriptConditionReady = "Ready"
)

// AdminScriptStatus reflects the observed state of the AdminScript.
type AdminScriptStatus struct {
	// ObservedGeneration is the .metadata.generation the controller last
	// reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase is a coarse summary of the AdminScript's lifecycle.
	// +optional
	Phase AdminScriptPhase `json:"phase,omitempty"`

	// Conditions are the structured per-aspect state signals.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// CronJobName is the name of the managed CronJob (equals the AdminScript
	// name, in the same namespace).
	// +optional
	CronJobName string `json:"cronJobName,omitempty"`

	// Active is the number of currently running Jobs spawned by the CronJob.
	// +optional
	Active int32 `json:"active,omitempty"`

	// LastScheduleTime is the last time the CronJob was scheduled.
	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`

	// LastSuccessfulTime is the last time a run completed successfully.
	// +optional
	LastSuccessfulTime *metav1.Time `json:"lastSuccessfulTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=swas,categories=seaweedfs
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterRef.name`
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.spec.schedule`
// +kubebuilder:printcolumn:name="Suspend",type=boolean,JSONPath=`.spec.suspend`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="LastSchedule",type=date,JSONPath=`.status.lastScheduleTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AdminScript schedules a `weed shell` administrative script against a
// Seaweed cluster by reconciling into a native Kubernetes CronJob.
type AdminScript struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AdminScriptSpec   `json:"spec,omitempty"`
	Status AdminScriptStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AdminScriptList contains a list of AdminScript.
type AdminScriptList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AdminScript `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AdminScript{}, &AdminScriptList{})
}
