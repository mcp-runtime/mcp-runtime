// Package credential owns metadata-only secret broker management. It never
// resolves or prints provider secret values.
package credential

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"mcp-runtime/internal/cli/platformapi"
	cb "mcp-runtime/pkg/credentialbroker"
)

func New() *cobra.Command {
	cmd := &cobra.Command{Use: "credential", Short: "Manage scoped credential sources and bindings", Long: "Manage metadata-only credential sources and per-tool bindings. Secret values stay in the backing provider and are delivered only to authorized server gateways."}
	for _, kind := range []string{"sources", "bindings"} {
		group := &cobra.Command{Use: kind[:len(kind)-1], Short: "Manage credential " + kind}
		group.AddCommand(applyCommand(kind), readCommand(kind, false), readCommand(kind, true), deleteCommand(kind))
		if kind == "sources" {
			group.AddCommand(authorizeKubernetesCommand())
		}
		cmd.AddCommand(group)
	}
	cmd.AddCommand(configureCommand())
	return cmd
}

func applyCommand(kind string) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "apply", Short: "Create or update metadata from a JSON file", Long: "Create with revision 0; update with the revision returned by get. Unknown fields and raw credential values are rejected. Sources require a platform administrator; bindings require namespace ownership and source authorization.", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if file == "" {
			return errors.New("--file is required")
		}
		var r io.Reader = cmd.InOrStdin()
		var f *os.File
		var err error
		if file != "-" {
			f, err = os.Open(file) // #nosec G304 -- explicitly selected local metadata file; raw values and unknown fields are rejected.
			if err != nil {
				return errors.New("credential metadata file unavailable")
			}
			defer f.Close()
			r = f
		}
		raw, err := io.ReadAll(io.LimitReader(r, 64<<10+1))
		if err != nil || len(raw) > 64<<10 {
			return errors.New("credential metadata exceeds 64 KiB")
		}
		var ns, name string
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if kind == "sources" {
			var source cb.Source
			if decoder.Decode(&source) != nil {
				return errors.New("invalid source metadata")
			}
			if err = source.Validate(); err != nil {
				return err
			}
			ns, name = source.Namespace, source.Name
		} else {
			var binding cb.Binding
			if decoder.Decode(&binding) != nil {
				return errors.New("invalid binding metadata")
			}
			if err = binding.Validate(); err != nil {
				return err
			}
			ns, name = binding.Namespace, binding.Name
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("exactly one metadata object required")
		}
		client, err := platformapi.NewPlatformClient()
		if err != nil {
			return err
		}
		result, err := client.CredentialMetadata(cmd.Context(), http.MethodPut, ns, kind, name, raw, 0)
		if err != nil {
			return err
		}
		return printMetadata(cmd, result)
	}}
	cmd.Flags().StringVar(&file, "file", "", "Metadata JSON file or - for stdin (never secret values)")
	return cmd
}

func readCommand(kind string, item bool) *cobra.Command {
	var ns string
	use, short := "list", "List metadata visible in a namespace"
	args := cobra.NoArgs
	if item {
		use, short = "get NAME", "Get redacted metadata by name"
		args = cobra.ExactArgs(1)
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
		name := ""
		if item {
			name = args[0]
		}
		client, err := platformapi.NewPlatformClient()
		if err != nil {
			return err
		}
		result, err := client.CredentialMetadata(cmd.Context(), http.MethodGet, ns, kind, name, nil, 0)
		if err != nil {
			return err
		}
		return printMetadata(cmd, result)
	}}
	cmd.Flags().StringVar(&ns, "namespace", "", "Managed namespace (required)")
	return cmd
}

func deleteCommand(kind string) *cobra.Command {
	var ns string
	var revision int64
	var yes bool
	cmd := &cobra.Command{Use: "delete NAME", Short: "Delete metadata at its current revision", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !yes || revision < 1 {
			return errors.New("--yes and --revision are required; remove bindings before deleting their source")
		}
		client, err := platformapi.NewPlatformClient()
		if err != nil {
			return err
		}
		result, err := client.CredentialMetadata(cmd.Context(), http.MethodDelete, ns, kind, args[0], nil, revision)
		if err != nil {
			return err
		}
		return printMetadata(cmd, result)
	}}
	cmd.Flags().StringVar(&ns, "namespace", "", "Managed namespace (required)")
	cmd.Flags().Int64Var(&revision, "revision", 0, "Exact revision returned by get")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm metadata deletion")
	return cmd
}

func printMetadata(cmd *cobra.Command, raw json.RawMessage) error {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return errors.New("invalid credential metadata response")
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), out.String())
	return err
}
