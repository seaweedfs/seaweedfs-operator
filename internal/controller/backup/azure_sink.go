package backup

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// AzureCredentialExtractor implements CredentialExtractor for Azure credentials
type AzureCredentialExtractor struct {
	secretRef *seaweedv1.AzureCredentialsSecretRef
}

func (e *AzureCredentialExtractor) GetSecretRef() *string {
	if e.secretRef != nil {
		return &e.secretRef.Name
	}
	return nil
}

func (e *AzureCredentialExtractor) GetMapping() interface{} {
	if e.secretRef != nil {
		return e.secretRef.Mapping
	}
	return nil
}

// ExtractAzureCredentials extracts Azure credentials from secret or uses provided values
func ExtractAzureCredentials(ctx context.Context, azureConfig *seaweedv1.AzureSinkConfig, namespace string, log logr.Logger, secretGetter SecretGetter) (string, string) {
	accountName := azureConfig.AccountName
	accountKey := azureConfig.AccountKey

	if azureConfig.AzureCredentialsSecretRef != nil {
		extractor := &AzureCredentialExtractor{secretRef: azureConfig.AzureCredentialsSecretRef}
		secret, err := extractCredentialsFromSecret(ctx, extractor, namespace, log, secretGetter)
		if err == nil && secret != nil {
			mapping := azureConfig.AzureCredentialsSecretRef.Mapping

			accountNameKey := mapping.AccountName

			if accountNameKey == "" {
				accountNameKey = "accountName"
			} else if name, exists := secret[accountNameKey]; exists {
				accountName = name
			} else {
				log.Info("Secret key not found in secret", "secret", azureConfig.AzureCredentialsSecretRef.Name, "mapping", accountNameKey)
			}

			accountKeyKey := mapping.AccountKey

			if accountKeyKey == "" {
				accountKeyKey = "accountKey"
			} else if key, exists := secret[accountKeyKey]; exists {
				accountKey = key
			} else {
				log.Info("Secret key not found in secret", "secret", azureConfig.AzureCredentialsSecretRef.Name, "mapping", accountKeyKey)
			}
		}
	}

	return accountName, accountKey
}

// GenerateAzureSinkConfig generates configuration for Azure sink
func GenerateAzureSinkConfig(ctx context.Context, config *strings.Builder, azureConfig *seaweedv1.AzureSinkConfig, namespace string, log logr.Logger, secretGetter SecretGetter) {
	accountName, accountKey := ExtractAzureCredentials(ctx, azureConfig, namespace, log, secretGetter)

	config.WriteString("[sink.azure]\n")
	config.WriteString("enabled = true\n")
	config.WriteString(fmt.Sprintf("account_name = \"%s\"\n", accountName))
	config.WriteString(fmt.Sprintf("account_key = \"%s\"\n", accountKey))
	config.WriteString(fmt.Sprintf("container = \"%s\"\n", azureConfig.Container))
	config.WriteString(fmt.Sprintf("directory = \"%s\"\n", azureConfig.Directory))
	config.WriteString(fmt.Sprintf("is_incremental = %t\n\n", azureConfig.IsIncremental))
}
