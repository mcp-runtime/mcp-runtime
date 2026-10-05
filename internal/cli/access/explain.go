package access

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
	kubeapply "mcp-runtime/internal/cli/kube"
	"mcp-runtime/internal/cli/kubeerr"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/policy"
)

type explainOptions struct {
	Server     string
	Namespace  string
	HumanID    string
	AgentID    string
	TeamID     string
	SessionID  string
	Tool       string
	RPCMethod  string
	PolicyFile string
	JSON       bool
}

// explainOutput is the stable, scriptable result of access explain. Its field
// names intentionally follow the gateway audit payload vocabulary.
type explainOutput struct {
	Decision                string       `json:"decision"`
	Status                  int          `json:"status"`
	Reason                  string       `json:"reason"`
	Server                  string       `json:"server"`
	Namespace               string       `json:"namespace"`
	RPCMethod               string       `json:"rpc_method"`
	ToolName                string       `json:"tool_name,omitempty"`
	HumanID                 string       `json:"human_id,omitempty"`
	AgentID                 string       `json:"agent_id,omitempty"`
	TeamID                  string       `json:"subject_team_id,omitempty"`
	SessionID               string       `json:"session_id,omitempty"`
	PolicyVersion           string       `json:"policy_version,omitempty"`
	PolicyRevision          string       `json:"policy_revision,omitempty"`
	MatchedGrant            string       `json:"matched_grant,omitempty"`
	MatchedGrantNamespace   string       `json:"matched_grant_namespace,omitempty"`
	MatchedRule             *explainRule `json:"matched_rule,omitempty"`
	MatchedSession          string       `json:"matched_session,omitempty"`
	MatchedSessionNamespace string       `json:"matched_session_namespace,omitempty"`
	RequiredTrust           string       `json:"required_trust,omitempty"`
	RequiredSideEffect      string       `json:"required_side_effect,omitempty"`
	RiskLevel               string       `json:"risk_level,omitempty"`
	AdminTrust              string       `json:"admin_trust,omitempty"`
	ConsentedTrust          string       `json:"consented_trust,omitempty"`
	EffectiveTrust          string       `json:"effective_trust,omitempty"`
}

type explainRule struct {
	Name          string `json:"name"`
	Decision      string `json:"decision,omitempty"`
	RequiredTrust string `json:"required_trust,omitempty"`
}

func newExplainCmd(mgr *AccessManager) *cobra.Command {
	opts := explainOptions{RPCMethod: "tools/call"}
	cmd := &cobra.Command{
		Use:   "explain",
		Short: "Explain a hypothetical access decision",
		Long: `Evaluate a hypothetical MCP request against the rendered gateway policy without sending traffic.

The default policy source is the live rendered policy for --server. Use --policy-file to evaluate a local rendered policy document instead. A denied decision is printed and exits with status 1; an allowed decision exits with status 0.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mgr.ExplainAccess(opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&opts.Server, "server", "", "MCPServer name to evaluate")
	cmd.Flags().StringVar(&opts.Namespace, "namespace", core.NamespaceMCPServers, "MCPServer namespace")
	cmd.Flags().StringVar(&opts.HumanID, "human", "", "Human identity to evaluate")
	cmd.Flags().StringVar(&opts.AgentID, "agent", "", "Agent identity to evaluate")
	cmd.Flags().StringVar(&opts.TeamID, "team", "", "Team identity to evaluate")
	cmd.Flags().StringVar(&opts.SessionID, "session", "", "Agent session identity to evaluate")
	cmd.Flags().StringVar(&opts.Tool, "tool", "", "MCP tool name to evaluate")
	cmd.Flags().StringVar(&opts.RPCMethod, "rpc-method", opts.RPCMethod, "MCP JSON-RPC method to evaluate")
	cmd.Flags().StringVar(&opts.PolicyFile, "policy-file", "", "Local rendered policy JSON file (skips live policy lookup)")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Print a JSON decision for scripting")
	_ = cmd.MarkFlagRequired("server")
	return cmd
}

// ExplainAccess evaluates a hypothetical request against a local or live
// rendered policy document and writes a human or JSON explanation to out.
func (m *AccessManager) ExplainAccess(opts explainOptions, out io.Writer) error {
	server, namespace, err := core.ValidateK8sNameAndNamespace("server name", core.ErrInvalidServerName, opts.Server, opts.Namespace)
	if err != nil {
		return err
	}

	identity := policy.Identity{
		HumanID:   policy.HumanID(strings.TrimSpace(opts.HumanID)),
		AgentID:   policy.AgentID(strings.TrimSpace(opts.AgentID)),
		TeamID:    policy.TeamID(strings.TrimSpace(opts.TeamID)),
		SessionID: policy.SessionID(strings.TrimSpace(opts.SessionID)),
	}
	rpcMethod, err := core.ValidateManifestField("rpc method", opts.RPCMethod)
	if err != nil {
		return err
	}
	toolName := strings.TrimSpace(opts.Tool)
	if rpcMethod == "tools/call" {
		if identity.HumanID == "" && identity.AgentID == "" && identity.TeamID == "" {
			return core.NewWithBase(nil, "one of --human, --agent, or --team is required for tools/call")
		}
		if toolName == "" {
			return core.NewWithBase(nil, "--tool is required when --rpc-method is tools/call")
		}
		if _, err := core.ValidateManifestField("tool", toolName); err != nil {
			return err
		}
	}

	doc, err := m.loadExplainPolicy(context.Background(), server, namespace, opts.PolicyFile)
	if err != nil {
		return err
	}
	if string(doc.Server.Name) != server || string(doc.Server.Namespace) != namespace {
		return core.NewWithBase(nil, fmt.Sprintf("policy server is %s/%s, want %s/%s", doc.Server.Namespace, doc.Server.Name, namespace, server))
	}
	decision := policy.Authorize(doc, policy.Request{
		Identity:  identity,
		RPCMethod: rpcMethod,
		ToolName:  policy.ToolName(toolName),
	}, time.Now())
	result := newExplainOutput(doc, decision, server, namespace, rpcMethod, toolName, identity)
	if err := writeExplainOutput(out, result, opts.JSON); err != nil {
		return err
	}
	if !decision.Allowed {
		return core.NewWithBase(nil, fmt.Sprintf("policy denied: %s (status %d)", decision.Reason, decision.Status))
	}
	return nil
}

func (m *AccessManager) loadExplainPolicy(ctx context.Context, server, namespace, file string) (*policy.Document, error) {
	var (
		data []byte
		err  error
	)
	if strings.TrimSpace(file) != "" {
		path, pathErr := kubeapply.ResolveRegularFilePath(file)
		if pathErr != nil {
			return nil, pathErr
		}
		data, err = kubeapply.ReadFileAtPath(path)
	} else {
		plat, useKube, resolveErr := platformapi.ResolvePlatformOrKube(m.useKube)
		if resolveErr != nil {
			return nil, resolveErr
		}
		if !useKube {
			data, err = plat.GetRuntimePolicy(ctx, namespace, server)
		} else {
			configMapName := mcpdefaults.GatewayPolicyConfigMapName(server)
			args := []string{"get", "configmap", configMapName, "-n", namespace, "-o", `go-template={{index .data "policy.json"}}`}
			data, err = m.kubectl.Output(args)
			if err != nil {
				err = fmt.Errorf("%s: %w", kubeerr.DirectModeFailureMessage(fmt.Sprintf("failed to inspect rendered policy for server %q in namespace %q", server, namespace), err.Error()), err)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read policy: %w", err)
	}

	var doc policy.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode policy JSON: %w", err)
	}
	if err := policy.Validate(&doc); err != nil {
		return nil, fmt.Errorf("invalid rendered policy: %w", err)
	}
	return &doc, nil
}

func newExplainOutput(doc *policy.Document, decision policy.Decision, server, namespace, rpcMethod, toolName string, identity policy.Identity) explainOutput {
	result := explainOutput{
		Decision:                mcpdefaults.PolicyDecision,
		Status:                  decision.Status,
		Reason:                  decision.Reason,
		Server:                  server,
		Namespace:               namespace,
		RPCMethod:               rpcMethod,
		ToolName:                toolName,
		HumanID:                 string(identity.HumanID),
		AgentID:                 string(identity.AgentID),
		TeamID:                  string(identity.TeamID),
		SessionID:               string(identity.SessionID),
		PolicyVersion:           decision.PolicyVersion,
		PolicyRevision:          doc.Revision,
		MatchedGrant:            decision.MatchedGrant,
		MatchedGrantNamespace:   decision.MatchedGrantNamespace,
		MatchedSession:          decision.MatchedSession,
		MatchedSessionNamespace: decision.MatchedSessionNamespace,
		RequiredTrust:           decision.RequiredTrust,
		RequiredSideEffect:      decision.RequiredSideEffect,
		RiskLevel:               decision.RiskLevel,
		AdminTrust:              decision.AdminTrust,
		ConsentedTrust:          decision.ConsentedTrust,
		EffectiveTrust:          decision.EffectiveTrust,
	}
	if decision.Allowed {
		result.Decision = "allow"
	}
	result.MatchedRule = matchedExplainRule(doc, decision, toolName)
	return result
}

func matchedExplainRule(doc *policy.Document, decision policy.Decision, toolName string) *explainRule {
	if doc == nil || decision.MatchedGrant == "" || toolName == "" {
		return nil
	}
	for _, grant := range doc.Grants {
		if grant.Name != decision.MatchedGrant || string(grant.Namespace) != decision.MatchedGrantNamespace {
			continue
		}
		for _, rule := range grant.ToolRules {
			if string(rule.Name) == toolName {
				return &explainRule{Name: string(rule.Name), Decision: rule.Decision, RequiredTrust: rule.RequiredTrust}
			}
		}
	}
	return nil
}

func writeExplainOutput(out io.Writer, result explainOutput, asJSON bool) error {
	if out == nil {
		out = os.Stdout
	}
	if asJSON {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return fmt.Errorf("encode explanation: %w", err)
		}
		_, err = fmt.Fprintln(out, string(data))
		return err
	}
	_, err := fmt.Fprintf(out, "decision: %s (%d)\nreason: %s\nserver: %s/%s\nrpc method: %s\n", result.Decision, result.Status, result.Reason, result.Namespace, result.Server, result.RPCMethod)
	if err != nil {
		return err
	}
	if result.ToolName != "" {
		if _, err := fmt.Fprintf(out, "tool: %s\n", result.ToolName); err != nil {
			return err
		}
	}
	if result.MatchedGrant != "" {
		if _, err := fmt.Fprintf(out, "matched grant: %s/%s\n", result.MatchedGrantNamespace, result.MatchedGrant); err != nil {
			return err
		}
	}
	if result.MatchedRule != nil {
		if _, err := fmt.Fprintf(out, "matched rule: %s -> %s\n", result.MatchedRule.Name, result.MatchedRule.Decision); err != nil {
			return err
		}
	}
	if result.MatchedSession != "" {
		if _, err := fmt.Fprintf(out, "matched session: %s/%s\n", result.MatchedSessionNamespace, result.MatchedSession); err != nil {
			return err
		}
	}
	if result.AdminTrust != "" || result.ConsentedTrust != "" || result.EffectiveTrust != "" {
		if _, err := fmt.Fprintf(out, "trust: admin=%s consented=%s effective=%s\n", result.AdminTrust, result.ConsentedTrust, result.EffectiveTrust); err != nil {
			return err
		}
	}
	if result.PolicyVersion != "" {
		if _, err := fmt.Fprintf(out, "policy version: %s\n", result.PolicyVersion); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "policy revision: %s\n", result.PolicyRevision)
	return err
}
