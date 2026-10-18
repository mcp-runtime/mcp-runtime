package platform

import (
	"fmt"
	"go.uber.org/zap"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/registry"
	"mcp-runtime/internal/cli/setup/assetpath"
	"os"
	"strings"
)

func buildOperatorImage(image string) error {
	if err := validateDockerImageBuilder(); err != nil {
		return err
	}
	platform, err := resolveSetupImagePlatformClientGo()
	if err != nil {
		return err
	}
	target := "docker-build-operator-no-test"
	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	cmd, err := core.ExecCommandWithValidators("make", []string{"-f", "Makefile.operator", target, "IMG=" + image, "DOCKER_PLATFORM=" + platform})
	if err != nil {
		return err
	}
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func buildGatewayProxyImage(image string) error {
	if err := validateDockerImageBuilder(); err != nil {
		return err
	}
	platform, err := resolveSetupImagePlatformClientGo()
	if err != nil {
		return err
	}
	dockerfilePath, err := assetpath.ResolveRepoAssetPath(gatewayProxyDockerfilePath)
	if err != nil {
		return err
	}
	buildContext, err := assetpath.ResolveRepoAssetPath(gatewayProxyBuildContext)
	if err != nil {
		return err
	}

	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	cmd, err := core.ExecCommandWithValidators("docker", []string{
		"build",
		"--platform", platform,
		"-f", dockerfilePath,
		"-t", image,
		buildContext,
	})
	if err != nil {
		return err
	}
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	return cmd.Run()
}

func buildAnalyticsImage(image, dockerfilePath, buildContext string) error {
	if err := validateDockerImageBuilder(); err != nil {
		return err
	}
	platform, err := resolveSetupImagePlatformClientGo()
	if err != nil {
		return err
	}
	resolvedDockerfilePath, err := assetpath.ResolveRepoAssetPath(dockerfilePath)
	if err != nil {
		return err
	}
	resolvedBuildContext, err := assetpath.ResolveRepoAssetPath(buildContext)
	if err != nil {
		return err
	}

	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	cmd, err := core.ExecCommandWithValidators("docker", []string{
		"build",
		"--platform", platform,
		"-f", resolvedDockerfilePath,
		"-t", image,
		resolvedBuildContext,
	})
	if err != nil {
		return err
	}
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	return cmd.Run()
}

func pushOperatorImage(image string) error {
	// #nosec G204 -- image from internal build process or validated config.
	cmd, err := core.ExecCommandWithValidators("docker", []string{"push", image})
	if err != nil {
		return err
	}
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	return cmd.Run()
}

func pushGatewayProxyImage(image string) error {
	// #nosec G204 -- image from internal build process or validated config.
	cmd, err := core.ExecCommandWithValidators("docker", []string{"push", image})
	if err != nil {
		return err
	}
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	return cmd.Run()
}

func pushAnalyticsImage(image string) error {
	// #nosec G204 -- image from internal build process or validated config.
	cmd, err := core.ExecCommandWithValidators("docker", []string{"push", image})
	if err != nil {
		return err
	}
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	return cmd.Run()
}

func pushOperatorImageToInternalRegistry(logger *zap.Logger, sourceImage, targetImage, helperNamespace string) error {
	mgr := registry.DefaultRegistryManager(logger)
	if err := mgr.PushInCluster(sourceImage, targetImage, helperNamespace); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrPushImageInClusterFailed,
			err,
			fmt.Sprintf("failed to push image in-cluster: %v", err),
			map[string]any{"source_image": sourceImage, "target_image": targetImage, "namespace": helperNamespace, "component": "setup"},
		)
		core.Error("Failed to push image in-cluster")
		core.LogStructuredError(logger, wrappedErr, "Failed to push image in-cluster")
		return wrappedErr
	}
	return nil
}

func pushGatewayProxyImageToInternalRegistry(logger *zap.Logger, sourceImage, targetImage, helperNamespace string) error {
	mgr := registry.DefaultRegistryManager(logger)
	if err := mgr.PushInCluster(sourceImage, targetImage, helperNamespace); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrPushImageInClusterFailed,
			err,
			fmt.Sprintf("failed to push image in-cluster: %v", err),
			map[string]any{"source_image": sourceImage, "target_image": targetImage, "namespace": helperNamespace, "component": "gateway-proxy"},
		)
		core.Error("Failed to push image in-cluster")
		core.LogStructuredError(logger, wrappedErr, "Failed to push image in-cluster")
		return wrappedErr
	}
	return nil
}

func pushAnalyticsImageToInternalRegistry(logger *zap.Logger, sourceImage, targetImage, helperNamespace string) error {
	mgr := registry.DefaultRegistryManager(logger)
	if err := mgr.PushInCluster(sourceImage, targetImage, helperNamespace); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrPushImageInClusterFailed,
			err,
			fmt.Sprintf("failed to push image in-cluster: %v", err),
			map[string]any{"source_image": sourceImage, "target_image": targetImage, "namespace": helperNamespace, "component": "analytics"},
		)
		core.Error("Failed to push image in-cluster")
		core.LogStructuredError(logger, wrappedErr, "Failed to push image in-cluster")
		return wrappedErr
	}
	return nil
}

func validateDockerImageBuilder() error {
	if strings.TrimSpace(os.Getenv("DOCKER_BUILDKIT")) == "0" {
		return nil
	}
	cmd, err := core.ExecCommandWithValidators("docker", []string{"buildx", "version"})
	if err == nil {
		err = cmd.Run()
	}
	if err != nil {
		return fmt.Errorf("docker BuildKit requires a working buildx plugin: install docker-buildx, or explicitly use DOCKER_BUILDKIT=0 with a Docker version that supports the legacy builder: %w", err)
	}
	return nil
}
