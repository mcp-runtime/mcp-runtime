package credentialapi

// Cloud secret readers (GCP, AWS, Azure, Vault). UNTESTED REFERENCE CODE, not
// verified against any live provider. DISABLED BY DESIGN: only the
// kubernetes provider is supported today. cb.ProviderSupported rejects these
// kinds at source creation, cb.Registry refuses to register or return them, and
// providers() never calls cloudReaders. They are kept so that enabling a
// provider later is a small change; tests exercise them directly.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"golang.org/x/oauth2/google"
	cb "mcp-runtime/pkg/credentialbroker"
)

func cloudReaders(httpClient *http.Client) map[string]cb.Provider {
	return map[string]cb.Provider{
		"gcp": providerFunc(func(ctx context.Context, s cb.Source) (cb.Value, error) {
			tokens, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
			if err != nil {
				return cb.Value{}, cb.ErrUnavailable
			}
			token, err := tokens.Token()
			if err != nil {
				return cb.Value{}, cb.ErrUnavailable
			}
			version := s.Version
			if version == "" {
				version = "latest"
			}
			var response struct {
				Name    string `json:"name"`
				Payload struct {
					Data string `json:"data"`
				} `json:"payload"`
			}
			if err = providerJSON(ctx, httpClient, "https://secretmanager.googleapis.com/v1/"+s.Resource+"/versions/"+version+":access", "Authorization", "Bearer "+token.AccessToken, &response); err != nil {
				return cb.Value{}, err
			}
			value, err := base64.StdEncoding.DecodeString(response.Payload.Data)
			if err != nil {
				return cb.Value{}, cb.ErrUnavailable
			}
			return checked(value, response.Name)
		}),
		"aws": providerFunc(func(ctx context.Context, s cb.Source) (cb.Value, error) {
			parts := strings.Split(s.Resource, ":")
			if len(parts) < 7 {
				return cb.Value{}, cb.ErrUnavailable
			}
			config, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(parts[3]), awsconfig.WithHTTPClient(awsSecretHTTPClient{httpClient}))
			if err != nil {
				return cb.Value{}, cb.ErrUnavailable
			}
			input := &secretsmanager.GetSecretValueInput{SecretId: &s.Resource}
			if s.Version != "" {
				input.VersionStage = &s.Version
			}
			response, err := secretsmanager.NewFromConfig(config).GetSecretValue(ctx, input)
			if err != nil {
				return cb.Value{}, cb.ErrUnavailable
			}
			value := response.SecretBinary
			if response.SecretString != nil {
				value = []byte(*response.SecretString)
			}
			version := ""
			if response.VersionId != nil {
				version = *response.VersionId
			}
			return checked(value, version)
		}),
		"azure": providerFunc(func(ctx context.Context, s cb.Source) (cb.Value, error) {
			identity, err := azidentity.NewDefaultAzureCredential(nil)
			if err != nil {
				return cb.Value{}, cb.ErrUnavailable
			}
			return azureValue(ctx, s, identity, httpClient)
		}),
		"vault": providerFunc(func(ctx context.Context, s cb.Source) (cb.Value, error) {
			base, err := url.Parse(os.Getenv("CREDENTIAL_VAULT_ADDR"))
			if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Path != "" {
				return cb.Value{}, cb.ErrUnavailable
			}
			tokenFile := os.Getenv("CREDENTIAL_VAULT_KUBERNETES_TOKEN_FILE")
			if tokenFile == "" {
				return cb.Value{}, cb.ErrUnavailable
			}
			jwt, err := os.ReadFile(tokenFile) // #nosec G304 G703 -- administrator-selected projected token, never API data.
			if err != nil || len(jwt) > 16<<10 {
				return cb.Value{}, cb.ErrUnavailable
			}
			role := os.Getenv("CREDENTIAL_VAULT_ROLE")
			if !cb.NameValid(role) {
				return cb.Value{}, cb.ErrUnavailable
			}
			payload, _ := json.Marshal(map[string]string{"role": role, "jwt": strings.TrimSpace(string(jwt))})
			var login struct {
				Auth struct {
					ClientToken string `json:"client_token"`
				} `json:"auth"`
			}
			if err = providerRequest(ctx, httpClient, http.MethodPost, base.String()+"/v1/auth/kubernetes/login", "", "", payload, &login); err != nil || login.Auth.ClientToken == "" {
				return cb.Value{}, cb.ErrUnavailable
			}
			parts := strings.SplitN(s.Resource, "/", 2)
			if len(parts) != 2 {
				return cb.Value{}, cb.ErrUnavailable
			}
			endpoint := base.String() + "/v1/" + parts[0] + "/data/" + parts[1]
			if s.Version != "" {
				endpoint += "?version=" + s.Version
			}
			var response struct {
				Data struct {
					Data     map[string]json.RawMessage `json:"data"`
					Metadata struct {
						Version      json.Number `json:"version"`
						Destroyed    bool        `json:"destroyed"`
						DeletionTime string      `json:"deletion_time"`
					} `json:"metadata"`
				} `json:"data"`
			}
			if err = providerJSON(ctx, httpClient, endpoint, "X-Vault-Token", login.Auth.ClientToken, &response); err != nil {
				return cb.Value{}, err
			}
			if response.Data.Metadata.Destroyed || response.Data.Metadata.DeletionTime != "" {
				return cb.Value{}, cb.ErrUnavailable
			}
			var value string
			if json.Unmarshal(response.Data.Data[s.Key], &value) != nil {
				return cb.Value{}, cb.ErrUnavailable
			}
			return checked([]byte(value), response.Data.Metadata.Version.String())
		}),
	}
}

type awsSecretHTTPClient struct{ client *http.Client }

func (c awsSecretHTTPClient) Do(request *http.Request) (*http.Response, error) {
	// Preserve native metadata credential discovery, but never resolve a
	// SecretString/SecretBinary through an insecure SDK endpoint override.
	if request.Header.Get("X-Amz-Target") == "secretsmanager.GetSecretValue" && request.URL.Scheme != "https" {
		return nil, cb.ErrUnavailable
	}
	return c.client.Do(request) // #nosec G704 -- official AWS SDK endpoints; secret-value requests require HTTPS above, and source metadata is an exact ARN, never a URL.
}

func azureValue(ctx context.Context, s cb.Source, identity azcore.TokenCredential, httpClient *http.Client) (cb.Value, error) {
	u, err := url.Parse(s.Resource)
	if err != nil {
		return cb.Value{}, cb.ErrUnavailable
	}
	client, err := azsecrets.NewClient("https://"+u.Host, identity, &azsecrets.ClientOptions{ClientOptions: azcore.ClientOptions{Transport: httpClient}})
	if err != nil {
		return cb.Value{}, cb.ErrUnavailable
	}
	response, err := client.GetSecret(ctx, strings.TrimPrefix(u.Path, "/secrets/"), s.Version, nil) // #nosec G704 -- validated exact Azure Key Vault HTTPS reference; SDK scoped token and redirect refusal.
	if err != nil || response.Value == nil {
		return cb.Value{}, cb.ErrUnavailable
	}
	version := ""
	if response.ID != nil {
		version = string(*response.ID)
	}
	return checked([]byte(*response.Value), version)
}

func providerJSON(ctx context.Context, c *http.Client, endpoint, header, token string, out any) error {
	return providerRequest(ctx, c, http.MethodGet, endpoint, header, token, nil, out)
}

func providerRequest(ctx context.Context, c *http.Client, method, endpoint, header, token string, body []byte, out any) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return cb.ErrUnavailable
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body)) // #nosec G704 -- fixed cloud endpoint or administrator-configured Vault origin; exact source validation, HTTPS and no redirects.
	if err != nil {
		return cb.ErrUnavailable
	}
	if header != "" {
		r.Header.Set(header, token)
	}
	if len(body) > 0 {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(r) // #nosec G704 -- fixed cloud endpoint or administrator-configured Vault origin; HTTPS and redirects refused.
	if err != nil {
		return cb.ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cb.ErrUnavailable
	}
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 2*cb.MaxValueBytes+1))
	if err != nil || len(responseBody) > 2*cb.MaxValueBytes || json.Unmarshal(responseBody, out) != nil {
		return cb.ErrUnavailable
	}
	return nil
}
