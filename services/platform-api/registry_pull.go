package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"mcp-runtime/pkg/registryauth"
)

var registryNamespacePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var registryRepositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)

func (s *apiServer) handleRegistryPullCredentials(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	w.Header().Set("Cache-Control", "no-store")
	if s.platform == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodPost:
		var scope registryauth.PullScope
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&scope); err != nil || !registryNamespacePattern.MatchString(scope.Namespace) || len(scope.Repositories) > 128 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// Prefix authority is derived here, never from caller-supplied JSON.
		if len(scope.Prefixes) != 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		scope.Prefixes = []string{scope.Namespace}
		if slug, ok := strings.CutPrefix(scope.Namespace, "mcp-team-"); ok && slug != "" {
			scope.Prefixes = append(scope.Prefixes, slug)
		}
		if scope.Namespace == "mcp-servers-public" {
			scope.Prefixes = append(scope.Prefixes, "public")
		}
		if scope.Namespace == "mcp-servers-org" {
			scope.Prefixes = append(scope.Prefixes, "org")
		}
		for _, repo := range scope.Repositories {
			if len(repo) > 255 || !registryRepositoryPattern.MatchString(repo) {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
		}
		record, err := s.platform.CreateRegistryPullCredential(r.Context(), scope)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, http.StatusCreated, record)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if !strings.HasPrefix(id, "rp_") || len(id) > 64 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err := s.platform.RevokeRegistryPullCredential(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
