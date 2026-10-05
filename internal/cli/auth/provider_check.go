package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/cli/core"
)

type providerMetadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

func providerCheck(ctx context.Context, issuer string, client *http.Client) (providerMetadata, error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return providerMetadata{}, fmt.Errorf("OIDC issuer must be an absolute HTTPS URL")
	}
	metadataURL := issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return providerMetadata{}, fmt.Errorf("create discovery request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return providerMetadata{}, fmt.Errorf("OIDC discovery request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return providerMetadata{}, fmt.Errorf("OIDC discovery returned HTTP %d", resp.StatusCode)
	}
	var metadata providerMetadata
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		return providerMetadata{}, fmt.Errorf("decode OIDC discovery: %w", err)
	}
	if metadata.Issuer == "" || metadata.AuthorizationEndpoint == "" || metadata.TokenEndpoint == "" || metadata.JWKSURI == "" {
		return providerMetadata{}, fmt.Errorf("OIDC discovery is missing issuer, authorization_endpoint, token_endpoint, or jwks_uri")
	}
	if strings.TrimRight(metadata.Issuer, "/") != issuer {
		return providerMetadata{}, fmt.Errorf("OIDC discovery issuer %q does not match %q", metadata.Issuer, issuer)
	}
	for name, endpoint := range map[string]string{
		"authorization_endpoint": metadata.AuthorizationEndpoint,
		"token_endpoint":         metadata.TokenEndpoint,
		"jwks_uri":               metadata.JWKSURI,
	} {
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return providerMetadata{}, fmt.Errorf("OIDC %s must be an absolute HTTPS URL", name)
		}
	}
	return metadata, nil
}

func newProviderCheckCmd() *cobra.Command {
	var issuer string
	cmd := &cobra.Command{
		Use:   "provider-check",
		Short: "Check an OIDC provider before mcp-auth setup",
		RunE: func(cmd *cobra.Command, _ []string) error {
			issuer = strings.TrimSpace(issuer)
			if issuer == "" {
				return core.NewWithBase(core.ErrAuthAPIURLRequired, "OIDC issuer URL is required (pass --issuer-url)")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			metadata, err := providerCheck(ctx, issuer, &http.Client{Timeout: 15 * time.Second})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "OIDC provider is compatible with mcp-auth\nissuer: %s\nauthorization_endpoint: %s\ntoken_endpoint: %s\njwks_uri: %s\n", metadata.Issuer, metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.JWKSURI)
			return nil
		},
	}
	cmd.Flags().StringVar(&issuer, "issuer-url", "", "OIDC issuer URL (HTTPS; discovery is fetched from /.well-known/openid-configuration)")
	return cmd
}
