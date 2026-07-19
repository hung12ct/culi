package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubClaude installs a fake `claude` binary that replies with the given
// wrapper JSONs in sequence (one per invocation, tracked via a counter file).
func stubClaude(t *testing.T, wrappers ...string) {
	t.Helper()
	dir := t.TempDir()
	for i, w := range wrappers {
		if err := os.WriteFile(filepath.Join(dir, "resp"+string(rune('0'+i))+".json"), []byte(w), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
cat > /dev/null
d="$(dirname "$0")"
n=0
[ -f "$d/count" ] && n="$(cat "$d/count")"
echo $((n+1)) > "$d/count"
cat "$d/resp$n.json"
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func wrapper(result string, in, out int) string {
	raw, _ := json.Marshal(map[string]any{
		"type": "result", "is_error": false, "result": result,
		"usage": map[string]int{"input_tokens": in, "output_tokens": out},
	})
	return string(raw)
}

func TestCLIMergerFencedOutput(t *testing.T) {
	// The real CLI fences JSON even when told not to — verified against
	// claude -p; the decoder must strip it.
	inner := `{"canonical_markdown":"# Canonical\n\nMerged.","residues":[],"notes":""}`
	stubClaude(t, wrapper("```json\n"+inner+"\n```", 100, 50))

	m, err := NewCLIMerger("claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ModelName(); !strings.Contains(got, "claude-cli") {
		t.Errorf("ModelName = %q, want transport tag", got)
	}
	res, err := m.MergeCluster(context.Background(), ClusterInput{
		Kind: "agent", Name: "reviewer",
		Copies: []Copy{{Repo: "a", Body: "x"}, {Repo: "b", Body: "y"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.CanonicalBody, "Canonical") {
		t.Errorf("canonical body = %q", res.CanonicalBody)
	}
	if res.Usage.Prompt != 100 || res.Usage.Completion != 50 {
		t.Errorf("usage = %+v", res.Usage)
	}
}

func TestCLIMergerRetriesOnGarbage(t *testing.T) {
	inner := `{"canonical_markdown":"ok body","residues":[],"notes":""}`
	stubClaude(t,
		wrapper("I think the merged version should be...", 10, 10), // no JSON
		wrapper(inner, 10, 10),
	)
	m, err := NewCLIMerger("claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.MergeCluster(context.Background(), ClusterInput{
		Kind: "agent", Name: "x", Copies: []Copy{{Repo: "a", Body: "b"}},
	})
	if err != nil {
		t.Fatalf("retry should have recovered: %v", err)
	}
	if res.CanonicalBody != "ok body" {
		t.Errorf("body = %q", res.CanonicalBody)
	}
	// Usage sums across both attempts.
	if res.Usage.Prompt != 20 {
		t.Errorf("usage should accumulate over retries: %+v", res.Usage)
	}
}

func TestCLIMergerMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := NewCLIMerger("m"); err == nil {
		t.Fatal("want error when claude is not in PATH")
	}
}

func TestOllamaMerger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req["model"] != "qwen3" || req["format"] == nil || req["stream"] != false {
			t.Errorf("request = %v", req)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"message":           map[string]string{"content": `{"cards":[],"residual_markdown":"# repo facts","notes":"n"}`},
			"prompt_eval_count": 40,
			"eval_count":        20,
		})
	}))
	defer srv.Close()

	m := NewOllamaMerger(srv.URL, "qwen3")
	if got := m.ModelName(); !strings.Contains(got, "ollama") {
		t.Errorf("ModelName = %q, want transport tag", got)
	}
	dec, err := m.DecomposeClaudeMD(context.Background(), ClaudeMDInput{Repo: "r", Content: "# doc"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Residual != "# repo facts" || dec.Usage.Prompt != 40 {
		t.Errorf("decomposition = %+v", dec)
	}
}

func TestDecodeLooseJSON(t *testing.T) {
	var out struct {
		A int `json:"a"`
	}
	for _, s := range []string{
		`{"a":1}`,
		"```json\n{\"a\":1}\n```",
		"Here you go:\n\n{\"a\":1}\n\nHope that helps!",
	} {
		out.A = 0
		if err := decodeLooseJSON(s, &out); err != nil || out.A != 1 {
			t.Errorf("decodeLooseJSON(%q) = %v, a=%d", s, err, out.A)
		}
	}
	if err := decodeLooseJSON("no json here", &out); err == nil {
		t.Error("want error for JSON-free text")
	}
}
