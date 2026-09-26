package access

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"mcp-runtime/internal/cli/core"
	kubeapply "mcp-runtime/internal/cli/kube"
	"mcp-runtime/internal/cli/kubeerr"
	"mcp-runtime/internal/cli/platformapi"
)

const (
	// GrantResource is the kubectl resource name for MCPAccessGrant.
	GrantResource = "mcpaccessgrant"
	// SessionResource is the kubectl resource name for MCPAgentSession.
	SessionResource = "mcpagentsession"
)

// AccessManager handles access grant and session operations.
type AccessManager struct {
	kubectl *core.KubectlClient
	logger  *zap.Logger
	// useKube forces direct Kubernetes mode; when false, commands require platform API auth.
	useKube bool
}

// NewAccessManager creates an AccessManager with explicit dependencies (tests and advanced wiring).
func NewAccessManager(kubectl *core.KubectlClient, logger *zap.Logger) *AccessManager {
	return &AccessManager{kubectl: kubectl, logger: logger}
}

// DefaultAccessManager returns an AccessManager using the shared runtime clients.
func DefaultAccessManager(runtime *core.Runtime) *AccessManager {
	return NewAccessManager(runtime.KubectlClient(), runtime.Logger())
}

// BindUseKubeFlag wires the shared --use-kube flag onto the command.
func (m *AccessManager) BindUseKubeFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().BoolVar(&m.useKube, "use-kube", false, "Use direct Kubernetes mode with kubectl; requires admin/operator cluster access (admin/dev/test only)")
}

func (m *AccessManager) accessListQueryNamespace(namespace string, allNamespaces bool) string {
	switch {
	case namespace != "":
		return namespace
	case allNamespaces:
		return ""
	default:
		return core.NamespaceMCPServers
	}
}

type accessManifestInitOptions struct {
	Kind               string
	Name               string
	Namespace          string
	Server             string
	ServerNamespace    string
	HumanID            string
	AgentID            string
	TeamID             string
	Trust              string
	SideEffects        []string
	Tools              []string
	ToolRules          []string
	Output             string
	Force              bool
	PolicyVersion      string
	UpstreamSecretName string
	UpstreamSecretKey  string
	ExpiresAt          string
	ExpiresIn          string
	Revoked            bool
}

func (m *AccessManager) InitGrantManifest(opts accessManifestInitOptions) error {
	opts.Kind = "MCPAccessGrant"
	if strings.TrimSpace(opts.Trust) == "" {
		opts.Trust = "low"
	}
	if len(opts.SideEffects) == 0 {
		opts.SideEffects = []string{"read"}
	}
	if strings.TrimSpace(opts.Output) == "" {
		opts.Output = "grant.yaml"
	}
	body, err := buildAccessManifest(opts)
	if err != nil {
		return err
	}
	if err := writeAccessManifest(opts.Output, body, opts.Force); err != nil {
		return err
	}
	core.Success(fmt.Sprintf("Wrote access grant manifest to %s", opts.Output))
	return nil
}

func (m *AccessManager) InitSessionManifest(opts accessManifestInitOptions) error {
	opts.Kind = "MCPAgentSession"
	if strings.TrimSpace(opts.Trust) == "" {
		opts.Trust = "low"
	}
	if strings.TrimSpace(opts.Output) == "" {
		opts.Output = "session.yaml"
	}
	body, err := buildAccessManifest(opts)
	if err != nil {
		return err
	}
	if err := writeAccessManifest(opts.Output, body, opts.Force); err != nil {
		return err
	}
	core.Success(fmt.Sprintf("Wrote access session manifest to %s", opts.Output))
	return nil
}

// RevokeGrantSessions revokes sessions linked to a grant through the audited platform API.
func (m *AccessManager) RevokeGrantSessions(name, namespace string) error {
	if m.useKube {
		return fmt.Errorf("revoke all grant sessions requires the platform API; omit --use-kube and authenticate with mcp-runtime auth login")
	}
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		return err
	}
	count, err := client.RevokeGrantSessions(context.Background(), strings.TrimSpace(namespace), strings.TrimSpace(name))
	if err != nil {
		return err
	}
	core.Success(fmt.Sprintf("Revoked %d session(s) linked to grant %s/%s", count, strings.TrimSpace(namespace), strings.TrimSpace(name)))
	return nil
}

func buildAccessManifest(opts accessManifestInitOptions) ([]byte, error) {
	name, namespace, err := validateAccessResourceInput(opts.Name, opts.Namespace)
	if err != nil {
		return nil, err
	}
	server, err := core.ValidateManifestField("server", opts.Server)
	if err != nil {
		return nil, err
	}
	serverNamespace := strings.TrimSpace(opts.ServerNamespace)
	if serverNamespace == "" {
		serverNamespace = namespace
	}
	serverNamespace, err = core.ValidateManifestField("server namespace", serverNamespace)
	if err != nil {
		return nil, err
	}
	subject := map[string]string{}
	if humanID := strings.TrimSpace(opts.HumanID); humanID != "" {
		subject["humanID"] = humanID
	}
	if agentID := strings.TrimSpace(opts.AgentID); agentID != "" {
		subject["agentID"] = agentID
	}
	if teamID := strings.TrimSpace(opts.TeamID); teamID != "" {
		subject["teamID"] = teamID
	}
	if len(subject) == 0 {
		return nil, core.NewWithSentinel(nil, "one of --human-id, --agent-id, or --team-id is required")
	}
	trust, err := normalizeAccessTrust(opts.Trust)
	if err != nil {
		return nil, err
	}

	spec := map[string]any{
		"serverRef": map[string]string{
			"name":      server,
			"namespace": serverNamespace,
		},
		"subject": subject,
	}
	switch opts.Kind {
	case "MCPAccessGrant":
		sideEffects, err := normalizeSideEffects(opts.SideEffects)
		if err != nil {
			return nil, err
		}
		spec["maxTrust"] = trust
		spec["allowedSideEffects"] = sideEffects
		expiresAt, err := normalizeSessionExpiry(opts.ExpiresAt, opts.ExpiresIn)
		if err != nil {
			return nil, err
		}
		if expiresAt != "" {
			spec["expiresAt"] = expiresAt
		}
		if version := strings.TrimSpace(opts.PolicyVersion); version != "" {
			spec["policyVersion"] = version
		}
		rules, err := initToolRules(opts.Tools, opts.ToolRules, trust)
		if err != nil {
			return nil, err
		}
		if len(rules) > 0 {
			spec["toolRules"] = rules
		}
	case "MCPAgentSession":
		spec["consentedTrust"] = trust
		if version := strings.TrimSpace(opts.PolicyVersion); version != "" {
			spec["policyVersion"] = version
		}
		expiresAt, err := normalizeSessionExpiry(opts.ExpiresAt, opts.ExpiresIn)
		if err != nil {
			return nil, err
		}
		if expiresAt != "" {
			spec["expiresAt"] = expiresAt
		}
		if opts.Revoked {
			spec["revoked"] = true
		}
		if secretName := strings.TrimSpace(opts.UpstreamSecretName); secretName != "" {
			secretKey := strings.TrimSpace(opts.UpstreamSecretKey)
			if secretKey == "" {
				secretKey = "token"
			}
			spec["upstreamTokenSecretRef"] = map[string]string{"name": secretName, "key": secretKey}
		}
	default:
		return nil, core.NewWithSentinel(nil, fmt.Sprintf("unsupported access manifest kind %q", opts.Kind))
	}

	doc := map[string]any{
		"apiVersion": "mcpruntime.org/v1alpha1",
		"kind":       opts.Kind,
		"metadata": map[string]string{
			"name":      name,
			"namespace": namespace,
		},
		"spec": spec,
	}
	return yaml.Marshal(doc)
}

func normalizeSessionExpiry(expiresAt, expiresIn string) (string, error) {
	expiresAt = strings.TrimSpace(expiresAt)
	expiresIn = strings.TrimSpace(expiresIn)
	if expiresAt != "" && expiresIn != "" {
		return "", core.NewWithSentinel(nil, "use either --expires-at or --expires-in, not both")
	}
	if expiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return "", core.WrapWithSentinel(nil, err, fmt.Sprintf("--expires-at must be RFC3339: %v", err))
		}
		if !parsed.After(time.Now()) {
			return "", core.NewWithSentinel(nil, "--expires-at must be in the future")
		}
		return parsed.UTC().Format(time.RFC3339), nil
	}
	if expiresIn == "" {
		return "", nil
	}
	duration, err := time.ParseDuration(expiresIn)
	if err != nil {
		return "", core.WrapWithSentinel(nil, err, fmt.Sprintf("--expires-in must be a duration like 1h or 30m: %v", err))
	}
	if duration <= 0 {
		return "", core.NewWithSentinel(nil, "--expires-in must be greater than zero")
	}
	return time.Now().UTC().Add(duration).Format(time.RFC3339), nil
}

func writeAccessManifest(path string, body []byte, force bool) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return core.NewWithSentinel(nil, "--output is required")
	}
	if _, err := os.Stat(path); err == nil && !force {
		return core.NewWithSentinel(nil, fmt.Sprintf("%s already exists; pass --force to replace it", path))
	} else if err != nil && !os.IsNotExist(err) {
		return core.WrapWithSentinel(nil, err, fmt.Sprintf("failed to inspect %q: %v", path, err))
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return core.WrapWithSentinel(nil, err, fmt.Sprintf("failed to write %q: %v", path, err))
	}
	return nil
}

func normalizeAccessTrust(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "low", "medium", "high":
		return value, nil
	default:
		return "", core.NewWithSentinel(nil, fmt.Sprintf("unsupported trust level %q (use low|medium|high)", value))
	}
}

func normalizeSideEffects(values []string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		switch value {
		case "read", "write", "destructive":
		default:
			return nil, core.NewWithSentinel(nil, fmt.Sprintf("unsupported side effect %q (use read|write|destructive)", value))
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func initToolRules(tools, explicitRules []string, trust string) ([]map[string]string, error) {
	seen := map[string]struct{}{}
	var out []map[string]string
	for _, tool := range tools {
		tool = strings.TrimSpace(tool)
		if tool == "" {
			continue
		}
		if _, ok := seen[tool]; ok {
			continue
		}
		seen[tool] = struct{}{}
		out = append(out, map[string]string{
			"name":          tool,
			"decision":      "allow",
			"requiredTrust": trust,
		})
	}
	for _, rule := range explicitRules {
		rule = strings.TrimSpace(rule)
		if rule == "" {
			continue
		}
		parts := strings.Split(rule, ":")
		if len(parts) != 3 {
			return nil, core.NewWithSentinel(nil, fmt.Sprintf("unsupported tool rule %q (use name:allow|deny:low|medium|high)", rule))
		}
		name := strings.TrimSpace(parts[0])
		decision := strings.ToLower(strings.TrimSpace(parts[1]))
		requiredTrust, err := normalizeAccessTrust(parts[2])
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, core.NewWithSentinel(nil, fmt.Sprintf("unsupported tool rule %q: tool name is required", rule))
		}
		switch decision {
		case "allow", "deny":
		default:
			return nil, core.NewWithSentinel(nil, fmt.Sprintf("unsupported tool rule %q: decision must be allow or deny", rule))
		}
		if _, ok := seen[name]; ok {
			return nil, core.NewWithSentinel(nil, fmt.Sprintf("duplicate tool rule for %q", name))
		}
		seen[name] = struct{}{}
		out = append(out, map[string]string{
			"name":          name,
			"decision":      decision,
			"requiredTrust": requiredTrust,
		})
	}
	return out, nil
}

// ListAccessResources lists grants or sessions via the platform API unless --use-kube is set.
func (m *AccessManager) ListAccessResources(resource, namespace string, allNamespaces bool) error {
	plat, kube, err := platformapi.ResolvePlatformOrKube(m.useKube)
	if err != nil {
		return err
	}
	if !kube {
		return m.listAccessPlatform(context.Background(), plat, resource, m.accessListQueryNamespace(namespace, allNamespaces))
	}

	args := []string{"get", resource}
	switch {
	case namespace != "":
		args = append(args, "-n", namespace)
	case allNamespaces:
		args = append(args, "-A")
	default:
		args = append(args, "-n", m.accessListQueryNamespace(namespace, allNamespaces))
	}

	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinelAndContext(nil, err, kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to list %s resources", resource), err.Error()), map[string]any{
			"resource":  resource,
			"namespace": namespace,
			"component": "access",
		})
	}
	return nil
}

func (m *AccessManager) listAccessPlatform(ctx context.Context, plat *platformapi.PlatformClient, resource, nsFilter string) error {
	switch resource {
	case GrantResource:
		grants, err := plat.ListGrants(ctx, nsFilter)
		if err != nil {
			return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("list grants: %v", err), map[string]any{"component": "access"})
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tNAMESPACE\tSERVER\tDISABLED")
		for _, g := range grants {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%v\n", g.Name, g.Namespace, g.ServerRef.Name, g.Disabled)
		}
		_ = tw.Flush()
		return nil
	case SessionResource:
		sessions, err := plat.ListSessions(ctx, nsFilter)
		if err != nil {
			return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("list sessions: %v", err), map[string]any{"component": "access"})
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tNAMESPACE\tSERVER\tREVOKED")
		for _, s := range sessions {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%v\n", s.Name, s.Namespace, s.ServerRef.Name, s.Revoked)
		}
		_ = tw.Flush()
		return nil
	default:
		return core.NewWithSentinel(nil, fmt.Sprintf("unsupported access resource %q", resource))
	}
}

// GetAccessResource prints one grant or session via platform API or explicit direct Kubernetes mode.
func (m *AccessManager) GetAccessResource(resource, name, namespace string) error {
	name, namespace, err := validateAccessResourceInput(name, namespace)
	if err != nil {
		return err
	}

	plat, kube, err := platformapi.ResolvePlatformOrKube(m.useKube)
	if err != nil {
		return err
	}
	if !kube {
		return m.getAccessPlatform(context.Background(), plat, resource, name, namespace)
	}

	args := []string{"get", resource, name, "-n", namespace, "-o", "yaml"}
	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinelAndContext(nil, err, kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to get %s %q in namespace %q", resource, name, namespace), err.Error()), map[string]any{
			"resource":  resource,
			"name":      name,
			"namespace": namespace,
			"component": "access",
		})
	}
	return nil
}

func (m *AccessManager) getAccessPlatform(ctx context.Context, plat *platformapi.PlatformClient, resource, name, namespace string) error {
	switch resource {
	case GrantResource:
		grant, err := plat.GetGrant(ctx, namespace, name)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(grant, "", "  ")
		_, _ = os.Stdout.Write(append(b, '\n'))
		return nil
	case SessionResource:
		session, err := plat.GetSession(ctx, namespace, name)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(session, "", "  ")
		_, _ = os.Stdout.Write(append(b, '\n'))
		return nil
	default:
		return core.NewWithSentinel(nil, fmt.Sprintf("unsupported access resource %q", resource))
	}
}

// ApplyAccessResource applies a grant or session manifest via platform API or explicit direct Kubernetes mode.
func (m *AccessManager) ApplyAccessResource(file string) error {
	m.warnAccessManifest(file)
	plat, kube, err := platformapi.ResolvePlatformOrKube(m.useKube)
	if err != nil {
		return err
	}
	if !kube {
		if err := plat.ApplyAccessFromYAMLFile(context.Background(), file); err != nil {
			return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("apply access resource from file %q: %v", file, err), map[string]any{
				"file":      file,
				"component": "access",
			})
		}
		return nil
	}
	if err := kubeapply.ApplyManifestFromFile(m.kubectl.CommandArgs, file, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinelAndContext(nil, err, kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to apply access resource from file %q", file), err.Error()), map[string]any{
			"file":      file,
			"component": "access",
		})
	}
	return nil
}

func (m *AccessManager) warnAccessManifest(file string) {
	warnings, err := accessManifestWarningsFromFile(file)
	if err != nil {
		core.Warn(fmt.Sprintf("Could not inspect %s for access identity warnings: %v", file, err))
		return
	}
	for _, warning := range warnings {
		core.Warn(warning)
	}
}

// DeleteAccessResource deletes a grant or session via platform API or explicit direct Kubernetes mode.
func (m *AccessManager) DeleteAccessResource(resource, name, namespace string) error {
	name, namespace, err := validateAccessResourceInput(name, namespace)
	if err != nil {
		return err
	}

	plat, kube, err := platformapi.ResolvePlatformOrKube(m.useKube)
	if err != nil {
		return err
	}
	if !kube {
		ctx := context.Background()
		switch resource {
		case GrantResource:
			err = plat.DeleteGrant(ctx, namespace, name)
		case SessionResource:
			err = plat.DeleteSession(ctx, namespace, name)
		default:
			return core.NewWithSentinel(nil, fmt.Sprintf("unsupported access resource %q", resource))
		}
		if err != nil {
			return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("delete %s %q: %v", resource, name, err), map[string]any{
				"resource":  resource,
				"name":      name,
				"namespace": namespace,
				"component": "access",
			})
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s %q deleted\n", resource, name)
		return nil
	}

	args := []string{"delete", resource, name, "-n", namespace}
	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinelAndContext(nil, err, kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to delete %s %q in namespace %q", resource, name, namespace), err.Error()), map[string]any{
			"resource":  resource,
			"name":      name,
			"namespace": namespace,
			"component": "access",
		})
	}
	return nil
}

// ToggleAccessResource enables/disables grants or revokes/unrevokes sessions.
func (m *AccessManager) ToggleAccessResource(resource, name, namespace string, value bool) error {
	name, namespace, err := validateAccessResourceInput(name, namespace)
	if err != nil {
		return err
	}

	plat, kube, err := platformapi.ResolvePlatformOrKube(m.useKube)
	if err != nil {
		return err
	}
	if !kube {
		ctx := context.Background()
		switch resource {
		case GrantResource:
			if value {
				err = plat.PatchGrant(ctx, namespace, name, true)
			} else {
				err = plat.PatchGrant(ctx, namespace, name, false)
			}
		case SessionResource:
			if value {
				err = plat.PatchSession(ctx, namespace, name, true)
			} else {
				err = plat.PatchSession(ctx, namespace, name, false)
			}
		default:
			return core.NewWithSentinel(nil, fmt.Sprintf("unsupported access resource %q", resource))
		}
		if err != nil {
			return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("toggle %s %q: %v", resource, name, err), map[string]any{
				"resource":  resource,
				"name":      name,
				"namespace": namespace,
				"component": "access",
			})
		}
		_, _ = fmt.Fprintf(os.Stdout, "updated %s %q\n", resource, name)
		return nil
	}

	patchValue := map[string]any{"spec": map[string]any{}}
	switch resource {
	case GrantResource:
		patchValue["spec"].(map[string]any)["disabled"] = value
	case SessionResource:
		patchValue["spec"].(map[string]any)["revoked"] = value
	default:
		return core.NewWithSentinel(nil, fmt.Sprintf("unsupported access resource %q", resource))
	}

	data, err := json.Marshal(patchValue)
	if err != nil {
		return core.WrapWithSentinelAndContext(nil, err, fmt.Sprintf("failed to marshal access patch for %s %q: %v", resource, name, err), map[string]any{
			"resource":  resource,
			"name":      name,
			"namespace": namespace,
			"component": "access",
		})
	}

	args := []string{"patch", resource, name, "-n", namespace, "--type", "merge", "--patch", string(data)}
	if err := m.kubectl.RunWithOutput(args, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithSentinelAndContext(nil, err, kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to patch %s %q in namespace %q", resource, name, namespace), err.Error()), map[string]any{
			"resource":  resource,
			"name":      name,
			"namespace": namespace,
			"component": "access",
		})
	}
	return nil
}
