package serve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialStatus(t *testing.T) {
	t.Setenv("CULI_TEST_API_KEY", "set")
	if ready, status := credentialStatus("CULI_TEST_API_KEY", ""); !ready || status == "" {
		t.Fatalf("env credential = %v, %q", ready, status)
	}
	t.Setenv("CULI_TEST_API_KEY", "")
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ready, status := credentialStatus("CULI_TEST_API_KEY", key); !ready || status != "Key file ready" {
		t.Fatalf("file credential = %v, %q", ready, status)
	}
	if ready, status := credentialStatus("CULI_TEST_API_KEY", key+"-missing"); ready || status != "Key file not found" {
		t.Fatalf("missing credential = %v, %q", ready, status)
	}
}

func TestOllamaBackendStatus(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()
	s := &server{}
	if ready, status := s.ollamaBackendStatus(context.Background(), endpoint.URL); !ready || status != "Ollama is running" {
		t.Fatalf("ollama status = %v, %q", ready, status)
	}
}
