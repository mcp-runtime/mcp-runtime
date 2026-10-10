package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sort"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/platforminventory"
	"mcp-runtime/pkg/registryauth"
)

const nativeSignerSecret = "mcp-registry-token-signer" // #nosec G101 -- Kubernetes Secret name, not credential material.
const nativeRootSecret = "registry-token-root"
const nativePullSecret = "mcp-runtime-registry-pull" // #nosec G101 -- Kubernetes Secret name, not credential material.
const nativeManagedLabel = "mcpruntime.org/registry-auth"

type nativeAuthOptions struct {
	Realm            string
	DryRun, TestMode bool
	Namespaces       []string
	// AllowBundledBroker accepts a platform-api image served by the registry
	// it protects. Pulls of that image then depend on a running broker; see
	// docs/internals/registry-auth.md for the cold-start recovery procedure.
	AllowBundledBroker bool
}

// SetupNativeAuthOptions configures activation performed by setup.
type SetupNativeAuthOptions struct {
	// Realm is the public HTTPS token URL ending /api/v1/registry/token.
	Realm string
	// APIBaseURL is the public platform API origin used for credential issuance.
	APIBaseURL string
	// APIKey is a platform administrator service key read from the cluster.
	APIKey string
}

// NativeAuthActive reports whether the bundled registry already enforces
// Distribution token authentication.
func NativeAuthActive(ctx context.Context, cs kubernetes.Interface) (bool, error) {
	deployment, err := cs.AppsV1().Deployments(core.NamespaceRegistry).Get(ctx, core.RegistryDeploymentName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	return registryDeploymentUsesTokenAuth(deployment.Spec.Template.Spec), nil
}

func registryDeploymentUsesTokenAuth(spec corev1.PodSpec) bool {
	for _, container := range spec.Containers {
		for _, env := range container.Env {
			if env.Name == "REGISTRY_AUTH" && env.Value == "token" {
				return true
			}
		}
	}
	return false
}

// EnableNativeAuthForSetup activates native registry authentication as the
// final setup step. It uses a cluster-held administrator key instead of a CLI
// login and accepts a bundled broker image, because setup always publishes
// platform-api to the bundled registry. An already active registry is left
// unchanged; rotate node credentials with registry enable-auth.
func EnableNativeAuthForSetup(ctx context.Context, opts SetupNativeAuthOptions) error {
	if strings.TrimSpace(opts.APIKey) == "" {
		return fmt.Errorf("native registry authentication requires a platform administrator service key")
	}
	clients, err := newRegistryKubernetesClients()
	if err != nil {
		return err
	}
	active, err := NativeAuthActive(ctx, clients.Clientset)
	if err != nil {
		return err
	}
	if active {
		core.Info("Native registry authentication is already enabled")
		return nil
	}
	if err := validateNativeRealm(opts.Realm, false); err != nil {
		return err
	}
	api, err := platformapi.NewPlatformClientWithAPIKey(opts.APIBaseURL, opts.APIKey)
	if err != nil {
		return err
	}
	if err := api.CheckRegistryAdmin(ctx); err != nil {
		return err
	}
	return configureNativeAuth(ctx, clients, api, nativeAuthOptions{Realm: opts.Realm, AllowBundledBroker: true})
}

func validateNativeRealm(raw string, testMode bool) error {
	realm, err := url.Parse(raw)
	if err != nil || realm.Host == "" || realm.User != nil || realm.RawQuery != "" || realm.Fragment != "" || realm.Path != "/api/v1/registry/token" || (realm.Scheme != "https" && !(testMode && realm.Scheme == "http")) {
		return fmt.Errorf("realm must be an HTTPS URL ending /api/v1/registry/token (HTTP requires --test-mode)")
	}
	return nil
}

type registryCredentialCreator interface {
	CheckRegistryAdmin(context.Context) error
	CreateRegistryPullCredential(context.Context, registryauth.PullScope) (platformapi.RegistryPullCredential, error)
}

func (m *RegistryManager) enableNativeAuth(ctx context.Context, opts nativeAuthOptions) error {
	if err := validateNativeRealm(opts.Realm, opts.TestMode); err != nil {
		return err
	}
	clients, err := newRegistryKubernetesClients()
	if err != nil {
		return err
	}
	api, err := platformapi.NewPlatformClient()
	if err != nil {
		return err
	}
	if err := api.CheckRegistryAdmin(ctx); err != nil {
		return err
	}
	return configureNativeAuth(ctx, clients, api, opts)
}

func configureNativeAuth(ctx context.Context, clients *k8sclient.Clients, api registryCredentialCreator, opts nativeAuthOptions) error {
	cs := clients.Clientset
	registryDeploy, err := cs.AppsV1().Deployments(core.NamespaceRegistry).Get(ctx, core.RegistryDeploymentName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	apiDeploy, err := cs.AppsV1().Deployments(platforminventory.PlatformNamespace).Get(ctx, "mcp-platform-api", metav1.GetOptions{})
	if err != nil {
		return err
	}
	service, err := cs.CoreV1().Services(core.NamespaceRegistry).Get(ctx, core.RegistryServiceName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	hosts := []string{fmt.Sprintf("registry.registry.svc.%s:5000", nativeClusterDomain()), "registry.registry.svc:5000", "registry.registry:5000"}
	if service.Spec.ClusterIP != "" {
		hosts = append(hosts, service.Spec.ClusterIP+":5000")
	}
	ingress, err := cs.NetworkingV1().Ingresses(core.NamespaceRegistry).Get(ctx, core.RegistryServiceName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	for _, rule := range ingress.Spec.Rules {
		if rule.Host != "" {
			hosts = append(hosts, rule.Host)
		}
	}
	// A broker served by the registry it authenticates can only be re-pulled
	// while a broker replica is running. Require an explicit acknowledgement.
	if bundledBrokerImage(apiDeploy.Spec.Template.Spec, hosts) {
		if !opts.AllowBundledBroker {
			return fmt.Errorf("platform-api must use an external/public bootstrap image before enabling registry authentication; pass --allow-bundled-broker to accept a broker image served by this registry")
		}
		core.Warn("platform-api is served by the registry it authenticates; new platform-api pulls require a running broker replica (see registry authentication recovery docs)")
	}
	namespaces, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, name := range opts.Namespaces {
		selected[name] = true
	}
	var targets []string
	for _, ns := range namespaces.Items {
		managed := strings.HasPrefix(ns.Name, "mcp-team-") || strings.HasPrefix(ns.Name, "mcp-servers") || ns.Name == platforminventory.PlatformNamespace || ns.Name == platforminventory.OperatorNamespace || ns.Name == platforminventory.ObservabilityNamespace || ns.Name == platforminventory.LogCollectorNamespace
		if managed && (len(selected) == 0 || selected[ns.Name]) {
			targets = append(targets, ns.Name)
			delete(selected, ns.Name)
		}
	}
	if len(selected) > 0 {
		return fmt.Errorf("requested pull namespace does not exist or is not Runtime-managed")
	}
	active := registryDeploymentUsesTokenAuth(registryDeploy.Spec.Template.Spec)
	if !active && len(opts.Namespaces) != 0 {
		return fmt.Errorf("initial activation must provision all managed namespaces; --pull-namespace is for rotation after activation")
	}
	rotation, err := planRegistryAdminRotation(ctx, cs, targets)
	if err != nil {
		return err
	}
	if opts.DryRun {
		core.Info(fmt.Sprintf("Would install registry token signing material, scope pull credentials in %d namespaces, verify the broker, then enable native registry authentication and remove NodePort", len(targets)))
		return nil
	}
	key, cert, err := ensureRegistrySigningMaterial(ctx, cs)
	if err != nil {
		return err
	}
	if _, err := registryauth.NewSigner(key, cert); err != nil {
		return err
	}
	if err := mountNativeSecret(&apiDeploy.Spec.Template.Spec, nativeSignerSecret, "registry-token-signer", "/registry-token-signer", "platform-api"); err != nil {
		return err
	}
	for i := range apiDeploy.Spec.Template.Spec.Containers {
		if apiDeploy.Spec.Template.Spec.Containers[i].Name == "platform-api" || len(apiDeploy.Spec.Template.Spec.Containers) == 1 {
			setNativeEnv(&apiDeploy.Spec.Template.Spec.Containers[i], "REGISTRY_TOKEN_REALM", opts.Realm)
			setNativeEnv(&apiDeploy.Spec.Template.Spec.Containers[i], "REGISTRY_TOKEN_SIGNING_KEY_FILE", "/registry-token-signer/signing.key")
			setNativeEnv(&apiDeploy.Spec.Template.Spec.Containers[i], "REGISTRY_TOKEN_SIGNING_CERT_FILE", "/registry-token-signer/signing.crt")
		}
	}
	if _, err := cs.AppsV1().Deployments(apiDeploy.Namespace).Update(ctx, apiDeploy, metav1.UpdateOptions{FieldManager: "mcp-runtime-registry-auth"}); err != nil {
		return err
	}
	if err := k8sclient.WaitForDeploymentRolledOut(ctx, clients, apiDeploy.Namespace, apiDeploy.Name, 5*time.Minute); err != nil {
		return fmt.Errorf("registry broker rollout failed; backend authentication has not been changed: %w", err)
	}
	// Verify the public token realm before modifying the backend or issuing keys.
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, opts.Realm+"?service="+registryauth.Service, nil)
	if err != nil {
		cancel()
		return err
	}
	httpClient := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := httpClient.Do(request)
	cancel()
	if err != nil {
		return fmt.Errorf("registry token realm is unreachable; backend authentication unchanged")
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("registry token realm preflight returned HTTP %d; backend authentication unchanged", response.StatusCode)
	}
	if len(rotation.Replacement) > 0 {
		encoded, err := json.Marshal(rotation.Replacement)
		if err != nil {
			return err
		}
		journal := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mcp-registry-admin-rotation", Namespace: "mcp-platform", Labels: map[string]string{nativeManagedLabel: "rotation"}}, Data: map[string][]byte{"replacement.json": encoded}}
		if _, err = cs.CoreV1().Secrets(journal.Namespace).Create(ctx, journal, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	for _, namespace := range targets {
		repos, err := registryNamespaceRepositories(ctx, cs, namespace, hosts)
		if err != nil {
			return err
		}
		credential, err := api.CreateRegistryPullCredential(ctx, registryauth.PullScope{Namespace: namespace, Repositories: append(repos, "mcp-gateway")})
		if err != nil {
			return err
		}
		if err := installRegistryPullCredential(ctx, cs, namespace, hosts, credential, rotation.Replacement); err != nil {
			return fmt.Errorf("install %s pull credential: %w", namespace, err)
		}
	}
	if err := enableRuntimeNativePulls(ctx, clients); err != nil {
		return err
	}
	if err := mountNativeSecret(&registryDeploy.Spec.Template.Spec, nativeRootSecret, "registry-token-root", "/registry-token-root", "registry"); err != nil {
		return err
	}
	for i := range registryDeploy.Spec.Template.Spec.Containers {
		container := &registryDeploy.Spec.Template.Spec.Containers[i]
		if container.Name == "registry" {
			for name, value := range map[string]string{"REGISTRY_AUTH": "token", "REGISTRY_AUTH_TOKEN_REALM": opts.Realm, "REGISTRY_AUTH_TOKEN_SERVICE": registryauth.Service, "REGISTRY_AUTH_TOKEN_ISSUER": registryauth.Issuer, "REGISTRY_AUTH_TOKEN_ROOTCERTBUNDLE": "/registry-token-root/signing.crt"} {
				setNativeEnv(container, name, value)
			}
		}
	}
	if _, err := cs.AppsV1().Deployments(registryDeploy.Namespace).Update(ctx, registryDeploy, metav1.UpdateOptions{FieldManager: "mcp-runtime-registry-auth"}); err != nil {
		return err
	}
	if err := k8sclient.WaitForDeploymentRolledOut(ctx, clients, registryDeploy.Namespace, registryDeploy.Name, 5*time.Minute); err != nil {
		return fmt.Errorf("native registry auth rollout failed; authentication has not been disabled: %w", err)
	}
	service.Spec.Type = corev1.ServiceTypeClusterIP
	for i := range service.Spec.Ports {
		service.Spec.Ports[i].NodePort = 0
	}
	if _, err := cs.CoreV1().Services(service.Namespace).Update(ctx, service, metav1.UpdateOptions{FieldManager: "mcp-runtime-registry-auth"}); err != nil {
		return err
	}
	if _, err := cs.NetworkingV1().Ingresses(ingress.Namespace).Update(ctx, ingress, metav1.UpdateOptions{FieldManager: "mcp-runtime-registry-auth"}); err != nil {
		return err
	}
	if err := applyRegistryAdminRotation(ctx, clients, rotation); err != nil {
		return err
	}
	publisher := platformapi.RegistryPullCredential{ID: "registry-publisher", Username: "platform-service", Password: rotation.PublisherKey}
	if err := installRegistryPublisherCredential(ctx, cs, hosts, publisher); err != nil {
		return err
	}
	if err := cs.CoreV1().Secrets("mcp-platform").Delete(ctx, "mcp-registry-admin-rotation", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	core.Info("Native registry authentication enabled; namespace pull credentials expire after 90 days. Rotate them with registry enable-auth before expiry.")
	return nil
}

func bundledBrokerImage(spec corev1.PodSpec, hosts []string) bool {
	for _, container := range spec.Containers {
		host, _, _ := strings.Cut(container.Image, "/")
		for _, registryHost := range hosts {
			if host == registryHost {
				return true
			}
		}
	}
	return false
}

func setNativeEnv(container *corev1.Container, name, value string) {
	for i := range container.Env {
		if container.Env[i].Name == name {
			container.Env[i] = corev1.EnvVar{Name: name, Value: value}
			return
		}
	}
	container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: value})
}
func mountNativeSecret(spec *corev1.PodSpec, secret, name, path, containerName string) error {
	if spec.SecurityContext == nil {
		spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	if spec.SecurityContext.FSGroup == nil {
		group := int64(1000)
		spec.SecurityContext.FSGroup = &group
	}
	found := false
	for _, volume := range spec.Volumes {
		if volume.Name == name {
			if volume.Secret == nil || volume.Secret.SecretName != secret {
				return fmt.Errorf("conflicting registry authentication volume")
			}
			found = true
		}
	}
	if !found {
		spec.Volumes = append(spec.Volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: secret, DefaultMode: nativeSecretMode()}}})
	}
	matched := false
	for i := range spec.Containers {
		if spec.Containers[i].Name != containerName && len(spec.Containers) != 1 {
			continue
		}
		matched = true
		found = false
		for _, mount := range spec.Containers[i].VolumeMounts {
			if mount.Name == name {
				if mount.MountPath != path || !mount.ReadOnly {
					return fmt.Errorf("conflicting registry authentication mount")
				}
				found = true
			}
		}
		if !found {
			spec.Containers[i].VolumeMounts = append(spec.Containers[i].VolumeMounts, corev1.VolumeMount{Name: name, MountPath: path, ReadOnly: true})
		}
	}
	if !matched {
		return fmt.Errorf("registry authentication container not found")
	}
	return nil
}
func nativeSecretMode() *int32 { mode := int32(0440); return &mode }
func ensureRegistrySigningMaterial(ctx context.Context, cs kubernetes.Interface) ([]byte, []byte, error) {
	secret, err := cs.CoreV1().Secrets(platforminventory.PlatformNamespace).Get(ctx, nativeSignerSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		key, cert, err := registryauth.GenerateMaterial()
		if err != nil {
			return nil, nil, err
		}
		secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: nativeSignerSecret, Namespace: platforminventory.PlatformNamespace, Labels: map[string]string{nativeManagedLabel: "signer"}}, Data: map[string][]byte{"signing.key": key, "signing.crt": cert}}
		secret, err = cs.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, err
	}
	if secret.Labels[nativeManagedLabel] != "signer" {
		return nil, nil, fmt.Errorf("refusing to adopt unmanaged registry signing Secret")
	}
	key, cert := secret.Data["signing.key"], secret.Data["signing.crt"]
	if _, err := registryauth.NewSigner(key, cert); err != nil {
		return nil, nil, err
	}
	root, err := cs.CoreV1().Secrets(core.NamespaceRegistry).Get(ctx, nativeRootSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		root = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: nativeRootSecret, Namespace: core.NamespaceRegistry, Labels: map[string]string{nativeManagedLabel: "trust"}}, Data: map[string][]byte{"signing.crt": cert}}
		_, err = cs.CoreV1().Secrets(root.Namespace).Create(ctx, root, metav1.CreateOptions{})
		if err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, err
	} else if root.Labels[nativeManagedLabel] != "trust" || string(root.Data["signing.crt"]) != string(cert) {
		return nil, nil, fmt.Errorf("registry signing trust mismatch; refusing implicit root rotation")
	}
	return key, cert, nil
}

func registryNamespaceRepositories(ctx context.Context, cs kubernetes.Interface, namespace string, hosts []string) ([]string, error) {
	repositories := map[string]bool{}
	collect := func(spec corev1.PodSpec) {
		for _, container := range append(spec.InitContainers, spec.Containers...) {
			image := strings.SplitN(container.Image, "@", 2)[0]
			host, repo, ok := strings.Cut(image, "/")
			if !ok {
				continue
			}
			for _, allowed := range hosts {
				if host == allowed {
					if i := strings.LastIndex(repo, ":"); i >= 0 {
						repo = repo[:i]
					}
					repositories[repo] = true
				}
			}
		}
	}
	deployments, err := cs.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, workload := range deployments.Items {
		collect(workload.Spec.Template.Spec)
	}
	states, err := cs.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, workload := range states.Items {
		collect(workload.Spec.Template.Spec)
	}
	daemons, err := cs.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, workload := range daemons.Items {
		collect(workload.Spec.Template.Spec)
	}
	var result []string
	for repo := range repositories {
		result = append(result, repo)
	}
	sort.Strings(result)
	return result, nil
}

func installRegistryPullCredential(ctx context.Context, cs kubernetes.Interface, namespace string, hosts []string, credential platformapi.RegistryPullCredential, legacyKeys map[string]string) error {
	auths := map[string]any{}
	for _, host := range hosts {
		auths[host] = map[string]string{"username": credential.Username, "password": credential.Password, "auth": base64.StdEncoding.EncodeToString([]byte(credential.Username + ":" + credential.Password))}
	}
	data, err := json.Marshal(map[string]any{"auths": auths})
	if err != nil {
		return err
	}
	secret, err := cs.CoreV1().Secrets(namespace).Get(ctx, nativePullSecret, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: nativePullSecret, Namespace: namespace, Labels: map[string]string{nativeManagedLabel: "pull"}}, Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: data}}
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations["mcpruntime.org/registry-credential-id"] = credential.ID
		secret.Annotations["mcpruntime.org/registry-credential-expires"] = credential.ExpiresAt.Format(time.RFC3339)
		_, err = cs.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if secret.Labels[nativeManagedLabel] != "pull" {
			legacy := false
			for _, password := range dockerCredentialPasswords(secret.Data[corev1.DockerConfigJsonKey]) {
				if _, ok := legacyKeys[password]; ok {
					legacy = true
				}
			}
			if !legacy {
				return fmt.Errorf("refusing to replace unmanaged registry pull Secret")
			}
			if secret.Labels == nil {
				secret.Labels = map[string]string{}
			}
			secret.Labels[nativeManagedLabel] = "pull"
		}
		secret.Data = map[string][]byte{corev1.DockerConfigJsonKey: data}
		if secret.Annotations == nil {
			secret.Annotations = map[string]string{}
		}
		secret.Annotations["mcpruntime.org/registry-credential-id"] = credential.ID
		secret.Annotations["mcpruntime.org/registry-credential-expires"] = credential.ExpiresAt.Format(time.RFC3339)
		if _, err = cs.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	accounts, err := cs.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, account := range accounts.Items {
		appendPullSecret(&account.ImagePullSecrets)
		if _, err := cs.CoreV1().ServiceAccounts(namespace).Update(ctx, &account, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	deployments, err := cs.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, workload := range deployments.Items {
		appendPullSecret(&workload.Spec.Template.Spec.ImagePullSecrets)
		if _, err := cs.AppsV1().Deployments(namespace).Update(ctx, &workload, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	states, err := cs.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, workload := range states.Items {
		appendPullSecret(&workload.Spec.Template.Spec.ImagePullSecrets)
		if _, err := cs.AppsV1().StatefulSets(namespace).Update(ctx, &workload, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	daemons, err := cs.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, workload := range daemons.Items {
		appendPullSecret(&workload.Spec.Template.Spec.ImagePullSecrets)
		if _, err := cs.AppsV1().DaemonSets(namespace).Update(ctx, &workload, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return nil
}
func appendPullSecret(refs *[]corev1.LocalObjectReference) {
	for _, ref := range *refs {
		if ref.Name == nativePullSecret {
			return
		}
	}
	*refs = append(*refs, corev1.LocalObjectReference{Name: nativePullSecret})
}

func nativeClusterDomain() string {
	domain := strings.Trim(strings.TrimSpace(os.Getenv("MCP_CLUSTER_DOMAIN")), ".")
	if domain == "" {
		return "cluster.local"
	}
	return domain
}

func installRegistryPublisherCredential(ctx context.Context, cs kubernetes.Interface, hosts []string, credential platformapi.RegistryPullCredential) error {
	auths := map[string]any{}
	for _, host := range hosts {
		auths[host] = map[string]string{"username": credential.Username, "password": credential.Password, "auth": base64.StdEncoding.EncodeToString([]byte(credential.Username + ":" + credential.Password))}
	}
	data, err := json.Marshal(map[string]any{"auths": auths})
	if err != nil {
		return err
	}
	name := "mcp-registry-publisher"
	secret, err := cs.CoreV1().Secrets(core.NamespaceRegistry).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: core.NamespaceRegistry, Labels: map[string]string{nativeManagedLabel: "publisher"}}, Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: data}}
		_, err = cs.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if secret.Labels[nativeManagedLabel] != "publisher" {
		return fmt.Errorf("refusing unmanaged registry publisher Secret")
	}
	secret.Data = map[string][]byte{corev1.DockerConfigJsonKey: data}
	_, err = cs.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
	return err
}

func enableRuntimeNativePulls(ctx context.Context, clients *k8sclient.Clients) error {
	workload, err := clients.Clientset.AppsV1().Deployments(platforminventory.PlatformNamespace).Get(ctx, "mcp-runtime-api", metav1.GetOptions{})
	if err != nil {
		return err
	}
	for i := range workload.Spec.Template.Spec.Containers {
		setNativeEnv(&workload.Spec.Template.Spec.Containers[i], "MCP_REGISTRY_NATIVE_AUTH", "true")
	}
	if _, err := clients.Clientset.AppsV1().Deployments(workload.Namespace).Update(ctx, workload, metav1.UpdateOptions{FieldManager: "mcp-runtime-registry-auth"}); err != nil {
		return err
	}
	return k8sclient.WaitForDeploymentRolledOut(ctx, clients, workload.Namespace, workload.Name, 5*time.Minute)
}
