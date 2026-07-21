package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Version prints the build's version, git commit, and commit time. It reads
// Go's embedded build info — populated automatically by `go build`/`go install`
// from a git checkout, so no ldflags or Makefile wiring is needed. A local
// `go install ./cmd/culi` shows the commit it was built from (with +dirty when
// the tree had uncommitted changes); a tagged `go install ...@vX.Y.Z` shows the
// version.
func Version(_ []string) error {
	fmt.Println(versionString())
	return nil
}

func versionString() string {
	version := "(devel)"
	commit, built, dirty := "unknown", "unknown", false
	if bi, ok := debug.ReadBuildInfo(); ok {
		if bi.Main.Version != "" {
			version = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				commit = s.Value
			case "vcs.time":
				built = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if dirty {
		commit += "+dirty"
	}
	return fmt.Sprintf("culi %s\n  commit: %s\n  built:  %s\n  go:     %s",
		version, commit, built, runtime.Version())
}
