// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/api"
)

// THE SESSION'S CHANGES: the files in its folder that changed since the run started, and
// the current text of one of them, so the session view can show what the agent did beside
// the conversation (TARGET §2, the Conductor concept).
//
// Read-only, and nothing is executed: no git in the agent's folder, because a folder the
// agent writes can carry repository config that runs commands (fsmonitor, clean and
// textconv filters) and this read runs in the engine, outside the session's confinement.
// Every read goes through os.Root, so neither a path nor a symlink leaves the folder.
//
// The committed side of a change (?rev=HEAD) is read the same way, without git: the
// repository's own objects, parsed in git_head.go.
const (
	changesMaxListed   = 200
	changesMaxExamined = 20000
	changesMaxFileSize = 256 << 10
	// changesSlack absorbs clock granularity between the run's start and the first write.
	changesSlack = 2 * time.Second
)

// changesSkippedDirs are not the session's work: VCS metadata and dependency trees.
var changesSkippedDirs = map[string]bool{".git": true, "node_modules": true, ".venv": true, "__pycache__": true}

type changedFile struct {
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
}

type runChangesDTO struct {
	// Folder is the run's folder by name; the full path is on the run itself.
	Folder string        `json:"folder"`
	Since  string        `json:"since,omitempty"`
	Files  []changedFile `json:"files"`
	// Truncated is true when the folder held more than the read examines or lists.
	Truncated bool `json:"truncated"`
}

type runChangedFileDTO struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Text is the file's current content when it is UTF-8 text; empty for a binary file.
	Text      string `json:"text"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}

// listChanges walks root and returns the regular files modified at or after since,
// newest first.
func listChanges(root *os.Root, since time.Time) ([]changedFile, bool, error) {
	files := []changedFile{}
	examined, truncated := 0, false
	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable entry is not a change we can show
		}
		examined++
		if examined > changesMaxExamined {
			truncated = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if p != "." && changesSkippedDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		files = append(files, changedFile{Path: p, Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339)})
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModifiedAt > files[j].ModifiedAt })
	if len(files) > changesMaxListed {
		files, truncated = files[:changesMaxListed], true
	}
	return files, truncated, nil
}

// readChangedFile returns one file of root as text, bounded.
func readChangedFile(root *os.Root, rel string) (runChangedFileDTO, error) {
	clean, err := cleanFolderPath(rel)
	if err != nil {
		return runChangedFileDTO{}, err
	}
	info, err := root.Lstat(clean)
	if err != nil {
		return runChangedFileDTO{}, err
	}
	if !info.Mode().IsRegular() {
		return runChangedFileDTO{}, fs.ErrInvalid
	}
	f, err := root.Open(clean)
	if err != nil {
		return runChangedFileDTO{}, err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, changesMaxFileSize+1))
	if err != nil {
		return runChangedFileDTO{}, err
	}
	return changedFileDTO(clean, info.Size(), buf), nil
}

// changedFileDTO is one file as text: the first changesMaxFileSize bytes of buf when they
// are UTF-8 text, else just the fact that it is binary.
func changedFileDTO(rel string, size int64, buf []byte) runChangedFileDTO {
	out := runChangedFileDTO{Path: rel, Size: size, Truncated: len(buf) > changesMaxFileSize}
	if out.Truncated {
		buf = buf[:changesMaxFileSize]
	}
	if !utf8.Valid(buf) || bytes.IndexByte(buf, 0) >= 0 {
		out.Binary = true
		return out
	}
	out.Text = string(buf)
	return out
}

// readHeadFile returns one file of the folder as git HEAD holds it, bounded the same way.
func readHeadFile(root *os.Root, rel string) (runChangedFileDTO, error) {
	clean, err := cleanFolderPath(rel)
	if err != nil {
		return runChangedFileDTO{}, err
	}
	buf, err := headBlob(root, clean)
	if err != nil {
		return runChangedFileDTO{}, err
	}
	return changedFileDTO(clean, int64(len(buf)), buf), nil
}

// runRoot opens the run's folder; a nil root means the run has no folder this node can
// open.
func (m *Module) runRoot(r *http.Request, mc api.ModuleContext) (runDTO, *os.Root, error) {
	dto, err := m.getRun(r.Context(), mc.Tenant, chi.URLParam(r, "ref"))
	if err != nil {
		return runDTO{}, nil, err
	}
	if dto.WorkspacePath == "" {
		return dto, nil, nil
	}
	root, err := os.OpenRoot(dto.WorkspacePath)
	if err != nil {
		return dto, nil, nil
	}
	return dto, root, nil
}

// handleRunChanges lists the files in the run's folder that changed since the run started,
// newest first. Nothing is executed in the folder.
func (m *Module) handleRunChanges(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	dto, root, err := m.runRoot(r, mc)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	out := runChangesDTO{Folder: path.Base(strings.TrimRight(dto.WorkspacePath, "/")), Files: []changedFile{}}
	if root == nil {
		out.Folder = ""
		writeJSON(w, http.StatusOK, out)
		return
	}
	defer root.Close()
	since := dto.StartedAt
	if since == "" {
		since = dto.CreatedAt
	}
	start, perr := time.Parse(time.RFC3339, since)
	if perr != nil {
		start = time.Time{}
	} else {
		out.Since = since
		start = start.Add(-changesSlack)
	}
	files, truncated, err := listChanges(root, start)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("could not read the session's folder"))
		return
	}
	out.Files, out.Truncated = files, truncated
	writeJSON(w, http.StatusOK, out)
}

// handleRunChangedFile returns the current text of one file in the run's folder, or with
// rev=HEAD the text git HEAD holds for it (?path=, relative to the folder; at most 256 KiB;
// 404 when the folder has no readable git history or HEAD has no such file).
func (m *Module) handleRunChangedFile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	read := readChangedFile
	switch rev := r.URL.Query().Get("rev"); rev {
	case "":
	case headRev:
		read = readHeadFile
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("rev must be HEAD"))
		return
	}
	_, root, err := m.runRoot(r, mc)
	if err != nil {
		writeRunErr(w, err)
		return
	}
	if root == nil {
		writeJSON(w, http.StatusNotFound, errorBody("this session has no folder"))
		return
	}
	defer root.Close()
	out, err := read(root, r.URL.Query().Get("path"))
	switch {
	case errors.Is(err, fs.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, errorBody("path must name a file inside the session's folder"))
	case errors.Is(err, errNoRepository), errors.Is(err, errNotInHead):
		writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
	case errors.Is(err, errHeadTooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody(err.Error()))
	case err != nil:
		writeJSON(w, http.StatusNotFound, errorBody("no such file in the session's folder"))
	default:
		writeJSON(w, http.StatusOK, out)
	}
}
