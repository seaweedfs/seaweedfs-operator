package v1

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// ComponentAccessor is the interface to access component details, which respects the cluster-level properties
// and component-level overrides
// +kubebuilder:object:root=false
// +kubebuilder:object:generate=false
type ComponentAccessor interface {
	ImagePullPolicy() corev1.PullPolicy
	ImagePullSecrets() []corev1.LocalObjectReference
	HostNetwork() bool
	Affinity() *corev1.Affinity
	PriorityClassName() *string
	NodeSelector() map[string]string
	Annotations() map[string]string
	Tolerations() []corev1.Toleration
	SchedulerName() string
	DNSPolicy() corev1.DNSPolicy
	BuildPodSpec() corev1.PodSpec
	Env() []corev1.EnvVar
	TerminationGracePeriodSeconds() *int64
	StatefulSetUpdateStrategy() appsv1.StatefulSetUpdateStrategyType
}

type componentAccessorImpl struct {
	imagePullPolicy           corev1.PullPolicy
	imagePullSecrets          []corev1.LocalObjectReference
	hostNetwork               *bool
	affinity                  *corev1.Affinity
	priorityClassName         *string
	schedulerName             string
	clusterNodeSelector       map[string]string
	clusterAnnotations        map[string]string
	tolerations               []corev1.Toleration
	statefulSetUpdateStrategy appsv1.StatefulSetUpdateStrategyType

	// ComponentSpec is the Component Spec
	ComponentSpec *ComponentSpec
}

func (a *componentAccessorImpl) StatefulSetUpdateStrategy() appsv1.StatefulSetUpdateStrategyType {
	strategy := a.ComponentSpec.StatefulSetUpdateStrategy
	if len(strategy) != 0 {
		return strategy
	}

	strategy = a.statefulSetUpdateStrategy
	if len(strategy) != 0 {
		return strategy
	}

	return appsv1.RollingUpdateStatefulSetStrategyType
}

func (a *componentAccessorImpl) ImagePullPolicy() corev1.PullPolicy {
	pp := a.ComponentSpec.ImagePullPolicy
	if pp == nil {
		return a.imagePullPolicy
	}
	return *pp
}

func (a *componentAccessorImpl) ImagePullSecrets() []corev1.LocalObjectReference {
	ips := a.ComponentSpec.ImagePullSecrets
	if ips == nil {
		return a.imagePullSecrets
	}
	return ips
}

func (a *componentAccessorImpl) HostNetwork() bool {
	hostNetwork := a.ComponentSpec.HostNetwork
	if hostNetwork == nil {
		hostNetwork = a.hostNetwork
	}
	if hostNetwork == nil {
		return false
	}
	return *hostNetwork
}

func (a *componentAccessorImpl) Affinity() *corev1.Affinity {
	affi := a.ComponentSpec.Affinity
	if affi == nil {
		affi = a.affinity
	}
	return affi
}

func (a *componentAccessorImpl) PriorityClassName() *string {
	pcn := a.ComponentSpec.PriorityClassName
	if pcn == nil {
		pcn = a.priorityClassName
	}
	return pcn
}

func (a *componentAccessorImpl) SchedulerName() string {
	pcn := a.ComponentSpec.SchedulerName
	if pcn == nil {
		pcn = &a.schedulerName
	}
	return *pcn
}

func (a *componentAccessorImpl) NodeSelector() map[string]string {
	sel := map[string]string{}
	for k, v := range a.clusterNodeSelector {
		sel[k] = v
	}
	for k, v := range a.ComponentSpec.NodeSelector {
		sel[k] = v
	}
	return sel
}

func (a *componentAccessorImpl) Annotations() map[string]string {
	anno := map[string]string{}
	for k, v := range a.clusterAnnotations {
		anno[k] = v
	}
	for k, v := range a.ComponentSpec.Annotations {
		anno[k] = v
	}
	return anno
}

func (a *componentAccessorImpl) Tolerations() []corev1.Toleration {
	tols := a.ComponentSpec.Tolerations
	if len(tols) == 0 {
		tols = a.tolerations
	}
	return tols
}

func (a *componentAccessorImpl) DNSPolicy() corev1.DNSPolicy {
	dnsPolicy := corev1.DNSClusterFirst // same as kubernetes default
	if a.HostNetwork() {
		dnsPolicy = corev1.DNSClusterFirstWithHostNet
	}
	return dnsPolicy
}

func (a *componentAccessorImpl) BuildPodSpec() corev1.PodSpec {
	spec := corev1.PodSpec{
		SchedulerName: a.SchedulerName(),
		Affinity:      a.Affinity(),
		NodeSelector:  a.NodeSelector(),
		HostNetwork:   a.HostNetwork(),
		RestartPolicy: corev1.RestartPolicyAlways,
		Tolerations:   a.Tolerations(),
	}
	if a.PriorityClassName() != nil {
		spec.PriorityClassName = *a.PriorityClassName()
	}
	if a.ImagePullSecrets() != nil {
		spec.ImagePullSecrets = a.ImagePullSecrets()
	}
	if a.TerminationGracePeriodSeconds() != nil {
		spec.TerminationGracePeriodSeconds = a.TerminationGracePeriodSeconds()
	}
	return spec
}

func (a *componentAccessorImpl) Env() []corev1.EnvVar {
	return a.ComponentSpec.Env
}

func (a *componentAccessorImpl) TerminationGracePeriodSeconds() *int64 {
	return a.ComponentSpec.TerminationGracePeriodSeconds
}

func buildSeaweedComponentAccessor(spec *SeaweedSpec, componentSpec *ComponentSpec) ComponentAccessor {
	return &componentAccessorImpl{
		imagePullPolicy:           spec.ImagePullPolicy,
		imagePullSecrets:          spec.ImagePullSecrets,
		hostNetwork:               spec.HostNetwork,
		affinity:                  spec.Affinity,
		schedulerName:             spec.SchedulerName,
		clusterNodeSelector:       spec.NodeSelector,
		clusterAnnotations:        spec.Annotations,
		tolerations:               spec.Tolerations,
		statefulSetUpdateStrategy: spec.StatefulSetUpdateStrategy,

		ComponentSpec: componentSpec,
	}
}

// BaseMasterSpec provides merged spec of masters
func (s *Seaweed) BaseMasterSpec() ComponentAccessor {
	return buildSeaweedComponentAccessor(&s.Spec, &s.Spec.Master.ComponentSpec)
}

// BaseFilerSpec provides merged spec of filers
func (s *Seaweed) BaseFilerSpec() ComponentAccessor {
	return buildSeaweedComponentAccessor(&s.Spec, &s.Spec.Filer.ComponentSpec)
}

// BaseVolumeSpec provides merged spec of volumes
func (s *Seaweed) BaseVolumeSpec() ComponentAccessor {
	return buildSeaweedComponentAccessor(&s.Spec, &s.Spec.Volume.ComponentSpec)
}

// BaseIAMSpec provides merged spec of iam
func (s *Seaweed) BaseIAMSpec() ComponentAccessor {
	return buildSeaweedComponentAccessor(&s.Spec, &s.Spec.IAM.ComponentSpec)
}
