package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"mcp-runtime/pkg/authgateway"
)

func newMCPAuthGatewayCmd() *cobra.Command {
	root := &cobra.Command{Use: "mcp-auth-gateway", Short: "Configure the optional bundled MCP Auth credential gateway"}
	var file, mappings, tls, kubeconfig, kubeContext, clusterID string
	var files []string
	var yes, dryRun bool
	cmd := &cobra.Command{Use: "apply", Short: "Mount private gateway configuration and expose its OAuth resource paths", RunE: func(cmd *cobra.Command, _ []string) error {
		if file == "" || mappings == "" || tls == "" || kubeconfig == "" || kubeContext == "" || clusterID == "" {
			return errors.New("--file, --mappings-file, --tls-secret, --kubeconfig, --context and --expect-cluster-id are required")
		}
		if !dryRun && !yes {
			return errors.New("pass --yes after reviewing --dry-run")
		}
		data, err := os.ReadFile(file) // #nosec G304 -- explicit operator-supplied configuration.
		if err != nil {
			return errors.New("gateway configuration file is unreadable")
		}
		cfg, err := authgateway.Parse(data)
		if err != nil {
			return err
		}
		private, err := os.ReadFile(mappings) // #nosec G304 -- explicit private mapping input.
		if err != nil || !json.Valid(private) {
			return errors.New("mappings file must contain valid JSON")
		}
		payload := map[string][]byte{"config.json": data, "mappings.json": private}
		for _, entry := range files {
			key, path, ok := strings.Cut(entry, "=")
			if !ok || len(validation.IsConfigMapKey(key)) > 0 || key == "config.json" || key == "mappings.json" || payload[key] != nil {
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
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		rules.ExplicitPath = kubeconfig
		rest, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
		if err != nil {
			return errors.New("selected Kubernetes context is unavailable")
		}
		client, err := kubernetes.NewForConfig(rest)
		if err != nil {
			return err
		}
		ns, err := client.CoreV1().Namespaces().Get(cmd.Context(), "kube-system", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if string(ns.UID) != clusterID {
			return errors.New("selected cluster ID does not match --expect-cluster-id")
		}
		if err := applyAuthGateway(cmd.Context(), client, cfg, payload, tls, dryRun); err != nil {
			return err
		}
		action := "configured"
		if dryRun {
			action = "would configure"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s/%s: %d resource(s), gateway port %d; private values are omitted\n", action, mcpAuthNamespace, authgateway.SecretName, len(cfg.Services), cfg.Listen.Port)
		return nil
	}}
	cmd.Flags().StringVar(&file, "file", "", "Gateway JSON file; references must use /etc/mcp-auth-gateway/<key>")
	cmd.Flags().StringVar(&mappings, "mappings-file", "", "Private identity-to-vault-reference mapping JSON file")
	cmd.Flags().StringArrayVar(&files, "secret-file", nil, "Mount a private KEY=FILE input (repeat for provider token, session key and resource-client key)")
	cmd.Flags().StringVar(&tls, "tls-secret", "", "Existing TLS Secret covering the auth hostname; no certificate is issued")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Explicit kubeconfig path")
	cmd.Flags().StringVar(&kubeContext, "context", "", "Explicit Kubernetes context")
	cmd.Flags().StringVar(&clusterID, "expect-cluster-id", "", "Expected kube-system namespace UID")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate inputs and show the configuration plan without writing")
	cmd.Flags().BoolVar(&yes, "yes", false, "Apply the reviewed configuration without another prompt")
	root.AddCommand(cmd)
	return root
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
						return errors.New("gateway file references must use /etc/mcp-auth-gateway/<key>")
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

func gatewayObjectMeta() metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: authgateway.SecretName, Namespace: mcpAuthNamespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"}}
}

func applyAuthGateway(ctx context.Context, c kubernetes.Interface, cfg *authgateway.Config, payload map[string][]byte, tlsSecret string, dryRun bool) error {
	d, err := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, "mcp-auth-server", metav1.GetOptions{})
	if err != nil {
		return err
	}
	if len(d.Spec.Template.Spec.Containers) != 1 || d.Spec.Template.Spec.SecurityContext == nil || d.Spec.Template.Spec.SecurityContext.FSGroup == nil {
		return errors.New("bundled auth requires one container and an explicit fsGroup for private mounts")
	}
	issuer := ""
	for _, v := range d.Spec.Template.Spec.Containers[0].Env {
		if v.Name == "MCP_AUTH_ISSUER" {
			issuer = v.Value
		}
	}
	if issuer != cfg.AuthorizationServer.Issuer {
		return errors.New("gateway issuer does not match the installed auth deployment")
	}
	existingIngress, e := c.NetworkingV1().Ingresses(mcpAuthNamespace).Get(ctx, "mcp-auth-server", metav1.GetOptions{})
	if e != nil {
		return errors.New("existing bundled auth ingress is unavailable")
	}
	issuerURL, _ := url.Parse(issuer)
	covered := false
	for _, tls := range existingIngress.Spec.TLS {
		if tls.SecretName == tlsSecret {
			for _, host := range tls.Hosts {
				if host == issuerURL.Hostname() {
					covered = true
				}
			}
		}
	}
	if !covered {
		return errors.New("--tls-secret must reuse the installed auth ingress certificate for this host")
	}
	if dryRun {
		return nil
	}
	// Credentials remain exclusively in a read-only Secret; never log payloads.
	secret := &corev1.Secret{ObjectMeta: gatewayObjectMeta(), Type: corev1.SecretTypeOpaque, Data: payload}
	secretRevision := ""
	if old, e := c.CoreV1().Secrets(mcpAuthNamespace).Get(ctx, secret.Name, metav1.GetOptions{}); e == nil {
		if old.Labels["app.kubernetes.io/managed-by"] != "mcp-runtime" {
			return errors.New("gateway Secret exists without Runtime ownership")
		}
		if reflect.DeepEqual(old.Data, payload) {
			secretRevision = old.ResourceVersion
		} else {
			old.Data = payload
			updated, e := c.CoreV1().Secrets(mcpAuthNamespace).Update(ctx, old, metav1.UpdateOptions{})
			if e != nil {
				return e
			}
			secretRevision = updated.ResourceVersion
		}
	} else if apierrors.IsNotFound(e) {
		created, e := c.CoreV1().Secrets(mcpAuthNamespace).Create(ctx, secret, metav1.CreateOptions{})
		if e != nil {
			return e
		}
		secretRevision = created.ResourceVersion
	} else {
		return e
	}
	selector := metav1.LabelSelector{MatchLabels: map[string]string{"app": "mcp-auth-server"}}
	policy := &networkingv1.NetworkPolicy{ObjectMeta: gatewayObjectMeta(), Spec: networkingv1.NetworkPolicySpec{PodSelector: selector, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}, {NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "traefik"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: protocolTCP(), Port: portPointer(cfg.Listen.Port)}}}}}}
	if old, e := c.NetworkingV1().NetworkPolicies(mcpAuthNamespace).Get(ctx, policy.Name, metav1.GetOptions{}); e == nil {
		policy.ResourceVersion = old.ResourceVersion
		_, err = c.NetworkingV1().NetworkPolicies(mcpAuthNamespace).Update(ctx, policy, metav1.UpdateOptions{})
	} else if apierrors.IsNotFound(e) {
		_, err = c.NetworkingV1().NetworkPolicies(mcpAuthNamespace).Create(ctx, policy, metav1.CreateOptions{})
	} else {
		return e
	}
	if err != nil {
		return err
	}
	svc := &corev1.Service{ObjectMeta: gatewayObjectMeta(), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Selector: selector.MatchLabels, Ports: []corev1.ServicePort{{Name: "http", Port: cfg.Listen.Port, TargetPort: intstr.FromInt32(cfg.Listen.Port), Protocol: corev1.ProtocolTCP}}}}
	if old, e := c.CoreV1().Services(mcpAuthNamespace).Get(ctx, svc.Name, metav1.GetOptions{}); e == nil {
		old.Spec.Ports = svc.Spec.Ports
		old.Spec.Selector = svc.Spec.Selector
		_, err = c.CoreV1().Services(mcpAuthNamespace).Update(ctx, old, metav1.UpdateOptions{})
	} else if apierrors.IsNotFound(e) {
		_, err = c.CoreV1().Services(mcpAuthNamespace).Create(ctx, svc, metav1.CreateOptions{})
	} else {
		return e
	}
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, e := c.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, d.Name, metav1.GetOptions{})
		if e != nil {
			return e
		}
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		prior, e := authgateway.RegisteredResources(current.Annotations[authgateway.ResourceAnnotation], issuer)
		if e != nil {
			return e
		}
		registered := cfg.Resources()
		encoded, _ := json.Marshal(registered)
		current.Annotations[authgateway.ResourceAnnotation] = string(encoded)
		envs := current.Spec.Template.Spec.Containers[0].Env
		set := func(name, value string) {
			for i := range envs {
				if envs[i].Name == name {
					envs[i].Value = value
					envs[i].ValueFrom = nil
					return
				}
			}
			envs = append(envs, corev1.EnvVar{Name: name, Value: value})
		}
		resources := map[string]bool{}
		scopes := map[string][]string{}
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
			resources[r] = true
			scopes[r] = s
		}
		list := make([]string, 0, len(resources))
		for r := range resources {
			list = append(list, r)
		}
		sort.Strings(list)
		set("MCP_AUTH_RESOURCES", strings.Join(list, ","))
		encoded, _ = json.Marshal(scopes)
		set("MCP_AUTH_RESOURCE_SCOPES", string(encoded))
		set("MCP_AUTH_CREDENTIAL_GATEWAY_CONFIG", authgateway.MountPath+"/config.json")
		current.Spec.Template.Spec.Containers[0].Env = envs
		volume := corev1.Volume{Name: "credential-gateway", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: authgateway.SecretName, DefaultMode: mode0440()}}}
		found := false
		for i, v := range current.Spec.Template.Spec.Volumes {
			if v.Name == volume.Name {
				current.Spec.Template.Spec.Volumes[i] = volume
				found = true
			}
		}
		if !found {
			current.Spec.Template.Spec.Volumes = append(current.Spec.Template.Spec.Volumes, volume)
		}
		mount := corev1.VolumeMount{Name: volume.Name, MountPath: authgateway.MountPath, ReadOnly: true}
		found = false
		for i, m := range current.Spec.Template.Spec.Containers[0].VolumeMounts {
			if m.Name == mount.Name {
				current.Spec.Template.Spec.Containers[0].VolumeMounts[i] = mount
				found = true
			}
		}
		if !found {
			current.Spec.Template.Spec.Containers[0].VolumeMounts = append(current.Spec.Template.Spec.Containers[0].VolumeMounts, mount)
		}
		if current.Spec.Template.Annotations == nil {
			current.Spec.Template.Annotations = map[string]string{}
		}
		// ResourceVersion triggers reload after token/key rotation without
		// exposing a brute-forceable hash of any secret value.
		current.Spec.Template.Annotations["mcpruntime.org/auth-gateway-secret-version"] = secretRevision

		if _, e = c.AppsV1().Deployments(mcpAuthNamespace).Update(ctx, current, metav1.UpdateOptions{}); e != nil {
			return e
		}
		return applyGatewayIngress(ctx, c, cfg, tlsSecret)
	})
}

func applyGatewayIngress(ctx context.Context, c kubernetes.Interface, cfg *authgateway.Config, tlsSecret string) error {
	class := "traefik"
	exact, prefix := networkingv1.PathTypeExact, networkingv1.PathTypePrefix
	paths := []networkingv1.HTTPIngressPath{}
	for _, s := range cfg.Services {
		u, _ := url.Parse(s.ResourceURL)
		backend := networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: authgateway.SecretName, Port: networkingv1.ServiceBackendPort{Number: cfg.Listen.Port}}}
		paths = append(paths, networkingv1.HTTPIngressPath{Path: u.Path, PathType: &prefix, Backend: backend}, networkingv1.HTTPIngressPath{Path: "/.well-known/oauth-protected-resource" + u.Path, PathType: &exact, Backend: backend})
	}
	u, _ := url.Parse(cfg.AuthorizationServer.Issuer)
	meta := gatewayObjectMeta()
	meta.Annotations = map[string]string{"traefik.ingress.kubernetes.io/router.entrypoints": "websecure", "traefik.ingress.kubernetes.io/router.tls": "true"}
	ingress := &networkingv1.Ingress{ObjectMeta: meta, Spec: networkingv1.IngressSpec{IngressClassName: &class, TLS: []networkingv1.IngressTLS{{Hosts: []string{u.Hostname()}, SecretName: tlsSecret}}, Rules: []networkingv1.IngressRule{{Host: u.Hostname(), IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths}}}}}}
	if old, e := c.NetworkingV1().Ingresses(mcpAuthNamespace).Get(ctx, ingress.Name, metav1.GetOptions{}); e == nil {
		ingress.ResourceVersion = old.ResourceVersion
		_, e = c.NetworkingV1().Ingresses(mcpAuthNamespace).Update(ctx, ingress, metav1.UpdateOptions{})
		return e
	} else if !apierrors.IsNotFound(e) {
		return e
	}
	_, err := c.NetworkingV1().Ingresses(mcpAuthNamespace).Create(ctx, ingress, metav1.CreateOptions{})
	return err
}

func protocolTCP() *corev1.Protocol           { v := corev1.ProtocolTCP; return &v }
func portPointer(p int32) *intstr.IntOrString { v := intstr.FromInt32(p); return &v }
func mode0440() *int32                        { v := int32(0440); return &v }
