package embed

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVecRoundTrip(t *testing.T) {
	in := []float32{0.1, -2.5, 3.75, 0}
	got := DecodeVec(EncodeVec(in))
	if len(got) != len(in) {
		t.Fatalf("len = %d, want %d", len(got), len(in))
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("vec[%d] = %v, want %v", i, got[i], in[i])
		}
	}
}

func TestDecodeVecMalformed(t *testing.T) {
	if DecodeVec(nil) != nil {
		t.Error("nil blob should decode to nil")
	}
	if DecodeVec([]byte{1, 2, 3}) != nil {
		t.Error("non-multiple-of-4 blob should decode to nil")
	}
}

func TestNormalizeAndDot(t *testing.T) {
	a := Normalize([]float32{3, 4})
	if math.Abs(Dot(a, a)-1) > 1e-6 {
		t.Errorf("self-dot of normalized vec = %v, want 1", Dot(a, a))
	}
	b := Normalize([]float32{4, -3})
	if math.Abs(Dot(a, b)) > 1e-6 {
		t.Errorf("orthogonal dot = %v, want 0", Dot(a, b))
	}
	if Dot(a, []float32{1}) != 0 {
		t.Error("mismatched dims should score 0")
	}
	z := Normalize([]float32{0, 0})
	if z[0] != 0 || z[1] != 0 {
		t.Error("zero vector should survive Normalize unchanged")
	}
}

func TestOllamaEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		if req.Model != "test-model" || len(req.Input) != 2 {
			t.Errorf("request = %+v", req)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float32{{3, 4}, {0, 5}},
		})
	}))
	defer srv.Close()

	o := NewOllama(srv.URL, "test-model", "")
	vecs, err := o.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors", len(vecs))
	}
	// Normalized on return.
	if math.Abs(Dot(vecs[0], vecs[0])-1) > 1e-6 {
		t.Errorf("vector not normalized: %v", vecs[0])
	}
}

func TestOllamaEmbedErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()
	o := NewOllama(srv.URL, "missing", "")
	if _, err := o.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("want error on non-200")
	}
	srv.Close()
	if _, err := o.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("want error on connection refused")
	}
	if vecs, err := o.Embed(context.Background(), nil); err != nil || vecs != nil {
		t.Fatal("empty input should be a no-op")
	}
}

// keep_alive keeps the model resident between prompts; without it Ollama
// unloads an idle model and the cold reload blows the hot path's embed budget.
// Empty must omit the field entirely so the server default still applies.
func TestOllamaKeepAlive(t *testing.T) {
	for _, tc := range []struct{ keepAlive, want string }{
		{"30m", "30m"},
		{"-1", "-1"},
		{"", ""}, // omitempty ⇒ absent from the payload
	} {
		var got string
		var present bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decoding request: %v", err)
			}
			v, ok := req["keep_alive"]
			present = ok
			got, _ = v.(string)
			json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 0}}})
		}))
		o := NewOllama(srv.URL, "m", tc.keepAlive)
		if _, err := o.Embed(context.Background(), []string{"x"}); err != nil {
			t.Fatalf("Embed(%q): %v", tc.keepAlive, err)
		}
		srv.Close()
		if tc.want == "" && present {
			t.Errorf("keepAlive %q: field should be omitted, got %q", tc.keepAlive, got)
		}
		if got != tc.want {
			t.Errorf("keepAlive %q: sent keep_alive=%q, want %q", tc.keepAlive, got, tc.want)
		}
	}
}

func TestOllamaCountMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1}}})
	}))
	defer srv.Close()
	o := NewOllama(srv.URL, "m", "")
	if _, err := o.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("want error when embedding count mismatches input count")
	}
}
