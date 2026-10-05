// Package update owns the `mcp-runtime update` command, which moves an
// installed MCP Runtime platform to a release by patching only the images of
// platform services whose versions changed.
package update

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/platformrelease"
	"mcp-runtime/pkg/k8sclient"
)

// Options holds parsed update flags.
type Options struct {
	To                 string
	ReleaseManifest    string
	CRDs               string
	DryRun             bool
	Yes                bool
	Only               []string
	IncludeAuth        bool
	IncludeCertManager bool
	AllowDowngrade     bool
	RollbackOnFailure  bool
	Timeout            time.Duration
	Output             string
	Kubeconfig         string
	Context            string
	Build              bool
	Source             string
	ImagePlatform      string
	BuildParallelism   int
}

// kubeHandle is the Kubernetes client pair update needs for inventory and CRD apply.
type kubeHandle struct {
	Clientset kubernetes.Interface
	Clients   *k8sclient.Clients
	Cluster   ClusterInfo
}

// deps are injectable for tests.
type deps struct {
	loadManifest func(ctx context.Context, source string) ([]byte, error)
	kube         func(kubeconfig, context string) (kubeHandle, error)
	waiter       func(cs kubernetes.Interface) RolloutWaiter
	confirm      func(in io.Reader, out io.Writer, cluster ClusterInfo) (bool, error)
	stdin        io.Reader
	build        BuildOptions
}

func defaultDeps() deps {
	return deps{
		loadManifest: func(ctx context.Context, source string) ([]byte, error) {
			return platformrelease.LoadManifest(ctx, source, nil)
		},
		kube:    kubeClient,
		waiter:  DefaultWaiter,
		confirm: promptConfirm,
		stdin:   os.Stdin,
	}
}

// New returns the update command.
func New(_ *core.Runtime) *cobra.Command {
	return newCommand(defaultDeps())
}

func newCommand(d deps) *cobra.Command {
	opts := Options{}
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update installed platform services to a release",
		Long: `Update an installed MCP Runtime platform to a release.

The target comes from a release component manifest (service -> image
repository, tag, optional digest), selected with --to (fetches the manifest
attached to that GitHub release) or --release-manifest (local path or https
URL). update compares it with the images running in the cluster and patches
only the Deployments whose images changed, one at a time, waiting for each
rollout. Every component the release changes is included; unchanged
components are left alone.

When the release sets crdChange, the manifest embeds CustomResourceDefinition
YAML (field "crds") so update applies only those CRD objects whose live spec
differs, waits until each written CRD is Established, then rolls images. Pass
--crds or use the release's platform-crds.yaml when the JSON omits the bundle.
With --only, CRD apply is skipped so scoped image updates cannot mutate
cluster schemas. update never modifies Secrets, PVCs, ConfigMaps, cert-manager
Issuers/Certificates, Services, or Ingresses, and never deletes or recreates
workloads. mcp-auth and cert-manager are skipped unless selected with
--include-auth, --include-cert-manager, or --only.

With --build, update builds and pushes only Built-component images from the
release that are missing from the registry (sequential by default; use
--build-parallelism to raise concurrency), then rolls those Deployments.
Images already present are reused; Deployments still on an older tag are
patched. Failed builds retry once and cancel sibling builds. Without --build,
images must already be published.

The plan always shows the kube context and cluster ID. Without --dry-run,
update asks for confirmation (or requires --yes when not interactive).
Relative repositories resolve against the registry of the running image.`,
		Example: `  mcp-runtime update --to v0.5.0 --dry-run
  mcp-runtime update --release-manifest ./platform-manifest.json
  mcp-runtime update --to v0.5.0 --only ui,platform-api --yes
  mcp-runtime update --release-manifest ./platform-manifest.json --crds ./platform-crds.yaml --yes
  mcp-runtime update --to v0.3.1 --release-manifest ./platform-manifest.json --build --source . --yes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), cmd.OutOrStdout(), opts, d)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.To, "to", "", "Target release version (for example v0.5.0); fetches that release's platform-manifest.json unless --release-manifest is set")
	f.StringVar(&opts.ReleaseManifest, "release-manifest", "", "Release component manifest path or https URL; its version must match --to when both are set")
	f.StringVar(&opts.CRDs, "crds", "", "CRD multi-document YAML path or https URL when the manifest omits embedded crds (optional; --to also tries platform-crds.yaml)")
	f.BoolVar(&opts.DryRun, "dry-run", false, "Print the update plan and exit without changing the cluster")
	f.BoolVar(&opts.Yes, "yes", false, "Apply without the interactive confirmation prompt")
	f.StringSliceVar(&opts.Only, "only", nil, "Comma-separated components to consider (default: all non-opt-in components)")
	f.BoolVar(&opts.IncludeAuth, "include-auth", false, "Include the mcp-auth authorization server")
	f.BoolVar(&opts.IncludeCertManager, "include-cert-manager", false, "Include cert-manager images (patch releases only; CRDs are not upgraded)")
	f.BoolVar(&opts.AllowDowngrade, "allow-downgrade", false, "Allow a target version lower than the installed version")
	f.BoolVar(&opts.RollbackOnFailure, "rollback-on-failure", true, "Restore previous images of workloads changed in this run if a rollout fails")
	f.DurationVar(&opts.Timeout, "timeout", 5*time.Minute, "Rollout wait timeout per workload")
	f.StringVar(&opts.Output, "output", "text", "Output format: text or json")
	f.StringVar(&opts.Kubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: KUBECONFIG or ~/.kube/config)")
	f.StringVar(&opts.Context, "context", "", "Kubernetes context to use (default: current context)")
	f.BoolVar(&opts.Build, "build", false, "Build and push missing Built-component images from --source before rolling Deployments")
	f.StringVar(&opts.Source, "source", ".", "Repository root used with --build (must contain go.mod, services/, k8s/)")
	f.StringVar(&opts.ImagePlatform, "image-platform", "", "Docker --platform for --build (default: MCP_IMAGE_PLATFORM or linux/amd64)")
	f.IntVar(&opts.BuildParallelism, "build-parallelism", defaultBuildParallelism, "Max concurrent image builds with --build (default 1; raise carefully)")
	return cmd
}

func run(ctx context.Context, out io.Writer, opts Options, d deps) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateOptions(opts); err != nil {
		return err
	}
	manifest, source, err := resolveManifest(ctx, opts, d)
	if err != nil {
		return err
	}
	kh, err := d.kube(opts.Kubeconfig, opts.Context)
	if err != nil {
		return core.WrapWithBase(core.ErrUpdateKubeClientFailed, err, fmt.Sprintf("connect to Kubernetes: %v (pass --kubeconfig/--context)", err))
	}
	plan, err := BuildPlan(ctx, kh.Clientset, manifest, Selection{
		Only:               opts.Only,
		IncludeAuth:        opts.IncludeAuth,
		IncludeCertManager: opts.IncludeCertManager,
		AllowDowngrade:     opts.AllowDowngrade,
	})
	if err != nil {
		return err
	}
	plan.Cluster = kh.Cluster
	plan.ManifestSource = source

	if plan.ApplyCRDs && kh.Clients != nil {
		preview, err := refineCRDPlan(ctx, kh.Clients, plan)
		if err != nil {
			return core.WrapWithBase(core.ErrUpdateCRDChange, err, fmt.Sprintf("compare CustomResourceDefinitions: %v", err))
		}
		plan.CRDPreview = preview
	}

	jsonOut := opts.Output == "json"
	report := func(res *Result) error {
		if jsonOut {
			return writeJSON(out, plan, res, opts.DryRun)
		}
		if res != nil {
			writeResultText(out, res)
		}
		return nil
	}

	if blocked := plan.Blocked(); len(blocked) > 0 {
		if !jsonOut {
			writePlanText(out, plan)
		}
		_ = report(nil)
		names := make([]string, 0, len(blocked))
		for _, r := range blocked {
			names = append(names, r.Component)
		}
		return core.NewWithBase(core.ErrUpdateBlocked, fmt.Sprintf("update blocked for %s; see plan reasons", strings.Join(names, ", ")))
	}

	buildOpts := d.build
	buildOpts.Enabled = opts.Build
	if opts.Build {
		if buildOpts.Source == "" {
			buildOpts.Source = opts.Source
		}
		if buildOpts.ImagePlatform == "" {
			buildOpts.ImagePlatform = opts.ImagePlatform
		}
		if buildOpts.Parallelism <= 0 {
			buildOpts.Parallelism = opts.BuildParallelism
		}
		if buildOpts.Out == nil {
			buildOpts.Out = out
		}
		// Dry-run must not require Docker/registry unless a test injects a probe.
		if opts.DryRun && buildOpts.RegistryHasImage == nil {
			buildOpts.SkipRegistryProbe = true
		}
		if err := rewriteBuiltTargetsToTags(plan); err != nil {
			return core.WrapWithBase(core.ErrUpdateBuildFailed, err, err.Error())
		}
		actions, err := planImageBuilds(ctx, plan, buildOpts)
		if err != nil {
			return core.WrapWithBase(core.ErrUpdateBuildFailed, err, err.Error())
		}
		plan.ImageBuilds = actions
	}

	if !jsonOut {
		writePlanText(out, plan)
	}

	changed := plan.Changed()
	if opts.DryRun || !plan.NeedsApply() {
		if !jsonOut {
			if !plan.NeedsApply() {
				fmt.Fprintln(out, "\nPlatform is up to date; nothing to roll out.")
			} else {
				n := len(changed)
				extra := ""
				if plan.ApplyCRDs {
					extra = fmt.Sprintf(" and %d CustomResourceDefinition(s)", len(plan.CRDNames))
				}
				buildNote := ""
				if opts.Build {
					var builds, reuses int
					for _, a := range plan.ImageBuilds {
						if a.Action == ImageActionBuild {
							builds++
						} else {
							reuses++
						}
					}
					buildNote = fmt.Sprintf(" (%d image build(s), %d reuse)", builds, reuses)
				}
				fmt.Fprintf(out, "\nDry run (plan only; no CRD apply, builds, or rollouts were attempted): %d component(s)%s%s would be updated. Re-run without --dry-run to apply.\n", n, extra, buildNote)
			}
		}
		return report(nil)
	}

	if !opts.Yes {
		if !jsonOut {
			writeConfirmSummary(out, plan, opts.Build)
		}
		ok, err := d.confirm(d.stdin, out, kh.Cluster)
		if err != nil {
			return err
		}
		if !ok {
			return core.NewWithBase(core.ErrUpdateAborted, "update aborted; no changes were made")
		}
	}

	progress := func(msg string) {
		if !jsonOut {
			fmt.Fprintln(out, msg)
		}
	}
	if opts.Build {
		buildOpts.Progress = progress
		buildOpts.SkipRegistryProbe = false
		if err := buildAndPushChanged(ctx, plan.ImageBuilds, buildOpts); err != nil {
			return err
		}
	}
	res := Apply(ctx, kh.Clientset, plan, ApplyOptions{
		Timeout:           opts.Timeout,
		RollbackOnFailure: opts.RollbackOnFailure,
		Waiter:            d.waiter(kh.Clientset),
		Clients:           kh.Clients,
		Progress:          progress,
	})
	if err := report(res); err != nil {
		return err
	}
	if res.Failed {
		return core.NewWithBase(core.ErrUpdateRolloutFailed, "platform update failed; see per-workload status and recovery commands above")
	}
	return nil
}

func validateOptions(opts Options) error {
	if strings.TrimSpace(opts.To) == "" && strings.TrimSpace(opts.ReleaseManifest) == "" {
		return core.NewWithBase(core.ErrUpdateTargetRequired, "no update target: pass --to <version> or --release-manifest <path|https-url>; update never picks a release implicitly")
	}
	if opts.To != "" {
		if _, err := platformrelease.ParseVersion(opts.To); err != nil {
			return core.NewWithBase(core.ErrUpdateInvalidFlag, fmt.Sprintf("--to: %v", err))
		}
	}
	if opts.Output != "text" && opts.Output != "json" {
		return core.NewWithBase(core.ErrUpdateInvalidFlag, fmt.Sprintf("--output must be text or json, got %q", opts.Output))
	}
	if opts.Timeout <= 0 {
		return core.NewWithBase(core.ErrUpdateInvalidFlag, "--timeout must be positive")
	}
	if opts.Build {
		if _, err := resolveSourceDir(opts.Source); err != nil {
			return core.WrapWithBase(core.ErrUpdateInvalidFlag, err, fmt.Sprintf("--source: %v", err))
		}
		if opts.BuildParallelism <= 0 {
			return core.NewWithBase(core.ErrUpdateInvalidFlag, "--build-parallelism must be positive")
		}
	}
	return nil
}

func resolveManifest(ctx context.Context, opts Options, d deps) (*platformrelease.Manifest, string, error) {
	source := strings.TrimSpace(opts.ReleaseManifest)
	if source == "" {
		source = platformrelease.ReleaseManifestURL(opts.To)
	}
	data, err := d.loadManifest(ctx, source)
	if err != nil {
		return nil, source, core.WrapWithBase(core.ErrUpdateManifestInvalid, err, fmt.Sprintf("load release manifest: %v", err))
	}
	m, err := platformrelease.ParseManifest(data)
	if err != nil {
		return nil, source, core.WrapWithBase(core.ErrUpdateManifestInvalid, err, fmt.Sprintf("release manifest %s: %v", source, err))
	}
	if opts.To != "" && m.Version != opts.To {
		return nil, source, core.NewWithBase(core.ErrUpdateTargetMismatch, fmt.Sprintf("release manifest %s is for %s but --to is %s; refusing to change the target silently", source, m.Version, opts.To))
	}
	if err := ensureManifestCRDs(ctx, m, opts, d); err != nil {
		return nil, source, err
	}
	return m, source, nil
}

// ensureManifestCRDs fills m.CRDs when the release changes CRDs but the JSON
// omitted the embedded bundle. Order: --crds, then platform-crds.yaml for --to.
func ensureManifestCRDs(ctx context.Context, m *platformrelease.Manifest, opts Options, d deps) error {
	if m == nil || !m.CRDChange || strings.TrimSpace(m.CRDs) != "" {
		return nil
	}
	sources := make([]string, 0, 2)
	if s := strings.TrimSpace(opts.CRDs); s != "" {
		sources = append(sources, s)
	}
	if opts.To != "" {
		sources = append(sources, platformrelease.ReleaseCRDsURL(opts.To))
	}
	var last error
	for _, src := range sources {
		data, err := d.loadManifest(ctx, src)
		if err != nil {
			last = err
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			last = fmt.Errorf("empty CRD bundle at %s", src)
			continue
		}
		m.CRDs = text
		return nil
	}
	if last != nil {
		return core.WrapWithBase(core.ErrUpdateCRDChange, last, fmt.Sprintf("release %s changes CustomResourceDefinitions but no CRD bundle was found (embed crds in platform-manifest.json, pass --crds, or publish %s on the release): %v", m.Version, platformrelease.CRDsAssetName, last))
	}
	return core.NewWithBase(core.ErrUpdateCRDChange, fmt.Sprintf("release %s changes CustomResourceDefinitions but the manifest has no embedded crds; pass --crds <path|https-url> or publish %s on the release", m.Version, platformrelease.CRDsAssetName))
}

// kubeClient builds clients from kubeconfig and reports the context,
// API server, and cluster ID (kube-system namespace UID).
func kubeClient(kubeconfig, kubeContext string) (kubeHandle, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext})
	raw, err := cfg.RawConfig()
	if err != nil {
		return kubeHandle{}, err
	}
	info := ClusterInfo{Context: kubeContext}
	if info.Context == "" {
		info.Context = raw.CurrentContext
	}
	if kc, ok := raw.Contexts[info.Context]; ok {
		if cl, ok := raw.Clusters[kc.Cluster]; ok {
			info.Server = cl.Server
		}
	}
	restCfg, err := cfg.ClientConfig()
	if err != nil {
		return kubeHandle{Cluster: info}, err
	}
	if info.Server == "" {
		info.Server = restCfg.Host
	}
	clients, err := k8sclient.NewFromConfig(restCfg)
	if err != nil {
		return kubeHandle{Cluster: info}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ns, err := clients.Clientset.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return kubeHandle{Cluster: info}, fmt.Errorf("read cluster identity (kube-system namespace) on %s: %w", info.Server, err)
	}
	info.ClusterID = string(ns.UID)
	return kubeHandle{Clientset: clients.Clientset, Clients: clients, Cluster: info}, nil
}

func promptConfirm(in io.Reader, out io.Writer, cluster ClusterInfo) (bool, error) {
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) { // #nosec G115 -- file descriptors fit in int.
		return false, core.NewWithBase(core.ErrUpdateConfirmationMissing, "refusing to update without confirmation: stdin is not a terminal; review the plan (or --dry-run) and pass --yes")
	}
	fmt.Fprintf(out, "\nApply this update to context %q (cluster %s)? Type 'yes' to continue: ", cluster.Context, cluster.ClusterID)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	return strings.TrimSpace(line) == "yes", nil
}

func writeJSON(out io.Writer, plan *Plan, res *Result, dryRun bool) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		DryRun bool    `json:"dryRun"`
		Plan   *Plan   `json:"plan"`
		Result *Result `json:"result,omitempty"`
	}{DryRun: dryRun, Plan: plan, Result: res})
}
