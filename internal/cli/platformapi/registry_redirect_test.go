package platformapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRegistryUploadRefusesMethodChangingRedirect(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirected" {
			redirected.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/redirected", http.StatusMovedPermanently)
	}))
	defer server.Close()
	archive := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(archive, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &PlatformClient{baseURL: server.URL, apiPrefix: "/api/v1", http: server.Client()}
	err := client.PushRegistryImage(context.Background(), archive, "registry/example:v1", "tenant")
	if err == nil || !strings.Contains(err.Error(), "HTTPS platform API origin") || redirected.Load() != 0 {
		t.Fatalf("upload followed redirect as GET: %v redirected=%d", err, redirected.Load())
	}
}

func TestRegistryUploadDirectRequestRetainsMultipartPOST(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("x-api-key") != "test-key" {
			t.Error("lost upload method or credential")
			w.WriteHeader(400)
			return
		}
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Error(err)
			return
		}
		if r.FormValue("scope") != "tenant" || r.FormValue("target") != "registry/example:v1" {
			t.Error("lost target or scope")
		}
		file, _, err := r.FormFile("image_tar")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		content, _ := io.ReadAll(file)
		if string(content) != "image" {
			t.Error("lost image content")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	archive := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(archive, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &PlatformClient{baseURL: server.URL, apiPrefix: "/api/v1", token: "test-key", http: server.Client()}
	if err := client.PushRegistryImage(context.Background(), archive, "registry/example:v1", "tenant"); err != nil {
		t.Fatal(err)
	}
}
