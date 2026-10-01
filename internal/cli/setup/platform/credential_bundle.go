package platform

import (
	"bytes"
	"context"
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"gopkg.in/yaml.v3"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/platforminventory"
)

// Snapshot canonical owner objects once. A present-but-empty key is never
// recovered from the old mirror: doing so could resurrect revoked credentials.
func renderCredentialBundleClientGo() (string, error) {
	clients, err := platformKubernetesClients()
	if err != nil {
		return "", err
	}
	snapshots := map[string]map[string]string{}
	for _, set := range platforminventory.CredentialSets() {
		secret, err := clients.Clientset.CoreV1().Secrets(core.DefaultAnalyticsNamespace).Get(context.Background(), set.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		values := map[string]string{}
		for key, value := range secret.Data {
			values[key] = string(value)
		}
		snapshots[set.Name] = values
	}
	return renderCredentialBundleWithSnapshots(existingSecretDataValueClientGo, snapshots)
}

func renderCredentialBundleWithSnapshots(read analyticsSecretValueReader, snapshots map[string]map[string]string) (string, error) {
	for _, key := range []string{"API_KEYS", "JWT_SECRET", "INTERNAL_AUTH_TOKEN", "POSTGRES_DSN", "POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB", "UI_API_KEY", "INGEST_API_KEYS", "GRAFANA_ADMIN_PASSWORD", "OAUTH_PRIVATE_KEY"} {
		owner, _ := platforminventory.CredentialOwner(key)
		if values, present := snapshots[owner]; present && values[key] == "" {
			return "", fmt.Errorf("required credential %q missing from owner %q", key, owner)
		}
	}
	ownerReader := func(ns, name, key string) (string, error) {
		if name == platforminventory.LegacyCredentialSecret {
			if owner, ok := platforminventory.CredentialOwner(key); ok {
				if values, present := snapshots[owner]; present {
					return values[key], nil
				}
			}
		}
		return read(ns, name, key)
	}
	legacy, err := renderAnalyticsSecretManifestWithReader(ownerReader)
	if err != nil {
		return "", err
	}
	return splitCredentialManifest(legacy)
}

// Keep a synchronized legacy mirror while old pods, doctor and preflight still
// use it. Only allowlisted keys are copied to each new object; unknown keys
// block rendering rather than silently gaining new consumers.
func splitCredentialManifest(legacy string) (string, error) {
	var source struct {
		Metadata struct {
			Namespace string `yaml:"namespace"`
		} `yaml:"metadata"`
		StringData map[string]string `yaml:"stringData"`
	}
	if err := yaml.Unmarshal([]byte(legacy), &source); err != nil {
		return "", err
	}
	for key := range source.StringData {
		if _, ok := platforminventory.CredentialOwner(key); !ok {
			return "", fmt.Errorf("credential key %q has no declared owner", key)
		}
	}
	var out bytes.Buffer
	out.WriteString(legacy)
	for _, set := range platforminventory.CredentialSets() {
		values := map[string]string{}
		for _, key := range set.Keys {
			if value, ok := source.StringData[key]; ok {
				values[key] = value
			}
		}
		manifest := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": set.Name, "namespace": source.Metadata.Namespace, "labels": map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"}}, "type": "Opaque", "stringData": values}
		rendered, err := yaml.Marshal(manifest)
		if err != nil {
			return "", err
		}
		out.WriteString("---\n")
		out.Write(rendered)
	}
	return out.String(), nil
}
