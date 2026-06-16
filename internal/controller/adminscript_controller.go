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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

// adminScriptRequeueAfter is the backoff used while the referenced Seaweed
// cluster does not yet exist; the CronJob cannot be rendered until it does.
const adminScriptRequeueAfter = 30 * time.Second

// AdminScriptReconciler reconciles an AdminScript into a native batch/v1
// CronJob whose pod runs `weed shell` against the referenced cluster's
// masters/filer with the spec'd script piped to stdin.
type AdminScriptReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=adminscripts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=adminscripts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=seaweed.seaweedfs.com,resources=adminscripts/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile renders the CronJob for an AdminScript and mirrors the CronJob's
// schedule status back onto the AdminScript.
func (r *AdminScriptReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("adminscript", req.NamespacedName)

	var script seaweedv1.AdminScript
	if err := r.Get(ctx, req.NamespacedName, &script); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Resolve the referenced cluster in the AdminScript's own namespace. The
	// owned CronJob is garbage-collected automatically when the AdminScript
	// is deleted, so no finalizer is needed.
	var cluster seaweedv1.Seaweed
	clusterKey := client.ObjectKey{Namespace: script.Namespace, Name: script.Spec.ClusterRef.Name}
	if err := r.Get(ctx, clusterKey, &cluster); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("referenced Seaweed cluster not found; waiting", "cluster", clusterKey)
			msg := fmt.Sprintf("Seaweed cluster %q not found in namespace %q", clusterKey.Name, clusterKey.Namespace)
			if err := r.patchStatus(ctx, &script, seaweedv1.AdminScriptPhasePending, metav1.ConditionFalse, "ClusterNotFound", msg); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: adminScriptRequeueAfter}, nil
		}
		return ctrl.Result{}, err
	}

	desired := r.buildCronJob(&script, &cluster)
	cronJob, err := r.createOrUpdateCronJob(ctx, &script, desired)
	if err != nil {
		r.Recorder.Eventf(&script, corev1.EventTypeWarning, "CronJobReconcileFailed", "failed to reconcile CronJob: %v", err)
		if statusErr := r.patchStatus(ctx, &script, seaweedv1.AdminScriptPhasePending, metav1.ConditionFalse, "CronJobReconcileFailed", err.Error()); statusErr != nil {
			log.Error(statusErr, "failed to update status after CronJob reconcile error")
		}
		return ctrl.Result{}, err
	}

	phase := seaweedv1.AdminScriptPhaseActive
	reason, message := "Scheduled", fmt.Sprintf("CronJob %q scheduling %q", cronJob.Name, script.Spec.Schedule)
	if script.Spec.Suspend != nil && *script.Spec.Suspend {
		phase = seaweedv1.AdminScriptPhaseSuspended
		reason, message = "Suspended", "scheduling is suspended via spec.suspend"
	}

	r.applyCronJobStatus(&script, cronJob)
	if err := r.patchStatus(ctx, &script, phase, metav1.ConditionTrue, reason, message); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// buildCronJob renders the desired CronJob for an AdminScript. The pod mirrors
// the cluster's admin/worker pods: same image, the cluster's logging args, and
// — when the cluster has mTLS enabled — the same security.toml/TLS mounts so
// `weed shell` can authenticate to the masters over gRPC.
func (r *AdminScriptReconciler) buildCronJob(script *seaweedv1.AdminScript, cluster *seaweedv1.Seaweed) *batchv1.CronJob {
	labels := labelsForAdminScript(script.Name)

	image := cluster.Spec.Image
	if script.Spec.Image != nil && *script.Spec.Image != "" {
		image = *script.Spec.Image
	}

	// `weed <logging> [-config_dir=…] shell -master=… [-filer=…]`, with the
	// script piped over stdin. weed shell reads commands line-by-line from a
	// non-interactive stdin (see weed/shell RunShell), so the script is held
	// in an env var and replayed by printf to avoid any shell re-parsing.
	weedCmd := weedPreamble(cluster, cluster.BaseAdminSpec().LoggingArgs(), "shell")
	weedCmd = append(weedCmd, "-master="+getMasterPeersString(cluster))
	if cluster.Spec.Filer != nil {
		weedCmd = append(weedCmd, "-filer="+getFilerAddress(cluster))
	}
	// The weed invocation is passed as positional parameters and run via
	// "$@", so the wrapping shell never re-splits an argument that contains
	// spaces or metacharacters (e.g. a logging flag). This matches how the
	// admin/worker startup scripts handle argv.
	shellScript := `printf '%s\n' "$` + adminScriptEnvVar + `" | "$@"`

	podSpec := corev1.PodSpec{
		RestartPolicy:    adminScriptRestartPolicy(script),
		NodeSelector:     script.Spec.NodeSelector,
		Affinity:         script.Spec.Affinity,
		Tolerations:      script.Spec.Tolerations,
		ImagePullSecrets: adminScriptImagePullSecrets(script, cluster),
	}
	if script.Spec.ServiceAccountName != "" {
		podSpec.ServiceAccountName = script.Spec.ServiceAccountName
	}

	var volumeMounts []corev1.VolumeMount
	if tlsVols, tlsMounts := tlsVolumesAndMounts(cluster); len(tlsVols) > 0 {
		podSpec.Volumes = append(podSpec.Volumes, tlsVols...)
		volumeMounts = append(volumeMounts, tlsMounts...)
	}

	env := append([]corev1.EnvVar{{Name: adminScriptEnvVar, Value: script.Spec.Script}}, kubernetesEnvVars...)

	var envFrom []corev1.EnvFromSource
	if credName := adminScriptCredentialsSecretName(script, cluster); credName != "" {
		envFrom = append(envFrom, corev1.EnvFromSource{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: credName},
				Optional:             ptrBool(true),
			},
		})
	}

	podSpec.Containers = []corev1.Container{{
		Name:            "weed-shell",
		Image:           image,
		ImagePullPolicy: cluster.BaseAdminSpec().ImagePullPolicy(),
		Command:         append([]string{"/bin/sh", "-ec", shellScript, "--"}, weedCmd...),
		Env:             env,
		EnvFrom:         envFrom,
		Resources:       filterContainerResources(script.Spec.Resources),
		VolumeMounts:    volumeMounts,
	}}

	jobSpec := batchv1.JobSpec{
		BackoffLimit:          script.Spec.BackoffLimit,
		ActiveDeadlineSeconds: script.Spec.ActiveDeadlineSeconds,
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec:       podSpec,
		},
	}

	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      script.Name,
			Namespace: script.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   script.Spec.Schedule,
			TimeZone:                   script.Spec.TimeZone,
			Suspend:                    script.Spec.Suspend,
			ConcurrencyPolicy:          adminScriptConcurrencyPolicy(script),
			StartingDeadlineSeconds:    script.Spec.StartingDeadlineSeconds,
			SuccessfulJobsHistoryLimit: script.Spec.SuccessfulJobsHistoryLimit,
			FailedJobsHistoryLimit:     script.Spec.FailedJobsHistoryLimit,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       jobSpec,
			},
		},
	}
}

// createOrUpdateCronJob upserts the desired CronJob owned by the AdminScript.
// The desired spec is stamped into a last-applied annotation so server-side
// defaulting of the embedded Job/Pod template does not cause an update on
// every reconcile — only a genuine change to the rendered spec or labels does.
func (r *AdminScriptReconciler) createOrUpdateCronJob(ctx context.Context, owner *seaweedv1.AdminScript, desired *batchv1.CronJob) (*batchv1.CronJob, error) {
	specJSON, err := json.Marshal(desired.Spec)
	if err != nil {
		return nil, err
	}
	if desired.Annotations == nil {
		desired.Annotations = map[string]string{}
	}
	desired.Annotations[LastAppliedConfigAnnotation] = string(specJSON)

	existing := &batchv1.CronJob{}
	getErr := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(getErr) {
		if err := ctrl.SetControllerReference(owner, desired, r.Scheme); err != nil {
			return nil, err
		}
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		r.Recorder.Eventf(owner, corev1.EventTypeNormal, "CronJobCreated", "created CronJob %q", desired.Name)
		return desired, nil
	}
	if getErr != nil {
		return nil, getErr
	}

	if existing.Annotations[LastAppliedConfigAnnotation] == string(specJSON) &&
		apiequality.Semantic.DeepEqual(existing.Labels, desired.Labels) {
		return existing, nil
	}

	existing.Labels = desired.Labels
	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	existing.Annotations[LastAppliedConfigAnnotation] = string(specJSON)
	existing.Spec = desired.Spec
	if err := ctrl.SetControllerReference(owner, existing, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Update(ctx, existing); err != nil {
		return nil, err
	}
	r.Recorder.Eventf(owner, corev1.EventTypeNormal, "CronJobUpdated", "updated CronJob %q", existing.Name)
	return existing, nil
}

// applyCronJobStatus mirrors the CronJob's schedule status onto the AdminScript.
func (r *AdminScriptReconciler) applyCronJobStatus(script *seaweedv1.AdminScript, cronJob *batchv1.CronJob) {
	script.Status.CronJobName = cronJob.Name
	script.Status.Active = int32(len(cronJob.Status.Active))
	script.Status.LastScheduleTime = cronJob.Status.LastScheduleTime
	script.Status.LastSuccessfulTime = cronJob.Status.LastSuccessfulTime
}

// patchStatus sets the phase, Ready condition and observedGeneration, then
// writes the status subresource.
func (r *AdminScriptReconciler) patchStatus(ctx context.Context, script *seaweedv1.AdminScript, phase seaweedv1.AdminScriptPhase, status metav1.ConditionStatus, reason, message string) error {
	script.Status.Phase = phase
	script.Status.ObservedGeneration = script.Generation
	meta.SetStatusCondition(&script.Status.Conditions, metav1.Condition{
		Type:               seaweedv1.AdminScriptConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: script.Generation,
	})
	return r.Status().Update(ctx, script)
}

func labelsForAdminScript(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "admin-script",
		label.InstanceLabelKey:  name,
	}
}

// adminScriptEnvVar holds the script body in the pod so the wrapping /bin/sh
// never re-parses it.
const adminScriptEnvVar = "WEED_SHELL_SCRIPT"

func adminScriptConcurrencyPolicy(script *seaweedv1.AdminScript) batchv1.ConcurrencyPolicy {
	if script.Spec.ConcurrencyPolicy != "" {
		return script.Spec.ConcurrencyPolicy
	}
	return batchv1.ForbidConcurrent
}

func adminScriptRestartPolicy(script *seaweedv1.AdminScript) corev1.RestartPolicy {
	if script.Spec.RestartPolicy != "" {
		return script.Spec.RestartPolicy
	}
	return corev1.RestartPolicyNever
}

// adminScriptImagePullSecrets prefers the AdminScript's own pull secrets and
// falls back to the cluster's so a private-registry image pulls the same way
// the cluster pods do.
func adminScriptImagePullSecrets(script *seaweedv1.AdminScript, cluster *seaweedv1.Seaweed) []corev1.LocalObjectReference {
	if len(script.Spec.ImagePullSecrets) > 0 {
		return script.Spec.ImagePullSecrets
	}
	return cluster.BaseAdminSpec().ImagePullSecrets()
}

// adminScriptCredentialsSecretName resolves the Secret whose keys are exported
// into the run's pod as env vars: the AdminScript's own reference wins, else
// the referenced cluster's admin CredentialsSecret.
func adminScriptCredentialsSecretName(script *seaweedv1.AdminScript, cluster *seaweedv1.Seaweed) string {
	if script.Spec.CredentialsSecret != nil && script.Spec.CredentialsSecret.Name != "" {
		return script.Spec.CredentialsSecret.Name
	}
	if cluster.Spec.Admin != nil && cluster.Spec.Admin.CredentialsSecret != nil {
		return cluster.Spec.Admin.CredentialsSecret.Name
	}
	return ""
}

func ptrBool(b bool) *bool { return &b }

// SetupWithManager wires the reconciler into the manager. AdminScripts are
// re-reconciled when their referenced Seaweed cluster changes, since the
// rendered CronJob (image, filer flag, mTLS mounts, credentials) is derived
// from that cluster.
func (r *AdminScriptReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&seaweedv1.AdminScript{}).
		Owns(&batchv1.CronJob{}).
		Watches(&seaweedv1.Seaweed{}, handler.EnqueueRequestsFromMapFunc(r.mapSeaweedToAdminScripts)).
		Complete(r)
}

// mapSeaweedToAdminScripts enqueues every AdminScript in the changed cluster's
// namespace that references it, so a cluster mutation (image upgrade, mTLS
// toggle, filer add/remove) re-renders the affected CronJobs.
func (r *AdminScriptReconciler) mapSeaweedToAdminScripts(ctx context.Context, obj client.Object) []reconcile.Request {
	cluster, ok := obj.(*seaweedv1.Seaweed)
	if !ok {
		return nil
	}
	var list seaweedv1.AdminScriptList
	if err := r.List(ctx, &list, client.InNamespace(cluster.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if list.Items[i].Spec.ClusterRef.Name == cluster.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
		}
	}
	return reqs
}
