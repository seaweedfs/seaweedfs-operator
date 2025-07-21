package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

func (r *SeaweedReconciler) createFilerBackupConfigMap(m *seaweedv1.Seaweed) *corev1.ConfigMap {
	labels := labelsForFilerBackup(m.Name)

	config := ""
	if m.Spec.FilerBackup.Config != nil {
		config = *m.Spec.FilerBackup.Config
	} else {
		config = r.generateBackupConfig(m)
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

func (r *SeaweedReconciler) generateBackupConfig(m *seaweedv1.Seaweed) string {
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
			awsAccessKeyID := m.Spec.FilerBackup.Sink.S3.AWSAccessKeyID
			awsSecretAccessKey := m.Spec.FilerBackup.Sink.S3.AWSSecretAccessKey

			// Get credentials from secret if specified
			if m.Spec.FilerBackup.Sink.S3.AWSCredentialsSecretRef != nil && m.Spec.FilerBackup.Sink.S3.AWSCredentialsSecretRef.Name != "" {
				secret := &corev1.Secret{}
				err := r.Get(context.Background(), client.ObjectKey{
					Namespace: m.Namespace,
					Name:      m.Spec.FilerBackup.Sink.S3.AWSCredentialsSecretRef.Name,
				}, secret)
				if err == nil {
					mapping := m.Spec.FilerBackup.Sink.S3.AWSCredentialsSecretRef.Mapping
					if accessKey, exists := secret.Data[mapping.AWSAccessKeyID]; exists {
						awsAccessKeyID = string(accessKey)
					}
					if secretKey, exists := secret.Data[mapping.AWSSecretAccessKey]; exists {
						awsSecretAccessKey = string(secretKey)
					}
				}
			}

			config.WriteString("[sink.s3]\n")
			config.WriteString("enabled = true\n")
			config.WriteString(fmt.Sprintf("aws_access_key_id = \"%s\"\n", awsAccessKeyID))
			config.WriteString(fmt.Sprintf("aws_secret_access_key = \"%s\"\n", awsSecretAccessKey))
			config.WriteString(fmt.Sprintf("region = \"%s\"\n", m.Spec.FilerBackup.Sink.S3.Region))
			config.WriteString(fmt.Sprintf("bucket = \"%s\"\n", m.Spec.FilerBackup.Sink.S3.Bucket))
			config.WriteString(fmt.Sprintf("directory = \"%s\"\n", m.Spec.FilerBackup.Sink.S3.Directory))
			config.WriteString(fmt.Sprintf("endpoint = \"%s\"\n", m.Spec.FilerBackup.Sink.S3.Endpoint))
			config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", m.Spec.FilerBackup.Sink.S3.IsIncremental))
		}

		if m.Spec.FilerBackup.Sink.GoogleCloudStorage != nil && m.Spec.FilerBackup.Sink.GoogleCloudStorage.Enabled {
			googleApplicationCredentials := m.Spec.FilerBackup.Sink.GoogleCloudStorage.GoogleApplicationCredentials

			// Get credentials from secret if specified
			if m.Spec.FilerBackup.Sink.GoogleCloudStorage.GoogleCloudStorageCredentialsSecretRef != nil && m.Spec.FilerBackup.Sink.GoogleCloudStorage.GoogleCloudStorageCredentialsSecretRef.Name != "" {
				secret := &corev1.Secret{}
				err := r.Get(context.Background(), client.ObjectKey{
					Namespace: m.Namespace,
					Name:      m.Spec.FilerBackup.Sink.GoogleCloudStorage.GoogleCloudStorageCredentialsSecretRef.Name,
				}, secret)
				if err == nil {
					mapping := m.Spec.FilerBackup.Sink.GoogleCloudStorage.GoogleCloudStorageCredentialsSecretRef.Mapping
					if creds, exists := secret.Data[mapping.GoogleApplicationCredentials]; exists {
						googleApplicationCredentials = string(creds)
					}
				}
			}

			config.WriteString("[sink.google_cloud_storage]\n")
			config.WriteString("enabled = true\n")
			config.WriteString(fmt.Sprintf("google_application_credentials = \"%s\"\n", googleApplicationCredentials))
			config.WriteString(fmt.Sprintf("bucket = \"%s\"\n", m.Spec.FilerBackup.Sink.GoogleCloudStorage.Bucket))
			config.WriteString(fmt.Sprintf("directory = \"%s\"\n", m.Spec.FilerBackup.Sink.GoogleCloudStorage.Directory))
			config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", m.Spec.FilerBackup.Sink.GoogleCloudStorage.IsIncremental))
		}

		if m.Spec.FilerBackup.Sink.Azure != nil && m.Spec.FilerBackup.Sink.Azure.Enabled {
			accountName := m.Spec.FilerBackup.Sink.Azure.AccountName
			accountKey := m.Spec.FilerBackup.Sink.Azure.AccountKey

			// Get credentials from secret if specified
			if m.Spec.FilerBackup.Sink.Azure.AzureCredentialsSecretRef != nil && m.Spec.FilerBackup.Sink.Azure.AzureCredentialsSecretRef.Name != "" {
				secret := &corev1.Secret{}
				err := r.Get(context.Background(), client.ObjectKey{
					Namespace: m.Namespace,
					Name:      m.Spec.FilerBackup.Sink.Azure.AzureCredentialsSecretRef.Name,
				}, secret)
				if err == nil {
					mapping := m.Spec.FilerBackup.Sink.Azure.AzureCredentialsSecretRef.Mapping
					if name, exists := secret.Data[mapping.AccountName]; exists {
						accountName = string(name)
					}
					if key, exists := secret.Data[mapping.AccountKey]; exists {
						accountKey = string(key)
					}
				}
			}

			config.WriteString("[sink.azure]\n")
			config.WriteString("enabled = true\n")
			config.WriteString(fmt.Sprintf("account_name = \"%s\"\n", accountName))
			config.WriteString(fmt.Sprintf("account_key = \"%s\"\n", accountKey))
			config.WriteString(fmt.Sprintf("container = \"%s\"\n", m.Spec.FilerBackup.Sink.Azure.Container))
			config.WriteString(fmt.Sprintf("directory = \"%s\"\n", m.Spec.FilerBackup.Sink.Azure.Directory))
			config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", m.Spec.FilerBackup.Sink.Azure.IsIncremental))
		}

		if m.Spec.FilerBackup.Sink.Backblaze != nil && m.Spec.FilerBackup.Sink.Backblaze.Enabled {
			b2AccountID := m.Spec.FilerBackup.Sink.Backblaze.B2AccountID
			b2MasterApplicationKey := m.Spec.FilerBackup.Sink.Backblaze.B2MasterApplicationKey

			// Get credentials from secret if specified
			if m.Spec.FilerBackup.Sink.Backblaze.BackblazeCredentialsSecretRef != nil && m.Spec.FilerBackup.Sink.Backblaze.BackblazeCredentialsSecretRef.Name != "" {
				secret := &corev1.Secret{}
				err := r.Get(context.Background(), client.ObjectKey{
					Namespace: m.Namespace,
					Name:      m.Spec.FilerBackup.Sink.Backblaze.BackblazeCredentialsSecretRef.Name,
				}, secret)
				if err == nil {
					mapping := m.Spec.FilerBackup.Sink.Backblaze.BackblazeCredentialsSecretRef.Mapping
					if accountID, exists := secret.Data[mapping.B2AccountID]; exists {
						b2AccountID = string(accountID)
					}
					if masterKey, exists := secret.Data[mapping.B2MasterApplicationKey]; exists {
						b2MasterApplicationKey = string(masterKey)
					}
				}
			}

			config.WriteString("[sink.backblaze]\n")
			config.WriteString("enabled = true\n")
			config.WriteString(fmt.Sprintf("b2_account_id = \"%s\"\n", b2AccountID))
			config.WriteString(fmt.Sprintf("b2_master_application_key = \"%s\"\n", b2MasterApplicationKey))
			config.WriteString(fmt.Sprintf("b2_region = \"%s\"\n", m.Spec.FilerBackup.Sink.Backblaze.B2Region))
			config.WriteString(fmt.Sprintf("bucket = \"%s\"\n", m.Spec.FilerBackup.Sink.Backblaze.Bucket))
			config.WriteString(fmt.Sprintf("directory = \"%s\"\n", m.Spec.FilerBackup.Sink.Backblaze.Directory))
			config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", m.Spec.FilerBackup.Sink.Backblaze.IsIncremental))
		}
	}

	return config.String()
}
