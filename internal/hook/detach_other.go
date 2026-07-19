//go:build !unix

package hook

import "os/exec"

// detach is a no-op off unix; the worker still starts, just not in its own
// session.
func detach(*exec.Cmd) {}
