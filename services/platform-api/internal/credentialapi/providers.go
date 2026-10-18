package credentialapi

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	cb "mcp-runtime/pkg/credentialbroker"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type providerFunc func(context.Context, cb.Source) (cb.Value, error)

func (f providerFunc) Read(ctx context.Context, s cb.Source) (cb.Value, error) { return f(ctx, s) }

// Providers are process-owned; source metadata cannot choose endpoints or
// supply provider credentials. Reads use ambient workload identity. No provider
// error text or response body escapes this package.
// Providers returns the reader Registry the broker resolves through. Only the
// kubernetes provider is enabled (cb.ProviderSupported); cloud readers live in
// cloud_readers.go and are intentionally not registered.
//
// Readers are process-owned; source metadata cannot choose endpoints or supply
// provider credentials. No provider error text escapes this package.
func Providers(kube client.Client) cb.Registry {
	return providers(kube)
}

func providers(kube client.Client) cb.Registry {
	registry := cb.Registry{}
	if err := registry.Register("kubernetes", kubernetesReader(kube)); err != nil {
		panic(err) // kubernetes is always a supported kind; unreachable.
	}
	return registry
}

func kubernetesReader(kube client.Client) cb.Provider {
	return providerFunc(func(ctx context.Context, s cb.Source) (cb.Value, error) {
		if kube == nil {
			return cb.Value{}, cb.ErrUnavailable
		}
		secret := &corev1.Secret{}
		if err := kube.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: s.Resource}, secret); err != nil {
			return cb.Value{}, cb.ErrUnavailable
		}
		return checked(secret.Data[s.Key], secret.ResourceVersion)
	})
}

func checked(value []byte, version string) (cb.Value, error) {
	if !cb.ValidValue(value) {
		return cb.Value{}, cb.ErrUnavailable
	}
	return cb.Value{Bytes: value, Version: version}, nil
}
