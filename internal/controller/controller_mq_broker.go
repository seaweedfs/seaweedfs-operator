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

func (r *SeaweedReconciler) ensureMQBrokerServers(seaweedCR *seaweedv1.Seaweed) (done bool, result ctrl.Result, err error) {
	_ = context.Background()
	_ = r.Log.With("seaweed", seaweedCR.Name)

	if done, result, err = r.ensureMQBrokerPeerService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureMQBrokerService(seaweedCR); done {
		return
	}

	if done, result, err = r.ensureMQBrokerStatefulSet(seaweedCR); done {
		return
	}

	metricsPort := resolveMetricsPort(seaweedCR, seaweedCR.Spec.MessageQueue.Broker.MetricsPort)

	if metricsPort != nil {
		if done, result, err = r.ensureMQBrokerServiceMonitor(seaweedCR); done {
			return
		}
	}

	return
}

func (r *SeaweedReconciler) ensureMQBrokerStatefulSet(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-mq-broker-statefulset", seaweedCR.Name)

	mqBrokerStatefulSet := r.createMQBrokerStatefulSet(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, mqBrokerStatefulSet, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdate(mqBrokerStatefulSet, func(existing, desired runtime.Object) error {
		existingStatefulSet := existing.(*appsv1.StatefulSet)
		desiredStatefulSet := desired.(*appsv1.StatefulSet)

		existingStatefulSet.Spec.Replicas = desiredStatefulSet.Spec.Replicas
		existingStatefulSet.Spec.Template.ObjectMeta = desiredStatefulSet.Spec.Template.ObjectMeta
		existingStatefulSet.Spec.Template.Spec = desiredStatefulSet.Spec.Template.Spec
		return nil
	})

	log.Info("ensure mq broker stateful set " + mqBrokerStatefulSet.Name)
	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureMQBrokerPeerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-mq-broker-peer-service", seaweedCR.Name)

	mqBrokerPeerService := r.createMQBrokerPeerService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, mqBrokerPeerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}

	_, err := r.CreateOrUpdateService(mqBrokerPeerService)
	log.Info("ensure mq broker peer service " + mqBrokerPeerService.Name)

	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureMQBrokerService(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-mq-broker-service", seaweedCR.Name)

	mqBrokerService := r.createMQBrokerService(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, mqBrokerService, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateService(mqBrokerService)

	log.Info("ensure mq broker service " + mqBrokerService.Name)

	return ReconcileResult(err)
}

func (r *SeaweedReconciler) ensureMQBrokerServiceMonitor(seaweedCR *seaweedv1.Seaweed) (bool, ctrl.Result, error) {
	log := r.Log.With("sw-mq-broker-servicemonitor", seaweedCR.Name)

	mqBrokerServiceMonitor := r.createMQBrokerServiceMonitor(seaweedCR)
	if err := controllerutil.SetControllerReference(seaweedCR, mqBrokerServiceMonitor, r.Scheme); err != nil {
		return ReconcileResult(err)
	}
	_, err := r.CreateOrUpdateServiceMonitor(mqBrokerServiceMonitor)

	log.Info("ensure mq broker service monitor " + mqBrokerServiceMonitor.Name)
	return ReconcileResult(err)
}

func labelsForMQBroker(name string) map[string]string {
	return map[string]string{
		label.ManagedByLabelKey: "seaweedfs-operator",
		label.NameLabelKey:      "seaweedfs",
		label.ComponentLabelKey: "mq-broker",
		label.InstanceLabelKey:  name,
	}
}
