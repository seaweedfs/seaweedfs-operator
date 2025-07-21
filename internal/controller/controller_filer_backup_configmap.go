package controller

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createFilerBackupConfigMap(m *seaweedv1.Seaweed) *corev1.ConfigMap {
	labels := labelsForFilerBackup(m.Name)

	config := ""
	if m.Spec.FilerBackup.Config != nil {
		config = *m.Spec.FilerBackup.Config
	} else {
		config = generateBackupConfig(m)
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

func generateBackupConfig(m *seaweedv1.Seaweed) string {
	var config strings.Builder

	// Sink configurations
	if m.Spec.FilerBackup.Sink != nil {
		if m.Spec.FilerBackup.Sink.Local != nil && m.Spec.FilerBackup.Sink.Local.Enabled {
			config.WriteString("[sink.local]\n")
			config.WriteString("enabled = true\n")
			config.WriteString(fmt.Sprintf("directory = \"%s\"\n", m.Spec.FilerBackup.Sink.Local.Directory))
			config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", m.Spec.FilerBackup.Sink.Local.IsIncremental))
		}

		if m.Spec.FilerBackup.Sink.Filer != nil && m.Spec.FilerBackup.Sink.Filer.Enabled {
			config.WriteString("[sink.filer]\n")
			config.WriteString("enabled = true\n")
			config.WriteString(fmt.Sprintf("grpcAddress = \"%s\"\n", m.Spec.FilerBackup.Sink.Filer.GRPCAddress))
			config.WriteString(fmt.Sprintf("directory = \"%s\"\n", m.Spec.FilerBackup.Sink.Filer.Directory))
			config.WriteString(fmt.Sprintf("replication = \"%s\"\n", m.Spec.FilerBackup.Sink.Filer.Replication))
			config.WriteString(fmt.Sprintf("collection = \"%s\"\n", m.Spec.FilerBackup.Sink.Filer.Collection))
			config.WriteString(fmt.Sprintf("ttlSec = %d\n", m.Spec.FilerBackup.Sink.Filer.TTLSec))
			config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", m.Spec.FilerBackup.Sink.Filer.IsIncremental))
		}

		if m.Spec.FilerBackup.Sink.S3 != nil && m.Spec.FilerBackup.Sink.S3.Enabled {
			if m.Spec.FilerBackup.Sink.S3.AWSCredentialsSecretRef != nil && m.Spec.FilerBackup.Sink.S3.AWSCredentialsSecretRef.Name != "" {
				// TODO: Get the secret
			}

			config.WriteString("[sink.s3]\n")
			config.WriteString("enabled = true\n")
			config.WriteString(fmt.Sprintf("aws_access_key_id = \"%s\"\n", m.Spec.FilerBackup.Sink.S3.AWSAccessKeyID))
			config.WriteString(fmt.Sprintf("aws_secret_access_key = \"%s\"\n", m.Spec.FilerBackup.Sink.S3.AWSSecretAccessKey))
			config.WriteString(fmt.Sprintf("region = \"%s\"\n", m.Spec.FilerBackup.Sink.S3.Region))
			config.WriteString(fmt.Sprintf("bucket = \"%s\"\n", m.Spec.FilerBackup.Sink.S3.Bucket))
			config.WriteString(fmt.Sprintf("directory = \"%s\"\n", m.Spec.FilerBackup.Sink.S3.Directory))
			config.WriteString(fmt.Sprintf("endpoint = \"%s\"\n", m.Spec.FilerBackup.Sink.S3.Endpoint))
			config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", m.Spec.FilerBackup.Sink.S3.IsIncremental))
		}

		if m.Spec.FilerBackup.Sink.GoogleCloudStorage != nil && m.Spec.FilerBackup.Sink.GoogleCloudStorage.Enabled {
			config.WriteString("[sink.google_cloud_storage]\n")
			config.WriteString("enabled = true\n")
			config.WriteString(fmt.Sprintf("google_application_credentials = \"%s\"\n", m.Spec.FilerBackup.Sink.GoogleCloudStorage.GoogleApplicationCredentials))
			config.WriteString(fmt.Sprintf("bucket = \"%s\"\n", m.Spec.FilerBackup.Sink.GoogleCloudStorage.Bucket))
			config.WriteString(fmt.Sprintf("directory = \"%s\"\n", m.Spec.FilerBackup.Sink.GoogleCloudStorage.Directory))
			config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", m.Spec.FilerBackup.Sink.GoogleCloudStorage.IsIncremental))
		}

		if m.Spec.FilerBackup.Sink.Azure != nil && m.Spec.FilerBackup.Sink.Azure.Enabled {
			config.WriteString("[sink.azure]\n")
			config.WriteString("enabled = true\n")
			config.WriteString(fmt.Sprintf("account_name = \"%s\"\n", m.Spec.FilerBackup.Sink.Azure.AccountName))
			config.WriteString(fmt.Sprintf("account_key = \"%s\"\n", m.Spec.FilerBackup.Sink.Azure.AccountKey))
			config.WriteString(fmt.Sprintf("container = \"%s\"\n", m.Spec.FilerBackup.Sink.Azure.Container))
			config.WriteString(fmt.Sprintf("directory = \"%s\"\n", m.Spec.FilerBackup.Sink.Azure.Directory))
			config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", m.Spec.FilerBackup.Sink.Azure.IsIncremental))
		}

		if m.Spec.FilerBackup.Sink.Backblaze != nil && m.Spec.FilerBackup.Sink.Backblaze.Enabled {
			config.WriteString("[sink.backblaze]\n")
			config.WriteString("enabled = true\n")
			config.WriteString(fmt.Sprintf("b2_account_id = \"%s\"\n", m.Spec.FilerBackup.Sink.Backblaze.B2AccountID))
			config.WriteString(fmt.Sprintf("b2_master_application_key = \"%s\"\n", m.Spec.FilerBackup.Sink.Backblaze.B2MasterApplicationKey))
			config.WriteString(fmt.Sprintf("b2_region = \"%s\"\n", m.Spec.FilerBackup.Sink.Backblaze.B2Region))
			config.WriteString(fmt.Sprintf("bucket = \"%s\"\n", m.Spec.FilerBackup.Sink.Backblaze.Bucket))
			config.WriteString(fmt.Sprintf("directory = \"%s\"\n", m.Spec.FilerBackup.Sink.Backblaze.Directory))
			config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", m.Spec.FilerBackup.Sink.Backblaze.IsIncremental))
		}
	}

	return config.String()
}
