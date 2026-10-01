package core

// This file defines error handling utilities for the CLI, including:
//   - Sentinel errors for different error categories (CLI, Cluster, Registry, etc.)
//   - Error wrapping functions that integrate with the errx error system
//   - Structured error logging with context
//   - Debug mode management for error output

import (
	"errors"
	"sync"

	"go.uber.org/zap"

	"mcp-runtime/pkg/errx"
)

var (
	debugMode   bool
	debugModeMu sync.RWMutex
)

// SetDebugMode sets the global debug mode flag.
// When enabled, logStructuredError will output structured error logs to terminal.
func SetDebugMode(enabled bool) {
	debugModeMu.Lock()
	defer debugModeMu.Unlock()
	debugMode = enabled
}

// IsDebugMode returns whether debug mode is enabled.
func IsDebugMode() bool {
	debugModeMu.RLock()
	defer debugModeMu.RUnlock()
	return debugMode
}

type errorSpec struct {
	code        string
	description string
}

// newSentinelError creates a sentinel error and registers it in errorSpecs in one step.
// This eliminates redundancy between error definitions and errorSpecs mapping.
func newSentinelError(msg string, code, description string) error {
	err := errors.New(msg)
	errorSpecs[err] = errorSpec{code: code, description: description}
	return err
}

// errorSpecs maps sentinel errors to their error codes and descriptions.
// Populated automatically by newSentinelError() during variable initialization.
// Must be declared before sentinel errors to ensure proper initialization order.
var errorSpecs = make(map[error]errorSpec)

// lookupSpec provides a lookup function for errx.FromSentinel.
func lookupSpec(sentinel error) (code, description string) {
	spec := specFor(sentinel)
	return spec.code, spec.description
}

// newWithSentinel creates a new error using the appropriate errx category helper.
// The base error (sentinel) is used to determine the category, and the message provides context.
func newWithSentinel(base error, msg string) error {
	if base == nil {
		return errx.CreateByCode(errx.CodeCLI, errx.DescCLI, msg, nil)
	}
	return errx.FromSentinel(base, lookupSpec, msg, nil)
}

func NewWithSentinel(base error, msg string) error {
	return newWithSentinel(base, msg)
}

// wrapWithSentinel wraps a cause error using the appropriate errx category helper.
// The base error (sentinel) is used to determine the category, and the message provides context.
func wrapWithSentinel(base, cause error, msg string) error {
	if base == nil {
		return errx.CreateByCode(errx.CodeCLI, errx.DescCLI, msg, cause)
	}
	return errx.FromSentinel(base, lookupSpec, msg, cause)
}

func WrapWithSentinel(base, cause error, msg string) error {
	return wrapWithSentinel(base, cause, msg)
}

// wrapWithSentinelAndContext wraps an error with additional structured context.
// This is useful for adding debugging information like namespace, resource names, etc.
func wrapWithSentinelAndContext(base, cause error, msg string, context map[string]any) error {
	err := wrapWithSentinel(base, cause, msg)
	if errxErr, ok := err.(*errx.Error); ok && len(context) > 0 {
		return errxErr.WithContextMap(context)
	}
	return err
}

func WrapWithSentinelAndContext(base, cause error, msg string, context map[string]any) error {
	return wrapWithSentinelAndContext(base, cause, msg, context)
}

func NewSetupStepFailedError() error {
	return newWithSentinel(ErrSetupStepFailed, "cluster readiness or diagnostics found unmet requirements; see docs/cluster-readiness.md")
}

// Sentinel errors for CLI operations.
// Errors are defined and registered in one step using newSentinelError to eliminate redundancy.
var (
	// CLI errors.
	ErrImageRequired             = newSentinelError("image is required", errx.CodeCLI, errx.DescCLI)
	ErrInvalidServerName         = newSentinelError("invalid server name", errx.CodeCLI, errx.DescCLI)
	ErrGetWorkingDirectoryFailed = newSentinelError("get working directory", errx.CodeCLI, errx.DescCLI)
	ErrControlCharsNotAllowed    = newSentinelError("value must not contain control characters", errx.CodeCLI, errx.DescCLI)
	ErrFieldRequired             = newSentinelError("field is required", errx.CodeCLI, errx.DescCLI)
	ErrGetHomeDirectoryFailed    = newSentinelError("failed to get home directory", errx.CodeCLI, errx.DescCLI)
	ErrUnknownRegistryMode       = newSentinelError("unknown registry mode", errx.CodeCLI, errx.DescCLI)

	// Auth package errors.
	ErrAuthAPIURLRequired                  = newSentinelError("api URL is required", errx.CodeAuth, errx.DescAuth)
	ErrAuthAPIURLInvalid                   = newSentinelError("api URL must include scheme and host", errx.CodeAuth, errx.DescAuth)
	ErrAuthEmailPasswordRequired           = newSentinelError("email and password are both required for password login", errx.CodeAuth, errx.DescAuth)
	ErrAuthPlatformLoginFailed             = newSentinelError("platform login failed", errx.CodeAuth, errx.DescAuth)
	ErrAuthReadStdinFailed                 = newSentinelError("read stdin", errx.CodeAuth, errx.DescAuth)
	ErrAuthTTYRequired                     = newSentinelError("not a TTY: pass --token, --token-stdin, or run in an interactive terminal", errx.CodeAuth, errx.DescAuth)
	ErrAuthReadTokenFailed                 = newSentinelError("read token", errx.CodeAuth, errx.DescAuth)
	ErrAuthTokenRequired                   = newSentinelError("token is required", errx.CodeAuth, errx.DescAuth)
	ErrAuthTokenVerificationFailed         = newSentinelError("API token could not be verified", errx.CodeAuth, errx.DescAuth)
	ErrAuthLoginHTTPStatus                 = newSentinelError("login HTTP status failed", errx.CodeAuth, errx.DescAuth)
	ErrAuthLoginResponseMissingAccessToken = newSentinelError("login response did not include access_token", errx.CodeAuth, errx.DescAuth)
	ErrAuthServerRejectedToken             = newSentinelError("server rejected the token", errx.CodeAuth, errx.DescAuth)
	ErrAuthAPIURLMayBeWrong                = newSentinelError("API URL may be wrong", errx.CodeAuth, errx.DescAuth)
	ErrAuthVerifyRequestFailed             = newSentinelError("verify request failed", errx.CodeAuth, errx.DescAuth)
	ErrAuthFileDescriptorOutOfRange        = newSentinelError("file descriptor out of range", errx.CodeAuth, errx.DescAuth)

	// Pipeline errors.
	ErrLoadMetadataFailed      = newSentinelError("failed to load metadata", errx.CodePipeline, errx.DescPipeline)
	ErrNoServersInMetadata     = newSentinelError("no servers found in metadata", errx.CodePipeline, errx.DescPipeline)
	ErrGenerateCRDsFailed      = newSentinelError("failed to generate CRDs", errx.CodePipeline, errx.DescPipeline)
	ErrListManifestFilesFailed = newSentinelError("failed to list manifest files", errx.CodePipeline, errx.DescPipeline)
	ErrNoManifestFilesFound    = newSentinelError("no manifest files found", errx.CodePipeline, errx.DescPipeline)
	ErrApplyManifestFailed     = newSentinelError("failed to apply manifest", errx.CodePipeline, errx.DescPipeline)

	// Operator errors.
	ErrOperatorNotFound = newSentinelError("operator not found", errx.CodeOperator, errx.DescOperator)
	ErrOperatorNotReady = newSentinelError("operator not ready", errx.CodeOperator, errx.DescOperator)

	// Setup errors.
	ErrClusterInitFailed                   = newSentinelError("failed to initialize cluster", errx.CodeSetup, errx.DescSetup)
	ErrClusterConfigFailed                 = newSentinelError("cluster configuration failed", errx.CodeSetup, errx.DescSetup)
	ErrTLSSetupFailed                      = newSentinelError("TLS setup failed", errx.CodeSetup, errx.DescSetup)
	ErrDeployRegistryFailed                = newSentinelError("failed to deploy registry", errx.CodeSetup, errx.DescSetup)
	ErrOperatorImageBuildFailed            = newSentinelError("operator image build failed", errx.CodeSetup, errx.DescSetup)
	ErrGatewayProxyImageBuildFailed        = newSentinelError("gateway proxy image build failed", errx.CodeSetup, errx.DescSetup)
	ErrEnsureRegistryNamespaceFailed       = newSentinelError("failed to ensure registry namespace", errx.CodeSetup, errx.DescSetup)
	ErrPushOperatorImageInternalFailed     = newSentinelError("failed to push operator image to internal registry", errx.CodeSetup, errx.DescSetup)
	ErrPushGatewayProxyImageInternalFailed = newSentinelError("failed to push gateway proxy image to internal registry", errx.CodeSetup, errx.DescSetup)
	ErrOperatorDeploymentFailed            = newSentinelError("operator deployment failed", errx.CodeSetup, errx.DescSetup)
	ErrConfigureExternalRegistryEnvFailed  = newSentinelError("failed to configure external registry env on operator", errx.CodeSetup, errx.DescSetup)
	ErrRestartOperatorDeploymentFailed     = newSentinelError("failed to restart operator deployment after registry env update", errx.CodeSetup, errx.DescSetup)
	ErrCRDCheckFailed                      = newSentinelError("CRD check failed", errx.CodeSetup, errx.DescSetup)
	ErrRenderSecretManifestFailed          = newSentinelError("render secret manifest", errx.CodeSetup, errx.DescSetup)
	ErrApplySecretManifestFailed           = newSentinelError("apply secret manifest", errx.CodeSetup, errx.DescSetup)
	ErrMarshalDockerConfigFailed           = newSentinelError("marshal docker config", errx.CodeSetup, errx.DescSetup)
	ErrApplyImagePullSecretFailed          = newSentinelError("apply imagePullSecret", errx.CodeSetup, errx.DescSetup)
	ErrPushImageInClusterFailed            = newSentinelError("failed to push image in-cluster", errx.CodeSetup, errx.DescSetup)
	ErrSetupStepFailed                     = newSentinelError("setup step failed", errx.CodeSetup, errx.DescSetup)
	ErrApplyCRDFailed                      = newSentinelError("failed to apply CRD", errx.CodeSetup, errx.DescSetup)
	ErrEnsureOperatorNamespaceFailed       = newSentinelError("failed to ensure operator namespace", errx.CodeSetup, errx.DescSetup)
	ErrApplyRBACFailed                     = newSentinelError("failed to apply RBAC", errx.CodeSetup, errx.DescSetup)
	ErrReadManagerYAMLFailed               = newSentinelError("failed to read manager.yaml", errx.CodeSetup, errx.DescSetup)
	ErrReadIngressManifestFailed           = newSentinelError("failed to read ingress manifest", errx.CodeSetup, errx.DescSetup)
	ErrParseManagerYAMLFailed              = newSentinelError("failed to parse manager.yaml", errx.CodeSetup, errx.DescSetup)
	ErrSetOperatorImageFailed              = newSentinelError("failed to set operator image", errx.CodeSetup, errx.DescSetup)
	ErrMutateManagerYAMLFailed             = newSentinelError("failed to mutate manager.yaml", errx.CodeSetup, errx.DescSetup)
	ErrRenderManagerYAMLFailed             = newSentinelError("failed to render mutated manager.yaml", errx.CodeSetup, errx.DescSetup)
	ErrCreateTempFileFailed                = newSentinelError("failed to create temp file", errx.CodeSetup, errx.DescSetup)
	ErrCloseTempFileFailed                 = newSentinelError("failed to close temp file", errx.CodeSetup, errx.DescSetup)
	ErrWriteTempFileFailed                 = newSentinelError("failed to write temp file", errx.CodeSetup, errx.DescSetup)
	ErrApplyManagerDeploymentFailed        = newSentinelError("failed to apply manager deployment", errx.CodeSetup, errx.DescSetup)
	ErrClusterIssuerApplyFailed            = newSentinelError("failed to apply ClusterIssuer", errx.CodeSetup, errx.DescSetup)
	ErrCreateRegistryNamespaceFailed       = newSentinelError("failed to create registry namespace", errx.CodeSetup, errx.DescSetup)
	ErrApplyCertificateFailed              = newSentinelError("failed to apply Certificate", errx.CodeSetup, errx.DescSetup)

	// Setup platform package errors.
	ErrSetupImagePlatformNoNodeArchitectures       = newSentinelError("could not resolve setup image platform: no Kubernetes node architectures were reported", errx.CodeSetup, errx.DescSetup)
	ErrSetupImagePlatformMixedNodeArchitectures    = newSentinelError("mixed Kubernetes node architectures detected", errx.CodeSetup, errx.DescSetup)
	ErrSetupImagePlatformMismatch                  = newSentinelError("MCP_IMAGE_PLATFORM does not match Kubernetes node architecture", errx.CodeSetup, errx.DescSetup)
	ErrSetupImagePlatformInvalid                   = newSentinelError("invalid MCP_IMAGE_PLATFORM", errx.CodeSetup, errx.DescSetup)
	ErrSetupImagePlatformUnsupported               = newSentinelError("unsupported MCP_IMAGE_PLATFORM", errx.CodeSetup, errx.DescSetup)
	ErrSetupImagePlatformKubectlNil                = newSentinelError("could not resolve setup image platform: kubectl runner is nil", errx.CodeSetup, errx.DescSetup)
	ErrSetupInspectNodeArchitecturesFailed         = newSentinelError("could not inspect Kubernetes node architectures", errx.CodeSetup, errx.DescSetup)
	ErrSetupInvalidStorageMode                     = newSentinelError("invalid storage mode", errx.CodeSetup, errx.DescSetup)
	ErrSetupInvalidPlatformMode                    = newSentinelError("invalid platform mode", errx.CodeSetup, errx.DescSetup)
	ErrSetupInvalidRegistryMode                    = newSentinelError("invalid registry mode", errx.CodeSetup, errx.DescSetup)
	ErrSetupSetRuntimeTestModeFailed               = newSentinelError("set MCP_RUNTIME_TEST_MODE", errx.CodeSetup, errx.DescSetup)
	ErrSetupUnsetRuntimeTestModeFailed             = newSentinelError("unset MCP_RUNTIME_TEST_MODE", errx.CodeSetup, errx.DescSetup)
	ErrSetupSetPlatformModeFailed                  = newSentinelError("set MCP_PLATFORM_MODE", errx.CodeSetup, errx.DescSetup)
	ErrSetupListTraefikDeploymentsFailed           = newSentinelError("list traefik deployments", errx.CodeSetup, errx.DescSetup)
	ErrSetupMarshalTraefikDeploymentPatchFailed    = newSentinelError("marshal traefik deployment patch", errx.CodeSetup, errx.DescSetup)
	ErrSetupReadTraefikDeploymentFailed            = newSentinelError("read traefik deployment", errx.CodeSetup, errx.DescSetup)
	ErrSetupDecodeTraefikDeploymentFailed          = newSentinelError("decode traefik deployment", errx.CodeSetup, errx.DescSetup)
	ErrSetupDeploymentReadinessDeadlineExceeded    = newSentinelError("deployment readiness deadline exceeded", errx.CodeSetup, errx.DescSetup)
	ErrSetupTLSKubectlRunnerNil                    = newSentinelError("kubectl runner is nil", errx.CodeSetup, errx.DescSetup)
	ErrSetupInspectClusterIssuerFailed             = newSentinelError("inspect ClusterIssuer", errx.CodeSetup, errx.DescSetup)
	ErrSetupTLSCertificateSANsEmpty                = newSentinelError("no DNS names or IP addresses resolved for the Certificate", errx.CodeSetup, errx.DescSetup)
	ErrSetupDeleteClickHouseInitJobFailed          = newSentinelError("delete existing clickhouse init job", errx.CodeSetup, errx.DescSetup)
	ErrSetupAnalyticsRolloutFailed                 = newSentinelError("analytics components failed to roll out", errx.CodeSetup, errx.DescSetup)
	ErrSetupRenderManifestFailed                   = newSentinelError("render manifest", errx.CodeSetup, errx.DescSetup)
	ErrSetupApplyPlatformUIIngressFailed           = newSentinelError("apply platform UI ingress", errx.CodeSetup, errx.DescSetup)
	ErrSetupRemovePathBasedSentinelIngressesFailed = newSentinelError("remove path-based sentinel ingresses for public platform host", errx.CodeSetup, errx.DescSetup)
	ErrSetupDecodeAnalyticsConfigManifestFailed    = newSentinelError("decode analytics config manifest", errx.CodeSetup, errx.DescSetup)
	ErrSetupEncodeAnalyticsConfigManifestFailed    = newSentinelError("encode analytics config manifest", errx.CodeSetup, errx.DescSetup)
	ErrSetupReadConfigMapFailed                    = newSentinelError("read configmap", errx.CodeSetup, errx.DescSetup)
	ErrSetupDecodeConfigMapFailed                  = newSentinelError("decode configmap", errx.CodeSetup, errx.DescSetup)
	ErrSetupReadSecretKeyFailed                    = newSentinelError("read secret key", errx.CodeSetup, errx.DescSetup)
	ErrSetupDecodeSecretKeyFailed                  = newSentinelError("decode secret key", errx.CodeSetup, errx.DescSetup)

	// Platform update errors.
	ErrUpdateTargetRequired      = newSentinelError("update target required", errx.CodeSetup, errx.DescSetup)
	ErrUpdateManifestInvalid     = newSentinelError("release manifest invalid", errx.CodeSetup, errx.DescSetup)
	ErrUpdateTargetMismatch      = newSentinelError("release manifest version does not match --to", errx.CodeSetup, errx.DescSetup)
	ErrUpdateInvalidFlag         = newSentinelError("invalid update flag", errx.CodeSetup, errx.DescSetup)
	ErrUpdateKubeClientFailed    = newSentinelError("create Kubernetes client for update", errx.CodeSetup, errx.DescSetup)
	ErrUpdateNotInstalled        = newSentinelError("cluster is not an MCP Runtime install", errx.CodeSetup, errx.DescSetup)
	ErrUpdateInventoryFailed     = newSentinelError("read installed platform inventory", errx.CodeSetup, errx.DescSetup)
	ErrUpdateCRDChange           = newSentinelError("release changes CRDs", errx.CodeSetup, errx.DescSetup)
	ErrUpdateBlocked             = newSentinelError("update plan has blocked components", errx.CodeSetup, errx.DescSetup)
	ErrUpdateConfirmationMissing = newSentinelError("update confirmation required", errx.CodeSetup, errx.DescSetup)
	ErrUpdateAborted             = newSentinelError("update aborted", errx.CodeSetup, errx.DescSetup)
	ErrUpdateRolloutFailed       = newSentinelError("platform update rollout failed", errx.CodeSetup, errx.DescSetup)
	ErrUpdateBuildFailed         = newSentinelError("platform update image build failed", errx.CodeSetup, errx.DescSetup)

	// Cert errors.
	ErrCertManagerNotInstalled     = newSentinelError("cert-manager not installed", errx.CodeCert, errx.DescCert)
	ErrCertManagerInstallFailed    = newSentinelError("cert-manager install failed", errx.CodeCert, errx.DescCert)
	ErrCASecretNotFound            = newSentinelError("CA secret not found", errx.CodeCert, errx.DescCert)
	ErrCASecretInvalid             = newSentinelError("CA secret invalid", errx.CodeCert, errx.DescCert)
	ErrCAExpired                   = newSentinelError("CA certificate expired", errx.CodeCert, errx.DescCert)
	ErrCANearExpiry                = newSentinelError("CA certificate near expiry", errx.CodeCert, errx.DescCert)
	ErrCertificateNotReady         = newSentinelError("certificate not ready", errx.CodeCert, errx.DescCert)
	ErrClusterIssuerNotFound       = newSentinelError("ClusterIssuer not found", errx.CodeCert, errx.DescCert)
	ErrRegistryCertificateNotFound = newSentinelError("registry Certificate not found", errx.CodeCert, errx.DescCert)

	// Certmanager package errors.
	ErrCertEncodeGeneratedCAFailed         = newSentinelError("failed to encode generated internal CA", errx.CodeCert, errx.DescCert)
	ErrCertLookupRegistryIngressFailed     = newSentinelError("failed to look up registry ingress", errx.CodeCert, errx.DescCert)
	ErrCertRemoveRegistryIngressAnnotation = newSentinelError("failed to remove cert-manager.io/cluster-issuer from registry ingress", errx.CodeCert, errx.DescCert)
	ErrCertRegistryTLSSecretConflict       = newSentinelError("registry TLS secret is already referenced by Certificate(s)", errx.CodeCert, errx.DescCert)
	ErrCertListCertificatesFailed          = newSentinelError("failed to list cert-manager Certificates", errx.CodeCert, errx.DescCert)
	ErrCertParseCertificatesFailed         = newSentinelError("failed to parse cert-manager Certificates", errx.CodeCert, errx.DescCert)
	ErrCertACMEPublicDNSNameRequired       = newSentinelError("ACME public CA requires a public DNS name", errx.CodeCert, errx.DescCert)
	ErrCertACMEPublicDNSNameInvalid        = newSentinelError("ACME public CA requires a public DNS name; invalid host", errx.CodeCert, errx.DescCert)
	ErrCertACMEIngressManifestInvalid      = newSentinelError("http-01 ingress manifest is not valid for Let's Encrypt", errx.CodeCert, errx.DescCert)
	ErrCertTraefikNotReady                 = newSentinelError("traefik not ready", errx.CodeCert, errx.DescCert)
	ErrCertACMEEmailRequired               = newSentinelError("ACME email is required", errx.CodeCert, errx.DescCert)
	ErrCertCertificateSANsEmpty            = newSentinelError("TLS has no DNS names or IP addresses to request", errx.CodeCert, errx.DescCert)

	// Cluster errors.
	ErrCRDNotInstalled                = newSentinelError("MCPServer CRD not installed", errx.CodeCluster, errx.DescCluster)
	ErrClusterNotAccessible           = newSentinelError("cluster not accessible", errx.CodeCluster, errx.DescCluster)
	ErrNamespaceNotFound              = newSentinelError("namespace not found", errx.CodeCluster, errx.DescCluster)
	ErrDeploymentTimeout              = newSentinelError("deployment timed out waiting for readiness", errx.CodeCluster, errx.DescCluster)
	ErrInstallCRDFailed               = newSentinelError("failed to install CRD", errx.CodeCluster, errx.DescCluster)
	ErrEnsureRuntimeNamespaceFailed   = newSentinelError("failed to ensure mcp-runtime namespace", errx.CodeCluster, errx.DescCluster)
	ErrEnsureServersNamespaceFailed   = newSentinelError("failed to ensure mcp-servers namespace", errx.CodeCluster, errx.DescCluster)
	ErrKubeconfigNotReadable          = newSentinelError("kubeconfig not found or not readable", errx.CodeCluster, errx.DescCluster)
	ErrSetKubeconfigFailed            = newSentinelError("failed to set KUBECONFIG", errx.CodeCluster, errx.DescCluster)
	ErrSetContextFailed               = newSentinelError("failed to set context", errx.CodeCluster, errx.DescCluster)
	ErrAKSKubeconfigNotImplemented    = newSentinelError("AKS kubeconfig not yet implemented", errx.CodeCluster, errx.DescCluster)
	ErrGKEKubeconfigNotImplemented    = newSentinelError("GKE kubeconfig not yet implemented", errx.CodeCluster, errx.DescCluster)
	ErrUnsupportedProvider            = newSentinelError("unsupported provider", errx.CodeCluster, errx.DescCluster)
	ErrInvalidClusterName             = newSentinelError("invalid cluster name", errx.CodeCluster, errx.DescCluster)
	ErrInvalidNodeCount               = newSentinelError("invalid node count", errx.CodeCluster, errx.DescCluster)
	ErrUnsupportedIngressController   = newSentinelError("unsupported ingress controller", errx.CodeCluster, errx.DescCluster)
	ErrInstallIngressControllerFailed = newSentinelError("failed to install ingress controller", errx.CodeCluster, errx.DescCluster)
	ErrCreateKindConfigFailed         = newSentinelError("failed to create temp kind config", errx.CodeCluster, errx.DescCluster)
	ErrCloseKindConfigFailed          = newSentinelError("failed to close kind config", errx.CodeCluster, errx.DescCluster)
	ErrWriteKindConfigFailed          = newSentinelError("failed to write kind config", errx.CodeCluster, errx.DescCluster)
	ErrCreateKindClusterFailed        = newSentinelError("failed to create kind cluster", errx.CodeCluster, errx.DescCluster)
	ErrDockerDaemonNotReachable       = newSentinelError("docker daemon not reachable", errx.CodeCluster, errx.DescCluster)
	ErrKindClusterAlreadyExists       = newSentinelError("kind cluster already exists", errx.CodeCluster, errx.DescCluster)
	ErrGKEProvisioningNotImplemented  = newSentinelError("GKE provisioning not yet implemented", errx.CodeCluster, errx.DescCluster)
	ErrProvisionEKSFailed             = newSentinelError("failed to provision EKS cluster", errx.CodeCluster, errx.DescCluster)
	ErrAKSProvisioningNotImplemented  = newSentinelError("AKS provisioning not yet implemented", errx.CodeCluster, errx.DescCluster)

	// Cluster doctor package errors.
	ErrDoctorResourceNotFoundBeforeTimeout = newSentinelError("resource not found before timeout", errx.CodeCluster, errx.DescCluster)
	ErrDoctorDeploymentRolloutFailed       = newSentinelError("deployment rollout failed", errx.CodeCluster, errx.DescCluster)
	ErrDoctorPodsNotScheduledBeforeTimeout = newSentinelError("no scheduled pod found before timeout", errx.CodeCluster, errx.DescCluster)
	ErrDoctorDecodeBase64Failed            = newSentinelError("decode base64 value", errx.CodeCluster, errx.DescCluster)
	ErrDoctorImagePullStatusFailed         = newSentinelError("pod image pull status failed", errx.CodeCluster, errx.DescCluster)
	ErrDoctorPodPhaseFailed                = newSentinelError("pod phase Failed", errx.CodeCluster, errx.DescCluster)
	ErrDoctorKubectlError                  = newSentinelError("kubectl error", errx.CodeCluster, errx.DescCluster)
	ErrDoctorTraefikServiceNotFound        = newSentinelError("traefik service not found", errx.CodeCluster, errx.DescCluster)
	ErrDoctorDeploymentNotFound            = newSentinelError("deployment not found", errx.CodeCluster, errx.DescCluster)
	ErrDoctorUnexpectedReplicaStatus       = newSentinelError("unexpected replica status", errx.CodeCluster, errx.DescCluster)

	// Registry errors.
	ErrRegistryNotReady             = newSentinelError("registry not ready", errx.CodeRegistry, errx.DescRegistry)
	ErrRegistryNotFound             = newSentinelError("registry not found", errx.CodeRegistry, errx.DescRegistry)
	ErrBuildOperatorImageFailed     = newSentinelError("failed to build operator image", errx.CodeRegistry, errx.DescRegistry)
	ErrPushOperatorImageFailed      = newSentinelError("failed to push operator image", errx.CodeRegistry, errx.DescRegistry)
	ErrBuildGatewayProxyImageFailed = newSentinelError("failed to build gateway proxy image", errx.CodeRegistry, errx.DescRegistry)
	ErrPushGatewayProxyImageFailed  = newSentinelError("failed to push gateway proxy image", errx.CodeRegistry, errx.DescRegistry)
	ErrUnsupportedRegistryType      = newSentinelError("unsupported registry type", errx.CodeRegistry, errx.DescRegistry)
	ErrEnsureNamespaceFailed        = newSentinelError("failed to ensure namespace", errx.CodeRegistry, errx.DescRegistry)
	ErrReadRegistryStorageFailed    = newSentinelError("failed to read current registry storage size", errx.CodeRegistry, errx.DescRegistry)
	ErrUpdateRegistryStorageFailed  = newSentinelError("failed to update registry storage size", errx.CodeRegistry, errx.DescRegistry)
	ErrRegistryLoginFailed          = newSentinelError("failed to login to registry", errx.CodeRegistry, errx.DescRegistry)
	ErrTagImageFailed               = newSentinelError("failed to tag image", errx.CodeRegistry, errx.DescRegistry)
	ErrPushImageFailed              = newSentinelError("failed to push image", errx.CodeRegistry, errx.DescRegistry)
	ErrHelperNamespaceNotFound      = newSentinelError("helper namespace not found", errx.CodeRegistry, errx.DescRegistry)
	ErrSaveImageFailed              = newSentinelError("failed to save image", errx.CodeRegistry, errx.DescRegistry)
	ErrStartHelperPodFailed         = newSentinelError("failed to start helper pod", errx.CodeRegistry, errx.DescRegistry)
	ErrHelperPodNotReady            = newSentinelError("helper pod not ready", errx.CodeRegistry, errx.DescRegistry)
	ErrCopyImageToHelperFailed      = newSentinelError("failed to copy image tar to helper pod", errx.CodeRegistry, errx.DescRegistry)
	ErrPushImageFromHelperFailed    = newSentinelError("failed to push image from helper pod", errx.CodeRegistry, errx.DescRegistry)

	// Config errors.
	ErrRegistryURLRequired           = newSentinelError("registry url is required", errx.CodeConfig, errx.DescConfig)
	ErrRegistryURLMissingInConfig    = newSentinelError("registry url missing in config", errx.CodeConfig, errx.DescConfig)
	ErrSaveRegistryConfigFailed      = newSentinelError("failed to save registry config", errx.CodeConfig, errx.DescConfig)
	ErrReadRegistryConfigFailed      = newSentinelError("failed to read registry config", errx.CodeConfig, errx.DescConfig)
	ErrUnmarshalRegistryConfigFailed = newSentinelError("failed to unmarshal registry config", errx.CodeConfig, errx.DescConfig)

	// Build errors.
	ErrBuildImageFailed         = newSentinelError("failed to build image", errx.CodeBuild, errx.DescBuild)
	ErrMetadataFileNotFound     = newSentinelError("metadata file not found", errx.CodeBuild, errx.DescBuild)
	ErrServerNotFoundInMetadata = newSentinelError("server not found in metadata", errx.CodeBuild, errx.DescBuild)
	ErrMarshalMetadataFailed    = newSentinelError("failed to marshal metadata", errx.CodeBuild, errx.DescBuild)
	ErrWriteMetadataFailed      = newSentinelError("failed to write metadata", errx.CodeBuild, errx.DescBuild)

	// Server errors.
	ErrMarshalManifestFailed = newSentinelError("failed to marshal manifest", errx.CodeServer, errx.DescServer)
	ErrWriteManifestFailed   = newSentinelError("failed to write manifest", errx.CodeServer, errx.DescServer)
	ErrInvalidFilePath       = newSentinelError("invalid file path", errx.CodeServer, errx.DescServer)
	ErrFileNotAccessible     = newSentinelError("cannot access file", errx.CodeServer, errx.DescServer)
	ErrFileIsDirectory       = newSentinelError("path is a directory, not a file", errx.CodeServer, errx.DescServer)
	ErrGetMCPServerFailed    = newSentinelError("kubectl get mcpserver failed", errx.CodeServer, errx.DescServer)
	ErrListServersFailed     = newSentinelError("failed to list servers", errx.CodeServer, errx.DescServer)
	ErrCreateServerFailed    = newSentinelError("failed to create server", errx.CodeServer, errx.DescServer)
	ErrDeleteServerFailed    = newSentinelError("failed to delete server", errx.CodeServer, errx.DescServer)
	ErrViewServerLogsFailed  = newSentinelError("failed to view server logs", errx.CodeServer, errx.DescServer)
)

func specFor(base error) errorSpec {
	spec, ok := errorSpecs[base]
	if ok {
		return spec
	}
	return errorSpec{code: errx.CodeCLI, description: errx.DescCLI}
}

const maxDebugChainBytes = 64 * 1024

func trimDebugChainString(s string) string {
	if len(s) <= maxDebugChainBytes {
		return s
	}
	return s[:maxDebugChainBytes] + "\n... [error.debug_chain truncated]\n"
}

// logStructuredError logs an error with structured fields to terminal.
// Only logs when debug mode is enabled (via --debug flag).
// The zap logger is configured with console encoding, so structured fields
// are displayed in a human-readable format in the terminal.
//
// This extracts all context from errx.Error and logs it with structured fields:
// - error.code: "SETUP_CLUSTER_INIT_FAILED"
// - error.category: "Setup"
// - error.context.namespace: "registry"
// - error.context.image: "my-image:latest"
// - error.context.component: "registry" | "operator" | "server"
// - error.debug_chain: full flattened chain from errx.DebugString (all setup errors)
//
// Note: The Kubernetes operator (which runs in-cluster) uses controller-runtime's
// zap logger for structured logging that can be collected by log aggregation systems.
func logStructuredError(logger *zap.Logger, err error, msg string) {
	if logger == nil || err == nil || !IsDebugMode() {
		return
	}

	chain := trimDebugChainString(errx.DebugString(err))
	if keysAndValues, ok := errx.LogrKV(err); ok {
		fields := []zap.Field{zap.Error(err)}
		for i := 0; i+1 < len(keysAndValues); i += 2 {
			key, _ := keysAndValues[i].(string)
			fields = append(fields, zap.Any(key, keysAndValues[i+1]))
		}
		if chain != "" {
			fields = append(fields, zap.String("error.debug_chain", chain))
		}
		logger.Error(msg, fields...)
	} else {
		// Fallback for non-errx errors
		if chain != "" {
			logger.Error(msg, zap.Error(err), zap.String("error.debug_chain", chain))
		} else {
			logger.Error(msg, zap.Error(err))
		}
	}
}

func LogStructuredError(logger *zap.Logger, err error, msg string) {
	logStructuredError(logger, err, msg)
}
