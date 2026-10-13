package platformauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mcp-runtime/pkg/serviceutil"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultResolverCacheTTL = 45 * time.Second

type HTTPUserKeyResolver struct {
	BaseURL  string
	Token    string
	Client   *http.Client
	CacheTTL time.Duration
	mu       sync.Mutex
	cache    map[string]resolverCacheEntry
}

type resolverCacheEntry struct {
	principal Principal
	ok        bool
	expiresAt time.Time
}

func (r *HTTPUserKeyResolver) ResolveAPIKey(ctx context.Context, rawKey string) (Principal, bool, error) {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return Principal{}, false, nil
	}
	cacheKey := apiKeyCacheKey(rawKey)
	if principal, ok, found := r.cached(cacheKey); found {
		return principal, ok, nil
	}

	body, err := json.Marshal(map[string]string{"api_key": rawKey})
	if err != nil {
		return Principal{}, false, err
	}
	endpoint, err := resolveAuthURL(r.BaseURL)
	if err != nil {
		return Principal{}, false, err
	}
	// #nosec G704 -- endpoint is built from operator PLATFORM_API_URL after parseServiceBaseURL checks.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Principal{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(r.Token))
	req.Header.Set("Content-Type", "application/json")

	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	// #nosec G704 -- same validated internal resolve endpoint as NewRequest above.
	resp, err := serviceutil.DoHTTP(client, req)
	if err != nil {
		return Principal{}, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return Principal{}, false, errors.New("internal auth resolver rejected credentials")
	}
	if resp.StatusCode != http.StatusOK {
		return Principal{}, false, errors.New("internal auth resolver failed")
	}
	var result struct {
		OK        bool      `json:"ok"`
		Principal Principal `json:"principal"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Principal{}, false, err
	}
	// Cache misses only. Successful auth must revalidate so revoked keys fail immediately.
	if !result.OK {
		r.store(cacheKey, result.Principal, result.OK)
	}
	return result.Principal, result.OK, nil
}

func (r *HTTPUserKeyResolver) cached(key string) (Principal, bool, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, found := r.cache[key]
	if !found || time.Now().After(entry.expiresAt) {
		delete(r.cache, key)
		return Principal{}, false, false
	}
	return entry.principal, entry.ok, true
}

func (r *HTTPUserKeyResolver) store(key string, principal Principal, ok bool) {
	ttl := r.CacheTTL
	if ttl <= 0 {
		ttl = defaultResolverCacheTTL
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache == nil {
		r.cache = make(map[string]resolverCacheEntry)
	}
	r.cache[key] = resolverCacheEntry{principal: principal, ok: ok, expiresAt: time.Now().Add(ttl)}
}

func apiKeyCacheKey(rawKey string) string {
	sum := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(sum[:])
}
