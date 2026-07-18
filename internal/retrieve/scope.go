package retrieve

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Scope is the deterministic routing context for one prompt: which card
// scopes are candidates and their precedence boosts. Costs a few stats and
// small file reads — no exec, no LLM.
type Scope struct {
	Repo    string // "" outside git
	Branch  string
	Langs   []string
	Allowed map[string]bool
}

// Precedence boosts (additive, post-fusion): a highly relevant global card
// still beats a barely relevant branch card — precedence is a tiebreaker,
// never a hard tier.
const (
	boostBranch = 0.012
	boostRepo   = 0.008
	boostLang   = 0.004
)

// DetectScope resolves cwd → scope chain. Never fails: worst case is
// global + dir:<hash>.
func DetectScope(cwd string) Scope {
	sc := Scope{Allowed: map[string]bool{"global": true}}

	root, gitDir := findGitRoot(cwd)
	if root == "" {
		sum := sha256.Sum256([]byte(cwd))
		sc.Allowed["dir:"+hex.EncodeToString(sum[:4])] = true
		return sc
	}
	sc.Repo = filepath.Base(root)
	sc.Allowed["repo:"+sc.Repo] = true
	if branch := readBranch(gitDir); branch != "" {
		sc.Branch = branch
		sc.Allowed["branch:"+sc.Repo+"@"+branch] = true
	}
	for _, lang := range detectLangs(root) {
		sc.Langs = append(sc.Langs, lang)
		sc.Allowed["lang:"+lang] = true
	}
	return sc
}

// Boost returns the additive precedence bonus for a card's narrowest
// in-scope rank (see knowledge.NarrowestScopeRank).
func (Scope) Boost(narrowestRank int) float64 {
	switch narrowestRank {
	case 3:
		return boostBranch
	case 2:
		return boostRepo
	case 1:
		return boostLang
	default:
		return 0
	}
}

// findGitRoot walks up from cwd looking for .git (dir, or file for
// worktrees). Returns the repo root and the effective git dir ("" if none).
func findGitRoot(cwd string) (root, gitDir string) {
	dir := cwd
	for i := 0; i < 24; i++ { // depth cap: never loop on weird mounts
		g := filepath.Join(dir, ".git")
		info, err := os.Stat(g)
		if err == nil {
			if info.IsDir() {
				return dir, g
			}
			// Worktree: .git is a file "gitdir: <path>".
			raw, err := os.ReadFile(g)
			if err == nil {
				line := strings.TrimSpace(strings.TrimPrefix(string(raw), "gitdir:"))
				if !filepath.IsAbs(line) {
					line = filepath.Join(dir, line)
				}
				return dir, line
			}
			return dir, ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ""
		}
		dir = parent
	}
	return "", ""
}

// readBranch parses HEAD: "ref: refs/heads/<branch>" → branch; detached HEAD
// → first 8 sha chars. Tolerates mid-rebase/missing states with "".
func readBranch(gitDir string) string {
	raw, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(raw))
	if ref, ok := strings.CutPrefix(head, "ref: "); ok {
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	if len(head) >= 8 {
		return head[:8] // detached
	}
	return ""
}

// langManifests maps manifest files to language scopes — existence checks
// only, no content parsing on the hot path.
var langManifests = []struct {
	file string
	lang string
}{
	{"go.mod", "go"},
	{"tsconfig.json", "typescript"},
	{"package.json", "javascript"},
	{"pyproject.toml", "python"},
	{"requirements.txt", "python"},
	{"Cargo.toml", "rust"},
	{"Gemfile", "ruby"},
	{"composer.json", "php"},
	{"pom.xml", "java"},
	{"build.gradle", "java"},
}

func detectLangs(root string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range langManifests {
		if seen[m.lang] {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, m.file)); err == nil {
			seen[m.lang] = true
			out = append(out, m.lang)
		}
	}
	return out
}
