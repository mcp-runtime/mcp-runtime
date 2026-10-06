package registry

// This file implements the "registry" command for managing the container registry.
// It handles registry provisioning, status checks, image pushing, and registry information display.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"mcp-runtime/internal/cli/cluster/registrycompat"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kube"
	"mcp-runtime/internal/cli/kubeerr"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/internal/cli/registry/config"
	"mcp-runtime/internal/cli/registry/ref"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/kubeworkload"
	"mcp-runtime/pkg/publishscope"
	"mcp-runtime/pkg/registrypush"
)

const defaultRegistryImage = "registry:2.8.3"
const registryImageOverrideEnv = "MCP_RUNTIME_REGISTRY_IMAGE_OVERRIDE"

var newRegistryKubernetesClients = k8sclient.New

const registryClientGoProbeTimeout = 3 * time.Second

// RegistryManager handles registry operations with injected dependencies.
type RegistryManager struct {
	kubectl *core.KubectlClient
	exec    core.Executor
	logger  *zap.Logger
}

// NewRegistryManager creates a RegistryManager with the given dependencies.
func NewRegistryManager(kubectl *core.KubectlClient, exec core.Executor, logger *zap.Logger) *RegistryManager {
	return &RegistryManager{
		kubectl: kubectl,
		exec:    exec,
		logger:  logger,
	}
}

// DefaultRegistryManager returns a RegistryManager using default clients.
func DefaultRegistryManager(logger *zap.Logger) *RegistryManager {
	return NewRegistryManager(core.DefaultKubectlClient(), core.DefaultExecutor(), logger)
}

// RunRegistryProvision contains the registry provision command flow for folder packages.
func RunRegistryProvision(mgr *RegistryManager, url, username, password, operatorImage string, dryRun bool) error {
	flagCfg := &config.ExternalRegistryConfig{
		URL:      url,
		Username: username,
		Password: password,
	}
	cfg, err := resolveExternalRegistryConfig(flagCfg)
	if err != nil {
		return err
	}
	if cfg == nil || cfg.URL == "" {
		err := core.NewWithBase(core.ErrRegistryURLRequired, "registry url is required (flag, env PROVISIONED_REGISTRY_URL, or config file)")
		core.Error("Registry URL required")
		core.LogStructuredError(mgr.logger, err, "Registry URL required")
		return err
	}
	if dryRun {
		core.Info(fmt.Sprintf("[dry-run] would save registry config: url=%s username=%q", cfg.URL, cfg.Username))
		if cfg.Username != "" && cfg.Password != "" {
			core.Info(fmt.Sprintf("[dry-run] would docker login to %s", cfg.URL))
		}
		if operatorImage != "" {
			core.Info(fmt.Sprintf("[dry-run] would build and push operator image: %s", operatorImage))
		}
		core.Success("Dry-run complete; no changes made")
		return nil
	}
	if err := config.Save(cfg); err != nil {
		wrappedErr := core.WrapWithBase(core.ErrSaveRegistryConfigFailed, err, fmt.Sprintf("failed to save registry config: %v", err))
		core.Error("Failed to save registry config")
		core.LogStructuredError(mgr.logger, wrappedErr, "Failed to save registry config")
		return wrappedErr
	}
	if cfg.Username != "" && cfg.Password != "" {
		mgr.logger.Info("Performing docker login to external registry", zap.String("url", cfg.URL))
		if err := mgr.LoginRegistry(cfg.URL, cfg.Username, cfg.Password); err != nil {
			return err
		}
	}
	if operatorImage != "" {
		mgr.logger.Info("Building and pushing operator image to external registry", zap.String("image", operatorImage))
		if err := buildOperatorImage(operatorImage); err != nil {
			wrappedErr := core.WrapWithBaseAndContext(
				core.ErrBuildOperatorImageFailed,
				err,
				fmt.Sprintf("failed to build operator image: %v", err),
				map[string]any{"image": operatorImage, "component": "registry"},
			)
			core.Error("Failed to build operator image")
			core.LogStructuredError(mgr.logger, wrappedErr, "Failed to build operator image")
			return wrappedErr
		}
		if err := pushOperatorImage(operatorImage); err != nil {
			wrappedErr := core.WrapWithBaseAndContext(
				core.ErrPushOperatorImageFailed,
				err,
				fmt.Sprintf("failed to push operator image: %v", err),
				map[string]any{"image": operatorImage, "component": "registry"},
			)
			core.Error("Failed to push operator image")
			core.LogStructuredError(mgr.logger, wrappedErr, "Failed to push operator image")
			return wrappedErr
		}
	}
	mgr.logger.Info("External registry configured", zap.String("url", cfg.URL))
	fmt.Printf("External registry configured: %s\n", cfg.URL)
	return nil
}

// RunRegistryPush pushes an image through the platform API.
func RunRegistryPush(ctx context.Context, mgr *RegistryManager, image, registryURL, name, scope string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if image == "" {
		err := core.NewWithBase(core.ErrImageRequired, "image is required (use --image)")
		core.Error("Image required")
		core.LogStructuredError(mgr.logger, err, "Image required")
		return err
	}
	platformClient, err := requirePlatformPushCredentials(ctx)
	if err != nil {
		return err
	}
	target, normalizedScope, err := buildRegistryPushTarget(ctx, mgr, platformClient, image, registryURL, name, scope, "platform")
	if err != nil {
		return err
	}

	mgr.logger.Info("Pushing image", zap.String("source", image), zap.String("target", target))
	if err := mgr.PushViaPlatform(ctx, platformClient, image, target, string(normalizedScope)); err != nil {
		return err
	}
	mgr.recordImagePublish(ctx, platformClient, image, target, "platform")
	return nil
}

// RunAdminRegistryPush pushes an image using direct Kubernetes access for
// operator debugging. Normal users should use server push instead.
func RunAdminRegistryPush(ctx context.Context, mgr *RegistryManager, image, registryURL, name, scope, mode, helperNamespace string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireAdminClusterAccess(mgr); err != nil {
		return err
	}
	if image == "" {
		err := core.NewWithBase(core.ErrImageRequired, "image is required (use --image)")
		core.Error("Image required")
		core.LogStructuredError(mgr.logger, err, "Image required")
		return err
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "in-cluster"
	}
	switch mode {
	case "direct", "in-cluster":
	default:
		err := core.NewWithBase(core.ErrUnknownRegistryMode, fmt.Sprintf("admin registry push mode must be direct or in-cluster, not %q", mode))
		core.Error("Unknown registry mode")
		core.LogStructuredError(mgr.logger, err, "Unknown registry mode")
		return err
	}

	var platformClient *platformapi.PlatformClient
	normalizedScope, err := publishscope.Normalize(scope)
	if err != nil {
		return err
	}
	if normalizedScope == publishscope.Tenant {
		platformClient, err = platformapi.NewPlatformClient()
		if err != nil {
			return fmt.Errorf("tenant scope requires platform credentials: %w", err)
		}
	}
	target, _, err := buildRegistryPushTarget(ctx, mgr, platformClient, image, registryURL, name, scope, mode)
	if err != nil {
		return err
	}

	mgr.logger.Info("Pushing image via admin Kubernetes path", zap.String("source", image), zap.String("target", target), zap.String("mode", mode))
	var pushErr error
	switch mode {
	case "direct":
		pushErr = mgr.PushDirect(image, target)
	case "in-cluster":
		pushErr = mgr.PushInCluster(image, target, helperNamespace)
	}
	if pushErr != nil {
		return pushErr
	}
	publishedRef := target
	if mode == "in-cluster" && strings.TrimSpace(registryURL) == "" {
		if pullRef, ok := adminPushPullRef(target, adminPushPublicRegistryHost(mgr.logger)); ok {
			publishedRef = pullRef
			core.Success(fmt.Sprintf("Image available to deploy as %s (pass this ref to server deploy --image)", pullRef))
		}
	}
	if platformClient != nil {
		mgr.recordImagePublish(ctx, platformClient, image, publishedRef, mode)
	}
	return nil
}

// adminPushPublicRegistryHost resolves the registry host users and nodes pull
// from (registry.<domain>), as opposed to the in-cluster endpoint the helper
// pod pushes to. It is a variable so tests can stub cluster state.
var adminPushPublicRegistryHost = func(logger *zap.Logger) string {
	return resolvePlatformRegistryURL(logger)
}

// adminPushPullRef rewrites an in-cluster push target (for example a
// ClusterIP:5000 helper destination) onto the public registry host. The
// registry stores images by repository path, so the same image is pullable
// under the public host. It returns false when there is no distinct public
// host to report.
func adminPushPullRef(target, publicHost string) (string, bool) {
	publicHost = strings.TrimSuffix(strings.TrimSpace(publicHost), "/")
	publicHost = strings.TrimPrefix(strings.TrimPrefix(publicHost, "https://"), "http://")
	host, rest, found := strings.Cut(strings.TrimSpace(target), "/")
	if !found || publicHost == "" || strings.EqualFold(host, publicHost) || rest == "" {
		return target, false
	}
	return publicHost + "/" + rest, true
}

func buildRegistryPushTarget(ctx context.Context, mgr *RegistryManager, platformClient *platformapi.PlatformClient, image, registryURL, name, scope, mode string) (string, publishscope.Scope, error) {
	normalizedScope, err := publishscope.Normalize(scope)
	if err != nil {
		return "", "", err
	}
	targetRegistry := registryURL
	if targetRegistry == "" && strings.TrimSpace(mode) == "in-cluster" {
		targetRegistry = resolveInClusterPushRegistryURL(mgr.logger)
	}
	if targetRegistry == "" {
		if ext, err := resolveExternalRegistryConfig(nil); err == nil && ext != nil && ext.URL != "" {
			targetRegistry = strings.TrimSuffix(ext.URL, "/")
		}
	}
	if targetRegistry == "" {
		targetRegistry = resolvePlatformRegistryURL(mgr.logger)
	}

	repo, tag := ref.SplitImage(image)
	if name != "" {
		repo = name
	} else {
		repo = ref.DropRegistryPrefix(repo)
	}
	if normalizedScope != "" {
		repo, err = ScopedRegistryRepository(ctx, platformClient, repo, normalizedScope)
		if err != nil {
			return "", "", err
		}
	}
	target := targetRegistry + "/" + repo
	if tag != "" {
		target = target + ":" + tag
	}
	return target, normalizedScope, nil
}

func requireAdminClusterAccess(mgr *RegistryManager) error {
	if mgr == nil || mgr.kubectl == nil {
		return core.NewWithBase(nil, kubeerr.DirectModeFailureMessage("admin registry push requires admin cluster access", "kubectl client is unavailable"))
	}
	cmd, err := mgr.kubectl.CommandArgs([]string{"cluster-info"})
	if err != nil {
		return core.NewWithBase(nil, kubeerr.DirectModeFailureMessage("admin registry push requires admin cluster access", err.Error()))
	}
	output, execErr := cmd.CombinedOutput()
	if execErr != nil {
		detail := kubeerr.CommandDetail(string(output), execErr)
		return core.NewWithBase(nil, kubeerr.DirectModeFailureMessage("admin registry push requires admin cluster access", detail))
	}
	return nil
}

// ScopedRegistryRepository applies the repository prefix implied by a publish scope.
func ScopedRegistryRepository(ctx context.Context, client *platformapi.PlatformClient, repo string, scope publishscope.Scope) (string, error) {
	repo = strings.Trim(repo, "/")
	if scope == "" || repo == "" {
		return repo, nil
	}
	if alias, ok := publishscope.RegistryAlias(scope); ok {
		return prefixRepositoryScope(repo, alias), nil
	}
	if scope != publishscope.Tenant {
		return repo, nil
	}
	if client == nil {
		return "", fmt.Errorf("resolve tenant registry scope: platform client is required")
	}
	principal, err := client.CurrentPrincipal(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve tenant registry scope: %w", err)
	}
	return scopedTenantRegistryRepository(repo, principal)
}

func prefixRepositoryScope(repo, scope string) string {
	if repo == scope || strings.HasPrefix(repo, scope+"/") {
		return repo
	}
	return scope + "/" + repo
}

func scopedTenantRegistryRepository(repo string, principal platformapi.Principal) (string, error) {
	scopes := tenantRegistryScopes(principal)
	if len(scopes) == 0 {
		return "", fmt.Errorf("resolve tenant registry scope: authenticated principal has no team membership")
	}
	if strings.Contains(repo, "/") {
		prefix := strings.TrimSpace(strings.SplitN(repo, "/", 2)[0])
		if stringInSlice(prefix, scopes) {
			return repo, nil
		}
		return "", fmt.Errorf("resolve tenant registry scope: repository must be scoped to one of your teams (%s)", strings.Join(quoteStrings(scopes), " or "))
	}
	if len(principal.Teams) > 1 {
		return "", fmt.Errorf("resolve tenant registry scope: multiple team memberships; use a team-scoped repository such as <team>/%s", repo)
	}
	return scopes[0] + "/" + repo, nil
}

func tenantRegistryScopes(principal platformapi.Principal) []string {
	scopes := make([]string, 0, len(principal.Teams)*2)
	for _, team := range principal.Teams {
		if slug := strings.TrimSpace(team.Slug); slug != "" {
			scopes = append(scopes, slug)
		}
		if namespace := strings.TrimSpace(team.Namespace); namespace != "" {
			scopes = append(scopes, namespace)
		}
	}
	return dedupeStrings(scopes)
}

func stringInSlice(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func quoteStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, fmt.Sprintf("%q", value))
	}
	return out
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func requirePlatformPushCredentials(ctx context.Context) (*platformapi.PlatformClient, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	client, err := platformapi.NewPlatformClient()
	if err != nil {
		return nil, fmt.Errorf("server push requires platform credentials; run mcp-runtime auth login or set MCP_PLATFORM_API_TOKEN with a saved or explicit MCP_PLATFORM_API_URL: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := client.ValidateCredentials(ctx); err != nil {
		return nil, fmt.Errorf("registry push credentials were rejected: %w", err)
	}
	return client, nil
}

// PushViaPlatform saves the local image and asks the platform API to push it in-cluster.
func (m *RegistryManager) PushViaPlatform(ctx context.Context, client *platformapi.PlatformClient, source, target, scope string) error {
	if client == nil {
		return fmt.Errorf("platform client is required")
	}
	core.Info(fmt.Sprintf("Saving local image %s to a temporary archive", source))
	tmpFile, err := os.CreateTemp("", "mcp-img-*.tar")
	if err != nil {
		return core.WrapWithBase(core.ErrCreateTempFileFailed, err, fmt.Sprintf("failed to create temp file: %v", err))
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return core.WrapWithBase(core.ErrCloseTempFileFailed, err, fmt.Sprintf("failed to close temp file: %v", err))
	}
	defer os.Remove(tmpPath)

	saveCmd, err := m.exec.Command("docker", []string{"save", "-o", tmpPath, source})
	if err != nil {
		return err
	}
	saveCmd.SetStdout(os.Stdout)
	saveCmd.SetStderr(os.Stderr)
	if err := saveCmd.Run(); err != nil {
		return core.WrapWithBaseAndContext(
			core.ErrSaveImageFailed,
			err,
			fmt.Sprintf("failed to save image: %v", err),
			map[string]any{"source": source, "component": "registry"},
		)
	}
	if info, err := os.Stat(tmpPath); err == nil {
		core.Info(fmt.Sprintf("Saved image archive (%s); uploading to platform API", formatPushArchiveSize(info.Size())))
	} else {
		core.Info("Saved image archive; uploading to platform API")
	}

	uploadCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	core.Info(fmt.Sprintf("Uploading image for publish target %s", target))
	if err := client.PushRegistryImage(uploadCtx, tmpPath, target, scope); err != nil {
		return core.WrapWithBaseAndContext(
			core.ErrPushImageInClusterFailed,
			err,
			fmt.Sprintf("failed to push image via platform API: %v", err),
			map[string]any{"target": target, "component": "registry"},
		)
	}
	core.Success(fmt.Sprintf("Pushed %s via platform API", target))
	return nil
}

func formatPushArchiveSize(size int64) string {
	const mib = 1024 * 1024
	if size <= 0 {
		return "size unknown"
	}
	return fmt.Sprintf("%.1f MiB", float64(size)/mib)
}

func (m *RegistryManager) recordImagePublish(ctx context.Context, client *platformapi.PlatformClient, source, target, mode string) {
	if client == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := client.RecordImagePublish(ctx, platformapi.ImagePublishRecord{
		ImageRef:    target,
		SourceImage: source,
		Mode:        mode,
	}); err != nil {
		m.logger.Debug("image publish audit event was not recorded", zap.Error(err), zap.String("image", target))
	}
}

// resolveExternalRegistryConfig returns the external registry config using precedence:
// CLI flags > environment variables (PROVISIONED_REGISTRY_*) > config file.
// Returns (nil, nil) if no source provides a URL.
func resolveExternalRegistryConfig(flagCfg *config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
	cfg, err := config.Resolve(flagCfg, registryConfigEnv())
	if err == nil {
		return cfg, nil
	}
	if errors.Is(err, config.ErrURLRequired) {
		wrapped := core.NewWithBase(core.ErrRegistryURLRequired, "registry url is required")
		core.Error("Registry URL required")
		return nil, wrapped
	}
	if errors.Is(err, config.ErrURLMissingInConfig) {
		return nil, core.NewWithBase(core.ErrRegistryURLMissingInConfig, "registry url missing in config")
	}
	return nil, err
}

func ResolveExternalRegistryConfig(flagCfg *config.ExternalRegistryConfig) (*config.ExternalRegistryConfig, error) {
	return resolveExternalRegistryConfig(flagCfg)
}

func registryConfigEnv() config.Env {
	return config.Env{
		URL:      core.DefaultCLIConfig.ProvisionedRegistryURL,
		Username: core.DefaultCLIConfig.ProvisionedRegistryUsername,
		Password: core.DefaultCLIConfig.ProvisionedRegistryPassword,
	}
}

func deployRegistry(logger *zap.Logger, namespace string, port int, registryType, registryStorageSize, manifestPath string) error {
	logger.Info("Deploying container registry", zap.String("namespace", namespace), zap.String("type", registryType))

	if registryType == "" {
		registryType = "docker"
	}

	switch registryType {
	case "docker":
		// continue
	default:
		err := core.NewWithBase(core.ErrUnsupportedRegistryType, fmt.Sprintf("unsupported registry type %q (supported: docker; harbor coming soon)", registryType))
		core.Error("Unsupported registry type")
		core.LogStructuredError(logger, err, "Unsupported registry type")
		return err
	}

	if manifestPath == "" {
		manifestPath = "config/registry"
	}

	// Ensure Namespace
	if err := ensureNamespace(namespace); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrEnsureNamespaceFailed,
			err,
			fmt.Sprintf("failed to ensure namespace: %v", err),
			map[string]any{"namespace": namespace, "component": "registry"},
		)
		core.Error("Failed to ensure namespace")
		core.LogStructuredError(logger, wrappedErr, "Failed to ensure namespace")
		return wrappedErr
	}
	// Apply registry manifests via kustomize with namespace override
	logger.Info("Applying registry manifests")
	overrideImage := strings.TrimSpace(os.Getenv(registryImageOverrideEnv))
	manifest, err := renderKustomizeManifest(core.DefaultKubectlClient(), manifestPath)
	if err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrDeployRegistryFailed,
			err,
			fmt.Sprintf("failed to render registry manifest %q: %v", manifestPath, err),
			map[string]any{"namespace": namespace, "manifest_path": manifestPath, "registry_type": registryType, "component": "registry"},
		)
		core.Error("Failed to render registry manifest")
		core.LogStructuredError(logger, wrappedErr, "Failed to render registry manifest")
		return wrappedErr
	}
	registryHost := core.GetRegistryIngressHost()
	clients, clientsErr := registryKubernetesClients()
	if clientsErr == nil {
		var source string
		registryHost, source = k8sclient.ResolveRegistryPublicHost(context.Background(), clients, registryHost)
		logger.Info("Resolved registry ingress host", zap.String("host", registryHost), zap.String("source", source))
	}
	manifest = rewriteRegistryHost(manifest, registryHost)
	manifest = stripRegistryClusterIssuerAnnotation(manifest)
	if clientsErr == nil {
		manifest, err = preserveNativeRegistryManifest(context.Background(), clients.Clientset, namespace, manifest)
		if err != nil {
			return err
		}
	}
	if clientsErr == nil {
		if err := k8sclient.CheckRegistryIngressManifest(context.Background(), clients, manifest); err != nil {
			wrappedErr := core.WrapWithBaseAndContext(
				core.ErrDeployRegistryFailed,
				err,
				err.Error(),
				map[string]any{"namespace": namespace, "manifest_path": manifestPath, "registry_type": registryType, "component": "registry"},
			)
			core.Error("Refusing to downgrade registry ingress host")
			core.LogStructuredError(logger, wrappedErr, "Refusing to downgrade registry ingress host")
			return wrappedErr
		}
	}
	if overrideImage != "" {
		logger.Info("Applying registry image override", zap.String("image", overrideImage))
		updated := strings.Replace(manifest, "image: "+defaultRegistryImage, "image: "+overrideImage, 1)
		if updated == manifest {
			err := fmt.Errorf("registry image reference %q not found in manifest", defaultRegistryImage)
			wrappedErr := core.WrapWithBaseAndContext(
				core.ErrDeployRegistryFailed,
				err,
				err.Error(),
				map[string]any{"namespace": namespace, "manifest_path": manifestPath, "registry_type": registryType, "component": "registry"},
			)
			core.Error("Failed to rewrite registry image")
			core.LogStructuredError(logger, wrappedErr, "Failed to rewrite registry image")
			return wrappedErr
		}
		if err := kube.ApplyManifestContentWithNamespace(core.DefaultKubectlClient().CommandArgs, updated, namespace); err != nil {
			wrappedErr := core.WrapWithBaseAndContext(
				core.ErrDeployRegistryFailed,
				err,
				fmt.Sprintf("failed to deploy registry with image override %q: %v", overrideImage, err),
				map[string]any{"namespace": namespace, "manifest_path": manifestPath, "registry_type": registryType, "component": "registry"},
			)
			core.Error("Failed to deploy registry")
			core.LogStructuredError(logger, wrappedErr, "Failed to deploy registry")
			return wrappedErr
		}
	} else {
		if err := kube.ApplyManifestContentWithNamespace(core.DefaultKubectlClient().CommandArgs, manifest, namespace); err != nil {
			wrappedErr := core.WrapWithBaseAndContext(
				core.ErrDeployRegistryFailed,
				err,
				fmt.Sprintf("failed to deploy registry: %v", err),
				map[string]any{"namespace": namespace, "manifest_path": manifestPath, "registry_type": registryType, "component": "registry"},
			)
			core.Error("Failed to deploy registry")
			core.LogStructuredError(logger, wrappedErr, "Failed to deploy registry")
			return wrappedErr
		}
	}

	if err := applyRegistryCompatibilityOverlay(logger, namespace, manifestPath); err != nil {
		return err
	}

	if err := ensureRegistryStorageSize(logger, namespace, registryStorageSize); err != nil {
		return err
	}

	// Wait for registry to be ready
	logger.Info("Waiting for registry to be ready")
	deployTimeout := 5 * time.Minute
	if err := waitForDeploymentAvailable(logger, "registry", namespace, "app=registry", deployTimeout); err != nil {
		logger.Warn("Registry deployment may still be in progress", zap.Error(err))
	}

	logger.Info("Registry deployed successfully")
	return nil
}

func DeployRegistry(logger *zap.Logger, namespace string, port int, registryType, registryStorageSize, manifestPath string) error {
	return deployRegistry(logger, namespace, port, registryType, registryStorageSize, manifestPath)
}

func applyRegistryCompatibilityOverlay(logger *zap.Logger, namespace, manifestPath string) error {
	overlaySubPath := registrycompat.OverlayPath(core.DefaultKubectlClient())
	if overlaySubPath == "" {
		return nil
	}
	compatPath := registrycompat.ResolveOverlayPath(manifestPath, overlaySubPath)
	if logger != nil {
		logger.Info("Applying registry compatibility overlay", zap.String("path", compatPath))
	}
	manifest, err := renderKustomizeManifest(core.DefaultKubectlClient(), compatPath)
	if err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrDeployRegistryFailed,
			err,
			fmt.Sprintf("failed to render registry compatibility overlay %q: %v", compatPath, err),
			map[string]any{"namespace": namespace, "manifest_path": compatPath, "component": "registry"},
		)
		core.Error("Failed to render registry compatibility overlay")
		core.LogStructuredError(logger, wrappedErr, "Failed to render registry compatibility overlay")
		return wrappedErr
	}
	if clients, err := registryKubernetesClients(); err == nil {
		manifest, err = preserveNativeRegistryManifest(context.Background(), clients.Clientset, namespace, manifest)
		if err != nil {
			return err
		}
	}
	if err := kube.ApplyManifestContentWithNamespace(core.DefaultKubectlClient().CommandArgs, manifest, namespace); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrDeployRegistryFailed,
			err,
			fmt.Sprintf("failed to apply registry compatibility overlay: %v", err),
			map[string]any{"namespace": namespace, "manifest_path": compatPath, "component": "registry"},
		)
		core.Error("Failed to apply registry compatibility overlay")
		core.LogStructuredError(logger, wrappedErr, "Failed to apply registry compatibility overlay")
		return wrappedErr
	}
	return nil
}

func ensureNamespace(namespace string) error {
	clients, err := registryKubernetesClients()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), registryClientGoProbeTimeout)
		defer cancel()
		if err := k8sclient.EnsureNamespace(ctx, clients, namespace, nil); err == nil {
			return nil
		}
	}
	return kube.EnsureNamespace(core.DefaultKubectlClient().CommandArgs, namespace)
}

func registryKubernetesClients() (*k8sclient.Clients, error) {
	return newRegistryKubernetesClients()
}

func buildOperatorImage(image string) error {
	cmd, err := core.ExecCommandWithValidators("make", []string{"-f", "Makefile.operator", "docker-build-operator-no-test", "IMG=" + image})
	if err != nil {
		return err
	}
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	return cmd.Run()
}

func pushOperatorImage(image string) error {
	cmd, err := core.ExecCommandWithValidators("docker", []string{"push", image})
	if err != nil {
		return err
	}
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	return cmd.Run()
}

func waitForDeploymentAvailable(logger *zap.Logger, name, namespace, selector string, timeout time.Duration) error {
	if logger != nil {
		logger.Info("Waiting for deployment rollout", zap.String("deployment", name), zap.String("namespace", namespace), zap.String("selector", selector))
	}
	clients, err := registryKubernetesClients()
	if err == nil {
		waitCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := k8sclient.WaitForDeploymentRolledOut(waitCtx, clients, namespace, name, timeout); err == nil {
			return nil
		} else if logger != nil {
			logger.Debug("Client-go deployment wait failed, falling back to kubectl rollout status", zap.Error(err))
		}
	}
	return core.DefaultKubectlClient().RunWithOutput([]string{
		"rollout", "status",
		"deployment/" + name,
		"-n", namespace,
		"--timeout=" + timeout.String(),
	}, os.Stdout, os.Stderr)
}

func rewriteRegistryHost(manifest, host string) string {
	host = strings.TrimSpace(host)
	if host == "" || host == "registry.local" {
		return manifest
	}
	return strings.ReplaceAll(manifest, "registry.local", host)
}

// stripRegistryClusterIssuerAnnotation keeps registry TLS owned by the explicit
// registry/registry-cert Certificate instead of cert-manager ingress-shim.
func stripRegistryClusterIssuerAnnotation(manifest string) string {
	lines := strings.SplitAfter(manifest, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "cert-manager.io/cluster-issuer:") || strings.HasPrefix(trimmed, `"cert-manager.io/cluster-issuer":`) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "")
}

func renderKustomizeManifest(kubectl core.KubectlRunner, manifestPath string) (string, error) {
	renderCmd, err := kubectl.CommandArgs([]string{"kustomize", manifestPath})
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	renderCmd.SetStdout(&stdout)
	renderCmd.SetStderr(&stderr)
	if err := renderCmd.Run(); err != nil {
		return "", fmt.Errorf("kubectl kustomize %s failed: %w (%s)", manifestPath, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func ensureRegistryStorageSize(logger *zap.Logger, namespace, registryStorageSize string) error {
	storageSize := strings.TrimSpace(registryStorageSize)
	if storageSize == "" {
		return nil
	}
	clients, err := registryKubernetesClients()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), registryClientGoProbeTimeout)
		defer cancel()
		currentSize, err := k8sclient.PersistentVolumeClaimStorage(ctx, clients, namespace, core.RegistryPVCName)
		if err == nil {
			if currentSize == storageSize {
				logger.Info("Registry storage size already matches requested value", zap.String("size", storageSize))
				return nil
			}
			logger.Info("Updating registry storage size", zap.String("from", currentSize), zap.String("to", storageSize))
			if err := k8sclient.UpdatePersistentVolumeClaimStorage(ctx, clients, namespace, core.RegistryPVCName, storageSize); err == nil {
				return nil
			} else if logger != nil {
				logger.Debug("Client-go PVC storage update failed, falling back to kubectl patch", zap.Error(err))
			}
		} else if logger != nil {
			logger.Debug("Client-go PVC storage read failed, falling back to kubectl get", zap.Error(err))
		}
	}

	// #nosec G204 -- fixed kubectl command, namespace from internal config.
	getCmd, err := core.DefaultKubectlClient().CommandArgs([]string{"get", "pvc", core.RegistryPVCName, "-n", namespace, "-o", "jsonpath={.spec.resources.requests.storage}"})
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	getCmd.SetStdout(&stdout)
	getCmd.SetStderr(&stderr)
	if err := getCmd.Run(); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrReadRegistryStorageFailed,
			err,
			fmt.Sprintf("failed to read current registry storage size: %v (%s)", err, strings.TrimSpace(stderr.String())),
			map[string]any{"namespace": namespace, "pvc": core.RegistryPVCName, "component": "registry"},
		)
		core.Error("Failed to read registry storage size")
		core.LogStructuredError(logger, wrappedErr, "Failed to read registry storage size")
		return wrappedErr
	}

	currentSize := strings.TrimSpace(stdout.String())
	if currentSize == storageSize {
		logger.Info("Registry storage size already matches requested value", zap.String("size", storageSize))
		return nil
	}

	logger.Info("Updating registry storage size", zap.String("from", currentSize), zap.String("to", storageSize))
	patchPayload := fmt.Sprintf(`{"spec":{"resources":{"requests":{"storage":"%s"}}}}`, storageSize)
	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	if err := core.DefaultKubectlClient().RunWithOutput([]string{"patch", "pvc", core.RegistryPVCName, "-n", namespace, "-p", patchPayload}, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrUpdateRegistryStorageFailed,
			err,
			fmt.Sprintf("failed to update registry storage size to %s: %v", storageSize, err),
			map[string]any{"namespace": namespace, "pvc": core.RegistryPVCName, "storage_size": storageSize, "component": "registry"},
		)
		core.Error("Failed to update registry storage size")
		core.LogStructuredError(logger, wrappedErr, "Failed to update registry storage size")
		return wrappedErr
	}

	return nil
}

// CheckRegistryStatus checks and displays registry status.
func (m *RegistryManager) CheckRegistryStatus(namespace string) error {
	m.logger.Info("Checking registry status")

	core.Header("Registry Status")
	core.DefaultPrinter.Println()

	replicas, endpoint, podPhase, err := registryStatusWithClientGo(namespace)
	if err != nil {
		readyOut, readyErr := m.kubectl.Output([]string{"get", "deployment", core.RegistryDeploymentName, "-n", namespace, "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"})
		if readyErr != nil {
			core.Error("Registry deployment not found")
			return readyErr
		}
		ipOut, _ := m.kubectl.Output([]string{"get", "service", core.RegistryServiceName, "-n", namespace, "-o", "jsonpath={.spec.clusterIP}:{.spec.ports[0].port}"})
		podOut, _ := m.kubectl.Output([]string{"get", "pods", "-n", namespace, "-l", core.SelectorRegistry, "-o", "jsonpath={.items[0].status.phase}"})
		replicas = strings.TrimSpace(string(readyOut))
		endpoint = strings.TrimSpace(string(ipOut))
		podPhase = strings.TrimSpace(string(podOut))
	}

	// Build status table
	status := core.Green("Healthy")
	if replicas == "" || strings.HasPrefix(replicas, "/") || strings.HasPrefix(replicas, "0/") {
		status = core.Yellow("Starting")
	}

	tableData := [][]string{
		{"Property", "Value"},
		{"Status", status},
		{"Replicas", replicas},
		{"Endpoint", endpoint},
		{"Pod Phase", podPhase},
	}

	core.TableBoxed(tableData)

	return nil
}

// LoginRegistry logs into a container registry.
func (m *RegistryManager) LoginRegistry(registryURL, username, password string) error {
	m.logger.Info("Logging into registry", zap.String("url", registryURL))

	// #nosec G204 -- credentials from validated config; password via stdin (not command line).
	cmd, err := m.exec.Command("docker", []string{"login", "-u", username, "--password-stdin", registryURL})
	if err != nil {
		return err
	}
	cmd.SetStdin(strings.NewReader(password))
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)

	if err := cmd.Run(); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrRegistryLoginFailed,
			err,
			fmt.Sprintf("failed to login to registry: %v", err),
			map[string]any{"registry_url": registryURL, "component": "registry"},
		)
		core.Error("Failed to login to registry")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to login to registry")
		return wrappedErr
	}

	m.logger.Info("Successfully logged into registry")
	return nil
}

// ShowRegistryInfo displays registry connection information.
func (m *RegistryManager) ShowRegistryInfo() error {
	ns := core.NamespaceRegistry
	host, ip, p, err := registryInfoWithClientGo(ns)
	if err != nil {
		ingressHost, ingressErr := m.kubectl.Output([]string{"get", "ingress", core.RegistryServiceName, "-n", ns, "-o", "jsonpath={.spec.rules[0].host}"})
		if ingressErr != nil {
			m.logger.Debug("Failed to get registry ingress host", zap.Error(ingressErr))
		}
		clusterIP, ipErr := m.kubectl.Output([]string{"get", "service", core.RegistryServiceName, "-n", ns, "-o", "jsonpath={.spec.clusterIP}"})
		if ipErr != nil {
			m.logger.Debug("Failed to get registry cluster IP", zap.Error(ipErr))
		}
		port, portErr := m.kubectl.Output([]string{"get", "service", core.RegistryServiceName, "-n", ns, "-o", "jsonpath={.spec.ports[0].port}"})
		if portErr != nil {
			m.logger.Debug("Failed to get registry port", zap.Error(portErr))
		}
		host = strings.TrimSpace(string(ingressHost))
		ip = strings.TrimSpace(string(clusterIP))
		p = strings.TrimSpace(string(port))
	}

	if ip != "" && p != "" {
		core.Header("Registry Information")
		core.DefaultPrinter.Println()

		tableData := [][]string{
			{"Property", "Value"},
			{"Ingress Host", host},
			{"Internal URL", fmt.Sprintf("%s:%s", ip, p)},
			{"Service DNS", fmt.Sprintf("registry.registry.svc.cluster.local:%s", p)},
		}
		core.TableBoxed(tableData)

		core.DefaultPrinter.Println()
		core.Section("Local Access")
		if host != "" {
			core.Info("Option 1: Use the ingress host:")
			core.DefaultPrinter.Printf("  %s\n", host)
			core.DefaultPrinter.Println()
			core.Info("If running without TLS, add the ingress host to your runtime's insecure registry list.")
			core.DefaultPrinter.Println()
		}
		core.Info("Option 2: Add the internal service IP to /etc/docker/daemon.json:")
		core.DefaultPrinter.Printf("  \"insecure-registries\": [\"%s:%s\"]\n", ip, p)
		core.DefaultPrinter.Println()
		core.Info("Option 3: Use port-forward:")
		core.DefaultPrinter.Printf("  kubectl port-forward -n registry svc/registry %s:%s\n", p, p)
		core.DefaultPrinter.Printf("  Then use: localhost:%s\n", p)
	} else {
		core.Warn("Registry not found. Deploy it with: mcp-runtime setup")
	}

	return nil
}

// PushDirect pushes an image directly using docker.
func (m *RegistryManager) PushDirect(source, target string) error {
	// #nosec G204 -- source/target are image references from internal push logic.
	tagCmd, err := m.exec.Command("docker", []string{"tag", source, target})
	if err != nil {
		return err
	}
	tagCmd.SetStdout(os.Stdout)
	tagCmd.SetStderr(os.Stderr)
	if err := tagCmd.Run(); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrTagImageFailed,
			err,
			fmt.Sprintf("failed to tag image: %v", err),
			map[string]any{"source": source, "target": target, "component": "registry"},
		)
		core.Error("Failed to tag image")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to tag image")
		return wrappedErr
	}

	// #nosec G204 -- target is image reference from internal push logic.
	pushCmd, err := m.exec.Command("docker", []string{"push", target})
	if err != nil {
		return err
	}
	pushCmd.SetStdout(os.Stdout)
	pushCmd.SetStderr(os.Stderr)
	if err := pushCmd.Run(); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrPushImageFailed,
			err,
			fmt.Sprintf("failed to push image: %v", err),
			map[string]any{"target": target, "component": "registry"},
		)
		core.Error("Failed to push image")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to push image")
		return wrappedErr
	}

	core.Success(fmt.Sprintf("Pushed %s", target))
	return nil
}

// PushInCluster pushes an image using an in-cluster helper pod.
func (m *RegistryManager) PushInCluster(source, target, helperNS string) error {
	helperName := fmt.Sprintf("registry-pusher-%d", time.Now().UnixNano())

	// #nosec G204 -- helperNS from CLI flag, kubectl validates namespace names.
	if err := m.kubectl.Run([]string{"get", "namespace", helperNS}); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrHelperNamespaceNotFound,
			err,
			fmt.Sprintf("helper namespace %q not found (create it or pass --namespace): %v", helperNS, err),
			map[string]any{"namespace": helperNS, "component": "registry"},
		)
		core.Error("Helper namespace not found")
		core.LogStructuredError(m.logger, wrappedErr, "Helper namespace not found")
		return wrappedErr
	}

	// Ensure source is saved to tar; use CWD to satisfy kubectl path validation.
	tmpFile, err := os.CreateTemp(".", "mcp-img-*.tar")
	if err != nil {
		wrappedErr := core.WrapWithBase(core.ErrCreateTempFileFailed, err, fmt.Sprintf("failed to create temp file: %v", err))
		core.Error("Failed to create temp file")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to create temp file")
		return wrappedErr
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		wrappedErr := core.WrapWithBase(core.ErrCloseTempFileFailed, err, fmt.Sprintf("failed to close temp file: %v", err))
		core.Error("Failed to close temp file")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to close temp file")
		return wrappedErr
	}
	defer os.Remove(tmpPath)

	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	saveCmd, err := m.exec.Command("docker", []string{"save", "-o", tmpPath, source})
	if err != nil {
		return err
	}
	saveCmd.SetStdout(os.Stdout)
	saveCmd.SetStderr(os.Stderr)
	if err := saveCmd.Run(); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrSaveImageFailed,
			err,
			fmt.Sprintf("failed to save image: %v", err),
			map[string]any{"source": source, "component": "registry"},
		)
		core.Error("Failed to save image")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to save image")
		return wrappedErr
	}

	publisherSecret := ""
	if registryNativeAuthActive() {
		if helperNS != core.NamespaceRegistry {
			return core.NewWithBase(core.ErrStartHelperPodFailed, fmt.Sprintf("native registry publication helpers must run in the %s namespace", core.NamespaceRegistry))
		}
		publisherSecret = registryPublisherSecret
	}
	overrides, err := registryPushHelperOverrides(helperName, publisherSecret)
	if err != nil {
		wrappedErr := core.WrapWithBase(core.ErrStartHelperPodFailed, err, fmt.Sprintf("failed to build helper pod security overrides: %v", err))
		core.Error("Failed to build helper pod security overrides")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to build helper pod security overrides")
		return wrappedErr
	}

	// Start helper pod with skopeo
	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	if err := m.kubectl.RunWithOutput([]string{"run", helperName, "-n", helperNS, "--image=" + core.GetSkopeoImage(), "--restart=Never", "--overrides=" + overrides, "--command", "--", "sh", "-c", "while true; do sleep 3600; done"}, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrStartHelperPodFailed,
			err,
			fmt.Sprintf("failed to start helper pod: %v", err),
			map[string]any{"pod": helperName, "namespace": helperNS, "component": "registry"},
		)
		core.Error("Failed to start helper pod")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to start helper pod")
		return wrappedErr
	}
	defer func() {
		// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
		_ = m.kubectl.Run([]string{"delete", "pod", helperName, "-n", helperNS, "--ignore-not-found"})
	}()

	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	timeout := core.GetHelperPodTimeout()
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if err := m.kubectl.RunWithOutput([]string{"wait", "--for=condition=Ready", "pod/" + helperName, "-n", helperNS, "--timeout=" + timeout.String()}, os.Stdout, os.Stderr); err != nil {
		// Best-effort diagnostics for common real-cluster failures (DiskPressure, taints, quotas, etc).
		_ = m.kubectl.RunWithOutput([]string{"describe", "pod", helperName, "-n", helperNS, "--request-timeout=10s"}, os.Stdout, os.Stderr)
		_ = m.kubectl.RunWithOutput([]string{"get", "events", "-n", helperNS, "--request-timeout=10s", "--field-selector", "involvedObject.name=" + helperName, "--sort-by=.lastTimestamp"}, os.Stdout, os.Stderr)
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrHelperPodNotReady,
			err,
			fmt.Sprintf("helper pod not ready: %v", err),
			map[string]any{"pod": helperName, "namespace": helperNS, "timeout": timeout.String(), "component": "registry"},
		)
		core.Error("Helper pod not ready")
		core.LogStructuredError(m.logger, wrappedErr, "Helper pod not ready")
		return wrappedErr
	}

	// Copy tar into pod
	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	if err := m.kubectl.RunWithOutput([]string{"cp", tmpPath, fmt.Sprintf("%s/%s:%s", helperNS, helperName, "/tmp/image.tar")}, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrCopyImageToHelperFailed,
			err,
			fmt.Sprintf("failed to copy image tar to helper pod: %v", err),
			map[string]any{"pod": helperName, "namespace": helperNS, "component": "registry"},
		)
		core.Error("Failed to copy image to helper pod")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to copy image to helper pod")
		return wrappedErr
	}

	// The helper pod uses cluster DNS, which does not resolve the external ingress host
	// (e.g. registry.local). Rewrite the destination host to the in-cluster registry
	// service DNS so skopeo can reach the registry from inside the cluster. The Docker
	// registry stores images by repository path, so the resulting image is still
	// addressable via any hostname that routes to the same registry service.
	pushTarget := rewriteTargetHostForInClusterPush(target, m.kubectl)

	// Push using skopeo from inside cluster (registry is http, so disable tls verify)
	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	skopeoArgs := []string{"exec", "-n", helperNS, helperName, "--", "skopeo", "copy", "--dest-tls-verify=false"}
	if publisherSecret != "" {
		skopeoArgs = append(skopeoArgs, "--dest-authfile="+registryPublisherAuthFile)
	}
	skopeoArgs = append(skopeoArgs, "docker-archive:/tmp/image.tar", "docker://"+pushTarget)
	if err := m.kubectl.RunWithOutput(skopeoArgs, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrPushImageFromHelperFailed,
			err,
			fmt.Sprintf("failed to push image from helper pod: %v", err),
			map[string]any{"pod": helperName, "namespace": helperNS, "target": target, "push_target": pushTarget, "component": "registry"},
		)
		core.Error("Failed to push image from helper pod")
		core.LogStructuredError(m.logger, wrappedErr, "Failed to push image from helper pod")
		return wrappedErr
	}

	if pushTarget != target {
		core.Success(fmt.Sprintf("Pushed %s via in-cluster helper (helper destination %s)", target, pushTarget))
	} else {
		core.Success(fmt.Sprintf("Pushed via in-cluster helper (helper destination %s)", pushTarget))
	}
	return nil
}

// registryPublisherSecret is the trusted publication credential installed by
// native registry authentication. It exists only in the registry namespace.
const registryPublisherSecret = "mcp-registry-publisher" // #nosec G101 -- Kubernetes Secret name, not credential material.
const registryPublisherAuthFile = "/registry-publisher/config.json"

// registryNativeAuthActive reports whether the bundled registry enforces
// token authentication. Read errors report false: an unauthenticated push to
// a protected registry is rejected by the registry itself.
var registryNativeAuthActive = func() bool {
	clients, err := newRegistryKubernetesClients()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), registryClientGoProbeTimeout)
	defer cancel()
	active, err := NativeAuthActive(ctx, clients.Clientset)
	return err == nil && active
}

func registryPushHelperOverrides(helperName string, publisherSecret string) (string, error) {
	container := map[string]any{
		"name":            helperName,
		"image":           core.GetSkopeoImage(),
		"command":         []string{"sh", "-c", "while true; do sleep 3600; done"},
		"securityContext": kubeworkload.RestrictedContainerSecurityContext(),
	}
	podSecurity := kubeworkload.RestrictedPodSecurityContext()
	spec := map[string]any{
		"automountServiceAccountToken": false,
		"securityContext":              podSecurity,
	}
	if publisherSecret != "" {
		group := int64(1000)
		podSecurity.FSGroup = &group
		mode := int32(0440)
		spec["volumes"] = []map[string]any{{
			"name": "publisher",
			"secret": map[string]any{
				"secretName":  publisherSecret,
				"defaultMode": mode,
				"items":       []map[string]any{{"key": ".dockerconfigjson", "path": "config.json"}},
			},
		}}
		container["volumeMounts"] = []map[string]any{{"name": "publisher", "mountPath": "/registry-publisher", "readOnly": true}}
	}
	spec["containers"] = []map[string]any{container}
	overrides := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"labels": map[string]string{registrypush.HelperLabelKey: registrypush.HelperLabelValue},
		},
		"spec": spec,
	}
	b, err := json.Marshal(overrides)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// rewriteTargetHostForInClusterPush replaces the host portion of an image reference
// with the in-cluster registry service DNS when the target points at the bundled
// internal registry (identified by the configured endpoint or ingress host). Image
// data in a Docker registry is keyed by repository path, so pushing via the service
// DNS stores the image at the same repo path, leaving the original hostname (e.g. the
// ingress host) usable for subsequent pulls. Targets outside the internal registry
// (e.g. a user-provided external registry) are returned unchanged.
func rewriteTargetHostForInClusterPush(target string, kubectl *core.KubectlClient) string {
	slash := strings.Index(target, "/")
	if slash <= 0 {
		return target
	}
	host := target[:slash]
	rest := target[slash:]

	lowerHost := strings.ToLower(host)
	if strings.Contains(lowerHost, ".svc.cluster.local") {
		return target
	}

	hostNoPort := lowerHost
	if idx := strings.LastIndex(hostNoPort, ":"); idx >= 0 {
		hostNoPort = hostNoPort[:idx]
	}

	internal := map[string]struct{}{}
	addInternalHost := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			return
		}
		if idx := strings.LastIndex(value, ":"); idx >= 0 {
			value = value[:idx]
		}
		if value != "" {
			internal[value] = struct{}{}
		}
	}
	addInternalHost(core.GetRegistryEndpoint())
	addInternalHost(core.GetRegistryIngressHost())
	if host, _, err := discoverRegistryHostAndPort(kubectl); err == nil {
		addInternalHost(host)
	}

	if _, ok := internal[hostNoPort]; !ok {
		return target
	}

	port := core.GetRegistryPort()
	if _, discoveredPort, err := discoverRegistryHostAndPort(kubectl); err == nil && discoveredPort != "" {
		port = parsePortOrDefault(discoveredPort, port)
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d%s", core.RegistryServiceName, core.NamespaceRegistry, port, rest)
}

func discoverRegistryHostAndPort(kubectl *core.KubectlClient) (string, string, error) {
	clients, err := registryKubernetesClients()
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), registryClientGoProbeTimeout)
		defer cancel()
		svc, svcErr := clients.Clientset.CoreV1().Services(core.NamespaceRegistry).Get(ctx, core.RegistryServiceName, metav1.GetOptions{})
		if svcErr == nil {
			port := ""
			if len(svc.Spec.Ports) > 0 {
				port = fmt.Sprintf("%d", svc.Spec.Ports[0].Port)
			}
			ingressHost := ""
			if ing, ingErr := clients.Clientset.NetworkingV1().Ingresses(core.NamespaceRegistry).Get(ctx, core.RegistryServiceName, metav1.GetOptions{}); ingErr == nil {
				if len(ing.Spec.Rules) > 0 {
					ingressHost = strings.TrimSpace(ing.Spec.Rules[0].Host)
				}
			}
			return ingressHost, port, nil
		}
	}
	if kubectl == nil {
		return "", "", fmt.Errorf("kubectl client unavailable")
	}
	var host, port string
	if ingressCmd, err := kubectl.CommandArgs([]string{"get", "ingress", core.RegistryServiceName, "-n", core.NamespaceRegistry, "-o", "jsonpath={.spec.rules[0].host}"}); err == nil {
		if out, err := ingressCmd.Output(); err == nil {
			host = strings.TrimSpace(string(out))
		}
	}
	if portCmd, err := kubectl.CommandArgs([]string{"get", "service", core.RegistryServiceName, "-n", core.NamespaceRegistry, "-o", "jsonpath={.spec.ports[0].port}"}); err == nil {
		if out, err := portCmd.Output(); err == nil {
			port = strings.TrimSpace(string(out))
		}
	}
	if host == "" && port == "" {
		return "", "", fmt.Errorf("registry host and port not discoverable")
	}
	return host, port, nil
}

func registryStatusWithClientGo(namespace string) (string, string, string, error) {
	clients, err := registryKubernetesClients()
	if err != nil {
		return "", "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), registryClientGoProbeTimeout)
	defer cancel()
	deploy, err := clients.Clientset.AppsV1().Deployments(namespace).Get(ctx, core.RegistryDeploymentName, metav1.GetOptions{})
	if err != nil {
		return "", "", "", err
	}
	replicas := int32(1)
	if deploy.Spec.Replicas != nil {
		replicas = *deploy.Spec.Replicas
	}
	service, err := clients.Clientset.CoreV1().Services(namespace).Get(ctx, core.RegistryServiceName, metav1.GetOptions{})
	if err != nil {
		return "", "", "", err
	}
	endpoint := strings.TrimSpace(service.Spec.ClusterIP)
	if endpoint != "" && len(service.Spec.Ports) > 0 {
		endpoint = fmt.Sprintf("%s:%d", endpoint, service.Spec.Ports[0].Port)
	}
	pods, err := clients.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: core.SelectorRegistry, Limit: 1})
	if err != nil {
		return "", "", "", err
	}
	podPhase := ""
	if len(pods.Items) > 0 {
		podPhase = string(pods.Items[0].Status.Phase)
	}
	return fmt.Sprintf("%d/%d", deploy.Status.ReadyReplicas, replicas), endpoint, podPhase, nil
}

func registryInfoWithClientGo(namespace string) (string, string, string, error) {
	clients, err := registryKubernetesClients()
	if err != nil {
		return "", "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), registryClientGoProbeTimeout)
	defer cancel()
	ingressHost := ""
	if ingress, err := clients.Clientset.NetworkingV1().Ingresses(namespace).Get(ctx, core.RegistryServiceName, metav1.GetOptions{}); err == nil {
		if len(ingress.Spec.Rules) > 0 {
			ingressHost = strings.TrimSpace(ingress.Spec.Rules[0].Host)
		}
	}
	service, err := clients.Clientset.CoreV1().Services(namespace).Get(ctx, core.RegistryServiceName, metav1.GetOptions{})
	if err != nil {
		return "", "", "", err
	}
	clusterIP := strings.TrimSpace(service.Spec.ClusterIP)
	port := ""
	if len(service.Spec.Ports) > 0 {
		port = fmt.Sprintf("%d", service.Spec.Ports[0].Port)
	}
	return ingressHost, clusterIP, port, nil
}

func parsePortOrDefault(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 65535 {
		return def
	}
	return n
}
