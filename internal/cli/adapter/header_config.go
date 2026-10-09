package adapter

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"mcp-runtime/internal/agentadapter"
)

type headerConfigFlags struct {
	configFile string
	headerEnv  []string
}

// proxyFileConfig contains source references only, never secret values.
type proxyFileConfig struct {
	AuthMode          string                                   `yaml:"authMode"`
	RuntimeURL        string                                   `yaml:"runtimeURL"`
	CredentialHeaders map[string]agentadapter.CredentialSource `yaml:"credentialHeaders"`
}

func bindHeaderConfigFlags(cmd *cobra.Command, f *identityFlags, h *headerConfigFlags) {
	cmd.Flags().StringVar(&f.authMode, "auth-mode", os.Getenv(agentadapter.EnvAuthMode),
		"Adapter authentication mode: certificate (default) or header for upstream-owned credential authentication (env MCP_RUNTIME_AUTH_MODE)")
	cmd.Flags().StringVar(&h.configFile, "config", "", "Adapter YAML file containing authMode, runtimeURL, and credentialHeaders source references")
	cmd.Flags().StringArrayVar(&h.headerEnv, "credential-header-env", nil,
		"Inject a credential from a local environment variable: Header-Name=ENV_NAME (repeatable; overrides that header's config source)")
}

func resolveHeaderConfig(cmd *cobra.Command, f identityFlags, h headerConfigFlags) (identityFlags, map[string]agentadapter.CredentialSource, error) {
	sources := map[string]agentadapter.CredentialSource{}
	if h.configFile != "" {
		file, err := readProxyFileConfig(h.configFile)
		if err != nil {
			return f, nil, err
		}
		if !cmd.Flags().Changed("auth-mode") && f.authMode == "" {
			f.authMode = file.AuthMode
		}
		if !cmd.Flags().Changed("runtime-url") && f.runtimeURL == "" {
			f.runtimeURL = file.RuntimeURL
		}
		// A file's credentials must not silently follow a URL override.
		if len(file.CredentialHeaders) > 0 && file.RuntimeURL != "" && f.runtimeURL != file.RuntimeURL {
			return f, nil, fmt.Errorf("runtime URL override conflicts with the credential config's target")
		}
		for name, source := range file.CredentialHeaders {
			if source.File != "" && !filepath.IsAbs(source.File) {
				source.File = filepath.Join(filepath.Dir(h.configFile), source.File)
			}
			sources[name] = source
		}
	}
	seenFlags := map[string]bool{}
	for _, entry := range h.headerEnv {
		name, env, ok := strings.Cut(entry, "=")
		if !ok {
			return f, nil, fmt.Errorf("credential-header-env requires Header-Name=ENV_NAME")
		}
		lower := strings.ToLower(name)
		if seenFlags[lower] {
			return f, nil, fmt.Errorf("duplicate credential-header-env name")
		}
		seenFlags[lower] = true
		for existing := range sources {
			if strings.EqualFold(existing, name) {
				delete(sources, existing)
			}
		}
		sources[name] = agentadapter.CredentialSource{Env: env}
	}
	if err := agentadapter.ValidateCredentialSources(sources); err != nil {
		return f, nil, err
	}
	return f, sources, nil
}

func readProxyFileConfig(path string) (proxyFileConfig, error) {
	var cfg proxyFileConfig
	f, err := os.Open(path) // #nosec G304 -- path is the user-selected adapter config file.
	if err != nil {
		return cfg, fmt.Errorf("cannot open adapter config file")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return cfg, fmt.Errorf("cannot read adapter config file or exceeds 1 MiB")
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		// Decoder errors may quote user data. Config contains references only,
		// but malformed files can accidentally contain an actual credential.
		return cfg, fmt.Errorf("invalid adapter config; use authMode, runtimeURL, and credentialHeaders with env or file references")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cfg, fmt.Errorf("adapter config must contain exactly one YAML document")
	}
	if err := agentadapter.ValidateCredentialSources(cfg.CredentialHeaders); err != nil {
		return cfg, err
	}
	return cfg, nil
}
