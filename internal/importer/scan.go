// Package importer reconciles drifted .claude directories and CLAUDE.md files
// from the user's repos into the canonical knowledge store. Scan is read-only;
// merge writes only to knowledge/.import/staged/; apply is the single reviewed
// step that touches knowledge/ proper (contract C4).
package importer

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hung12ct/culi/internal/knowledge"
)

// Item is one copy of an artifact found in a repo.
type Item struct {
	Repo        string   `json:"repo"`      // repo name (unique across the scan)
	RepoPath    string   `json:"repo_path"` // absolute repo root
	Kind        string   `json:"kind"`      // agent | skill | claudemd
	Name        string   `json:"name"`      // identity within kind
	Path        string   `json:"path"`      // absolute file path (SKILL.md for skills)
	Hash        string   `json:"hash"`      // sha256 of raw content
	NormHash    string   `json:"norm_hash"` // sha256 of normalized content
	Lines       int      `json:"lines"`
	Description string   `json:"description,omitempty"` // from the artifact's own frontmatter
	Attachments []string `json:"attachments,omitempty"` // skill sibling files, dir-relative
}

// Cluster groups all copies of one artifact identity across repos.
type Cluster struct {
	Key             string  `json:"key"`  // "<kind>/<name>"
	Kind            string  `json:"kind"` // agent | skill
	Name            string  `json:"name"`
	Class           string  `json:"class"`     // identical | superset | diverged | unique
	Canonical       string  `json:"canonical"` // repo whose copy merge stages (LLM output for diverged)
	Similarity      float64 `json:"similarity,omitempty"`
	AttachmentDrift bool    `json:"attachment_drift,omitempty"`
	Items           []Item  `json:"items"`
}

// RepoInfo summarizes one scanned repo.
type RepoInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Agents   int    `json:"agents"`
	Skills   int    `json:"skills"`
	ClaudeMD bool   `json:"claude_md"`
}

// Report is the full scan result, persisted to knowledge/.import/scan.json so
// the user can inspect it and merge can consume it without rescanning.
type Report struct {
	GeneratedAt time.Time  `json:"generated_at"`
	Repos       []RepoInfo `json:"repos"`
	Clusters    []Cluster  `json:"clusters"`
	ClaudeMD    []Item     `json:"claude_md"` // root CLAUDE.md files, decomposed at merge
}

// artifactMeta is the subset of Claude Code agent/skill frontmatter scan reads.
type artifactMeta struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Scan inventories every repo and clusters agents and skills by identity.
func Scan(repoPaths []string) (Report, error) {
	if len(repoPaths) == 0 {
		return Report{}, fmt.Errorf("importer: no repos to scan (set `repos:` in config.yaml or pass paths)")
	}
	rep := Report{GeneratedAt: time.Now().UTC()}
	byKey := map[string][]Item{}
	seen := map[string]int{}
	for _, root := range repoPaths {
		root = filepath.Clean(root)
		if _, err := os.Stat(root); err != nil {
			return Report{}, fmt.Errorf("importer: repo %s: %w", root, err)
		}
		name := uniqueRepoName(filepath.Base(root), seen)
		info := RepoInfo{Name: name, Path: root}
		items, err := scanRepo(name, root)
		if err != nil {
			return Report{}, err
		}
		for _, it := range items {
			switch it.Kind {
			case "claudemd":
				info.ClaudeMD = true
				rep.ClaudeMD = append(rep.ClaudeMD, it)
			case "agent":
				info.Agents++
				byKey[it.Kind+"/"+it.Name] = append(byKey[it.Kind+"/"+it.Name], it)
			case "skill":
				info.Skills++
				byKey[it.Kind+"/"+it.Name] = append(byKey[it.Kind+"/"+it.Name], it)
			}
		}
		rep.Repos = append(rep.Repos, info)
	}
	for key, items := range byKey {
		kind, name, _ := strings.Cut(key, "/")
		rep.Clusters = append(rep.Clusters, classify(Cluster{Key: key, Kind: kind, Name: name, Items: items}))
	}
	sort.Slice(rep.Clusters, func(i, j int) bool { return rep.Clusters[i].Key < rep.Clusters[j].Key })
	return rep, nil
}

// uniqueRepoName disambiguates repos sharing a basename (phin, phin-2, …).
func uniqueRepoName(base string, seen map[string]int) string {
	seen[base]++
	if n := seen[base]; n > 1 {
		return fmt.Sprintf("%s-%d", base, n)
	}
	return base
}

// scanRepo inventories one repo: .claude/agents/*.md, .claude/skills/*/SKILL.md
// (plus attachments), and the root CLAUDE.md.
func scanRepo(name, root string) ([]Item, error) {
	var out []Item
	agentsDir := filepath.Join(root, ".claude", "agents")
	entries, _ := os.ReadDir(agentsDir) // missing dir is fine
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		it, err := readItem(name, root, "agent", strings.TrimSuffix(e.Name(), ".md"), filepath.Join(agentsDir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	skillsDir := filepath.Join(root, ".claude", "skills")
	entries, _ = os.ReadDir(skillsDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		if _, err := os.Stat(path); err != nil {
			continue // dir without SKILL.md is not a skill
		}
		it, err := readItem(name, root, "skill", e.Name(), path)
		if err != nil {
			return nil, err
		}
		if it.Attachments, err = listAttachments(filepath.Join(skillsDir, e.Name())); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	cmPath := filepath.Join(root, "CLAUDE.md")
	if _, err := os.Stat(cmPath); err == nil {
		it, err := readItem(name, root, "claudemd", name, cmPath)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// readItem loads one artifact file and computes its identity hashes.
func readItem(repo, root, kind, name, path string) (Item, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Item{}, fmt.Errorf("importer: reading %s: %w", path, err)
	}
	norm := normalize(raw)
	it := Item{
		Repo: repo, RepoPath: root, Kind: kind, Name: name, Path: path,
		Hash:     hashBytes(raw),
		NormHash: hashBytes([]byte(strings.Join(norm, "\n"))),
		Lines:    len(norm),
	}
	if fm, _, err := knowledge.SplitFrontmatter(raw); err == nil && len(fm) > 0 {
		var meta artifactMeta
		if yaml.Unmarshal(fm, &meta) == nil {
			it.Description = strings.TrimSpace(meta.Description)
		}
		if it.Description == "" {
			// Claude Code tolerates frontmatter that strict YAML rejects
			// (unquoted values containing ": "); fall back to line scraping
			// so the card summary survives.
			it.Description = scrapeDescription(fm)
		}
	}
	return it, nil
}

// scrapeDescription extracts a single-line description: value from raw
// frontmatter bytes when YAML parsing fails or yields nothing.
func scrapeDescription(fm []byte) string {
	for line := range strings.SplitSeq(string(fm), "\n") {
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			return strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
	return ""
}

// listAttachments returns every non-SKILL.md file in a skill dir, dir-relative.
func listAttachments(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != "SKILL.md" && !strings.HasPrefix(filepath.Base(rel), ".") {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("importer: listing attachments in %s: %w", dir, err)
	}
	sort.Strings(out)
	return out, nil
}

// WriteReport persists the scan report for review and for merge to consume.
func WriteReport(knowledgeDir string, rep Report) (string, error) {
	dir := filepath.Join(knowledgeDir, ".import")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("importer: creating %s: %w", dir, err)
	}
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", fmt.Errorf("importer: encoding scan report: %w", err)
	}
	path := filepath.Join(dir, "scan.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", fmt.Errorf("importer: writing %s: %w", path, err)
	}
	return path, nil
}

// ReadReport loads a previously written scan report.
func ReadReport(knowledgeDir string) (Report, error) {
	raw, err := os.ReadFile(filepath.Join(knowledgeDir, ".import", "scan.json"))
	if err != nil {
		return Report{}, fmt.Errorf("importer: reading scan report (run `culi import scan` first): %w", err)
	}
	var rep Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return Report{}, fmt.Errorf("importer: decoding scan report: %w", err)
	}
	return rep, nil
}
