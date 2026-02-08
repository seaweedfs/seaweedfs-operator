package controller

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func buildMQBrokerStartupScript(m *seaweedv1.Seaweed) string {
	commands := []string{"weed", "-logtostderr=true", "mq.broker"}

	port := seaweedv1.MQBrokerGRPCPort
	if m.Spec.MessageQueue.Broker.Port != nil {
		port = int(*m.Spec.MessageQueue.Broker.Port)
	}

	commands = append(commands, fmt.Sprintf("-port=%d", port))
	commands = append(commands, fmt.Sprintf("-master=%s", getMasterPeersString(m)))

	// Optional topology settings
	if m.Spec.MessageQueue.Broker.FilerGroup != nil && *m.Spec.MessageQueue.Broker.FilerGroup != "" {
		commands = append(commands, fmt.Sprintf("-filerGroup=%s", *m.Spec.MessageQueue.Broker.FilerGroup))
	}

	if m.Spec.MessageQueue.Broker.DataCenter != nil && *m.Spec.MessageQueue.Broker.DataCenter != "" {
		commands = append(commands, fmt.Sprintf("-dataCenter=%s", *m.Spec.MessageQueue.Broker.DataCenter))
	}

	if m.Spec.MessageQueue.Broker.Rack != nil && *m.Spec.MessageQueue.Broker.Rack != "" {
		commands = append(commands, fmt.Sprintf("-rack=%s", *m.Spec.MessageQueue.Broker.Rack))
	}

	metricsPort := resolveMetricsPort(m, m.Spec.MessageQueue.Broker.MetricsPort)
	if metricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *metricsPort))
	}

	return strings.Join(commands, " ")
}

func (r *SeaweedReconciler) createMQBrokerStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForMQBroker(m.Name)
	annotations := m.Spec.MessageQueue.Broker.Annotations

	port := int32(seaweedv1.MQBrokerGRPCPort)
	if m.Spec.MessageQueue.Broker.Port != nil {
		port = *m.Spec.MessageQueue.Broker.Port
	}

	ports := []corev1.ContainerPort{
		{
			ContainerPort: port,
			Name:          "mq-broker-grpc",
		},
	}

	metricsPort := resolveMetricsPort(m, m.Spec.MessageQueue.Broker.MetricsPort)
	if metricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *metricsPort,
			Name:          "mq-broker-metrics",
		})
	}

	replicas := int32(m.Spec.MessageQueue.Broker.Replicas)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	mqBrokerPodSpec := m.BaseMessageQueueBrokerSpec().BuildPodSpec()
	mqBrokerPodSpec.EnableServiceLinks = &enableServiceLinks
	mqBrokerPodSpec.Subdomain = m.Name + "-mq-broker-peer"
	mqBrokerPodSpec.Containers = []corev1.Container{{
		Name:            "mq-broker",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseMessageQueueBrokerSpec().ImagePullPolicy(),
		Env:             append(m.BaseMessageQueueBrokerSpec().Env(), kubernetesEnvVars...),
		Resources:       filterContainerResources(m.Spec.MessageQueue.Broker.ResourceRequirements),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildMQBrokerStartupScript(m),
		},
		Ports: ports,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(int(port)),
				},
			},
			InitialDelaySeconds: 10,
			TimeoutSeconds:      3,
			PeriodSeconds:       15,
			SuccessThreshold:    1,
			FailureThreshold:    100,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromInt(int(port)),
				},
			},
			InitialDelaySeconds: 20,
			TimeoutSeconds:      3,
			PeriodSeconds:       30,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
	}}

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-mq-broker",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-mq-broker-peer",
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Replicas:            &replicas,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: &rollingUpdatePartition,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: mqBrokerPodSpec,
			},
		},
	}
	return dep
}
