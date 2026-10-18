// Package credentialapi owns metadata management and private, workload-bound
// secret delivery. The resolution endpoint never authenticates human/API keys.
package credentialapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	mcpv1 "mcp-runtime/api/v1alpha1"
	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Store interface {
	ListCredentials(context.Context, platformauth.Principal, string, string) ([]json.RawMessage, error)
	PutCredential(context.Context, platformauth.Principal, *cb.Source, *cb.Binding) (json.RawMessage, error)
	DeleteCredential(context.Context, platformauth.Principal, string, string, string, int64) error
	// UseCredentials calls authorize before and again after fetch. fetch (provider
	// I/O) must run without holding shared locks; the value is returned only if
	// the second authorize, run after re-reading current state, still allows it.
	UseCredentials(ctx context.Context, server sessionstate.Server, request cb.ResolutionRequest, authorize func(sessionstate.Session, []cb.Binding, []cb.Source) error, fetch func(sessionstate.Session, []cb.Binding, []cb.Source) (cb.Delivery, error)) (cb.Delivery, error)
}

type Handler struct {
	Store        Store
	Kube         client.Client
	Authenticate func(context.Context, string) (sessionstate.Server, error)
	Providers    map[string]cb.Provider
	// Limits bounds resolution requests per source address (before TokenReview)
	// and per server, and briefly remembers provider failures. Nil uses a
	// process-wide default.
	Limits *Limits
}

func (h Handler) Management(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := platformauth.FromContext(r.Context())
	if !ok {
		respondError(w, cb.ErrDenied)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, cb.ManagementPath+"/"), "/")
	if len(parts) < 2 || len(parts) > 3 || !cb.NameValid(parts[0]) || (parts[1] != "sources" && parts[1] != "bindings") || (len(parts) == 3 && !cb.NameValid(parts[2])) {
		http.NotFound(w, r)
		return
	}
	if h.Store == nil {
		respondError(w, cb.ErrUnavailable)
		return
	}
	ns, kind := parts[0], parts[1]
	name := ""
	if len(parts) == 3 {
		name = parts[2]
	}
	switch r.Method {
	case http.MethodGet:
		items, err := h.Store.ListCredentials(r.Context(), p, ns, kind)
		if err != nil {
			respondError(w, err)
			return
		}
		if name != "" {
			for _, item := range items {
				var meta struct {
					Name string `json:"name"`
				}
				if json.Unmarshal(item, &meta) == nil && meta.Name == name {
					respond(w, item)
					return
				}
			}
			respondError(w, cb.ErrNotFound)
			return
		}
		respond(w, items)
	case http.MethodPut:
		if name == "" {
			http.Error(w, "item name required", http.StatusBadRequest)
			return
		}
		var source *cb.Source
		var binding *cb.Binding
		if kind == "sources" {
			source = &cb.Source{}
			if !decode(w, r, source) {
				return
			}
			if source.Namespace != ns || source.Name != name || source.Validate() != nil {
				http.Error(w, "invalid source metadata", http.StatusBadRequest)
				return
			}
		}
		if kind == "bindings" {
			binding = &cb.Binding{}
			if !decode(w, r, binding) {
				return
			}
			if binding.Namespace != ns || binding.Name != name || binding.Validate() != nil {
				http.Error(w, "invalid binding metadata", http.StatusBadRequest)
				return
			}
			if err := h.checkBinding(r.Context(), *binding); err != nil {
				respondError(w, err)
				return
			}
		}
		result, err := h.Store.PutCredential(r.Context(), p, source, binding)
		if err != nil {
			respondError(w, err)
			return
		}
		respond(w, result)
	case http.MethodDelete:
		revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
		if name == "" || err != nil || revision < 1 {
			http.Error(w, "exact revision required", http.StatusBadRequest)
			return
		}
		if err = h.Store.DeleteCredential(r.Context(), p, ns, kind, name, revision); err != nil {
			respondError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h Handler) checkBinding(ctx context.Context, b cb.Binding) error {
	if h.Kube == nil {
		return cb.ErrUnavailable
	}
	server := &mcpv1.MCPServer{}
	if err := h.Kube.Get(ctx, types.NamespacedName{Namespace: b.Namespace, Name: b.Server}, server); err != nil {
		return cb.ErrDenied
	}
	if server.Spec.Gateway != nil && server.Spec.Gateway.UpstreamURL != "" && !cb.LocalUpstream(server.Spec.Gateway.UpstreamURL, int(server.Spec.Port)) {
		return cb.ErrDenied
	}
	if string(server.UID) != b.ServerUID || server.DeletionTimestamp != nil || server.Annotations["mcpruntime.org/retiring"] == "true" || !slices.Contains(server.Spec.CredentialBindings, b.Name) {
		return cb.ErrDenied
	}
	for _, tool := range b.Tools {
		found := false
		for _, metadata := range server.Spec.Tools {
			if metadata.Name == tool {
				found = true
			}
		}
		if !found {
			return cb.ErrDenied
		}
	}
	return nil
}

func (h Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if h.Store == nil || h.Kube == nil || h.Authenticate == nil {
		respondError(w, cb.ErrUnavailable)
		return
	}
	// Only a live Pod-bound token for the dedicated broker audience selects the
	// workload. Ignore every identity/namespace header from the public request.
	credential := r.Header.Get("Authorization")
	if !strings.HasPrefix(credential, "Bearer ") || r.Header.Get("X-Api-Key") != "" {
		respondError(w, cb.ErrDenied)
		return
	}
	limits := h.limits()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// A recent verdict for this exact token (keyed by its hash, never stored in
	// clear) skips TokenReview and every pre-authentication limiter, so invalid
	// traffic sharing a source or proxy address cannot deny a valid caller.
	token := strings.TrimPrefix(credential, "Bearer ")
	key := tokenHash(token)
	server, valid, known := limits.CachedAuth(key)
	if !known {
		// Only cold tokens consume the per-source and global TokenReview budgets.
		if !limits.AllowColdReview(platformauth.RequestIP(r)) {
			respondError(w, errRateLimited)
			return
		}
		var err error
		server, err = h.Authenticate(ctx, token)
		valid = err == nil
		limits.RecordAuth(key, server, valid)
	}
	if !valid {
		respondError(w, cb.ErrDenied)
		return
	}
	if !limits.AllowServer(server.UID) {
		respondError(w, errRateLimited)
		return
	}
	var request cb.ResolutionRequest
	if !decode(w, r, &request) {
		return
	}
	authorize := func(session sessionstate.Session, bindings []cb.Binding, _ []cb.Source) error {
		if err := h.authorize(ctx, server, session, request); err != nil {
			return err
		}
		for _, binding := range bindings {
			if err := h.checkBinding(ctx, binding); err != nil {
				return err
			}
		}
		return nil
	}
	// fetch runs the bounded provider read. It must not hold shared locks; the
	// store re-validates grant, session, server and binding revisions after it.
	fetch := func(_ sessionstate.Session, bindings []cb.Binding, sources []cb.Source) (cb.Delivery, error) {
		out := cb.Delivery{Headers: map[string]string{}}
		for i, binding := range bindings {
			provider := h.Providers[sources[i].Provider]
			if provider == nil {
				return cb.Delivery{}, cb.ErrUnavailable
			}
			failure := limits.failureKey(server.UID, sources[i])
			if limits.RecentFailure(failure) {
				return cb.Delivery{}, cb.ErrUnavailable
			}
			value, err := provider.Read(ctx, sources[i])
			if err != nil || !cb.ValidValue(value.Bytes) {
				limits.RecordFailure(failure)
				return cb.Delivery{}, cb.ErrUnavailable
			}
			header := http.CanonicalHeaderKey(binding.Header)
			if _, exists := out.Headers[header]; exists {
				return cb.Delivery{}, cb.ErrDenied
			}
			out.Headers[header] = string(value.Bytes)
		}
		return out, nil
	}
	result, err := h.Store.UseCredentials(ctx, server, request, authorize, fetch)
	if err != nil {
		respondError(w, err)
		return
	}
	respond(w, result)
}

func (h Handler) authorize(ctx context.Context, identity sessionstate.Server, session sessionstate.Session, request cb.ResolutionRequest) error {
	server := &mcpv1.MCPServer{}
	if err := h.Kube.Get(ctx, types.NamespacedName{Namespace: identity.Namespace, Name: identity.Name}, server); err != nil {
		return cb.ErrDenied
	}
	if string(server.UID) != identity.UID || server.DeletionTimestamp != nil || server.Annotations["mcpruntime.org/retiring"] == "true" || server.Spec.Policy == nil || server.Spec.Policy.Mode != mcpv1.PolicyModeAllowList || server.Spec.Policy.DefaultDecision != mcpv1.PolicyDecisionDeny || mcpv1.AuthUsesHeaderMode(server.Spec.Auth) {
		return cb.ErrDenied
	}
	for _, name := range request.Bindings {
		if !slices.Contains(server.Spec.CredentialBindings, name) {
			return cb.ErrDenied
		}
	}
	grant := &mcpv1.MCPAccessGrant{}
	if err := h.Kube.Get(ctx, types.NamespacedName{Namespace: session.GrantNamespace, Name: session.GrantName}, grant); err != nil {
		return cb.ErrDenied
	}
	refNS := grant.Spec.ServerRef.Namespace
	if refNS == "" {
		refNS = grant.Namespace
	}
	if refNS != identity.Namespace || grant.Spec.ServerRef.Name != identity.Name || grant.DeletionTimestamp != nil || grant.Annotations["mcpruntime.org/server-uid"] != identity.UID || string(grant.Spec.Mode) == "monitor" {
		return cb.ErrDenied
	}
	doc := &policy.Document{Server: policy.Server{UID: identity.UID, Name: policy.ServerName(identity.Name), Namespace: policy.Namespace(identity.Namespace), TeamID: policy.TeamID(server.Spec.TeamID)}, Policy: &policy.Config{Mode: "allow-list", DefaultDecision: "deny", PolicyVersion: server.Spec.Policy.PolicyVersion, MaxSideEffect: string(server.Spec.Policy.MaxSideEffect)}, Session: &policy.Session{Required: true, Store: "platform"}}
	for _, tool := range server.Spec.Tools {
		doc.Tools = append(doc.Tools, policy.Tool{Name: policy.ToolName(tool.Name), RequiredTrust: string(tool.RequiredTrust), SideEffect: string(tool.SideEffect), RiskLevel: string(tool.RiskLevel)})
	}
	spec := grant.Spec
	team := spec.Subject.TeamID
	if team == "" {
		team = server.Spec.TeamID
	}
	g := policy.Grant{Name: grant.Name, Namespace: policy.Namespace(grant.Namespace), Mode: string(spec.Mode), HumanID: policy.HumanID(spec.Subject.HumanID), AgentID: policy.AgentID(spec.Subject.AgentID), TeamID: policy.TeamID(team), MaxTrust: string(spec.MaxTrust), PolicyVersion: spec.PolicyVersion, Disabled: spec.Disabled}
	if g.MaxTrust == "" {
		g.MaxTrust = "low"
	}
	if spec.ExpiresAt != nil {
		g.ExpiresAt = spec.ExpiresAt.UTC().Format(time.RFC3339)
	}
	for _, effect := range spec.AllowedSideEffects {
		g.AllowedSideEffects = append(g.AllowedSideEffects, string(effect))
	}
	for _, rule := range spec.ToolRules {
		decision := string(rule.Decision)
		if decision == "" {
			decision = "allow"
		}
		trust := string(rule.RequiredTrust)
		if trust == "" {
			trust = "low"
		}
		g.ToolRules = append(g.ToolRules, policy.ToolAccess{Name: policy.ToolName(rule.Name), Decision: decision, RequiredTrust: trust})
	}
	doc.Grants = []policy.Grant{g}
	if policy.Stamp(doc, "") != nil || policy.Validate(doc) != nil {
		return cb.ErrDenied
	}
	b := session.Binding
	decision := policy.Authorize(doc, policy.Request{Identity: policy.Identity{HumanID: b.HumanID, AgentID: b.AgentID, TeamID: b.TeamID, SessionID: b.Name}, RPCMethod: "tools/call", ToolName: policy.ToolName(request.Tool), Session: &b}, time.Now())
	if !decision.Allowed || decision.WouldDeny || decision.MatchedGrant != session.GrantName || decision.MatchedGrantNamespace != session.GrantNamespace {
		return cb.ErrDenied
	}
	return nil
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid credential metadata", http.StatusBadRequest)
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func respondError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	message := "credential operation unavailable"
	switch {
	case errors.Is(err, errRateLimited):
		w.Header().Set("Retry-After", "1")
		status, message = http.StatusTooManyRequests, "credential resolution rate limited"
	case errors.Is(err, cb.ErrDenied):
		status, message = http.StatusForbidden, "credential access denied"
	case errors.Is(err, cb.ErrConflict):
		status, message = http.StatusConflict, "credential metadata revision conflict or source in use"
	case errors.Is(err, cb.ErrNotFound):
		status, message = http.StatusNotFound, "credential metadata not found"
	}
	http.Error(w, message, status)
}
