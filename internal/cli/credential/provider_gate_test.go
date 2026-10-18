package credential

import (
	"strings"
	"testing"
)

func TestConfigureRefusesCloudProviderFlagsAndHidesThem(t *testing.T) {
	for _, args := range [][]string{
		{"--gcp-service-account", "fixture@fixture-project.iam.gserviceaccount.com"},
		{"--aws-role-arn", "arn:aws:iam::123456789012:role/broker"},
		{"--azure-client-id", "11111111-1111-1111-1111-111111111111"},
		{"--vault-url", "https://vault.example.test", "--vault-role", "broker"},
	} {
		cmd := configureCommand()
		cmd.SetArgs(append([]string{"--broker-url", "https://platform.example.test", "--yes"}, args...))
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		err := cmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "not yet supported") {
			t.Errorf("%s: want a not yet supported error, got %v", args[0], err)
		}
	}
	for _, name := range []string{"gcp-service-account", "aws-role-arn", "azure-client-id", "vault-url", "vault-role"} {
		if f := configureCommand().Flags().Lookup(name); f == nil || !f.Hidden {
			t.Errorf("--%s must be hidden while the provider is unsupported", name)
		}
	}
}
