// Package bootstrap owns routing for the bootstrap top-level command.
package bootstrap

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
)

type manager struct {
	kubectl core.KubectlRunner
}

func newManager(runtime *core.Runtime) *manager {
	return &manager{kubectl: runtime.KubectlRunner()}
}

func detectProvider(kubectl core.KubectlRunner) (string, error) {
	out, err := kubectlOutput(kubectl, []string{"get", "nodes", "-o", "jsonpath={range .items[*]}{.status.nodeInfo.kubeletVersion}{\"\\n\"}{end}"})
	if err != nil {
		return "", core.WrapWithBase(core.ErrClusterNotAccessible, err, formatKubectlFailure("kubectl get nodes failed", out, err))
	}
	lower := strings.ToLower(string(out))
	switch {
	case strings.Contains(lower, "k3s"):
		return "k3s", nil
	case strings.Contains(lower, "rke2"):
		return "rke2", nil
	default:
		return "generic", nil
	}
}

func runBootstrapPreflight(kubectl core.KubectlRunner) error {
	core.Info("Preflight: kubectl connectivity")
	if err := kubectl.Run([]string{"version", "--client=true"}); err != nil {
		return core.WrapWithBase(core.ErrClusterNotAccessible, err, fmt.Sprintf("kubectl not available: %v", err))
	}
	out, err := kubectlOutput(kubectl, []string{"get", "nodes"})
	if err != nil {
		return core.WrapWithBase(core.ErrClusterNotAccessible, err, formatKubectlFailure("kubectl cannot reach cluster", out, err))
	}

	core.Info("Preflight: CoreDNS")
	if err := checkDeploymentExists(kubectl, "kube-system", "coredns"); err != nil {
		core.Warn("CoreDNS not detected (kube-system/deployment coredns). Cluster DNS must be installed for in-cluster service discovery.")
	}

	core.Info("Preflight: Default StorageClass")
	if err := checkHasDefaultStorageClass(kubectl); err != nil {
		core.Warn(fmt.Sprintf("No default StorageClass detected: %v", err))
	}

	core.Info("Preflight: IngressClass traefik")
	if err := kubectl.Run([]string{"get", "ingressclass", "traefik"}); err != nil {
		core.Warn("IngressClass traefik not found. If you plan to use Traefik, install it before running setup (or let setup install it when configured).")
	}

	core.Info("Preflight: MetalLB")
	if err := kubectl.Run([]string{"get", "ns", "metallb-system"}); err != nil {
		core.Warn("MetalLB not detected (namespace metallb-system). If you need LoadBalancer services on bare metal, install MetalLB.")
	}

	return nil
}

func bootstrapApplyK3s(kubectl core.KubectlRunner) error {
	core.Info("Applying k3s addons: CoreDNS + local-path provisioner (if missing)")

	paths := []string{
		"/var/lib/rancher/k3s/server/manifests/coredns.yaml",
		"/var/lib/rancher/k3s/server/manifests/local-storage.yaml",
	}
	var missing []string
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		msg := fmt.Sprintf("k3s manifests missing on disk (%s); bootstrap --apply expects to run on the k3s server node", strings.Join(missing, ", "))
		return core.WrapWithBase(core.ErrClusterConfigFailed, fmt.Errorf("missing manifests"), msg)
	}

	for _, p := range paths {
		if err := kubectl.Run([]string{"apply", "-f", p}); err != nil {
			return core.WrapWithBase(core.ErrClusterConfigFailed, err, fmt.Sprintf("failed to apply %s: %v", p, err))
		}
	}

	core.Info("Waiting for kube-system addons to be ready")
	if err := kubectl.Run([]string{"rollout", "status", "deployment/coredns", "-n", "kube-system", "--timeout=180s"}); err != nil {
		return core.WrapWithBase(core.ErrDeploymentTimeout, err, fmt.Sprintf("coredns rollout failed: %v", err))
	}
	if err := kubectl.Run([]string{"rollout", "status", "deployment/local-path-provisioner", "-n", "kube-system", "--timeout=180s"}); err != nil {
		return core.WrapWithBase(core.ErrDeploymentTimeout, err, fmt.Sprintf("local-path-provisioner rollout failed: %v", err))
	}

	core.Info("Node disk-pressure check")
	cond, err := kubectlOutput(kubectl, []string{"get", "nodes", "-o", "jsonpath={range .items[*]}{.metadata.name}{\" \"}{range .status.conditions[?(@.type==\"DiskPressure\")]}{.status}{end}{\"\\n\"}{end}"})
	if err == nil {
		core.Info(strings.TrimSpace(string(cond)))
	}

	return nil
}

func checkDeploymentExists(kubectl core.KubectlRunner, namespace, name string) error {
	return kubectl.Run([]string{"get", "deployment", name, "-n", namespace})
}

func checkHasDefaultStorageClass(kubectl core.KubectlRunner) error {
	out, err := kubectlOutput(kubectl, []string{"get", "storageclass", "-o", "jsonpath={range .items[*]}{.metadata.name}{\" \"}{.metadata.annotations.storageclass\\.kubernetes\\.io/is-default-class}{\"\\n\"}{end}"})
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "true" {
			return nil
		}
	}
	return fmt.Errorf("no StorageClass annotated with storageclass.kubernetes.io/is-default-class=true")
}

func kubectlOutput(kubectl core.KubectlRunner, args []string) ([]byte, error) {
	cmd, err := kubectl.CommandArgs(args)
	if err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}

func formatKubectlFailure(prefix string, out []byte, err error) string {
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		detail = strings.TrimSpace(err.Error())
	}
	hint := ""
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "connection refused"), strings.Contains(lower, " was refused"):
		hint = " (kube-apiserver endpoint refused the connection; verify the active context points to a running cluster)"
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "name or service not known"):
		hint = " (cluster endpoint hostname could not be resolved; verify kubeconfig server host/DNS)"
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "i/o timeout"):
		hint = " (cluster endpoint timed out; verify network path and API server health)"
	}
	return fmt.Sprintf("%s: %s%s", prefix, detail, hint)
}

// New returns the bootstrap command.
func New(runtime *core.Runtime) *cobra.Command {
	var apply bool
	var provider string
	mgr := newManager(runtime)

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap cluster prerequisites (on-prem focused)",
		Long: `Bootstrap validates and (optionally) installs cluster prerequisites needed by mcp-runtime setup.

By design, this does not provision Kubernetes clusters end-to-end across all distributions.
Use this to prepare an existing cluster for running 'mcp-runtime setup'.

Note: bootstrap --apply is automated for k3s only and must be executed on the k3s server node (it expects local manifests under /var/lib/rancher/k3s/server/manifests).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			core.Section("MCP Runtime Bootstrap")
			chosenProvider := provider
			if chosenProvider == "" || chosenProvider == "auto" {
				detectedProvider, err := detectProvider(mgr.kubectl)
				if err != nil {
					return err
				}
				chosenProvider = detectedProvider
			}
			core.Info(fmt.Sprintf("Provider: %s", chosenProvider))

			if err := runBootstrapPreflight(mgr.kubectl); err != nil {
				return err
			}

			if !apply {
				core.Success("Bootstrap preflight complete (no changes applied)")
				core.Info("Next: run `./bin/mcp-runtime setup` (or `./bin/mcp-runtime setup --storage-mode hostpath` for single-node dev)")
				return nil
			}

			switch chosenProvider {
			case "k3s":
				if err := bootstrapApplyK3s(mgr.kubectl); err != nil {
					return err
				}
			case "rke2", "kubeadm", "generic":
				core.Warn("Apply mode is currently only automated for k3s. For other distributions, use the preflight output and install DNS/storage/ingress/load-balancer via your standard platform tooling.")
			default:
				core.Warn(fmt.Sprintf("Unknown provider %q; skipping apply", chosenProvider))
			}

			core.Success("Bootstrap complete")
			core.Info("Next: run `./bin/mcp-runtime setup`")
			return nil
		},
	}

	cmd.Flags().BoolVar(&apply, "apply", false, "Apply safe bootstrap fixes when possible (k3s only today; run on the k3s server node)")
	cmd.Flags().StringVar(&provider, "provider", "auto", "Cluster provider hint (auto|k3s|rke2|kubeadm|generic)")
	return cmd
}
