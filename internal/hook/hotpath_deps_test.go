package hook

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestHotPathDependencyFirewall enforces contract C2: the per-prompt hot-path
// packages import only stdlib + modernc + yaml. A learning/LLM dependency
// (gopheragent, the MCP SDK, the Anthropic SDK) reaching these packages —
// even transitively, e.g. via a careless import in the shared learn/queue —
// would pull heavy init and blow the 150ms latency budget on every prompt.
//
// This converts the once-by-hand `go list -deps | grep gopheragent` check into
// a regression guard that runs under `make test` and CI's `go test ./...`, so
// the firewall can no longer be breached silently.
func TestHotPathDependencyFirewall(t *testing.T) {
	const prefix = "github.com/hung12ct/culi/internal/"
	// Packages named by the CLAUDE.md §2 dependency contract that exist today.
	hotPath := []string{"hook", "store", "knowledge", "retrieve", "pack", "embed"}
	// Heavy modules that must never reach the hot path. gopheragent is the one
	// the contract calls out by name; the MCP and Anthropic SDKs are the other
	// large trees that only belong in the pull server / learning pipeline.
	forbidden := []string{
		"github.com/hung12ct/gopheragent",
		"github.com/modelcontextprotocol/go-sdk",
		"github.com/anthropics/anthropic-sdk-go",
	}

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH; cannot compute the dependency graph")
	}
	for _, pkg := range hotPath {
		importPath := prefix + pkg
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		out, err := exec.CommandContext(ctx, goBin, "list", "-deps", importPath).Output()
		cancel()
		if err != nil {
			t.Fatalf("go list -deps %s: %v", importPath, err)
		}
		for dep := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			for _, bad := range forbidden {
				if dep == bad || strings.HasPrefix(dep, bad+"/") {
					t.Errorf("C2 firewall breach: hot-path %s transitively imports %s (via %s)",
						importPath, bad, dep)
				}
			}
		}
	}
}
