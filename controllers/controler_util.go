package controllers

import (
	"context"
	"fmt"
	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	extensionsv1beta1 "k8s.io/api/extensions/v1beta1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// svcName is the backend service name
func createIngress(seaweedCR *seaweedv1.Seaweed, svcName string, port int) *extensionsv1beta1.Ingress {
	ingressLabel := map[string]string{"app": "seaweedfs", "role": "ingress", "name": svcName}

	ingress := &extensionsv1beta1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName + "-ingress",
			Namespace: seaweedCR.Namespace,
			Labels:    ingressLabel,
		},
		Spec: extensionsv1beta1.IngressSpec{
			Rules: []extensionsv1beta1.IngressRule{},
		},
	}

	for _, host := range seaweedCR.Spec.Hosts {
		rule := extensionsv1beta1.IngressRule{
			Host: host,
			IngressRuleValue: extensionsv1beta1.IngressRuleValue{
				HTTP: &extensionsv1beta1.HTTPIngressRuleValue{
					Paths: []extensionsv1beta1.HTTPIngressPath{
						{
							Path: "/",
							Backend: extensionsv1beta1.IngressBackend{
								ServiceName: svcName,
								ServicePort: intstr.FromInt(port),
							},
						},
					},
				},
			},
		}
		ingress.Spec.Rules = append(ingress.Spec.Rules, rule)
	}
	return ingress
}

// the following is adapted from tidb-operator/pkg/controller/generic_control.go

type MergeFn func(existing, desired runtime.Object) error

// CreateOrUpdate create an object to the Kubernetes cluster for controller, if the object to create is existed,
// call mergeFn to merge the change in new object to the existing object, then update the existing object.
// The object will also be adopted by the given controller.
func (r *SeaweedReconciler) CreateOrUpdate(controller, obj runtime.Object, mergeFn MergeFn) (runtime.Object, error) {

	// controller-runtime/client will mutate the object pointer in-place,
	// to be consistent with other methods in our controller, we copy the object
	// to avoid the in-place mutation here and hereafter.
	desired := obj.DeepCopyObject()

	// 1. try to create and see if there is any conflicts
	err := r.Create(context.TODO(), desired)
	if errors.IsAlreadyExists(err) {

		// 2. object has already existed, merge our desired changes to it
		existing, err := EmptyClone(obj)
		if err != nil {
			return nil, err
		}
		key, err := client.ObjectKeyFromObject(existing)
		if err != nil {
			return nil, err
		}
		err = r.Get(context.TODO(), key, existing)
		if err != nil {
			return nil, err
		}

		mutated := existing.DeepCopyObject()
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

// EmptyClone create an clone of the resource with the same name and namespace (if namespace-scoped), with other fields unset
func EmptyClone(obj runtime.Object) (runtime.Object, error) {
	meta, ok := obj.(metav1.Object)
	if !ok {
		return nil, fmt.Errorf("Obj %v is not a metav1.Object, cannot call EmptyClone", obj)
	}
	gvk, err := InferObjectKind(obj)
	if err != nil {
		return nil, err
	}
	inst, err := scheme.Scheme.New(gvk)
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
func InferObjectKind(obj runtime.Object) (schema.GroupVersionKind, error) {
	gvks, _, err := scheme.Scheme.ObjectKinds(obj)
	if err != nil {
		return schema.GroupVersionKind{}, err
	}
	if len(gvks) != 1 {
		return schema.GroupVersionKind{}, fmt.Errorf("Object %v has ambigious GVK", obj)
	}
	return gvks[0], nil
}
