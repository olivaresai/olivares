// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/egress"
	"github.com/olivaresai/olivares/sdk/gitlayout"
)

// The local publication preparation uses the session runner's OS boundary, with
// no provider home, publication credential, inherited environment or transport.
type sessionGit struct {
	module      *Module
	dir, preset string
	workspace   *resolvedWorkspace
}

type runGitStatus struct {
	gitpublish.LocalStatus
	Writable bool `json:"writable"`
}

func (g sessionGit) writable() bool {
	return g.preset != PresetReadOnly && (g.workspace == nil ||
		(g.workspace.mountMode != mountRO && len(g.workspace.allowSubpaths) == 0 && len(g.workspace.readOnlyFolders) == 0))
}
func (g sessionGit) status(ctx context.Context) (runGitStatus, error) {
	reader := g
	reader.preset = PresetReadOnly
	s, err := (gitpublish.LocalRepository{Run: reader.exec}).Status(ctx)
	if g.workspace != nil && len(g.workspace.allowSubpaths) > 0 {
		files := s.Files[:0]
		for _, f := range s.Files {
			if underAllowedSubpath(g.dir, g.workspace.allowSubpaths, filepath.Join(g.dir, filepath.FromSlash(f.Path))) {
				files = append(files, f)
			}
		}
		s.Files = files
	}
	return runGitStatus{LocalStatus: s, Writable: g.writable()}, err
}
func (g sessionGit) stage(ctx context.Context, paths []string, unstage bool) error {
	if !g.writable() {
		return &runErr{http.StatusForbidden, "this session folder is read-only or has restricted folder access; Git changes are unavailable"}
	}
	return (gitpublish.LocalRepository{Run: g.exec}).Stage(ctx, paths, unstage)
}
func (g sessionGit) commit(ctx context.Context, message string, p auth.Principal) error {
	if !g.writable() {
		return &runErr{http.StatusForbidden, "this session folder does not permit Git changes"}
	}
	if p.Kind != auth.KindUser {
		return badRequest("sign in with your user profile to commit")
	}
	return (gitpublish.LocalRepository{Run: g.exec}).Commit(ctx, message, p.DisplayName, p.Email)
}
func (g sessionGit) branch(ctx context.Context, name string, create bool) error {
	if !g.writable() {
		return &runErr{http.StatusForbidden, "this session folder does not permit Git changes"}
	}
	return (gitpublish.LocalRepository{Run: g.exec}).Branch(ctx, name, create)
}

func (g sessionGit) exec(ctx context.Context, args []string, input string, identity []string) (string, error) {
	layout, ok := gitlayout.Read(g.dir)
	if !ok {
		return "", &runErr{http.StatusNotFound, "this session folder is not a Git repository"}
	}
	program, err := exec.LookPath("git")
	if err != nil {
		return "", &runErr{http.StatusServiceUnavailable, "Git is unavailable on this server"}
	}
	policy := g.module.sessionConfinement(g.dir, nil, g.preset)
	if policy == nil {
		return "", &runErr{http.StatusServiceUnavailable, "session Git requires OS confinement on this server"}
	}
	// Never inherit GIT_CONFIG_COUNT, GIT_EXEC_PATH, SSH helpers or provider homes.
	env := []EnvVar{{"PATH", os.Getenv("PATH")}, {"LANG", "C"}, {"LC_ALL", "C"},
		{"GIT_CONFIG_NOSYSTEM", "1"}, {"GIT_CONFIG_GLOBAL", "/dev/null"},
		{"GIT_TERMINAL_PROMPT", "0"}, {"GIT_NO_REPLACE_OBJECTS", "1"}, {"GIT_NO_LAZY_FETCH", "1"},
		{"GIT_OPTIONAL_LOCKS", "0"}, {"GIT_COMMON_DIR", layout.CommonDir}}
	for _, pair := range identity {
		name, value, _ := strings.Cut(pair, "=")
		env = append(env, EnvVar{name, value})
	}
	fixed := []string{"--git-dir=" + layout.GitDir, "--work-tree=" + g.dir, "--literal-pathspecs",
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.allow=never",
		"-c", "commit.gpgsign=false", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "-c", "checkout.workers=1"}
	ctx, cancel := context.WithTimeout(ctx, worktreeGitTimeout)
	defer cancel()
	spec := LaunchSpec{Program: program, Args: append(fixed, args...), Dir: g.dir, Env: env, Confinement: policy, ConfinementRequired: true,
		ConfinementRequireTruncateProtection: g.preset == PresetReadOnly}
	if g.workspace != nil {
		spec.ConfinementFiles = g.workspace.readOnlyHandles
	}
	cmd, childEnv, _, release, err := (&procRunner{}).command(ctx, spec)
	if err != nil {
		return "", err
	}
	defer release()
	cmd.Dir = g.dir
	for _, v := range childEnv {
		cmd.Env = append(cmd.Env, v.Name+"="+v.Value)
	}
	// Local Git needs no provider or host endpoint. This also confines a filter
	// introduced by a concurrent writer between the config check and execution.
	ready, closeNetwork, _, err := egress.Wrap(ctx, cmd, egress.Policy{Offline: true})
	if err != nil {
		return "", networkBoundaryErr(err)
	}
	defer closeNetwork()
	configureProcGroup(cmd)
	cmd.WaitDelay = worktreeGitWaitDelay
	stdout, stderr := &cappedBuffer{limit: 1024 * 1024}, &cappedBuffer{limit: worktreeGitOutputLimit}
	cmd.Stdout, cmd.Stderr, cmd.Stdin = stdout, stderr, strings.NewReader(input)
	if err = cmd.Start(); err != nil {
		return "", networkBoundaryErr(err)
	}
	if ready != nil {
		if err = ready(); err != nil {
			cancel()
			_ = cmd.Wait()
			return "", networkBoundaryErr(err)
		}
	}
	if err = cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		refused := &runErr{http.StatusConflict, "Git refused the operation: " + gitMessage(stderr.buf.String())}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", &gitpublish.LocalCommandExit{Code: exit.ExitCode(), Err: refused}
		}
		return "", fmt.Errorf("session Git could not finish: %w", err)
	}
	if stdout.buf.Len() >= stdout.limit {
		return "", &runErr{http.StatusUnprocessableEntity, "Git output exceeds the session limit; narrow the changes before retrying"}
	}
	return stdout.buf.String(), nil
}

type runGitRequest struct {
	Paths          []string `json:"paths,omitempty"`
	Message        string   `json:"message,omitempty"`
	Name           string   `json:"name,omitempty"`
	Create         bool     `json:"create,omitempty"`
	WorkLeaseFence *int64   `json:"work_lease_fence,omitempty"`
}

// openRunGit is the confined Git of one run: the run's folder, bound to its
// workspace and permission preset. release frees the run lock and the
// workspace's read-only handles. The run's workspace must still be the folder's.
func (m *Module) openRunGit(ctx context.Context, tenant model.TenantID, runRef string) (g sessionGit, rec model.Record, release func(), err error) {
	unlock, err := m.rt.lockRunContext(ctx, liveKey(tenant, runRef))
	if err != nil {
		return g, nil, nil, err
	}
	// held, not the named release: a bare return below overwrites release with nil.
	held := unlock
	defer func() {
		if err != nil {
			held()
		}
	}()
	rec, err = m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return g, nil, nil, err
	}
	if isolation := rec.String(colIsolation); isolation != "" && isolation != string(IsolationNative) {
		return g, nil, nil, &runErr{http.StatusUnprocessableEntity, "session Git requires native session isolation"}
	}
	ws, err := m.resolveLaunchWorkspace(ctx, tenant, rec.String(colWorkspaceRef))
	if err != nil {
		return g, nil, nil, err
	}
	if ws != nil {
		held = func() { ws.closeReadOnlyHandles(); unlock() }
	}
	dir := rec.String(colRunWorkspacePath)
	if dir == "" {
		return g, nil, nil, &runErr{http.StatusNotFound, "this session has no folder"}
	}
	// A changed registration must never grant access to yesterday's folder.
	if ws != nil && rec.String(colRunWorktreeBranch) == "" && resolvedPath(dir) != ws.rootReal {
		return g, nil, nil, &runErr{http.StatusConflict, "this session folder no longer matches its workspace"}
	}
	if ws != nil && rec.String(colRunWorktreeBranch) != "" {
		linked, linkedOK := gitlayout.Read(dir)
		registered, registeredOK := gitlayout.Read(ws.rootReal)
		if !linkedOK || !registeredOK || !linked.Linked() || linked.CommonDir != registered.CommonDir {
			return g, nil, nil, &runErr{http.StatusConflict, "this session worktree no longer belongs to its workspace"}
		}
	}
	preset := (LaunchIntent{PermissionMode: rec.String(colPermissionMode)}).Preset()
	if ws != nil && ws.mountMode == mountRO {
		preset = PresetReadOnly
	}
	return sessionGit{module: m, dir: dir, preset: preset, workspace: ws}, rec, held, nil
}

// beginRunGitWrite admits one change of the run's folder: no unfenced active
// work (or the caller's fence), a writable folder, and the audit intent sealed
// before the index, refs or worktree are touched. Neither a commit message nor
// file contents enter the ledger, only the hash of the request.
func (m *Module) beginRunGitWrite(ctx context.Context, tenant model.TenantID, rec model.Record, g sessionGit, p auth.Principal, op string, request any, fence *int64) error {
	var err error
	if fence != nil {
		_, err = m.assertRunWorkLease(ctx, tenant, rec.String(colRunRef), *fence)
	} else {
		err = m.refuseUnfencedActiveWork(ctx, tenant, rec)
	}
	if err != nil {
		return err
	}
	if !g.writable() {
		return &runErr{http.StatusForbidden, "this session folder is read-only or has restricted folder access; Git changes are unavailable"}
	}
	payload, _ := json.Marshal(request)
	hash := sha256.Sum256(payload)
	audit := wsMutationInput{workspaceRef: rec.String(colWorkspaceRef), op: "git-" + op, path: rec.String(colRunRef), contentHash: hex.EncodeToString(hash[:]), actor: p.Actor(), actorKind: p.ActorKind()}
	if g.workspace != nil {
		audit.workspaceID = g.workspace.id
	}
	return m.sealWorkspaceMutation(ctx, tenant, audit)
}

func (m *Module) handleRunGit(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.Data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("runtime not available"))
		return
	}
	g, rec, release, err := m.openRunGit(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		writeRuntimeControlErr(w, err)
		return
	}
	defer release()
	if r.Method == http.MethodGet {
		status, err := g.status(r.Context())
		if err != nil {
			writeSessionGitErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
		return
	}
	var body runGitRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}
	if body.WorkLeaseFence != nil && *body.WorkLeaseFence <= 0 {
		writeRunErr(w, badRequest("work_lease_fence must be positive"))
		return
	}
	action := path.Base(r.URL.Path)
	if err := m.beginRunGitWrite(r.Context(), mc.Tenant, rec, g, mc.Principal, action, body, body.WorkLeaseFence); err != nil {
		writeRuntimeControlErr(w, err)
		return
	}
	switch action {
	case "stage", "unstage":
		err = g.stage(r.Context(), body.Paths, action == "unstage")
	case "commit":
		err = g.commit(r.Context(), body.Message, mc.Principal)
	case "branch":
		err = g.branch(r.Context(), body.Name, body.Create)
	default:
		err = badRequest("unknown Git action")
	}
	if err != nil {
		writeSessionGitErr(w, err)
		return
	}
	// colRunWorktreeBranch records the branch cleanup owns, not today's checkout.
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// CommitRun commits every change of a run's folder as the caller and names the
// branch, commit and tree to publish: the local half of shipping a worktree
// session. It is the Git routes' own door (the confined Git, the work-lease and
// read-only checks, the audit intent), and the run is read in workspace only,
// so a run elsewhere is store.ErrNotFound. A refusal of the folder (read-only,
// active work, a missing profile or message) wraps gitpublish.ErrLocalInput, and
// a folder with no branch or no commit answers ErrNoBranch or ErrNoCommit.
func (m *Module) CommitRun(ctx context.Context, p auth.Principal, tenant model.TenantID, workspace model.ID, runRef, message string) (gitpublish.LocalHead, error) {
	if m == nil || m.Data == nil {
		return gitpublish.LocalHead{}, errors.New("sessions: run Git has no data handle")
	}
	dir, err := m.ReadRunWorkspacePath(ctx, tenant, workspace, runRef)
	if err != nil {
		return gitpublish.LocalHead{}, err
	}
	g, rec, release, err := m.openRunGit(ctx, tenant, runRef)
	if err != nil {
		return gitpublish.LocalHead{}, refusedRunGit(err)
	}
	defer release()
	if g.dir != dir {
		return gitpublish.LocalHead{}, store.ErrNotFound
	}
	if p.Kind != auth.KindUser {
		return gitpublish.LocalHead{}, fmt.Errorf("%w: sign in with your user profile to commit", gitpublish.ErrLocalInput)
	}
	// Only a folder with changes is written to: a clean one is read, and a refused
	// message or profile is answered before the audit intent is sealed.
	status, err := g.status(ctx)
	if err != nil {
		return gitpublish.LocalHead{}, refusedRunGit(err)
	}
	if len(status.Files) > 0 {
		if err := gitpublish.CommitInput(message, p.DisplayName, p.Email); err != nil {
			return gitpublish.LocalHead{}, err
		}
		if err := m.beginRunGitWrite(ctx, tenant, rec, g, p, "ship", runGitRequest{Message: message}, nil); err != nil {
			return gitpublish.LocalHead{}, refusedRunGit(err)
		}
	}
	head, err := (gitpublish.LocalRepository{Run: g.exec}).CommitAll(ctx, message, p.DisplayName, p.Email)
	return head, refusedRunGit(err)
}

// refusedRunGit turns a refusal of the run's folder, which the Git routes
// answer with a status, into gitpublish.ErrLocalInput; anything else stays.
func refusedRunGit(err error) error {
	var re *runErr
	switch {
	case errors.As(err, &re):
		return fmt.Errorf("%w: %s", gitpublish.ErrLocalInput, re.msg)
	case asWorkError(err) != nil:
		return fmt.Errorf("%w: %s", gitpublish.ErrLocalInput, err)
	}
	return err
}

func writeSessionGitErr(w http.ResponseWriter, err error) {
	if errors.Is(err, gitpublish.ErrLocalInput) {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	writeRunErr(w, err)
}
