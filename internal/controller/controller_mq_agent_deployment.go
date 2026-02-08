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

func buildMQAgentStartupScript(m *seaweedv1.Seaweed) string {
	commands := []string{"weed", "-logtostderr=true", "mq.agent"}

	port := seaweedv1.MQAgentGRPCPort
	if m.Spec.MessageQueue.Agent.Port != nil {
		port = int(*m.Spec.MessageQueue.Agent.Port)
	}

	commands = append(commands, fmt.Sprintf("-port=%d", port))

	// Agent connects to broker service
	brokerAddress := getMQBrokerServiceAddress(m)
	commands = append(commands, fmt.Sprintf("-broker=%s", brokerAddress))

	metricsPort := resolveMetricsPort(m, m.Spec.MessageQueue.Agent.MetricsPort)
	if metricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *metricsPort))
	}

	return strings.Join(commands, " ")
}

func (r *SeaweedReconciler) createMQAgentDeployment(m *seaweedv1.Seaweed) *appsv1.Deployment {
	labels := labelsForMQAgent(m.Name)
	annotations := m.Spec.MessageQueue.Agent.Annotations

	port := int32(seaweedv1.MQAgentGRPCPort)
	if m.Spec.MessageQueue.Agent.Port != nil {
		port = *m.Spec.MessageQueue.Agent.Port
	}

	ports := []corev1.ContainerPort{
		{
			ContainerPort: port,
			Name:          "mq-agent-grpc",
		},
	}

	metricsPort := resolveMetricsPort(m, m.Spec.MessageQueue.Agent.MetricsPort)
	if metricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *metricsPort,
			Name:          "mq-agent-metrics",
		})
	}

	replicas := int32(m.Spec.MessageQueue.Agent.Replicas)
	enableServiceLinks := false

	mqAgentPodSpec := m.BaseMessageQueueAgentSpec().BuildPodSpec()
	mqAgentPodSpec.EnableServiceLinks = &enableServiceLinks
	mqAgentPodSpec.Containers = []corev1.Container{{
		Name:            "mq-agent",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseMessageQueueAgentSpec().ImagePullPolicy(),
		Env:             append(m.BaseMessageQueueAgentSpec().Env(), kubernetesEnvVars...),
		Resources:       filterContainerResources(m.Spec.MessageQueue.Agent.ResourceRequirements),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildMQAgentStartupScript(m),
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

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-mq-agent",
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: mqAgentPodSpec,
			},
		},
	}
	return dep
}
