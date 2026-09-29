package platform

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kube"
	"mcp-runtime/internal/cli/setup/assetpath"
	"mcp-runtime/pkg/metadata"
)

// mcpAuthInternalIssuerURLForCluster returns the in-cluster address gateway
// sidecars use for metadata and JWKS discovery, using the configured cluster
// service DNS suffix instead of assuming cluster.local.
// mcpAuthSigningKeyPath is where the signing key Secret is mounted. The Secret
// must carry the RSA private key in PEM form under this file name.
const mcpAuthSigningKeyPath = "/etc/mcp-auth-key/private-key.pem"

func mcpAuthInternalIssuerURLForCluster() string {
	return "http://" + clusterServiceDNS("mcp-auth-server", "mcp-sentinel") + ":8080"
}

// DefaultMCPAuthIssuerURL derives the bundled server's fixed public route from
// the platform domain, returning empty when no public domain is configured.
func DefaultMCPAuthIssuerURL() string {
	domain := metadata.NormalizePlatformDomain(os.Getenv("MCP_PLATFORM_DOMAIN"))
	if domain == "" {
		return ""
	}
	return "https://auth." + domain + "/mcp-auth"
}

// secretKeyPattern is the character set Kubernetes accepts for Secret data
// keys. Connector files supply these names, so they are validated before they
// are concatenated into a manifest.
var secretKeyPattern = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// mcpAuthServerOptions is the rendering input for k8s/23-mcp-auth-server.yaml.
type mcpAuthServerOptions struct {
	Image            string
	IssuerURL        string
	ResourceURLs     []string
	TLSSecret        string
	SigningKeySecret string
	ConnectorsFile   string
	Connector        string
	TestMode         bool
}

func deployMCPAuthServer(image, configuredIssuer string, configuredResources []string, tlsSecret, signingKeySecret, connectorsFile, connector string, testMode bool, deps SetupDeps) error {
	path, err := assetpath.ResolveRepoAssetPath("k8s/23-mcp-auth-server.yaml")
	if err != nil {
		return err
	}
	raw, err := kube.ReadFileAtPath(path)
	if err != nil {
		return err
	}
	opts := mcpAuthServerOptions{
		Image:            image,
		IssuerURL:        configuredIssuer,
		ResourceURLs:     configuredResources,
		TLSSecret:        tlsSecret,
		SigningKeySecret: signingKeySecret,
		ConnectorsFile:   connectorsFile,
		Connector:        connector,
		TestMode:         testMode,
	}
	if strings.TrimSpace(opts.IssuerURL) == "" {
		if testMode {
			opts.IssuerURL = "http://localhost:18080/mcp-auth"
		} else {
			opts.IssuerURL = DefaultMCPAuthIssuerURL()
		}
	}
	opts.IssuerURL = strings.TrimRight(strings.TrimSpace(opts.IssuerURL), "/")
	manifest, err := renderMCPAuthServerManifest(string(raw), opts)
	if err != nil {
		return err
	}

	if strings.TrimSpace(connectorsFile) != "" {
		connectorJSON, secretValues, err := readMCPAuthConnector(connectorsFile, connector)
		if err != nil {
			return err
		}
		if err := applyManifestYAML(renderMCPAuthConnectorConfigMap(connectorJSON), "", os.Stdout); err != nil {
			return fmt.Errorf("apply mcp-auth connector config: %w", err)
		}
		if len(secretValues) > 0 {
			secret, err := renderMCPAuthConnectorSecret(secretValues)
			if err != nil {
				return err
			}
			if err := applyManifestYAML(secret, "", os.Stdout); err != nil {
				return fmt.Errorf("apply mcp-auth connector secrets: %w", err)
			}
		}
	}

	core.Info("Applying optional bundled mcp-auth authorization server")
	if err := applyManifestYAML(manifest, "", os.Stdout); err != nil {
		return fmt.Errorf("apply mcp-auth authorization server: %w", err)
	}
	// Kind/port-forward only: auto-wire the in-cluster issuer backchannel under
	// --test-mode (or an explicit OAUTH_INTERNAL_ISSUER_URL). Production keeps
	// gateways on public HTTPS JWKS — see issue #528.
	operatorEnvArgs := []string{
		"set", "env", "deployment/mcp-runtime-operator-controller-manager",
		"-n", core.NamespaceMCPRuntime,
		"MCP_AUTH_ISSUER_URL=" + opts.IssuerURL,
	}
	if explicitInternal := strings.TrimSpace(os.Getenv("OAUTH_INTERNAL_ISSUER_URL")); explicitInternal != "" {
		operatorEnvArgs = append(operatorEnvArgs, "OAUTH_INTERNAL_ISSUER_URL="+explicitInternal)
	} else if opts.TestMode {
		operatorEnvArgs = append(operatorEnvArgs, "OAUTH_INTERNAL_ISSUER_URL="+mcpAuthInternalIssuerURLForCluster())
	} else {
		// Clear a leftover Kind backchannel so prod setup reruns do not keep
		// breaking gateway OAuth after a prior test-mode or buggy install.
		operatorEnvArgs = append(operatorEnvArgs, "OAUTH_INTERNAL_ISSUER_URL-")
	}
	cmd, err := core.DefaultKubectlClient().CommandArgs(operatorEnvArgs)
	if err != nil {
		return fmt.Errorf("prepare operator OAuth issuer update: %w", err)
	}
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("configure operator OAuth issuer: %w", err)
	}
	if err := deps.RestartDeployment("mcp-runtime-operator-controller-manager", core.NamespaceMCPRuntime); err != nil {
		return fmt.Errorf("restart operator after OAuth issuer update: %w", err)
	}
	return nil
}

// checkMCPAuthPrerequisites runs before image builds and deployment. Production
// auth uses durable signing material and a certificate-backed ingress; failing
// here avoids a long setup followed by an opaque FailedMount or TLS failure.
func checkMCPAuthPrerequisites(signingKeySecret string, testMode bool) error {
	if testMode {
		return nil
	}
	kubectl := core.DefaultKubectlClient()
	for _, prerequisite := range []struct {
		name   string
		secret string
		key    string
		remedy string
	}{
		{
			name:   "mcp-auth signing key",
			secret: signingKeySecret,
			key:    "private-key.pem",
			remedy: "create the Secret with an RSA PEM key under private-key.pem in namespace mcp-sentinel before setup",
		},
	} {
		keyPath := strings.ReplaceAll(prerequisite.key, ".", `\.`)
		cmd, err := kubectl.CommandArgs([]string{"get", "secret", prerequisite.secret, "-n", "mcp-sentinel", "-o", "jsonpath={.data." + keyPath + "}"})
		if err != nil {
			return fmt.Errorf("prepare %s Secret check: %w", prerequisite.name, err)
		}
		value, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("%s Secret %q is not available in namespace %q: %w; %s", prerequisite.name, prerequisite.secret, "mcp-sentinel", err, prerequisite.remedy)
		}
		if strings.TrimSpace(string(value)) == "" {
			return fmt.Errorf("%s Secret %q is missing non-empty data key %q; %s", prerequisite.name, prerequisite.secret, prerequisite.key, prerequisite.remedy)
		}
	}
	return nil
}

// renderMCPAuthServerManifest substitutes the deployment-specific values into
// the authorization server manifest. It performs no I/O so the rendered output,
// including every production guard, is directly testable.
func renderMCPAuthServerManifest(raw string, opts mcpAuthServerOptions) (string, error) {
	issuer := strings.TrimRight(strings.TrimSpace(opts.IssuerURL), "/")
	if issuer == "" && opts.TestMode {
		issuer = "http://localhost:18080/mcp-auth"
	}
	parsedIssuer, err := url.Parse(issuer)
	if err != nil || parsedIssuer.Host == "" {
		return "", fmt.Errorf("mcp-auth issuer URL %q is not an absolute URL", issuer)
	}
	if !opts.TestMode {
		if parsedIssuer.Scheme != "https" {
			return "", fmt.Errorf("production mcp-auth deployment requires an absolute HTTPS issuer URL")
		}
		if strings.TrimSpace(opts.ConnectorsFile) == "" || strings.TrimSpace(opts.Connector) == "" {
			return "", fmt.Errorf("production mcp-auth deployment requires a connector file and selected connector")
		}
		if strings.TrimSpace(opts.TLSSecret) == "" {
			return "", fmt.Errorf("production mcp-auth deployment requires a TLS Secret")
		}
		// The authorization server refuses to start outside local development
		// without a persistent signing key: an ephemeral key would invalidate
		// every issued token on restart.
		if strings.TrimSpace(opts.SigningKeySecret) == "" {
			return "", fmt.Errorf("production mcp-auth deployment requires a signing key Secret")
		}
	}

	resources, err := mcpAuthResourceURLs(opts.ResourceURLs, issuer, opts.TestMode)
	if err != nil {
		return "", err
	}

	manifest := strings.ReplaceAll(raw, "image: docker.io/princekrroshan01/mcp-auth-server:latest", "image: "+opts.Image)
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_ISSUER_VALUE", issuer)
	// Every resolved resource has to reach the authorization server, not just
	// the first: a deployment fronting two MCP servers would otherwise mint
	// tokens for one of them and reject the other's resource parameter with
	// "resource is not recognized" at /authorize. MCP_AUTH_RESOURCES is the
	// multi-resource setting and takes precedence server-side.
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_RESOURCES_VALUE", strconv.Quote(strings.Join(resources, ",")))
	firstResource := ""
	if len(resources) > 0 {
		firstResource = resources[0]
	}
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_RESOURCE_VALUE", strconv.Quote(firstResource))
	// Quoted: a container env value is a string, and a bare true/false renders
	// as a YAML boolean that the API server rejects on the EnvVar.Value field.
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_LOCAL_DEVELOPMENT_VALUE", strconv.FormatBool(opts.TestMode))
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_LOCAL_TOKEN_EXCHANGE_VALUE", strconv.FormatBool(opts.TestMode))
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_REQUIRE_HTTPS_VALUE", strconv.FormatBool(!opts.TestMode))
	// The platform always fronts the authorization server with an ingress
	// that terminates TLS, and k8s/23-mcp-auth-server.yaml restricts ingress
	// to that controller, so the forwarded-proto header can be trusted here.
	// A deployment that exposes the pod directly must not set this.
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_TRUST_PROXY_TLS_VALUE", strconv.FormatBool(!opts.TestMode))
	store := "sqlite"
	databaseURL := "/data/mcp-auth.db"
	dataVolume := "persistentVolumeClaim:\n            claimName: mcp-auth-server-data"
	if opts.TestMode {
		store = "memory"
		databaseURL = "/tmp/mcp-auth.db"
		dataVolume = "emptyDir: {}"
	}
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_STORE_VALUE", store)
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_DATABASE_URL_VALUE", databaseURL)
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_DATA_VOLUME_BLOCK", dataVolume)
	// Outside test mode the signing key is mounted from a Secret and named
	// through MCP_AUTH_PRIVATE_KEY_FILE; in test mode the server generates an
	// ephemeral key, which it only permits for a loopback issuer.
	if secret := strings.TrimSpace(opts.SigningKeySecret); secret != "" {
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_SIGNING_KEY_ENV_BLOCK", "- {name: MCP_AUTH_PRIVATE_KEY_FILE, value: "+mcpAuthSigningKeyPath+"}")
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_SIGNING_KEY_VOLUME_MOUNT_BLOCK", "- name: signing-key\n              mountPath: /etc/mcp-auth-key\n              readOnly: true")
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_SIGNING_KEY_POD_VOLUME_BLOCK", "- name: signing-key\n          secret: {secretName: "+strconv.Quote(secret)+", defaultMode: 0440}")
	} else {
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_SIGNING_KEY_ENV_BLOCK", "")
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_SIGNING_KEY_VOLUME_MOUNT_BLOCK", "")
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_SIGNING_KEY_POD_VOLUME_BLOCK", "")
	}
	manifest = strings.ReplaceAll(manifest, "MCP_AUTH_INGRESS_HOST_VALUE", parsedIssuer.Host)
	// The placeholder owns its whole line, so the block carries its own
	// indentation and test mode removes the line rather than leaving a stray
	// indented blank inside the Ingress spec.
	if opts.TestMode {
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_INGRESS_TLS_BLOCK\n", "")
	} else {
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_INGRESS_TLS_BLOCK", "  tls:\n    - hosts: ["+strconv.Quote(parsedIssuer.Host)+"]\n      secretName: "+opts.TLSSecret)
	}

	if strings.TrimSpace(opts.ConnectorsFile) != "" {
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_CONNECTOR_ENV_BLOCK", "- {name: MCP_AUTH_CONNECTORS_FILE, value: /etc/mcp-auth/connectors.json}\n            - {name: MCP_AUTH_CONNECTOR, value: "+strconv.Quote(opts.Connector)+"}")
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_CONNECTOR_ENVFROM_BLOCK", "- secretRef: {name: mcp-auth-connector-secrets}")
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_CONNECTOR_VOLUME_MOUNT_BLOCK", "- name: connectors\n              mountPath: /etc/mcp-auth\n              readOnly: true")
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_CONNECTOR_POD_VOLUME_BLOCK", "- name: connectors\n          configMap: {name: mcp-auth-connectors}\n        - name: connector-secrets\n          secret: {secretName: mcp-auth-connector-secrets}")
	} else {
		manifest = strings.ReplaceAll(manifest, "MCP_AUTH_CONNECTOR_ENV_BLOCK", "")
		// These placeholders occupy indented lines in the YAML template. The
		// empty-list form must replace the parent key too; inserting `[]` at
		// the placeholder indentation would create invalid YAML (`envFrom:\n
		//   []`).
		manifest = strings.ReplaceAll(manifest, "          envFrom:\n            MCP_AUTH_CONNECTOR_ENVFROM_BLOCK", "          envFrom: []")
		manifest = strings.ReplaceAll(manifest, "            MCP_AUTH_CONNECTOR_VOLUME_MOUNT_BLOCK", "            # no connector volume")
		manifest = strings.ReplaceAll(manifest, "        MCP_AUTH_CONNECTOR_POD_VOLUME_BLOCK", "        # no connector volume")
	}
	if remaining := unresolvedManifestPlaceholders(manifest); len(remaining) > 0 {
		return "", fmt.Errorf("mcp-auth manifest has unresolved placeholders: %s", strings.Join(remaining, ", "))
	}
	return manifest, nil
}

// mcpAuthResourceURLs resolves MCP_AUTH_RESOURCES, the set of resources the
// authorization server will mint tokens for. Each entry must equal the
// spec.auth.audience of the MCP server it fronts, because that audience is also
// the resource identifier the gateway advertises and validates.
//
// Outside --test-mode an empty initial set is valid: the operator supplies the
// current OAuth MCPServer audiences after those resources are created.
func mcpAuthResourceURLs(configured []string, issuer string, testMode bool) ([]string, error) {
	resources := make([]string, 0, len(configured))
	for _, value := range configured {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			resources = append(resources, strings.TrimRight(trimmed, "/"))
		}
	}
	if len(resources) == 0 {
		if !testMode {
			// The operator fills this list from live OAuth MCPServer audiences.
			return resources, nil
		}
		// Test mode serves the Go example's OAuth route through the Runtime
		// gateway.
		base := strings.TrimSuffix(issuer, "/mcp-auth")
		return []string{
			base + "/oauth-example-go-2025-11-25-gateway/mcp",
		}, nil
	}
	seen := map[string]bool{}
	for _, resource := range resources {
		parsed, err := url.Parse(resource)
		if err != nil || parsed.Host == "" {
			return nil, fmt.Errorf("mcp-auth resource URL %q is not an absolute URL", resource)
		}
		if !testMode && parsed.Scheme != "https" {
			return nil, fmt.Errorf("production mcp-auth resource URL %q must use https", resource)
		}
		if seen[resource] {
			return nil, fmt.Errorf("mcp-auth resource URL %q is listed twice", resource)
		}
		seen[resource] = true
	}
	return resources, nil
}

func renderMCPAuthConnectorConfigMap(connectorJSON []byte) string {
	configMap := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: mcp-auth-connectors\n  namespace: mcp-sentinel\ndata:\n  connectors.json: |\n"
	for _, line := range strings.Split(strings.TrimRight(string(connectorJSON), "\n"), "\n") {
		configMap += "    " + line + "\n"
	}
	return configMap
}

// renderMCPAuthConnectorSecret builds the connector credential Secret. Keys
// come from the connector file, so they are validated against the Kubernetes
// Secret key charset before being written into the manifest rather than after,
// when a malformed name would either be rejected by the API server with an
// opaque error or alter the surrounding YAML.
func renderMCPAuthConnectorSecret(values map[string]string) (string, error) {
	names := make([]string, 0, len(values))
	for name := range values {
		if !secretKeyPattern.MatchString(name) {
			return "", fmt.Errorf("connector credential name %q is not a valid Kubernetes Secret key", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	// This is a manifest template; it contains no credential or secret value.
	secret := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: mcp-auth-connector-secrets\n  namespace: mcp-sentinel\nstringData:\n" // #nosec G101 -- Kubernetes kind/name is not a credential.
	for _, name := range names {
		encoded, err := json.Marshal(values[name])
		if err != nil {
			return "", fmt.Errorf("encode connector credential %q: %w", name, err)
		}
		secret += "  " + name + ": " + string(encoded) + "\n"
	}
	return secret, nil
}

// unresolvedManifestPlaceholders reports template tokens the renderer left
// behind. Applying a partially rendered manifest would push the literal
// placeholder into the cluster as a container env value.
func unresolvedManifestPlaceholders(manifest string) []string {
	var remaining []string
	for _, placeholder := range []string{
		"MCP_AUTH_ISSUER_VALUE",
		"MCP_AUTH_RESOURCES_VALUE",
		"MCP_AUTH_RESOURCE_VALUE",
		"MCP_AUTH_LOCAL_DEVELOPMENT_VALUE",
		"MCP_AUTH_LOCAL_TOKEN_EXCHANGE_VALUE",
		"MCP_AUTH_REQUIRE_HTTPS_VALUE",
		"MCP_AUTH_TRUST_PROXY_TLS_VALUE",
		"MCP_AUTH_STORE_VALUE",
		"MCP_AUTH_DATABASE_URL_VALUE",
		"MCP_AUTH_DATA_VOLUME_BLOCK",
		"MCP_AUTH_SIGNING_KEY_ENV_BLOCK",
		"MCP_AUTH_SIGNING_KEY_VOLUME_MOUNT_BLOCK",
		"MCP_AUTH_SIGNING_KEY_POD_VOLUME_BLOCK",
		"MCP_AUTH_INGRESS_HOST_VALUE",
		"MCP_AUTH_INGRESS_TLS_BLOCK",
		"MCP_AUTH_CONNECTOR_ENV_BLOCK",
		"MCP_AUTH_CONNECTOR_ENVFROM_BLOCK",
		"MCP_AUTH_CONNECTOR_VOLUME_MOUNT_BLOCK",
		"MCP_AUTH_CONNECTOR_POD_VOLUME_BLOCK",
	} {
		if strings.Contains(manifest, placeholder) {
			remaining = append(remaining, placeholder)
		}
	}
	return remaining
}

func readMCPAuthConnector(path, selected string) ([]byte, map[string]string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- explicit setup flag path.
	if err != nil {
		return nil, nil, fmt.Errorf("read --mcp-auth-connectors-file: %w", err)
	}
	var connectors map[string]map[string]any
	if err := json.Unmarshal(data, &connectors); err != nil {
		return nil, nil, fmt.Errorf("parse --mcp-auth-connectors-file: %w", err)
	}
	if strings.TrimSpace(selected) == "" {
		return nil, nil, fmt.Errorf("--mcp-auth-connector is required when a connector file is provided")
	}
	config, ok := connectors[selected]
	if !ok {
		return nil, nil, fmt.Errorf("connector %q is not defined in %s", selected, path)
	}
	if exchangeID, ok := config["exchange_client_id"].(string); !ok || strings.TrimSpace(exchangeID) == "" {
		return nil, nil, fmt.Errorf("connector %q is missing exchange_client_id (required by mcp-auth)", selected)
	}
	secrets := map[string]string{}
	for _, key := range []string{"client_secret_env", "client_id_env"} {
		if name, ok := config[key].(string); ok && strings.TrimSpace(name) != "" {
			if value, exists := os.LookupEnv(name); exists {
				secrets[name] = value
			} else {
				return nil, nil, fmt.Errorf("connector %q references unset environment variable %s", selected, name)
			}
		}
	}
	return data, secrets, nil
}
