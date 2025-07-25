package backup

import (
	"context"
)

// ControllerSecretGetter implements SecretGetter by wrapping the controller's getSecret method
type ControllerSecretGetter struct {
	getSecretFunc func(ctx context.Context, secretName string, namespace string) (map[string]string, error)
}

// NewControllerSecretGetter creates a new ControllerSecretGetter
func NewControllerSecretGetter(getSecretFunc func(ctx context.Context, secretName string, namespace string) (map[string]string, error)) *ControllerSecretGetter {
	return &ControllerSecretGetter{
		getSecretFunc: getSecretFunc,
	}
}

// GetSecret implements SecretGetter interface
func (g *ControllerSecretGetter) GetSecret(ctx context.Context, secretName string, namespace string) (map[string]string, error) {
	return g.getSecretFunc(ctx, secretName, namespace)
}
