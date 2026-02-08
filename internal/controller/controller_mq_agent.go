package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	label "github.com/seaweedfs/seaweedfs-operator/internal/controller/label"
)

func (r *SeaweedReconciler) ensureMQAgentServers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.With("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureMQAgentService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureMQAgentDeployment(seaweedCR); done {
		return
	}

	metricsPort := resolveMetricsPort(seaweedCR, seaweedCR.Spec.MessageQueue.Agent.MetricsPort)

	if metricsPort != nil {
		if done, result, err = r.ensureMQAgentServiceMonitor(seaweedCR); done {
			return
		}
	}

	return
}

func (r *SeaweedReconciler) ensureMQAgentDeployment(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-mq-agent-deployment", seaweedCR.Name)

	mqAgentDeployment := r.createMQAgentDeployment(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, mqAgentDeployment, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdate(mqAgentDeployment, func(existing, desired runtime.Object) error {
		existingDeployment := existing.(*appsv1.Deployment)
		desiredDeployment := desired.(*appsv1.Deployment)

		existingDeployment.Spec.Replicas = desiredDeployment.Spec.Replicas
		existingDeployment.Spec.Template.ObjectMeta = desiredDeployment.Spec.Template.ObjectMeta
		existingDeployment.Spec.Template.Spec = desiredDeployment.Spec.Template.Spec
		return nil
	})

	log.Info("ensure mq agent deployment " + mqAgentDeployment.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureMQAgentService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-mq-agent-service", seaweedCR.Name)

	mqAgentService := r.createMQAgentService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, mqAgentService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(mqAgentService)

	log.Info("ensure mq agent service " + mqAgentService.Name)

	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureMQAgentServiceMonitor(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-mq-agent-servicemonitor", seaweedCR.Name)

	mqAgentServiceMonitor := r.createMQAgentServiceMonitor(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, mqAgentServiceMonitor, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateServiceMonitor(mqAgentServiceMonitor)

	log.Info("ensure mq agent service monitor " + mqAgentServiceMonitor.Name)
	return ReconcileResult(err)
}

func labelsForMQAgent(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "mq-agent",
		label.InstanceLabelKey:  name,
	}
}
