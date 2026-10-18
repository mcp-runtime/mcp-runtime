package platform

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"strings"
)

// preflightIssue describes a cluster state problem that setup detected before
// running any steps. Fatal issues stop setup immediately; non-fatal ones are
// printed as warnings so the operator can decide whether to clean up.
type preflightIssue struct {
	fatal   bool
	message string
	cleanup []string // kubectl commands to resolve the issue
}

// preflightStep runs before every other setup step to detect stale or
// conflicting cluster state that would cause setup to fail mid-run (often
// after a long wait). It collects all issues in a single pass and prints them
// together so the operator can resolve everything before re-running.
type preflightStep struct{}

func (s preflightStep) Name() string { return "preflight" }

func (s preflightStep) Run(logger *zap.Logger, _ SetupDeps, ctx *SetupContext) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		if ctx.Plan.ExpectClusterID != "" {
			// Never proceed on an unverified cluster when an identity was requested.
			return fmt.Errorf("cannot verify --expect-cluster-id: %w", err)
		}
		// Cluster unreachable — the cluster step will surface a clearer error;
		// skip preflight so we don't double-report a connection failure.
		core.Warn("Pre-flight: cannot connect to cluster, skipping pre-flight checks")
		return nil
	}

	var issues []preflightIssue
	bg := context.Background()

	// Identity and retention guards come first: they protect production.
	issues = append(issues, checkClusterIdentity(bg, clients, ctx.Plan.ExpectClusterID)...)
	if !ctx.Plan.TestMode {
		issues = append(issues, checkExistingMCPAuthRetained(bg, clients, ctx.Plan.DeployMCPAuthServer, ctx.Plan.MCPAuthServerImage)...)
	}

	// Cluster-wide structural checks (run regardless of plan options).
	issues = append(issues, checkTerminatingNamespaces(bg, clients, ctx.Plan.DeployAnalytics)...)
	issues = append(issues, checkTerminatingCRD(bg, clients)...)
	issues = append(issues, checkStuckOperatorDeployment(bg, clients)...)

	// Registry checks.
	issues = append(issues, checkStuckRegistryPVC(bg, clients)...)
	issues = append(issues, checkStuckRegistryDeployment(bg, clients)...)
	if !ctx.Plan.TestMode {
		issues = append(issues, checkStaleRegistryIngress(bg, clients)...)
	}

	if ctx.Plan.TLSEnabled {
		// Host-based TLS checks are skipped in test mode: registry.local is
		// intentional for local Kind clusters and these checks would always fire.
		if !ctx.Plan.TestMode {
			issues = append(issues, checkRegistryHostForTLS()...)
		}
		if !ctx.Plan.ProvidedTLSSecrets {
			if !ctx.Plan.TestMode {
				issues = append(issues, checkStaleRegistryCertificate(bg, clients)...)
				issues = append(issues, checkOrphanedRegistryTLSSecret(bg, clients)...)
			}
			issues = append(issues, checkCertManagerCRDs(bg, clients, ctx.Plan.InstallCertManager)...)
			issues = append(issues, checkClusterIssuerExists(bg, clients, ctx.Plan.TLSClusterIssuer)...)
			issues = append(issues, checkFailedCertificateRequests(bg, clients)...)
			issues = append(issues, checkConflictingSecretOwners(bg, clients)...)
		}
	}
	if issuer := strings.TrimSpace(ctx.Plan.MTLSClusterIssuer); issuer != "" {
		if !ctx.Plan.TLSEnabled {
			issues = append(issues, checkCertManagerCRDs(bg, clients, ctx.Plan.InstallCertManager)...)
		}
		// The bundled mcp-runtime-ca issuer is created later by setupWorkloadPKI,
		// so it will not exist at preflight on a clean cluster — don't require it
		// up front. This mirrors the managed-provisioning condition there.
		managedIssuerWillBeCreated := ctx.Plan.TestMode && issuer == setupplan.DefaultTestMTLSClusterIssuer
		if issuer != strings.TrimSpace(ctx.Plan.TLSClusterIssuer) && !managedIssuerWillBeCreated {
			issues = append(issues, checkClusterIssuerExists(bg, clients, issuer)...)
		}
	}

	if ctx.Plan.DeployAnalytics {
		issues = append(issues, checkStaleAnalyticsJob(bg, clients)...)
		issues = append(issues, checkStalePullSecrets(bg, clients)...)
		issues = append(issues, checkPostgresPasswordSync(bg, clients)...)
		issues = append(issues, checkStuckAnalyticsStatefulSets(bg, clients)...)
	}

	if len(issues) == 0 {
		core.Info("Pre-flight checks passed")
		return nil
	}

	fmt.Println()
	hasFatal := false
	for i, issue := range issues {
		prefix := fmt.Sprintf("[%d/%d]", i+1, len(issues))
		if issue.fatal {
			hasFatal = true
			core.Error(fmt.Sprintf("Pre-flight %s %s", prefix, issue.message))
		} else {
			core.Warn(fmt.Sprintf("Pre-flight %s %s", prefix, issue.message))
		}
		if len(issue.cleanup) > 0 {
			fmt.Println()
			fmt.Println("         Run to fix:")
			for _, cmd := range issue.cleanup {
				fmt.Println("           " + cmd)
			}
			fmt.Println()
		}
	}

	if hasFatal {
		return core.NewWithBase(
			core.ErrSetupStepFailed,
			fmt.Sprintf("pre-flight checks found %d blocker(s) — resolve the issues above and re-run setup", countFatal(issues)),
		)
	}
	return nil
}

func countFatal(issues []preflightIssue) int {
	n := 0
	for _, i := range issues {
		if i.fatal {
			n++
		}
	}
	return n
}
