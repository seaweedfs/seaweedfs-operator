package backup

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// BackblazeCredentialExtractor implements CredentialExtractor for Backblaze credentials
type BackblazeCredentialExtractor struct {
	secretRef *seaweedv1.BackblazeCredentialsSecretRef
}

func (e *BackblazeCredentialExtractor) GetSecretRef() *string {
	if e.secretRef != nil {
		return &e.secretRef.Name
	}
	return nil
}

func (e *BackblazeCredentialExtractor) GetMapping() interface{} {
	if e.secretRef != nil {
		return e.secretRef.Mapping
	}
	return nil
}

// ExtractBackblazeCredentials extracts Backblaze credentials from secret or uses provided values
func ExtractBackblazeCredentials(ctx context.Context, backblazeConfig *seaweedv1.BackblazeSinkConfig, namespace string, log logr.Logger, secretGetter SecretGetter) (string, string) {
	b2AccountID := backblazeConfig.B2AccountID
	b2MasterApplicationKey := backblazeConfig.B2MasterApplicationKey

	if backblazeConfig.BackblazeCredentialsSecretRef != nil {
		extractor := &BackblazeCredentialExtractor{secretRef: backblazeConfig.BackblazeCredentialsSecretRef}
		secret, err := extractCredentialsFromSecret(ctx, extractor, namespace, log, secretGetter)
		if err == nil && secret != nil {
			mapping := backblazeConfig.BackblazeCredentialsSecretRef.Mapping
			if accountID, exists := secret[mapping.B2AccountID]; exists {
				b2AccountID = accountID
			}
			if masterKey, exists := secret[mapping.B2MasterApplicationKey]; exists {
				b2MasterApplicationKey = masterKey
			}
		}
	}

	return b2AccountID, b2MasterApplicationKey
}

// GenerateBackblazeSinkConfig generates configuration for Backblaze sink
func GenerateBackblazeSinkConfig(ctx context.Context, config *strings.Builder, backblazeConfig *seaweedv1.BackblazeSinkConfig, namespace string, log logr.Logger, secretGetter SecretGetter) {
	b2AccountID, b2MasterApplicationKey := ExtractBackblazeCredentials(ctx, backblazeConfig, namespace, log, secretGetter)

	config.WriteString("[sink.backblaze]\n")
	config.WriteString("enabled = true\n")
	config.WriteString(fmt.Sprintf("b2_account_id = \"%s\"\n", b2AccountID))
	config.WriteString(fmt.Sprintf("b2_master_application_key = \"%s\"\n", b2MasterApplicationKey))
	config.WriteString(fmt.Sprintf("b2_region = \"%s\"\n", backblazeConfig.B2Region))
	config.WriteString(fmt.Sprintf("bucket = \"%s\"\n", backblazeConfig.Bucket))
	config.WriteString(fmt.Sprintf("directory = \"%s\"\n", backblazeConfig.Directory))
	config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", backblazeConfig.IsIncremental))
}
