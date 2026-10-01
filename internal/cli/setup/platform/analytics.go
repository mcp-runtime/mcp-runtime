package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kube"
	"mcp-runtime/internal/cli/kubeerr"
	"mcp-runtime/internal/cli/registry"
	"mcp-runtime/internal/cli/setup/assetpath"
	"mcp-runtime/internal/cli/setup/ingressmanifest"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

const (
	kafkaStatefulSetName     = "kafka"
	kafkaTopicInitJob        = "kafka-topic-init"
	kafkaPodName             = "kafka-0"
	kafkaPodContainer        = "kafka"
	kafkaPVCName             = "kafka-data-kafka-0"
	kafkaHeadlessServiceName = "kafka-headless"
	kafkaKRaftReplicaCount   = int32(3)
)

type analyticsRolloutTarget struct{ kind, name, namespace string }

func removeBundledOAuthServer(kubectl core.KubectlRunner) error {
	resources := []struct{ kind, name string }{
		{kind: "service", name: "mcp-oauth-server"},
		{kind: "deployment", name: "mcp-oauth-server"},
		{kind: "ingress", name: "mcp-oauth-server"},
		{kind: "networkpolicy", name: "mcp-oauth-server-egress"},
	}
	for _, resource := range resources {
		cmd, err := kubectl.CommandArgs([]string{"delete", resource.kind, resource.name, "-n", core.DefaultAnalyticsNamespace, "--ignore-not-found"})
		if err != nil {
			return err
		}
		cmd.SetStdout(os.Stdout)
		cmd.SetStderr(os.Stderr)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("remove bundled OAuth server %s/%s: %w", resource.kind, resource.name, err)
		}
	}
	return nil
}

func removeBundledOAuthServerClientGo() error {
	return removeBundledOAuthServer(core.DefaultKubectlClient())
}

// removeLegacyPromtail removes the collector left behind by installations that
// ran it in mcp-sentinel. It runs only after the replacement DaemonSet is ready.
func removeLegacyPromtail(kubectl core.KubectlRunner) error {
	for _, resource := range []string{"daemonset/promtail", "serviceaccount/promtail", "configmap/promtail-config"} {
		if err := kubectl.RunWithOutput([]string{"delete", resource, "-n", core.DefaultAnalyticsNamespace, "--ignore-not-found", "--cascade=foreground", "--wait=true"}, os.Stdout, os.Stderr); err != nil {
			return fmt.Errorf("remove legacy log collector %s from namespace %s: %w", resource, core.DefaultAnalyticsNamespace, err)
		}
	}
	// This path shares typed-client setup for namespace/RBAC finalization.
	return finishCollectorAdmissionCutoverClientGo()
}

func removeLegacyPromtailClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if err := clients.Clientset.AppsV1().DaemonSets(core.DefaultAnalyticsNamespace).Delete(ctx, "promtail", metav1.DeleteOptions{PropagationPolicy: propagationForeground()}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove legacy log collector daemonset from namespace %s: %w", core.DefaultAnalyticsNamespace, err)
	}
	// Foreground deletion waits for the old hostPath pods to disappear before
	// revoking their discovery access or tightening the source namespace policy.
	if err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := clients.Clientset.AppsV1().DaemonSets(core.DefaultAnalyticsNamespace).Get(ctx, "promtail", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}); err != nil {
		return fmt.Errorf("wait for legacy collector deletion: %w", err)
	}
	if err := clients.Clientset.CoreV1().ServiceAccounts(core.DefaultAnalyticsNamespace).Delete(ctx, "promtail", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove legacy log collector service account from namespace %s: %w", core.DefaultAnalyticsNamespace, err)
	}
	if err := clients.Clientset.CoreV1().ConfigMaps(core.DefaultAnalyticsNamespace).Delete(ctx, "promtail-config", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("remove legacy log collector config from namespace %s: %w", core.DefaultAnalyticsNamespace, err)
	}
	return finishCollectorAdmissionCutoverClientGo()
}

func analyticsServiceManifests(postgresManifest string) []string {
	manifests := []string{
		postgresManifest,
		"k8s/06-ingest.yaml",
		"k8s/07-processor.yaml",
		"k8s/08-platform-api.yaml",
		"k8s/08-platform-api-rbac.yaml",
		"k8s/08-runtime-api.yaml",
		"k8s/08-runtime-api-rbac.yaml",
		"k8s/08-analytics-api.yaml",
		"k8s/22-split-api-networkpolicy.yaml",
		"k8s/09-ui.yaml",
		"k8s/10-gateway.yaml",
		"k8s/11-prometheus.yaml",
		"k8s/15-otel-collector.yaml",
		"k8s/16-tempo.yaml",
		"k8s/17-loki.yaml",
		"k8s/18-promtail.yaml",
		"k8s/19-grafana-datasources.yaml",
		"k8s/21-grafana-dashboards.yaml",
		"k8s/12-grafana.yaml",
	}
	return manifests
}

func deployAnalyticsManifests(logger *zap.Logger, images AnalyticsImageSet, storageMode, platformMode string) error {
	return deployAnalyticsManifestsClientGo(logger, images, storageMode, platformMode)
}

func deployAnalyticsManifestsClientGo(logger *zap.Logger, images AnalyticsImageSet, storageMode, platformMode string) error {
	if err := ensurePlatformNamespaceBeforeIngress(); err != nil {
		return err
	}
	rolloutTimeoutDuration := analyticsRolloutTimeoutDuration()
	rolloutTimeout := rolloutTimeoutDuration.String()

	if err := ensureRepoManagedTraefikMiddlewareResourcesClientGo(logger); err != nil {
		return err
	}
	if err := removeBundledOAuthServerClientGo(); err != nil {
		return err
	}

	core.Info("Applying mcp-sentinel namespace and config")
	manifests := []string{
		"k8s/00-namespace.yaml",
		"k8s/01-config.yaml",
	}
	for _, manifest := range manifests {
		if err := applyRenderedManifestClientGo(manifest, images, "", platformMode); err != nil {
			return err
		}
	}
	if err := applyCatalogNamespaceForMode(platformMode); err != nil {
		return err
	}

	core.Info("Applying mcp-sentinel managed secrets")
	secretManifest, err := renderAnalyticsSecretManifestClientGo()
	if err != nil {
		return err
	}
	if err := applyManifestYAML(secretManifest, "", os.Stdout); err != nil {
		return err
	}

	imagePullSecretName, err := ensureAnalyticsImagePullSecretClientGo(images)
	if err != nil {
		return err
	}
	if err := ensureOperatorPublicRegistryPullSecretClientGo(images); err != nil {
		return err
	}

	// Sync Postgres password: if Postgres is already running, the pod has an
	// existing database password that may differ from what was just written to
	// mcp-sentinel-secrets. Apply the current secret value via ALTER USER so
	// the API can connect on reruns without needing a Postgres pod restart.
	if err := syncPostgresPasswordClientGo(); err != nil {
		core.Warn(fmt.Sprintf("Could not sync Postgres password (will retry on next run): %v", err))
	}

	clickhouseManifest := "k8s/03-clickhouse.yaml"
	kafkaManifest := "k8s/05-kafka.yaml"
	postgresManifest := "k8s/20-postgres.yaml"
	if storageMode == setupplan.StorageModeHostpath {
		clickhouseManifest = "k8s/03-clickhouse-hostpath.yaml"
		kafkaManifest = "k8s/05-kafka-hostpath.yaml"
		postgresManifest = "k8s/20-postgres-hostpath.yaml"
	}

	if err := ensureAnalyticsHostpathDirs(storageMode); err != nil {
		return err
	}
	if err := reconcileKafkaStatefulSetForKRaftUpgradeClientGo(); err != nil {
		return err
	}

	core.Info("Applying analytics storage and messaging components")
	for _, manifest := range []string{
		clickhouseManifest,
		kafkaManifest,
	} {
		if err := applyRenderedManifestClientGo(manifest, images, imagePullSecretName, platformMode); err != nil {
			return err
		}
	}

	if err := waitForRolloutStatusWithClientGo("statefulset", "clickhouse", core.DefaultAnalyticsNamespace, rolloutTimeoutDuration); err != nil {
		return mcpSentinelDependencyRolloutFailed(core.DefaultKubectlClient(), err, "statefulset", "clickhouse", core.DefaultAnalyticsNamespace, "storage (clickhouse)")
	}
	if err := waitForKafkaRolloutClientGo(logger, rolloutTimeoutDuration, storageMode); err != nil {
		return mcpSentinelDependencyRolloutFailed(core.DefaultKubectlClient(), err, "statefulset", "kafka", core.DefaultAnalyticsNamespace, "messaging (kafka)")
	}
	if err := initializeKafkaTopicsClientGo(images, imagePullSecretName, platformMode, rolloutTimeoutDuration); err != nil {
		return err
	}

	core.Info("Initializing ClickHouse schema")
	if err := deleteJobIfExistsClientGo("clickhouse-init", core.DefaultAnalyticsNamespace); err != nil {
		return core.WrapWithSentinel(core.ErrSetupDeleteClickHouseInitJobFailed, err, fmt.Sprintf("delete existing clickhouse init job: %v", err))
	}
	if err := applyRenderedManifestClientGo("k8s/04-clickhouse-init.yaml", images, imagePullSecretName, platformMode); err != nil {
		return err
	}
	if err := waitForJobCompletionClientGo("clickhouse-init", core.DefaultAnalyticsNamespace, rolloutTimeoutDuration); err != nil {
		return mcpSentinelDependencyJobFailed(core.DefaultKubectlClient(), err, "clickhouse-init", core.DefaultAnalyticsNamespace, "clickhouse init schema")
	}

	core.Info("Applying analytics services")
	for _, manifest := range analyticsServiceManifests(postgresManifest) {
		if err := applyRenderedManifestClientGo(manifest, images, imagePullSecretName, platformMode); err != nil {
			return err
		}
	}

	if err := applyPlatformIngressIfConfigured(); err != nil {
		return err
	}
	if err := ensureSessionLocalDeploymentReplicasClientGo(logger); err != nil {
		return err
	}

	core.Info(fmt.Sprintf("Waiting for mcp-sentinel workload rollouts (per-resource timeout %s; override with MCP_DEPLOYMENT_TIMEOUT)", rolloutTimeout))
	targets := []analyticsRolloutTarget{
		{kind: "statefulset", name: "mcp-sentinel-postgres"},
		{kind: "deployment", name: "mcp-sentinel-ingest"},
		{kind: "deployment", name: "mcp-sentinel-processor"},
		{kind: "deployment", name: "mcp-platform-api"},
		{kind: "deployment", name: "mcp-runtime-api"},
		{kind: "deployment", name: "mcp-analytics-api"},
		{kind: "deployment", name: "mcp-sentinel-ui"},
		{kind: "deployment", name: "mcp-sentinel-gateway"},
		{kind: "deployment", name: "prometheus"},
		{kind: "deployment", name: "grafana"},
		{kind: "deployment", name: "otel-collector"},
		{kind: "statefulset", name: "tempo"},
		{kind: "statefulset", name: "loki"},
		{kind: "daemonset", name: "promtail", namespace: core.LogCollectorNamespace},
	}
	rolloutFailures, failedForDebug := waitForAnalyticsTargetsClientGo(targets, rolloutTimeoutDuration)
	if len(rolloutFailures) == 0 {
		if err := removeLegacyPromtailClientGo(); err != nil {
			return err
		}
		core.Success("mcp-sentinel manifests deployed successfully")
		return nil
	}
	if recovered, recoverErr := recoverKafkaClusterIDMismatchClientGo(logger, storageMode); recoverErr != nil {
		return recoverErr
	} else if recovered {
		rolloutFailures, failedForDebug = waitForAnalyticsTargetsClientGo(targets, rolloutTimeoutDuration)
		if len(rolloutFailures) == 0 {
			if err := removeLegacyPromtailClientGo(); err != nil {
				return err
			}
			core.Success("mcp-sentinel manifests deployed successfully")
			return nil
		}
	}

	printAnalyticsRolloutDiagnostics(core.DefaultKubectlClient())
	summary := strings.Join(rolloutFailures, "; ")
	cause := core.NewWithSentinel(core.ErrSetupAnalyticsRolloutFailed, summary)
	msg := fmt.Sprintf("analytics components failed to roll out: %s", summary)
	ctx := map[string]any{"component": "mcp-sentinel", "rollout_failures": summary}
	if core.IsDebugMode() {
		if diag := buildAnalyticsRolloutDebugDetail(core.DefaultKubectlClient(), failedForDebug); diag != "" {
			ctx["diagnostics"] = trimDiagnosticsString(diag)
		}
	}
	return core.WrapWithSentinelAndContext(core.ErrOperatorDeploymentFailed, cause, msg, ctx)
}

func deployAnalyticsManifestsWithKubectl(kubectl core.KubectlRunner, logger *zap.Logger, images AnalyticsImageSet, storageMode, platformMode string) error {
	if err := ensurePlatformNamespaceBeforeIngress(); err != nil {
		return err
	}
	rolloutTimeout := analyticsRolloutTimeoutString()

	if err := ensureRepoManagedTraefikMiddlewareResources(kubectl, logger); err != nil {
		return err
	}
	if err := removeBundledOAuthServer(kubectl); err != nil {
		return err
	}

	core.Info("Applying mcp-sentinel namespace and config")
	manifests := []string{
		"k8s/00-namespace.yaml",
		"k8s/01-config.yaml",
	}
	for _, manifest := range manifests {
		if err := applyRenderedManifest(kubectl, manifest, images, "", platformMode); err != nil {
			return err
		}
	}
	if err := applyCatalogNamespaceForMode(platformMode); err != nil {
		return err
	}

	core.Info("Applying mcp-sentinel managed secrets")
	secretManifest, err := renderCredentialBundleClientGo()
	if err != nil {
		return err
	}
	if err := applyManifestYAML(secretManifest, "", os.Stdout); err != nil {
		return err
	}

	imagePullSecretName, err := ensureAnalyticsImagePullSecret(kubectl, images)
	if err != nil {
		return err
	}
	if err := ensureOperatorPublicRegistryPullSecret(kubectl, images); err != nil {
		return err
	}

	clickhouseManifest := "k8s/03-clickhouse.yaml"
	kafkaManifest := "k8s/05-kafka.yaml"
	postgresManifest := "k8s/20-postgres.yaml"
	if storageMode == setupplan.StorageModeHostpath {
		clickhouseManifest = "k8s/03-clickhouse-hostpath.yaml"
		kafkaManifest = "k8s/05-kafka-hostpath.yaml"
		postgresManifest = "k8s/20-postgres-hostpath.yaml"
	}

	if err := ensureAnalyticsHostpathDirs(storageMode); err != nil {
		return err
	}
	if err := reconcileKafkaStatefulSetForKRaftUpgradeWithKubectl(kubectl); err != nil {
		return err
	}

	core.Info("Applying analytics storage and messaging components")
	for _, manifest := range []string{
		clickhouseManifest,
		kafkaManifest,
	} {
		if err := applyRenderedManifest(kubectl, manifest, images, imagePullSecretName, platformMode); err != nil {
			return err
		}
	}

	if err := waitForRolloutStatusWithKubectl(kubectl, "statefulset", "clickhouse", core.DefaultAnalyticsNamespace, rolloutTimeout); err != nil {
		return mcpSentinelDependencyRolloutFailed(kubectl, err, "statefulset", "clickhouse", core.DefaultAnalyticsNamespace, "storage (clickhouse)")
	}
	if err := waitForKafkaRolloutWithKubectl(kubectl, rolloutTimeout, storageMode); err != nil {
		return mcpSentinelDependencyRolloutFailed(kubectl, err, "statefulset", "kafka", core.DefaultAnalyticsNamespace, "messaging (kafka)")
	}
	if err := initializeKafkaTopicsWithKubectl(kubectl, images, imagePullSecretName, platformMode, rolloutTimeout); err != nil {
		return err
	}

	core.Info("Initializing ClickHouse schema")
	if err := deleteJobIfExistsWithKubectl(kubectl, "clickhouse-init", core.DefaultAnalyticsNamespace); err != nil {
		return core.WrapWithSentinel(core.ErrSetupDeleteClickHouseInitJobFailed, err, fmt.Sprintf("delete existing clickhouse init job: %v", err))
	}
	if err := applyRenderedManifest(kubectl, "k8s/04-clickhouse-init.yaml", images, imagePullSecretName, platformMode); err != nil {
		return err
	}
	if err := waitForJobCompletionWithKubectl(kubectl, "clickhouse-init", core.DefaultAnalyticsNamespace, rolloutTimeout); err != nil {
		return mcpSentinelDependencyJobFailed(kubectl, err, "clickhouse-init", core.DefaultAnalyticsNamespace, "clickhouse init schema")
	}

	core.Info("Applying analytics services")
	for _, manifest := range analyticsServiceManifests(postgresManifest) {
		if err := applyRenderedManifest(kubectl, manifest, images, imagePullSecretName, platformMode); err != nil {
			return err
		}
	}
	if err := ensureRuntimeKubernetesAPIEgressClientGo(); err != nil {
		return err
	}

	if err := applyPlatformIngressIfConfigured(); err != nil {
		return err
	}
	if err := ensureSessionLocalDeploymentReplicas(kubectl, logger); err != nil {
		return err
	}

	core.Info(fmt.Sprintf("Waiting for mcp-sentinel workload rollouts (per-resource timeout %s; override with MCP_DEPLOYMENT_TIMEOUT)", rolloutTimeout))
	targets := []analyticsRolloutTarget{
		{kind: "statefulset", name: "mcp-sentinel-postgres"},
		{kind: "deployment", name: "mcp-sentinel-ingest"},
		{kind: "deployment", name: "mcp-sentinel-processor"},
		{kind: "deployment", name: "mcp-platform-api"},
		{kind: "deployment", name: "mcp-runtime-api"},
		{kind: "deployment", name: "mcp-analytics-api"},
		{kind: "deployment", name: "mcp-sentinel-ui"},
		{kind: "deployment", name: "mcp-sentinel-gateway"},
		{kind: "deployment", name: "prometheus"},
		{kind: "deployment", name: "grafana"},
		{kind: "deployment", name: "otel-collector"},
		{kind: "statefulset", name: "tempo"},
		{kind: "statefulset", name: "loki"},
		{kind: "daemonset", name: "promtail", namespace: core.LogCollectorNamespace},
	}
	rolloutFailures, failedForDebug := waitForAnalyticsTargetsWithKubectl(kubectl, targets, rolloutTimeout)
	if len(rolloutFailures) == 0 {
		if err := removeLegacyPromtail(kubectl); err != nil {
			return err
		}
		core.Success("mcp-sentinel manifests deployed successfully")
		return nil
	}
	if recovered, recoverErr := recoverKafkaClusterIDMismatchWithKubectl(kubectl, storageMode); recoverErr != nil {
		return recoverErr
	} else if recovered {
		rolloutFailures, failedForDebug = waitForAnalyticsTargetsWithKubectl(kubectl, targets, rolloutTimeout)
		if len(rolloutFailures) == 0 {
			if err := removeLegacyPromtail(kubectl); err != nil {
				return err
			}
			core.Success("mcp-sentinel manifests deployed successfully")
			return nil
		}
	}

	printAnalyticsRolloutDiagnostics(kubectl)
	summary := strings.Join(rolloutFailures, "; ")
	cause := core.NewWithSentinel(core.ErrSetupAnalyticsRolloutFailed, summary)
	msg := fmt.Sprintf("analytics components failed to roll out: %s", summary)
	ctx := map[string]any{"component": "mcp-sentinel", "rollout_failures": summary}
	if core.IsDebugMode() {
		if diag := buildAnalyticsRolloutDebugDetail(kubectl, failedForDebug); diag != "" {
			ctx["diagnostics"] = trimDiagnosticsString(diag)
		}
	}
	return core.WrapWithSentinelAndContext(core.ErrOperatorDeploymentFailed, cause, msg, ctx)
}

// ensureRuntimeKubernetesAPIEgressClientGo adds the discovered Kubernetes API
// destination to the runtime API NetworkPolicy. Service IPs are inside the
// cluster CIDR, so a broad public 0.0.0.0/0 rule does not reliably cover them
// on k3s/Cilium and similar CNIs. Discovering the Service and client endpoint
// keeps setup portable across distributions and API ports.
func ensureRuntimeKubernetesAPIEgressClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	ctx := context.Background()
	policy, err := clients.Clientset.NetworkingV1().NetworkPolicies(core.DefaultAnalyticsNamespace).Get(ctx, "mcp-runtime-api-platform-egress", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read runtime API NetworkPolicy: %w", err)
	}

	addCIDR := func(cidr string, port int32) {
		if strings.TrimSpace(cidr) == "" {
			return
		}
		for _, rule := range policy.Spec.Egress {
			for _, peer := range rule.To {
				if peer.IPBlock == nil || peer.IPBlock.CIDR != cidr {
					continue
				}
				for _, rulePort := range rule.Ports {
					if rulePort.Port != nil && rulePort.Port.IntVal == port {
						return
					}
				}
			}
		}
		policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: protocolPtr(corev1.ProtocolTCP), Port: intstrPtr(port)}},
		})
	}

	if service, getErr := clients.Clientset.CoreV1().Services("default").Get(ctx, "kubernetes", metav1.GetOptions{}); getErr == nil {
		if ip := net.ParseIP(strings.TrimSpace(service.Spec.ClusterIP)); ip != nil {
			addCIDR(networkHostCIDR(ip), 443)
		}
	}
	if clients.Config != nil {
		if parsed, parseErr := url.Parse(clients.Config.Host); parseErr == nil {
			host := parsed.Hostname()
			if ip := net.ParseIP(host); ip != nil {
				port := int32(443)
				if parsed.Port() != "" {
					if parsedPort, convErr := strconv.ParseInt(parsed.Port(), 10, 32); convErr == nil {
						port = int32(parsedPort)
					}
				}
				addCIDR(networkHostCIDR(ip), port)
			}
		}
	}
	if _, err := clients.Clientset.NetworkingV1().NetworkPolicies(core.DefaultAnalyticsNamespace).Update(ctx, policy, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update runtime API Kubernetes API egress: %w", err)
	}
	return nil
}

func networkHostCIDR(ip net.IP) string {
	if ip.To4() == nil {
		return ip.String() + "/128"
	}
	return ip.String() + "/32"
}

func protocolPtr(value corev1.Protocol) *corev1.Protocol { return &value }
func intstrPtr(value int32) *intstr.IntOrString          { result := intstr.FromInt(int(value)); return &result }

func waitForAnalyticsTargetsClientGo(targets []analyticsRolloutTarget, rolloutTimeout time.Duration) ([]string, []analyticsFailedRollout) {
	var rolloutFailures []string
	var failedForDebug []analyticsFailedRollout
	for _, target := range targets {
		namespace := target.namespace
		if namespace == "" {
			namespace = core.DefaultAnalyticsNamespace
		}
		rolloutLog, err := runRolloutWithOptionalDebugCaptureClientGo(target.kind, target.name, namespace, rolloutTimeout)
		if err != nil {
			rolloutFailures = append(rolloutFailures, fmt.Sprintf("%s/%s/%s: %v", namespace, target.kind, target.name, err))
			failedForDebug = append(failedForDebug, analyticsFailedRollout{
				kind: target.kind, name: target.name, namespace: namespace, rolloutLog: rolloutLog,
			})
		}
	}
	return rolloutFailures, failedForDebug
}

func waitForAnalyticsTargetsWithKubectl(kubectl core.KubectlRunner, targets []analyticsRolloutTarget, rolloutTimeout string) ([]string, []analyticsFailedRollout) {
	var rolloutFailures []string
	var failedForDebug []analyticsFailedRollout
	for _, target := range targets {
		namespace := target.namespace
		if namespace == "" {
			namespace = core.DefaultAnalyticsNamespace
		}
		rolloutLog, err := runRolloutWithOptionalDebugCapture(kubectl, target.kind, target.name, namespace, rolloutTimeout)
		if err != nil {
			rolloutFailures = append(rolloutFailures, fmt.Sprintf("%s/%s/%s: %v", namespace, target.kind, target.name, err))
			failedForDebug = append(failedForDebug, analyticsFailedRollout{
				kind: target.kind, name: target.name, namespace: namespace, rolloutLog: rolloutLog,
			})
		}
	}
	return rolloutFailures, failedForDebug
}

func waitForKafkaRolloutClientGo(logger *zap.Logger, rolloutTimeout time.Duration, storageMode string) error {
	if err := waitForRolloutStatusWithClientGo("statefulset", kafkaStatefulSetName, core.DefaultAnalyticsNamespace, rolloutTimeout); err == nil {
		return nil
	} else {
		recovered, recoverErr := recoverKafkaClusterIDMismatchClientGo(logger, storageMode)
		if recoverErr != nil {
			return recoverErr
		}
		if recovered {
			return waitForRolloutStatusWithClientGo("statefulset", kafkaStatefulSetName, core.DefaultAnalyticsNamespace, rolloutTimeout)
		}
		return err
	}
}

func isKubectlNotFound(output string) bool {
	normalized := strings.ToLower(output)
	return strings.Contains(normalized, "not found") || strings.Contains(normalized, "notfound")
}

func waitForKafkaRolloutWithKubectl(kubectl core.KubectlRunner, rolloutTimeout, storageMode string) error {
	if err := waitForRolloutStatusWithKubectl(kubectl, "statefulset", kafkaStatefulSetName, core.DefaultAnalyticsNamespace, rolloutTimeout); err == nil {
		return nil
	} else {
		recovered, recoverErr := recoverKafkaClusterIDMismatchWithKubectl(kubectl, storageMode)
		if recoverErr != nil {
			return recoverErr
		}
		if recovered {
			return waitForRolloutStatusWithKubectl(kubectl, "statefulset", kafkaStatefulSetName, core.DefaultAnalyticsNamespace, rolloutTimeout)
		}
		return err
	}
}

func recoverKafkaClusterIDMismatchClientGo(logger *zap.Logger, _ string) (bool, error) {
	clients, err := platformKubernetesClients()
	if err != nil {
		return false, err
	}
	logs, err := kafkaLogsClientGo(clients)
	if err != nil || !isKafkaClusterIDMismatchLog(logs) {
		return false, nil
	}
	if logger != nil {
		logger.Warn("Kafka cluster ID mismatch detected; refusing destructive automatic recovery")
	}
	return false, fmt.Errorf(
		"kafka cluster ID does not match stored metadata; setup will not delete persistent volume %s/%s automatically. Restore or migrate metadata to match the Kafka volume, or explicitly reset both stores if data loss is acceptable",
		core.DefaultAnalyticsNamespace, kafkaPVCName,
	)
}

func recoverKafkaClusterIDMismatchWithKubectl(kubectl core.KubectlRunner, _ string) (bool, error) {
	logs, err := kafkaLogsWithKubectl(kubectl)
	if err != nil || !isKafkaClusterIDMismatchLog(logs) {
		return false, nil
	}
	return false, fmt.Errorf(
		"kafka cluster ID does not match stored metadata; setup will not delete persistent volume %s/%s automatically. Restore or migrate metadata to match the Kafka volume, or explicitly reset both stores if data loss is acceptable",
		core.DefaultAnalyticsNamespace, kafkaPVCName,
	)
}

func kafkaLogsClientGo(clients *k8sclient.Clients) (string, error) {
	for _, previous := range []bool{false, true} {
		req := clients.Clientset.CoreV1().Pods(core.DefaultAnalyticsNamespace).GetLogs(kafkaPodName, &corev1.PodLogOptions{
			Container: kafkaPodContainer,
			Previous:  previous,
		})
		stream, err := req.Stream(context.Background())
		if err != nil {
			continue
		}
		data, readErr := io.ReadAll(stream)
		_ = stream.Close()
		if readErr == nil && strings.TrimSpace(string(data)) != "" {
			return string(data), nil
		}
	}
	return "", fmt.Errorf("kafka logs unavailable")
}

func kafkaLogsWithKubectl(kubectl core.KubectlRunner) (string, error) {
	for _, args := range [][]string{
		{"logs", kafkaPodName, "-n", core.DefaultAnalyticsNamespace, "-c", kafkaPodContainer},
		{"logs", kafkaPodName, "-n", core.DefaultAnalyticsNamespace, "-c", kafkaPodContainer, "--previous"},
	} {
		out, err := kubectlText(kubectl, args)
		if err == nil && strings.TrimSpace(out) != "" {
			return out, nil
		}
	}
	return "", fmt.Errorf("kafka logs unavailable")
}

func isKafkaClusterIDMismatchLog(logs string) bool {
	return strings.Contains(logs, "InconsistentClusterIdException") && strings.Contains(logs, "clusterId")
}

func initializeKafkaTopicsClientGo(images AnalyticsImageSet, imagePullSecretName, platformMode string, timeout time.Duration) error {
	if err := deleteJobIfExistsClientGo(kafkaTopicInitJob, core.DefaultAnalyticsNamespace); err != nil {
		return err
	}
	if err := applyRenderedManifestClientGo("k8s/05-kafka-topic-init.yaml", images, imagePullSecretName, platformMode); err != nil {
		return err
	}
	if err := waitForJobCompletionClientGo(kafkaTopicInitJob, core.DefaultAnalyticsNamespace, timeout); err != nil {
		return mcpSentinelDependencyJobFailed(core.DefaultKubectlClient(), err, kafkaTopicInitJob, core.DefaultAnalyticsNamespace, "Kafka topic initialization")
	}
	return nil
}

func initializeKafkaTopicsWithKubectl(kubectl core.KubectlRunner, images AnalyticsImageSet, imagePullSecretName, platformMode, timeout string) error {
	if err := deleteJobIfExistsWithKubectl(kubectl, kafkaTopicInitJob, core.DefaultAnalyticsNamespace); err != nil {
		return err
	}
	if err := applyRenderedManifest(kubectl, "k8s/05-kafka-topic-init.yaml", images, imagePullSecretName, platformMode); err != nil {
		return err
	}
	if err := waitForJobCompletionWithKubectl(kubectl, kafkaTopicInitJob, core.DefaultAnalyticsNamespace, timeout); err != nil {
		return mcpSentinelDependencyJobFailed(kubectl, err, kafkaTopicInitJob, core.DefaultAnalyticsNamespace, "Kafka topic initialization")
	}
	return nil
}

func applyCatalogNamespaceForMode(platformMode string) error {
	namespace := setupplan.CatalogNamespaceForPlatformMode(platformMode)
	if strings.TrimSpace(namespace) == "" {
		return nil
	}
	core.Info(fmt.Sprintf("Applying platform catalog namespace %s", namespace))
	return ensureNamespaceWithLabels(namespace, catalogNamespaceLabels(platformMode))
}

func trimDiagnosticsString(s string) string {
	const maxBytes = 300 * 1024
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n... [diagnostics truncated]\n"
}

// buildAnalyticsRolloutDebugDetail collects kubectl output for mcp-sentinel (describe + get) when --debug is set.
func buildAnalyticsRolloutDebugDetail(kubectl core.KubectlRunner, failed []analyticsFailedRollout) string {
	var b strings.Builder
	for _, w := range failed {
		if strings.TrimSpace(w.rolloutLog) != "" {
			b.WriteString(fmt.Sprintf("---- kubectl rollout status %s/%s\n", w.kind, w.name))
			b.WriteString(w.rolloutLog)
		}
		namespace := w.namespace
		if namespace == "" {
			namespace = core.DefaultAnalyticsNamespace
		}
		b.WriteString(fmt.Sprintf("---- describe %s %s in %s\n", w.kind, w.name, namespace))
		out, err := kubectlText(kubectl, []string{
			"describe", w.kind, w.name, "-n", namespace, "--request-timeout=30s",
		})
		if err != nil {
			b.WriteString(fmt.Sprintf("error: %v\n", err))
			continue
		}
		b.WriteString(out)
	}
	b.WriteString("---- get pods (wide)\n")
	if out, err := kubectlText(kubectl, []string{"get", "pods", "-n", core.DefaultAnalyticsNamespace, "-o", "wide", "--request-timeout=30s"}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- get events (sorted)\n")
	if out, err := kubectlText(kubectl, []string{
		"get", "events", "-n", core.DefaultAnalyticsNamespace, "--sort-by", ".lastTimestamp", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	return b.String()
}

func applyRenderedManifest(kubectl core.KubectlRunner, manifestPath string, images AnalyticsImageSet, imagePullSecretName, platformMode string) error {
	resolvedManifestPath, err := assetpath.ResolveRepoAssetPath(manifestPath)
	if err != nil {
		return core.WrapWithSentinel(core.ErrReadManagerYAMLFailed, err, fmt.Sprintf("failed to resolve manifest %s: %v", manifestPath, err))
	}

	content, err := kube.ReadFileAtPath(resolvedManifestPath)
	if err != nil {
		return core.WrapWithSentinel(core.ErrReadManagerYAMLFailed, err, fmt.Sprintf("failed to read manifest %s: %v", resolvedManifestPath, err))
	}
	rendered := ""
	if manifestPath == "k8s/01-config.yaml" {
		rendered, err = renderAnalyticsConfigManifest(kubectl, string(content), platformMode, images)
	} else {
		rendered, err = renderAnalyticsManifest(string(content), images, imagePullSecretName, platformMode)
	}
	if err != nil {
		return core.WrapWithSentinel(core.ErrSetupRenderManifestFailed, err, fmt.Sprintf("render manifest %s: %v", manifestPath, err))
	}
	rendered, err = stampDependencyRevisionsClientGo(rendered)
	if err != nil {
		return fmt.Errorf("plan dependency rollouts for %s: %w", manifestPath, err)
	}
	if manifestPath == "k8s/00-namespace.yaml" {
		rendered, err = deferPlatformAdmissionManifest(rendered)
		if err != nil {
			return err
		}
	}
	return applyManifestYAML(rendered, "", os.Stdout)
}

func applyRenderedManifestClientGo(manifestPath string, images AnalyticsImageSet, imagePullSecretName, platformMode string) error {
	resolvedManifestPath, err := assetpath.ResolveRepoAssetPath(manifestPath)
	if err != nil {
		return core.WrapWithSentinel(core.ErrReadManagerYAMLFailed, err, fmt.Sprintf("failed to resolve manifest %s: %v", manifestPath, err))
	}

	content, err := kube.ReadFileAtPath(resolvedManifestPath)
	if err != nil {
		return core.WrapWithSentinel(core.ErrReadManagerYAMLFailed, err, fmt.Sprintf("failed to read manifest %s: %v", resolvedManifestPath, err))
	}
	rendered := ""
	if manifestPath == "k8s/01-config.yaml" {
		rendered, err = renderAnalyticsConfigManifestClientGo(string(content), platformMode, images)
	} else {
		rendered, err = renderAnalyticsManifest(string(content), images, imagePullSecretName, platformMode)
	}
	if err != nil {
		return core.WrapWithSentinel(core.ErrSetupRenderManifestFailed, err, fmt.Sprintf("render manifest %s: %v", manifestPath, err))
	}
	rendered, err = stampDependencyRevisionsClientGo(rendered)
	if err != nil {
		return fmt.Errorf("plan dependency rollouts for %s: %w", manifestPath, err)
	}
	if manifestPath == "k8s/00-namespace.yaml" {
		rendered, err = deferPlatformAdmissionManifest(rendered)
		if err != nil {
			return err
		}
	}
	return applyManifestYAML(rendered, "", os.Stdout)
}

func applyPlatformIngressIfConfigured() error {
	host := strings.TrimSpace(core.GetPlatformIngressHost())
	if host == "" {
		return nil
	}
	manifest := ingressmanifest.RenderPlatformUIIngress(host, core.GetRegistryClusterIssuerName(), core.GetRegistryClusterIssuerName() != "" || core.GetProvidedTLSSecrets(), core.DefaultAnalyticsNamespace)
	core.Info(fmt.Sprintf("Applying platform UI ingress for %s", host))
	if err := applyManifestYAML(manifest, "", os.Stdout); err != nil {
		return core.WrapWithSentinel(core.ErrSetupApplyPlatformUIIngressFailed, err, fmt.Sprintf("apply platform UI ingress: %v", err))
	}
	if err := removePathBasedSentinelIngresses(); err != nil {
		return err
	}
	return nil
}

func removePathBasedSentinelIngresses() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	for _, name := range pathBasedSentinelIngressNames {
		err := clients.Clientset.NetworkingV1().Ingresses(core.DefaultAnalyticsNamespace).Delete(context.Background(), name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return core.WrapWithSentinel(core.ErrSetupRemovePathBasedSentinelIngressesFailed, err, fmt.Sprintf("remove path-based sentinel ingress %s/%s for public platform host: %v", core.DefaultAnalyticsNamespace, name, err))
		}
	}
	return nil
}

func renderAnalyticsManifest(content string, images AnalyticsImageSet, imagePullSecretName, platformMode string) (string, error) {
	replacements := map[string]string{}
	if strings.Contains(content, "# MCP_KUBERNETES_API_PORT") {
		port := core.DefaultCLIConfig.KubernetesAPIPort
		if port < 1 || port > 65535 {
			return "", fmt.Errorf("MCP_KUBERNETES_API_PORT must be between 1 and 65535, got %d", port)
		}
		replacements["port: 6443 # MCP_KUBERNETES_API_PORT"] = fmt.Sprintf("port: %d # MCP_KUBERNETES_API_PORT", port)
	}
	if mode, ok := setupplan.NormalizePlatformMode(platformMode); ok && mode != "" {
		replacements[`PLATFORM_MODE: "tenant"`] = fmt.Sprintf(`PLATFORM_MODE: "%s"`, mode)
	}
	if strings.TrimSpace(images.Ingest) != "" {
		replacements["image: mcp-sentinel-ingest:latest"] = "image: " + images.Ingest
	}
	if strings.TrimSpace(images.PlatformAPI) != "" {
		replacements["image: mcp-platform-api:latest"] = "image: " + images.PlatformAPI
	}
	if strings.TrimSpace(images.RuntimeAPI) != "" {
		replacements["image: mcp-runtime-api:latest"] = "image: " + images.RuntimeAPI
	}
	if strings.TrimSpace(images.AnalyticsAPI) != "" {
		replacements["image: mcp-analytics-api:latest"] = "image: " + images.AnalyticsAPI
	}
	if strings.TrimSpace(images.Processor) != "" {
		replacements["image: mcp-sentinel-processor:latest"] = "image: " + images.Processor
	}
	if strings.TrimSpace(images.UI) != "" {
		replacements["image: mcp-sentinel-ui:latest"] = "image: " + images.UI
	}
	if strings.TrimSpace(images.Traefik) != "" {
		replacements["image: traefik:v3.0"] = "image: " + images.Traefik
	}
	if strings.TrimSpace(images.ClickHouse) != "" {
		replacements["image: clickhouse/clickhouse-server:23.8"] = "image: " + images.ClickHouse
	}
	if strings.TrimSpace(images.Kafka) != "" {
		replacements["image: confluentinc/cp-kafka:7.5.1"] = "image: " + images.Kafka
	}
	if strings.TrimSpace(images.Prometheus) != "" {
		replacements["image: prom/prometheus:v2.49.1"] = "image: " + images.Prometheus
	}
	if strings.TrimSpace(images.OTelCollector) != "" {
		replacements["image: otel/opentelemetry-collector:0.92.0"] = "image: " + images.OTelCollector
	}
	if strings.TrimSpace(images.Tempo) != "" {
		replacements["image: grafana/tempo:2.3.1"] = "image: " + images.Tempo
	}
	if strings.TrimSpace(images.Loki) != "" {
		replacements["image: grafana/loki:2.9.4"] = "image: " + images.Loki
	}
	if strings.TrimSpace(images.Promtail) != "" {
		replacements["image: grafana/promtail:2.9.4"] = "image: " + images.Promtail
	}
	if strings.TrimSpace(images.Grafana) != "" {
		replacements["image: grafana/grafana:10.2.3"] = "image: " + images.Grafana
	}
	rendered := content
	if dnsKey := strings.TrimSpace(os.Getenv("MCP_DNS_LABEL_KEY")); dnsKey != "" {
		dnsValue := strings.TrimSpace(os.Getenv("MCP_DNS_LABEL_VALUE"))
		if dnsValue == "" {
			return "", fmt.Errorf("MCP_DNS_LABEL_VALUE is required when MCP_DNS_LABEL_KEY is set")
		}
		rendered = strings.ReplaceAll(rendered, "k8s-app: kube-dns", dnsKey+": "+dnsValue)
	}
	if clusterDomain := strings.TrimSpace(os.Getenv("MCP_CLUSTER_DOMAIN")); clusterDomain != "" {
		rendered = strings.ReplaceAll(rendered, ".svc.cluster.local", ".svc."+strings.TrimSuffix(clusterDomain, "."))
	}
	if storageClass := strings.TrimSpace(os.Getenv("MCP_STORAGE_CLASS")); storageClass != "" {
		rendered = strings.ReplaceAll(rendered, "storageClassName: local-path", "storageClassName: "+storageClass)
	}
	for oldValue, newValue := range replacements {
		rendered = strings.ReplaceAll(rendered, oldValue, newValue)
	}
	// Test-mode setup republishes these services under :latest. An existing
	// Kind node may already cache that tag, so those workloads must pull it
	// again when their pod template changes. Keep pinned release images on the
	// manifest's IfNotPresent policy.
	for _, image := range []string{images.PlatformAPI, images.RuntimeAPI, images.AnalyticsAPI, images.UI} {
		if !strings.HasSuffix(strings.TrimSpace(image), ":latest") {
			continue
		}
		oldValue := "image: " + image + "\n          imagePullPolicy: IfNotPresent"
		newValue := "image: " + image + "\n          imagePullPolicy: Always"
		rendered = strings.ReplaceAll(rendered, oldValue, newValue)
	}
	if strings.TrimSpace(imagePullSecretName) == "" {
		return rendered, nil
	}

	rendered, err := injectImagePullSecretsIntoManifest(rendered, imagePullSecretName)
	if err != nil {
		return "", err
	}
	return rendered, nil
}

type analyticsConfigMapReader func(namespace, name string) (map[string]string, error)
type analyticsTraefikNamespaceResolver func() string

func renderAnalyticsConfigManifest(kubectl core.KubectlRunner, content, platformMode string, images AnalyticsImageSet) (string, error) {
	return renderAnalyticsConfigManifestWithReaders(
		content,
		platformMode,
		images,
		func(namespace, name string) (map[string]string, error) {
			return existingConfigMapData(kubectl, namespace, name)
		},
		func() string {
			return activeTraefikNamespaceForPlatform(kubectl)
		},
	)
}

func renderAnalyticsConfigManifestClientGo(content, platformMode string, images AnalyticsImageSet) (string, error) {
	return renderAnalyticsConfigManifestWithReaders(
		content,
		platformMode,
		images,
		existingConfigMapDataClientGo,
		activeTraefikNamespaceForPlatformClientGo,
	)
}

func renderAnalyticsConfigManifestWithReaders(content, platformMode string, images AnalyticsImageSet, readConfigMap analyticsConfigMapReader, resolveTraefikNamespace analyticsTraefikNamespaceResolver) (string, error) {
	type configMapManifest struct {
		APIVersion string            `yaml:"apiVersion"`
		Kind       string            `yaml:"kind"`
		Metadata   map[string]any    `yaml:"metadata"`
		Data       map[string]string `yaml:"data"`
	}

	var manifest configMapManifest
	if err := yaml.Unmarshal([]byte(content), &manifest); err != nil {
		return "", core.WrapWithSentinel(core.ErrSetupDecodeAnalyticsConfigManifestFailed, err, fmt.Sprintf("decode analytics config manifest: %v", err))
	}
	if manifest.Data == nil {
		manifest.Data = map[string]string{}
	}

	existingData, err := readConfigMap(core.DefaultAnalyticsNamespace, "mcp-sentinel-config")
	if err != nil {
		return "", err
	}
	for _, key := range []string{
		"GOOGLE_CLIENT_ID",
		"MCP_SENTINEL_INGEST_URL",
		"OIDC_ISSUER",
		"OIDC_AUDIENCE",
		"OIDC_JWKS_URL",
		"MCP_PLATFORM_DOMAIN",
		"OAUTH_ISSUER_URL",
		"OAUTH_ALLOWED_REDIRECT_URI_SCHEMES",
		"OAUTH_INTERNAL_ISSUER_URL",
		"MCP_MCP_INGRESS_HOST",
		"MCP_REGISTRY_ENDPOINT",
		"MCP_REGISTRY_INGRESS_HOST",
		"PLATFORM_REGISTRY_URL",
		"PLATFORM_TRAEFIK_NAMESPACE",
		"PLATFORM_TEAM_TRAEFIK_WATCH",
		"MCP_MTLS_CLUSTER_ISSUER",
		"MCP_TRUST_DOMAIN",
	} {
		if envValue := setupAnalyticsConfigEnvValue(key); envValue != "" {
			manifest.Data[key] = envValue
			continue
		}
		if strings.TrimSpace(manifest.Data[key]) == "" && strings.TrimSpace(existingData[key]) != "" {
			manifest.Data[key] = existingData[key]
		}
	}
	if smokeImage := strings.TrimSpace(images.DoctorSmoke); smokeImage != "" {
		manifest.Data["MCP_DOCTOR_SMOKE_IMAGE"] = smokeImage
	}
	if os.Getenv("MCP_RUNTIME_TEST_MODE") == "1" {
		manifest.Data["MCP_RUNTIME_TEST_MODE"] = "1"
	} else {
		delete(manifest.Data, "MCP_RUNTIME_TEST_MODE")
	}
	applyGoogleOIDCDefaults(manifest.Data)
	if registryIngressHost := strings.TrimSpace(core.GetRegistryIngressHost()); registryIngressHost != "" && registryIngressHost != core.DefaultRegistryIngressHost {
		manifest.Data["MCP_REGISTRY_ENDPOINT"] = registryIngressHost
	} else if registryEndpoint := strings.TrimSpace(resolveInternalPlatformRegistryURLClientGo(nil)); registryEndpoint != "" {
		manifest.Data["MCP_REGISTRY_ENDPOINT"] = registryEndpoint
	}
	if registryIngressHost := strings.TrimSpace(core.GetRegistryIngressHost()); registryIngressHost != "" {
		manifest.Data["MCP_REGISTRY_INGRESS_HOST"] = registryIngressHost
	}
	if registryHost := platformRegistryHostForConfig(images); registryHost != "" {
		manifest.Data["PLATFORM_REGISTRY_URL"] = registryHost
	}
	if strings.TrimSpace(manifest.Data["OAUTH_ISSUER_URL"]) == "" {
		if platformHost := strings.TrimSpace(core.GetPlatformIngressHost()); platformHost != "" {
			scheme := "http"
			if strings.TrimSpace(core.GetRegistryClusterIssuerName()) != "" || core.GetProvidedTLSSecrets() {
				scheme = "https"
			}
			manifest.Data["OAUTH_ISSUER_URL"] = scheme + "://" + platformHost + "/oauth"
		} else {
			manifest.Data["OAUTH_ISSUER_URL"] = "http://localhost:18080/oauth"
		}
	}
	if strings.TrimSpace(manifest.Data["PLATFORM_TRAEFIK_NAMESPACE"]) == "" {
		if namespace := resolveTraefikNamespace(); namespace != "" {
			manifest.Data["PLATFORM_TRAEFIK_NAMESPACE"] = namespace
		}
	}
	applyPlatformTeamTraefikWatchDefault(manifest.Data)
	if existingMode, ok := setupplan.NormalizePlatformMode(existingData["PLATFORM_MODE"]); ok {
		requestedMode, requestedOK := setupplan.NormalizePlatformMode(platformMode)
		switch {
		case !requestedOK:
			manifest.Data["PLATFORM_MODE"] = existingMode
		case requestedMode == setupplan.PlatformModeTenant && existingMode != setupplan.PlatformModeTenant:
			manifest.Data["PLATFORM_MODE"] = existingMode
		default:
			manifest.Data["PLATFORM_MODE"] = requestedMode
		}
	} else if mode, ok := setupplan.NormalizePlatformMode(platformMode); ok {
		manifest.Data["PLATFORM_MODE"] = mode
	}

	rendered, err := yaml.Marshal(manifest)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrSetupEncodeAnalyticsConfigManifestFailed, err, fmt.Sprintf("encode analytics config manifest: %v", err))
	}
	return string(rendered), nil
}

func setupAnalyticsConfigEnvValue(key string) string {
	candidates := []string{key}
	if key == "GOOGLE_CLIENT_ID" {
		candidates = append(candidates, "MCP_GOOGLE_CLIENT_ID")
	} else if strings.HasPrefix(key, "OIDC_") {
		candidates = append(candidates, "MCP_"+key)
	}
	for _, candidate := range candidates {
		if value := strings.TrimSpace(os.Getenv(candidate)); value != "" {
			return value
		}
	}
	return ""
}

func applyPlatformTeamTraefikWatchDefault(data map[string]string) {
	if data == nil {
		return
	}
	if strings.TrimSpace(data["PLATFORM_TEAM_TRAEFIK_WATCH"]) != "" {
		return
	}
	if env := setupAnalyticsConfigEnvValue("PLATFORM_TEAM_TRAEFIK_WATCH"); env != "" {
		data["PLATFORM_TEAM_TRAEFIK_WATCH"] = env
		return
	}
	// k3s and other external Traefik installs watch ingress cluster-wide and must
	// not be patched through repo-managed traefik/traefik namespace args.
	if strings.TrimSpace(data["PLATFORM_TRAEFIK_NAMESPACE"]) == "kube-system" {
		data["PLATFORM_TEAM_TRAEFIK_WATCH"] = "disabled"
	}
}

func applyGoogleOIDCDefaults(data map[string]string) {
	clientID := strings.TrimSpace(data["GOOGLE_CLIENT_ID"])
	if clientID == "" {
		return
	}
	if strings.TrimSpace(data["OIDC_ISSUER"]) == "" {
		data["OIDC_ISSUER"] = "https://accounts.google.com"
	}
	if strings.TrimSpace(data["OIDC_AUDIENCE"]) == "" {
		data["OIDC_AUDIENCE"] = clientID
	}
	if strings.TrimSpace(data["OIDC_JWKS_URL"]) == "" {
		data["OIDC_JWKS_URL"] = "https://www.googleapis.com/oauth2/v3/certs"
	}
}

func existingConfigMapData(kubectl core.KubectlRunner, namespace, name string) (map[string]string, error) {
	cmd, err := kubectl.CommandArgs([]string{"get", "configmap", name, "-n", namespace, "-o", "json"})
	if err != nil {
		return nil, err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.ToLower(strings.TrimSpace(string(out)))
		if strings.Contains(detail, "not found") || strings.Contains(detail, "notfound") {
			return map[string]string{}, nil
		}
		return nil, core.WrapWithSentinel(core.ErrSetupReadConfigMapFailed, err, fmt.Sprintf("read configmap %s/%s: %v", namespace, name, err))
	}
	if strings.TrimSpace(string(out)) == "" {
		return map[string]string{}, nil
	}
	var payload struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, core.WrapWithSentinel(core.ErrSetupDecodeConfigMapFailed, err, fmt.Sprintf("decode configmap %s/%s: %v", namespace, name, err))
	}
	if payload.Data == nil {
		return map[string]string{}, nil
	}
	return payload.Data, nil
}

func existingConfigMapDataClientGo(namespace, name string) (map[string]string, error) {
	clients, err := platformKubernetesClients()
	if err != nil {
		return nil, err
	}
	data, err := k8sclient.ConfigMapData(context.Background(), clients, namespace, name)
	if err != nil {
		return nil, core.WrapWithSentinel(core.ErrSetupReadConfigMapFailed, err, fmt.Sprintf("read configmap %s/%s: %v", namespace, name, err))
	}
	return data, nil
}

func renderAnalyticsSecretManifest(kubectl core.KubectlRunner) (string, error) {
	return renderAnalyticsSecretManifestWithReader(func(namespace, name, key string) (string, error) {
		return existingSecretDataValue(kubectl, namespace, name, key)
	})
}

func renderAnalyticsSecretManifestClientGo() (string, error) {
	return renderCredentialBundleClientGo()
}

type analyticsSecretValueReader func(namespace, name, key string) (string, error)

func renderAnalyticsSecretManifestWithReader(readSecret analyticsSecretValueReader) (string, error) {
	apiKeys, err := existingSecretDataValueOrRandomWithReader(readSecret, core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "API_KEYS", 16)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	ingestAPIKeys, err := existingSecretDataValueOrRandomWithReader(readSecret, core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "INGEST_API_KEYS", 16)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	uiAPIKey, err := existingSecretDataValueOrRandomWithReader(readSecret, core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "UI_API_KEY", 16)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	apiKeys = ensureCSVIncludes(apiKeys, uiAPIKey)
	adminAPIKeys, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "ADMIN_API_KEYS")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	adminAPIKeys = ensureCSVIncludes(adminAPIKeys, uiAPIKey)
	grafanaPassword, err := existingSecretDataValueOrRandomWithReader(readSecret, core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "GRAFANA_ADMIN_PASSWORD", 16)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	postgresUser, err := existingSecretDataValueOrDefaultWithReader(readSecret, core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "POSTGRES_USER", "mcp_runtime")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	postgresPassword, err := existingSecretDataValueOrRandomWithReader(readSecret, core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "POSTGRES_PASSWORD", 16)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	postgresDB, err := existingSecretDataValueOrDefaultWithReader(readSecret, core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "POSTGRES_DB", "mcp_runtime")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	postgresDSN, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "POSTGRES_DSN")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	if postgresDSN == "" {
		postgresDSN = fmt.Sprintf(
			"postgres://%s@mcp-sentinel-postgres.%s.svc.cluster.local:5432/%s?sslmode=disable",
			url.UserPassword(postgresUser, postgresPassword).String(),
			core.DefaultAnalyticsNamespace,
			postgresDB,
		)
	}
	jwtSecret, err := existingSecretDataValueOrRandomWithReader(readSecret, core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "JWT_SECRET", 32)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	internalAuthToken, err := existingSecretDataValueOrRandomWithReader(readSecret, core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "INTERNAL_AUTH_TOKEN", 32)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	oauthPrivateKey, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "OAUTH_PRIVATE_KEY")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	if override := setupSecretEnvValue("MCP_OAUTH_PRIVATE_KEY", "OAUTH_PRIVATE_KEY"); override != "" {
		oauthPrivateKey = override
	}
	if strings.TrimSpace(oauthPrivateKey) == "" {
		oauthPrivateKey, err = generateOAuthPrivateKey()
		if err != nil {
			return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("generate OAuth signing key: %v", err))
		}
	}
	platformAdminEmail, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "PLATFORM_ADMIN_EMAIL")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	platformAdminPassword, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "PLATFORM_ADMIN_PASSWORD")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	adminUsers, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "ADMIN_USERS")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	envPlatformAdminEmail := setupSecretEnvValue("MCP_PLATFORM_ADMIN_EMAIL", "PLATFORM_ADMIN_EMAIL")
	envPlatformAdminPassword := setupSecretEnvValue("MCP_PLATFORM_ADMIN_PASSWORD", "PLATFORM_ADMIN_PASSWORD")
	if envPlatformAdminEmail != "" {
		platformAdminEmail = envPlatformAdminEmail
	}
	if envPlatformAdminPassword != "" {
		platformAdminPassword = envPlatformAdminPassword
	}
	if platformAdminEmail == "" || platformAdminPassword == "" {
		platformAdminEmail = ""
		platformAdminPassword = ""
	}
	adminUserCandidates := []string{setupSecretEnvValue("MCP_ADMIN_USERS", "ADMIN_USERS")}
	if envPlatformAdminEmail != "" {
		adminUserCandidates = append(adminUserCandidates, envPlatformAdminEmail)
	}
	if platformAdminEmail != "" {
		adminUserCandidates = append(adminUserCandidates, platformAdminEmail)
	}
	adminUsers = ensureCSVIncludesValues(adminUsers, adminUserCandidates...)
	platformDevLoginEnabled := ""
	platformDevUserEmail, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "PLATFORM_DEV_USER_EMAIL")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	platformDevUserPassword, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "PLATFORM_DEV_USER_PASSWORD")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	platformDevAdminEmail, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "PLATFORM_DEV_ADMIN_EMAIL")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	platformDevAdminPassword, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "PLATFORM_DEV_ADMIN_PASSWORD")
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to read analytics secrets: %v", err))
	}
	if os.Getenv("MCP_RUNTIME_TEST_MODE") == "1" {
		platformDevLoginEnabled = "true"
		if platformDevUserEmail == "" {
			platformDevUserEmail = defaultDevUserEmail
		}
		if platformDevUserPassword == "" {
			platformDevUserPassword = defaultDevUserPassword
		}
		if platformDevAdminEmail == "" {
			platformDevAdminEmail = defaultDevAdminEmail
		}
		if platformDevAdminPassword == "" {
			platformDevAdminPassword = defaultDevAdminPassword
		}
	} else {
		platformDevLoginEnabled = "false"
		platformDevUserEmail = ""
		platformDevUserPassword = ""
		platformDevAdminEmail = ""
		platformDevAdminPassword = ""
	}
	stringData := map[string]string{
		"API_KEYS":                apiKeys,
		"INGEST_API_KEYS":         ingestAPIKeys,
		"ADMIN_API_KEYS":          adminAPIKeys,
		"UI_API_KEY":              uiAPIKey,
		"ADMIN_USERS":             adminUsers,
		"PLATFORM_ADMIN_EMAIL":    platformAdminEmail,
		"PLATFORM_ADMIN_PASSWORD": platformAdminPassword,
		"POSTGRES_USER":           postgresUser,
		"POSTGRES_PASSWORD":       postgresPassword,
		"POSTGRES_DB":             postgresDB,
		"POSTGRES_DSN":            postgresDSN,
		"JWT_SECRET":              jwtSecret,
		"INTERNAL_AUTH_TOKEN":     internalAuthToken,
		"OAUTH_PRIVATE_KEY":       oauthPrivateKey,
		"GRAFANA_ADMIN_USER":      "admin",
		"GRAFANA_ADMIN_PASSWORD":  grafanaPassword,
	}
	if platformDevLoginEnabled != "" ||
		platformDevUserEmail != "" ||
		platformDevUserPassword != "" ||
		platformDevAdminEmail != "" ||
		platformDevAdminPassword != "" {
		stringData["PLATFORM_DEV_LOGIN_ENABLED"] = platformDevLoginEnabled
		stringData["PLATFORM_DEV_USER_EMAIL"] = platformDevUserEmail
		stringData["PLATFORM_DEV_USER_PASSWORD"] = platformDevUserPassword
		stringData["PLATFORM_DEV_ADMIN_EMAIL"] = platformDevAdminEmail
		stringData["PLATFORM_DEV_ADMIN_PASSWORD"] = platformDevAdminPassword
	}
	secretManifest := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]string{
			"name":      "mcp-sentinel-secrets",
			"namespace": core.DefaultAnalyticsNamespace,
		},
		"type":       "Opaque",
		"stringData": stringData,
	}
	rendered, err := yaml.Marshal(secretManifest)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrRenderSecretManifestFailed, err, fmt.Sprintf("failed to render analytics secrets: %v", err))
	}
	return string(rendered), nil
}

func generateOAuthPrivateKey() (string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

func ensureCSVIncludes(csv, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return strings.TrimSpace(csv)
	}
	parts := make([]string, 0)
	found := false
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if part == value {
			found = true
		}
		parts = append(parts, part)
	}
	if !found {
		parts = append(parts, value)
	}
	return strings.Join(parts, ",")
}

func ensureCSVIncludesValues(csv string, values ...string) string {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			csv = ensureCSVIncludes(csv, part)
		}
	}
	return csv
}

func setupSecretEnvValue(candidates ...string) string {
	for _, candidate := range candidates {
		if value := strings.TrimSpace(os.Getenv(candidate)); value != "" {
			return value
		}
	}
	return ""
}

func ensureAnalyticsImagePullSecret(kubectl core.KubectlRunner, images AnalyticsImageSet) (string, error) {
	if explicit := platformImagePullSecretOverride(); explicit != "" {
		return explicit, nil
	}
	extRegistry, err := registry.ResolveExternalRegistryConfig(nil)
	if err != nil {
		return "", err
	}
	if extRegistry == nil || extRegistry.URL == "" || (extRegistry.Username == "" && extRegistry.Password == "") {
		return ensureBundledPublicRegistryPullSecret(
			kubectl,
			core.DefaultAnalyticsNamespace,
			analyticsImagePullSecretCandidates(images),
			func(namespace, name, key string) (string, error) {
				return existingSecretDataValue(kubectl, namespace, name, key)
			},
		)
	}
	if err := ensureImagePullSecretWithKubectl(kubectl, core.DefaultAnalyticsNamespace, defaultRegistrySecretName, extRegistry.URL, extRegistry.Username, extRegistry.Password); err != nil {
		return "", err
	}
	return defaultRegistrySecretName, nil
}

func platformImagePullSecretOverride() string {
	return setupSecretEnvValue("MCP_PLATFORM_IMAGE_PULL_SECRET", "MCP_REGISTRY_PULL_SECRET_NAME")
}

func ensureAnalyticsImagePullSecretClientGo(images AnalyticsImageSet) (string, error) {
	if explicit := platformImagePullSecretOverride(); explicit != "" {
		return explicit, nil
	}
	extRegistry, err := registry.ResolveExternalRegistryConfig(nil)
	if err != nil {
		return "", err
	}
	if extRegistry == nil || extRegistry.URL == "" || (extRegistry.Username == "" && extRegistry.Password == "") {
		return ensureBundledPublicRegistryPullSecretClientGo(core.DefaultAnalyticsNamespace, analyticsImagePullSecretCandidates(images))
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return "", err
	}
	if err := k8sclient.UpsertDockerConfigSecret(context.Background(), clients, core.DefaultAnalyticsNamespace, defaultRegistrySecretName, extRegistry.URL, extRegistry.Username, extRegistry.Password); err != nil {
		return "", err
	}
	return defaultRegistrySecretName, nil
}

func ensureOperatorPublicRegistryPullSecret(kubectl core.KubectlRunner, images AnalyticsImageSet) error {
	if platformImagePullSecretOverride() != "" {
		return nil
	}
	secretName, err := ensureBundledPublicRegistryPullSecret(
		kubectl,
		core.NamespaceMCPRuntime,
		analyticsImagePullSecretCandidates(images),
		func(namespace, name, key string) (string, error) {
			return existingSecretDataValue(kubectl, namespace, name, key)
		},
	)
	if err != nil || strings.TrimSpace(secretName) == "" {
		return err
	}
	return patchDeploymentImagePullSecret(kubectl, core.NamespaceMCPRuntime, core.OperatorDeploymentName, secretName)
}

func ensureOperatorPublicRegistryPullSecretClientGo(images AnalyticsImageSet) error {
	if platformImagePullSecretOverride() != "" {
		return nil
	}
	secretName, err := ensureBundledPublicRegistryPullSecretClientGo(core.NamespaceMCPRuntime, analyticsImagePullSecretCandidates(images))
	if err != nil || strings.TrimSpace(secretName) == "" {
		return err
	}
	return patchDeploymentImagePullSecretClientGo(core.NamespaceMCPRuntime, core.OperatorDeploymentName, secretName)
}

func ensureBundledPublicRegistryPullSecret(kubectl core.KubectlRunner, namespace string, images []string, readSecret analyticsSecretValueReader) (string, error) {
	registryHost := bundledPublicRegistryPullSecretHost(images)
	if registryHost == "" {
		return "", nil
	}
	password, err := readSecret(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "UI_API_KEY")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(password) == "" {
		return "", fmt.Errorf("cannot create pull secret for public registry %q: mcp-sentinel-secrets UI_API_KEY is empty", registryHost)
	}
	if err := ensureImagePullSecretWithKubectl(kubectl, namespace, defaultRegistrySecretName, registryHost, "platform-service", password); err != nil {
		return "", err
	}
	return defaultRegistrySecretName, nil
}

func ensureBundledPublicRegistryPullSecretClientGo(namespace string, images []string) (string, error) {
	registryHost := bundledPublicRegistryPullSecretHost(images)
	if registryHost == "" {
		return "", nil
	}
	password, err := existingSecretDataValueClientGo(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "UI_API_KEY")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(password) == "" {
		return "", fmt.Errorf("cannot create pull secret for public registry %q: mcp-sentinel-secrets UI_API_KEY is empty", registryHost)
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return "", err
	}
	if err := k8sclient.UpsertDockerConfigSecret(context.Background(), clients, namespace, defaultRegistrySecretName, registryHost, "platform-service", password); err != nil {
		return "", err
	}
	return defaultRegistrySecretName, nil
}

func analyticsImagePullSecretCandidates(images AnalyticsImageSet) []string {
	return []string{
		images.Ingest,
		images.PlatformAPI,
		images.RuntimeAPI,
		images.AnalyticsAPI,
		images.Processor,
		images.UI,
	}
}

func bundledPublicRegistryPullSecretHost(images []string) string {
	candidates := []string{}
	if registryIngressHostExplicitlyConfigured() {
		candidates = append(candidates, core.GetRegistryIngressHost())
	}
	if registryEndpointExplicitlyConfiguredForPlatform() {
		candidates = append(candidates, core.GetRegistryEndpoint())
	}
	for _, candidate := range candidates {
		host := normalizeImageRegistryHost(candidate)
		if !isPublicRegistryPullHost(host) {
			continue
		}
		for _, image := range images {
			if normalizeImageRegistryHost(registryHostFromImage(image)) == host {
				return host
			}
		}
	}
	return ""
}

func normalizeImageRegistryHost(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, "/")
	if value == "" {
		return ""
	}
	if scheme := strings.Index(value, "://"); scheme >= 0 {
		value = value[scheme+3:]
	}
	if before, _, found := strings.Cut(value, "/"); found {
		value = before
	}
	return strings.TrimSpace(value)
}

func isPublicRegistryPullHost(host string) bool {
	host = normalizeImageRegistryHost(host)
	if host == "" || host == core.DefaultRegistryIngressHost || host == core.DefaultRegistryEndpoint {
		return false
	}
	if strings.EqualFold(host, "localhost") || strings.HasPrefix(host, "localhost:") {
		return false
	}
	if strings.Contains(host, ".svc") || strings.Contains(host, ".cluster.local") {
		return false
	}
	hostOnly := host
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		hostOnly = parsedHost
	}
	if net.ParseIP(hostOnly) != nil {
		return false
	}
	return strings.Contains(hostOnly, ".")
}

func patchDeploymentImagePullSecret(kubectl core.KubectlRunner, namespace, name, secretName string) error {
	patch := fmt.Sprintf(`{"spec":{"template":{"spec":{"imagePullSecrets":[{"name":%q}]}}}}`, secretName)
	cmd, err := kubectl.CommandArgs([]string{"patch", "deployment", name, "-n", namespace, "-p", patch})
	if err != nil {
		return err
	}
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	return cmd.Run()
}

func patchDeploymentImagePullSecretClientGo(namespace, name, secretName string) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deploy, err := clients.Clientset.AppsV1().Deployments(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		for _, existing := range deploy.Spec.Template.Spec.ImagePullSecrets {
			if existing.Name == secretName {
				return nil
			}
		}
		deploy.Spec.Template.Spec.ImagePullSecrets = append(deploy.Spec.Template.Spec.ImagePullSecrets, corev1.LocalObjectReference{Name: secretName})
		_, err = clients.Clientset.AppsV1().Deployments(namespace).Update(context.Background(), deploy, metav1.UpdateOptions{})
		return err
	})
}

func existingSecretDataValue(kubectl core.KubectlRunner, namespace, name, key string) (string, error) {
	cmd, err := kubectl.CommandArgs([]string{"get", "secret", name, "-n", namespace, "-o", "jsonpath={.data." + key + "}"})
	if err != nil {
		return "", err
	}

	output, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(output))
	if err != nil {
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "not found") || strings.Contains(lower, "notfound") {
			return "", nil
		}
		return "", core.WrapWithSentinel(core.ErrSetupReadSecretKeyFailed, err, fmt.Sprintf("read secret %s/%s key %s: %v", namespace, name, key, err))
	}
	if trimmed == "" {
		return "", nil
	}

	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrSetupDecodeSecretKeyFailed, err, fmt.Sprintf("decode secret %s/%s key %s: %v", namespace, name, key, err))
	}
	return string(decoded), nil
}

func existingSecretDataValueClientGo(namespace, name, key string) (string, error) {
	clients, err := platformKubernetesClients()
	if err != nil {
		return "", err
	}
	value, err := k8sclient.SecretStringDataValue(context.Background(), clients, namespace, name, key)
	if err != nil {
		return "", core.WrapWithSentinel(core.ErrSetupReadSecretKeyFailed, err, fmt.Sprintf("read secret %s/%s key %s: %v", namespace, name, key, err))
	}
	return value, nil
}

func existingSecretDataValueOrRandomWithReader(readSecret analyticsSecretValueReader, namespace, name, key string, size int) (string, error) {
	value, err := readSecret(namespace, name, key)
	if err != nil {
		return "", err
	}
	if value != "" {
		return value, nil
	}
	return randomHex(size)
}

func existingSecretDataValueOrDefaultWithReader(readSecret analyticsSecretValueReader, namespace, name, key, fallback string) (string, error) {
	value, err := readSecret(namespace, name, key)
	if err != nil {
		return "", err
	}
	if value != "" {
		return value, nil
	}
	return fallback, nil
}

func injectImagePullSecretsIntoManifest(manifest, secretName string) (string, error) {
	decoder := yaml.NewDecoder(strings.NewReader(manifest))
	var renderedDocs []string

	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if isEmptyYAMLDocument(&doc) {
			continue
		}

		injectImagePullSecretIntoDocument(&doc, secretName)
		var rendered strings.Builder
		encoder := yaml.NewEncoder(&rendered)
		encoder.SetIndent(2)
		if err := encoder.Encode(&doc); err != nil {
			_ = encoder.Close()
			return "", err
		}
		if err := encoder.Close(); err != nil {
			return "", err
		}
		renderedDocs = append(renderedDocs, strings.TrimRight(rendered.String(), "\n"))
	}

	if len(renderedDocs) == 0 {
		return manifest, nil
	}
	return strings.Join(renderedDocs, "\n---\n") + "\n", nil
}

func isEmptyYAMLDocument(doc *yaml.Node) bool {
	root := yamlDocumentRoot(doc)
	return root == nil || (root.Kind == yaml.ScalarNode && root.Tag == "!!null" && strings.TrimSpace(root.Value) == "")
}

func injectImagePullSecretIntoDocument(doc *yaml.Node, secretName string) {
	podSpec := manifestPodSpec(doc)
	if podSpec == nil {
		return
	}

	existing := mappingValueNode(podSpec, "imagePullSecrets")
	if existing != nil && existing.Kind == yaml.SequenceNode {
		if imagePullSecretSequenceContains(existing, secretName) {
			return
		}
		existing.Content = append(existing.Content, imagePullSecretNode(secretName))
		return
	}

	setMappingValueNode(podSpec, "imagePullSecrets", &yaml.Node{
		Kind:    yaml.SequenceNode,
		Tag:     "!!seq",
		Content: []*yaml.Node{imagePullSecretNode(secretName)},
	})
}

func manifestPodSpec(doc *yaml.Node) *yaml.Node {
	root := yamlDocumentRoot(doc)
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	kind := strings.ToLower(strings.TrimSpace(mappingScalarValue(root, "kind")))
	spec := mappingValueNode(root, "spec")
	if spec == nil || spec.Kind != yaml.MappingNode {
		return nil
	}

	switch kind {
	case "deployment", "statefulset", "daemonset", "job":
		template := ensureMappingChildNode(spec, "template")
		return ensureMappingChildNode(template, "spec")
	case "cronjob":
		jobTemplate := ensureMappingChildNode(spec, "jobTemplate")
		jobSpec := ensureMappingChildNode(jobTemplate, "spec")
		template := ensureMappingChildNode(jobSpec, "template")
		return ensureMappingChildNode(template, "spec")
	default:
		return nil
	}
}

func yamlDocumentRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil
		}
		return doc.Content[0]
	}
	return doc
}

func mappingValueNode(root *yaml.Node, key string) *yaml.Node {
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return root.Content[i+1]
		}
	}
	return nil
}

func mappingScalarValue(root *yaml.Node, key string) string {
	value := mappingValueNode(root, key)
	if value == nil || value.Kind != yaml.ScalarNode {
		return ""
	}
	return value.Value
}

func setMappingValueNode(root *yaml.Node, key string, value *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content[i+1] = value
			return
		}
	}
	root.Content = append(root.Content, stringNode(key), value)
}

func ensureMappingChildNode(root *yaml.Node, key string) *yaml.Node {
	if existing := mappingValueNode(root, key); existing != nil && existing.Kind == yaml.MappingNode {
		return existing
	}
	created := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setMappingValueNode(root, key, created)
	return created
}

func imagePullSecretSequenceContains(seq *yaml.Node, secretName string) bool {
	for _, item := range seq.Content {
		if item.Kind == yaml.MappingNode && strings.TrimSpace(mappingScalarValue(item, "name")) == secretName {
			return true
		}
	}
	return false
}

func imagePullSecretNode(secretName string) *yaml.Node {
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Tag:  "!!map",
		Content: []*yaml.Node{
			stringNode("name"),
			stringNode(secretName),
		},
	}
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func randomHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

// syncPostgresPasswordClientGo runs ALTER USER on the Postgres pod so that the
// database password matches whatever was just written to mcp-sentinel-secrets.
// This is needed because Kubernetes env vars snapshotted at pod start are NOT
// automatically refreshed when a Secret is updated, and the StatefulSet restart
// that would re-read POSTGRES_PASSWORD may differ from what the already-running
// database cluster uses as the auth password for the mcp_runtime user.
func syncPostgresPasswordClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	ctx := context.Background()

	// Only sync if the Postgres pod is actually running.
	podName, err := k8sclient.GetFirstReadyPodName(ctx, clients, core.DefaultAnalyticsNamespace, "app=mcp-sentinel-postgres")
	if err != nil || podName == "" {
		return nil // not running yet; fresh install will use the correct password
	}

	password, err := existingSecretDataValueClientGo(core.DefaultAnalyticsNamespace, "mcp-sentinel-secrets", "POSTGRES_PASSWORD")
	if err != nil || strings.TrimSpace(password) == "" {
		return err
	}

	// Use psql inside the pod with password auth via PGPASSWORD so the ALTER USER
	// takes effect regardless of pg_hba.conf peer-auth configuration.
	sql := fmt.Sprintf("ALTER USER mcp_runtime PASSWORD '%s';", strings.ReplaceAll(password, "'", "''"))
	kubectl := core.DefaultKubectlClient()
	cmd, err := kubectl.CommandArgs([]string{
		"exec", "-n", core.DefaultAnalyticsNamespace, podName, "--",
		"sh", "-c", fmt.Sprintf("PGPASSWORD=%s psql -h localhost -U mcp_runtime -c %q",
			shellQuote(password), sql),
	})
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ALTER USER mcp_runtime: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

var analyticsHostpathDirs = []string{
	"/var/lib/mcp-runtime/registry",
	"/var/lib/mcp-runtime/clickhouse",
	"/var/lib/mcp-runtime/kafka/0",
	"/var/lib/mcp-runtime/kafka/1",
	"/var/lib/mcp-runtime/kafka/2",
}

func ensureAnalyticsHostpathDirs(storageMode string) error {
	if storageMode != setupplan.StorageModeHostpath {
		return nil
	}
	var failures []string
	for _, dir := range analyticsHostpathDirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", dir, err))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	core.Warn(fmt.Sprintf(
		"Could not create hostpath directories on this machine (%s). Create them on the node labeled mcp-runtime.org/local-storage=true before Kafka/ClickHouse pods start.",
		strings.Join(failures, "; "),
	))
	return nil
}

func reconcileKafkaStatefulSetForKRaftUpgradeClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	namespace := core.DefaultAnalyticsNamespace
	statefulSets := clients.Clientset.AppsV1().StatefulSets(namespace)
	current, err := statefulSets.Get(context.Background(), kafkaStatefulSetName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("inspect Kafka StatefulSet: %w", err)
	}
	if !kafkaStatefulSetNeedsKRaftRecreate(current) {
		return nil
	}
	core.Warn("Legacy Kafka StatefulSet layout detected; recreating it for KRaft migration (PVCs are preserved)")
	propagation := metav1.DeletePropagationForeground
	if err := statefulSets.Delete(context.Background(), kafkaStatefulSetName, metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete legacy Kafka StatefulSet: %w", err)
	}
	return waitForStatefulSetDeletionClientGo(clients, namespace, kafkaStatefulSetName, 5*time.Minute)
}

func reconcileKafkaStatefulSetForKRaftUpgradeWithKubectl(kubectl core.KubectlRunner) error {
	namespace := core.DefaultAnalyticsNamespace
	output, err := kubectlText(kubectl, []string{
		"get", "statefulset", kafkaStatefulSetName, "-n", namespace, "-o", "json",
	})
	if err != nil {
		if isKubectlNotFound(output) {
			return nil
		}
		return fmt.Errorf("inspect Kafka StatefulSet: %s", kubeerr.CommandDetail(output, err))
	}
	if strings.TrimSpace(output) == "" {
		return nil
	}
	var current appsv1.StatefulSet
	if err := json.Unmarshal([]byte(output), &current); err != nil {
		return fmt.Errorf("decode Kafka StatefulSet: %w", err)
	}
	if !kafkaStatefulSetNeedsKRaftRecreate(&current) {
		return nil
	}
	core.Warn("Legacy Kafka StatefulSet layout detected; recreating it for KRaft migration (PVCs are preserved)")
	if err := kubectl.RunWithOutput([]string{
		"delete", "statefulset/" + kafkaStatefulSetName,
		"-n", namespace,
		"--wait=true",
		"--timeout=300s",
	}, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("delete legacy Kafka StatefulSet: %w", err)
	}
	return nil
}

func kafkaStatefulSetNeedsKRaftRecreate(sts *appsv1.StatefulSet) bool {
	if sts == nil {
		return false
	}
	if sts.Spec.ServiceName != kafkaHeadlessServiceName {
		return true
	}
	if sts.Spec.PodManagementPolicy != appsv1.ParallelPodManagement {
		return true
	}
	if sts.Spec.Replicas == nil || *sts.Spec.Replicas != kafkaKRaftReplicaCount {
		return true
	}
	if sts.Annotations == nil || sts.Annotations["mcpruntime.org/kafka-mode"] != "kraft" {
		return true
	}
	return false
}

func waitForStatefulSetDeletionClientGo(clients *k8sclient.Clients, namespace, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := clients.Clientset.AppsV1().StatefulSets(namespace).Get(context.Background(), name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for statefulset/%s deletion", name)
}

func propagationForeground() *metav1.DeletionPropagation {
	policy := metav1.DeletePropagationForeground
	return &policy
}

// deferPlatformAdmissionManifest leaves source admission untouched until the
// replacement rolls out and the old hostPath collector finishes deletion.
func deferPlatformAdmissionManifest(content string) (string, error) {
	var out bytes.Buffer
	decoder := yaml.NewDecoder(strings.NewReader(content))
	encoder := yaml.NewEncoder(&out)
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return "", err
		}
		metadata, _ := doc["metadata"].(map[string]any)
		if doc["kind"] == "Namespace" && metadata["name"] == core.DefaultAnalyticsNamespace {
			continue
		}
		if len(doc) > 0 {
			if err := encoder.Encode(doc); err != nil {
				return "", err
			}
		}
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func finishCollectorAdmissionCutoverClientGo() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	ctx := context.Background()
	// The new collector has its own binding; an interrupted rollout leaves the
	// legacy binding untouched. Refuse to remove an unrelated custom binding.
	binding, err := clients.Clientset.RbacV1().ClusterRoleBindings().Get(ctx, "promtail", metav1.GetOptions{})
	if err == nil {
		if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != "promtail" || len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "ServiceAccount" || binding.Subjects[0].Name != "promtail" || binding.Subjects[0].Namespace != core.DefaultAnalyticsNamespace {
			return fmt.Errorf("legacy promtail ClusterRoleBinding has unexpected ownership; refusing cleanup")
		}
		if err := clients.Clientset.RbacV1().ClusterRoleBindings().Delete(ctx, "promtail", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	return k8sclient.EnsureNamespace(ctx, clients, core.DefaultAnalyticsNamespace, map[string]string{
		"pod-security.kubernetes.io/enforce": "restricted", "pod-security.kubernetes.io/audit": "restricted", "pod-security.kubernetes.io/warn": "restricted",
	})
}

// Create a fresh source namespace restricted, but preserve a legacy admission
// exception while its collector may still restart during an interrupted setup.
func ensurePlatformNamespaceBeforeIngress() error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	ctx := context.Background()
	_, err = clients.Clientset.CoreV1().Namespaces().Get(ctx, core.DefaultAnalyticsNamespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return k8sclient.EnsureNamespace(ctx, clients, core.DefaultAnalyticsNamespace, map[string]string{
			"pod-security.kubernetes.io/enforce": "restricted", "pod-security.kubernetes.io/audit": "restricted", "pod-security.kubernetes.io/warn": "restricted",
		})
	}
	return err
}
