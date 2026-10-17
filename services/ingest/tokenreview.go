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
	"net/url"
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

	// mcpServerAPIVersion is the MCPServer group/version; it must match api/v1alpha1.
	mcpServerAPIVersion = "mcpruntime.org/v1alpha1"
)

// workloadIdentity is the MCPServer generation a projected token was verified
// to belong to. It is derived from the Kubernetes API, never from the caller.
type workloadIdentity struct {
	Namespace string
	Server    string
	UID       string
}

type cachedIdentity struct {
	identity workloadIdentity
	expires  time.Time
}

// serviceAccountAuthenticator validates projected Kubernetes service account
// tokens through the TokenReview API. Only Pod-bound tokens minted for the
// configured audience and belonging to the configured ServiceAccount name (in
// any namespace) are accepted, and each is resolved through UID-checked
// controller references (Pod, ReplicaSet, Deployment, MCPServer) to the
// current MCPServer so audit attribution cannot be chosen by the caller.
type serviceAccountAuthenticator struct {
	apiURL         string
	apiBase        string
	audience       string
	serviceAccount string
	client         *http.Client
	readToken      func() (string, error)
	now            func() time.Time

	mu    sync.Mutex
	cache map[string]cachedIdentity
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
		apiBase:        "https://" + hostPort(host, port),
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
		cache: map[string]cachedIdentity{},
	}, nil
}

func hostPort(host, port string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// Authenticate reports whether token is a Pod-bound service account token for
// the configured audience and ServiceAccount name, and returns the verified
// MCPServer identity of the workload that holds it.
func (a *serviceAccountAuthenticator) Authenticate(ctx context.Context, token string) (workloadIdentity, bool) {
	if a == nil || token == "" {
		return workloadIdentity{}, false
	}
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	now := a.now()

	a.mu.Lock()
	if c, ok := a.cache[key]; ok && now.Before(c.expires) {
		a.mu.Unlock()
		return c.identity, true
	}
	a.mu.Unlock()

	id, err := a.review(ctx, token)
	if err != nil {
		log.Printf("service account token verification failed: %v", err)
		return workloadIdentity{}, false
	}

	a.mu.Lock()
	if len(a.cache) >= tokenReviewMaxCache {
		for k, c := range a.cache {
			if !now.Before(c.expires) {
				delete(a.cache, k)
			}
		}
		if len(a.cache) >= tokenReviewMaxCache {
			a.cache = map[string]cachedIdentity{}
		}
	}
	a.cache[key] = cachedIdentity{identity: id, expires: now.Add(tokenReviewCacheTTL)}
	a.mu.Unlock()
	return id, true
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
			Username string              `json:"username"`
			Extra    map[string][]string `json:"extra"`
		} `json:"user"`
		Error string `json:"error"`
	} `json:"status"`
}

var errNotAuthenticated = errors.New("token not accepted")

func (a *serviceAccountAuthenticator) review(ctx context.Context, token string) (workloadIdentity, error) {
	body, err := json.Marshal(tokenReviewRequest{
		APIVersion: "authentication.k8s.io/v1",
		Kind:       "TokenReview",
		Spec:       tokenReviewSpec{Token: token, Audiences: []string{a.audience}},
	})
	if err != nil {
		return workloadIdentity{}, err
	}
	raw, err := a.call(ctx, http.MethodPost, a.apiURL, body)
	if err != nil {
		return workloadIdentity{}, err
	}
	var out tokenReviewResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return workloadIdentity{}, fmt.Errorf("decode tokenreview: %w", err)
	}
	if !out.Status.Authenticated || out.Status.Error != "" || !containsString(out.Status.Audiences, a.audience) {
		return workloadIdentity{}, errNotAuthenticated
	}
	namespace, ok := a.allowedUser(out.Status.User.Username)
	if !ok {
		return workloadIdentity{}, errNotAuthenticated
	}
	podName := out.Status.User.Extra["authentication.kubernetes.io/pod-name"]
	podUID := out.Status.User.Extra["authentication.kubernetes.io/pod-uid"]
	if len(podName) != 1 || len(podUID) != 1 || podName[0] == "" || podUID[0] == "" {
		return workloadIdentity{}, errors.New("token is not bound to a Pod")
	}
	return a.resolveWorkload(ctx, namespace, podName[0], podUID[0])
}

// call performs one authenticated Kubernetes API request.
func (a *serviceAccountAuthenticator) call(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	own, err := a.readToken()
	if err != nil {
		return nil, fmt.Errorf("read ingest service account token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+own)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kubernetes api request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("kubernetes api returned status %d", resp.StatusCode)
	}
	return raw, nil
}

type ownerRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Controller *bool  `json:"controller"`
}

type kubeObject struct {
	Metadata struct {
		Name            string     `json:"name"`
		UID             string     `json:"uid"`
		OwnerReferences []ownerRef `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		ServiceAccountName string `json:"serviceAccountName"`
	} `json:"spec"`
}

func (a *serviceAccountAuthenticator) get(ctx context.Context, path string) (kubeObject, error) {
	var o kubeObject
	raw, err := a.call(ctx, http.MethodGet, a.apiBase+path, nil)
	if err != nil {
		return o, err
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return o, fmt.Errorf("decode %s: %w", path, err)
	}
	return o, nil
}

func (o kubeObject) controller(kind, apiVersion string) (ownerRef, error) {
	for _, r := range o.Metadata.OwnerReferences {
		if r.Controller != nil && *r.Controller && r.Kind == kind && r.APIVersion == apiVersion && r.UID != "" && r.Name != "" {
			return r, nil
		}
	}
	return ownerRef{}, fmt.Errorf("missing %s controller", kind)
}

// resolveWorkload follows UID-checked controller references from the token's
// bound Pod to the current MCPServer, mirroring platform session identity.
func (a *serviceAccountAuthenticator) resolveWorkload(ctx context.Context, namespace, podName, podUID string) (workloadIdentity, error) {
	ns := url.PathEscape(namespace)
	pod, err := a.get(ctx, "/api/v1/namespaces/"+ns+"/pods/"+url.PathEscape(podName))
	if err != nil {
		return workloadIdentity{}, err
	}
	if pod.Metadata.UID != podUID || pod.Spec.ServiceAccountName != a.serviceAccount {
		return workloadIdentity{}, errors.New("Pod binding mismatch")
	}
	rsRef, err := pod.controller("ReplicaSet", "apps/v1")
	if err != nil {
		return workloadIdentity{}, err
	}
	rs, err := a.get(ctx, "/apis/apps/v1/namespaces/"+ns+"/replicasets/"+url.PathEscape(rsRef.Name))
	if err != nil {
		return workloadIdentity{}, err
	}
	if rs.Metadata.UID != rsRef.UID {
		return workloadIdentity{}, errors.New("ReplicaSet generation mismatch")
	}
	depRef, err := rs.controller("Deployment", "apps/v1")
	if err != nil {
		return workloadIdentity{}, err
	}
	dep, err := a.get(ctx, "/apis/apps/v1/namespaces/"+ns+"/deployments/"+url.PathEscape(depRef.Name))
	if err != nil {
		return workloadIdentity{}, err
	}
	if dep.Metadata.UID != depRef.UID {
		return workloadIdentity{}, errors.New("Deployment generation mismatch")
	}
	srvRef, err := dep.controller("MCPServer", mcpServerAPIVersion)
	if err != nil {
		return workloadIdentity{}, err
	}
	srv, err := a.get(ctx, "/apis/"+mcpServerAPIVersion+"/namespaces/"+ns+"/mcpservers/"+url.PathEscape(srvRef.Name))
	if err != nil {
		return workloadIdentity{}, err
	}
	if srv.Metadata.UID != srvRef.UID || srv.Metadata.Name != srvRef.Name {
		return workloadIdentity{}, errors.New("MCPServer generation mismatch")
	}
	if depRef.Name != srv.Metadata.Name && depRef.Name != srv.Metadata.Name+"-canary" {
		return workloadIdentity{}, errors.New("unexpected server deployment")
	}
	return workloadIdentity{Namespace: namespace, Server: srv.Metadata.Name, UID: srv.Metadata.UID}, nil
}

// allowedUser accepts system:serviceaccount:<namespace>:<serviceAccount> for
// the configured ServiceAccount name in any namespace and returns the namespace.
func (a *serviceAccountAuthenticator) allowedUser(username string) (string, bool) {
	parts := strings.Split(username, ":")
	if len(parts) == 4 && parts[0] == "system" && parts[1] == "serviceaccount" &&
		parts[2] != "" && parts[3] == a.serviceAccount {
		return parts[2], true
	}
	return "", false
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
