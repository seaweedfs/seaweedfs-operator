package backup

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// GCSCredentialExtractor implements CredentialExtractor for GCS credentials
type GCSCredentialExtractor struct {
	secretRef *seaweedv1.GoogleCloudStorageCredentialsSecretRef
}

func (e *GCSCredentialExtractor) GetSecretRef() *string {
	if e.secretRef != nil {
		return &e.secretRef.Name
	}
	return nil
}

func (e *GCSCredentialExtractor) GetMapping() interface{} {
	if e.secretRef != nil {
		return e.secretRef.Mapping
	}
	return nil
}

// ExtractGCSCredentials extracts GCS credentials from secret or uses provided values
func ExtractGCSCredentials(ctx context.Context, gcsConfig *seaweedv1.GoogleCloudStorageSinkConfig, namespace string, log *zap.SugaredLogger, secretGetter SecretGetter) string {
	googleApplicationCredentials := gcsConfig.GoogleApplicationCredentials

	if gcsConfig.GoogleCloudStorageCredentialsSecretRef != nil {
		extractor := &GCSCredentialExtractor{secretRef: gcsConfig.GoogleCloudStorageCredentialsSecretRef}
		secret, err := extractCredentialsFromSecret(ctx, extractor, namespace, log, secretGetter)
		if err == nil && secret != nil {
			mapping := gcsConfig.GoogleCloudStorageCredentialsSecretRef.Mapping

			googleApplicationCredentialsKey := mapping.GoogleApplicationCredentials
			if googleApplicationCredentialsKey == "" {
				googleApplicationCredentialsKey = "googleApplicationCredentials"
			}

			if creds, exists := secret[googleApplicationCredentialsKey]; exists {
				googleApplicationCredentials = creds
			} else {
				log.Warnw("secret key not found in secret", "secret", gcsConfig.GoogleCloudStorageCredentialsSecretRef.Name, "mapping", googleApplicationCredentialsKey)
			}
		}
	}

	return googleApplicationCredentials
}

// GenerateGCSSinkConfig generates configuration for GCS sink
func GenerateGCSSinkConfig(ctx context.Context, config *strings.Builder, gcsConfig *seaweedv1.GoogleCloudStorageSinkConfig, namespace string, log *zap.SugaredLogger, secretGetter SecretGetter) {
	googleApplicationCredentials := ExtractGCSCredentials(ctx, gcsConfig, namespace, log, secretGetter)

	config.WriteString("[sink.google_cloud_storage]\n")
	config.WriteString("enabled = true\n")
	config.WriteString(fmt.Sprintf("google_application_credentials = \"%s\"\n", googleApplicationCredentials))
	config.WriteString(fmt.Sprintf("bucket = \"%s\"\n", gcsConfig.Bucket))
	config.WriteString(fmt.Sprintf("directory = \"%s\"\n", gcsConfig.Directory))
	config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", gcsConfig.IsIncremental))
}
