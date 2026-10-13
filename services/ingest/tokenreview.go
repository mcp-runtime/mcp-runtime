package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	inClusterTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	inClusterCAPath    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"

	// tokenReviewCacheTTL bounds how long a successful review is reused so a
	// busy gateway does not cost one API server call per event. Revoked tokens
	// stay accepted for at most this long.
	tokenReviewCacheTTL = 60 * time.Second
	tokenReviewMaxCache = 4096
)

// serviceAccountAuthenticator validates projected Kubernetes service account
// tokens through the TokenReview API. Only tokens minted for the configured
// audience and belonging to the configured ServiceAccount name (in any
// namespace) are accepted.
type serviceAccountAuthenticator struct {
	apiURL         string
	audience       string
	serviceAccount string
	client         *http.Client
	readToken      func() (string, error)
	now            func() time.Time

	mu    sync.Mutex
	cache map[string]time.Time
}

func newInClusterServiceAccountAuthenticator(audience, serviceAccount string) (*serviceAccountAuthenticator, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("KUBERNETES_SERVICE_HOST/PORT not set; not running in a cluster")
	}
	caPEM, err := os.ReadFile(inClusterCAPath)
	if err != nil {
		return nil, fmt.Errorf("read cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("cluster CA contains no certificates")
	}
	return &serviceAccountAuthenticator{
		apiURL:         "https://" + hostPort(host, port) + "/apis/authentication.k8s.io/v1/tokenreviews",
		audience:       audience,
		serviceAccount: serviceAccount,
		client: &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		},
		readToken: func() (string, error) {
			b, err := os.ReadFile(inClusterTokenPath)
			return strings.TrimSpace(string(b)), err
		},
		now:   time.Now,
		cache: map[string]time.Time{},
	}, nil
}

func hostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// Authenticate reports whether token is a valid service account token for the
// configured audience and ServiceAccount name.
func (a *serviceAccountAuthenticator) Authenticate(ctx context.Context, token string) bool {
	if a == nil || token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	now := a.now()

	a.mu.Lock()
	if exp, ok := a.cache[key]; ok && now.Before(exp) {
		a.mu.Unlock()
		return true
	}
	a.mu.Unlock()

	ok, err := a.review(ctx, token)
	if err != nil {
		log.Printf("service account token review failed: %v", err)
		return false
	}
	if !ok {
		return false
	}

	a.mu.Lock()
	if len(a.cache) >= tokenReviewMaxCache {
		for k, exp := range a.cache {
			if !now.Before(exp) {
				delete(a.cache, k)
			}
		}
		if len(a.cache) >= tokenReviewMaxCache {
			a.cache = map[string]time.Time{}
		}
	}
	a.cache[key] = now.Add(tokenReviewCacheTTL)
	a.mu.Unlock()
	return true
}

type tokenReviewRequest struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Spec       tokenReviewSpec `json:"spec"`
}

type tokenReviewSpec struct {
	Token     string   `json:"token"`
	Audiences []string `json:"audiences"`
}

type tokenReviewResponse struct {
	Status struct {
		Authenticated bool     `json:"authenticated"`
		Audiences     []string `json:"audiences"`
		User          struct {
			Username string `json:"username"`
		} `json:"user"`
		Error string `json:"error"`
	} `json:"status"`
}

func (a *serviceAccountAuthenticator) review(ctx context.Context, token string) (bool, error) {
	body, err := json.Marshal(tokenReviewRequest{
		APIVersion: "authentication.k8s.io/v1",
		Kind:       "TokenReview",
		Spec:       tokenReviewSpec{Token: token, Audiences: []string{a.audience}},
	})
	if err != nil {
		return false, err
	}
	own, err := a.readToken()
	if err != nil {
		return false, fmt.Errorf("read ingest service account token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+own)
	resp, err := a.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("tokenreview request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return false, fmt.Errorf("tokenreview returned status %d", resp.StatusCode)
	}
	var out tokenReviewResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, fmt.Errorf("decode tokenreview: %w", err)
	}
	if !out.Status.Authenticated {
		return false, nil
	}
	if !containsString(out.Status.Audiences, a.audience) {
		return false, nil
	}
	return a.allowedUser(out.Status.User.Username), nil
}

// allowedUser accepts system:serviceaccount:<namespace>:<serviceAccount> for
// the configured ServiceAccount name in any namespace.
func (a *serviceAccountAuthenticator) allowedUser(username string) bool {
	parts := strings.Split(username, ":")
	return len(parts) == 4 && parts[0] == "system" && parts[1] == "serviceaccount" &&
		parts[2] != "" && parts[3] == a.serviceAccount
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
