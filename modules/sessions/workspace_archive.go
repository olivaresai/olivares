// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// maxArchiveEntries bounds the files and folders one archive holds, so a request cannot make
// the engine walk an unbounded tree. The bytes are bounded by maxUploadBytes.
// ponytail: one audit row per file, so a full archive appends 1000 rows one after the other;
// batch them in one ledger write if archives of that size become routine.
const maxArchiveEntries = 1000

// handleArchiveFiles returns the workspace entries named by the repeated path query parameter,
// folders with everything below them, as one zip.
func (m *Module) handleArchiveFiles(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	paths := r.URL.Query()["path"]
	zipped, classes, err := m.archiveFiles(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), paths,
		mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+archiveName(paths)+`"`)
	if len(classes) > 0 {
		// The labels a single read shows its reader: classes only, never matched values.
		w.Header().Set(archiveSensitivityHeader, strings.Join(classes, ","))
	}
	// ServeContent writes in chunks, so the stall deadline rearms along the transfer.
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(zipped))
}

// archiveSensitivityHeader names the DLP classes found in the files of one archive.
const archiveSensitivityHeader = "X-Olivares-Sensitivity"

var archiveNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// archiveName names the download after the one entry asked for, else "files".
func archiveName(paths []string) string {
	if len(paths) != 1 {
		return "files.zip"
	}
	return archiveNameUnsafe.ReplaceAllString(path.Base(normRel(paths[0])), "_") + ".zip"
}

// archiveRead is one file the archive holds, kept for the audit once the zip is whole.
type archiveRead struct {
	path    string
	classes []string
}

// archive builds one zip in memory. A request is answered whole or refused: a zip that
// silently lacks a file the caller named is worse than no zip.
type archive struct {
	m      *Module
	ctx    context.Context
	ws     *resolvedWorkspace
	buf    bytes.Buffer
	zw     *zip.Writer
	names  map[string]bool
	reads  []archiveRead
	denied *archiveRead // the file DLP refused, for the audit of the refusal
	total  int64
}

// archiveFiles reads every named file as a governed read (jail, size limit, DLP posture)
// and audits each one after the zip is complete. A folder is walked without following
// links: a link inside it names nothing the folder holds. The distinct DLP classes found
// come back with the zip.
func (m *Module) archiveFiles(ctx context.Context, tenant model.TenantID, ref string, paths []string, actor, actorKind string) ([]byte, []string, error) {
	if len(paths) == 0 {
		return nil, nil, badRequest("name at least one path to archive")
	}
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		return nil, nil, err
	}
	a := &archive{m: m, ctx: ctx, ws: ws, names: map[string]bool{}}
	a.zw = zip.NewWriter(&a.buf)
	audit := func(op string, read archiveRead) {
		m.auditWorkspaceRead(ctx, tenant, wsMutationInput{op: op, workspaceID: ws.id, workspaceRef: ws.ref, path: read.path, actor: actor, actorKind: actorKind, classes: read.classes})
	}
	for _, rel := range paths {
		if err := a.addTree(rel); err != nil {
			if a.denied != nil {
				audit("archive-denied", *a.denied)
			}
			return nil, nil, err
		}
	}
	if err := a.zw.Close(); err != nil {
		return nil, nil, &runErr{http.StatusInternalServerError, "could not build the archive"}
	}
	var classes []string
	for _, read := range a.reads {
		audit("archive", read)
		for _, c := range read.classes {
			if !slices.Contains(classes, c) {
				classes = append(classes, c)
			}
		}
	}
	return a.buf.Bytes(), classes, nil
}

// addTree adds the entry rel names and, for a folder, everything below it. Entries are
// named from the entry's own folder: asking for docs/b.txt and docs gives b.txt and docs/….
func (a *archive) addTree(rel string) error {
	abs, err := a.ws.jail(rel, true)
	if err != nil {
		return mapFSErr(err)
	}
	if abs == a.ws.rootReal {
		return badRequest("name a file or folder inside the workspace, not its root")
	}
	top := path.Base(normRel(rel))
	return filepath.WalkDir(abs, func(p string, d fs.DirEntry, walkErr error) error {
		if err := a.ctx.Err(); err != nil {
			return err // the client left: stop walking
		}
		if walkErr != nil {
			return mapFSErr(walkErr)
		}
		if p != abs && !d.IsDir() && !d.Type().IsRegular() {
			return nil // a link or special file below a folder is not followed
		}
		inside, _ := filepath.Rel(abs, p)
		name := top
		if inside != "." {
			name = top + "/" + filepath.ToSlash(inside)
		}
		fromRoot, _ := filepath.Rel(a.ws.rootReal, p)
		fromRoot = filepath.ToSlash(fromRoot)
		if p != abs {
			if _, err := a.ws.jail(fromRoot, true); err != nil {
				return mapFSErr(err)
			}
		}
		info, err := d.Info()
		if err != nil {
			return mapFSErr(err)
		}
		return a.add(name, p, fromRoot, info)
	})
}

// add writes one entry: a folder as its own entry (so an empty folder survives), a file
// after the same size limit and DLP posture a single read applies.
func (a *archive) add(name, abs, rel string, info fs.FileInfo) error {
	if strings.Contains(name, `\`) {
		// A Windows extractor reads the backslash as a separator and could write outside its target.
		return badRequest("an entry name holds a backslash: " + name)
	}
	if a.names[name] {
		return badRequest("two entries would be named " + name)
	}
	if len(a.names) >= maxArchiveEntries {
		return &runErr{http.StatusRequestEntityTooLarge, "the archive would hold more than the entry ceiling"}
	}
	a.names[name] = true
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: info.ModTime()}
	if info.IsDir() {
		header.Name, header.Method = name+"/", zip.Store
		_, err := a.zw.CreateHeader(header)
		return mapFSErr(err)
	}
	data, err := a.read(abs, rel, info.Size())
	if err != nil {
		return err
	}
	w, err := a.zw.CreateHeader(header)
	if err != nil {
		return mapFSErr(err)
	}
	_, err = w.Write(data)
	return mapFSErr(err)
}

// read returns a file's bytes once the size limit and the DLP posture allow them.
func (a *archive) read(abs, rel string, size int64) ([]byte, error) {
	if size > a.ws.maxReadBytes {
		return nil, &runErr{http.StatusRequestEntityTooLarge, rel + " exceeds the workspace size limit"}
	}
	f, _, err := openRegularForRead(abs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	// A file that grew since the stat is cut at the limit, then refused like a big one.
	data, err := io.ReadAll(io.LimitReader(f, a.ws.maxReadBytes+1))
	if err != nil {
		return nil, mapFSErr(err)
	}
	if int64(len(data)) > a.ws.maxReadBytes {
		return nil, &runErr{http.StatusRequestEntityTooLarge, rel + " exceeds the workspace size limit"}
	}
	// The bytes read count, not the stat: a file that grew since cannot slip past the ceiling.
	if a.total += int64(len(data)); a.total > maxUploadBytes {
		return nil, &runErr{http.StatusRequestEntityTooLarge, "the archive exceeds the size ceiling"}
	}
	hits, deny := a.m.classifyContent(a.ctx, a.ws.dlpMode, data)
	read := archiveRead{path: rel, classes: dlpClasses(hits)}
	if deny {
		a.denied = &read
		return nil, forbiddenErr("archive denied by workspace DLP policy: " + rel + classSuffix(read.classes))
	}
	a.reads = append(a.reads, read)
	return data, nil
}
