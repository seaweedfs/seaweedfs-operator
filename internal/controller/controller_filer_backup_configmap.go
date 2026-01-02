package controller

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	"github.com/seaweedfs/seaweedfs-operator/internal/controller/backup"
)

func (r *SeaweedReconciler) createFilerBackupConfigMap(ctx context.Context, m *seaweedv1.Seaweed) *corev1.ConfigMap {
	labels := labelsForFilerBackup(m.Name)

	config := ""
	if m.Spec.FilerBackup.Config != nil {
		config = *m.Spec.FilerBackup.Config
	} else {
		config = r.generateBackupConfig(ctx, m)
	}

	dep := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-filer-backup",
			Namespace: m.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			"replication.toml": config,
		},
	}
	return dep
}

func (r *SeaweedReconciler) generateBackupConfig(ctx context.Context, m *seaweedv1.Seaweed) string {
	var config strings.Builder
	log := r.Log.With("generateBackupConfig", m.Name)

	// Create a secret getter that wraps the controller's getSecret method
	secretGetter := backup.NewControllerSecretGetter(r.getSecret)

	// Sink configurations
	if m.Spec.FilerBackup.Sink != nil {
		// Local sink
		if m.Spec.FilerBackup.Sink.Local != nil && m.Spec.FilerBackup.Sink.Local.Enabled {
			backup.GenerateLocalSinkConfig(&config, m.Spec.FilerBackup.Sink.Local)
		}

		// Filer sink
		if m.Spec.FilerBackup.Sink.Filer != nil && m.Spec.FilerBackup.Sink.Filer.Enabled {
			backup.GenerateFilerSinkConfig(&config, m.Spec.FilerBackup.Sink.Filer)
		}

		// S3 sink
		if m.Spec.FilerBackup.Sink.S3 != nil && m.Spec.FilerBackup.Sink.S3.Enabled {
			backup.GenerateS3SinkConfig(ctx, &config, m.Spec.FilerBackup.Sink.S3, m.Namespace, log, secretGetter)
		}

		// Google Cloud Storage sink
		if m.Spec.FilerBackup.Sink.GoogleCloudStorage != nil && m.Spec.FilerBackup.Sink.GoogleCloudStorage.Enabled {
			backup.GenerateGCSSinkConfig(ctx, &config, m.Spec.FilerBackup.Sink.GoogleCloudStorage, m.Namespace, log, secretGetter)
		}

		// Azure sink
		if m.Spec.FilerBackup.Sink.Azure != nil && m.Spec.FilerBackup.Sink.Azure.Enabled {
			backup.GenerateAzureSinkConfig(ctx, &config, m.Spec.FilerBackup.Sink.Azure, m.Namespace, log, secretGetter)
		}

		// Backblaze sink
		if m.Spec.FilerBackup.Sink.Backblaze != nil && m.Spec.FilerBackup.Sink.Backblaze.Enabled {
			backup.GenerateBackblazeSinkConfig(ctx, &config, m.Spec.FilerBackup.Sink.Backblaze, m.Namespace, log, secretGetter)
		}
	}

	return config.String()
}
