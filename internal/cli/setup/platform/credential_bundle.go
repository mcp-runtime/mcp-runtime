package platform

import (
	"bytes"
	"context"
	"fmt"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"gopkg.in/yaml.v3"
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
		secret, err := clients.Clientset.CoreV1().Secrets(set.Namespace()).Get(context.Background(), set.Name, metav1.GetOptions{})
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
		values, ownerPresent := snapshots[owner]
		if !ownerPresent {
			continue
		}
		value, keyPresent := values[key]
		if keyPresent && value == "" {
			return "", fmt.Errorf("required credential %q missing from owner %q", key, owner)
		}
	}
	ownerReader := func(ns, name, key string) (string, error) {
		if values, present := snapshots[name]; present {
			if value, ok := values[key]; ok {
				return value, nil
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

// splitCredentialManifest places each declared key into the consumer Secret
// for that key's owner. The input document is only a value bag.
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
	for i, set := range platforminventory.CredentialSets() {
		if i > 0 {
			out.WriteString("---\n")
		}
		values := map[string]string{}
		for _, key := range set.Keys {
			if value, ok := source.StringData[key]; ok {
				values[key] = value
			}
		}
		manifest := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": set.Name, "namespace": set.Namespace(), "labels": map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"}}, "type": "Opaque", "stringData": values}
		rendered, err := yaml.Marshal(manifest)
		if err != nil {
			return "", err
		}
		out.WriteString("---\n")
		out.Write(rendered)
	}
	return out.String(), nil
}
