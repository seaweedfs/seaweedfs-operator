package backup

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// S3CredentialExtractor implements CredentialExtractor for S3 credentials
type S3CredentialExtractor struct {
	secretRef *seaweedv1.AWSCredentialsSecretRef
}

func (e *S3CredentialExtractor) GetSecretRef() *string {
	if e.secretRef != nil {
		return &e.secretRef.Name
	}
	return nil
}

func (e *S3CredentialExtractor) GetMapping() interface{} {
	if e.secretRef != nil {
		return e.secretRef.Mapping
	}
	return nil
}

// ExtractS3Credentials extracts S3 credentials from secret or uses provided values
func ExtractS3Credentials(ctx context.Context, s3Config *seaweedv1.S3SinkConfig, namespace string, log logr.Logger, secretGetter SecretGetter) (string, string) {
	awsAccessKeyID := s3Config.AWSAccessKeyID
	awsSecretAccessKey := s3Config.AWSSecretAccessKey

	if s3Config.AWSCredentialsSecretRef != nil {
		extractor := &S3CredentialExtractor{secretRef: s3Config.AWSCredentialsSecretRef}
		secret, err := extractCredentialsFromSecret(ctx, extractor, namespace, log, secretGetter)
		if err == nil && secret != nil {
			mapping := s3Config.AWSCredentialsSecretRef.Mapping

			secretAccessKeyKey := mapping.AWSSecretAccessKey
			if secretAccessKeyKey == "" {
				secretAccessKeyKey = "awsSecretAccessKey"
			}

			if accessKey, exists := secret[secretAccessKeyKey]; exists {
				awsSecretAccessKey = accessKey
			} else if mapping.AWSSecretAccessKey != "" {
				log.Info("Secret key not found in secret", "secret", s3Config.AWSCredentialsSecretRef.Name, "mapping", secretAccessKeyKey)
			}

			accessKeyIDKey := mapping.AWSAccessKeyID
			if accessKeyIDKey == "" {
				accessKeyIDKey = "awsAccessKeyID"
			}

			if accessKey, exists := secret[accessKeyIDKey]; exists {
				awsAccessKeyID = accessKey
			} else if mapping.AWSAccessKeyID != "" {
				log.Info("Secret key not found in secret", "secret", s3Config.AWSCredentialsSecretRef.Name, "mapping", accessKeyIDKey)
			}
		}
	}

	return awsAccessKeyID, awsSecretAccessKey
}

// GenerateS3SinkConfig generates configuration for S3 sink
func GenerateS3SinkConfig(ctx context.Context, config *strings.Builder, s3Config *seaweedv1.S3SinkConfig, namespace string, log logr.Logger, secretGetter SecretGetter) {
	awsAccessKeyID, awsSecretAccessKey := ExtractS3Credentials(ctx, s3Config, namespace, log, secretGetter)

	config.WriteString("[sink.s3]\n")
	config.WriteString("enabled = true\n")
	config.WriteString(fmt.Sprintf("aws_access_key_id = \"%s\"\n", awsAccessKeyID))
	config.WriteString(fmt.Sprintf("aws_secret_access_key = \"%s\"\n", awsSecretAccessKey))
	config.WriteString(fmt.Sprintf("region = \"%s\"\n", s3Config.Region))
	config.WriteString(fmt.Sprintf("bucket = \"%s\"\n", s3Config.Bucket))
	config.WriteString(fmt.Sprintf("directory = \"%s\"\n", s3Config.Directory))
	config.WriteString(fmt.Sprintf("endpoint = \"%s\"\n", s3Config.Endpoint))
	config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", s3Config.IsIncremental))
}
