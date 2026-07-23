package serve

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
)

// histEntry is one line of a card's git audit trail for the KB detail view.
type histEntry struct {
	Action string `json:"action"`
	Detail string `json:"detail"`
	Date   string `json:"date"`
	SHA    string `json:"sha"`
	Dot    string `json:"dot"`
}

// cardHistory reads the last few commits touching a card file from the
// knowledge git repo. Best-effort: a non-repo, missing file, or absent git
// yields nil — the console simply shows no history (files are truth; the git
// log is a convenience, not a dependency).
func cardHistory(ctx context.Context, kdir, relPath string, limit int) []histEntry {
	if relPath == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", kdir, "log",
		"--no-color", "--date=short",
		"--pretty=format:%h\x1f%ad\x1f%s", "-n", strconv.Itoa(limit), "--", relPath)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var entries []histEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 {
			continue
		}
		action, detail := splitCommitSubject(parts[2])
		entries = append(entries, histEntry{
			Action: action, Detail: detail, Date: parts[1], SHA: parts[0], Dot: actionDot(action),
		})
	}
	return entries
}

// splitCommitSubject peels the leading "verb:" off a culi commit subject
// ("learn: reinforced …" → "learn:", "reinforced …"). Subjects without a
// recognized verb keep the whole line as the detail under a neutral label.
func splitCommitSubject(subject string) (action, detail string) {
	for _, v := range []string{"learn", "merge", "import", "manual", "gen", "mcp", "retire", "review"} {
		if strings.HasPrefix(subject, v+":") {
			return v + ":", strings.TrimSpace(strings.TrimPrefix(subject, v+":"))
		}
	}
	return "commit:", subject
}

func actionDot(action string) string {
	switch action {
	case "learn:":
		return "#169b6b"
	case "merge:":
		return "#6db4ff"
	case "gen:":
		return "#57c785"
	case "mcp:":
		return "#ff7ea8"
	case "retire:", "review:", "manual:":
		return "#c97a16"
	default:
		return "#7c8799"
	}
}
