package controller

import (
	"context"
	"encoding/json"
	"fmt"
	monitorv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// the following is adapted from tidb-operator/pkg/controller/generic_control.go

const (
	// LastAppliedPodTemplate is annotation key of the last applied pod template
	LastAppliedPodTemplate = "seaweedfs.com/last-applied-podtemplate"

	// LastAppliedConfigAnnotation is annotation key of last applied configuration
	LastAppliedConfigAnnotation = "seaweedfs.com/last-applied-configuration"
)

// MergeFn is to resolve conflicts
type MergeFn func(existing, desired runtime.Object) error

// CreateOrUpdate create an object to the Kubernetes cluster for controller, if the object to create is existed,
// call mergeFn to merge the change in new object to the existing object, then update the existing object.
// The object will also be adopted by the given controller.
func (r *SeaweedReconciler) CreateOrUpdate(obj runtime.Object, mergeFn MergeFn) (runtime.Object, error) {

	// controller-runtime/client will mutate the object pointer in-place,
	// to be consistent with other methods in our controller, we copy the object
	// to avoid the in-place mutation here and hereafter.
	desired := obj.DeepCopyObject().(client.Object)

	// 1. try to create and see if there is any conflicts
	err := r.Create(context.TODO(), desired)
	if errors.IsAlreadyExists(err) {
		// 2. object has already existed, merge our desired changes to it
		existing, err := r.EmptyClone(obj)

		if err != nil {
			return nil, err
		}
		err = r.Get(context.TODO(), client.ObjectKeyFromObject(desired), existing.(client.Object))
		if err != nil {
			return nil, err
		}

		mutated := existing.DeepCopyObject().(client.Object)
		// 4. invoke mergeFn to mutate a copy of the existing object
		if err := mergeFn(mutated, desired); err != nil {
			return nil, err
		}

		// 5. check if the copy is actually mutated
		if !apiequality.Semantic.DeepEqual(existing, mutated) {
			err := r.Update(context.TODO(), mutated)
			return mutated, err
		}

		return mutated, nil
	}

	return desired, err
}

func (r *SeaweedReconciler) addSpecToAnnotation(d *appsv1.Deployment) error {
	b, err := json.Marshal(d.Spec.Template.Spec)
	if err != nil {
		return err
	}
	if d.Annotations == nil {
		d.Annotations = map[string]string{}
	}
	d.Annotations[LastAppliedPodTemplate] = string(b)
	return nil
}

func (r *SeaweedReconciler) CreateOrUpdateDeployment(deploy *appsv1.Deployment) (*appsv1.Deployment, error) {
	r.addSpecToAnnotation(deploy)
	result, err := r.CreateOrUpdate(deploy, func(existing, desired runtime.Object) error {
		existingDep := existing.(*appsv1.Deployment)
		desiredDep := desired.(*appsv1.Deployment)

		existingDep.Spec.Replicas = desiredDep.Spec.Replicas
		existingDep.Labels = desiredDep.Labels

		if existingDep.Annotations == nil {
			existingDep.Annotations = map[string]string{}
		}
		for k, v := range desiredDep.Annotations {
			existingDep.Annotations[k] = v
		}
		// only override the default strategy if it is explicitly set in the desiredDep
		if string(desiredDep.Spec.Strategy.Type) != "" {
			existingDep.Spec.Strategy.Type = desiredDep.Spec.Strategy.Type
			if existingDep.Spec.Strategy.RollingUpdate != nil {
				existingDep.Spec.Strategy.RollingUpdate = desiredDep.Spec.Strategy.RollingUpdate
			}
		}
		// pod selector of deployment is immutable, so we don't mutate the labels of pod
		for k, v := range desiredDep.Spec.Template.Annotations {
			existingDep.Spec.Template.Annotations[k] = v
		}
		// podSpec of deployment is hard to merge, use an annotation to assist
		if DeploymentPodSpecChanged(desiredDep, existingDep) {
			// Record last applied spec in favor of future equality check
			b, err := json.Marshal(desiredDep.Spec.Template.Spec)
			if err != nil {
				return err
			}
			existingDep.Annotations[LastAppliedConfigAnnotation] = string(b)
			existingDep.Spec.Template.Spec = desiredDep.Spec.Template.Spec
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*appsv1.Deployment), err
}

func (r *SeaweedReconciler) CreateOrUpdateService(svc *corev1.Service) (*corev1.Service, error) {
	result, err := r.CreateOrUpdate(svc, func(existing, desired runtime.Object) error {
		existingSvc := existing.(*corev1.Service)
		desiredSvc := desired.(*corev1.Service)

		if existingSvc.Annotations == nil {
			existingSvc.Annotations = map[string]string{}
		}
		for k, v := range desiredSvc.Annotations {
			existingSvc.Annotations[k] = v
		}
		existingSvc.Labels = desiredSvc.Labels
		equal, err := ServiceEqual(desiredSvc, existingSvc)
		if err != nil {
			return err
		}
		if !equal {
			// record desiredSvc Spec in annotations in favor of future equality checks
			b, err := json.Marshal(desiredSvc.Spec)
			if err != nil {
				return err
			}
			existingSvc.Annotations[LastAppliedConfigAnnotation] = string(b)
			clusterIp := existingSvc.Spec.ClusterIP
			ports := existingSvc.Spec.Ports
			serviceType := existingSvc.Spec.Type

			existingSvc.Spec = desiredSvc.Spec
			existingSvc.Spec.ClusterIP = clusterIp

			// If the existed service and the desired service is NodePort or LoadBalancerType, we should keep the nodePort unchanged.
			if (serviceType == corev1.ServiceTypeNodePort || serviceType == corev1.ServiceTypeLoadBalancer) &&
				(desiredSvc.Spec.Type == corev1.ServiceTypeNodePort || desiredSvc.Spec.Type == corev1.ServiceTypeLoadBalancer) {
				for i, dport := range existingSvc.Spec.Ports {
					for _, eport := range ports {
						// Because the portName could be edited,
						// we use Port number to link the desired Service Port and the existed Service Port in the nested loop
						if dport.Port == eport.Port && dport.Protocol == eport.Protocol {
							dport.NodePort = eport.NodePort
							existingSvc.Spec.Ports[i] = dport
							break
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*corev1.Service), nil
}

func (r *SeaweedReconciler) CreateOrUpdateIngress(ingress *networkingv1.Ingress) (*networkingv1.Ingress, error) {
	result, err := r.CreateOrUpdate(ingress, func(existing, desired runtime.Object) error {
		existingIngress := existing.(*networkingv1.Ingress)
		desiredIngress := desired.(*networkingv1.Ingress)

		if existingIngress.Annotations == nil {
			existingIngress.Annotations = map[string]string{}
		}
		for k, v := range desiredIngress.Annotations {
			existingIngress.Annotations[k] = v
		}
		existingIngress.Labels = desiredIngress.Labels
		equal, err := IngressEqual(desiredIngress, existingIngress)
		if err != nil {
			return err
		}
		if !equal {
			// record desiredIngress Spec in annotations in favor of future equality checks
			b, err := json.Marshal(desiredIngress.Spec)
			if err != nil {
				return err
			}
			existingIngress.Annotations[LastAppliedConfigAnnotation] = string(b)
			existingIngress.Spec = desiredIngress.Spec
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*networkingv1.Ingress), nil
}

func (r *SeaweedReconciler) CreateOrUpdateConfigMap(configMap *corev1.ConfigMap) (*corev1.ConfigMap, error) {
	result, err := r.CreateOrUpdate(configMap, func(existing, desired runtime.Object) error {
		existingConfigMap := existing.(*corev1.ConfigMap)
		desiredConfigMap := desired.(*corev1.ConfigMap)

		if existingConfigMap.Annotations == nil {
			existingConfigMap.Annotations = map[string]string{}
		}
		for k, v := range desiredConfigMap.Annotations {
			existingConfigMap.Annotations[k] = v
		}
		existingConfigMap.Labels = desiredConfigMap.Labels
		existingConfigMap.Data = desiredConfigMap.Data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*corev1.ConfigMap), nil
}

func (r *SeaweedReconciler) CreateOrUpdateServiceMonitor(serviceMonitor *monitorv1.ServiceMonitor) (*monitorv1.ServiceMonitor, error) {

	result, err := r.CreateOrUpdate(serviceMonitor, func(existing, desired runtime.Object) error {
		existingServiceMonitor := existing.(*monitorv1.ServiceMonitor)
		desiredServiceMonitor := desired.(*monitorv1.ServiceMonitor)

		if existingServiceMonitor.Annotations == nil {
			existingServiceMonitor.Annotations = map[string]string{}
		}
		for k, v := range desiredServiceMonitor.Annotations {
			existingServiceMonitor.Annotations[k] = v
		}
		existingServiceMonitor.Labels = desiredServiceMonitor.Labels
		existingServiceMonitor.Spec = desiredServiceMonitor.Spec
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*monitorv1.ServiceMonitor), nil
}

// EmptyClone create an clone of the resource with the same name and namespace (if namespace-scoped), with other fields unset
func (r *SeaweedReconciler) EmptyClone(obj runtime.Object) (runtime.Object, error) {

	meta, ok := obj.(metav1.Object)
	if !ok {
		return nil, fmt.Errorf("Obj %v is not a metav1.Object, cannot call EmptyClone", obj)
	}

	gvk, err := r.InferObjectKind(obj)
	if err != nil {
		return nil, err
	}
	inst, err := r.Scheme.New(gvk)
	if err != nil {
		return nil, err
	}
	instMeta, ok := inst.(metav1.Object)
	if !ok {
		return nil, fmt.Errorf("New instatnce %v created from scheme is not a metav1.Object, EmptyClone failed", inst)
	}

	instMeta.SetName(meta.GetName())
	instMeta.SetNamespace(meta.GetNamespace())
	return inst, nil
}

// InferObjectKind infers the object kind
func (r *SeaweedReconciler) InferObjectKind(obj runtime.Object) (schema.GroupVersionKind, error) {
	gvks, _, err := r.Scheme.ObjectKinds(obj)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	if len(gvks) != 1 {
		return schema.GroupVersionKind{}, fmt.Errorf("Object %v has ambigious GVK", obj)
	}
	return gvks[0], nil
}

// GetDeploymentLastAppliedPodTemplate set last applied pod template from Deployment's annotation
func GetDeploymentLastAppliedPodTemplate(dep *appsv1.Deployment) (*corev1.PodSpec, error) {
	applied, ok := dep.Annotations[LastAppliedPodTemplate]
	if !ok {
		return nil, fmt.Errorf("deployment:[%s/%s] not found spec's apply config", dep.GetNamespace(), dep.GetName())
	}
	podSpec := &corev1.PodSpec{}
	err := json.Unmarshal([]byte(applied), podSpec)
	if err != nil {
		return nil, err
	}
	return podSpec, nil
}

// DeploymentPodSpecChanged checks whether the new deployment differs with the old one's last-applied-config
func DeploymentPodSpecChanged(newDep *appsv1.Deployment, oldDep *appsv1.Deployment) bool {
	lastAppliedPodTemplate, err := GetDeploymentLastAppliedPodTemplate(oldDep)
	if err != nil {
		klog.Warningf("error get last-applied-config of deployment %s/%s: %v", oldDep.Namespace, oldDep.Name, err)
		return true
	}
	return !apiequality.Semantic.DeepEqual(newDep.Spec.Template.Spec, lastAppliedPodTemplate)
}

// ServiceEqual compares the new Service's spec with old Service's last applied config
func ServiceEqual(newSvc, oldSvc *corev1.Service) (bool, error) {
	oldSpec := corev1.ServiceSpec{}
	if lastAppliedConfig, ok := oldSvc.Annotations[LastAppliedConfigAnnotation]; ok {
		err := json.Unmarshal([]byte(lastAppliedConfig), &oldSpec)
		if err != nil {
			klog.Errorf("unmarshal ServiceSpec: [%s/%s]'s applied config failed,error: %v", oldSvc.GetNamespace(), oldSvc.GetName(), err)
			return false, err
		}
		return apiequality.Semantic.DeepEqual(oldSpec, newSvc.Spec), nil
	}
	return false, nil
}

func IngressEqual(newIngress, oldIngres *networkingv1.Ingress) (bool, error) {
	oldIngressSpec := networkingv1.IngressSpec{}
	if lastAppliedConfig, ok := oldIngres.Annotations[LastAppliedConfigAnnotation]; ok {
		err := json.Unmarshal([]byte(lastAppliedConfig), &oldIngressSpec)
		if err != nil {
			klog.Errorf("unmarshal IngressSpec: [%s/%s]'s applied config failed,error: %v", oldIngres.GetNamespace(), oldIngres.GetName(), err)
			return false, err
		}
		return apiequality.Semantic.DeepEqual(oldIngressSpec, newIngress.Spec), nil
	}
	return false, nil
}
