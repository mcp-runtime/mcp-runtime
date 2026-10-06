package status_test

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformstatus"
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

func TestAnalyticsNamespaceInstalledRequiresExactMatch(t *testing.T) {
	resetStatusTestConfig(t)

	responses := map[string]commandResponse{
		commandKey("kubectl", "get", "namespace", "mcp-observability", "-o", "jsonpath={.metadata.name}"): {
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
		commandKey("kubectl", "get", "namespace", "mcp-observability", "-o", "jsonpath={.metadata.name}"): {
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
