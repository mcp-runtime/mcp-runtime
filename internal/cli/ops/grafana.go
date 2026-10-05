package ops

import (
	"fmt"
	"os"
	"strings"

	"mcp-runtime/internal/cli/core"
)

// Grafana has two independent authentication layers. The platform ingress gate
// (platform-admin-auth) decides who may reach /grafana; Grafana then verifies
// its own persisted admin account. The check below runs inside the Grafana pod
// against 127.0.0.1, so it bypasses the ingress gate and exercises only the
// Grafana login layer. The configured password is read from the pod's own
// environment (sourced from mcp-grafana-credentials) and never crosses the CLI.
const (
	grafanaExecTarget = "deploy/grafana"
	grafanaContainer  = "grafana"
)

var (
	// grafanaProbeScript prints "health=<code> auth=<code>"; an empty code means
	// no HTTP response. Busybox wget -S writes response headers to stderr.
	grafanaProbeScript = oneLine(`code() { sed -n 's/^ *HTTP\/[0-9.]* \([0-9][0-9]*\).*/\1/p' | tail -n 1; }
base=http://127.0.0.1:3000/grafana
h=$(wget -q -S -O /dev/null "$base/api/health" 2>&1 | code)
cred=$(printf '%s:%s' "$GF_SECURITY_ADMIN_USER" "$GF_SECURITY_ADMIN_PASSWORD" | base64 | tr -d '\n')
a=$(wget -q -S -O /dev/null --header "Authorization: Basic $cred" "$base/api/user" 2>&1 | code)
echo "health=$h auth=$a"`)

	// grafanaBackupScript copies the persisted Grafana database (dashboards,
	// datasources, users) next to itself before any account change.
	grafanaBackupScript = oneLine(`set -e
dir=/var/lib/grafana/backups
mkdir -p "$dir"
dest="$dir/grafana.db.$(date +%Y%m%d%H%M%S)"
cp /var/lib/grafana/grafana.db "$dest"
echo "$dest"`)

	// grafanaResetScript pipes the configured password into the Grafana CLI over
	// stdin so it never appears in an argument list.
	grafanaResetScript = `printf '%s' "$GF_SECURITY_ADMIN_PASSWORD" | grafana cli --homepath /usr/share/grafana admin reset-admin-password --password-from-stdin`
)

// oneLine joins a multi-line shell script into a single line. The kubectl
// client rejects newlines, tabs and carriage returns in any argument, so a
// script passed to `sh -c` must not contain them.
func oneLine(script string) string {
	lines := strings.Split(strings.TrimSpace(script), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.Join(lines, "; ")
}

// GrafanaState classifies the result of the Grafana credential probe.
type GrafanaState string

const (
	// GrafanaAuthOK means the configured admin credentials authenticate.
	GrafanaAuthOK GrafanaState = "ok"
	// GrafanaCredentialDrift means Grafana is healthy but rejects the configured
	// credentials (persisted account differs from the Secret).
	GrafanaCredentialDrift GrafanaState = "drift"
	// GrafanaUnhealthy means Grafana did not answer its health endpoint.
	GrafanaUnhealthy GrafanaState = "unhealthy"
	// GrafanaUnknown means Grafana answered with an unexpected status.
	GrafanaUnknown GrafanaState = "unknown"
)

// GrafanaCheckResult is the parsed probe output.
type GrafanaCheckResult struct {
	State      GrafanaState
	HealthCode string
	AuthCode   string
}

// ParseGrafanaProbe parses the "health=<code> auth=<code>" probe output.
func ParseGrafanaProbe(output string) GrafanaCheckResult {
	res := GrafanaCheckResult{}
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, "health=") {
			continue
		}
		for _, field := range strings.Fields(line) {
			switch {
			case strings.HasPrefix(field, "health="):
				res.HealthCode = strings.TrimPrefix(field, "health=")
			case strings.HasPrefix(field, "auth="):
				res.AuthCode = strings.TrimPrefix(field, "auth=")
			}
		}
	}
	switch {
	case res.HealthCode != "200":
		res.State = GrafanaUnhealthy
	case res.AuthCode == "200":
		res.State = GrafanaAuthOK
	case res.AuthCode == "401":
		res.State = GrafanaCredentialDrift
	default:
		res.State = GrafanaUnknown
	}
	return res
}

func grafanaExecArgs(script string) []string {
	return []string{
		"exec", "-n", core.ComponentNamespace("grafana"), "-c", grafanaContainer,
		grafanaExecTarget, "--", "sh", "-c", script,
	}
}

// CheckGrafanaCredentials probes Grafana read-only and reports whether the
// persisted admin account accepts the configured credentials. It never resets
// or modifies an account.
func (m *Manager) CheckGrafanaCredentials() (GrafanaCheckResult, error) {
	if err := m.requireAdminClusterAccess(); err != nil {
		return GrafanaCheckResult{}, err
	}
	out, err := m.kubectl.CombinedOutput(grafanaExecArgs(grafanaProbeScript))
	if err != nil {
		return GrafanaCheckResult{}, core.WrapWithBaseAndContext(nil, err, fmt.Sprintf("failed to probe Grafana in %s/%s: %v", core.ComponentNamespace("grafana"), grafanaExecTarget, err), map[string]any{
			"namespace": core.ComponentNamespace("grafana"),
			"component": "grafana",
		})
	}
	return ParseGrafanaProbe(string(out)), nil
}

// ShowGrafanaCheck prints the credential check and returns an error when the
// persisted credentials are not usable, so scripts can gate on the exit code.
func (m *Manager) ShowGrafanaCheck() error {
	res, err := m.CheckGrafanaCredentials()
	if err != nil {
		return err
	}
	printGrafanaResult(res)
	if res.State != GrafanaAuthOK {
		return core.NewWithBase(nil, grafanaStateMessage(res))
	}
	return nil
}

func printGrafanaResult(res GrafanaCheckResult) {
	core.Header("Grafana Credential Check")
	core.DefaultPrinter.Println()
	core.TableBoxed([][]string{
		{"Layer", "Check", "Result"},
		{"Platform ingress gate", "platform-admin-auth (not exercised here)", "see docs/platform-services.md"},
		{"Grafana health", "GET /grafana/api/health in pod", codeOrNone(res.HealthCode)},
		{"Grafana login", "GET /grafana/api/user with configured admin credentials in pod", codeOrNone(res.AuthCode)},
	})
	core.DefaultPrinter.Println(grafanaStateMessage(res))
}

func codeOrNone(code string) string {
	if code == "" {
		return "no response"
	}
	return code
}

func grafanaStateMessage(res GrafanaCheckResult) string {
	switch res.State {
	case GrafanaAuthOK:
		return "Grafana accepts the configured admin credentials."
	case GrafanaCredentialDrift:
		return "Grafana rejected the configured admin credentials (HTTP 401): the persisted admin account has drifted from mcp-grafana-credentials. The platform ingress gate may still admit you. After reviewing, recover deliberately with: mcp-runtime ops grafana reset-admin-password --yes"
	case GrafanaUnhealthy:
		return fmt.Sprintf("Grafana is not healthy (health=%s); credentials were not evaluated. Check: mcp-runtime ops logs grafana", codeOrNone(res.HealthCode))
	default:
		return fmt.Sprintf("Unexpected Grafana login response (auth=%s); credentials were not classified.", codeOrNone(res.AuthCode))
	}
}

// ResetGrafanaAdminPassword restores the persisted Grafana admin password to
// the configured value after a database backup. It is deliberate: it requires
// confirmation, refuses unless drift is detected, and verifies the result.
func (m *Manager) ResetGrafanaAdminPassword(confirmed bool) error {
	res, err := m.CheckGrafanaCredentials()
	if err != nil {
		return err
	}
	switch res.State {
	case GrafanaCredentialDrift:
	case GrafanaAuthOK:
		core.DefaultPrinter.Println("Grafana already accepts the configured admin credentials; nothing to reset.")
		return nil
	default:
		return core.NewWithBase(nil, "refusing to reset: "+grafanaStateMessage(res))
	}
	if !confirmed {
		return core.NewWithBase(nil, "Grafana admin credential drift detected. Re-run with --yes to back up the Grafana database and reset the persisted admin password to the value in mcp-grafana-credentials. Dashboards and datasources are preserved; any password an operator set in Grafana will be overwritten.")
	}

	backup, err := m.kubectl.Output(grafanaExecArgs(grafanaBackupScript))
	if err != nil {
		return core.WrapWithBaseAndContext(nil, err, fmt.Sprintf("failed to back up the Grafana database; password not changed: %v", err), map[string]any{
			"namespace": core.ComponentNamespace("grafana"),
			"component": "grafana",
		})
	}
	core.DefaultPrinter.Println("Grafana database backed up to " + strings.TrimSpace(string(backup)) + " (inside the grafana pod volume)")

	if err := m.kubectl.RunWithOutput(grafanaExecArgs(grafanaResetScript), os.Stdout, os.Stderr); err != nil {
		return core.WrapWithBaseAndContext(nil, err, fmt.Sprintf("failed to reset Grafana admin password: %v", err), map[string]any{
			"namespace": core.ComponentNamespace("grafana"),
			"component": "grafana",
		})
	}

	verify, err := m.CheckGrafanaCredentials()
	if err != nil {
		return err
	}
	if verify.State != GrafanaAuthOK {
		return core.NewWithBase(nil, "password reset ran but verification failed: "+grafanaStateMessage(verify))
	}
	core.DefaultPrinter.Println("Verified: Grafana accepts the configured admin credentials.")
	core.DefaultPrinter.Println("Store the working credentials and Grafana URL only in your private operator infra.env.")
	return nil
}
