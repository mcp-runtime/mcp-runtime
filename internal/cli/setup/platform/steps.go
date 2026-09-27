package platform

// This file defines the setup step execution framework.
// It provides a pipeline-based approach for running setup steps with dependency injection and testability.

import (
	"fmt"
	"strings"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/registry/config"
	setupplan "mcp-runtime/internal/cli/setup/plan"
)

// SetupContext carries state shared across setup steps.
type SetupContext struct {
	Plan                  setupplan.Plan
	ExternalRegistry      *config.ExternalRegistryConfig
	UsingExternalRegistry bool
	RegistryAuthStaged    bool
	RegistrySecretName    string
	OperatorImage         string
	GatewayProxyImage     string
	AnalyticsImages       AnalyticsImageSet
}

// SetupStep models a single setup phase.
type SetupStep interface {
	Name() string
	Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error
}

// SetupPipeline provides a fluent API for building step sequences.
type SetupPipeline struct {
	steps []SetupStep
}

func NewSetupPipeline() *SetupPipeline {
	return &SetupPipeline{}
}

func (p *SetupPipeline) With(step SetupStep) *SetupPipeline {
	p.steps = append(p.steps, step)
	return p
}

func (p *SetupPipeline) WithIf(condition bool, step SetupStep) *SetupPipeline {
	if condition {
		p.steps = append(p.steps, step)
	}
	return p
}

func (p *SetupPipeline) Build() []SetupStep {
	return p.steps
}

type clusterStep struct{}

func (s clusterStep) Name() string { return "cluster" }
func (s clusterStep) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	return setupClusterSteps(logger, ctx.Plan.Kubeconfig, ctx.Plan.Context, ctx.Plan.Ingress, deps)
}

type mcpAuthPrerequisiteStep struct{}

func (s mcpAuthPrerequisiteStep) Name() string { return "mcp-auth-prerequisites" }
func (s mcpAuthPrerequisiteStep) Run(_ *zap.Logger, _ SetupDeps, ctx *SetupContext) error {
	return checkMCPAuthPrerequisites(ctx.Plan.MCPAuthSigningKeySecret, ctx.Plan.TestMode)
}

type tlsStep struct{}

func (s tlsStep) Name() string { return "tls" }
func (s tlsStep) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	return setupTLSStep(logger, ctx.Plan, deps)
}

type workloadPKIStep struct{}

func (s workloadPKIStep) Name() string { return "workload-pki" }
func (s workloadPKIStep) Run(logger *zap.Logger, _ SetupDeps, ctx *SetupContext) error {
	return setupWorkloadPKI(logger, ctx.Plan)
}

type catalogNamespaceStep struct{}

func (s catalogNamespaceStep) Name() string { return "catalog-namespace" }
func (s catalogNamespaceStep) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	return setupCatalogNamespaceStep(logger, ctx.Plan, deps)
}

type registryStep struct{}

func (s registryStep) Name() string { return "registry" }
func (s registryStep) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	return setupRegistryStep(
		logger,
		ctx.ExternalRegistry,
		ctx.UsingExternalRegistry,
		ctx.Plan.RegistryType,
		ctx.Plan.RegistryStorageSize,
		ctx.Plan.RegistryManifest,
		ctx.Plan.RegistryMode,
		ctx.Plan.TLSEnabled,
		deps,
	)
}

type operatorImageStep struct{}

func (s operatorImageStep) Name() string { return "operator-image" }
func (s operatorImageStep) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	operatorImage, gatewayProxyImage, err := prepareDeploymentImages(
		logger,
		ctx.ExternalRegistry,
		ctx.UsingExternalRegistry,
		ctx.Plan.TestMode,
		ctx.Plan.ParallelBuilds,
		deps,
	)
	if err != nil {
		return err
	}
	ctx.OperatorImage = operatorImage
	ctx.GatewayProxyImage = gatewayProxyImage
	return nil
}

type registryAuthDisableStep struct{}

func (s registryAuthDisableStep) Name() string { return "registry-auth-disable" }
func (s registryAuthDisableStep) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	if !shouldStageRegistryIngressAuth(ctx.UsingExternalRegistry) {
		return nil
	}
	if err := deps.DisableRegistryIngressAuth(); err != nil {
		return err
	}
	ctx.RegistryAuthStaged = true
	return nil
}

type deployOperatorStepCmd struct{}

func (s deployOperatorStepCmd) Name() string { return "operator-deploy" }
func (s deployOperatorStepCmd) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	return deployOperatorStep(
		logger,
		ctx.OperatorImage,
		ctx.GatewayProxyImage,
		ctx.ExternalRegistry,
		ctx.RegistrySecretName,
		ctx.UsingExternalRegistry,
		ctx.Plan.OperatorArgs,
		deps,
	)
}

type analyticsImageStep struct{}

func (s analyticsImageStep) Name() string { return "analytics-images" }
func (s analyticsImageStep) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	images, err := prepareAnalyticsImages(
		logger,
		ctx.ExternalRegistry,
		ctx.UsingExternalRegistry,
		ctx.Plan.TestMode,
		ctx.Plan.ParallelBuilds,
		deps,
	)
	if err != nil {
		return err
	}
	ctx.AnalyticsImages = images
	return nil
}

type deployAnalyticsStep struct{}

func (s deployAnalyticsStep) Name() string { return "analytics-deploy" }
func (s deployAnalyticsStep) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	return deployAnalyticsStepCmd(logger, ctx.AnalyticsImages, ctx.Plan.StorageMode, ctx.Plan.PlatformMode, deps)
}

type verifyStep struct{}

type mcpAuthServerStep struct{}

func (s mcpAuthServerStep) Name() string { return "mcp-auth-server" }
func (s mcpAuthServerStep) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	if err := deployMCPAuthServer(ctx.Plan.MCPAuthServerImage, ctx.Plan.MCPAuthIssuerURL, ctx.Plan.MCPAuthResourceURLs, ctx.Plan.MCPAuthTLSSecret, ctx.Plan.MCPAuthSigningKeySecret, ctx.Plan.MCPAuthConnectorsFile, ctx.Plan.MCPAuthConnector, ctx.Plan.TestMode, deps); err != nil {
		return err
	}
	// An old ready auth pod may still serve an obsolete resource allowlist while
	// the replacement crashes. Require the current revision before proceeding.
	if err := deps.WaitForDeploymentRolledOut(logger, "mcp-auth-server", core.DefaultAnalyticsNamespace, "app=mcp-auth-server", analyticsRolloutTimeoutDuration()); err != nil {
		return fmt.Errorf("mcp-auth authorization server rollout: %w", err)
	}
	return deps.WaitForDeploymentRolledOut(logger, "mcp-runtime-operator-controller-manager", core.NamespaceMCPRuntime, "control-plane=controller-manager", deps.GetDeploymentTimeout())
}

func (s verifyStep) Name() string { return "verify" }
func (s verifyStep) Run(logger *zap.Logger, deps SetupDeps, ctx *SetupContext) error {
	if err := verifySetup(logger, ctx.UsingExternalRegistry, deps); err != nil {
		core.Error("Post-setup verification failed")
		return err
	}
	if deps.StampPlatformVersion != nil {
		// Best effort: version metadata helps `mcp-runtime update` report the
		// installed version, but update falls back to image tags without it.
		if err := deps.StampPlatformVersion(setupImageTag()); err != nil {
			core.Warn(fmt.Sprintf("Could not record platform version metadata on Deployments: %v", err))
		}
	}
	return nil
}

// Registry auth is re-enabled by a defer in setupPlatformWithDeps so it runs
// on every exit path (success or any earlier-step failure), instead of as a
// pipeline step that would be skipped on errors. See the defer block on
// ctx.RegistryAuthStaged for details.

func buildSetupSteps(ctx *SetupContext) []SetupStep {
	catalogMode := setupplan.CatalogNamespaceForPlatformMode(ctx.Plan.PlatformMode) != ""
	return NewSetupPipeline().
		With(preflightStep{}).
		With(clusterStep{}).
		WithIf(ctx.Plan.DeployMCPAuthServer, mcpAuthPrerequisiteStep{}).
		WithIf(catalogMode, catalogNamespaceStep{}).
		WithIf(ctx.Plan.TLSEnabled, tlsStep{}).
		WithIf(strings.TrimSpace(ctx.Plan.MTLSClusterIssuer) != "", workloadPKIStep{}).
		With(registryStep{}).
		With(registryAuthDisableStep{}).
		With(operatorImageStep{}).
		WithIf(ctx.Plan.DeployAnalytics, analyticsImageStep{}).
		With(deployOperatorStepCmd{}).
		WithIf(ctx.Plan.DeployAnalytics, deployAnalyticsStep{}).
		WithIf(ctx.Plan.DeployMCPAuthServer, mcpAuthServerStep{}).
		With(verifyStep{}).
		Build()
}

func runSetupSteps(logger *zap.Logger, deps SetupDeps, ctx *SetupContext, steps []SetupStep) error {
	for _, step := range steps {
		if err := step.Run(logger, deps, ctx); err != nil {
			wrappedErr := core.WrapWithSentinelAndContext(
				core.ErrSetupStepFailed,
				err,
				fmt.Sprintf("setup step %q failed: %v", step.Name(), err),
				map[string]any{"step": step.Name(), "component": "setup"},
			)
			core.Error("Setup step failed")
			core.LogStructuredError(logger, wrappedErr, "Setup step failed")
			return wrappedErr
		}
	}
	return nil
}
