package cluster

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/pkg/authgateway"
)

const (
	testIssuer = "https://auth.example.com/mcp-auth"
	testPubKey = "-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----"
)

var testGatewayJSON = []byte(`{"listen":{"host":"0.0.0.0","port":8090},"authorization_server":{"issuer":"` + testIssuer + `"},"session_binding_key_file":"/etc/mcp-gateway-credential/session.key","services":[{"id":"demo","resource_url":"https://auth.example.com/demo/mcp","upstream_url":"https://upstream.example.com/mcp","validation_url":"https://upstream.example.com/mcp/auth/check","required_scopes":["tools:read"],"source":"form","resource_client":{"client_id":"gateway-demo","key_id":"k1","private_key_file":"/etc/mcp-gateway-credential/client.pem"}}]}`)

func runtimeOwned(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: mcpAuthNamespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"}}
}

// authCluster is a cluster with the installed MCP Auth.
func authCluster(mutateAuth func(*appsv1.Deployment)) *fake.Clientset {
	group := int64(65532)
	auth := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: authDeploymentName, Namespace: mcpAuthNamespace}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		SecurityContext:  &corev1.PodSecurityContext{FSGroup: &group},
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: "mcp-runtime-registry-pull"}},
		Containers: []corev1.Container{{Name: "auth-server", Image: "auth-image", Env: []corev1.EnvVar{
			{Name: "MCP_AUTH_ISSUER", Value: testIssuer}, {Name: "MCP_AUTH_CONNECTOR", Value: "keycloak"},
			{Name: "MCP_AUTH_RESOURCES", Value: "https://mcp.example.com/ordinary/mcp"}, {Name: "KEEP", Value: "yes"},
		}, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}}}},
		Volumes: []corev1.Volume{{Name: "data"}},
	}}}}
	if mutateAuth != nil {
		mutateAuth(auth)
	}
	return fake.NewSimpleClientset(auth,
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: authDeploymentName, Namespace: mcpAuthNamespace}, Spec: networkingv1.IngressSpec{TLS: []networkingv1.IngressTLS{{Hosts: []string{"auth.example.com"}, SecretName: "existing-tls"}}}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: mcpAuthConnectorName, Namespace: mcpAuthNamespace}, Data: map[string]string{"connectors.json": `{"keycloak":{"issuer":"https://kc/realms/x","client_secret_env":"KC_SECRET"}}`}},
	)
}

func testInstall(t *testing.T, dryRun bool) gatewayInstall {
	t.Helper()
	cfg, err := authgateway.Parse(testGatewayJSON)
	if err != nil {
		t.Fatal(err)
	}
	wiring, err := authgateway.BuildWiring(cfg, map[string]string{"demo": testPubKey})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string][]byte{"config.json": testGatewayJSON, "client.pem": []byte("fixture-private-material"), "session.key": []byte(strings.Repeat("k", 40))}
	return gatewayInstall{cfg: cfg, payload: payload, wiring: wiring, tlsSecret: "existing-tls", image: "registry.example.com/mcp-gateway:v1", dryRun: dryRun}
}

func envOf(t *testing.T, c *fake.Clientset) map[string]string {
	t.Helper()
	d, err := c.AppsV1().Deployments(mcpAuthNamespace).Get(context.Background(), authDeploymentName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return plainEnv(d.Spec.Template.Spec.Containers[0].Env)
}

func TestInstallCreatesTheGatewayWiresAuthAndRetiresTheEarlierLayout(t *testing.T) {
	ctx := context.Background()
	c := authCluster(nil)
	report, err := installGateway(ctx, c, testInstall(t, false))
	if err != nil {
		t.Fatal(err)
	}
	if report.image != "registry.example.com/mcp-gateway:v1" {
		t.Fatalf("image = %q", report.image)
	}

	// The gateway is its own, locked-down workload.
	secret, _ := c.CoreV1().Secrets(mcpAuthNamespace).Get(ctx, authgateway.GatewayName, metav1.GetOptions{})
	if string(secret.Data["client.pem"]) != "fixture-private-material" || len(secret.Data["config.json"]) == 0 {
		t.Fatal("the gateway Secret does not hold the config and key")
	}
	d, err := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authgateway.GatewayName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod, container := d.Spec.Template.Spec, d.Spec.Template.Spec.Containers[0]
	if container.Image != "registry.example.com/mcp-gateway:v1" || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot ||
		*container.SecurityContext.ReadOnlyRootFilesystem != true || *container.SecurityContext.AllowPrivilegeEscalation != false {
		t.Fatalf("the gateway is not locked down: %+v", container.SecurityContext)
	}
	if len(pod.ImagePullSecrets) != 1 || pod.ImagePullSecrets[0].Name != "mcp-runtime-registry-pull" {
		t.Fatal("the gateway cannot pull from the platform registry")
	}
	if container.ReadinessProbe == nil || container.LivenessProbe == nil || plainEnv(container.Env)["MCP_GATEWAY_CONFIG"] != authgateway.MountPath+"/config.json" {
		t.Fatal("the gateway lacks probes or its config path")
	}
	ingress, _ := c.NetworkingV1().Ingresses(mcpAuthNamespace).Get(ctx, authgateway.GatewayName, metav1.GetOptions{})
	var paths []string
	for _, p := range ingress.Spec.Rules[0].HTTP.Paths {
		paths = append(paths, p.Path)
	}
	if ingress.Spec.Rules[0].Host != "auth.example.com" || strings.Join(paths, ",") != "/demo/mcp,/.well-known/oauth-protected-resource/demo/mcp" {
		t.Fatalf("ingress routes %v on %s", paths, ingress.Spec.Rules[0].Host)
	}
	if _, err := c.CoreV1().Services(mcpAuthNamespace).Get(ctx, authgateway.GatewayName, metav1.GetOptions{}); err != nil {
		t.Fatal("no gateway Service")
	}
	if _, err := c.NetworkingV1().NetworkPolicies(mcpAuthNamespace).Get(ctx, authgateway.GatewayName, metav1.GetOptions{}); err != nil {
		t.Fatal("no gateway NetworkPolicy")
	}

	// mcp-auth is wired to it without losing its own connector.
	connectors, _ := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, mcpAuthConnectorName, metav1.GetOptions{})
	var merged map[string]json.RawMessage
	_ = json.Unmarshal([]byte(connectors.Data["connectors.json"]), &merged)
	if _, ok := merged["keycloak"]; !ok || merged[authgateway.ConnectorName("demo")] == nil {
		t.Fatalf("connectors after merge: %v", connectors.Data["connectors.json"])
	}
	wiring, _ := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, authgateway.WiringName, metav1.GetOptions{})
	if !strings.Contains(wiring.Data["resource-logins.json"], `"form-demo:"`) || !strings.Contains(wiring.Data["external-credentials.json"], "gateway-demo") || wiring.Data["demo.public.pem"] == "" {
		t.Fatalf("wiring files: %v", wiring.Data)
	}
	if strings.Contains(strings.Join(mapValues(wiring.Data), ""), "fixture-private-material") {
		t.Fatal("private key material reached a ConfigMap")
	}
	env := envOf(t, c)
	if env["MCP_AUTH_EXTERNAL_CREDENTIALS_FILE"] != authgateway.ExternalCredentialsFile || env["MCP_AUTH_RESOURCE_LOGINS_FILE"] != authgateway.ResourceLoginsFile {
		t.Fatalf("mcp-auth was not pointed at the wiring: %v", env)
	}
	if env["KEEP"] != "yes" || env["MCP_AUTH_CONNECTOR"] != "keycloak" {
		t.Fatal("unrelated mcp-auth configuration changed")
	}
	if !strings.Contains(env["MCP_AUTH_RESOURCES"], "https://mcp.example.com/ordinary/mcp") || !strings.Contains(env["MCP_AUTH_RESOURCES"], "https://auth.example.com/demo/mcp") {
		t.Fatalf("resources = %q", env["MCP_AUTH_RESOURCES"])
	}
	auth, _ := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authDeploymentName, metav1.GetOptions{})
	if auth.Spec.Template.Spec.Containers[0].Image != "auth-image" || auth.Spec.Template.Annotations["mcpruntime.org/gateway-wiring-hash"] == "" {
		t.Fatal("the auth image changed or its restart marker is missing")
	}
}

func mapValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestDryRunWritesNothing(t *testing.T) {
	c := authCluster(nil)
	if _, err := installGateway(context.Background(), c, testInstall(t, true)); err != nil {
		t.Fatal(err)
	}
	for _, a := range c.Actions() {
		if a.GetVerb() != "get" {
			t.Fatalf("dry run did %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
}

func TestInstallingTwiceChangesNothingTheSecondTime(t *testing.T) {
	ctx := context.Background()
	c := authCluster(nil)
	if _, err := installGateway(ctx, c, testInstall(t, false)); err != nil {
		t.Fatal(err)
	}
	first, _ := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authDeploymentName, metav1.GetOptions{})
	connectors, _ := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, mcpAuthConnectorName, metav1.GetOptions{})
	if _, err := installGateway(ctx, c, testInstall(t, false)); err != nil {
		t.Fatal(err)
	}
	second, _ := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, authDeploymentName, metav1.GetOptions{})
	again, _ := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(ctx, mcpAuthConnectorName, metav1.GetOptions{})
	if first.Spec.Template.Annotations["mcpruntime.org/gateway-wiring-hash"] != second.Spec.Template.Annotations["mcpruntime.org/gateway-wiring-hash"] {
		t.Fatal("a repeat install changed the restart marker, so mcp-auth would restart for nothing")
	}
	if connectors.Data["connectors.json"] != again.Data["connectors.json"] {
		t.Fatal("a repeat install rewrote the connectors")
	}
	if got := plainEnv(second.Spec.Template.Spec.Containers[0].Env)["MCP_AUTH_RESOURCES"]; strings.Count(got, "https://auth.example.com/demo/mcp") != 1 {
		t.Fatalf("a resource was registered twice: %q", got)
	}
}

func TestInstallRefusesUnsafeOrUnsupportedInstallations(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		cluster func() *fake.Clientset
		mutate  func(*gatewayInstall)
	}{
		"login methods mode": {cluster: func() *fake.Clientset {
			return authCluster(func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Env = append(d.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "MCP_AUTH_LOGIN_METHODS_FILE", Value: "/x"})
			})
		}},
		"no single connector": {cluster: func() *fake.Clientset {
			return authCluster(func(d *appsv1.Deployment) {
				env := d.Spec.Template.Spec.Containers[0].Env
				for i := range env {
					if env[i].Name == "MCP_AUTH_CONNECTOR" {
						env[i].Value = ""
					}
				}
			})
		}},
		"foreign external credentials file": {cluster: func() *fake.Clientset {
			return authCluster(func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Env = append(d.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "MCP_AUTH_EXTERNAL_CREDENTIALS_FILE", Value: "/their/file.json"})
			})
		}},
		"issuer mismatch": {cluster: func() *fake.Clientset {
			return authCluster(func(d *appsv1.Deployment) {
				d.Spec.Template.Spec.Containers[0].Env[0].Value = "https://other.example.com/mcp-auth"
			})
		}},
		"wrong tls secret":          {cluster: func() *fake.Clientset { return authCluster(nil) }, mutate: func(in *gatewayInstall) { in.tlsSecret = "someone-elses-tls" }},
		"no image on first install": {cluster: func() *fake.Clientset { return authCluster(nil) }, mutate: func(in *gatewayInstall) { in.image = "" }},
	}
	for name, tc := range cases {
		in := testInstall(t, false)
		if tc.mutate != nil {
			tc.mutate(&in)
		}
		c := tc.cluster()
		if _, err := installGateway(ctx, c, in); err == nil {
			t.Errorf("accepted: %s", name)
			continue
		}
		for _, a := range c.Actions() {
			if a.GetVerb() != "get" {
				t.Errorf("%s: refused but still did %s %s", name, a.GetVerb(), a.GetResource().Resource)
			}
		}
	}
}

func TestUnownedGatewayObjectsAreNeverOverwritten(t *testing.T) {
	ctx := context.Background()
	c := authCluster(nil)
	if _, err := c.AppsV1().Deployments(mcpAuthNamespace).Create(ctx, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: authgateway.GatewayName, Namespace: mcpAuthNamespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := installGateway(ctx, c, testInstall(t, false)); err == nil {
		t.Fatal("a Deployment Runtime does not own was replaced")
	}
}

func TestMcpAuthIsLeftAloneWhenTheGatewayNeverBecomesReady(t *testing.T) {
	c := authCluster(nil)
	in := testInstall(t, false)
	in.waitTimeout = 50 * time.Millisecond // the fake cluster never reports readiness
	if _, err := installGateway(context.Background(), c, in); err == nil || !strings.Contains(err.Error(), "MCP Auth was not changed") {
		t.Fatalf("err = %v", err)
	}
	env := envOf(t, c)
	if env["MCP_AUTH_RESOURCE_LOGINS_FILE"] != "" {
		t.Fatal("mcp-auth was rewired although the gateway was not up")
	}
	connectors, _ := c.CoreV1().ConfigMaps(mcpAuthNamespace).Get(context.Background(), mcpAuthConnectorName, metav1.GetOptions{})
	if strings.Contains(connectors.Data["connectors.json"], authgateway.ConnectorPrefix) {
		t.Fatal("the connectors were changed before the gateway was up")
	}
}

func TestResourceClientPublicKeysAreDerivedFromThePrivateKeys(t *testing.T) {
	cfg, _ := authgateway.Parse(testGatewayJSON)
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	for name, block := range map[string]*pem.Block{
		"pkcs8": {Type: "PRIVATE KEY", Bytes: pkcs8},
		"pkcs1": {Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)},
	} {
		keys, err := resourceClientPublicKeys(cfg, map[string][]byte{"client.pem": pem.EncodeToMemory(block)})
		if err != nil || !strings.Contains(keys["demo"], "BEGIN PUBLIC KEY") || strings.Contains(keys["demo"], "PRIVATE") {
			t.Fatalf("%s: %v %q", name, err, keys["demo"])
		}
	}
	for name, payload := range map[string][]byte{"missing": nil, "not pem": []byte("nope"), "wrong block": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("x")})} {
		if _, err := resourceClientPublicKeys(cfg, map[string][]byte{"client.pem": payload}); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
}
