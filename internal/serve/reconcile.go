package serve

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/hung12ct/culi/internal/importer"
	"github.com/hung12ct/culi/internal/indexer"
	"github.com/hung12ct/culi/internal/knowledge"
)

// ---------- payload types ----------

type importRepo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Agents   int    `json:"agents"`
	Skills   int    `json:"skills"`
	ClaudeMD bool   `json:"claudeMd"`
}

// importCluster is one shared artifact across repos and how divergent its
// copies are — the unit the operator reasons about when deciding to merge.
type importCluster struct {
	Key        string  `json:"key"`
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	Class      string  `json:"class"`
	Repos      int     `json:"repos"`
	Similarity float64 `json:"similarity"`
}

type importScan struct {
	Scanned   bool            `json:"scanned"`
	At        string          `json:"at"`
	Repos     []importRepo    `json:"repos"`
	Diverged  int             `json:"diverged"`
	Unique    int             `json:"unique"`
	Identical int             `json:"identical"`
	Clusters  []importCluster `json:"clusters"` // diverged first, then the rest
}

// stagedCard is one merged card awaiting apply. Ready means no live card exists
// (or it is byte-identical) so Apply lands it silently; a conflict differs from
// a live card and needs the force toggle. Live/Staged bodies power the expand
// diff; Added/Removed are line counts for the collapsed summary.
type stagedCard struct {
	Rel      string `json:"rel"`
	Conflict bool   `json:"conflict"`
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	Live     string `json:"live"`
	Staged   string `json:"staged"`
}

// residualFile is a decomposed CLAUDE.md left for manual placement in its repo.
// RepoPath resolves the target from the watched-repo list so the UI can show a
// live diff against the file the user would replace.
type residualFile struct {
	Rel      string `json:"rel"`
	Repo     string `json:"repo"`
	RepoPath string `json:"repoPath"`
	Staged   string `json:"staged"`
	Live     string `json:"live"`
	Missing  bool   `json:"missing"` // no matching watched repo / CLAUDE.md found
}

type importStaging struct {
	Present   bool           `json:"present"`
	Ready     []stagedCard   `json:"ready"`
	Conflicts []stagedCard   `json:"conflicts"`
	Residuals []residualFile `json:"residuals"`
}

// mergeStatus reflects the background merge job. Running is true while serve's
// spawned `culi import merge` child is alive; Done/Total drive the progress bar
// (Total is the scan's mergeable-unit count, best-effort). Err surfaces a
// failed run so the UI can offer a resume.
type mergeStatus struct {
	Running bool   `json:"running"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Err     string `json:"err,omitempty"`
	HasWork bool   `json:"hasWork"` // a scan exists with something to merge
}

type importPayload struct {
	Scan    importScan    `json:"scan"`
	Merge   mergeStatus   `json:"merge"`
	Staging importStaging `json:"staging"`
}

// ---------- merge job tracking (server state) ----------

// mergeJob tracks the single in-flight background merge. Only one runs at a
// time; the store and staging dir are shared, so a second merge or an apply
// mid-merge would race. A mutex guards all fields.
type mergeJob struct {
	mu      sync.Mutex
	running bool
	err     string
}

func (j *mergeJob) snapshot() (bool, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.running, j.err
}

// ---------- builders ----------

func (s *server) buildImport() importPayload {
	var p importPayload
	rep, err := importer.ReadReport(s.kdir)
	if err == nil {
		p.Scan = scanView(rep)
	}
	running, mergeErr := s.merge.snapshot()
	total := mergeTotal(rep)
	p.Merge = mergeStatus{
		Running: running,
		Done:    clampMergeDone(countMergeProgress(s.kdir), total),
		Total:   total,
		Err:     mergeErr,
		HasWork: p.Scan.Diverged > 0 || len(rep.ClaudeMD) > 0,
	}
	p.Staging = s.stagingView()
	return p
}

func scanView(rep importer.Report) importScan {
	sv := importScan{Scanned: true, At: humanizeTime(rep.GeneratedAt.Local().Format("2006-01-02 15:04:05"))}
	for _, r := range rep.Repos {
		sv.Repos = append(sv.Repos, importRepo{
			Name: r.Name, Path: r.Path, Agents: r.Agents, Skills: r.Skills, ClaudeMD: r.ClaudeMD,
		})
	}
	for _, c := range rep.Clusters {
		switch c.Class {
		case "diverged":
			sv.Diverged++
		case "unique":
			sv.Unique++
		default:
			sv.Identical++
		}
		sv.Clusters = append(sv.Clusters, importCluster{
			Key: c.Key, Kind: c.Kind, Name: c.Name, Class: c.Class,
			Repos: len(c.Items), Similarity: c.Similarity,
		})
	}
	// Diverged clusters first (the ones a merge acts on), then by key.
	sort.SliceStable(sv.Clusters, func(a, b int) bool {
		da, db := sv.Clusters[a].Class == "diverged", sv.Clusters[b].Class == "diverged"
		if da != db {
			return da
		}
		return sv.Clusters[a].Key < sv.Clusters[b].Key
	})
	return sv
}

// stagingView classifies every staged file the way importer.Apply would, but
// read-only: nothing is moved. Residuals are matched to their watched repo so
// the UI can diff against the CLAUDE.md the user would replace.
func (s *server) stagingView() importStaging {
	staged := filepath.Join(s.kdir, ".import", "staged")
	if _, err := os.Stat(staged); err != nil {
		return importStaging{}
	}
	out := importStaging{Present: true}
	repoByBase := map[string]string{}
	for _, r := range s.config().Repos {
		repoByBase[filepath.Base(r)] = r
	}
	_ = filepath.WalkDir(staged, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(staged, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		body, _ := os.ReadFile(path)
		if strings.HasPrefix(rel, "residual/") {
			out.Residuals = append(out.Residuals, residualView(rel, string(body), repoByBase))
			return nil
		}
		live, lerr := os.ReadFile(filepath.Join(s.kdir, filepath.FromSlash(rel)))
		if lerr != nil { // no live card → ready to land
			out.Ready = append(out.Ready, stagedCard{Rel: rel, Staged: string(body)})
			return nil
		}
		if bytes.Equal(live, body) { // identical → apply drains it silently
			out.Ready = append(out.Ready, stagedCard{Rel: rel, Staged: string(body)})
			return nil
		}
		add, rem := lineDelta(string(live), string(body))
		out.Conflicts = append(out.Conflicts, stagedCard{
			Rel: rel, Conflict: true, Added: add, Removed: rem,
			Live: string(live), Staged: string(body),
		})
		return nil
	})
	sort.Slice(out.Ready, func(a, b int) bool { return out.Ready[a].Rel < out.Ready[b].Rel })
	sort.Slice(out.Conflicts, func(a, b int) bool { return out.Conflicts[a].Rel < out.Conflicts[b].Rel })
	sort.Slice(out.Residuals, func(a, b int) bool { return out.Residuals[a].Rel < out.Residuals[b].Rel })
	return out
}

// residualView derives the target repo from the residual filename
// (residual/<repo>.CLAUDE.md) and loads that repo's live CLAUDE.md for diffing.
func residualView(rel, staged string, repoByBase map[string]string) residualFile {
	name := strings.TrimSuffix(filepath.Base(rel), ".CLAUDE.md")
	rf := residualFile{Rel: rel, Repo: name, Staged: staged, Missing: true}
	if repoPath, ok := repoByBase[name]; ok {
		rf.RepoPath = repoPath
		if live, err := os.ReadFile(filepath.Join(repoPath, "CLAUDE.md")); err == nil {
			rf.Live = string(live)
			rf.Missing = false
		}
	}
	return rf
}

// lineDelta reports how many lines the staged version adds and removes versus
// live, by set membership. Not a positional diff — for small markdown cards it
// conveys the size of the change without an LCS pass.
func lineDelta(live, staged string) (added, removed int) {
	liveSet := map[string]bool{}
	for _, ln := range strings.Split(live, "\n") {
		liveSet[ln] = true
	}
	stagedSet := map[string]bool{}
	for _, ln := range strings.Split(staged, "\n") {
		stagedSet[ln] = true
	}
	for ln := range stagedSet {
		if !liveSet[ln] {
			added++
		}
	}
	for ln := range liveSet {
		if !stagedSet[ln] {
			removed++
		}
	}
	return added, removed
}

// mergeTotal approximates the number of units a merge will step through:
// clusters plus root CLAUDE.md and Codex guidance files. Used only to scale the
// progress bar; done is capped to it in the handler.
func mergeTotal(rep importer.Report) int {
	return len(rep.Clusters) + len(rep.ClaudeMD) + len(rep.AgentsMD)
}

// countMergeProgress reads how many units the checkpoint file records as done.
func countMergeProgress(kdir string) int {
	raw, err := os.ReadFile(filepath.Join(kdir, ".import", "merge-progress.json"))
	if err != nil {
		return 0
	}
	var p struct {
		Done []string `json:"done"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return 0
	}
	return len(p.Done)
}

// ---------- handlers ----------

func (s *server) handleImport(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, s.buildImport())
}

// handleImportScan re-inventories the watched repos and persists the report,
// exactly like `culi import scan`. Read-only against the repos; fast (no LLM).
func (s *server) handleImportScan(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	if running, _ := s.merge.snapshot(); running {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "a merge is running — wait for it to finish"})
		return
	}
	repos := s.config().Repos
	rep, err := importer.ScanWithCodex(repos, codexHomeDir())
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if _, err := importer.WriteReport(s.kdir, rep); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, s.buildImport())
}

// handleImportMerge kicks off `culi import merge --resume` as a background
// child of serve, tracked so the UI can poll progress. The LLM job is minutes
// long and costs money, so it runs detached from the request; the browser
// confirms intent before calling this.
func (s *server) handleImportMerge(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	if err := s.startMerge(); err != nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "note": err.Error()})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) startMerge() error {
	s.merge.mu.Lock()
	if s.merge.running {
		s.merge.mu.Unlock()
		return errRunning
	}
	exe, err := os.Executable()
	if err != nil {
		s.merge.mu.Unlock()
		return err
	}
	cmd := exec.Command(exe, "import", "merge", "--resume")
	var stderr boundedBuffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		s.merge.mu.Unlock()
		return err
	}
	s.merge.running = true
	s.merge.err = ""
	s.merge.mu.Unlock()

	go func() {
		werr := cmd.Wait()
		s.merge.mu.Lock()
		s.merge.running = false
		if werr != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = werr.Error()
			}
			s.merge.err = msg
			log.Printf("serve: import merge failed: %s", msg)
		}
		s.merge.mu.Unlock()
	}()
	return nil
}

// handleImportApply lands the staged cards into the knowledge store (importer.
// Apply), re-syncs the index, and commits — `culi import apply`. The force flag
// (query ?force=1) overwrites conflicting live cards; without it they stay
// staged. Residuals are never applied here (they belong in their repos).
func (s *server) handleImportApply(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	if running, _ := s.merge.snapshot(); running {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "a merge is running — wait for it to finish"})
		return
	}
	force := r.URL.Query().Get("force") == "1"
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx := r.Context()
	res, err := importer.Apply(s.kdir, force)
	if err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(res.Applied) > 0 {
		if _, err := indexer.Sync(ctx, s.store, s.kdir); err != nil {
			s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if err := knowledge.Commit(s.kdir, "import: applied "+itoa(len(res.Applied))+" reviewed cards"); err != nil {
			log.Printf("serve: commit after import apply: %v", err)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "applied": len(res.Applied), "conflicts": len(res.Conflicts), "residual": len(res.Residual),
	})
}

// handleImportDiscard clears the staging area (the merge output), letting the
// operator throw away a re-merge and keep the live cards. The scan report is
// left in place. Residuals go too — they are regenerated by the next merge.
func (s *server) handleImportDiscard(w http.ResponseWriter, r *http.Request) {
	if !s.guardLocal(w, r) {
		return
	}
	if running, _ := s.merge.snapshot(); running {
		s.writeJSON(w, http.StatusConflict, map[string]string{"error": "a merge is running — wait for it to finish"})
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	staged := filepath.Join(s.kdir, ".import", "staged")
	if err := os.RemoveAll(staged); err != nil {
		s.writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = os.Remove(filepath.Join(s.kdir, ".import", "merge-progress.json"))
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// codexHomeDir mirrors cli.codexHome without importing the cli package.
func codexHomeDir() string {
	if path := strings.TrimSpace(os.Getenv("CODEX_HOME")); path != "" {
		return filepath.Clean(path)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// boundedBuffer keeps only the last ~4KB written — enough of a failed merge's
// stderr tail to explain the failure without unbounded growth over a long run.
type boundedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if b.buf.Len() > 4096 {
		tail := b.buf.Bytes()[b.buf.Len()-4096:]
		next := bytes.NewBuffer(append([]byte(nil), tail...))
		b.buf = *next
	}
	return n, err
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var errRunning = errImport("a merge is already running")

type errImport string

func (e errImport) Error() string { return string(e) }

// clampMergeDone caps a progress count to the total so the bar never exceeds
// 100% when the total estimate is low.
func clampMergeDone(done, total int) int {
	if total > 0 && done > total {
		return total
	}
	return done
}
