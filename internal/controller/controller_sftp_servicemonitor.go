/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
*/

package controller

import (
	monitorv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createSFTPServiceMonitor(m *seaweedv1.Seaweed) *monitorv1.ServiceMonitor {
	labels := labelsForSFTP(m.Name)
	return &monitorv1.ServiceMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-sftp",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Spec: monitorv1.ServiceMonitorSpec{
			Endpoints: []monitorv1.Endpoint{{
				Path: "/metrics",
				Port: "sftp-metrics",
			}},
			Selector: metav1.LabelSelector{MatchLabels: labels},
		},
	}
}
