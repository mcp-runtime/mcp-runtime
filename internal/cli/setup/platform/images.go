package platform

import (
	"fmt"
	"go.uber.org/zap"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/registry/config"
	"mcp-runtime/internal/cli/registry/ref"
	"mcp-runtime/internal/cli/setup/platform/imagecache"
	"strings"
	"sync"
)

func prepareDeploymentImages(logger *zap.Logger, extRegistry *config.ExternalRegistryConfig, usingExternalRegistry, testMode, parallelBuilds bool, deps SetupDeps) (string, string, error) {
	core.Step("Step 5: Publish runtime images")

	if parallelBuilds {
		core.Info("Parallel image publishing enabled for setup runtime images")

		parallelDeps, err := prepareParallelImagePublishDeps(logger, usingExternalRegistry, deps, "setup")
		if err != nil {
			core.Error("Failed to prepare internal registry image publishing")
			core.LogStructuredError(logger, err, "Failed to prepare internal registry image publishing")
			return "", "", err
		}

		type imageResult struct {
			kind  string
			image string
			err   error
		}
		results := make(chan imageResult, 2)
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			image, err := prepareOperatorImage(logger, extRegistry, usingExternalRegistry, testMode, parallelDeps)
			results <- imageResult{kind: "operator", image: image, err: err}
		}()
		go func() {
			defer wg.Done()
			image, err := prepareGatewayProxyImage(logger, extRegistry, usingExternalRegistry, testMode, parallelDeps)
			results <- imageResult{kind: "gateway", image: image, err: err}
		}()

		wg.Wait()
		close(results)

		var operatorImage, gatewayProxyImage string
		for result := range results {
			if result.err != nil {
				return "", "", result.err
			}
			switch result.kind {
			case "operator":
				operatorImage = result.image
			case "gateway":
				gatewayProxyImage = result.image
			}
		}
		return operatorImage, gatewayProxyImage, nil
	}

	operatorImage, err := prepareOperatorImage(logger, extRegistry, usingExternalRegistry, testMode, deps)
	if err != nil {
		return "", "", err
	}
	gatewayProxyImage, err := prepareGatewayProxyImage(logger, extRegistry, usingExternalRegistry, testMode, deps)
	if err != nil {
		return "", "", err
	}
	return operatorImage, gatewayProxyImage, nil
}

func prepareOperatorImage(logger *zap.Logger, extRegistry *config.ExternalRegistryConfig, usingExternalRegistry, testMode bool, deps SetupDeps) (string, error) {
	operatorImage := deps.OperatorImageFor(extRegistry)
	core.Info(fmt.Sprintf("Operator image: %s", operatorImage))

	core.Info("Building operator image")
	if err := ensureImageViaCache("operator", operatorImage, func() error {
		return deps.BuildOperatorImage(operatorImage)
	}); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrOperatorImageBuildFailed,
			err,
			fmt.Sprintf("operator image build failed for image %q: %v", operatorImage, err),
			map[string]any{
				"image":     operatorImage,
				"component": "operator",
			},
		)
		core.Error("Operator image build failed")
		core.LogStructuredError(logger, wrappedErr, "Operator image build failed")
		return "", wrappedErr
	}

	if usingExternalRegistry {
		if testMode {
			core.Info("Test mode: pushing operator image to external registry")
		} else {
			core.Info("Pushing operator image to external registry")
		}
		if err := deps.PushOperatorImage(operatorImage); err != nil {
			core.Warn(fmt.Sprintf("Could not push image to external registry: %v", err))
		}
		return operatorImage, nil
	}

	core.Info("Pushing operator image to internal registry")
	internalRegistryURL := deps.ResolvePlatformRegistryURL(logger)
	_, operatorTag := ref.SplitImage(operatorImage)
	if operatorTag == "" {
		operatorTag = setupImageTag()
	}
	internalOperatorImage := fmt.Sprintf("%s/mcp-runtime-operator:%s", internalRegistryURL, operatorTag)

	if err := ensureRegistryNamespaceForImagePush(deps, "setup"); err != nil {
		core.Error("Failed to ensure registry namespace")
		core.LogStructuredError(logger, err, "Failed to ensure registry namespace")
		return "", err
	}

	if err := deps.PushOperatorImageToInternal(logger, operatorImage, internalOperatorImage, "registry"); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrPushOperatorImageInternalFailed,
			err,
			fmt.Sprintf("failed to push operator image %q to internal registry %q: %v", operatorImage, internalOperatorImage, err),
			map[string]any{
				"source_image": operatorImage,
				"target_image": internalOperatorImage,
				"namespace":    "registry",
				"component":    "operator",
			},
		)
		core.Error("Failed to push operator image to internal registry")
		core.LogStructuredError(logger, wrappedErr, "Failed to push operator image to internal registry")
		return "", wrappedErr
	}
	core.Info(fmt.Sprintf("Using internal registry image: %s", internalOperatorImage))
	return internalOperatorImage, nil
}

func prepareGatewayProxyImage(logger *zap.Logger, extRegistry *config.ExternalRegistryConfig, usingExternalRegistry, testMode bool, deps SetupDeps) (string, error) {
	gatewayProxyImage := deps.GatewayProxyImageFor(extRegistry)
	core.Info(fmt.Sprintf("Gateway proxy image: %s", gatewayProxyImage))

	core.Info("Building gateway proxy image")
	if err := ensureImageViaCache("gateway-proxy", gatewayProxyImage, func() error {
		return deps.BuildGatewayProxyImage(gatewayProxyImage)
	}); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrGatewayProxyImageBuildFailed,
			err,
			fmt.Sprintf("gateway proxy image build failed for image %q: %v", gatewayProxyImage, err),
			map[string]any{
				"image":     gatewayProxyImage,
				"component": "gateway-proxy",
			},
		)
		core.Error("Gateway proxy image build failed")
		core.LogStructuredError(logger, wrappedErr, "Gateway proxy image build failed")
		return "", wrappedErr
	}

	if usingExternalRegistry {
		if testMode {
			core.Info("Test mode: pushing gateway proxy image to external registry")
		} else {
			core.Info("Pushing gateway proxy image to external registry")
		}
		if err := deps.PushGatewayProxyImage(gatewayProxyImage); err != nil {
			core.Warn(fmt.Sprintf("Could not push gateway proxy image to external registry: %v", err))
		}
		return gatewayProxyImage, nil
	}

	core.Info("Pushing gateway proxy image to internal registry")
	internalRegistryURL := deps.ResolvePlatformRegistryURL(logger)
	_, gatewayTag := ref.SplitImage(gatewayProxyImage)
	if gatewayTag == "" {
		gatewayTag = setupImageTag()
	}
	internalGatewayProxyImage := fmt.Sprintf("%s/%s:%s", internalRegistryURL, defaultGatewayProxyRepository, gatewayTag)

	if err := ensureRegistryNamespaceForImagePush(deps, "setup"); err != nil {
		core.Error("Failed to ensure registry namespace")
		core.LogStructuredError(logger, err, "Failed to ensure registry namespace")
		return "", err
	}

	if err := deps.PushGatewayProxyImageToInternal(logger, gatewayProxyImage, internalGatewayProxyImage, "registry"); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrPushGatewayProxyImageInternalFailed,
			err,
			fmt.Sprintf("failed to push gateway proxy image %q to internal registry %q: %v", gatewayProxyImage, internalGatewayProxyImage, err),
			map[string]any{
				"source_image": gatewayProxyImage,
				"target_image": internalGatewayProxyImage,
				"namespace":    "registry",
				"component":    "gateway-proxy",
			},
		)
		core.Error("Failed to push gateway proxy image to internal registry")
		core.LogStructuredError(logger, wrappedErr, "Failed to push gateway proxy image to internal registry")
		return "", wrappedErr
	}

	core.Info(fmt.Sprintf("Using internal registry gateway proxy image: %s", internalGatewayProxyImage))
	return internalGatewayProxyImage, nil
}

func prepareAnalyticsImages(logger *zap.Logger, extRegistry *config.ExternalRegistryConfig, usingExternalRegistry, testMode, parallelBuilds bool, deps SetupDeps) (AnalyticsImageSet, error) {
	core.Step("Step 5a: Publish analytics images")
	components := analyticsComponentsForSetup(testMode)

	images := AnalyticsImageSet{
		Ingest:       analyticsImageFor(extRegistry, analyticsComponents[0].Repository),
		PlatformAPI:  analyticsImageFor(extRegistry, analyticsComponents[1].Repository),
		RuntimeAPI:   analyticsImageFor(extRegistry, analyticsComponents[2].Repository),
		AnalyticsAPI: analyticsImageFor(extRegistry, analyticsComponents[3].Repository),
		Processor:    analyticsImageFor(extRegistry, analyticsComponents[4].Repository),
		UI:           analyticsImageFor(extRegistry, analyticsComponents[5].Repository),
		DoctorSmoke:  analyticsImageFor(extRegistry, analyticsComponents[6].Repository),
	}
	if !testMode {
	}

	if parallelBuilds {
		core.Info("Parallel image publishing enabled for setup analytics images")
		return prepareAnalyticsImagesParallel(logger, extRegistry, usingExternalRegistry, testMode, deps, images)
	}

	for _, component := range components {
		image, err := buildAndPublishAnalyticsComponent(logger, extRegistry, usingExternalRegistry, testMode, deps, component)
		if err != nil {
			return AnalyticsImageSet{}, err
		}
		assignAnalyticsImage(&images, component.Repository, image)
	}

	return images, nil
}

func prepareAnalyticsImagesParallel(logger *zap.Logger, extRegistry *config.ExternalRegistryConfig, usingExternalRegistry, testMode bool, deps SetupDeps, images AnalyticsImageSet) (AnalyticsImageSet, error) {
	parallelDeps, err := prepareParallelImagePublishDeps(logger, usingExternalRegistry, deps, "analytics")
	if err != nil {
		return AnalyticsImageSet{}, err
	}

	type analyticsResult struct {
		repository string
		image      string
		err        error
	}

	components := analyticsComponentsForSetup(testMode)
	results := make(chan analyticsResult, len(components))
	var wg sync.WaitGroup
	wg.Add(len(components))

	for _, component := range components {
		component := component
		go func() {
			defer wg.Done()
			finalImage, err := buildAndPublishAnalyticsComponent(logger, extRegistry, usingExternalRegistry, testMode, parallelDeps, component)
			results <- analyticsResult{repository: component.Repository, image: finalImage, err: err}
		}()
	}

	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			return AnalyticsImageSet{}, result.err
		}
		assignAnalyticsImage(&images, result.repository, result.image)
	}

	return images, nil
}

func buildAndPublishAnalyticsComponent(logger *zap.Logger, extRegistry *config.ExternalRegistryConfig, usingExternalRegistry, testMode bool, deps SetupDeps, component analyticsComponent) (string, error) {
	image := analyticsImageFor(extRegistry, component.Repository)
	if testMode {
		core.Info(fmt.Sprintf("Test mode: building analytics %s image: %s", component.Name, image))
	} else {
		core.Info(fmt.Sprintf("Building analytics %s image: %s", component.Name, image))
	}
	cacheComponent := component.Name
	if mapped := imagecache.ComponentFromLocalImage(image); mapped != "" {
		cacheComponent = mapped
	}
	if err := ensureImageViaCache(cacheComponent, image, func() error {
		return deps.BuildAnalyticsImage(image, component.Dockerfile, component.BuildContext)
	}); err != nil {
		return "", core.WrapWithBaseAndContext(
			core.ErrBuildImageFailed,
			err,
			fmt.Sprintf("failed to build analytics %s image %q: %v", component.Name, image, err),
			map[string]any{"image": image, "component": component.Name},
		)
	}
	if usingExternalRegistry {
		if testMode {
			core.Info(fmt.Sprintf("Test mode: pushing analytics %s image to external registry", component.Name))
		} else {
			core.Info(fmt.Sprintf("Pushing analytics %s image to external registry", component.Name))
		}
		if err := deps.PushAnalyticsImage(image); err != nil {
			core.Warn(fmt.Sprintf("Could not push analytics %s image to external registry: %v", component.Name, err))
		}
		return image, nil
	}

	if testMode {
		core.Info(fmt.Sprintf("Test mode: pushing analytics %s image to internal registry", component.Name))
	} else {
		core.Info(fmt.Sprintf("Pushing analytics %s image to internal registry", component.Name))
	}
	if err := ensureRegistryNamespaceForImagePush(deps, component.Name); err != nil {
		return "", err
	}
	internalRegistryURL := deps.ResolvePlatformRegistryURL(logger)
	_, imageTag := ref.SplitImage(image)
	if imageTag == "" {
		imageTag = setupImageTag()
	}
	internalImage := fmt.Sprintf("%s/%s:%s", internalRegistryURL, component.Repository, imageTag)
	if err := deps.PushAnalyticsImageToInternal(logger, image, internalImage, "registry"); err != nil {
		return "", core.WrapWithBaseAndContext(
			core.ErrPushImageInClusterFailed,
			err,
			fmt.Sprintf("failed to push analytics %s image %q to internal registry %q: %v", component.Name, image, internalImage, err),
			map[string]any{"source_image": image, "target_image": internalImage, "component": component.Name},
		)
	}
	return internalImage, nil
}

func prepareParallelImagePublishDeps(logger *zap.Logger, usingExternalRegistry bool, deps SetupDeps, component string) (SetupDeps, error) {
	if usingExternalRegistry {
		return deps, nil
	}
	if err := ensureRegistryNamespaceForImagePush(deps, component); err != nil {
		return SetupDeps{}, err
	}
	internalRegistryURL := deps.ResolvePlatformRegistryURL(logger)
	parallelDeps := deps
	parallelDeps.EnsureNamespace = func(string) error { return nil }
	parallelDeps.ResolvePlatformRegistryURL = func(*zap.Logger) string { return internalRegistryURL }
	return parallelDeps, nil
}

func ensureRegistryNamespaceForImagePush(deps SetupDeps, component string) error {
	if err := deps.EnsureNamespace("registry"); err != nil {
		return core.WrapWithBaseAndContext(
			core.ErrEnsureRegistryNamespaceFailed,
			err,
			fmt.Sprintf("failed to ensure registry namespace: %v", err),
			map[string]any{"namespace": "registry", "component": component},
		)
	}
	return nil
}

func assignAnalyticsImage(images *AnalyticsImageSet, repository, image string) {
	switch repository {
	case "mcp-ingest":
		images.Ingest = image
	case "mcp-platform-api":
		images.PlatformAPI = image
	case "mcp-runtime-api":
		images.RuntimeAPI = image
	case "mcp-analytics-api":
		images.AnalyticsAPI = image
	case "mcp-processor":
		images.Processor = image
	case "mcp-ui":
		images.UI = image
	case "mcp-runtime-doctor-smoke":
		images.DoctorSmoke = image
	}
}

func getOperatorImage(ext *config.ExternalRegistryConfig) string {
	tag := setupImageTag()

	// Check for explicit override first
	if override := core.GetOperatorImageOverride(); override != "" {
		return override
	}

	if ext != nil && ext.URL != "" {
		return strings.TrimSuffix(ext.URL, "/") + "/mcp-runtime-operator:" + tag
	}
	return fmt.Sprintf("%s/mcp-runtime-operator:%s", resolveInternalPlatformRegistryURLClientGo(nil), tag)
}

func getGatewayProxyImage(ext *config.ExternalRegistryConfig) string {
	tag := setupImageTag()

	if override := core.GetGatewayProxyImageOverride(); override != "" {
		return override
	}

	if ext != nil && ext.URL != "" {
		return strings.TrimSuffix(ext.URL, "/") + "/" + defaultGatewayProxyRepository + ":" + tag
	}
	return fmt.Sprintf("%s/%s:%s", resolveInternalPlatformRegistryURLClientGo(nil), defaultGatewayProxyRepository, tag)
}

func analyticsImageFor(ext *config.ExternalRegistryConfig, repository string) string {
	tag := setupImageTag()

	if ext != nil && ext.URL != "" {
		return strings.TrimSuffix(ext.URL, "/") + "/" + repository + ":" + tag
	}
	return fmt.Sprintf("%s/%s:%s", resolveInternalPlatformRegistryURLClientGo(nil), repository, tag)
}
