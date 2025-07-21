package backup

import (
	"context"

	"github.com/go-logr/logr"
)

// SecretGetter defines the interface for retrieving secrets
type SecretGetter interface {
	GetSecret(ctx context.Context, secretName string, namespace string) (map[string]string, error)
}

// CredentialExtractor defines the interface for extracting credentials from secrets
type CredentialExtractor interface {
	GetSecretRef() *string
	GetMapping() interface{}
}

// extractCredentialsFromSecret retrieves credentials from a Kubernetes secret
func extractCredentialsFromSecret(ctx context.Context, extractor CredentialExtractor, namespace string, log logr.Logger, secretGetter SecretGetter) (map[string]string, error) {
	secretRef := extractor.GetSecretRef()
	if secretRef == nil || *secretRef == "" {
		return nil, nil
	}

	log.Info("Getting credentials from secret", "secret", *secretRef)
	secret, err := secretGetter.GetSecret(ctx, *secretRef, namespace)
	if err != nil {
		log.Error(err, "Error getting credentials from secret", "secret", *secretRef)
		return nil, err
	}

	return secret, nil
}
