// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workspace.go is the governed workspace plane: the registry CRUD, the
// ref→path RESOLUTION that launch consumes, and the file operations
// (list/stat/read/write/mkdir/move/delete) — each jailed to the workspace root,
// size-bounded, DLP-classified on read, and audit-anchored. The host filesystem is
// accessed via os here in the control-plane process; the jail (workspace_fs.go) is
// the single confinement point.

// maxListPage bounds one directory page (a workspace dir can be huge; the client
// paginates by name).
const (
	defaultListPage = 500
	maxListPage     = 2000
)

// resolvedWorkspace is a workspace row resolved for use: its canonical root and the
// governed policy. rootReal is EvalSymlinks-resolved so the jail compares real paths.
type resolvedWorkspace struct {
	id            model.ID
	ref           string
	rootReal      string
	mountMode     string
	containerTgt  string
	dlpMode       string
	maxReadBytes  int64
	allowSubpaths []string
}

// CreateWorkspaceParams is the validated input to register a workspace.
type CreateWorkspaceParams struct {
	Name            string
	RootPath        string
	MountMode       string
	ContainerTarget string
	AllowSubpaths   []string
	MaxReadBytes    int64
	DLPMode         string
	Actor           string
	ActorKind       string
}

// createWorkspace validates the registration, canonicalizes the host root (which MUST
// exist and be a directory), persists the row, and seals a register audit.
func (m *Module) createWorkspace(ctx context.Context, tenant model.TenantID, p CreateWorkspaceParams) (workspaceDTO, error) {
	if err := validateWorkspace(&p); err != nil {
		return workspaceDTO{}, err
	}
	// The root must be an absolute, existing directory; we store its CANONICAL real
	// path so a later symlink swap cannot redirect the jail.
	rootReal, err := filepath.EvalSymlinks(p.RootPath)
	if err != nil {
		return workspaceDTO{}, badRequest("root_path does not exist or is not resolvable")
	}
	info, err := os.Stat(rootReal)
	if err != nil || !info.IsDir() {
		return workspaceDTO{}, badRequest("root_path is not a directory")
	}

	ref := string(model.NewID())
	subpathsJSON, err := encodeSubpaths(p.AllowSubpaths)
	if err != nil {
		return workspaceDTO{}, badRequest("invalid allow_subpaths")
	}

	var out workspaceDTO
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		row := model.Record{
			colWsRef:          ref,
			colWsRootPath:     rootReal,
			colWsMountMode:    p.MountMode,
			colWsContainerTgt: p.ContainerTarget,
			colWsDLPMode:      p.DLPMode,
			colWsState:        wsActive,
		}
		setIf(row, colWsName, p.Name)
		if subpathsJSON != "" {
			row[colWsAllowSubpaths] = subpathsJSON
		}
		if p.MaxReadBytes > 0 {
			row[colWsMaxReadBytes] = p.MaxReadBytes
		}
		created, err := repo.Create(ctx, row)
		if err != nil {
			return err
		}
		wsID := model.ID(created.String(model.ColID))
		// Seal the registration ATOMICALLY with the insert (deny-closed: if the audit
		// cannot be appended, the whole registration rolls back — no row granting
		// filesystem reach without evidence).
		if aerr := appendWorkspaceAudit(ctx, sc, wsMutationInput{
			workspaceID: wsID, workspaceRef: ref, op: "register", path: rootReal,
			actor: p.Actor, actorKind: p.ActorKind,
		}); aerr != nil {
			return aerr
		}
		out = toWorkspaceDTO(created)
		return nil
	})
	if err != nil {
		return workspaceDTO{}, err
	}
	return out, nil
}

// listWorkspaces returns the tenant's workspaces, most-recent first.
func (m *Module) listWorkspaces(ctx context.Context, tenant model.TenantID, q model.Query) (listResponse[workspaceDTO], error) {
	q.Cursor = ""
	q.Sort = []model.Sort{{Column: model.ColCreatedAt, Desc: true}}
	out := listResponse[workspaceDTO]{Items: []workspaceDTO{}}
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toWorkspaceDTO(rec))
		}
		out.HasMore = page.HasMore
		return nil
	})
	return out, err
}

// getWorkspace returns one workspace by ref.
func (m *Module) getWorkspace(ctx context.Context, tenant model.TenantID, ref string) (workspaceDTO, error) {
	rec, err := m.loadWorkspaceRec(ctx, tenant, ref)
	if err != nil {
		return workspaceDTO{}, err
	}
	return toWorkspaceDTO(rec), nil
}

// deleteWorkspace removes a workspace registration. It NEVER touches host files (only
// the registration); the deregister audit and the row delete are ATOMIC (deny-closed:
// no deregistration without recorded evidence).
func (m *Module) deleteWorkspace(ctx context.Context, tenant model.TenantID, ref, actor, actorKind string) error {
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		r, err := findWorkspaceRec(ctx, repo, ref)
		if err != nil {
			return err
		}
		id, perr := model.ParseID(r.String(model.ColID))
		if perr != nil {
			return badRequest("malformed workspace id")
		}
		if aerr := appendWorkspaceAudit(ctx, sc, wsMutationInput{
			workspaceID: id, workspaceRef: ref, op: "deregister", path: r.String(colWsRootPath),
			actor: actor, actorKind: actorKind,
		}); aerr != nil {
			return aerr
		}
		return repo.Delete(ctx, id)
	})
}

// resolveWorkspace loads a workspace by ref, rejects a disabled one, and canonicalizes
// its root for the jail. It is the single resolution used by BOTH the file API and the
// launch ref→path formalization.
func (m *Module) resolveWorkspace(ctx context.Context, tenant model.TenantID, ref string) (*resolvedWorkspace, error) {
	rec, err := m.loadWorkspaceRec(ctx, tenant, ref)
	if err != nil {
		return nil, err
	}
	if rec.String(colWsState) == wsDisabled {
		return nil, conflictErr("workspace is disabled")
	}
	root := rec.String(colWsRootPath)
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, &runErr{http.StatusFailedDependency, "workspace root path is unavailable on this node"}
	}
	info, err := os.Stat(rootReal)
	if err != nil || !info.IsDir() {
		return nil, &runErr{http.StatusFailedDependency, "workspace root is not a directory on this node"}
	}
	tgt := rec.String(colWsContainerTgt)
	if tgt == "" {
		tgt = defaultContainerTarget
	}
	return &resolvedWorkspace{
		id:            model.ID(rec.String(model.ColID)),
		ref:           ref,
		rootReal:      rootReal,
		mountMode:     rec.String(colWsMountMode),
		containerTgt:  tgt,
		dlpMode:       rec.String(colWsDLPMode),
		maxReadBytes:  workspaceMaxRead(rec),
		allowSubpaths: decodeSubpaths(rec),
	}, nil
}

// resolveLaunchWorkspace resolves a run's workspace_ref for launch (the ref→path
// formalization deferred). An empty ref means no workspace (the behavior;
// the native runner uses the process cwd). A non-empty ref MUST resolve to a
// registered, active, tenant-owned workspace — otherwise the launch is DENIED
// (deny-closed). An unavailable root (e.g. wrong node) surfaces as 424.
func (m *Module) resolveLaunchWorkspace(ctx context.Context, tenant model.TenantID, ref string) (*resolvedWorkspace, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, nil
	}
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		var re *runErr
		if errors.As(err, &re) && re.status == http.StatusNotFound {
			return nil, badRequest("workspace_ref is not a registered workspace")
		}
		return nil, err
	}
	return ws, nil
}

// jail resolves rel within the workspace and enforces the allow-subpath policy.
func (ws *resolvedWorkspace) jail(rel string, mustExist bool) (string, error) {
	abs, err := resolveWithin(ws.rootReal, rel, mustExist)
	if err != nil {
		return "", err
	}
	if !underAllowedSubpath(ws.rootReal, ws.allowSubpaths, abs) {
		return "", errTraversal
	}
	return abs, nil
}

// listFiles lists one directory level under the workspace, paginated by name.
func (m *Module) listFiles(ctx context.Context, tenant model.TenantID, ref, rel string, limit int, cursor, actor, actorKind string) (fileListResponse, error) {
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		return fileListResponse{}, err
	}
	abs, err := ws.jail(rel, true)
	if err != nil {
		return fileListResponse{}, mapFSErr(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fileListResponse{}, mapFSErr(err)
	}
	if !info.IsDir() {
		return fileListResponse{}, badRequest("path is not a directory")
	}
	all, err := listDir(abs, ws.rootReal)
	if err != nil {
		return fileListResponse{}, mapFSErr(err)
	}
	page, next, more := pageByName(all, cursor, limit)
	m.auditWorkspaceRead(ctx, tenant, wsMutationInput{op: "list", workspaceID: ws.id, workspaceRef: ref, path: normRel(rel), actor: actor, actorKind: actorKind})
	return fileListResponse{Path: normRel(rel), Entries: page, Cursor: next, HasMore: more}, nil
}

// statFile returns metadata for one entry.
func (m *Module) statFile(ctx context.Context, tenant model.TenantID, ref, rel, actor, actorKind string) (fileEntry, error) {
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		return fileEntry{}, err
	}
	abs, err := ws.jail(rel, true)
	if err != nil {
		return fileEntry{}, mapFSErr(err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return fileEntry{}, mapFSErr(err)
	}
	relPath, _ := filepath.Rel(ws.rootReal, abs)
	m.auditWorkspaceRead(ctx, tenant, wsMutationInput{op: "stat", workspaceID: ws.id, workspaceRef: ref, path: normRel(rel), actor: actor, actorKind: actorKind})
	return entryFromInfo(filepath.Base(abs), filepath.ToSlash(relPath), info), nil
}

// readFile reads a file's content (size-bounded), applies the workspace's DLP posture,
// and audits the read with the detected classes (never the content).
func (m *Module) readFile(ctx context.Context, tenant model.TenantID, ref, rel, actor, actorKind string) (fileReadResponse, error) {
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		return fileReadResponse{}, err
	}
	abs, err := ws.jail(rel, true)
	if err != nil {
		return fileReadResponse{}, mapFSErr(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fileReadResponse{}, mapFSErr(err)
	}
	if info.IsDir() {
		return fileReadResponse{}, badRequest("path is a directory (use list)")
	}
	f, err := os.Open(abs)
	if err != nil {
		return fileReadResponse{}, mapFSErr(err)
	}
	defer func() { _ = f.Close() }()
	cap := ws.maxReadBytes
	data := make([]byte, 0, min64(info.Size(), cap)+1)
	buf := make([]byte, 32*1024)
	truncated := false
	for int64(len(data)) <= cap {
		n, rerr := f.Read(buf)
		if n > 0 {
			data = append(data, buf[:n]...)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fileReadResponse{}, mapFSErr(rerr)
		}
	}
	if int64(len(data)) > cap {
		data = data[:cap]
		truncated = true
	}

	hits, deny := m.classifyContent(ctx, ws.dlpMode, data)
	classes := dlpClasses(hits)
	if deny {
		m.auditWorkspaceRead(ctx, tenant, wsMutationInput{op: "read-denied", workspaceID: ws.id, workspaceRef: ref, path: normRel(rel), actor: actor, actorKind: actorKind, classes: classes})
		return fileReadResponse{}, forbiddenErr("read denied by workspace DLP policy" + classSuffix(classes))
	}
	resp := fileReadResponse{Path: normRel(rel), Size: info.Size(), Truncated: truncated, Sensitivity: hits, SHA256: sha256Hex(data)}
	if utf8.Valid(data) {
		resp.Encoding, resp.Content = "utf-8", string(data)
	} else {
		resp.Encoding, resp.Content = "base64", base64.StdEncoding.EncodeToString(data)
	}
	m.auditWorkspaceRead(ctx, tenant, wsMutationInput{op: "read", workspaceID: ws.id, workspaceRef: ref, path: normRel(rel), actor: actor, actorKind: actorKind, classes: classes})
	return resp, nil
}

// writeFile creates/overwrites a file. The write is anchored by content hash in the
// audit chain BEFORE the filesystem op (deny-closed: no unaudited mutation).
func (m *Module) writeFile(ctx context.Context, tenant model.TenantID, ref, rel string, content []byte, actor, actorKind string) (writeResponse, error) {
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		return writeResponse{}, err
	}
	if ws.mountMode == mountRO {
		return writeResponse{}, forbiddenErr("workspace is read-only")
	}
	if int64(len(content)) > ws.maxReadBytes {
		return writeResponse{}, badRequest("content exceeds the workspace size limit")
	}
	abs, err := ws.jail(rel, false)
	if err != nil {
		return writeResponse{}, mapFSErr(err)
	}
	if abs == ws.rootReal {
		return writeResponse{}, badRequest("cannot write the workspace root itself")
	}
	created := !pathExists(abs)
	hash := sha256Hex(content)
	if serr := m.sealWorkspaceMutation(ctx, tenant, wsMutationInput{
		workspaceID: ws.id, workspaceRef: ref, op: "write", path: normRel(rel), contentHash: hash,
		actor: actor, actorKind: actorKind,
	}); serr != nil {
		return writeResponse{}, &runErr{http.StatusServiceUnavailable, "could not record the write (denied)"}
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		return writeResponse{}, mapFSErr(err)
	}
	return writeResponse{Path: normRel(rel), Size: int64(len(content)), SHA256: hash, Created: created}, nil
}

// mkdir creates a directory (and parents) under the workspace.
func (m *Module) mkdir(ctx context.Context, tenant model.TenantID, ref, rel, actor, actorKind string) (fileEntry, error) {
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		return fileEntry{}, err
	}
	if ws.mountMode == mountRO {
		return fileEntry{}, forbiddenErr("workspace is read-only")
	}
	abs, err := ws.jail(rel, false)
	if err != nil {
		return fileEntry{}, mapFSErr(err)
	}
	if abs == ws.rootReal {
		return fileEntry{}, badRequest("cannot mkdir the workspace root itself")
	}
	if serr := m.sealWorkspaceMutation(ctx, tenant, wsMutationInput{
		workspaceID: ws.id, workspaceRef: ref, op: "mkdir", path: normRel(rel), actor: actor, actorKind: actorKind,
	}); serr != nil {
		return fileEntry{}, &runErr{http.StatusServiceUnavailable, "could not record the mkdir (denied)"}
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fileEntry{}, mapFSErr(err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return fileEntry{}, mapFSErr(err)
	}
	relPath, _ := filepath.Rel(ws.rootReal, abs)
	return entryFromInfo(filepath.Base(abs), filepath.ToSlash(relPath), info), nil
}

// moveFile renames/moves an entry within the workspace (both ends jailed).
func (m *Module) moveFile(ctx context.Context, tenant model.TenantID, ref, from, to, actor, actorKind string) error {
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		return err
	}
	if ws.mountMode == mountRO {
		return forbiddenErr("workspace is read-only")
	}
	fromAbs, err := ws.jail(from, true)
	if err != nil {
		return mapFSErr(err)
	}
	toAbs, err := ws.jail(to, false)
	if err != nil {
		return mapFSErr(err)
	}
	if fromAbs == ws.rootReal || toAbs == ws.rootReal {
		return badRequest("cannot move the workspace root itself")
	}
	if serr := m.sealWorkspaceMutation(ctx, tenant, wsMutationInput{
		workspaceID: ws.id, workspaceRef: ref, op: "move", path: normRel(from), path2: normRel(to),
		actor: actor, actorKind: actorKind,
	}); serr != nil {
		return &runErr{http.StatusServiceUnavailable, "could not record the move (denied)"}
	}
	if err := os.Rename(fromAbs, toAbs); err != nil {
		return mapFSErr(err)
	}
	return nil
}

// deleteFile removes a file or (with recursive) a directory subtree. The workspace
// root itself is never deletable.
func (m *Module) deleteFile(ctx context.Context, tenant model.TenantID, ref, rel string, recursive bool, actor, actorKind string) error {
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		return err
	}
	if ws.mountMode == mountRO {
		return forbiddenErr("workspace is read-only")
	}
	abs, err := ws.jail(rel, true)
	if err != nil {
		return mapFSErr(err)
	}
	if abs == ws.rootReal {
		return badRequest("cannot delete the workspace root itself")
	}
	op := "delete"
	if recursive {
		op = "delete-recursive"
	}
	if serr := m.sealWorkspaceMutation(ctx, tenant, wsMutationInput{
		workspaceID: ws.id, workspaceRef: ref, op: op, path: normRel(rel), actor: actor, actorKind: actorKind,
	}); serr != nil {
		return &runErr{http.StatusServiceUnavailable, "could not record the delete (denied)"}
	}
	if recursive {
		if err := os.RemoveAll(abs); err != nil {
			return mapFSErr(err)
		}
		return nil
	}
	if err := os.Remove(abs); err != nil {
		return mapFSErr(err)
	}
	return nil
}

// loadWorkspaceRec reads the workspace row by ref (typed not-found).
func (m *Module) loadWorkspaceRec(ctx context.Context, tenant model.TenantID, ref string) (model.Record, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, badRequest("workspace ref required")
	}
	var rec model.Record
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		r, err := findWorkspaceRec(ctx, repo, ref)
		if err != nil {
			return err
		}
		rec = r
		return nil
	})
	return rec, err
}

// findWorkspaceRec lists a workspace by its unique ref (typed not-found).
func findWorkspaceRec(ctx context.Context, repo store.GenericRepo, ref string) (model.Record, error) {
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colWsRef, ref)}, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, &runErr{http.StatusNotFound, "workspace not found"}
	}
	return recs[0], nil
}

// validateWorkspace normalizes and validates a registration.
func validateWorkspace(p *CreateWorkspaceParams) error {
	p.RootPath = strings.TrimSpace(p.RootPath)
	if p.RootPath == "" {
		return badRequest("root_path required")
	}
	if !filepath.IsAbs(p.RootPath) {
		return badRequest("root_path must be absolute")
	}
	if p.MountMode == "" {
		p.MountMode = mountRW
	}
	if p.MountMode != mountRW && p.MountMode != mountRO {
		return badRequest("invalid mount_mode (want rw|ro)")
	}
	if p.DLPMode == "" {
		p.DLPMode = dlpLabel
	}
	if p.DLPMode != dlpLabel && p.DLPMode != dlpDeny && p.DLPMode != dlpOff {
		return badRequest("invalid dlp_mode (want label|deny|off)")
	}
	p.ContainerTarget = strings.TrimSpace(p.ContainerTarget)
	if p.ContainerTarget == "" {
		p.ContainerTarget = defaultContainerTarget
	}
	if !filepath.IsAbs(p.ContainerTarget) {
		return badRequest("container_target must be absolute")
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.MaxReadBytes < 0 {
		return badRequest("max_read_bytes must be non-negative")
	}
	for i, s := range p.AllowSubpaths {
		s = strings.TrimSpace(s)
		if strings.IndexByte(s, 0) >= 0 || filepath.IsAbs(s) {
			return badRequest("allow_subpaths entries must be relative, NUL-free")
		}
		// Reject an escaping subpath at registration (a `..` that climbs to/above the
		// root would otherwise widen the allowlist to the whole root — a footgun, not
		// an escape, but the config must be unambiguous).
		c := filepath.Clean(filepath.FromSlash(s))
		if c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
			return badRequest("allow_subpaths entries must stay within the workspace (no ..)")
		}
		p.AllowSubpaths[i] = s
	}
	return nil
}

// --- small helpers ----------------------------------------------------------------

// pageByName returns one page of entries with name > cursor (entries are pre-sorted by
// name), the next cursor, and whether more remain.
func pageByName(all []fileEntry, cursor string, limit int) ([]fileEntry, string, bool) {
	if limit <= 0 {
		limit = defaultListPage
	}
	if limit > maxListPage {
		limit = maxListPage
	}
	start := 0
	if cursor != "" {
		start = sort.Search(len(all), func(i int) bool { return all[i].Name > cursor })
	}
	end := start + limit
	more := end < len(all)
	if end > len(all) {
		end = len(all)
	}
	page := all[start:end]
	next := ""
	if more && len(page) > 0 {
		next = page[len(page)-1].Name
	}
	return page, next, more
}

// mapFSErr maps a filesystem/jail error to a typed runErr (HTTP status).
func mapFSErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errTraversal):
		return forbiddenErr("path traversal denied")
	case errors.Is(err, errFileNotExist), errors.Is(err, fs.ErrNotExist):
		return &runErr{http.StatusNotFound, "path not found"}
	case errors.Is(err, fs.ErrPermission):
		return forbiddenErr("filesystem permission denied")
	case errors.Is(err, fs.ErrExist):
		return conflictErr("path already exists")
	default:
		return &runErr{http.StatusInternalServerError, "filesystem error"}
	}
}

// normRel returns the cleaned, slash-form workspace-relative path for display/audit
// ("" → ".").
func normRel(rel string) string {
	c := filepath.ToSlash(filepath.Clean("/" + strings.TrimSpace(rel)))
	c = strings.TrimPrefix(c, "/")
	if c == "" {
		return "."
	}
	return c
}

// classSuffix renders the DLP classes for an error message (no values).
func classSuffix(classes []string) string {
	if len(classes) == 0 {
		return ""
	}
	return " (classes: " + strings.Join(classes, ",") + ")"
}

func pathExists(abs string) bool {
	_, err := os.Lstat(abs)
	return err == nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// encodeSubpaths marshals the allowlist to JSON text for the KindJSON column.
func encodeSubpaths(subs []string) (string, error) {
	clean := subs[:0]
	for _, s := range subs {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	if len(clean) == 0 {
		return "", nil
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeSubpaths reads the JSON allowlist column ([] when NULL/empty).
func decodeSubpaths(rec model.Record) []string {
	raw := rec.String(colWsAllowSubpaths)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
