package platform

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/metadata"
	"mcp-runtime/pkg/platforminventory"
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
		// Cluster unreachable — the cluster step will surface a clearer error;
		// skip preflight so we don't double-report a connection failure.
		core.Warn("Pre-flight: cannot connect to cluster, skipping pre-flight checks")
		return nil
	}

	var issues []preflightIssue
	bg := context.Background()

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
		managedIssuerWillBeCreated := issuer == setupplan.DefaultTestMTLSClusterIssuer
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
		return core.NewWithSentinel(
			core.ErrSetupStepFailed,
			fmt.Sprintf("pre-flight checks found %d blocker(s) — resolve the issues above and re-run setup", countFatal(issues)),
		)
	}
	return nil
}

// checkCertManagerCRDs ensures cert-manager CRDs are installed when
// --skip-cert-manager-install is set. Without them every TLS step fails.
func checkCertManagerCRDs(ctx context.Context, clients *k8sclient.Clients, installCertManager bool) []preflightIssue {
	if installCertManager {
		return nil // setup will install cert-manager itself
	}
	if err := k8sclient.CheckCRDExists(ctx, clients, "certificates.cert-manager.io"); err != nil {
		return []preflightIssue{{
			fatal:   true,
			message: "cert-manager CRDs not found, but --skip-cert-manager-install is set. Install cert-manager before running setup.",
			cleanup: []string{
				"helm repo add jetstack https://charts.jetstack.io --force-update",
				"helm install cert-manager jetstack/cert-manager -n cert-manager --create-namespace --set crds.enabled=true",
				"# or omit --skip-cert-manager-install / MCP_SETUP_SKIP_CERT_MANAGER_INSTALL to let setup install it",
			},
		}}
	}
	return nil
}

// checkClusterIssuerExists verifies that a named ClusterIssuer exists when
// --tls-cluster-issuer is set. A missing issuer causes the TLS step to fail.
func checkClusterIssuerExists(ctx context.Context, clients *k8sclient.Clients, issuerName string) []preflightIssue {
	if strings.TrimSpace(issuerName) == "" {
		return nil
	}
	if err := k8sclient.CheckClusterIssuer(ctx, clients, issuerName); err != nil {
		if apierrors.IsNotFound(err) {
			return []preflightIssue{{
				fatal:   true,
				message: fmt.Sprintf("ClusterIssuer %q not found — it must exist before setup configures TLS.", issuerName),
				cleanup: []string{
					fmt.Sprintf("# Create the ClusterIssuer %q and then re-run setup.", issuerName),
					"# See: https://cert-manager.io/docs/configuration/",
				},
			}}
		}
	}
	return nil
}

// checkStaleRegistryCertificate detects a previously-failed Certificate whose
// DNS names no longer match the expected registry host. cert-manager will not
// re-issue for the new host until the old object is removed, causing a 5m
// timeout during the TLS step.
func checkStaleRegistryCertificate(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	dnsNames, err := k8sclient.GetCertificateDNSNames(ctx, clients, core.NamespaceRegistry, certmanager.RegistryCertificateName)
	if err != nil || len(dnsNames) == 0 {
		return nil
	}

	// Use metadata.ResolveRegistryHost() (reads env directly) instead of
	// core.GetRegistryIngressHost() (reads the init-time DefaultCLIConfig snapshot)
	// so tests using t.Setenv and setup runs after --env-file loading see the
	// correct value.
	expectedHost := metadata.ResolveRegistryHost()
	if expectedHost == "" || containsString(dnsNames, expectedHost) {
		return nil
	}

	return []preflightIssue{{
		fatal: true,
		message: fmt.Sprintf(
			"Stale Certificate %q in namespace %q has DNS names %v but the expected registry host is %q.\n"+
				"         cert-manager will not re-issue until this Certificate is removed.",
			certmanager.RegistryCertificateName, core.NamespaceRegistry, dnsNames, expectedHost,
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete certificate -n %s %s", core.NamespaceRegistry, certmanager.RegistryCertificateName),
			fmt.Sprintf("kubectl delete certificaterequest -n %s --all", core.NamespaceRegistry),
		},
	}}
}

// checkFailedCertificateRequests warns about failed CertificateRequests in the
// registry namespace. They don't block re-issuance on their own but can delay
// it; cleaning them up ensures cert-manager creates a fresh request immediately.
func checkFailedCertificateRequests(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	failed, _ := k8sclient.ListFailedCertificateRequestNames(ctx, clients, core.NamespaceRegistry)
	if len(failed) == 0 {
		return nil
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"%d failed CertificateRequest(s) in namespace %q (%s) — removing them ensures cert-manager creates a fresh request immediately.",
			len(failed), core.NamespaceRegistry, strings.Join(failed, ", "),
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete certificaterequest -n %s --all", core.NamespaceRegistry),
		},
	}}
}

// checkConflictingSecretOwners detects when the registry-tls Secret is claimed
// by more than one Certificate. cert-manager refuses to overwrite a Secret
// owned by a different Certificate, blocking TLS setup silently.
func checkConflictingSecretOwners(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	owners, err := k8sclient.CertificateOwnersForSecret(ctx, clients, core.NamespaceRegistry, certmanager.RegistryTLSSecretName)
	if err != nil || len(owners) <= 1 {
		return nil
	}
	return []preflightIssue{{
		fatal: true,
		message: fmt.Sprintf(
			"Secret %q in namespace %q is claimed by multiple Certificates %v — cert-manager cannot update it.",
			certmanager.RegistryTLSSecretName, core.NamespaceRegistry, owners,
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete certificate -n %s --all", core.NamespaceRegistry),
			fmt.Sprintf("kubectl delete secret -n %s %s", core.NamespaceRegistry, certmanager.RegistryTLSSecretName),
		},
	}}
}

// checkStuckOperatorDeployment warns when the operator Deployment from a
// previous setup run is stuck in ImagePullBackOff or CrashLoopBackOff. Setup
// will overwrite the image, but a stuck Pod may delay the new rollout.
func checkStuckOperatorDeployment(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	deploy, err := k8sclient.GetDeployment(ctx, clients, core.NamespaceMCPRuntime, core.OperatorDeploymentName)
	if err != nil || deploy == nil {
		return nil
	}
	pods, err := clients.Clientset.CoreV1().Pods(core.NamespaceMCPRuntime).List(ctx, metav1.ListOptions{LabelSelector: "control-plane=controller-manager"})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}
	var stuckPods []string
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			waiting := cs.State.Waiting
			if waiting == nil {
				continue
			}
			if waiting.Reason == "ImagePullBackOff" || waiting.Reason == "CrashLoopBackOff" || waiting.Reason == "ErrImagePull" {
				stuckPods = append(stuckPods, fmt.Sprintf("%s (%s: %s)", pod.Name, cs.Name, waiting.Reason))
			}
		}
	}
	if len(stuckPods) == 0 {
		return nil
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Operator pod(s) from a previous setup run are stuck: %s.\n"+
				"         Setup will update the image, but cleaning up the stuck pod(s) first speeds up the rollout.",
			strings.Join(stuckPods, "; "),
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete pod -n %s -l control-plane=controller-manager", core.NamespaceMCPRuntime),
		},
	}}
}

// checkTerminatingNamespaces detects namespaces stuck in Terminating phase.
// kubectl apply into a Terminating namespace hangs or fails with cryptic errors.
func checkTerminatingNamespaces(ctx context.Context, clients *k8sclient.Clients, includeAnalytics bool) []preflightIssue {
	namespaces := []string{core.NamespaceMCPRuntime, core.NamespaceRegistry}
	if includeAnalytics {
		namespaces = append(namespaces,
			core.ComponentNamespace("platform-api"),
			core.ComponentNamespace("analytics-api"),
			core.ComponentNamespace("promtail"),
		)
	}
	var stuck []string
	for _, ns := range namespaces {
		if terminating, err := k8sclient.IsNamespaceTerminating(ctx, clients, ns); err == nil && terminating {
			stuck = append(stuck, ns)
		}
	}
	if len(stuck) == 0 {
		return nil
	}
	var fixes []string
	for _, ns := range stuck {
		fixes = append(fixes, fmt.Sprintf("kubectl patch ns %s -p '{\"metadata\":{\"finalizers\":null}}' --type=merge", ns))
	}
	return []preflightIssue{{
		fatal:   true,
		message: fmt.Sprintf("Namespace(s) %v are stuck in Terminating — kubectl apply will hang until they are freed.", stuck),
		cleanup: fixes,
	}}
}

// checkTerminatingCRD detects when the mcpservers CRD is stuck deleting.
// Applying a new CRD version over a Terminating one silently fails.
func checkTerminatingCRD(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	const crdName = "mcpservers.mcpruntime.org"
	if terminating, err := k8sclient.IsCRDTerminating(ctx, clients, crdName); err == nil && terminating {
		return []preflightIssue{{
			fatal:   true,
			message: fmt.Sprintf("CRD %q is stuck in Terminating — remove its finalizers before re-running setup.", crdName),
			cleanup: []string{
				fmt.Sprintf("kubectl patch crd %s -p '{\"metadata\":{\"finalizers\":null}}' --type=merge", crdName),
			},
		}}
	}
	return nil
}

// checkRegistryHostForTLS verifies that the registry hostname is a real public
// name. When MCP_PLATFORM_DOMAIN is unset and no explicit ingress host vars are
// configured, GetRegistryIngressHost returns "registry.local" which Let's
// Encrypt (and any real CA) will refuse to issue a certificate for.
func checkRegistryHostForTLS() []preflightIssue {
	// Read directly from env so this check isn't affected by the DefaultCLIConfig
	// init-time snapshot (same reason we reload DefaultCLIConfig after loadEnvFile).
	host := metadata.ResolveRegistryHost()
	if host != "" && !isDevRegistryURL(host) {
		return nil
	}
	displayed := host
	if displayed == "" {
		displayed = "(empty)"
	}
	return []preflightIssue{{
		fatal: true,
		message: fmt.Sprintf(
			"Registry host is %q — a real public hostname is required for TLS certificate issuance.\n"+
				"         Set MCP_PLATFORM_DOMAIN (e.g. mcpruntime.org) or MCP_REGISTRY_INGRESS_HOST in your env file.",
			displayed,
		),
		cleanup: []string{
			"# In your env file:",
			"export MCP_PLATFORM_DOMAIN=your-domain.example.com",
			"# or",
			"export MCP_REGISTRY_INGRESS_HOST=registry.your-domain.example.com",
		},
	}}
}

// checkOrphanedRegistryTLSSecret warns when the registry-tls Secret exists but
// no cert-manager Certificate claims it. cert-manager will not overwrite an
// unowned Secret, so TLS setup silently skips re-issuance, leaving a stale cert.
func checkOrphanedRegistryTLSSecret(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	exists, err := k8sclient.SecretExists(ctx, clients, core.NamespaceRegistry, certmanager.RegistryTLSSecretName)
	if err != nil || !exists {
		return nil
	}
	owners, err := k8sclient.CertificateOwnersForSecret(ctx, clients, core.NamespaceRegistry, certmanager.RegistryTLSSecretName)
	if err != nil || len(owners) > 0 {
		return nil // has owner(s) — handled by checkConflictingSecretOwners if >1
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Secret %q in namespace %q exists but is not owned by any cert-manager Certificate.\n"+
				"         cert-manager will not update it — delete the secret so setup can issue a fresh one.",
			certmanager.RegistryTLSSecretName, core.NamespaceRegistry,
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete secret -n %s %s", core.NamespaceRegistry, certmanager.RegistryTLSSecretName),
		},
	}}
}

// checkStaleAnalyticsJob warns when a failed clickhouse-init Job is present.
// Setup calls deleteJobIfExists before re-applying, but surfacing it early lets
// the operator know why a previous analytics deploy may have failed.
func checkStaleAnalyticsJob(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	const jobName = "clickhouse-init"
	failed, err := k8sclient.IsJobFailed(ctx, clients, core.ComponentNamespace("clickhouse"), jobName)
	if err != nil || !failed {
		return nil
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Job %q in namespace %q is in a failed state from a previous run — setup will delete and re-run it.",
			jobName, core.ComponentNamespace("clickhouse"),
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete job -n %s %s", core.ComponentNamespace("clickhouse"), jobName),
		},
	}}
}

// checkStalePullSecrets detects when the mcp-runtime-registry-pull Secret in
// any namespace has a password that no longer matches the current UI_API_KEY.
// This causes ImagePullBackOff on new pods after a setup rerun rotates the key.
func checkStalePullSecrets(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	uiNamespace, uiSecret, ok := platforminventory.CredentialPlacement("UI_API_KEY")
	if !ok {
		return nil
	}
	currentKey, err := k8sclient.SecretStringDataValue(ctx, clients, uiNamespace, uiSecret, "UI_API_KEY")
	if err != nil || strings.TrimSpace(currentKey) == "" {
		return nil // secret not yet created; fresh install — nothing to check
	}

	namespaces := []string{core.ComponentNamespace("platform-api"), core.NamespaceMCPRuntime}
	var stale []string
	for _, ns := range namespaces {
		exists, err := k8sclient.SecretExists(ctx, clients, ns, defaultRegistrySecretName)
		if err != nil || !exists {
			continue
		}
		storedAuth, err := k8sclient.SecretStringDataValue(ctx, clients, ns, defaultRegistrySecretName, ".dockerconfigjson")
		if err != nil {
			continue
		}
		// The pull secret password appears as base64(user:password) in the auth field.
		// A quick check: does the raw JSON contain the current key?
		if !strings.Contains(storedAuth, currentKey) {
			stale = append(stale, ns)
		}
	}
	if len(stale) == 0 {
		return nil
	}

	var fixes []string
	for _, ns := range stale {
		fixes = append(fixes,
			fmt.Sprintf("kubectl create secret docker-registry %s -n %s --docker-server=%s --docker-username=platform-service --docker-password=<UI_API_KEY> --dry-run=client -o yaml | kubectl apply -f -",
				defaultRegistrySecretName, ns, core.GetRegistryIngressHost()),
		)
	}
	fixes = append(fixes, "# or re-run setup — it refreshes pull secrets automatically")
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Image pull secret %q in namespace(s) %v has a stale password — pods may get ImagePullBackOff after secret rotation.",
			defaultRegistrySecretName, stale,
		),
		cleanup: fixes,
	}}
}

// checkPostgresPasswordSync detects when the Postgres pod is running but the
// POSTGRES_DSN password in mcp-postgres-credentials no longer authenticates. This
// happens when setup rotated the secret on a rerun but didn't sync the live DB.
func checkPostgresPasswordSync(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	podName, err := k8sclient.GetFirstReadyPodName(ctx, clients, core.ComponentNamespace("postgres"), "app=mcp-postgres")
	if err != nil || podName == "" {
		return nil // Postgres not running yet
	}

	dsnNamespace, dsnSecret, ok := platforminventory.CredentialPlacement("POSTGRES_DSN")
	if !ok {
		return nil
	}
	dsn, err := k8sclient.SecretStringDataValue(ctx, clients, dsnNamespace, dsnSecret, "POSTGRES_DSN")
	if err != nil || strings.TrimSpace(dsn) == "" {
		return nil // secret not yet created
	}

	// Probe by exec-ing a quick psql connection test inside the pod.
	// Use PGPASSWORD + -h localhost to bypass peer auth and actually test the password.
	kubectl := core.DefaultKubectlClient()
	cmd, err := kubectl.CommandArgs([]string{
		"exec", "-n", core.ComponentNamespace("postgres"), podName, "--",
		"sh", "-c", fmt.Sprintf("PGPASSWORD=%s psql -h localhost -U mcp_runtime -c '\\l' -t -q 2>&1", shellQuote(extractPostgresPassword(dsn))),
	})
	if err != nil {
		return nil
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil // connected successfully — passwords are in sync
	}
	if !strings.Contains(string(out), "password authentication failed") {
		return nil // some other error; don't block setup
	}

	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Postgres pod %q is running but the password in mcp-postgres-credentials does not authenticate.\n"+
				"         Setup will fix this automatically by running ALTER USER on the Postgres pod.",
			podName,
		),
		cleanup: []string{
			"# Setup will run this automatically; or run it manually:",
			fmt.Sprintf("kubectl exec -n %s %s -- sh -c 'PGPASSWORD=<new_pass> psql -h localhost -U mcp_runtime -c \"ALTER USER mcp_runtime PASSWORD '\\''<new_pass>'\\''\"'",
				core.ComponentNamespace("postgres"), podName),
		},
	}}
}

// extractPostgresPassword parses a postgres://user:pass@host/db DSN and returns the password.
func extractPostgresPassword(dsn string) string {
	// Fast path: find the user:pass@ segment.
	// postgres://mcp_runtime:PASSWORD@host/db
	after, ok := strings.CutPrefix(dsn, "postgres://")
	if !ok {
		return ""
	}
	atIdx := strings.LastIndex(after, "@")
	if atIdx < 0 {
		return ""
	}
	userPass := after[:atIdx]
	colonIdx := strings.Index(userPass, ":")
	if colonIdx < 0 {
		return ""
	}
	return userPass[colonIdx+1:]
}

// checkStuckRegistryPVC detects when the registry-storage PVC is stuck in
// Pending. A Pending PVC prevents the registry pod from scheduling, so the
// rollout-wait in verifySetup times out after 5+ minutes.
func checkStuckRegistryPVC(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	pvc, err := clients.Clientset.CoreV1().PersistentVolumeClaims(core.NamespaceRegistry).Get(
		ctx, "registry-storage", metav1.GetOptions{})
	if err != nil {
		return nil // PVC doesn't exist yet — fresh install
	}
	if pvc.Status.Phase != "Pending" {
		return nil
	}
	return []preflightIssue{{
		fatal: true,
		message: fmt.Sprintf(
			"PersistentVolumeClaim %s/registry-storage is stuck in Pending — storage provisioning may have failed. The registry pod cannot schedule until this is resolved.",
			core.NamespaceRegistry,
		),
		cleanup: []string{
			fmt.Sprintf("kubectl describe pvc registry-storage -n %s  # inspect the error", core.NamespaceRegistry),
			fmt.Sprintf("kubectl delete pvc registry-storage -n %s    # delete to force re-creation", core.NamespaceRegistry),
		},
	}}
}

// checkStuckRegistryDeployment warns when the registry Deployment from a
// previous run has pods stuck in ImagePullBackOff or CrashLoopBackOff.
// Setup will update the image, but stuck pods slow the rollout.
func checkStuckRegistryDeployment(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	deploy, err := k8sclient.GetDeployment(ctx, clients, core.NamespaceRegistry, core.RegistryServiceName)
	if err != nil || deploy == nil {
		return nil
	}
	pods, err := clients.Clientset.CoreV1().Pods(core.NamespaceRegistry).List(
		ctx, metav1.ListOptions{LabelSelector: "app=registry"})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}
	var stuck []string
	for _, pod := range pods.Items {
		for _, cs := range pod.Status.ContainerStatuses {
			w := cs.State.Waiting
			if w == nil {
				continue
			}
			if w.Reason == "ImagePullBackOff" || w.Reason == "CrashLoopBackOff" || w.Reason == "ErrImagePull" {
				stuck = append(stuck, fmt.Sprintf("%s (%s)", pod.Name, w.Reason))
			}
		}
	}
	if len(stuck) == 0 {
		return nil
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Registry pod(s) from a previous run are stuck: %s — setup will update the image, but cleaning up first speeds up the rollout.",
			strings.Join(stuck, "; "),
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete pod -n %s -l app=registry", core.NamespaceRegistry),
		},
	}}
}

// checkStaleRegistryIngress warns when the registry Ingress exists with a
// host that does not match the expected registry hostname. This causes external
// TLS validation to fail and the platform API to use the wrong pull URL.
func checkStaleRegistryIngress(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	expectedHost := metadata.ResolveRegistryHost()
	if expectedHost == "" {
		return nil
	}
	ing, err := clients.Clientset.NetworkingV1().Ingresses(core.NamespaceRegistry).Get(
		ctx, core.RegistryServiceName, metav1.GetOptions{})
	if err != nil {
		return nil // ingress not created yet — fresh install
	}
	for _, rule := range ing.Spec.Rules {
		if rule.Host == expectedHost {
			return nil
		}
	}
	var current string
	if len(ing.Spec.Rules) > 0 {
		current = ing.Spec.Rules[0].Host
	}
	return []preflightIssue{{
		fatal: false,
		message: fmt.Sprintf(
			"Registry Ingress %q has host %q but the expected registry host is %q — setup will overwrite it.",
			core.RegistryServiceName, current, expectedHost,
		),
		cleanup: []string{
			fmt.Sprintf("kubectl delete ingress %s -n %s  # setup will recreate with correct host", core.RegistryServiceName, core.NamespaceRegistry),
		},
	}}
}

// checkStuckAnalyticsStatefulSets warns when analytics StatefulSet pods are
// stuck in CrashLoopBackOff, Error, or Terminating state. Stuck pods slow the
// rollout-wait that setup runs after applying manifests.
func checkStuckAnalyticsStatefulSets(ctx context.Context, clients *k8sclient.Clients) []preflightIssue {
	type stsSpec struct {
		name     string
		selector string
	}
	targets := []stsSpec{
		{"clickhouse", "app=clickhouse"},
		{"mcp-postgres", "app=mcp-postgres"},
		{"kafka", "app=kafka"},
		{"tempo", "app=tempo"},
		{"loki", "app=loki"},
	}
	var issues []preflightIssue
	for _, t := range targets {
		namespace := workloadNamespace(t.name)
		pods, err := clients.Clientset.CoreV1().Pods(namespace).List(
			ctx, metav1.ListOptions{LabelSelector: t.selector})
		if err != nil || len(pods.Items) == 0 {
			continue
		}
		var stuck []string
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp != nil {
				stuck = append(stuck, fmt.Sprintf("%s (Terminating)", pod.Name))
				continue
			}
			for _, cs := range pod.Status.ContainerStatuses {
				w := cs.State.Waiting
				if w == nil {
					continue
				}
				if w.Reason == "CrashLoopBackOff" || w.Reason == "Error" {
					stuck = append(stuck, fmt.Sprintf("%s (%s)", pod.Name, w.Reason))
				}
			}
		}
		if len(stuck) == 0 {
			continue
		}
		issues = append(issues, preflightIssue{
			fatal: false,
			message: fmt.Sprintf(
				"Analytics StatefulSet %q has stuck pod(s): %s — this may cause the rollout wait to time out.",
				t.name, strings.Join(stuck, "; "),
			),
			cleanup: []string{
				fmt.Sprintf("kubectl delete pod -n %s -l %s --grace-period=0 --force",
					namespace, t.selector),
			},
		})
	}
	return issues
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
