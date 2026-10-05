package server

// This file implements the "server build" command for building Docker images.
// It handles Docker image building, metadata file updates, and registry integration.
//
// Example usage:
//   mcp-runtime server build image my-server --tag v1.0.0
//   mcp-runtime server build image my-server --dockerfile custom.Dockerfile --registry my-registry.com

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/internal/cli/registry"
	"mcp-runtime/internal/cli/registry/resolve"
	"mcp-runtime/pkg/metadata"
	"mcp-runtime/pkg/publishscope"
)

const buildTenantScopeTimeout = 30 * time.Second
const defaultDockerBuildPlatform = "linux/amd64"

// yamlMarshal is a test seam for yaml.Marshal.
var yamlMarshal = yaml.Marshal

func buildImage(ctx context.Context, logger *zap.Logger, serverName, dockerfile, metadataFile, metadataDir, registryURL, tag, platform, contextDir string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Get registry URL. Use the same resolution as `server push` (saved
	// platform login first, then env/cluster discovery) so the tag written
	// here is the ref the user pushes and deploys. Without it a laptop user
	// with no MCP_* env and no kubeconfig got the in-cluster registry DNS name.
	if registryURL == "" {
		registryURL = registry.ResolvePlatformRegistryURL(logger)
	}

	// Get tag
	if tag == "" {
		tag = resolve.GitTag(func(name string, args []string) (resolve.OutputCommand, error) {
			return core.ExecCommandWithValidators(name, args)
		})
	}

	logger.Info("Building image", zap.String("server", serverName))

	// Determine image name
	repository, err := scopedRepositoryNameForBuild(ctx, serverName, metadataFile, metadataDir)
	if err != nil {
		core.Error("Failed to resolve image repository")
		core.LogStructuredError(logger, err, "Failed to resolve image repository")
		return err
	}
	imageName := fmt.Sprintf("%s/%s", registryURL, repository)
	fullImage := fmt.Sprintf("%s:%s", imageName, tag)
	platform = normalizeDockerBuildPlatform(platform)

	// Build Docker image
	buildArgs := []string{"build"}
	if platform != "" {
		buildArgs = append(buildArgs, "--platform", platform)
	}
	buildArgs = append(buildArgs, "-f", dockerfile, "-t", fullImage, contextDir)
	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	buildCmd, err := core.ExecCommandWithValidators("docker", buildArgs)
	if err != nil {
		return err
	}
	buildCmd.SetStdout(os.Stdout)
	buildCmd.SetStderr(os.Stderr)

	if err := buildCmd.Run(); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrBuildImageFailed,
			err,
			fmt.Sprintf("failed to build image for %s: %v", serverName, err),
			map[string]any{"server": serverName, "image": fullImage, "dockerfile": dockerfile, "component": "build"},
		)
		core.Error("Failed to build image")
		core.LogStructuredError(logger, wrappedErr, "Failed to build image")
		return wrappedErr
	}

	logger.Info("Image built successfully", zap.String("image", fullImage))

	// Update metadata file (required for a successful build: CI and scripts rely on non-zero exit)
	if err := updateMetadataImage(serverName, imageName, tag, metadataFile, metadataDir); err != nil {
		core.LogStructuredError(logger, err, "Image built but metadata update failed")
		return err
	}

	core.Success(fmt.Sprintf("Built image %s", fullImage))
	pushHint := fmt.Sprintf("mcp-runtime server push --image %s", fullImage)
	if scope, ok := metadataScopeForBuild(serverName, metadataFile, metadataDir); ok {
		pushHint += " --scope " + scope
	}
	core.Info("Push it with: " + pushHint)

	return nil
}

func metadataScopeForBuild(serverName, metadataFile, metadataDir string) (string, bool) {
	server, ok, err := findMetadataServer(serverName, metadataFile, metadataDir)
	if err != nil || !ok {
		return "", false
	}
	scope, err := publishscope.Normalize(string(server.Scope))
	if err != nil || scope == "" {
		return "", false
	}
	return string(scope), true
}

func scopedRepositoryNameForBuild(ctx context.Context, serverName, metadataFile, metadataDir string) (string, error) {
	server, ok, err := findMetadataServer(serverName, metadataFile, metadataDir)
	if err != nil {
		return "", core.WrapWithBase(core.ErrLoadMetadataFailed, err, fmt.Sprintf("failed to load metadata: %v", err))
	}
	if !ok {
		return serverName, nil
	}
	scope, err := publishscope.Normalize(string(server.Scope))
	if err != nil {
		return serverName, nil
	}
	if scope == publishscope.Tenant {
		client, err := platformapi.NewPlatformClient()
		if err != nil {
			return "", fmt.Errorf("build tenant-scoped image requires platform credentials; run mcp-runtime auth login or set MCP_PLATFORM_API_TOKEN with a saved or explicit MCP_PLATFORM_API_URL: %w", err)
		}
		scopedCtx, cancel := context.WithTimeout(ctx, buildTenantScopeTimeout)
		defer cancel()
		return registry.ScopedRegistryRepository(scopedCtx, client, serverName, scope)
	}
	return registry.ScopedRegistryRepository(ctx, nil, serverName, scope)
}

func findMetadataServer(serverName, metadataFile, metadataDir string) (metadata.ServerMetadata, bool, error) {
	files := []string{}
	if metadataFile != "" {
		files = append(files, metadataFile)
	} else {
		yamlFiles, _ := filepath.Glob(filepath.Join(metadataDir, "*.yaml"))
		ymlFiles, _ := filepath.Glob(filepath.Join(metadataDir, "*.yml"))
		files = append(files, yamlFiles...)
		files = append(files, ymlFiles...)
	}

	for _, file := range files {
		registry, err := metadata.LoadFromFile(file)
		if err != nil {
			if metadataFile != "" {
				return metadata.ServerMetadata{}, false, err
			}
			continue
		}
		for _, server := range registry.Servers {
			if server.Name == serverName {
				return server, true, nil
			}
		}
	}
	return metadata.ServerMetadata{}, false, nil
}

// BuildImage builds a Docker image and updates MCP metadata for the server.
func BuildImage(ctx context.Context, logger *zap.Logger, serverName, dockerfile, metadataFile, metadataDir, registryURL, tag, platform, contextDir string) error {
	return buildImage(ctx, logger, serverName, dockerfile, metadataFile, metadataDir, registryURL, tag, platform, contextDir)
}

func normalizeDockerBuildPlatform(platform string) string {
	if platform = strings.TrimSpace(platform); platform != "" {
		return platform
	}
	if platform = strings.TrimSpace(os.Getenv("MCP_DOCKER_PLATFORM")); platform != "" {
		return platform
	}
	return defaultDockerBuildPlatform
}

func updateMetadataImage(serverName, imageName, tag, metadataFile, metadataDir string) error {
	// Find the metadata file containing this server
	var targetFile string

	if metadataFile != "" {
		targetFile = metadataFile
	} else {
		// Search in metadata directory
		files, _ := filepath.Glob(filepath.Join(metadataDir, "*.yaml"))
		ymlFiles, _ := filepath.Glob(filepath.Join(metadataDir, "*.yml"))
		files = append(files, ymlFiles...)

		for _, file := range files {
			registry, err := metadata.LoadFromFile(file)
			if err != nil {
				continue
			}
			for _, s := range registry.Servers {
				if s.Name == serverName {
					targetFile = file
					break
				}
			}
			if targetFile != "" {
				break
			}
		}
	}

	if targetFile == "" {
		err := core.NewWithBase(core.ErrMetadataFileNotFound, fmt.Sprintf("metadata file not found for server %s", serverName))
		core.Error("Metadata file not found")
		// Note: No logger available in this helper function
		return err
	}

	// Load and update the entries as written, without persisting loader defaults.
	registry, err := loadMetadataForRewrite(targetFile)
	if err != nil {
		wrappedErr := core.WrapWithBase(core.ErrLoadMetadataFailed, err, fmt.Sprintf("failed to load metadata: %v", err))
		core.Error("Failed to load metadata")
		// Note: No logger available in this helper function
		return wrappedErr
	}

	// Update server image
	updated := false
	for i := range registry.Servers {
		if registry.Servers[i].Name == serverName {
			registry.Servers[i].Image = imageName
			registry.Servers[i].ImageTag = tag
			updated = true
			break
		}
	}

	if !updated {
		err := core.NewWithBase(core.ErrServerNotFoundInMetadata, fmt.Sprintf("server %s not found in metadata", serverName))
		core.Error("Server not found in metadata")
		// Note: No logger available in this helper function
		return err
	}

	// Write back
	data, err := yamlMarshal(registry)
	if err != nil {
		wrappedErr := core.WrapWithBase(core.ErrMarshalMetadataFailed, err, fmt.Sprintf("failed to marshal metadata: %v", err))
		core.Error("Failed to marshal metadata")
		// Note: No logger available in this helper function
		return wrappedErr
	}

	fileMode := os.FileMode(0o600)
	if info, statErr := os.Stat(targetFile); statErr == nil {
		fileMode = info.Mode().Perm()
		if fileMode&0o200 == 0 {
			writeErr := fmt.Errorf("file is not writable: %s", targetFile)
			wrappedErr := core.WrapWithBase(core.ErrWriteMetadataFailed, writeErr, fmt.Sprintf("failed to write metadata: %v", writeErr))
			core.Error("Failed to write metadata")
			// Note: No logger available in this helper function
			return wrappedErr
		}
	}

	if err := os.WriteFile(targetFile, data, fileMode); err != nil {
		wrappedErr := core.WrapWithBase(core.ErrWriteMetadataFailed, err, fmt.Sprintf("failed to write metadata: %v", err))
		core.Error("Failed to write metadata")
		// Note: No logger available in this helper function
		return wrappedErr
	}

	return nil
}
