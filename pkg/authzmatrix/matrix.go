package authzmatrix

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	RoleAnon      = "anon"
	RoleUserKey   = "user-key"
	RoleAdminKey  = "admin-key"
	RoleIngestKey = "ingest-key"
)

// Row is one authn/authz expectation from docs/security/authz-matrix.json.
type Row struct {
	Service             string `json:"service"`
	Path                string `json:"path"`
	Method              string `json:"method"`
	Role                string `json:"role"`
	Expect              int    `json:"expect,omitempty"`
	ExpectAuthenticated bool   `json:"expect_authenticated,omitempty"`
}

// Load reads the machine-readable authz matrix from path.
func Load(path string) ([]Row, error) {
	clean := filepath.Clean(path)
	dir := filepath.Dir(clean)
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid authz matrix path: %q", path)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open authz matrix root: %w", err)
	}
	defer root.Close()
	raw, err := root.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read authz matrix: %w", err)
	}
	var rows []Row
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode authz matrix: %w", err)
	}
	for i, row := range rows {
		if strings.TrimSpace(row.Service) == "" {
			return nil, fmt.Errorf("row %d: service is required", i)
		}
		if strings.TrimSpace(row.Path) == "" {
			return nil, fmt.Errorf("row %d: path is required", i)
		}
		if strings.TrimSpace(row.Method) == "" {
			return nil, fmt.Errorf("row %d: method is required", i)
		}
		if strings.TrimSpace(row.Role) == "" {
			return nil, fmt.Errorf("row %d: role is required", i)
		}
		if row.Expect <= 0 && !row.ExpectAuthenticated {
			return nil, fmt.Errorf("row %d: expect must be a positive HTTP status or expect_authenticated", i)
		}
	}
	return rows, nil
}

// Filter returns rows owned by service.
func Filter(rows []Row, service string) []Row {
	service = strings.TrimSpace(service)
	out := make([]Row, 0, len(rows))
	for _, row := range rows {
		if row.Service == service {
			out = append(out, row)
		}
	}
	return out
}

// ApplyRole sets request credentials for a matrix role using test key aliases.
func ApplyRole(req *http.Request, role string, keys map[string]string) {
	switch strings.TrimSpace(role) {
	case RoleAnon:
		return
	case RoleUserKey:
		if key := strings.TrimSpace(keys[RoleUserKey]); key != "" {
			req.Header.Set("x-api-key", key)
		}
	case RoleAdminKey:
		if key := strings.TrimSpace(keys[RoleAdminKey]); key != "" {
			req.Header.Set("x-api-key", key)
		}
	case RoleIngestKey:
		if key := strings.TrimSpace(keys[RoleIngestKey]); key != "" {
			req.Header.Set("x-api-key", key)
		}
	default:
		if token := strings.TrimSpace(keys[role]); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
}
