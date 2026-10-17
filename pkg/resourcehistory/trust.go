// Package resourcehistory exposes bounded, authenticated infrastructure samples
// for the platform's admin-only Prometheus sizing reports.
package resourcehistory

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const TrustConfigMap = "mcp-kubelet-resource-trust"

// PublicCertificates rejects mixed PEM bundles so an accidentally supplied
// private key or kubeconfig can never be copied into a public ConfigMap.
func PublicCertificates(data []byte) ([]byte, error) {
	if len(data) > 256<<10 {
		return nil, errors.New("certificate bundle too large")
	}
	var result []byte
	for len(strings.TrimSpace(string(data))) > 0 {
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return nil, errors.New("public certificate PEM required")
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return nil, errors.New("invalid public certificate")
		}
		result = append(result, pem.EncodeToMemory(block)...)
		data = rest
	}
	if len(result) == 0 {
		return nil, errors.New("certificate bundle empty")
	}
	return result, nil
}

// Target is provisioned by setup from an administrator-controlled trust source.
// Node UID and address are rechecked against Kubernetes before any credential is
// sent. CertificatePEM contains only public trust anchors, never a private key.
type Target struct {
	Name           string `json:"name"`
	UID            string `json:"uid"`
	Address        string `json:"address"`
	ServerName     string `json:"server_name"`
	CertificatePEM string `json:"certificate_pem"`
}

func EncodeTargets(targets []Target) (string, error) {
	if len(targets) > 64 {
		return "", errors.New("resource history supports at most 64 nodes")
	}
	data, err := json.Marshal(targets)
	return string(data), err
}

func DecodeTargets(data string) (map[string]Target, error) {
	if len(data) > 900<<10 {
		return nil, errors.New("resource trust configuration too large")
	}
	var targets []Target
	if err := json.Unmarshal([]byte(data), &targets); err != nil || len(targets) > 64 {
		return nil, errors.New("invalid resource trust configuration")
	}
	result := make(map[string]Target, len(targets))
	for _, target := range targets {
		if target.Name == "" || target.UID == "" || target.ServerName == "" || net.ParseIP(target.Address) == nil {
			return nil, errors.New("invalid resource trust identity")
		}
		if _, duplicate := result[target.Name]; duplicate {
			return nil, errors.New("duplicate resource trust identity")
		}
		public, err := PublicCertificates([]byte(target.CertificatePEM))
		if err != nil {
			return nil, err
		}
		target.CertificatePEM = string(public)
		result[target.Name] = target
	}
	return result, nil
}

func (target Target) matches(node corev1.Node) bool {
	if target.Name != node.Name || target.UID != string(node.UID) {
		return false
	}
	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP && address.Address == target.Address {
			return true
		}
	}
	return false
}

func (target Target) httpClient() *http.Client {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(target.CertificatePEM))
	return &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs: pool, ServerName: target.ServerName, MinVersion: tls.VersionTLS12,
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
