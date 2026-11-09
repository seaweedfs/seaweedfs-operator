package v1

import "fmt"

func (r *Seaweed) validateVector() []error {
	v := r.Spec.Vector
	if v == nil {
		return nil
	}
	var errs []error
	if r.Spec.Filer == nil {
		errs = append(errs, fmt.Errorf("spec.vector requires spec.filer"))
	}
	if v.MetricsPort != nil && v.Port != nil && *v.MetricsPort == *v.Port || v.MetricsPort != nil && v.Port == nil && *v.MetricsPort == 8090 {
		errs = append(errs, fmt.Errorf("spec.vector.metricsPort must differ from its gRPC port"))
	}
	return errs
}
