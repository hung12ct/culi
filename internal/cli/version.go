package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version is the friendly release version, stamped at build time via
//
//	-ldflags "-X github.com/hung12ct/culi/internal/cli.version=$(git describe ...)"
//
// (see the Makefile — `make install`/`make build`). It's empty for a plain
// `go build`/`go install ./cmd/culi`, in which case versionString falls back to
// Go's embedded module version (the tag for `go install ...@vX.Y.Z`, otherwise
// a commit-based pseudo-version).
var version string

// Version prints the build's version, git commit, commit time, and Go version
// so you can confirm which build is running after an update.
func Version(_ []string) error {
	fmt.Println(versionString())
	return nil
}

func versionString() string {
	v := version
	commit, built, dirty := "unknown", "unknown", false
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v == "" && bi.Main.Version != "" {
			v = bi.Main.Version
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
	if v == "" {
		v = "(devel)"
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if dirty {
		commit += "+dirty"
	}
	return fmt.Sprintf("culi %s\n  commit: %s\n  built:  %s\n  go:     %s",
		v, commit, built, runtime.Version())
}
