package v1alpha1

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"mcp-runtime/pkg/mcpdefaults"
)

var (
	_ admission.Defaulter[*MCPServer]       = mcpServerWebhook{}
	_ admission.Validator[*MCPServer]       = mcpServerWebhook{}
	_ admission.Validator[*MCPAccessGrant]  = mcpAccessGrantValidator{}
	_ admission.Validator[*MCPAgentSession] = mcpAgentSessionValidator{}
)

const (
	defaultImageTag          = "latest"
	defaultReplicas          = int32(1)
	defaultPort              = int32(mcpdefaults.MCPServerPort)
	defaultServicePort       = int32(80)
	defaultIngressClass      = "traefik"
	defaultGatewayPort       = int32(mcpdefaults.MCPGatewayPort)
	defaultToolRequiredTrust = "low"

	defaultAuthTokenHeader = mcpdefaults.AuthTokenHeader

	defaultPolicyMode      = PolicyMode(mcpdefaults.PolicyMode)
	defaultPolicyDecision  = PolicyDecision(mcpdefaults.PolicyDecision)
	defaultPolicyEnforceOn = mcpdefaults.PolicyEnforceOn
	defaultPolicyVersion   = mcpdefaults.PolicyVersion
	defaultSessionStore    = mcpdefaults.SessionStore
	defaultSessionMaxLife  = mcpdefaults.SessionMaxLife
	defaultSessionIdleTime = mcpdefaults.SessionIdleTime
	defaultSessionUpstream = mcpdefaults.SessionUpstream

	defaultAnalyticsEventType    = "mcp.request"
	defaultAnalyticsSourceSuffix = "-gateway"
	defaultRolloutStrategy       = RolloutStrategyRollingUpdate
	defaultRolloutMaxUnavailable = "25%"
	defaultRolloutMaxSurge       = "25%"
)

func defaultIngressPathFromName(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	return mcpdefaults.DefaultIngressPath(strings.TrimSpace(name))
}

func defaultPublicPathPrefixFromName(name string) string {
	return strings.TrimSpace(name)
}

func imageHasTagOrDigest(image string) bool {
	if strings.Contains(image, "@") {
		return true
	}

	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")
	return lastColon > lastSlash
}

func gatewayEnabled(spec MCPServerSpec) bool {
	return GatewayIsEnabled(spec.Gateway)
}

// MCPServerDefaultOptions holds operator-scoped values that the admission
// webhook can use while defaulting MCPServer objects.
type MCPServerDefaultOptions struct {
	DefaultIngressHost        string
	DefaultIngressTLS         bool
	DefaultAnalyticsIngestURL string
	DefaultOAuthIssuerURL     string
}

func (r *MCPServer) Default() {
	r.DefaultWithOptions(MCPServerDefaultOptions{})
}

// DefaultWithOptions applies MCPServer defaults, including operator-configured
// fallbacks when the webhook is registered by the operator manager.
// ResolveDerivedAuth fills an OAuth server's unset audience and issuer from
// platform state: the audience from the public URL the ingress serves, the
// issuer from the bundled authorization server. It is applied to the
// operator's in-memory copy on every reconcile and is never persisted by the
// admission webhook, so a later change to the host, path, TLS setting, or
// platform domain re-derives the values instead of leaving a stale copy in
// spec. Explicit values are kept.
func (r *MCPServer) ResolveDerivedAuth(options MCPServerDefaultOptions) {
	if r.Spec.Auth == nil {
		return
	}
	if strings.TrimSpace(r.Spec.Auth.IssuerURL) == "" {
		r.Spec.Auth.IssuerURL = strings.TrimSpace(options.DefaultOAuthIssuerURL)
	}
	if strings.TrimSpace(r.Spec.Auth.Audience) == "" {
		r.Spec.Auth.Audience = r.CanonicalResourceURL(PublicURLOptions{
			DefaultIngressHost: options.DefaultIngressHost,
			DefaultIngressTLS:  options.DefaultIngressTLS,
		})
	}
}

// ValidateResolvedAuth reports OAuth settings that are still missing after
// ResolveDerivedAuth. Admission cannot check this, because the values are
// derived later from operator state; the operator reports it on reconcile.
func (r *MCPServer) ValidateResolvedAuth() error {
	if r.Spec.Auth == nil {
		return nil
	}
	specPath := field.NewPath("spec")
	var allErrs field.ErrorList
	if gatewayEnabled(r.Spec) && strings.TrimSpace(r.Spec.Auth.IssuerURL) == "" {
		allErrs = append(allErrs, field.Required(specPath.Child("auth", "issuerURL"), "auth.issuerURL is required when no bundled authorization server is configured"))
	}
	if strings.TrimSpace(r.Spec.Auth.Audience) == "" {
		allErrs = append(allErrs, field.Required(specPath.Child("auth", "audience"), "auth.audience cannot be derived; set spec.ingressHost or MCP_DEFAULT_INGRESS_HOST on the operator so it defaults to the public MCP URL, or set auth.audience"))
	}
	if len(allErrs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(schema.GroupKind{Group: GroupVersion.Group, Kind: "MCPServer"}, r.Name, allErrs)
}

func (r *MCPServer) DefaultWithOptions(options MCPServerDefaultOptions) {
	ingressHostUnset := strings.TrimSpace(r.Spec.IngressHost) == ""
	publicPathPrefixUnset := strings.TrimSpace(r.Spec.PublicPathPrefix) == ""

	if strings.TrimSpace(r.Spec.ImageTag) == "" && !imageHasTagOrDigest(strings.TrimSpace(r.Spec.Image)) {
		r.Spec.ImageTag = defaultImageTag
	}
	if r.Spec.Replicas == nil {
		replicas := defaultReplicas
		r.Spec.Replicas = &replicas
	}
	if r.Spec.Port == 0 {
		r.Spec.Port = defaultPort
	}
	if r.Spec.ServicePort == 0 {
		r.Spec.ServicePort = defaultServicePort
	}
	if strings.TrimSpace(r.Spec.IngressPath) == "" {
		r.Spec.IngressPath = defaultIngressPathFromName(r.Name)
	}
	// Path-based public routing is the default for OAuth protected servers,
	// mtls: Traefik terminates the client mTLS and routes by path to the gateway.
	if strings.TrimSpace(r.Spec.PublicPathPrefix) == "" {
		r.Spec.PublicPathPrefix = defaultPublicPathPrefixFromName(r.Name)
	}
	if strings.TrimSpace(r.Spec.IngressClass) == "" {
		r.Spec.IngressClass = defaultIngressClass
	}
	if ingressHostUnset && publicPathPrefixUnset {
		r.Spec.IngressHost = strings.TrimSpace(options.DefaultIngressHost)
	}

	// Gateway is on by default for metrics/traces/analytics. Opt out with
	// enabled: false. Do not invent Spec.Policy: omitting policy means observe
	// mode at the gateway so tool calls are not deny-by-default.
	if r.Spec.Gateway == nil {
		r.Spec.Gateway = &GatewayConfig{}
	}
	if r.Spec.Gateway.Enabled == nil {
		r.Spec.Gateway.Enabled = BoolPtr(true)
	}

	if r.Spec.Auth != nil {
		if strings.TrimSpace(r.Spec.Auth.TokenHeader) == "" {
			r.Spec.Auth.TokenHeader = defaultAuthTokenHeader
		}
	}

	if r.Spec.Policy != nil {
		if strings.TrimSpace(string(r.Spec.Policy.Mode)) == "" {
			r.Spec.Policy.Mode = defaultPolicyMode
		}
		if strings.TrimSpace(string(r.Spec.Policy.DefaultDecision)) == "" {
			r.Spec.Policy.DefaultDecision = defaultPolicyDecision
		}
		if strings.TrimSpace(r.Spec.Policy.EnforceOn) == "" {
			r.Spec.Policy.EnforceOn = defaultPolicyEnforceOn
		}
		if strings.TrimSpace(r.Spec.Policy.PolicyVersion) == "" {
			r.Spec.Policy.PolicyVersion = defaultPolicyVersion
		}
	}

	if r.Spec.Session != nil {
		if strings.TrimSpace(r.Spec.Session.Store) == "" {
			r.Spec.Session.Store = defaultSessionStore
		}
		if strings.TrimSpace(r.Spec.Session.MaxLifetime) == "" {
			r.Spec.Session.MaxLifetime = defaultSessionMaxLife
		}
		if strings.TrimSpace(r.Spec.Session.IdleTimeout) == "" {
			r.Spec.Session.IdleTimeout = defaultSessionIdleTime
		}
		if strings.TrimSpace(r.Spec.Session.UpstreamTokenHeader) == "" {
			r.Spec.Session.UpstreamTokenHeader = defaultSessionUpstream
		}
	}

	for i := range r.Spec.Tools {
		if strings.TrimSpace(string(r.Spec.Tools[i].RequiredTrust)) == "" {
			r.Spec.Tools[i].RequiredTrust = TrustLevel(defaultToolRequiredTrust)
		}
	}

	if gatewayEnabled(r.Spec) {
		if r.Spec.Gateway.Port == 0 {
			r.Spec.Gateway.Port = defaultGatewayPort
		}
		if strings.TrimSpace(r.Spec.Gateway.UpstreamURL) == "" {
			r.Spec.Gateway.UpstreamURL = fmt.Sprintf("http://127.0.0.1:%d", r.Spec.Port)
		}
		// Analytics on by default when the gateway is on and an ingest URL is
		// available (operator default or already set). Opt out with
		// analytics.disabled: true. Do not invent an empty analytics block when
		// no ingest URL can be filled — the operator still emits when its
		// DefaultAnalyticsIngestURL is configured.
		if r.Spec.Analytics == nil && strings.TrimSpace(options.DefaultAnalyticsIngestURL) != "" {
			r.Spec.Analytics = &AnalyticsConfig{}
		}
	}

	if r.Spec.Analytics != nil && !r.Spec.Analytics.Disabled {
		if strings.TrimSpace(r.Spec.Analytics.Source) == "" {
			r.Spec.Analytics.Source = strings.TrimSpace(r.Name) + defaultAnalyticsSourceSuffix
		}
		if strings.TrimSpace(r.Spec.Analytics.EventType) == "" {
			r.Spec.Analytics.EventType = defaultAnalyticsEventType
		}
		ingestURL := strings.TrimSpace(r.Spec.Analytics.IngestURL)
		if ingestURL == "" {
			if def := strings.TrimSpace(options.DefaultAnalyticsIngestURL); def != "" {
				r.Spec.Analytics.IngestURL = def
			}
		}
	}

	if r.Spec.Rollout != nil {
		if strings.TrimSpace(string(r.Spec.Rollout.Strategy)) == "" {
			r.Spec.Rollout.Strategy = defaultRolloutStrategy
		}
		if strings.TrimSpace(r.Spec.Rollout.MaxUnavailable) == "" {
			r.Spec.Rollout.MaxUnavailable = defaultRolloutMaxUnavailable
		}
		if strings.TrimSpace(r.Spec.Rollout.MaxSurge) == "" {
			r.Spec.Rollout.MaxSurge = defaultRolloutMaxSurge
		}
	}
}

func (r *MCPServer) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return r.SetupWebhookWithManagerWithOptions(mgr, MCPServerDefaultOptions{})
}

func (r *MCPServer) SetupWebhookWithManagerWithOptions(mgr ctrl.Manager, options MCPServerDefaultOptions) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithDefaulter(mcpServerWebhook{defaultOptions: options}).
		WithValidator(mcpServerWebhook{defaultOptions: options}).
		Complete()
}

type mcpServerWebhook struct {
	defaultOptions MCPServerDefaultOptions
}

func (w mcpServerWebhook) Default(_ context.Context, obj *MCPServer) error {
	obj.DefaultWithOptions(w.defaultOptions)
	return nil
}

func (mcpServerWebhook) ValidateCreate(_ context.Context, obj *MCPServer) (admission.Warnings, error) {
	return obj.ValidateCreate()
}

func (mcpServerWebhook) ValidateUpdate(_ context.Context, oldObj *MCPServer, newObj *MCPServer) (admission.Warnings, error) {
	return newObj.ValidateUpdate(oldObj)
}

func (mcpServerWebhook) ValidateDelete(_ context.Context, obj *MCPServer) (admission.Warnings, error) {
	return obj.ValidateDelete()
}

func (r *MCPServer) ValidateCreate() (admission.Warnings, error) {
	return nil, r.validate()
}

func (r *MCPServer) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	return nil, r.validate()
}

func (r *MCPServer) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

func (r *MCPServer) validate() error {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")
	publicPathPrefix := strings.TrimSpace(r.Spec.PublicPathPrefix)
	ingressPath := strings.TrimSpace(r.Spec.IngressPath)

	if strings.TrimSpace(r.Spec.Image) == "" {
		allErrs = append(allErrs, field.Required(specPath.Child("image"), "image is required"))
	}
	if err := validateTeamIDField(specPath.Child("teamID"), r.Spec.TeamID); err != nil {
		allErrs = append(allErrs, err)
	}
	if publicPathPrefix != "" {
		trimmed := strings.Trim(publicPathPrefix, "/")
		if trimmed == "" {
			allErrs = append(allErrs, field.Invalid(specPath.Child("publicPathPrefix"), r.Spec.PublicPathPrefix, "publicPathPrefix must contain at least one non-slash character"))
		}
	}
	if publicPathPrefix == "" {
		if ingressPath == "" {
			allErrs = append(allErrs, field.Required(specPath.Child("ingressPath"), "ingressPath is required when ingressHost is used"))
		}
		if strings.TrimSpace(r.Spec.IngressHost) == "" {
			allErrs = append(allErrs, field.Required(specPath.Child("ingressHost"), "ingressHost is required when publicPathPrefix is not set; set spec.ingressHost or MCP_DEFAULT_INGRESS_HOST on the operator, or use spec.publicPathPrefix for hostless routing"))
		}
	}
	if gatewayEnabled(r.Spec) && r.Spec.Gateway != nil && r.Spec.Gateway.Port == r.Spec.Port {
		allErrs = append(allErrs, field.Invalid(specPath.Child("gateway", "port"), r.Spec.Gateway.Port, "gateway.port must differ from spec.port"))
	}
	if r.Spec.Auth != nil {
		// auth.audience is also the resource identifier the gateway advertises
		// in protected resource metadata, so it must be a URI a conforming
		// client can send back as the RFC 8707 resource parameter. An unset
		// audience or issuer is derived at reconcile (ResolveDerivedAuth).
		if audience := strings.TrimSpace(r.Spec.Auth.Audience); audience != "" {
			if parsed, err := url.Parse(audience); err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Fragment != "" {
				allErrs = append(allErrs, field.Invalid(specPath.Child("auth", "audience"), r.Spec.Auth.Audience, "auth.audience must be an absolute URI without a fragment, matching the canonical MCP server URL clients connect to"))
			}
		}
	}
	if !gatewayEnabled(r.Spec) {
		if r.Spec.Analytics != nil && !r.Spec.Analytics.Disabled &&
			(strings.TrimSpace(r.Spec.Analytics.IngestURL) != "" ||
				strings.TrimSpace(r.Spec.Analytics.Source) != "" ||
				strings.TrimSpace(r.Spec.Analytics.EventType) != "" ||
				r.Spec.Analytics.APIKeySecretRef != nil) {
			allErrs = append(allErrs, field.Forbidden(specPath.Child("analytics"), "analytics emission requires gateway.enabled; set spec.analytics.disabled to true or enable the gateway"))
		}
	}
	if r.Spec.Rollout != nil && r.Spec.Rollout.Strategy == RolloutStrategyCanary {
		if r.Spec.Rollout.CanaryReplicas == nil || *r.Spec.Rollout.CanaryReplicas <= 0 {
			allErrs = append(allErrs, field.Required(specPath.Child("rollout", "canaryReplicas"), "canaryReplicas must be greater than zero for canary strategy"))
		}
		if r.Spec.Replicas == nil {
			allErrs = append(allErrs, field.Required(specPath.Child("replicas"), "spec.replicas is required when rollout.strategy is Canary"))
		}
		if r.Spec.Replicas != nil && r.Spec.Rollout.CanaryReplicas != nil && *r.Spec.Rollout.CanaryReplicas >= *r.Spec.Replicas {
			allErrs = append(allErrs, field.Invalid(specPath.Child("rollout", "canaryReplicas"), *r.Spec.Rollout.CanaryReplicas, "must be less than spec.replicas"))
		}
	}
	if r.Spec.Rollout != nil {
		if err := validateRolloutValue(specPath.Child("rollout", "maxUnavailable"), r.Spec.Rollout.MaxUnavailable); err != nil {
			allErrs = append(allErrs, err)
		}
		if err := validateRolloutValue(specPath.Child("rollout", "maxSurge"), r.Spec.Rollout.MaxSurge); err != nil {
			allErrs = append(allErrs, err)
		}
	}

	if r.Spec.Analytics != nil && !r.Spec.Analytics.Disabled {
		if r.Spec.Analytics.APIKeySecretRef != nil {
			if strings.TrimSpace(r.Spec.Analytics.APIKeySecretRef.Name) == "" {
				allErrs = append(allErrs, field.Required(specPath.Child("analytics", "apiKeySecretRef", "name"), "secret name is required"))
			}
			if strings.TrimSpace(r.Spec.Analytics.APIKeySecretRef.Key) == "" {
				allErrs = append(allErrs, field.Required(specPath.Child("analytics", "apiKeySecretRef", "key"), "secret key is required"))
			}
		}
	}

	toolNames := make(map[string]struct{}, len(r.Spec.Tools))
	for i, tool := range r.Spec.Tools {
		toolPath := specPath.Child("tools").Index(i)
		if strings.TrimSpace(tool.Name) == "" {
			allErrs = append(allErrs, field.Required(toolPath.Child("name"), "tool name is required"))
			continue
		}
		if strings.TrimSpace(string(tool.SideEffect)) == "" {
			allErrs = append(allErrs, field.Required(toolPath.Child("sideEffect"), "tool sideEffect is required"))
		}
		if tool.SideEffect != "" && !validToolSideEffect(tool.SideEffect) {
			allErrs = append(allErrs, field.NotSupported(toolPath.Child("sideEffect"), tool.SideEffect, []string{
				string(ToolSideEffectRead),
				string(ToolSideEffectWrite),
				string(ToolSideEffectDestructive),
			}))
		}
		if _, exists := toolNames[tool.Name]; exists {
			allErrs = append(allErrs, field.Duplicate(toolPath.Child("name"), tool.Name))
		}
		toolNames[tool.Name] = struct{}{}
	}

	for i, envVar := range r.Spec.SecretEnvVars {
		envPath := specPath.Child("secretEnvVars").Index(i)
		if strings.TrimSpace(envVar.Name) == "" {
			allErrs = append(allErrs, field.Required(envPath.Child("name"), "secret env name is required"))
		}
		if envVar.SecretKeyRef == nil {
			allErrs = append(allErrs, field.Required(envPath.Child("secretKeyRef"), "secretKeyRef is required"))
			continue
		}
		if strings.TrimSpace(envVar.SecretKeyRef.Name) == "" {
			allErrs = append(allErrs, field.Required(envPath.Child("secretKeyRef", "name"), "secret name is required"))
		}
		if strings.TrimSpace(envVar.SecretKeyRef.Key) == "" {
			allErrs = append(allErrs, field.Required(envPath.Child("secretKeyRef", "key"), "secret key is required"))
		}
	}

	if len(allErrs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(schema.GroupKind{Group: GroupVersion.Group, Kind: "MCPServer"}, r.Name, allErrs)
}

func validateRolloutValue(fieldPath *field.Path, value string) *field.Error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}

	numeric := strings.TrimSuffix(trimmed, "%")
	if numeric == "" || strings.Contains(numeric, "%") {
		return field.Invalid(fieldPath, trimmed, "rollout value must be an integer or percentage")
	}

	parsed, err := strconv.Atoi(numeric)
	if err != nil || parsed < 0 {
		return field.Invalid(fieldPath, trimmed, "rollout value must be an integer or percentage")
	}

	return nil
}

func (r *MCPAccessGrant) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithValidator(mcpAccessGrantValidator{}).
		Complete()
}

type mcpAccessGrantValidator struct{}

func (mcpAccessGrantValidator) ValidateCreate(_ context.Context, obj *MCPAccessGrant) (admission.Warnings, error) {
	return obj.ValidateCreate()
}

func (mcpAccessGrantValidator) ValidateUpdate(_ context.Context, oldObj *MCPAccessGrant, newObj *MCPAccessGrant) (admission.Warnings, error) {
	return newObj.ValidateUpdate(oldObj)
}

func (mcpAccessGrantValidator) ValidateDelete(_ context.Context, obj *MCPAccessGrant) (admission.Warnings, error) {
	return obj.ValidateDelete()
}

func (r *MCPAccessGrant) ValidateCreate() (admission.Warnings, error) {
	return r.wildcardSubjectWarnings(), r.validate()
}

func (r *MCPAccessGrant) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	return r.wildcardSubjectWarnings(), r.validate()
}

func (r *MCPAccessGrant) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

func (r *MCPAccessGrant) wildcardSubjectWarnings() admission.Warnings {
	subject := r.Spec.Subject
	if strings.TrimSpace(subject.HumanID) != "" ||
		strings.TrimSpace(subject.AgentID) != "" ||
		strings.TrimSpace(subject.TeamID) != "" {
		return nil
	}
	return admission.Warnings{
		"MCPAccessGrant subject is empty (wildcard grant): the gateway matches any authenticated principal for the server; adapter session creation still requires subject alignment with the caller",
	}
}

func (r *MCPAccessGrant) validate() error {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	if strings.TrimSpace(r.Spec.ServerRef.Name) == "" {
		allErrs = append(allErrs, field.Required(specPath.Child("serverRef", "name"), "serverRef.name is required"))
	}
	if err := validateTeamIDField(specPath.Child("subject", "teamID"), r.Spec.Subject.TeamID); err != nil {
		allErrs = append(allErrs, err)
	}

	sideEffects := make(map[ToolSideEffect]struct{}, len(r.Spec.AllowedSideEffects))
	for i, sideEffect := range r.Spec.AllowedSideEffects {
		effectPath := specPath.Child("allowedSideEffects").Index(i)
		if strings.TrimSpace(string(sideEffect)) == "" {
			allErrs = append(allErrs, field.Required(effectPath, "allowed side effect is required"))
			continue
		}
		if !validToolSideEffect(sideEffect) {
			allErrs = append(allErrs, field.NotSupported(effectPath, sideEffect, []string{
				string(ToolSideEffectRead),
				string(ToolSideEffectWrite),
				string(ToolSideEffectDestructive),
			}))
			continue
		}
		if _, exists := sideEffects[sideEffect]; exists {
			allErrs = append(allErrs, field.Duplicate(effectPath, sideEffect))
		}
		sideEffects[sideEffect] = struct{}{}
	}

	toolNames := make(map[string]struct{}, len(r.Spec.ToolRules))
	for i, rule := range r.Spec.ToolRules {
		rulePath := specPath.Child("toolRules").Index(i)
		if strings.TrimSpace(rule.Name) == "" {
			allErrs = append(allErrs, field.Required(rulePath.Child("name"), "tool rule name is required"))
			continue
		}
		if strings.TrimSpace(string(rule.Decision)) == "" {
			allErrs = append(allErrs, field.Required(rulePath.Child("decision"), "tool rule decision is required"))
		}
		if _, exists := toolNames[rule.Name]; exists {
			allErrs = append(allErrs, field.Duplicate(rulePath.Child("name"), rule.Name))
		}
		toolNames[rule.Name] = struct{}{}
	}

	if len(allErrs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(schema.GroupKind{Group: GroupVersion.Group, Kind: "MCPAccessGrant"}, r.Name, allErrs)
}

func (r *MCPAgentSession) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithValidator(mcpAgentSessionValidator{}).
		Complete()
}

type mcpAgentSessionValidator struct{}

func (mcpAgentSessionValidator) ValidateCreate(_ context.Context, obj *MCPAgentSession) (admission.Warnings, error) {
	return obj.ValidateCreate()
}

func (mcpAgentSessionValidator) ValidateUpdate(_ context.Context, oldObj *MCPAgentSession, newObj *MCPAgentSession) (admission.Warnings, error) {
	return newObj.ValidateUpdate(oldObj)
}

func (mcpAgentSessionValidator) ValidateDelete(_ context.Context, obj *MCPAgentSession) (admission.Warnings, error) {
	return obj.ValidateDelete()
}

func (r *MCPAgentSession) ValidateCreate() (admission.Warnings, error) {
	return nil, r.validate()
}

func (r *MCPAgentSession) ValidateUpdate(_ runtime.Object) (admission.Warnings, error) {
	return nil, r.validate()
}

func (r *MCPAgentSession) ValidateDelete() (admission.Warnings, error) {
	return nil, nil
}

func (r *MCPAgentSession) validate() error {
	var allErrs field.ErrorList
	specPath := field.NewPath("spec")

	if strings.TrimSpace(r.Spec.ServerRef.Name) == "" {
		allErrs = append(allErrs, field.Required(specPath.Child("serverRef", "name"), "serverRef.name is required"))
	}
	if strings.TrimSpace(r.Spec.Subject.HumanID) == "" && strings.TrimSpace(r.Spec.Subject.AgentID) == "" && strings.TrimSpace(r.Spec.Subject.TeamID) == "" {
		allErrs = append(allErrs, field.Required(specPath.Child("subject"), "one of subject.humanID, subject.agentID, or subject.teamID is required"))
	}
	if err := validateTeamIDField(specPath.Child("subject", "teamID"), r.Spec.Subject.TeamID); err != nil {
		allErrs = append(allErrs, err)
	}
	if ref := r.Spec.UpstreamTokenSecretRef; ref != nil {
		if strings.TrimSpace(ref.Name) == "" {
			allErrs = append(allErrs, field.Required(specPath.Child("upstreamTokenSecretRef", "name"), "secret name is required"))
		}
		if strings.TrimSpace(ref.Key) == "" {
			allErrs = append(allErrs, field.Required(specPath.Child("upstreamTokenSecretRef", "key"), "secret key is required"))
		}
	}

	if len(allErrs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(schema.GroupKind{Group: GroupVersion.Group, Kind: "MCPAgentSession"}, r.Name, allErrs)
}

func validToolSideEffect(sideEffect ToolSideEffect) bool {
	switch sideEffect {
	case ToolSideEffectRead, ToolSideEffectWrite, ToolSideEffectDestructive:
		return true
	default:
		return false
	}
}

func validateTeamIDField(path *field.Path, value string) *field.Error {
	teamID := strings.TrimSpace(value)
	if teamID == "" {
		return nil
	}
	if teamID != value || strings.ContainsAny(teamID, " \t\r\n") {
		return field.Invalid(path, value, "teamID must be a stable identifier without whitespace")
	}
	if len(teamID) > 128 {
		return field.TooLong(path, value, 128)
	}
	return nil
}

func (r *MCPServer) String() string {
	return fmt.Sprintf("%s/%s", r.Namespace, r.Name)
}
