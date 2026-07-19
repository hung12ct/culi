package patterns

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// Work-unit tuning. Noise filtering keeps lockfiles, vendored trees, and
// mega-hunks out of both the clustering signal and the model input.
const (
	maxUnitCommits = 50
	unitTimeGap    = 48 * 3600 // seconds between commits before a new unit starts
	maxUnits       = 3
	maxHunkLines   = 60 // diff lines rendered per unit
	maxInputBytes  = 12 << 10
	maxFileHunk    = 1500 // numstat lines above this = generated, excluded
)

// commitInfo is one parsed commit in the branch window.
type commitInfo struct {
	SHA     string
	Time    int64
	Subject string
	Dirs    map[string]bool
	Files   []string
}

// unit is one clustered stretch of related work.
type unit struct {
	Commits []commitInfo
	Dirs    []string
}

// git runs a git subcommand, "" on failure (probes degrade like gitfacts).
func git(ctx context.Context, root string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return out.String()
}

// branchTips lists local branch → tip sha.
func branchTips(ctx context.Context, root string) (map[string]string, error) {
	out := git(ctx, root, "for-each-ref", "refs/heads", "--format=%(refname:short) %(objectname)")
	if out == "" {
		return nil, fmt.Errorf("patterns: no readable branches in %s", root)
	}
	tips := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if name, sha, ok := strings.Cut(line, " "); ok {
			tips[name] = sha
		}
	}
	return tips, nil
}

func defaultBranch(ctx context.Context, root string) string {
	if ref := strings.TrimSpace(git(ctx, root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")); ref != "" {
		return strings.TrimPrefix(ref, "origin/")
	}
	for _, b := range []string{"main", "master"} {
		if strings.TrimSpace(git(ctx, root, "rev-parse", "--verify", "--quiet", b)) != "" {
			return b
		}
	}
	return ""
}

func isAncestor(ctx context.Context, root, sha, branch string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "merge-base", "--is-ancestor", sha, branch)
	return cmd.Run() == nil
}

// branchCommits parses the merge-base..branch window into commits with their
// noise-filtered file sets. Empty when the branch has no unique work.
func branchCommits(ctx context.Context, root, base, branch string) []commitInfo {
	if base == "" || base == branch {
		return nil
	}
	mergeBase := strings.TrimSpace(git(ctx, root, "merge-base", base, branch))
	if mergeBase == "" {
		return nil
	}
	out := git(ctx, root, "log", "--numstat", "--no-merges",
		"--pretty=format:@%H|%ct|%s", "-n", strconv.Itoa(maxUnitCommits),
		mergeBase+".."+branch)
	if out == "" {
		return nil
	}
	var commits []commitInfo
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "@"); ok {
			parts := strings.SplitN(rest, "|", 3)
			if len(parts) != 3 {
				continue
			}
			ts, _ := strconv.ParseInt(parts[1], 10, 64)
			commits = append(commits, commitInfo{
				SHA: parts[0], Time: ts, Subject: parts[2], Dirs: map[string]bool{},
			})
			continue
		}
		if len(commits) == 0 {
			continue
		}
		added, deleted, path, ok := numstatLine(line)
		if !ok || noisyPath(path) || added+deleted > maxFileHunk {
			continue
		}
		c := &commits[len(commits)-1]
		if d := moduleDir(path); d != "" {
			c.Dirs[d] = true
		}
		c.Files = append(c.Files, path)
	}
	// Oldest first for chronological clustering.
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits
}

// clusterUnits groups commits into work units: consecutive commits within
// unitTimeGap that share at least one module dir stay together.
func clusterUnits(commits []commitInfo) []unit {
	var units []unit
	for _, c := range commits {
		if len(c.Files) == 0 {
			continue // pure-noise commit
		}
		if len(units) > 0 {
			u := &units[len(units)-1]
			last := u.Commits[len(u.Commits)-1]
			if c.Time-last.Time <= unitTimeGap && overlaps(u, c) {
				u.Commits = append(u.Commits, c)
				continue
			}
		}
		units = append(units, unit{Commits: []commitInfo{c}})
	}
	for i := range units {
		units[i].Dirs = unitDirs(units[i])
	}
	sort.SliceStable(units, func(a, b int) bool {
		return len(units[a].Commits) > len(units[b].Commits)
	})
	if len(units) > maxUnits {
		units = units[:maxUnits]
	}
	return units
}

func overlaps(u *unit, c commitInfo) bool {
	for _, prev := range u.Commits {
		for d := range c.Dirs {
			if prev.Dirs[d] {
				return true
			}
		}
	}
	return false
}

func unitDirs(u unit) []string {
	set := map[string]bool{}
	for _, c := range u.Commits {
		for d := range c.Dirs {
			set[d] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// renderUnits serializes work units + representative hunks for the model,
// byte-capped.
func renderUnits(ctx context.Context, root, branch string, units []unit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Branch %s work units\n\n", branch)
	for i, u := range units {
		var ub strings.Builder
		fmt.Fprintf(&ub, "## unit %d — dirs: %s\n\ncommits:\n", i+1, strings.Join(u.Dirs, ", "))
		for _, c := range u.Commits {
			fmt.Fprintf(&ub, "- %s\n", firstLine(c.Subject))
		}
		if hunks := unitHunks(ctx, root, u); hunks != "" {
			ub.WriteString("\nrepresentative diff:\n```\n" + hunks + "```\n")
		}
		ub.WriteString("\n")
		if b.Len()+ub.Len() > maxInputBytes {
			b.WriteString("(further units truncated)\n")
			break
		}
		b.WriteString(ub.String())
	}
	return b.String()
}

// unitHunks extracts a line-capped diff for the unit's commit range,
// restricted to its (noise-filtered) files.
func unitHunks(ctx context.Context, root string, u unit) string {
	first, last := u.Commits[0], u.Commits[len(u.Commits)-1]
	files := map[string]bool{}
	args := []string{"diff", first.SHA + "^", last.SHA, "--"}
	n := 0
	for _, c := range u.Commits {
		for _, f := range c.Files {
			if !files[f] && n < 6 {
				files[f] = true
				args = append(args, f)
				n++
			}
		}
	}
	out := git(ctx, root, args...)
	if out == "" {
		return ""
	}
	lines := strings.Split(out, "\n")
	if len(lines) > maxHunkLines {
		lines = append(lines[:maxHunkLines], "… (diff truncated)")
	}
	for i, l := range lines {
		if len(l) > 200 {
			lines[i] = l[:200] + "…"
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// numstatLine parses "12\t3\tpath" ("-" for binary → 0).
func numstatLine(line string) (added, deleted int, path string, ok bool) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) != 3 {
		return 0, 0, "", false
	}
	a, _ := strconv.Atoi(parts[0])
	d, _ := strconv.Atoi(parts[1])
	return a, d, parts[2], true
}

// noisyPath excludes lockfiles, vendored/generated trees, renames, and
// secret-bearing files from the pattern signal. The secret exclusions are
// load-bearing (§6): diff hunks quoted into a card body are injected into
// prompts forever — a committed credential must never reach the model input.
func noisyPath(path string) bool {
	if strings.Contains(path, "=>") {
		return true
	}
	base := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		base = path[i+1:]
	}
	switch base {
	case "go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "Cargo.lock", "poetry.lock", "composer.lock":
		return true
	case ".npmrc", ".netrc", ".pypirc", "id_rsa", "id_ed25519":
		return true
	}
	lower := strings.ToLower(base)
	if strings.HasPrefix(lower, ".env") || strings.HasPrefix(lower, "credentials") ||
		strings.HasPrefix(lower, "secret") {
		return true
	}
	for _, suf := range []string{".pem", ".key", ".p12", ".pfx", ".keystore"} {
		if strings.HasSuffix(lower, suf) {
			return true
		}
	}
	for _, dir := range []string{"vendor/", "node_modules/", "dist/", "build/", ".git/"} {
		if strings.HasPrefix(path, dir) || strings.Contains(path, "/"+dir) {
			return true
		}
	}
	return strings.HasSuffix(base, ".pb.go") || strings.HasSuffix(base, "_gen.go") ||
		strings.HasSuffix(base, ".min.js")
}

// moduleDir buckets a path like gitfacts does (duplicated deliberately —
// patterns must not depend on the package that feeds a different pipeline).
func moduleDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		return "."
	}
	switch parts[0] {
	case "internal", "cmd", "pkg", "src", "apps", "packages":
		if len(parts) > 2 {
			return parts[0] + "/" + parts[1]
		}
	}
	return parts[0]
}
