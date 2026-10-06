package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"mcp-platform-api/auth"
	"mcp-platform-api/internal/apiauth"
	"mcp-platform-api/internal/httperrors"
	"mcp-platform-api/internal/platformstore"
	"mcp-runtime/pkg/apihttp"
)

const imageActivityRequestMaxBytes = 16 * 1024

type UserAPIKeyStore interface {
	AuthenticateUserAPIKey(ctx context.Context, rawKey string) (apiauth.Principal, bool, error)
	ListUserAPIKeys(ctx context.Context, userID string) ([]platformstore.APIKeySummary, error)
	CreateUserAPIKey(ctx context.Context, userID, name string) (platformstore.APIKeySummary, string, error)
	RevokeUserAPIKey(ctx context.Context, userID, id string) (platformstore.APIKeySummary, error)
}

type Dependencies struct {
	Platform             *platformstore.Store
	UserKeys             UserAPIKeyStore
	PrincipalFromContext func(context.Context) (apiauth.Principal, bool)
	AuthenticateRequest  func(*http.Request) (apiauth.Principal, bool, error)
	WriteJSON            func(http.ResponseWriter, int, any)
	WriteBodyDecodeError func(http.ResponseWriter, error)
	RequestIP            func(*http.Request) string
	RequestSource        func(*http.Request) string
	AuditSource          func(*http.Request, apiauth.Principal) string
	AuditIdentityLabel   func(apiauth.Principal) string
}

type passwordUserCreateRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role,omitempty"`
}

func HandleAuthMe(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	p, ok := deps.PrincipalFromContext(r.Context())
	if !ok {
		httperrors.Unauthorized(w)
		return
	}
	type authPrincipal struct {
		Role              string                        `json:"role"`
		Subject           string                        `json:"subject,omitempty"`
		Email             string                        `json:"email,omitempty"`
		Namespace         string                        `json:"namespace,omitempty"`
		AllowedNamespaces []string                      `json:"allowedNamespaces,omitempty"`
		Teams             []platformstore.PrincipalTeam `json:"teams,omitempty"`
	}
	deps.WriteJSON(w, http.StatusOK, map[string]any{
		"authenticated":          true,
		"sharedCatalogNamespace": platformstore.SharedCatalogNamespace,
		"principal": authPrincipal{
			Role:              p.Role,
			Subject:           p.Subject,
			Email:             p.Email,
			Namespace:         p.Namespace,
			AllowedNamespaces: p.AllowedNamespaces,
			Teams:             p.Teams,
		},
	})
}

func HandleSignup(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	if deps.Platform == nil {
		httperrors.PlatformUnavailable(w)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("allow", "POST")
		httperrors.MethodNotAllowed(w, "POST")
		return
	}
	var req passwordUserCreateRequest
	r.Body = http.MaxBytesReader(w, r.Body, auth.PlatformSignupRequestMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		deps.WriteBodyDecodeError(w, err)
		return
	}
	role, err := NormalizePasswordUserRole(req.Role)
	if err != nil {
		httperrors.BadRequest(w, "invalid role")
		return
	}
	if role == platformstore.RoleAdmin {
		p, ok, err := deps.AuthenticateRequest(r)
		if err != nil {
			httperrors.AuthFailed(w)
			return
		}
		if !ok || p.Role != platformstore.RoleAdmin {
			httperrors.Forbidden(w, "admin signup requires an admin principal")
			return
		}
	}
	u, err := deps.Platform.CreatePasswordUser(r.Context(), req.Email, req.Password, role)
	if err != nil {
		writePasswordUserError(w, err)
		return
	}
	token, err := deps.Platform.CreateAccessToken(u, auth.PlatformAccessTokenTTL)
	if err != nil {
		httperrors.Internal(w, "failed to issue token")
		return
	}
	deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{UserID: u.ID, Action: "signup", Resource: "user", Namespace: u.Namespace, Status: "success", ActorIP: deps.RequestIP(r), Source: deps.RequestSource(r), AuthIdentity: "password:" + u.Email})
	deps.WriteJSON(w, http.StatusCreated, map[string]any{"access_token": token, "token_type": "bearer", "expires_in": int(auth.PlatformAccessTokenTTL.Seconds()), "user": u})
}

func HandleUsers(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	if deps.Platform == nil {
		httperrors.PlatformUnavailable(w)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("allow", "POST")
		httperrors.MethodNotAllowed(w, "POST")
		return
	}
	var req passwordUserCreateRequest
	r.Body = http.MaxBytesReader(w, r.Body, auth.PlatformSignupRequestMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		deps.WriteBodyDecodeError(w, err)
		return
	}
	role, err := NormalizePasswordUserRole(req.Role)
	if err != nil {
		httperrors.BadRequest(w, "invalid role")
		return
	}
	p, ok, err := deps.AuthenticateRequest(r)
	if err != nil {
		httperrors.AuthFailed(w)
		return
	}
	if !ok || p.Role != platformstore.RoleAdmin {
		httperrors.Forbidden(w, "admin role required")
		return
	}
	u, err := deps.Platform.CreatePasswordUser(r.Context(), req.Email, req.Password, role)
	if err != nil {
		writePasswordUserError(w, err)
		return
	}
	deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{UserID: p.UserID(), Action: "user_create", Resource: "user", Namespace: u.Namespace, Status: "success", ActorIP: deps.RequestIP(r), Source: deps.RequestSource(r), AuthIdentity: deps.AuditIdentityLabel(p)})
	deps.WriteJSON(w, http.StatusCreated, map[string]any{"user": u})
}

func HandleUserAPIKeys(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	p, ok := deps.PrincipalFromContext(r.Context())
	if !ok || p.UserID() == "" {
		httperrors.Unauthorized(w)
		return
	}
	if deps.UserKeys == nil {
		httperrors.UserKeyStoreUnavailable(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		keys, err := deps.UserKeys.ListUserAPIKeys(r.Context(), p.UserID())
		if err != nil {
			httperrors.QueryFailed(w, "failed to list user api keys")
			return
		}
		deps.WriteJSON(w, http.StatusOK, map[string]any{"keys": keys})
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, auth.PlatformSignupRequestMaxBytes)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			deps.WriteBodyDecodeError(w, err)
			return
		}
		key, cleartext, err := deps.UserKeys.CreateUserAPIKey(r.Context(), p.UserID(), req.Name)
		if err != nil {
			if deps.Platform != nil {
				deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{UserID: p.UserID(), Action: "api_key_create", Resource: strings.TrimSpace(req.Name), Namespace: p.Namespace, Status: "error", Message: err.Error(), ActorIP: deps.RequestIP(r), Source: deps.AuditSource(r, p), AuthIdentity: deps.AuditIdentityLabel(p)})
			}
			httperrors.BadRequest(w, err.Error())
			return
		}
		if deps.Platform != nil {
			deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{UserID: p.UserID(), Action: "api_key_create", Resource: key.ID, Namespace: p.Namespace, Status: "success", ActorIP: deps.RequestIP(r), Source: deps.AuditSource(r, p), AuthIdentity: deps.AuditIdentityLabel(p)})
		}
		deps.WriteJSON(w, http.StatusOK, map[string]any{"key": key, "api_key": cleartext, "one_time_key": cleartext})
	default:
		httperrors.MethodNotAllowed(w, "GET, POST")
	}
}

func HandleUserAPIKeyItem(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	p, ok := deps.PrincipalFromContext(r.Context())
	if !ok || p.UserID() == "" {
		httperrors.Unauthorized(w)
		return
	}
	if deps.UserKeys == nil {
		httperrors.UserKeyStoreUnavailable(w)
		return
	}
	keyID, allowed, valid := parseUserAPIKeyItemPath(r.Method, r.URL.Path)
	if !allowed {
		httperrors.MethodNotAllowed(w, "DELETE, POST")
		return
	}
	if !valid {
		httperrors.BadRequest(w, "invalid key path")
		return
	}
	key, revokeErr := deps.UserKeys.RevokeUserAPIKey(r.Context(), p.UserID(), keyID)
	if revokeErr != nil {
		if deps.Platform != nil {
			deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{UserID: p.UserID(), Action: "api_key_revoke", Resource: keyID, Namespace: p.Namespace, Status: "error", Message: revokeErr.Error(), ActorIP: deps.RequestIP(r), Source: deps.AuditSource(r, p), AuthIdentity: deps.AuditIdentityLabel(p)})
		}
		if apierrors.IsNotFound(revokeErr) || errors.Is(revokeErr, sql.ErrNoRows) {
			httperrors.NotFound(w, "key not found")
			return
		}
		httperrors.QueryFailed(w, "failed to revoke key")
		return
	}
	if deps.Platform != nil {
		deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{UserID: p.UserID(), Action: "api_key_revoke", Resource: key.ID, Namespace: p.Namespace, Status: "success", ActorIP: deps.RequestIP(r), Source: deps.AuditSource(r, p), AuthIdentity: deps.AuditIdentityLabel(p)})
	}
	deps.WriteJSON(w, http.StatusOK, map[string]any{"key": key})
}

func HandleRegistryCredentials(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	if deps.Platform == nil {
		httperrors.PlatformUnavailable(w)
		return
	}
	p, ok := deps.PrincipalFromContext(r.Context())
	if !ok || p.UserID() == "" {
		httperrors.Unauthorized(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		keys, err := deps.Platform.ListRegistryCredentials(r.Context(), p.UserID())
		if err != nil {
			httperrors.QueryFailed(w, "failed to list registry credentials")
			return
		}
		deps.WriteJSON(w, http.StatusOK, map[string]any{"credentials": keys})
	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, auth.PlatformSignupRequestMaxBytes)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			deps.WriteBodyDecodeError(w, err)
			return
		}
		key, cleartext, err := deps.Platform.CreateRegistryCredential(r.Context(), p.UserID(), req.Name)
		if err != nil {
			deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{UserID: p.UserID(), Action: "registry_credential_create", Resource: strings.TrimSpace(req.Name), Status: "error", Message: err.Error(), ActorIP: deps.RequestIP(r), Source: deps.AuditSource(r, p), AuthIdentity: deps.AuditIdentityLabel(p)})
			httperrors.BadRequest(w, err.Error())
			return
		}
		deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{UserID: p.UserID(), Action: "registry_credential_create", Resource: key.ID, Status: "success", ActorIP: deps.RequestIP(r), Source: deps.AuditSource(r, p), AuthIdentity: deps.AuditIdentityLabel(p)})
		deps.WriteJSON(w, http.StatusCreated, map[string]any{"credential": key, "username": RegistryCredentialUsername(p), "password": cleartext})
	default:
		httperrors.MethodNotAllowed(w, "GET, POST")
	}
}

func HandleRegistryCredentialItem(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	if deps.Platform == nil {
		httperrors.PlatformUnavailable(w)
		return
	}
	p, ok := deps.PrincipalFromContext(r.Context())
	if !ok || p.UserID() == "" {
		httperrors.Unauthorized(w)
		return
	}
	credentialID, allowed, valid := parseRegistryCredentialItemPath(r.Method, r.URL.Path)
	if !allowed {
		httperrors.MethodNotAllowed(w, "DELETE, POST")
		return
	}
	if !valid {
		httperrors.BadRequest(w, "invalid credential path")
		return
	}
	key, err := deps.Platform.RevokeRegistryCredential(r.Context(), p.UserID(), credentialID)
	if err != nil {
		deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{UserID: p.UserID(), Action: "registry_credential_revoke", Resource: credentialID, Status: "error", Message: err.Error(), ActorIP: deps.RequestIP(r), Source: deps.AuditSource(r, p), AuthIdentity: deps.AuditIdentityLabel(p)})
		if errors.Is(err, sql.ErrNoRows) {
			httperrors.NotFound(w, "credential not found")
			return
		}
		httperrors.QueryFailed(w, "failed to revoke credential")
		return
	}
	deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{UserID: p.UserID(), Action: "registry_credential_revoke", Resource: key.ID, Status: "success", ActorIP: deps.RequestIP(r), Source: deps.AuditSource(r, p), AuthIdentity: deps.AuditIdentityLabel(p)})
	deps.WriteJSON(w, http.StatusOK, map[string]any{"credential": key})
}

func HandleUserImagePublishActivity(w http.ResponseWriter, r *http.Request, deps Dependencies) {
	if deps.Platform == nil {
		httperrors.PlatformUnavailable(w)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("allow", "POST")
		httperrors.MethodNotAllowed(w, "POST")
		return
	}
	p, ok := deps.PrincipalFromContext(r.Context())
	if !ok || p.UserID() == "" {
		httperrors.Unauthorized(w)
		return
	}
	var req struct {
		ImageRef    string `json:"image_ref"`
		SourceImage string `json:"source_image,omitempty"`
		Mode        string `json:"mode,omitempty"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, imageActivityRequestMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		deps.WriteBodyDecodeError(w, err)
		return
	}
	imageRef := strings.TrimSpace(req.ImageRef)
	if imageRef == "" {
		httperrors.BadRequest(w, "image_ref is required")
		return
	}
	if len(imageRef) > 512 || len(req.SourceImage) > 512 || len(req.Mode) > 64 {
		httperrors.BadRequest(w, "image publish fields are too long")
		return
	}
	message := strings.TrimSpace(req.Mode)
	if message != "" {
		message = "mode=" + message
	}
	deps.Platform.WriteAudit(r.Context(), platformstore.AuditEvent{
		UserID:       p.UserID(),
		Action:       "image_publish",
		Resource:     strings.TrimSpace(req.SourceImage),
		Namespace:    p.Namespace,
		Status:       "success",
		Message:      message,
		ActorIP:      deps.RequestIP(r),
		Source:       deps.AuditSource(r, p),
		AuthIdentity: deps.AuditIdentityLabel(p),
		ImageRef:     imageRef,
	})
	deps.WriteJSON(w, http.StatusAccepted, map[string]any{"ok": true, "image_ref": imageRef})
}

func NormalizePasswordUserRole(raw string) (string, error) {
	role := strings.ToLower(strings.TrimSpace(raw))
	if role == "" {
		role = platformstore.RoleUser
	}
	if role != platformstore.RoleUser && role != platformstore.RoleAdmin {
		return "", errors.New("invalid role")
	}
	return role, nil
}

func RegistryCredentialUsername(p apiauth.Principal) string {
	if namespace := strings.TrimSpace(p.Namespace); namespace != "" {
		return namespace
	}
	if subject := strings.TrimSpace(p.Subject); subject != "" {
		return subject
	}
	return strings.TrimSpace(p.Email)
}

func parseUserAPIKeyItemPath(method, path string) (keyID string, allowed bool, valid bool) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, "/api/user/api-keys/"), "/"), "/")
	switch method {
	case http.MethodDelete:
		if len(parts) != 1 || parts[0] == "" {
			return "", true, false
		}
		return parts[0], true, true
	case http.MethodPost:
		if len(parts) != 2 || parts[0] == "" || parts[1] != "revoke" {
			return "", true, false
		}
		return parts[0], true, true
	default:
		return "", false, false
	}
}

func parseRegistryCredentialItemPath(method, path string) (credentialID string, allowed bool, valid bool) {
	const prefix = "/api/v1/user/registry-credentials/"
	if strings.HasPrefix(path, prefix) {
		path = strings.TrimPrefix(path, prefix)
	} else {
		path = strings.TrimPrefix(path, "/api/user/registry-credentials/")
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch method {
	case http.MethodDelete:
		if len(parts) != 1 || parts[0] == "" {
			return "", true, false
		}
		return parts[0], true, true
	case http.MethodPost:
		if len(parts) != 2 || parts[0] == "" || parts[1] != "revoke" {
			return "", true, false
		}
		return parts[0], true, true
	default:
		return "", false, false
	}
}

func writePasswordUserError(w http.ResponseWriter, err error) {
	var input platformstore.UserInputError
	switch {
	case errors.Is(err, platformstore.ErrEmailAlreadyRegistered):
		apihttp.WriteEnvelope(w, http.StatusConflict, apihttp.CodeConflict, platformstore.ErrEmailAlreadyRegistered.Error())
	case errors.As(err, &input):
		apihttp.WriteEnvelope(w, http.StatusBadRequest, apihttp.CodeInvalidRequestBody, input.Error())
	default:
		apihttp.WriteEnvelope(w, http.StatusInternalServerError, apihttp.CodeInternalError, "failed to create user")
	}
}
