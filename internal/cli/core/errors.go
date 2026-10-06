package core

// This file defines error handling utilities for the CLI, including:
//   - Base errors for different error categories (CLI, Cluster, Registry, etc.)
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

// newBaseError creates a base error and registers it in errorSpecs in one step.
// This eliminates redundancy between error definitions and errorSpecs mapping.
func newBaseError(msg string, code, description string) error {
	err := errors.New(msg)
	errorSpecs[err] = errorSpec{code: code, description: description}
	return err
}

// errorSpecs maps base errors to their error codes and descriptions.
// Populated automatically by newBaseError() during variable initialization.
// Must be declared before base errors to ensure proper initialization order.
var errorSpecs = make(map[error]errorSpec)

// lookupSpec provides a lookup function for errx.FromBase.
func lookupSpec(base error) (code, description string) {
	spec := specFor(base)
	return spec.code, spec.description
}

// newWithBase creates a new error using the appropriate errx category helper.
// The base error is used to determine the category, and the message provides context.
func newWithBase(base error, msg string) error {
	if base == nil {
		return errx.CreateByCode(errx.CodeCLI, errx.DescCLI, msg, nil)
	}
	return errx.FromBase(base, lookupSpec, msg, nil)
}

func NewWithBase(base error, msg string) error {
	return newWithBase(base, msg)
}

// wrapWithBase wraps a cause error using the appropriate errx category helper.
// The base error is used to determine the category, and the message provides context.
func wrapWithBase(base, cause error, msg string) error {
	if base == nil {
		return errx.CreateByCode(errx.CodeCLI, errx.DescCLI, msg, cause)
	}
	return errx.FromBase(base, lookupSpec, msg, cause)
}

func WrapWithBase(base, cause error, msg string) error {
	return wrapWithBase(base, cause, msg)
}

// wrapWithBaseAndContext wraps an error with additional structured context.
// This is useful for adding debugging information like namespace, resource names, etc.
func wrapWithBaseAndContext(base, cause error, msg string, context map[string]any) error {
	err := wrapWithBase(base, cause, msg)
	if errxErr, ok := err.(*errx.Error); ok && len(context) > 0 {
		return errxErr.WithContextMap(context)
	}
	return err
}

func WrapWithBaseAndContext(base, cause error, msg string, context map[string]any) error {
	return wrapWithBaseAndContext(base, cause, msg, context)
}

func NewSetupStepFailedError() error {
	return newWithBase(ErrSetupStepFailed, "cluster readiness or diagnostics found unmet requirements; see docs/cluster-readiness.md")
}

// Base errors for CLI operations.
// Errors are defined and registered in one step using newBaseError to eliminate redundancy.
var (
	// CLI errors.
	ErrImageRequired             = newBaseError("image is required", errx.CodeCLI, errx.DescCLI)
	ErrInvalidServerName         = newBaseError("invalid server name", errx.CodeCLI, errx.DescCLI)
	ErrGetWorkingDirectoryFailed = newBaseError("get working directory", errx.CodeCLI, errx.DescCLI)
	ErrControlCharsNotAllowed    = newBaseError("value must not contain control characters", errx.CodeCLI, errx.DescCLI)
	ErrFieldRequired             = newBaseError("field is required", errx.CodeCLI, errx.DescCLI)
	ErrGetHomeDirectoryFailed    = newBaseError("failed to get home directory", errx.CodeCLI, errx.DescCLI)
	ErrUnknownRegistryMode       = newBaseError("unknown registry mode", errx.CodeCLI, errx.DescCLI)

	// Auth package errors.
	ErrAuthAPIURLRequired                  = newBaseError("api URL is required", errx.CodeAuth, errx.DescAuth)
	ErrAuthAPIURLInvalid                   = newBaseError("api URL must include scheme and host", errx.CodeAuth, errx.DescAuth)
	ErrAuthEmailPasswordRequired           = newBaseError("email and password are both required for password login", errx.CodeAuth, errx.DescAuth)
	ErrAuthPlatformLoginFailed             = newBaseError("platform login failed", errx.CodeAuth, errx.DescAuth)
	ErrAuthReadStdinFailed                 = newBaseError("read stdin", errx.CodeAuth, errx.DescAuth)
	ErrAuthTTYRequired                     = newBaseError("not a TTY: pass --token, --token-stdin, or run in an interactive terminal", errx.CodeAuth, errx.DescAuth)
	ErrAuthReadTokenFailed                 = newBaseError("read token", errx.CodeAuth, errx.DescAuth)
	ErrAuthTokenRequired                   = newBaseError("token is required", errx.CodeAuth, errx.DescAuth)
	ErrAuthTokenVerificationFailed         = newBaseError("API token could not be verified", errx.CodeAuth, errx.DescAuth)
	ErrAuthLoginHTTPStatus                 = newBaseError("login HTTP status failed", errx.CodeAuth, errx.DescAuth)
	ErrAuthLoginResponseMissingAccessToken = newBaseError("login response did not include access_token", errx.CodeAuth, errx.DescAuth)
	ErrAuthServerRejectedToken             = newBaseError("server rejected the token", errx.CodeAuth, errx.DescAuth)
	ErrAuthAPIURLMayBeWrong                = newBaseError("API URL may be wrong", errx.CodeAuth, errx.DescAuth)
	ErrAuthVerifyRequestFailed             = newBaseError("verify request failed", errx.CodeAuth, errx.DescAuth)
	ErrAuthFileDescriptorOutOfRange        = newBaseError("file descriptor out of range", errx.CodeAuth, errx.DescAuth)

	// Pipeline errors.
	ErrLoadMetadataFailed      = newBaseError("failed to load metadata", errx.CodePipeline, errx.DescPipeline)
	ErrNoServersInMetadata     = newBaseError("no servers found in metadata", errx.CodePipeline, errx.DescPipeline)
	ErrGenerateCRDsFailed      = newBaseError("failed to generate CRDs", errx.CodePipeline, errx.DescPipeline)
	ErrListManifestFilesFailed = newBaseError("failed to list manifest files", errx.CodePipeline, errx.DescPipeline)
	ErrNoManifestFilesFound    = newBaseError("no manifest files found", errx.CodePipeline, errx.DescPipeline)
	ErrApplyManifestFailed     = newBaseError("failed to apply manifest", errx.CodePipeline, errx.DescPipeline)

	// Operator errors.
	ErrOperatorNotFound = newBaseError("operator not found", errx.CodeOperator, errx.DescOperator)
	ErrOperatorNotReady = newBaseError("operator not ready", errx.CodeOperator, errx.DescOperator)

	// Setup errors.
	ErrClusterInitFailed                   = newBaseError("failed to initialize cluster", errx.CodeSetup, errx.DescSetup)
	ErrClusterConfigFailed                 = newBaseError("cluster configuration failed", errx.CodeSetup, errx.DescSetup)
	ErrTLSSetupFailed                      = newBaseError("TLS setup failed", errx.CodeSetup, errx.DescSetup)
	ErrDeployRegistryFailed                = newBaseError("failed to deploy registry", errx.CodeSetup, errx.DescSetup)
	ErrOperatorImageBuildFailed            = newBaseError("operator image build failed", errx.CodeSetup, errx.DescSetup)
	ErrGatewayProxyImageBuildFailed        = newBaseError("gateway proxy image build failed", errx.CodeSetup, errx.DescSetup)
	ErrEnsureRegistryNamespaceFailed       = newBaseError("failed to ensure registry namespace", errx.CodeSetup, errx.DescSetup)
	ErrPushOperatorImageInternalFailed     = newBaseError("failed to push operator image to internal registry", errx.CodeSetup, errx.DescSetup)
	ErrPushGatewayProxyImageInternalFailed = newBaseError("failed to push gateway proxy image to internal registry", errx.CodeSetup, errx.DescSetup)
	ErrOperatorDeploymentFailed            = newBaseError("operator deployment failed", errx.CodeSetup, errx.DescSetup)
	ErrConfigureExternalRegistryEnvFailed  = newBaseError("failed to configure external registry env on operator", errx.CodeSetup, errx.DescSetup)
	ErrRestartOperatorDeploymentFailed     = newBaseError("failed to restart operator deployment after registry env update", errx.CodeSetup, errx.DescSetup)
	ErrCRDCheckFailed                      = newBaseError("CRD check failed", errx.CodeSetup, errx.DescSetup)
	ErrRenderSecretManifestFailed          = newBaseError("render secret manifest", errx.CodeSetup, errx.DescSetup)
	ErrApplySecretManifestFailed           = newBaseError("apply secret manifest", errx.CodeSetup, errx.DescSetup)
	ErrMarshalDockerConfigFailed           = newBaseError("marshal docker config", errx.CodeSetup, errx.DescSetup)
	ErrApplyImagePullSecretFailed          = newBaseError("apply imagePullSecret", errx.CodeSetup, errx.DescSetup)
	ErrPushImageInClusterFailed            = newBaseError("failed to push image in-cluster", errx.CodeSetup, errx.DescSetup)
	ErrSetupStepFailed                     = newBaseError("setup step failed", errx.CodeSetup, errx.DescSetup)
	ErrApplyCRDFailed                      = newBaseError("failed to apply CRD", errx.CodeSetup, errx.DescSetup)
	ErrEnsureOperatorNamespaceFailed       = newBaseError("failed to ensure operator namespace", errx.CodeSetup, errx.DescSetup)
	ErrApplyRBACFailed                     = newBaseError("failed to apply RBAC", errx.CodeSetup, errx.DescSetup)
	ErrReadManagerYAMLFailed               = newBaseError("failed to read manager.yaml", errx.CodeSetup, errx.DescSetup)
	ErrReadIngressManifestFailed           = newBaseError("failed to read ingress manifest", errx.CodeSetup, errx.DescSetup)
	ErrParseManagerYAMLFailed              = newBaseError("failed to parse manager.yaml", errx.CodeSetup, errx.DescSetup)
	ErrSetOperatorImageFailed              = newBaseError("failed to set operator image", errx.CodeSetup, errx.DescSetup)
	ErrMutateManagerYAMLFailed             = newBaseError("failed to mutate manager.yaml", errx.CodeSetup, errx.DescSetup)
	ErrRenderManagerYAMLFailed             = newBaseError("failed to render mutated manager.yaml", errx.CodeSetup, errx.DescSetup)
	ErrCreateTempFileFailed                = newBaseError("failed to create temp file", errx.CodeSetup, errx.DescSetup)
	ErrCloseTempFileFailed                 = newBaseError("failed to close temp file", errx.CodeSetup, errx.DescSetup)
	ErrWriteTempFileFailed                 = newBaseError("failed to write temp file", errx.CodeSetup, errx.DescSetup)
	ErrApplyManagerDeploymentFailed        = newBaseError("failed to apply manager deployment", errx.CodeSetup, errx.DescSetup)
	ErrClusterIssuerApplyFailed            = newBaseError("failed to apply ClusterIssuer", errx.CodeSetup, errx.DescSetup)
	ErrCreateRegistryNamespaceFailed       = newBaseError("failed to create registry namespace", errx.CodeSetup, errx.DescSetup)
	ErrApplyCertificateFailed              = newBaseError("failed to apply Certificate", errx.CodeSetup, errx.DescSetup)

	// Setup platform package errors.
	ErrSetupImagePlatformNoNodeArchitectures       = newBaseError("could not resolve setup image platform: no Kubernetes node architectures were reported", errx.CodeSetup, errx.DescSetup)
	ErrSetupImagePlatformMixedNodeArchitectures    = newBaseError("mixed Kubernetes node architectures detected", errx.CodeSetup, errx.DescSetup)
	ErrSetupImagePlatformMismatch                  = newBaseError("MCP_IMAGE_PLATFORM does not match Kubernetes node architecture", errx.CodeSetup, errx.DescSetup)
	ErrSetupImagePlatformInvalid                   = newBaseError("invalid MCP_IMAGE_PLATFORM", errx.CodeSetup, errx.DescSetup)
	ErrSetupImagePlatformUnsupported               = newBaseError("unsupported MCP_IMAGE_PLATFORM", errx.CodeSetup, errx.DescSetup)
	ErrSetupImagePlatformKubectlNil                = newBaseError("could not resolve setup image platform: kubectl runner is nil", errx.CodeSetup, errx.DescSetup)
	ErrSetupInspectNodeArchitecturesFailed         = newBaseError("could not inspect Kubernetes node architectures", errx.CodeSetup, errx.DescSetup)
	ErrSetupInvalidStorageMode                     = newBaseError("invalid storage mode", errx.CodeSetup, errx.DescSetup)
	ErrSetupInvalidPlatformMode                    = newBaseError("invalid platform mode", errx.CodeSetup, errx.DescSetup)
	ErrSetupInvalidRegistryMode                    = newBaseError("invalid registry mode", errx.CodeSetup, errx.DescSetup)
	ErrSetupSetRuntimeTestModeFailed               = newBaseError("set MCP_RUNTIME_TEST_MODE", errx.CodeSetup, errx.DescSetup)
	ErrSetupUnsetRuntimeTestModeFailed             = newBaseError("unset MCP_RUNTIME_TEST_MODE", errx.CodeSetup, errx.DescSetup)
	ErrSetupSetPlatformModeFailed                  = newBaseError("set MCP_PLATFORM_MODE", errx.CodeSetup, errx.DescSetup)
	ErrSetupListTraefikDeploymentsFailed           = newBaseError("list traefik deployments", errx.CodeSetup, errx.DescSetup)
	ErrSetupMarshalTraefikDeploymentPatchFailed    = newBaseError("marshal traefik deployment patch", errx.CodeSetup, errx.DescSetup)
	ErrSetupReadTraefikDeploymentFailed            = newBaseError("read traefik deployment", errx.CodeSetup, errx.DescSetup)
	ErrSetupDecodeTraefikDeploymentFailed          = newBaseError("decode traefik deployment", errx.CodeSetup, errx.DescSetup)
	ErrSetupDeploymentReadinessDeadlineExceeded    = newBaseError("deployment readiness deadline exceeded", errx.CodeSetup, errx.DescSetup)
	ErrSetupTLSKubectlRunnerNil                    = newBaseError("kubectl runner is nil", errx.CodeSetup, errx.DescSetup)
	ErrSetupInspectClusterIssuerFailed             = newBaseError("inspect ClusterIssuer", errx.CodeSetup, errx.DescSetup)
	ErrSetupTLSCertificateSANsEmpty                = newBaseError("no DNS names or IP addresses resolved for the Certificate", errx.CodeSetup, errx.DescSetup)
	ErrSetupDeleteClickHouseInitJobFailed          = newBaseError("delete existing clickhouse init job", errx.CodeSetup, errx.DescSetup)
	ErrSetupAnalyticsRolloutFailed                 = newBaseError("analytics components failed to roll out", errx.CodeSetup, errx.DescSetup)
	ErrSetupRenderManifestFailed                   = newBaseError("render manifest", errx.CodeSetup, errx.DescSetup)
	ErrSetupApplyPlatformUIIngressFailed           = newBaseError("apply platform UI ingress", errx.CodeSetup, errx.DescSetup)
	ErrSetupRemovePathBasedPlatformIngressesFailed = newBaseError("remove path-based platform ingresses for public platform host", errx.CodeSetup, errx.DescSetup)
	ErrSetupDecodeAnalyticsConfigManifestFailed    = newBaseError("decode analytics config manifest", errx.CodeSetup, errx.DescSetup)
	ErrSetupEncodeAnalyticsConfigManifestFailed    = newBaseError("encode analytics config manifest", errx.CodeSetup, errx.DescSetup)
	ErrSetupReadConfigMapFailed                    = newBaseError("read configmap", errx.CodeSetup, errx.DescSetup)
	ErrSetupDecodeConfigMapFailed                  = newBaseError("decode configmap", errx.CodeSetup, errx.DescSetup)
	ErrSetupReadSecretKeyFailed                    = newBaseError("read secret key", errx.CodeSetup, errx.DescSetup)
	ErrSetupDecodeSecretKeyFailed                  = newBaseError("decode secret key", errx.CodeSetup, errx.DescSetup)

	// Platform update errors.
	ErrUpdateTargetRequired      = newBaseError("update target required", errx.CodeSetup, errx.DescSetup)
	ErrUpdateManifestInvalid     = newBaseError("release manifest invalid", errx.CodeSetup, errx.DescSetup)
	ErrUpdateTargetMismatch      = newBaseError("release manifest version does not match --to", errx.CodeSetup, errx.DescSetup)
	ErrUpdateInvalidFlag         = newBaseError("invalid update flag", errx.CodeSetup, errx.DescSetup)
	ErrUpdateKubeClientFailed    = newBaseError("create Kubernetes client for update", errx.CodeSetup, errx.DescSetup)
	ErrUpdateNotInstalled        = newBaseError("cluster is not an MCP Runtime install", errx.CodeSetup, errx.DescSetup)
	ErrUpdateInventoryFailed     = newBaseError("read installed platform inventory", errx.CodeSetup, errx.DescSetup)
	ErrUpdateCRDChange           = newBaseError("release changes CRDs", errx.CodeSetup, errx.DescSetup)
	ErrUpdateBlocked             = newBaseError("update plan has blocked components", errx.CodeSetup, errx.DescSetup)
	ErrUpdateConfirmationMissing = newBaseError("update confirmation required", errx.CodeSetup, errx.DescSetup)
	ErrUpdateAborted             = newBaseError("update aborted", errx.CodeSetup, errx.DescSetup)
	ErrUpdateRolloutFailed       = newBaseError("platform update rollout failed", errx.CodeSetup, errx.DescSetup)
	ErrUpdateBuildFailed         = newBaseError("platform update image build failed", errx.CodeSetup, errx.DescSetup)

	// Cert errors.
	ErrCertManagerNotInstalled     = newBaseError("cert-manager not installed", errx.CodeCert, errx.DescCert)
	ErrCertManagerInstallFailed    = newBaseError("cert-manager install failed", errx.CodeCert, errx.DescCert)
	ErrCASecretNotFound            = newBaseError("CA secret not found", errx.CodeCert, errx.DescCert)
	ErrCASecretInvalid             = newBaseError("CA secret invalid", errx.CodeCert, errx.DescCert)
	ErrCAExpired                   = newBaseError("CA certificate expired", errx.CodeCert, errx.DescCert)
	ErrCANearExpiry                = newBaseError("CA certificate near expiry", errx.CodeCert, errx.DescCert)
	ErrCertificateNotReady         = newBaseError("certificate not ready", errx.CodeCert, errx.DescCert)
	ErrClusterIssuerNotFound       = newBaseError("ClusterIssuer not found", errx.CodeCert, errx.DescCert)
	ErrRegistryCertificateNotFound = newBaseError("registry Certificate not found", errx.CodeCert, errx.DescCert)

	// Certmanager package errors.
	ErrCertEncodeGeneratedCAFailed         = newBaseError("failed to encode generated internal CA", errx.CodeCert, errx.DescCert)
	ErrCertLookupRegistryIngressFailed     = newBaseError("failed to look up registry ingress", errx.CodeCert, errx.DescCert)
	ErrCertRemoveRegistryIngressAnnotation = newBaseError("failed to remove cert-manager.io/cluster-issuer from registry ingress", errx.CodeCert, errx.DescCert)
	ErrCertRegistryTLSSecretConflict       = newBaseError("registry TLS secret is already referenced by Certificate(s)", errx.CodeCert, errx.DescCert)
	ErrCertListCertificatesFailed          = newBaseError("failed to list cert-manager Certificates", errx.CodeCert, errx.DescCert)
	ErrCertParseCertificatesFailed         = newBaseError("failed to parse cert-manager Certificates", errx.CodeCert, errx.DescCert)
	ErrCertACMEPublicDNSNameRequired       = newBaseError("ACME public CA requires a public DNS name", errx.CodeCert, errx.DescCert)
	ErrCertACMEPublicDNSNameInvalid        = newBaseError("ACME public CA requires a public DNS name; invalid host", errx.CodeCert, errx.DescCert)
	ErrCertACMEIngressManifestInvalid      = newBaseError("http-01 ingress manifest is not valid for Let's Encrypt", errx.CodeCert, errx.DescCert)
	ErrCertTraefikNotReady                 = newBaseError("traefik not ready", errx.CodeCert, errx.DescCert)
	ErrCertACMEEmailRequired               = newBaseError("ACME email is required", errx.CodeCert, errx.DescCert)
	ErrCertCertificateSANsEmpty            = newBaseError("TLS has no DNS names or IP addresses to request", errx.CodeCert, errx.DescCert)

	// Cluster errors.
	ErrCRDNotInstalled                = newBaseError("MCPServer CRD not installed", errx.CodeCluster, errx.DescCluster)
	ErrClusterNotAccessible           = newBaseError("cluster not accessible", errx.CodeCluster, errx.DescCluster)
	ErrNamespaceNotFound              = newBaseError("namespace not found", errx.CodeCluster, errx.DescCluster)
	ErrDeploymentTimeout              = newBaseError("deployment timed out waiting for readiness", errx.CodeCluster, errx.DescCluster)
	ErrInstallCRDFailed               = newBaseError("failed to install CRD", errx.CodeCluster, errx.DescCluster)
	ErrEnsureRuntimeNamespaceFailed   = newBaseError("failed to ensure mcp-runtime namespace", errx.CodeCluster, errx.DescCluster)
	ErrEnsureServersNamespaceFailed   = newBaseError("failed to ensure mcp-servers namespace", errx.CodeCluster, errx.DescCluster)
	ErrKubeconfigNotReadable          = newBaseError("kubeconfig not found or not readable", errx.CodeCluster, errx.DescCluster)
	ErrSetKubeconfigFailed            = newBaseError("failed to set KUBECONFIG", errx.CodeCluster, errx.DescCluster)
	ErrSetContextFailed               = newBaseError("failed to set context", errx.CodeCluster, errx.DescCluster)
	ErrAKSKubeconfigNotImplemented    = newBaseError("AKS kubeconfig not yet implemented", errx.CodeCluster, errx.DescCluster)
	ErrGKEKubeconfigNotImplemented    = newBaseError("GKE kubeconfig not yet implemented", errx.CodeCluster, errx.DescCluster)
	ErrUnsupportedProvider            = newBaseError("unsupported provider", errx.CodeCluster, errx.DescCluster)
	ErrInvalidClusterName             = newBaseError("invalid cluster name", errx.CodeCluster, errx.DescCluster)
	ErrInvalidNodeCount               = newBaseError("invalid node count", errx.CodeCluster, errx.DescCluster)
	ErrUnsupportedIngressController   = newBaseError("unsupported ingress controller", errx.CodeCluster, errx.DescCluster)
	ErrInstallIngressControllerFailed = newBaseError("failed to install ingress controller", errx.CodeCluster, errx.DescCluster)
	ErrCreateKindConfigFailed         = newBaseError("failed to create temp kind config", errx.CodeCluster, errx.DescCluster)
	ErrCloseKindConfigFailed          = newBaseError("failed to close kind config", errx.CodeCluster, errx.DescCluster)
	ErrWriteKindConfigFailed          = newBaseError("failed to write kind config", errx.CodeCluster, errx.DescCluster)
	ErrCreateKindClusterFailed        = newBaseError("failed to create kind cluster", errx.CodeCluster, errx.DescCluster)
	ErrDockerDaemonNotReachable       = newBaseError("docker daemon not reachable", errx.CodeCluster, errx.DescCluster)
	ErrKindClusterAlreadyExists       = newBaseError("kind cluster already exists", errx.CodeCluster, errx.DescCluster)
	ErrGKEProvisioningNotImplemented  = newBaseError("GKE provisioning not yet implemented", errx.CodeCluster, errx.DescCluster)
	ErrProvisionEKSFailed             = newBaseError("failed to provision EKS cluster", errx.CodeCluster, errx.DescCluster)
	ErrAKSProvisioningNotImplemented  = newBaseError("AKS provisioning not yet implemented", errx.CodeCluster, errx.DescCluster)

	// Cluster doctor package errors.
	ErrDoctorResourceNotFoundBeforeTimeout = newBaseError("resource not found before timeout", errx.CodeCluster, errx.DescCluster)
	ErrDoctorDeploymentRolloutFailed       = newBaseError("deployment rollout failed", errx.CodeCluster, errx.DescCluster)
	ErrDoctorPodsNotScheduledBeforeTimeout = newBaseError("no scheduled pod found before timeout", errx.CodeCluster, errx.DescCluster)
	ErrDoctorDecodeBase64Failed            = newBaseError("decode base64 value", errx.CodeCluster, errx.DescCluster)
	ErrDoctorImagePullStatusFailed         = newBaseError("pod image pull status failed", errx.CodeCluster, errx.DescCluster)
	ErrDoctorPodPhaseFailed                = newBaseError("pod phase Failed", errx.CodeCluster, errx.DescCluster)
	ErrDoctorKubectlError                  = newBaseError("kubectl error", errx.CodeCluster, errx.DescCluster)
	ErrDoctorTraefikServiceNotFound        = newBaseError("traefik service not found", errx.CodeCluster, errx.DescCluster)
	ErrDoctorDeploymentNotFound            = newBaseError("deployment not found", errx.CodeCluster, errx.DescCluster)
	ErrDoctorUnexpectedReplicaStatus       = newBaseError("unexpected replica status", errx.CodeCluster, errx.DescCluster)

	// Registry errors.
	ErrRegistryNotReady             = newBaseError("registry not ready", errx.CodeRegistry, errx.DescRegistry)
	ErrRegistryNotFound             = newBaseError("registry not found", errx.CodeRegistry, errx.DescRegistry)
	ErrBuildOperatorImageFailed     = newBaseError("failed to build operator image", errx.CodeRegistry, errx.DescRegistry)
	ErrPushOperatorImageFailed      = newBaseError("failed to push operator image", errx.CodeRegistry, errx.DescRegistry)
	ErrBuildGatewayProxyImageFailed = newBaseError("failed to build gateway proxy image", errx.CodeRegistry, errx.DescRegistry)
	ErrPushGatewayProxyImageFailed  = newBaseError("failed to push gateway proxy image", errx.CodeRegistry, errx.DescRegistry)
	ErrUnsupportedRegistryType      = newBaseError("unsupported registry type", errx.CodeRegistry, errx.DescRegistry)
	ErrEnsureNamespaceFailed        = newBaseError("failed to ensure namespace", errx.CodeRegistry, errx.DescRegistry)
	ErrReadRegistryStorageFailed    = newBaseError("failed to read current registry storage size", errx.CodeRegistry, errx.DescRegistry)
	ErrUpdateRegistryStorageFailed  = newBaseError("failed to update registry storage size", errx.CodeRegistry, errx.DescRegistry)
	ErrRegistryLoginFailed          = newBaseError("failed to login to registry", errx.CodeRegistry, errx.DescRegistry)
	ErrTagImageFailed               = newBaseError("failed to tag image", errx.CodeRegistry, errx.DescRegistry)
	ErrPushImageFailed              = newBaseError("failed to push image", errx.CodeRegistry, errx.DescRegistry)
	ErrHelperNamespaceNotFound      = newBaseError("helper namespace not found", errx.CodeRegistry, errx.DescRegistry)
	ErrSaveImageFailed              = newBaseError("failed to save image", errx.CodeRegistry, errx.DescRegistry)
	ErrStartHelperPodFailed         = newBaseError("failed to start helper pod", errx.CodeRegistry, errx.DescRegistry)
	ErrHelperPodNotReady            = newBaseError("helper pod not ready", errx.CodeRegistry, errx.DescRegistry)
	ErrCopyImageToHelperFailed      = newBaseError("failed to copy image tar to helper pod", errx.CodeRegistry, errx.DescRegistry)
	ErrPushImageFromHelperFailed    = newBaseError("failed to push image from helper pod", errx.CodeRegistry, errx.DescRegistry)

	// Config errors.
	ErrRegistryURLRequired           = newBaseError("registry url is required", errx.CodeConfig, errx.DescConfig)
	ErrRegistryURLMissingInConfig    = newBaseError("registry url missing in config", errx.CodeConfig, errx.DescConfig)
	ErrSaveRegistryConfigFailed      = newBaseError("failed to save registry config", errx.CodeConfig, errx.DescConfig)
	ErrReadRegistryConfigFailed      = newBaseError("failed to read registry config", errx.CodeConfig, errx.DescConfig)
	ErrUnmarshalRegistryConfigFailed = newBaseError("failed to unmarshal registry config", errx.CodeConfig, errx.DescConfig)

	// Build errors.
	ErrBuildImageFailed         = newBaseError("failed to build image", errx.CodeBuild, errx.DescBuild)
	ErrMetadataFileNotFound     = newBaseError("metadata file not found", errx.CodeBuild, errx.DescBuild)
	ErrServerNotFoundInMetadata = newBaseError("server not found in metadata", errx.CodeBuild, errx.DescBuild)
	ErrMarshalMetadataFailed    = newBaseError("failed to marshal metadata", errx.CodeBuild, errx.DescBuild)
	ErrWriteMetadataFailed      = newBaseError("failed to write metadata", errx.CodeBuild, errx.DescBuild)

	// Server errors.
	ErrMarshalManifestFailed = newBaseError("failed to marshal manifest", errx.CodeServer, errx.DescServer)
	ErrWriteManifestFailed   = newBaseError("failed to write manifest", errx.CodeServer, errx.DescServer)
	ErrInvalidFilePath       = newBaseError("invalid file path", errx.CodeServer, errx.DescServer)
	ErrFileNotAccessible     = newBaseError("cannot access file", errx.CodeServer, errx.DescServer)
	ErrFileIsDirectory       = newBaseError("path is a directory, not a file", errx.CodeServer, errx.DescServer)
	ErrGetMCPServerFailed    = newBaseError("kubectl get mcpserver failed", errx.CodeServer, errx.DescServer)
	ErrListServersFailed     = newBaseError("failed to list servers", errx.CodeServer, errx.DescServer)
	ErrCreateServerFailed    = newBaseError("failed to create server", errx.CodeServer, errx.DescServer)
	ErrDeleteServerFailed    = newBaseError("failed to delete server", errx.CodeServer, errx.DescServer)
	ErrViewServerLogsFailed  = newBaseError("failed to view server logs", errx.CodeServer, errx.DescServer)
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
