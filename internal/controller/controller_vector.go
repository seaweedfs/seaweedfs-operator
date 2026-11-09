package controller

import (
	"context"
	"fmt"

	monitorv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func labelsForVector(name string) map[string]string {
	return map[string]string{label.ManagedByLabelKey: "seaweedfs-operator", label.NameLabelKey: "seaweedfs", label.ComponentLabelKey: ComponentVector, label.InstanceLabelKey: name}
}

func vectorEffectivePort(m *seaweedv1.Seaweed) int32 {
	if m.Spec.Vector.Port != nil {
		return *m.Spec.Vector.Port
	}
	return 8090
}

func (r *SeaweedReconciler) ensureVectors(ctx context.Context, m *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	name := m.Name + "-vector"
	if m.Spec.Vector != nil && m.Spec.Filer == nil {
		r.Log.Info("Vector engines require a Filer", "seaweed", m.Name)
	}
	if m.Spec.Vector == nil || m.Spec.Filer == nil {
		if err := r.pruneOwned(ctx, m, &appsv1.Deployment{}, name); err != nil {
			return ReconcileResult(err)
		}
		if err := r.pruneOwned(ctx, m, &corev1.Service{}, name); err != nil {
			return ReconcileResult(err)
		}
		return ReconcileResult(r.deleteOwnedServiceMonitor(ctx, m, name))
	}
	deployment := r.createVectorDeployment(m)
	if err := controllerutil.SetControllerReference(m, deployment, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	if _, err := r.CreateOrUpdateDeployment(deployment); err != nil {
		return ReconcileResult(err)
	}
	service := r.createVectorService(m)
	if err := controllerutil.SetControllerReference(m, service, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	if _, err := r.CreateOrUpdateService(service); err != nil {
		return ReconcileResult(err)
	}
	return r.reconcileServiceMonitor(ctx, m, m.Spec.Vector.MetricsPort, r.createVectorServiceMonitor(m))
}

func (r *SeaweedReconciler) createVectorDeployment(m *seaweedv1.Seaweed) *appsv1.Deployment {
	v, base := m.Spec.Vector, m.BaseVectorSpec()
	labels := labelsForVector(m.Name)
	pod := base.BuildPodSpec()
	if pod.NodeSelector == nil {
		pod.NodeSelector = map[string]string{}
	}
	pod.NodeSelector["kubernetes.io/os"] = "linux"
	if pod.Affinity == nil {
		pod.Affinity = &corev1.Affinity{}
	} else {
		pod.Affinity = pod.Affinity.DeepCopy()
	}
	if pod.Affinity.NodeAffinity == nil {
		pod.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	affinity := pod.Affinity.NodeAffinity
	requirement := corev1.NodeSelectorRequirement{Key: "kubernetes.io/arch", Operator: corev1.NodeSelectorOpIn, Values: []string{"amd64", "arm64"}}
	if affinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		affinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{requirement}}}}
	} else {
		for i := range affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			term := &affinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i]
			if len(term.MatchExpressions) > 0 || len(term.MatchFields) > 0 {
				term.MatchExpressions = append(term.MatchExpressions, requirement)
			}
		}
	}
	vols, mounts := tlsVolumesAndMounts(m)
	pod.Volumes = append(pod.Volumes, vols...)
	args := []string{"--masters=" + getMasterPeersString(m)}
	if v.ReadCacheMB != nil {
		args = append(args, fmt.Sprintf("--read-cache-mb=%d", *v.ReadCacheMB))
	}
	container := corev1.Container{
		Name: ComponentVector, Image: base.Image(), ImagePullPolicy: base.ImagePullPolicy(), SecurityContext: base.ContainerSecurityContext(),
		Command: []string{"/bin/sh", "-ec", fmt.Sprintf(`case "$POD_IP" in *:*) POD_IP="[$POD_IP]";; esac; exec /usr/bin/weed-vector --listen="$POD_IP:%d" "$@"`, vectorEffectivePort(m)), "vector"}, Resources: filterContainerResources(v.ResourceRequirements),
		Env: append(base.Env(), componentEnvVars(m)...), VolumeMounts: mergeVolumeMounts(mounts, base.VolumeMounts()),
		Ports: []corev1.ContainerPort{{Name: "vector-grpc", ContainerPort: vectorEffectivePort(m)}},
	}
	container.Env = append(container.Env, corev1.EnvVar{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"}}})
	if securityConfigNeeded(m) {
		container.Env = append(container.Env, corev1.EnvVar{Name: "WEED_VECTOR_SECURITY_TOML", Value: securityConfigMountPath + "/security.toml"})
	}
	if v.MetricsPort != nil {
		args = append(args, fmt.Sprintf("--metrics-port=%d", *v.MetricsPort), "--metrics-ip=$(POD_IP)")
		container.Ports = append(container.Ports, corev1.ContainerPort{Name: "vector-metrics", ContainerPort: *v.MetricsPort})
		container.ReadinessProbe, container.LivenessProbe = workerProbes(*v.MetricsPort)
	} else {
		probe := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("vector-grpc")}}, PeriodSeconds: 10, TimeoutSeconds: 3, FailureThreshold: 6}
		container.ReadinessProbe, container.LivenessProbe = probe, probe.DeepCopy()
	}
	container.StartupProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString("vector-grpc")}}, PeriodSeconds: 5, FailureThreshold: 60}
	applyProbeOverrides(&container, base.ReadinessProbe(), base.LivenessProbe())
	container.Args = append(args, base.ExtraArgs()...)
	pod.Containers = append([]corev1.Container{container}, base.Sidecars()...)
	pod.InitContainers = append(pod.InitContainers, base.InitContainers()...)
	enableServiceLinks := false
	pod.EnableServiceLinks = &enableServiceLinks
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: m.Name + "-vector", Namespace: m.Namespace}, Spec: appsv1.DeploymentSpec{
		Replicas: &v.Replicas, Selector: &metav1.LabelSelector{MatchLabels: labels},
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: mergePodLabels(labels, base.Labels()), Annotations: withJWTSigningAnnotation(m, base.Annotations())}, Spec: pod},
	}}
}

func (r *SeaweedReconciler) createVectorService(m *seaweedv1.Seaweed) *corev1.Service {
	labels := labelsForVector(m.Name)
	ports := []corev1.ServicePort{{Name: "vector-grpc", Port: vectorEffectivePort(m), TargetPort: intstr.FromString("vector-grpc")}}
	if m.Spec.Vector.MetricsPort != nil {
		ports = append(ports, corev1.ServicePort{Name: "vector-metrics", Port: *m.Spec.Vector.MetricsPort, TargetPort: intstr.FromString("vector-metrics")})
	}
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: m.Name + "-vector", Namespace: m.Namespace, Labels: labels}, Spec: corev1.ServiceSpec{Selector: labels, Ports: ports}}
}

func (r *SeaweedReconciler) createVectorServiceMonitor(m *seaweedv1.Seaweed) *monitorv1.ServiceMonitor {
	labels := labelsForVector(m.Name)
	return &monitorv1.ServiceMonitor{ObjectMeta: metav1.ObjectMeta{Name: m.Name + "-vector", Namespace: m.Namespace, Labels: labels}, Spec: monitorv1.ServiceMonitorSpec{
		Endpoints: []monitorv1.Endpoint{{Path: "/metrics", Port: "vector-metrics"}}, Selector: metav1.LabelSelector{MatchLabels: labels},
	}}
}
