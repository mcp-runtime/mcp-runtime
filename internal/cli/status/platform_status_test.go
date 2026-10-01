package status_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/pterm/pterm"
	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformstatus"
	"mcp-runtime/internal/cli/status"
)

type commandResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
}

type helperProcessCommand struct {
	cmd *exec.Cmd
}

func (c helperProcessCommand) Output() ([]byte, error)         { return c.cmd.Output() }
func (c helperProcessCommand) CombinedOutput() ([]byte, error) { return c.cmd.CombinedOutput() }
func (c helperProcessCommand) Run() error                      { return c.cmd.Run() }
func (c helperProcessCommand) SetStdout(w io.Writer)           { c.cmd.Stdout = w }
func (c helperProcessCommand) SetStderr(w io.Writer)           { c.cmd.Stderr = w }
func (c helperProcessCommand) SetStdin(r io.Reader)            { c.cmd.Stdin = r }

type helperProcessExecutor struct {
	command func(string, ...string) *exec.Cmd
}

func (e helperProcessExecutor) Command(name string, args []string, validators ...core.ExecValidator) (core.Command, error) {
	spec := core.ExecSpec{Name: name, Args: args}
	for _, validate := range validators {
		if err := validate(spec); err != nil {
			return nil, err
		}
	}
	return helperProcessCommand{cmd: e.command(name, args...)}, nil
}

func commandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

func fakeExecCommand(t *testing.T, base func(string, ...string) *exec.Cmd, responses map[string]commandResponse, calls *[]string) func(string, ...string) *exec.Cmd {
	t.Helper()
	return func(name string, args ...string) *exec.Cmd {
		if calls != nil {
			*calls = append(*calls, commandKey(name, args...))
		}
		cmd := base(os.Args[0], "-test.run=TestHelperProcess", "--", name)
		cmd.Args = append(cmd.Args, args...)
		payload, err := json.Marshal(responses)
		if err != nil {
			t.Fatalf("failed to marshal responses: %v", err)
		}
		cmd.Env = append(os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			"MCP_RUNTIME_TEST_COMMANDS="+string(payload),
		)
		return cmd
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	raw := os.Getenv("MCP_RUNTIME_TEST_COMMANDS")
	if raw == "" {
		_, _ = os.Stderr.WriteString("missing MCP_RUNTIME_TEST_COMMANDS\n")
		os.Exit(1)
	}

	var responses map[string]commandResponse
	if err := json.Unmarshal([]byte(raw), &responses); err != nil {
		_, _ = os.Stderr.WriteString("invalid MCP_RUNTIME_TEST_COMMANDS\n")
		os.Exit(1)
	}

	args := os.Args
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep == -1 || sep == len(args)-1 {
		_, _ = os.Stderr.WriteString("missing command args\n")
		os.Exit(1)
	}

	cmdArgs := args[sep+1:]
	key := strings.Join(cmdArgs, " ")
	response, ok := responses[key]
	if !ok {
		_, _ = os.Stderr.WriteString("unexpected command: " + key + "\n")
		os.Exit(1)
	}

	if response.Stdout != "" {
		_, _ = os.Stdout.WriteString(response.Stdout)
	}
	if response.Stderr != "" {
		_, _ = os.Stderr.WriteString(response.Stderr)
	}
	if response.ExitCode != 0 {
		os.Exit(response.ExitCode)
	}
	os.Exit(0)
}

func resetStatusTestConfig(t *testing.T) {
	t.Helper()
	orig := core.DefaultCLIConfig
	core.DefaultCLIConfig = &core.CLIConfig{}
	t.Cleanup(func() {
		core.DefaultCLIConfig = orig
	})
	t.Setenv("HOME", t.TempDir())
}

func setDefaultPrinterWriter(t *testing.T, w *bytes.Buffer) {
	t.Helper()
	orig := core.DefaultPrinter.Writer
	core.DefaultPrinter.Writer = w
	t.Cleanup(func() {
		core.DefaultPrinter.Writer = orig
	})
}

func runShowPlatformStatus(t *testing.T, responses map[string]commandResponse) string {
	t.Helper()
	return runShowPlatformStatusWithCalls(t, responses, nil)
}

func runShowPlatformStatusWithCalls(t *testing.T, responses map[string]commandResponse, calls *[]string) string {
	t.Helper()

	logger := zap.NewNop()
	kubectl := core.NewTestKubectlClient(helperProcessExecutor{
		command: fakeExecCommand(t, exec.Command, responses, calls),
	})

	var buf bytes.Buffer
	pterm.SetDefaultOutput(&buf)
	pterm.DisableStyling()
	setDefaultPrinterWriter(t, &buf)
	t.Cleanup(func() {
		pterm.SetDefaultOutput(os.Stdout)
		pterm.EnableStyling()
	})

	if err := status.ShowPlatformStatus(logger, kubectl); err != nil {
		t.Fatalf("ShowPlatformStatus() unexpected error = %v", err)
	}

	return buf.String()
}

func TestShowPlatformStatus(t *testing.T) {
	t.Run("marks-operator-pending-when-replicas-start-with-zero", func(t *testing.T) {
		resetStatusTestConfig(t)

		responses := map[string]commandResponse{
			commandKey("kubectl", "cluster-info"): {Stdout: "cluster ok\n"},
			commandKey("kubectl", "get", "deployment", "registry", "-n", "registry", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "deployment", "mcp-runtime-operator-controller-manager", "-n", "mcp-runtime", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "0/1",
			},
			commandKey("kubectl", "get", "namespace", "mcp-platform", "-o", "jsonpath={.metadata.name}"): {
				Stdout:   "Error from server (NotFound): namespaces \"mcp-platform\" not found\n",
				ExitCode: 1,
			},
			commandKey("kubectl", "get", "mcpserver", "--all-namespaces", "-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,IMAGE:.spec.image,REPLICAS:.spec.replicas,PATH:.spec.ingressPath"): {},
		}

		output := runShowPlatformStatus(t, responses)
		if !strings.Contains(output, "PENDING") {
			t.Fatalf("expected operator status to be PENDING, got output: %s", output)
		}
		if !strings.Contains(output, "Ready: 0/1") {
			t.Fatalf("expected operator replica details, got output: %s", output)
		}
		if !strings.Contains(output, "Analytics Stack") || !strings.Contains(output, "SKIPPED") {
			t.Fatalf("expected analytics stack to be reported as skipped, got output: %s", output)
		}
	})

	t.Run("shows-setup-hint-when-cluster-missing", func(t *testing.T) {
		resetStatusTestConfig(t)

		responses := map[string]commandResponse{
			commandKey("kubectl", "cluster-info"): {
				Stderr:   "exec: \"kubectl\": executable file not found in $PATH\n",
				ExitCode: 127,
			},
		}

		output := runShowPlatformStatus(t, responses)
		lower := strings.ToLower(output)
		if !strings.Contains(lower, "kubectl is missing") {
			t.Fatalf("expected kubectl missing hint when cluster missing, got output: %s", output)
		}
		if !strings.Contains(lower, "install kubectl") {
			t.Fatalf("expected install guidance in output, got: %s", output)
		}
	})

	t.Run("surfaces external registry config errors instead of falling back to in-cluster registry", func(t *testing.T) {
		resetStatusTestConfig(t)
		core.DefaultCLIConfig = &core.CLIConfig{ProvisionedRegistryUsername: "user-only"}

		var calls []string
		responses := map[string]commandResponse{
			commandKey("kubectl", "cluster-info"): {Stdout: "cluster ok\n"},
			commandKey("kubectl", "get", "deployment", "mcp-runtime-operator-controller-manager", "-n", "mcp-runtime", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "namespace", "mcp-platform", "-o", "jsonpath={.metadata.name}"): {
				Stdout:   "Error from server (NotFound): namespaces \"mcp-platform\" not found\n",
				ExitCode: 1,
			},
			commandKey("kubectl", "get", "mcpserver", "--all-namespaces", "-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,IMAGE:.spec.image,REPLICAS:.spec.replicas,PATH:.spec.ingressPath"): {},
		}

		output := runShowPlatformStatusWithCalls(t, responses, &calls)
		if !strings.Contains(output, "registry url is required") {
			t.Fatalf("expected registry config error in output, got: %s", output)
		}
		for _, call := range calls {
			if strings.Contains(call, "get deployment registry") {
				t.Fatalf("did not expect registry deployment probe when config is invalid, got calls: %v", calls)
			}
		}
	})

	t.Run("lists-analytics-services-when-installed", func(t *testing.T) {
		resetStatusTestConfig(t)
		var calls []string

		responses := map[string]commandResponse{
			commandKey("kubectl", "cluster-info"): {Stdout: "cluster ok\n"},
			commandKey("kubectl", "get", "deployment", "registry", "-n", "registry", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "deployment", "mcp-runtime-operator-controller-manager", "-n", "mcp-runtime", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "namespace", "mcp-platform", "-o", "jsonpath={.metadata.name}"): {
				Stdout: "mcp-platform",
			},
			commandKey("kubectl", "get", "statefulset", "clickhouse", "-n", "mcp-observability", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "statefulset", "kafka", "-n", "mcp-observability", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "3/3",
			},
			commandKey("kubectl", "get", "deployment", "mcp-ingest", "-n", "mcp-observability", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "2/2",
			},
			commandKey("kubectl", "get", "deployment", "mcp-processor", "-n", "mcp-observability", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "deployment", "mcp-platform-api", "-n", "mcp-platform", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "deployment", "mcp-runtime-api", "-n", "mcp-platform", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "deployment", "mcp-analytics-api", "-n", "mcp-observability", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "deployment", "mcp-ui", "-n", "mcp-platform", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "deployment", "mcp-platform-gateway", "-n", "mcp-platform", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "deployment", "prometheus", "-n", "mcp-observability", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "deployment", "grafana", "-n", "mcp-observability", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "deployment", "otel-collector", "-n", "mcp-observability", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "statefulset", "tempo", "-n", "mcp-observability", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "statefulset", "loki", "-n", "mcp-observability", "-o", "jsonpath={.status.readyReplicas}/{.spec.replicas}"): {
				Stdout: "1/1",
			},
			commandKey("kubectl", "get", "daemonset", "promtail", "-n", "mcp-log-collector", "-o", "jsonpath={.status.numberReady}/{.status.desiredNumberScheduled}"): {
				Stdout: "3/3",
			},
			commandKey("kubectl", "get", "mcpserver", "--all-namespaces", "-o", "custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,IMAGE:.spec.image,REPLICAS:.spec.replicas,PATH:.spec.ingressPath"): {},
		}

		output := runShowPlatformStatusWithCalls(t, responses, &calls)
		for _, component := range []string{"ClickHouse", "Ingest", "Gateway", "Promtail"} {
			if !strings.Contains(output, component) {
				t.Fatalf("expected %s in output, got: %s", component, output)
			}
		}

		foundPromtail := false
		for _, call := range calls {
			if strings.Contains(call, "get daemonset promtail") {
				foundPromtail = true
				break
			}
		}
		if !foundPromtail {
			t.Fatalf("expected daemonset readiness check for promtail, got calls: %v", calls)
		}
	})
}

func TestAnalyticsNamespaceInstalledRequiresExactMatch(t *testing.T) {
	resetStatusTestConfig(t)

	responses := map[string]commandResponse{
		commandKey("kubectl", "get", "namespace", "mcp-platform", "-o", "jsonpath={.metadata.name}"): {
			Stdout: "unexpected-namespace",
		},
	}

	kubectl := core.NewTestKubectlClient(helperProcessExecutor{
		command: fakeExecCommand(t, exec.Command, responses, nil),
	})

	installed, err := platformstatus.AnalyticsNamespaceInstalled(kubectl, true)
	if err != nil {
		t.Fatalf("AnalyticsNamespaceInstalled() unexpected error = %v", err)
	}
	if installed {
		t.Fatal("expected namespace check to fail on mismatched namespace name")
	}
}

func TestAnalyticsNamespaceInstalledReturnsErrorOnEmptyFailure(t *testing.T) {
	resetStatusTestConfig(t)

	responses := map[string]commandResponse{
		commandKey("kubectl", "get", "namespace", "mcp-platform", "-o", "jsonpath={.metadata.name}"): {
			ExitCode: 1,
		},
	}

	kubectl := core.NewTestKubectlClient(helperProcessExecutor{
		command: fakeExecCommand(t, exec.Command, responses, nil),
	})

	installed, err := platformstatus.AnalyticsNamespaceInstalled(kubectl, true)
	if err == nil {
		t.Fatal("expected empty namespace probe failure to surface an error")
	}
	if installed {
		t.Fatal("expected namespace check to report not installed")
	}
	if !strings.Contains(err.Error(), "empty output from namespace probe") {
		t.Fatalf("expected empty-output error, got %v", err)
	}
}
