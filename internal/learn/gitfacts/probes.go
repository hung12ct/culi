package gitfacts

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Module is one directory's churn summary.
type Module struct {
	Dir     string
	Commits int
	Lines   int
}

// modules aggregates `git log --numstat` by module directory: top-level dirs,
// one level deeper under the conventional Go/JS umbrella dirs.
func modules(ctx context.Context, root string) []Module {
	// format:@ marks each commit boundary with a bare "@" line (a plain
	// --format=@ is rejected as a pretty-format name).
	out := git(ctx, root, "log", "--numstat", "--no-merges",
		"--pretty=format:@", "-n", strconv.Itoa(churnCommits))
	if out == "" {
		return nil
	}
	type agg struct {
		lines int
		hits  int // commits touching this module (deduped per commit block)
	}
	stats := map[string]*agg{}
	seenThisCommit := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "@" {
			clear(seenThisCommit)
			continue
		}
		added, deleted, path, ok := numstatLine(line)
		if !ok || strings.Contains(path, "=>") {
			continue // renames would mis-bucket into a spurious module
		}
		dir := moduleDir(path)
		if dir == "" {
			continue
		}
		a := stats[dir]
		if a == nil {
			a = &agg{}
			stats[dir] = a
		}
		a.lines += added + deleted
		if !seenThisCommit[dir] {
			seenThisCommit[dir] = true
			a.hits++
		}
	}
	ms := make([]Module, 0, len(stats))
	for dir, a := range stats {
		ms = append(ms, Module{Dir: dir, Commits: a.hits, Lines: a.lines})
	}
	sort.Slice(ms, func(i, j int) bool {
		if ms[i].Commits != ms[j].Commits {
			return ms[i].Commits > ms[j].Commits
		}
		return ms[i].Dir < ms[j].Dir
	})
	if len(ms) > maxModules {
		ms = ms[:maxModules]
	}
	return ms
}

// numstatLine parses "12\t3\tpath/to/file.go" ("-" for binary).
func numstatLine(line string) (added, deleted int, path string, ok bool) {
	parts := strings.SplitN(line, "\t", 3)
	if len(parts) != 3 {
		return 0, 0, "", false
	}
	a, _ := strconv.Atoi(parts[0]) // "-" → 0
	d, _ := strconv.Atoi(parts[1])
	return a, d, parts[2], true
}

// moduleDir maps a file path to its module bucket: "internal/retrieve",
// "cmd/culi", or the top-level dir. Root files bucket as ".".
func moduleDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
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

var conventionalRe = regexp.MustCompile(`^([a-z]+)(\([^)]*\))?!?: `)

// conventions samples recent commit subjects and describes the style.
func conventions(ctx context.Context, root string) string {
	out := git(ctx, root, "log", "--no-merges", "--format=%s", "-n", strconv.Itoa(subjectSample))
	if out == "" {
		return ""
	}
	subjects := strings.Split(strings.TrimSpace(out), "\n")
	prefixes := map[string]int{}
	conventional := 0
	for _, s := range subjects {
		if m := conventionalRe.FindStringSubmatch(s); m != nil {
			conventional++
			prefixes[m[1]]++
		}
	}
	if len(subjects) == 0 {
		return ""
	}
	pct := conventional * 100 / len(subjects)
	if pct < 30 {
		return fmt.Sprintf("Free-form commit subjects (%d%% conventional-commit style in last %d).", pct, len(subjects))
	}
	type pc struct {
		p string
		n int
	}
	top := make([]pc, 0, len(prefixes))
	for p, n := range prefixes {
		top = append(top, pc{p, n})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].n != top[j].n {
			return top[i].n > top[j].n
		}
		return top[i].p < top[j].p
	})
	if len(top) > 6 {
		top = top[:6]
	}
	names := make([]string, len(top))
	for i, t := range top {
		names[i] = fmt.Sprintf("%s (%d)", t.p, t.n)
	}
	return fmt.Sprintf("Conventional commits (%d%% of last %d). Common types: %s.",
		pct, len(subjects), strings.Join(names, ", "))
}

// deps lists direct dependencies from the common manifests.
func deps(root string) []string {
	var out []string
	out = append(out, goModDeps(filepath.Join(root, "go.mod"))...)
	out = append(out, packageJSONDeps(filepath.Join(root, "package.json"))...)
	out = append(out, requirementsDeps(filepath.Join(root, "requirements.txt"))...)
	if len(out) > maxDeps {
		out = out[:maxDeps]
	}
	return out
}

func goModDeps(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	inBlock := false
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "require ("):
			inBlock = true
		case inBlock && t == ")":
			inBlock = false
		case inBlock && t != "" && !strings.Contains(t, "// indirect"):
			if mod, _, ok := strings.Cut(t, " "); ok {
				out = append(out, mod)
			}
		case strings.HasPrefix(t, "require ") && !strings.Contains(t, "("):
			fields := strings.Fields(t)
			if len(fields) >= 2 && !strings.Contains(t, "// indirect") {
				out = append(out, fields[1])
			}
		}
	}
	return out
}

func packageJSONDeps(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil
	}
	out := make([]string, 0, len(pkg.Dependencies))
	for name := range pkg.Dependencies {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func requirementsDeps(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "-") {
			continue
		}
		name := strings.FieldsFunc(t, func(r rune) bool {
			return r == '=' || r == '<' || r == '>' || r == '~' || r == '[' || r == ';' || r == ' '
		})
		if len(name) > 0 {
			out = append(out, name[0])
		}
	}
	sort.Strings(out)
	return out
}

var makeTargetRe = regexp.MustCompile(`(?m)^([A-Za-z0-9_.-]+):($|[^=])`)

// buildTest summarizes how the repo builds and tests.
func buildTest(root string) string {
	var parts []string
	if raw, err := os.ReadFile(filepath.Join(root, "Makefile")); err == nil {
		seen := map[string]bool{}
		var targets []string
		for _, m := range makeTargetRe.FindAllStringSubmatch(string(raw), -1) {
			t := m[1]
			if t == ".PHONY" || seen[t] {
				continue
			}
			seen[t] = true
			targets = append(targets, t)
		}
		if len(targets) > 14 {
			targets = targets[:14]
		}
		if len(targets) > 0 {
			parts = append(parts, "Makefile targets: "+strings.Join(targets, ", ")+".")
		}
	}
	if entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows")); err == nil && len(entries) > 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		parts = append(parts, "CI workflows: "+strings.Join(names, ", ")+".")
	}
	if n := countTestFiles(root); n > 0 {
		parts = append(parts, fmt.Sprintf("Go test files: %d (alongside sources).", n))
	}
	return strings.Join(parts, "\n")
}

// countTestFiles walks shallowly (skipping vendor/node_modules/.git) counting
// *_test.go files.
func countTestFiles(root string) int {
	n := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			n++
		}
		return nil
	})
	return n
}

// branchWork summarizes branch-unique commits vs the merge base with the
// default branch.
func branchWork(ctx context.Context, root, branch string) string {
	base := defaultBranch(ctx, root)
	if base == "" || base == branch {
		return ""
	}
	mergeBase := strings.TrimSpace(git(ctx, root, "merge-base", base, branch))
	if mergeBase == "" {
		return ""
	}
	log := strings.TrimSpace(git(ctx, root, "log", "--oneline",
		"-n", strconv.Itoa(maxBranchLog), mergeBase+".."+branch))
	if log == "" {
		return ""
	}
	dirs := strings.TrimSpace(git(ctx, root, "diff", "--name-only", mergeBase, branch))
	touched := map[string]bool{}
	for _, p := range strings.Split(dirs, "\n") {
		if d := moduleDir(p); d != "" {
			touched[d] = true
		}
	}
	dl := make([]string, 0, len(touched))
	for d := range touched {
		dl = append(dl, d)
	}
	sort.Strings(dl)
	return fmt.Sprintf("Commits unique to %s (vs %s):\n%s\n\nDirectories touched: %s",
		branch, base, log, strings.Join(dl, ", "))
}

// defaultBranch resolves origin/HEAD, falling back to main/master.
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

// prior returns the existing CLAUDE.md minus culi-generated marker spans —
// only the human-authored content is a prior.
func prior(root string) string {
	raw, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		return ""
	}
	s := stripMarkerSpans(string(raw))
	if len(s) > maxPriorChars {
		s = s[:maxPriorChars] + "\n… (truncated)"
	}
	return strings.TrimSpace(s)
}

var markerSpanRe = regexp.MustCompile(`(?s)<!-- culi:begin [^>]*-->.*?<!-- culi:end [^>]*-->\n?`)

// stripMarkerSpans removes culi-generated spans (shared shape with branchgen;
// duplicated deliberately — gitfacts must not depend on the generator it
// feeds).
func stripMarkerSpans(s string) string {
	return markerSpanRe.ReplaceAllString(s, "")
}
