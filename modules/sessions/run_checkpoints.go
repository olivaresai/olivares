// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// A SESSION'S TURNS, one checkpoint each (#3433): what each turn changed in the session's
// folder, and the folder as it was before any turn, to rewind to.
//
// The folder is the agent's, so nothing here runs in it or reads a setting from it. The
// snapshots live in a git repository of the engine's own, under the data directory (a
// confined session never reaches it), with the session's folder named as its work tree:
// git then reads the folder's files and its .gitignore and nothing else, no hook, filter
// or configuration the agent could have written. A snapshot is one tree object per turn
// start, taken before the tool is handed the message; a turn's files are the difference
// between its tree and the next turn's (the last one's, the folder now).
//
// A rewind writes into the folder as the engine, so it goes through os.Root, as the
// reads of run_changes.go do: git only materializes the files to restore into a
// directory of the engine's own, and each file is placed from there, so no path and no
// link can lead a write out of the folder. It is the session's own write path:
// openRunGit's admission, a folder that is not read-only, and the audit intent sealed
// before the first file is touched (beginRunGitWrite).
//
// ponytail: files the folder's .gitignore names, empty folders and the contents of a
// nested repository are not in a snapshot, so they are neither listed nor rewound. A
// folder so large that the first snapshot passes checkpointDeadline gets no checkpoint
// for that turn (the log says so) and the turn's changes join the previous turn's.
// Upgrade trigger: a person asks to rewind an ignored file, or the log shows missed
// turns; then snapshot in the background after the message is sent.
const (
	// checkpointDeadline bounds the snapshot a message waits for.
	checkpointDeadline = 20 * time.Second
	// checkpointMaxTurns is the most turns kept; the next message after it is sent
	// without a checkpoint.
	checkpointMaxTurns = 1000
	// checkpointPromptMax is how much of a message is kept with its turn: the index is
	// rewritten whole on every turn and listed whole, so 1000 turns stay under 16 MiB.
	checkpointPromptMax = 16 << 10
	checkpointIndexFile = "turns.json"
	checkpointRepoDir   = "repo.git"
	// checkpointBatch is how many paths one git call materializes.
	checkpointBatch = 200
	// checkpointAttributes: nothing the folder says may convert a file on its way in or out.
	checkpointAttributes = "* -text -ident -filter -working-tree-encoding\n"
)

var errNoCheckpoints = errors.New("turn checkpoints are not kept on this node")

// checkpointTurn is one turn as kept: its prompt, the tree its folder started from and,
// once the next turn (or a rewind) closed it, the tree it ended with and the files
// between them.
type checkpointTurn struct {
	Seq       int                `json:"seq"`
	Prompt    string             `json:"prompt"`
	Clipped   bool               `json:"clipped,omitempty"`
	At        string             `json:"at"`
	Start     string             `json:"start"`
	End       string             `json:"end,omitempty"`
	Files     []worktreeDiffFile `json:"files,omitempty"`
	Truncated bool               `json:"truncated,omitempty"`
}

type runCheckpointDTO struct {
	Seq    int    `json:"seq"`
	Prompt string `json:"prompt"`
	// Clipped is true when Prompt is the start of a longer message.
	Clipped bool   `json:"clipped"`
	At      string `json:"at"`
	// Open is true for the newest turn until the next one starts: its files are the
	// folder's changes so far.
	Open      bool               `json:"open"`
	Files     []worktreeDiffFile `json:"files"`
	Truncated bool               `json:"truncated"`
}

type runCheckpointsDTO struct {
	// Available is false when this session's folder cannot be checkpointed; Reason says why.
	Available bool               `json:"available"`
	Reason    string             `json:"reason,omitempty"`
	Turns     []runCheckpointDTO `json:"turns"`
}

type runRewindRequest struct {
	// To is start (the folder as it was before the turn) or end (as it was when the turn
	// ended, which undoes a rewind made after it).
	To string `json:"to"`
}

type rewindFile struct {
	Path string `json:"path"`
	// Action is restored (the file is back as it was) or removed (it did not exist then).
	Action string `json:"action"`
}

type runRewindDTO struct {
	Seq   int          `json:"seq"`
	To    string       `json:"to"`
	Files []rewindFile `json:"files"`
}

// WithCheckpointRoot sets where each session's turn checkpoints are kept. Unset, none are
// taken and the routes answer that they are not available.
func WithCheckpointRoot(dir string) Option {
	return func(m *Module) {
		if filepath.IsAbs(dir) {
			m.rt.checkpointRoot = filepath.Clean(dir)
		}
	}
}

// checkpointRunDir is where the run's checkpoints are kept (runDirUnder).
func (m *Module) checkpointRunDir(tenant model.TenantID, runRef string) (string, error) {
	if m.rt.checkpointRoot == "" {
		return "", errNoCheckpoints
	}
	return runDirUnder(m.rt.checkpointRoot, tenant, runRef)
}

func checkpointLockKey(tenant model.TenantID, runRef string) string {
	return "checkpoints|" + liveKey(tenant, runRef)
}

// shadowGit runs git on the checkpoint repository with folder as its work tree.
func shadowGit(ctx context.Context, repo, folder string, args ...string) (string, error) {
	return gitOK(ctx, repo, append([]string{
		"--git-dir=" + repo, "--work-tree=" + folder,
		"-c", "core.bare=false", "-c", "core.autocrlf=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false",
	}, args...)...)
}

// ensureCheckpointRepo makes the run's repository the first time and says where it is.
func ensureCheckpointRepo(ctx context.Context, dir string) (string, error) {
	repo := filepath.Join(dir, checkpointRepoDir)
	if _, err := os.Stat(filepath.Join(repo, "HEAD")); err == nil {
		return repo, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if _, err := gitOK(ctx, dir, "init", "--quiet", "--bare", repo); err != nil {
		return "", err
	}
	// The folders run_changes.go does not call the session's work are not snapshotted.
	var skip strings.Builder
	for _, name := range slices.Sorted(maps.Keys(changesSkippedDirs)) {
		skip.WriteString(name + "/\n")
	}
	if err := os.MkdirAll(filepath.Join(repo, "info"), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(repo, "info", "exclude"), []byte(skip.String()), 0o600); err != nil {
		return "", err
	}
	return repo, os.WriteFile(filepath.Join(repo, "info", "attributes"), []byte(checkpointAttributes), 0o600)
}

// checkpointSnapshot records the folder as a tree and returns its id. The repository's
// index is the cache, so a snapshot reads again only what changed.
func checkpointSnapshot(ctx context.Context, repo, folder string) (string, error) {
	if _, err := shadowGit(ctx, repo, folder, "add", "--all"); err != nil {
		// A git stopped by the deadline leaves its lock, which would refuse every snapshot after.
		_ = os.Remove(filepath.Join(repo, "index.lock"))
		return "", err
	}
	out, err := shadowGit(ctx, repo, folder, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(out)
	if !validGitObjectID(tree) {
		return "", errors.New("git did not name the snapshot")
	}
	// A ref keeps the tree out of any prune.
	if _, err := gitOK(ctx, repo, "update-ref", "refs/trees/"+tree, tree); err != nil {
		return "", err
	}
	return tree, nil
}

// checkpointDiff lists what differs between two snapshots, as the folder at b is to a.
func checkpointDiff(ctx context.Context, repo, a, b string) ([]worktreeDiffFile, bool, error) {
	if a == b {
		return []worktreeDiffFile{}, false, nil
	}
	out, err := gitOK(ctx, repo, "diff-tree", "-r", "-z", "--name-status", "--no-renames", a, b)
	if err != nil {
		return nil, false, err
	}
	files, truncated := worktreeDiffFiles(out, nil)
	return files, truncated, nil
}

func readCheckpointTurns(dir string) ([]checkpointTurn, error) {
	raw, err := os.ReadFile(filepath.Join(dir, checkpointIndexFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var turns []checkpointTurn
	return turns, json.Unmarshal(raw, &turns)
}

// writeCheckpointTurns replaces the index whole. Every caller holds the run's checkpoint
// lock, so the temporary name is its own.
func writeCheckpointTurns(dir string, turns []checkpointTurn) error {
	raw, err := json.Marshal(turns)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, checkpointIndexFile+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// Synced before the rename: a crash must leave the old index or the new one, never an
	// empty file that would fail every later read of this session's turns.
	if _, err := f.Write(raw); err == nil {
		err = f.Sync()
	}
	if err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, checkpointIndexFile))
}

// closeCheckpointTurn ends a turn at the tree end and keeps what it changed.
func closeCheckpointTurn(ctx context.Context, repo string, t *checkpointTurn, end string) error {
	files, truncated, err := checkpointDiff(ctx, repo, t.Start, end)
	if err != nil {
		return err
	}
	t.End, t.Files, t.Truncated = end, files, truncated
	return nil
}

// beginCheckpoint takes the snapshot a message's turn starts from, before the message goes
// to the tool, and returns what keeps the turn once the tool took the message. A message
// into a turn that is already open (a steer) starts none. Nothing here refuses a
// message: a snapshot that fails is said in the log and the turn goes without one.
func (m *Module) beginCheckpoint(ctx context.Context, tenant model.TenantID, runRef, text string, rec model.Record) func(accepted bool) {
	nothing := func(bool) {}
	folder := rec.String(colRunWorkspacePath)
	dir, err := m.checkpointRunDir(tenant, runRef)
	if err != nil || folder == "" || m.queueHold(rec) == queueHoldTurn {
		return nothing
	}
	ctx, cancel := context.WithTimeout(ctx, checkpointDeadline)
	defer cancel()
	tree, err := m.snapshotRun(ctx, tenant, runRef, dir, folder)
	if err != nil {
		m.warnf("session checkpoint not taken", "run_ref", runRef, "error", err)
		return nothing
	}
	return func(accepted bool) {
		if !accepted {
			return
		}
		if err := m.appendCheckpoint(context.WithoutCancel(ctx), tenant, runRef, dir, tree, text); err != nil {
			m.warnf("session checkpoint not kept", "run_ref", runRef, "error", err)
		}
	}
}

// snapshotRun is checkpointSnapshot for a run, under its checkpoint lock.
func (m *Module) snapshotRun(ctx context.Context, tenant model.TenantID, runRef, dir, folder string) (string, error) {
	release, err := m.rt.lockRunContext(ctx, checkpointLockKey(tenant, runRef))
	if err != nil {
		return "", err
	}
	defer release()
	repo, err := ensureCheckpointRepo(ctx, dir)
	if err != nil {
		return "", err
	}
	return checkpointSnapshot(ctx, repo, folder)
}

// appendCheckpoint keeps the turn that starts at tree, closing the one before it there.
func (m *Module) appendCheckpoint(ctx context.Context, tenant model.TenantID, runRef, dir, tree, prompt string) error {
	release, err := m.rt.lockRunContext(ctx, checkpointLockKey(tenant, runRef))
	if err != nil {
		return err
	}
	defer release()
	turns, err := readCheckpointTurns(dir)
	if err != nil {
		return err
	}
	if len(turns) >= checkpointMaxTurns {
		return fmt.Errorf("this session has the most turns kept (%d)", checkpointMaxTurns)
	}
	seq := 1
	if n := len(turns); n > 0 {
		if turns[n-1].End == "" {
			if err := closeCheckpointTurn(ctx, filepath.Join(dir, checkpointRepoDir), &turns[n-1], tree); err != nil {
				return err
			}
		}
		seq = turns[n-1].Seq + 1
	}
	turn := checkpointTurn{Seq: seq, Prompt: prompt, At: m.now().UTC().Format(time.RFC3339), Start: tree}
	if len(prompt) > checkpointPromptMax {
		turn.Prompt, turn.Clipped = strings.ToValidUTF8(prompt[:checkpointPromptMax], ""), true
	}
	return writeCheckpointTurns(dir, append(turns, turn))
}

// removeCheckpoints deletes what a released session kept.
func (m *Module) removeCheckpoints(tenant model.TenantID, runRef string) {
	dir, err := m.checkpointRunDir(tenant, runRef)
	if err == nil {
		err = os.RemoveAll(dir)
	}
	if err != nil && !errors.Is(err, errNoCheckpoints) {
		m.warnf("sessions: the checkpoints of a released session were not removed", "run_ref", runRef, "error", err)
	}
}

// checkpointUnavailable says why a run's folder cannot be checkpointed, "" when it can.
func (m *Module) checkpointUnavailable(tenant model.TenantID, runRef, folder string) (string, error) {
	if _, err := m.checkpointRunDir(tenant, runRef); err != nil {
		if errors.Is(err, errNoCheckpoints) {
			return err.Error(), nil
		}
		return "", err
	}
	if folder == "" {
		return "this session has no folder", nil
	}
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		return "the session's folder is gone", nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "git is not available on this node", nil
	}
	return "", nil
}

// handleRunCheckpoints lists the session's turns, oldest first: the prompt of each and the
// files it changed, read from snapshots the engine took (nothing is executed in the
// folder). The newest turn lists the folder's changes so far. A session whose folder
// cannot be checkpointed answers available false and why.
func (m *Module) handleRunCheckpoints(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := chi.URLParam(r, "ref")
	rec, err := m.loadRun(r.Context(), mc.Tenant, ref)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	folder := rec.String(colRunWorkspacePath)
	out := runCheckpointsDTO{Turns: []runCheckpointDTO{}}
	if out.Reason, err = m.checkpointUnavailable(mc.Tenant, ref, folder); err != nil {
		writeRunErr(w, err)
		return
	}
	if out.Reason != "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	out.Available = true
	dir, _ := m.checkpointRunDir(mc.Tenant, ref)
	release, err := m.rt.lockRunContext(r.Context(), checkpointLockKey(mc.Tenant, ref))
	if err != nil {
		writeRunErr(w, err)
		return
	}
	defer release()
	turns, err := readCheckpointTurns(dir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("could not read the session's turns"))
		return
	}
	for i, t := range turns {
		dto := runCheckpointDTO{Seq: t.Seq, Prompt: t.Prompt, Clipped: t.Clipped, At: t.At, Files: t.Files, Truncated: t.Truncated}
		if t.End == "" {
			// The newest turn: its files are what the folder differs by now.
			dto.Open = i == len(turns)-1
			repo := filepath.Join(dir, checkpointRepoDir)
			now, err := checkpointSnapshot(r.Context(), repo, folder)
			if err == nil {
				dto.Files, dto.Truncated, err = checkpointDiff(r.Context(), repo, t.Start, now)
			}
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, errorBody("could not read the session's folder"))
				return
			}
		}
		if dto.Files == nil {
			dto.Files = []worktreeDiffFile{}
		}
		out.Turns = append(out.Turns, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRunRewind puts the session's folder back as it was before a turn (to start), or
// as it was when the turn ended (to end, which undoes a rewind). Files the turns since
// changed or added are restored or removed; nothing else in the folder is touched, and
// the conversation of the tool is not changed. It needs the run's write permission, a
// folder that is not read-only, and a session that is not working on a turn; the intent
// is sealed in the ledger before the first file changes.
func (m *Module) handleRunRewind(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.refuseForeignSteering(w, r, mc) {
		return
	}
	ref := chi.URLParam(r, "ref")
	seq, err := strconv.Atoi(chi.URLParam(r, "seq"))
	if err != nil || seq < 1 {
		writeJSON(w, http.StatusBadRequest, errorBody("seq must be the number of a turn"))
		return
	}
	var body runRewindRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if body.To != "start" && body.To != "end" {
		writeJSON(w, http.StatusBadRequest, errorBody("to must be start or end"))
		return
	}
	dir, err := m.checkpointRunDir(mc.Tenant, ref)
	if err != nil {
		writeRunErr(w, &runErr{http.StatusServiceUnavailable, err.Error()})
		return
	}
	g, rec, release, err := m.openRunGit(r.Context(), mc.Tenant, ref)
	if err != nil {
		writeRuntimeControlErr(w, err)
		return
	}
	defer release()
	if hold := m.queueHold(rec); hold == queueHoldTurn || hold == queueHoldApproval {
		writeRunErr(w, conflictErr("the session is working: interrupt it, or wait for its turn to end, before rewinding"))
		return
	}
	if g.workspace != nil && g.workspace.mountMode == mountNoDelete {
		writeRunErr(w, &runErr{http.StatusForbidden, "this session folder does not allow removing files, so it cannot be rewound"})
		return
	}
	out, err := m.rewind(r.Context(), mc, rec, g, dir, seq, body.To)
	if err != nil {
		writeRuntimeControlErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// rewind is handleRunRewind after admission, under the run's lock.
func (m *Module) rewind(ctx context.Context, mc api.ModuleContext, rec model.Record, g sessionGit, dir string, seq int, to string) (runRewindDTO, error) {
	ref := rec.String(colRunRef)
	release, err := m.rt.lockRunContext(ctx, checkpointLockKey(mc.Tenant, ref))
	if err != nil {
		return runRewindDTO{}, err
	}
	defer release()
	turns, err := readCheckpointTurns(dir)
	if err != nil {
		return runRewindDTO{}, err
	}
	i := slices.IndexFunc(turns, func(t checkpointTurn) bool { return t.Seq == seq })
	if i < 0 {
		return runRewindDTO{}, &runErr{http.StatusNotFound, "this session has no such turn"}
	}
	repo := filepath.Join(dir, checkpointRepoDir)
	// The folder as it is now closes the newest turn, so the state before the rewind is
	// kept and a rewind can be undone.
	now, err := checkpointSnapshot(ctx, repo, g.dir)
	if err != nil {
		return runRewindDTO{}, err
	}
	if last := len(turns) - 1; turns[last].End == "" {
		if err := closeCheckpointTurn(ctx, repo, &turns[last], now); err != nil {
			return runRewindDTO{}, err
		}
		if err := writeCheckpointTurns(dir, turns); err != nil {
			return runRewindDTO{}, err
		}
	}
	target := turns[i].Start
	if to == "end" {
		target = turns[i].End
	}
	files, truncated, err := checkpointDiff(ctx, repo, now, target)
	if err != nil {
		return runRewindDTO{}, err
	}
	if truncated {
		return runRewindDTO{}, &runErr{http.StatusUnprocessableEntity, "too many files differ to rewind here"}
	}
	if err := m.beginRunGitWrite(ctx, mc.Tenant, rec, g, mc.Principal, "rewind", map[string]any{"seq": seq, "to": to}, nil); err != nil {
		return runRewindDTO{}, err
	}
	if err := restoreCheckpoint(ctx, dir, repo, g.dir, target, files); err != nil {
		return runRewindDTO{}, &runErr{http.StatusInternalServerError, "the rewind did not finish: " + gitMessage(err.Error())}
	}
	out := runRewindDTO{Seq: seq, To: to, Files: make([]rewindFile, 0, len(files))}
	for _, f := range files {
		// files read as the target is to now: a path the target has is restored, one it lacks removed.
		action := "restored"
		if f.Status == "deleted" {
			action = "removed"
		}
		out.Files = append(out.Files, rewindFile{Path: f.Path, Action: action})
	}
	return out, nil
}

// restoreCheckpoint makes the folder hold what the tree target holds at each of files
// (the diff from the folder now to target): a path only the folder has is removed, any
// other is placed from a copy git materializes in a directory of the engine's own.
func restoreCheckpoint(ctx context.Context, dir, repo, folder, target string, files []worktreeDiffFile) error {
	root, err := os.OpenRoot(folder)
	if err != nil {
		return err
	}
	defer root.Close()
	var place []string
	var failed []error
	for _, f := range files {
		if !isRepoRelativePath(f.Path) {
			failed = append(failed, fmt.Errorf("%q is not a path of the folder", f.Path))
			continue
		}
		if f.Status == "deleted" { // in the folder now, not in target
			failed = append(failed, removeCheckpointPath(root, f.Path))
		} else {
			place = append(place, f.Path)
		}
	}
	if len(place) > 0 {
		staging, err := os.MkdirTemp(dir, "restore-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(staging)
		if _, err := gitOK(ctx, repo, "read-tree", target); err != nil {
			return err
		}
		for batch := range slices.Chunk(place, checkpointBatch) {
			args := append([]string{"checkout-index", "-f", "--prefix=" + staging + "/", "--"}, batch...)
			if _, err := shadowGit(ctx, repo, folder, args...); err != nil {
				return err
			}
			for _, p := range batch {
				failed = append(failed, placeCheckpointFile(root, filepath.Join(staging, filepath.FromSlash(p)), p))
			}
		}
	}
	return errors.Join(failed...)
}

// removeCheckpointPath removes a file the turns added, and the folders it leaves empty.
func removeCheckpointPath(root *os.Root, rel string) error {
	if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
		if root.Remove(dir) != nil {
			break // not empty, or not ours to remove
		}
	}
	return nil
}

// placeCheckpointFile puts the file git materialized at staged into the folder at rel,
// replacing what is there.
func placeCheckpointFile(root *os.Root, staged, rel string) error {
	info, err := os.Lstat(staged)
	if err != nil {
		return err
	}
	if info.IsDir() { // a nested repository: only its place
		return root.MkdirAll(rel, 0o755)
	}
	if dir := path.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		to, err := os.Readlink(staged)
		if err != nil {
			return err
		}
		return root.Symlink(to, rel)
	}
	src, err := os.Open(staged)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}
