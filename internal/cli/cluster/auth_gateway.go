package cluster

import (
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/internal/platformrelease"
	"mcp-runtime/pkg/authgateway"
)

const authDeploymentName = "mcp-auth-server"

type gatewayOptions struct {
	file, tls, kubeconfig, kubeContext, clusterID, mcpHost, image string
	files                                                         []string
	yes, dryRun                                                   bool
	waitTimeout                                                   time.Duration
}

func (o *gatewayOptions) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.file, "file", "", "Gateway JSON file; file references must use "+authgateway.MountPath+"/<key>")
	cmd.Flags().StringArrayVar(&o.files, "secret-file", nil, "Mount a private KEY=FILE input (the session binding key and the resource-client key)")
	cmd.Flags().StringVar(&o.tls, "tls-secret", "", "Existing TLS Secret covering the auth hostname; no certificate is issued")
	cmd.Flags().StringVar(&o.image, "gateway-image", "", "mcp-gateway image to run in credential mode; defaults to the installed gateway's image, then the operator's MCP_GATEWAY_PROXY_IMAGE")
	cmd.Flags().StringVar(&o.mcpHost, "mcp-host", "", "Public MCP host for servers that set no ingressHost")
	cmd.Flags().StringVar(&o.kubeconfig, "kubeconfig", "", "Explicit kubeconfig path")
	cmd.Flags().StringVar(&o.kubeContext, "context", "", "Explicit Kubernetes context")
	cmd.Flags().StringVar(&o.clusterID, "expect-cluster-id", "", "Expected kube-system namespace UID")
	cmd.Flags().DurationVar(&o.waitTimeout, "wait-timeout", 3*time.Minute, "How long to wait for the gateway to become ready before wiring mcp-auth to it (0 skips the wait)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Validate inputs and show the plan without writing")
	cmd.Flags().BoolVar(&o.yes, "yes", false, "Apply the reviewed configuration without another prompt")
}

func (o *gatewayOptions) validate() error {
	if o.file == "" || o.tls == "" || o.kubeconfig == "" || o.kubeContext == "" || o.clusterID == "" {
		return errors.New("--file, --tls-secret, --kubeconfig, --context and --expect-cluster-id are required")
	}
	if !o.dryRun && !o.yes {
		return errors.New("pass --yes after reviewing --dry-run")
	}
	return nil
}

func newMCPAuthGatewayCmd() *cobra.Command {
	root := &cobra.Command{Use: "mcp-auth-gateway", Short: "Install the optional credential gateway and wire MCP Auth to it"}
	var opts gatewayOptions
	apply := &cobra.Command{Use: "apply", Short: "Install the credential gateway for the servers that declare auth.credentialGateway", Long: `Reads the base gateway JSON (listener, issuer, session key; services optional),
adds one service for every MCPServer with auth.mode header and an
auth.credentialGateway block, and installs the shared credential gateway in
mcp-platform (Secret, Deployment, Service, Ingress, NetworkPolicy). It runs the
existing mcp-gateway image in credential mode. After the gateway is ready, it
wires MCP Auth to it: a connector and a resource login per service, the
gateway's assertion client, and the allowed resources.

The headers a server declares in auth.headers become the sign-in inputs, so
pushing the server's .mcp/servers.yaml and running apply configures the gateway
and MCP Auth. Each derived service reads its resource-client key from client.pem,
supplied with --secret-file client.pem=<file>. Header values people submit live
only in the gateway's memory, so restarting MCP Auth does not clear them;
restarting the gateway does.`, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := opts.validate(); err != nil {
			return err
		}
		base, err := os.ReadFile(opts.file) // #nosec G304 -- explicit operator-supplied configuration.
		if err != nil {
			return errors.New("gateway configuration file is unreadable")
		}
		return runAuthGateway(cmd, &opts, base)
	}}
	opts.bind(apply)
	root.AddCommand(apply)
	return root
}

// runAuthGateway reads the cluster, extends the base configuration with the
// services its servers declare, validates the result and installs it.
func runAuthGateway(cmd *cobra.Command, o *gatewayOptions, data []byte) error {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = o.kubeconfig
	connect := func() (kubernetes.Interface, *rest.Config, error) {
		restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: o.kubeContext}).ClientConfig()
		if err != nil {
			return nil, nil, errors.New("selected Kubernetes context is unavailable")
		}
		client, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return nil, nil, err
		}
		ns, err := client.CoreV1().Namespaces().Get(cmd.Context(), "kube-system", metav1.GetOptions{})
		if err != nil {
			return nil, nil, err
		}
		if string(ns.UID) != o.clusterID {
			return nil, nil, errors.New("selected cluster ID does not match --expect-cluster-id")
		}
		return client, restConfig, nil
	}
	client, restConfig, err := connect()
	if err != nil {
		return err
	}
	if data, err = deriveGatewayServices(cmd.Context(), o, restConfig, data); err != nil {
		return err
	}
	cfg, err := authgateway.Parse(data)
	if err != nil {
		return err
	}
	payload := map[string][]byte{"config.json": data}
	for _, entry := range o.files {
		key, path, ok := strings.Cut(entry, "=")
		if !ok || len(validation.IsConfigMapKey(key)) > 0 || key == "config.json" || payload[key] != nil {
			return errors.New("--secret-file must be a unique KEY=FILE with a valid Secret key")
		}
		b, err := os.ReadFile(path) // #nosec G304 -- explicit operator-supplied secret file; never printed.
		if err != nil || len(b) == 0 {
			return errors.New("a supplied secret file is unreadable or empty")
		}
		payload[key] = b
	}
	if err := validateGatewayFiles(data, payload); err != nil {
		return err
	}
	publicKeys, err := resourceClientPublicKeys(cfg, payload)
	if err != nil {
		return err
	}
	wiring, err := authgateway.BuildWiring(cfg, publicKeys)
	if err != nil {
		return err
	}
	report, err := installGateway(cmd.Context(), client, gatewayInstall{cfg: cfg, payload: payload, wiring: wiring, tlsSecret: o.tls, image: o.image, dryRun: o.dryRun, waitTimeout: o.waitTimeout})
	if err != nil {
		return err
	}
	action := "installed"
	if o.dryRun {
		action = "would install"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s the credential gateway in %s: %d service(s), image %s; private values are omitted\n", action, mcpAuthNamespace, len(cfg.Services), report.image)
	for _, line := range report.notes {
		fmt.Fprintln(cmd.OutOrStdout(), "  "+line)
	}
	return nil
}

// resourceClientPublicKeys derives each service's public key from the private
// key the administrator supplied, so mcp-auth can verify its assertion requests.
func resourceClientPublicKeys(cfg *authgateway.Config, payload map[string][]byte) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range cfg.Services {
		name := strings.TrimPrefix(s.ResourceClient.PrivateKeyFile, authgateway.MountPath+"/")
		block, _ := pem.Decode(payload[name])
		if block == nil {
			return nil, fmt.Errorf("service %q: %s is not a PEM private key", s.ID, name)
		}
		var key any
		if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			key = parsed
		} else if parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
			key = parsed
		} else {
			return nil, fmt.Errorf("service %q: %s is not a usable RSA private key", s.ID, name)
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("service %q: %s has no public key", s.ID, name)
		}
		der, err := x509.MarshalPKIXPublicKey(signer.Public())
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", s.ID, err)
		}
		out[s.ID] = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
	return out, nil
}

var mcpServerGVR = schema.GroupVersionResource{Group: "mcpruntime.org", Version: "v1alpha1", Resource: "mcpservers"}

// deriveGatewayServices appends a form-source service for each header-mode
// MCPServer that declares auth.credentialGateway.
func deriveGatewayServices(ctx context.Context, o *gatewayOptions, restConfig *rest.Config, base []byte) ([]byte, error) {
	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}
	list, err := dyn.Resource(mcpServerGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, errors.New("could not list MCPServers in the selected cluster")
	}
	var servers []mcpv1alpha1.MCPServer
	for _, item := range list.Items {
		var server mcpv1alpha1.MCPServer
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &server); err != nil {
			return nil, fmt.Errorf("MCPServer %s/%s could not be read", item.GetNamespace(), item.GetName())
		}
		servers = append(servers, server)
	}
	return extendGatewayConfig(base, servers, o.mcpHost)
}

// extendGatewayConfig adds the derived services to the base configuration and
// keeps every other key of it as written.
func extendGatewayConfig(base []byte, servers []mcpv1alpha1.MCPServer, publicHost string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(base, &doc); err != nil {
		return nil, errors.New("gateway configuration must contain one JSON object")
	}
	authServer, _ := doc["authorization_server"].(map[string]any)
	issuer, _ := authServer["issuer"].(string)
	issuerURL, err := url.Parse(issuer)
	if err != nil || issuerURL.Host == "" {
		return nil, errors.New("gateway configuration needs authorization_server.issuer")
	}
	sort.Slice(servers, func(i, j int) bool {
		if servers[i].Namespace != servers[j].Namespace {
			return servers[i].Namespace < servers[j].Namespace
		}
		return servers[i].Name < servers[j].Name
	})
	services, _ := doc["services"].([]any)
	seen := map[string]string{}
	for _, server := range servers {
		auth := server.Spec.Auth
		if !mcpv1alpha1.AuthUsesHeaderMode(auth) || auth.CredentialGateway == nil {
			continue
		}
		service, err := authgateway.ServiceFor(authgateway.ServerInput{
			Name: server.Name, Namespace: server.Namespace, IngressHost: server.Spec.IngressHost, IngressPath: server.Spec.IngressPath,
			Headers: auth.Headers, ValidationPath: auth.CredentialGateway.ValidationPath, SecretHeaders: auth.CredentialGateway.SecretHeaders,
		}, issuerURL.Host, publicHost)
		if err != nil {
			return nil, err
		}
		if other, dup := seen[service.ID]; dup {
			return nil, fmt.Errorf("servers %s and %s/%s share the gateway service id %q", other, server.Namespace, server.Name, service.ID)
		}
		seen[service.ID] = server.Namespace + "/" + server.Name
		encoded, _ := json.Marshal(service)
		var entry any
		_ = json.Unmarshal(encoded, &entry)
		services = append(services, entry)
	}
	doc["services"] = services
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func validateGatewayFiles(raw []byte, payload map[string][]byte) error {
	total := 0
	for _, b := range payload {
		total += len(b)
	}
	if total > 900<<10 {
		return errors.New("gateway Secret payload exceeds 900 KiB")
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return errors.New("invalid gateway configuration")
	}
	var walk func(any) error
	walk = func(v any) error {
		switch v := v.(type) {
		case map[string]any:
			for key, item := range v {
				if strings.HasSuffix(key, "_file") {
					path, ok := item.(string)
					if !ok || !strings.HasPrefix(path, authgateway.MountPath+"/") {
						return errors.New("gateway file references must use " + authgateway.MountPath + "/<key>")
					}
					name := strings.TrimPrefix(path, authgateway.MountPath+"/")
					if payload[name] == nil {
						return errors.New("gateway configuration references an unprovided mounted file")
					}
				}
				if err := walk(item); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range v {
				if err := walk(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

type gatewayInstall struct {
	cfg         *authgateway.Config
	payload     map[string][]byte
	wiring      *authgateway.Wiring
	tlsSecret   string
	image       string
	dryRun      bool
	waitTimeout time.Duration
}

type installReport struct {
	image string
	notes []string
}

func ownedByRuntime(m metav1.ObjectMeta) bool {
	return m.Labels["app.kubernetes.io/managed-by"] == "mcp-runtime"
}

func gatewayMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: mcpAuthNamespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime", "app": authgateway.GatewayName}}
}

func plainEnv(envs []corev1.EnvVar) map[string]string {
	out := map[string]string{}
	for _, e := range envs {
		if e.ValueFrom == nil {
			out[e.Name] = e.Value
		}
	}
	return out
}

// installGateway runs the preflight checks, then installs the gateway workload,
// waits for it, and only then wires mcp-auth to it, so a sign-in is never sent
// to a gateway that is not up.
func installGateway(ctx context.Context, c kubernetes.Interface, in gatewayInstall) (*installReport, error) {
	auth, err := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authDeploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if len(auth.Spec.Template.Spec.Containers) != 1 {
		return nil, errors.New("bundled auth requires exactly one container")
	}
	env := plainEnv(auth.Spec.Template.Spec.Containers[0].Env)
	if env["MCP_AUTH_ISSUER"] != in.cfg.AuthorizationServer.Issuer {
		return nil, errors.New("gateway issuer does not match the installed auth deployment")
	}
	if env["MCP_AUTH_CONNECTOR"] == "" || env["MCP_AUTH_LOGIN_METHODS_FILE"] != "" {
		return nil, errors.New("the installed auth uses login methods mode; add the gateway's resource logins to its login methods file instead")
	}
	for name, want := range map[string]string{"MCP_AUTH_EXTERNAL_CREDENTIALS_FILE": authgateway.ExternalCredentialsFile, "MCP_AUTH_RESOURCE_LOGINS_FILE": authgateway.ResourceLoginsFile} {
		if v := env[name]; v != "" && v != want {
			return nil, fmt.Errorf("the installed auth already sets %s to another file; merge the gateway's entries into it by hand", name)
		}
	}
	issuerURL, err := url.Parse(in.cfg.AuthorizationServer.Issuer)
	if err != nil {
		return nil, err
	}
	existingIngress, err := c.NetworkingV1().Ingresses(mcpAuthNamespace).Get(ctx, authDeploymentName, metav1.GetOptions{})
	if err != nil {
		return nil, errors.New("existing bundled auth ingress is unavailable")
	}
	covered := false
	for _, tls := range existingIngress.Spec.TLS {
		if tls.SecretName != in.tlsSecret {
			continue
		}
		for _, host := range tls.Hosts {
			covered = covered || host == issuerURL.Hostname()
		}
	}
	if !covered {
		return nil, errors.New("--tls-secret must reuse the installed auth ingress certificate for this host")
	}
	connectors, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, mcpAuthConnectorName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read the MCP Auth connectors ConfigMap: %w", err)
	}
	mergedConnectors, err := authgateway.MergeConnectors(connectors.Data["connectors.json"], in.wiring.Connectors)
	if err != nil {
		return nil, err
	}
	existing, err := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authgateway.GatewayName, metav1.GetOptions{})
	switch {
	case err == nil:
		if !ownedByRuntime(existing.ObjectMeta) {
			return nil, errors.New("a gateway Deployment exists without Runtime ownership")
		}
		if in.image == "" && len(existing.Spec.Template.Spec.Containers) == 1 {
			in.image = existing.Spec.Template.Spec.Containers[0].Image
		}
	case !apierrors.IsNotFound(err):
		return nil, err
	}
	if in.image == "" {
		in.image = operatorGatewayImage(ctx, c)
	}
	if in.image == "" {
		return nil, errors.New("--gateway-image is required: the operator does not declare MCP_GATEWAY_PROXY_IMAGE")
	}
	report := &installReport{image: in.image}
	if in.dryRun {
		report.notes = append(report.notes,
			"would create or update the gateway Secret, Deployment, Service, NetworkPolicy and Ingress",
			"would wait for the gateway to become ready, then update the MCP Auth wiring and restart it if it changed",
			"would remove the objects of the earlier combined layout if present")
		return report, nil
	}

	secretRevision, err := upsertGatewaySecret(ctx, c, in.payload)
	if err != nil {
		return nil, err
	}
	if err := upsertGatewayDeployment(ctx, c, in, auth.Spec.Template.Spec.ImagePullSecrets, secretRevision); err != nil {
		return nil, err
	}
	if err := upsertGatewayNetwork(ctx, c, in); err != nil {
		return nil, err
	}
	report.notes = append(report.notes, "gateway Secret, Deployment, Service, NetworkPolicy and Ingress are in place")
	if in.waitTimeout > 0 {
		if err := waitForGateway(ctx, c, in.waitTimeout); err != nil {
			return nil, fmt.Errorf("the gateway did not become ready; MCP Auth was not changed (if its pod cannot pull the image from the platform registry, refresh the namespace's pull credential with `mcp-runtime registry enable-auth --pull-namespace %s` and run this again): %w", mcpAuthNamespace, err)
		}
		report.notes = append(report.notes, "the gateway is ready")
	}

	hash, err := applyAuthWiring(ctx, c, in, connectors, mergedConnectors)
	if err != nil {
		return nil, err
	}
	report.notes = append(report.notes, "MCP Auth is wired to the gateway (wiring "+hash[:12]+")")
	return report, nil
}

func upsertGatewaySecret(ctx context.Context, c kubernetes.Interface, payload map[string][]byte) (string, error) {
	desired := &corev1.Secret{ObjectMeta: gatewayMeta(authgateway.GatewayName), Type: corev1.SecretTypeOpaque, Data: payload}
	old, err := c.CoreV1().Secrets(mcpAuthNamespace).Get(ctx, desired.Name, metav1.GetOptions{})
	switch {
	case err == nil:
		if !ownedByRuntime(old.ObjectMeta) {
			return "", errors.New("gateway Secret exists without Runtime ownership")
		}
		if equalSecretData(old.Data, payload) {
			return old.ResourceVersion, nil
		}
		old.Data = payload
		updated, err := c.CoreV1().Secrets(mcpAuthNamespace).Update(ctx, old, metav1.UpdateOptions{})
		if err != nil {
			return "", err
		}
		return updated.ResourceVersion, nil
	case apierrors.IsNotFound(err):
		created, err := c.CoreV1().Secrets(mcpAuthNamespace).Create(ctx, desired, metav1.CreateOptions{})
		if err != nil {
			return "", err
		}
		return created.ResourceVersion, nil
	default:
		return "", err
	}
}

func equalSecretData(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if string(b[k]) != string(v) {
			return false
		}
	}
	return true
}

// operatorGatewayImage is the mcp-gateway image the operator injects into
// per-server sidecars. The shared credential gateway runs the same image.
func operatorGatewayImage(ctx context.Context, c kubernetes.Interface) string {
	d, err := c.AppsV1().Deployments("mcp-runtime").Get(ctx, platformrelease.OperatorDeployment, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	for _, container := range d.Spec.Template.Spec.Containers {
		for _, e := range container.Env {
			if e.Name == "MCP_GATEWAY_PROXY_IMAGE" {
				return e.Value
			}
		}
	}
	return ""
}

func upsertGatewayDeployment(ctx context.Context, c kubernetes.Interface, in gatewayInstall, pull []corev1.LocalObjectReference, secretRevision string) error {
	labels := map[string]string{"app": authgateway.GatewayName}
	port := in.cfg.Listen.Port
	runAs, fsGroup := int64(65532), int64(65532)
	yes, no := true, false
	replicas := int32(1)
	desired := &appsv1.Deployment{ObjectMeta: gatewayMeta(authgateway.GatewayName), Spec: appsv1.DeploymentSpec{
		Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: labels},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: map[string]string{"mcpruntime.org/gateway-secret-version": secretRevision}},
			Spec: corev1.PodSpec{
				ImagePullSecrets: pull,
				SecurityContext:  &corev1.PodSecurityContext{RunAsNonRoot: &yes, RunAsUser: &runAs, RunAsGroup: &runAs, FSGroup: &fsGroup, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
				Containers: []corev1.Container{{
					Name: "gateway", Image: in.image, ImagePullPolicy: corev1.PullIfNotPresent,
					Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: port}},
					Env:             []corev1.EnvVar{{Name: "MCP_GATEWAY_MODE", Value: "credential"}, {Name: "MCP_GATEWAY_CONFIG", Value: authgateway.MountPath + "/config.json"}},
					SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &no, ReadOnlyRootFilesystem: &yes, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
					VolumeMounts:    []corev1.VolumeMount{{Name: "config", MountPath: authgateway.MountPath, ReadOnly: true}},
					ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(port)}}, PeriodSeconds: 10},
					LivenessProbe:   &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(port)}}, InitialDelaySeconds: 5, PeriodSeconds: 20},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
						Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
					},
				}},
				Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: authgateway.GatewayName, DefaultMode: mode0440()}}}},
			},
		},
	}}
	old, err := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, desired.Name, metav1.GetOptions{})
	switch {
	case err == nil:
		if !ownedByRuntime(old.ObjectMeta) {
			return errors.New("a gateway Deployment exists without Runtime ownership")
		}
		desired.ResourceVersion = old.ResourceVersion
		_, err = c.AppsV1().Deployments(mcpAuthNamespace).Update(ctx, desired, metav1.UpdateOptions{})
		return err
	case apierrors.IsNotFound(err):
		_, err = c.AppsV1().Deployments(mcpAuthNamespace).Create(ctx, desired, metav1.CreateOptions{})
		return err
	default:
		return err
	}
}

func upsertGatewayNetwork(ctx context.Context, c kubernetes.Interface, in gatewayInstall) error {
	port := in.cfg.Listen.Port
	selector := metav1.LabelSelector{MatchLabels: map[string]string{"app": authgateway.GatewayName}}
	policy := &networkingv1.NetworkPolicy{ObjectMeta: gatewayMeta(authgateway.GatewayName), Spec: networkingv1.NetworkPolicySpec{
		PodSelector: selector, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{
				{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}},
				{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "mcp-auth-server"}}},
			},
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: protocolTCP(), Port: portPointer(port)}},
		}},
	}}
	if old, err := c.NetworkingV1().NetworkPolicies(mcpAuthNamespace).Get(ctx, policy.Name, metav1.GetOptions{}); err == nil {
		if !ownedByRuntime(old.ObjectMeta) {
			return errors.New("a gateway NetworkPolicy exists without Runtime ownership")
		}
		policy.ResourceVersion = old.ResourceVersion
		if _, err = c.NetworkingV1().NetworkPolicies(mcpAuthNamespace).Update(ctx, policy, metav1.UpdateOptions{}); err != nil {
			return err
		}
	} else if apierrors.IsNotFound(err) {
		if _, err = c.NetworkingV1().NetworkPolicies(mcpAuthNamespace).Create(ctx, policy, metav1.CreateOptions{}); err != nil {
			return err
		}
	} else {
		return err
	}

	svc := &corev1.Service{ObjectMeta: gatewayMeta(authgateway.GatewayName), Spec: corev1.ServiceSpec{
		Type: corev1.ServiceTypeClusterIP, Selector: selector.MatchLabels,
		Ports: []corev1.ServicePort{{Name: "http", Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP}},
	}}
	if old, err := c.CoreV1().Services(mcpAuthNamespace).Get(ctx, svc.Name, metav1.GetOptions{}); err == nil {
		if !ownedByRuntime(old.ObjectMeta) {
			return errors.New("a gateway Service exists without Runtime ownership")
		}
		old.Spec.Ports, old.Spec.Selector = svc.Spec.Ports, svc.Spec.Selector
		if _, err = c.CoreV1().Services(mcpAuthNamespace).Update(ctx, old, metav1.UpdateOptions{}); err != nil {
			return err
		}
	} else if apierrors.IsNotFound(err) {
		if _, err = c.CoreV1().Services(mcpAuthNamespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
			return err
		}
	} else {
		return err
	}
	return applyGatewayIngress(ctx, c, in.cfg, in.tlsSecret)
}

// applyGatewayIngress routes each service's path, its sign-in sub-paths and its
// protected-resource metadata on the auth host to the gateway.
func applyGatewayIngress(ctx context.Context, c kubernetes.Interface, cfg *authgateway.Config, tlsSecret string) error {
	class := "traefik"
	prefix := networkingv1.PathTypePrefix
	backend := networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: authgateway.GatewayName, Port: networkingv1.ServiceBackendPort{Number: cfg.Listen.Port}}}
	var paths []networkingv1.HTTPIngressPath
	for _, s := range cfg.Services {
		u, _ := url.Parse(s.ResourceURL)
		paths = append(paths,
			networkingv1.HTTPIngressPath{Path: u.Path, PathType: &prefix, Backend: backend},
			networkingv1.HTTPIngressPath{Path: "/.well-known/oauth-protected-resource" + u.Path, PathType: &prefix, Backend: backend})
	}
	issuer, _ := url.Parse(cfg.AuthorizationServer.Issuer)
	meta := gatewayMeta(authgateway.GatewayName)
	meta.Annotations = map[string]string{"traefik.ingress.kubernetes.io/router.entrypoints": "websecure", "traefik.ingress.kubernetes.io/router.tls": "true"}
	ingress := &networkingv1.Ingress{ObjectMeta: meta, Spec: networkingv1.IngressSpec{
		IngressClassName: &class,
		TLS:              []networkingv1.IngressTLS{{Hosts: []string{issuer.Hostname()}, SecretName: tlsSecret}},
		Rules:            []networkingv1.IngressRule{{Host: issuer.Hostname(), IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths}}}},
	}}
	if old, err := c.NetworkingV1().Ingresses(mcpAuthNamespace).Get(ctx, ingress.Name, metav1.GetOptions{}); err == nil {
		if !ownedByRuntime(old.ObjectMeta) {
			return errors.New("a gateway Ingress exists without Runtime ownership")
		}
		ingress.ResourceVersion = old.ResourceVersion
		_, err = c.NetworkingV1().Ingresses(mcpAuthNamespace).Update(ctx, ingress, metav1.UpdateOptions{})
		return err
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	_, err := c.NetworkingV1().Ingresses(mcpAuthNamespace).Create(ctx, ingress, metav1.CreateOptions{})
	return err
}

func waitForGateway(ctx context.Context, c kubernetes.Interface, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		d, err := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authgateway.GatewayName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return d.Status.ObservedGeneration >= d.Generation && d.Status.UpdatedReplicas >= 1 && d.Status.ReadyReplicas >= 1 && d.Status.UnavailableReplicas == 0, nil
	})
}

// applyAuthWiring tells mcp-auth about the gateway: it merges the connector
// entries, publishes the wiring ConfigMap, and patches the Deployment (volume,
// environment, allowed resources) so it restarts when any of that changed. The
// returned hash covers only public data: URLs, ids and public keys.
func applyAuthWiring(ctx context.Context, c kubernetes.Interface, in gatewayInstall, connectors *corev1.ConfigMap, merged string) (string, error) {
	sum := sha256.New()
	names := make([]string, 0, len(in.wiring.Files))
	for name := range in.wiring.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sum.Write([]byte(name + "\x00" + in.wiring.Files[name] + "\x00"))
	}
	sum.Write([]byte(merged))
	hash := hex.EncodeToString(sum.Sum(nil))

	cm := &corev1.ConfigMap{ObjectMeta: gatewayMeta(authgateway.WiringName), Data: in.wiring.Files}
	if old, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, cm.Name, metav1.GetOptions{}); err == nil {
		if !ownedByRuntime(old.ObjectMeta) {
			return "", errors.New("the gateway wiring ConfigMap exists without Runtime ownership")
		}
		old.Data = in.wiring.Files
		if _, err = c.CoreV1().ConfigMaps(mcpAuthNamespace).Update(ctx, old, metav1.UpdateOptions{}); err != nil {
			return "", err
		}
	} else if apierrors.IsNotFound(err) {
		if _, err = c.CoreV1().ConfigMaps(mcpAuthNamespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return "", err
		}
	} else {
		return "", err
	}
	if connectors.Data["connectors.json"] != merged {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current, err := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, mcpAuthConnectorName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			current.Data["connectors.json"] = merged
			_, err = c.CoreV1().ConfigMaps(mcpAuthNamespace).Update(ctx, current, metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			return "", err
		}
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authDeploymentName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		issuer := plainEnv(current.Spec.Template.Spec.Containers[0].Env)["MCP_AUTH_ISSUER"]
		prior, err := authgateway.RegisteredResources(current.Annotations[authgateway.ResourceAnnotation], issuer)
		if err != nil {
			return err
		}
		registered := in.cfg.Resources()
		encoded, _ := json.Marshal(registered)
		current.Annotations[authgateway.ResourceAnnotation] = string(encoded)

		envs := current.Spec.Template.Spec.Containers[0].Env
		set := func(name, value string) {
			for i := range envs {
				if envs[i].Name == name {
					envs[i].Value, envs[i].ValueFrom = value, nil
					return
				}
			}
			envs = append(envs, corev1.EnvVar{Name: name, Value: value})
		}
		resources, scopes := map[string]bool{}, map[string][]string{}
		for _, v := range envs {
			if v.Name == "MCP_AUTH_RESOURCES" {
				for _, r := range strings.Split(v.Value, ",") {
					if r != "" {
						resources[r] = true
					}
				}
			}
			if v.Name == "MCP_AUTH_RESOURCE_SCOPES" && v.Value != "" {
				if json.Unmarshal([]byte(v.Value), &scopes) != nil {
					return errors.New("installed resource scopes are invalid")
				}
			}
		}
		for r := range prior {
			delete(resources, r)
			delete(scopes, r)
		}
		for r, s := range registered {
			resources[r], scopes[r] = true, s
		}
		list := make([]string, 0, len(resources))
		for r := range resources {
			list = append(list, r)
		}
		sort.Strings(list)
		set("MCP_AUTH_RESOURCES", strings.Join(list, ","))
		encoded, _ = json.Marshal(scopes)
		set("MCP_AUTH_RESOURCE_SCOPES", string(encoded))
		set("MCP_AUTH_EXTERNAL_CREDENTIALS_FILE", authgateway.ExternalCredentialsFile)
		set("MCP_AUTH_RESOURCE_LOGINS_FILE", authgateway.ResourceLoginsFile)
		current.Spec.Template.Spec.Containers[0].Env = envs

		volume := corev1.Volume{Name: "gateway-wiring", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: authgateway.WiringName}, DefaultMode: mode0444()}}}
		volumes := current.Spec.Template.Spec.Volumes[:0]
		found := false
		for _, v := range current.Spec.Template.Spec.Volumes {
			if v.Name == volume.Name {
				v, found = volume, true
			}
			volumes = append(volumes, v)
		}
		if !found {
			volumes = append(volumes, volume)
		}
		current.Spec.Template.Spec.Volumes = volumes
		mount := corev1.VolumeMount{Name: volume.Name, MountPath: authgateway.WiringMountPath, ReadOnly: true}
		mounts := current.Spec.Template.Spec.Containers[0].VolumeMounts[:0]
		found = false
		for _, m := range current.Spec.Template.Spec.Containers[0].VolumeMounts {
			if m.Name == mount.Name {
				m, found = mount, true
			}
			mounts = append(mounts, m)
		}
		if !found {
			mounts = append(mounts, mount)
		}
		current.Spec.Template.Spec.Containers[0].VolumeMounts = mounts
		if current.Spec.Template.Annotations == nil {
			current.Spec.Template.Annotations = map[string]string{}
		}
		current.Spec.Template.Annotations["mcpruntime.org/gateway-wiring-hash"] = hash
		_, err = c.AppsV1().Deployments(mcpAuthNamespace).Update(ctx, current, metav1.UpdateOptions{})
		return err
	})
	return hash, err
}

func protocolTCP() *corev1.Protocol           { v := corev1.ProtocolTCP; return &v }
func portPointer(p int32) *intstr.IntOrString { v := intstr.FromInt32(p); return &v }
func mode0440() *int32                        { v := int32(0440); return &v }
func mode0444() *int32                        { v := int32(0444); return &v }
