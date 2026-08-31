// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// workspace_fs.go is the SECURITY CORE of the governed file API: the jail that
// confines every file operation to a workspace's canonical root. It defends against
// (1) lexical traversal (`../`, absolute paths, NUL bytes) and (2) SYMLINK escape (a
// link anywhere in the path — intermediate or final — that resolves outside the
// root). It is deny-closed and NEVER clamps a bad path to the root: an escape is an
// error, not a silent redirect.

// errTraversal is returned when a requested path escapes (or would escape) the
// workspace root. It maps to 403 (a refusal, never a clamp).
var errTraversal = errors.New("sessions: path escapes the workspace root (denied)")

// errFileNotExist is returned when a path that MUST exist (read/stat/list/delete)
// is absent. It maps to 404.
var errFileNotExist = errors.New("sessions: path does not exist")

// resolveWithin canonicalizes rel against the workspace root and returns the SAFE
// absolute path, refusing any escape. mustExist selects the resolution mode:
//
//   - mustExist=true  (read/stat/list/delete): the target must exist; its FULLY
//     symlink-resolved real path must lie within the canonical root.
//   - mustExist=false (write/mkdir/move-dest): the target may not exist yet; the
//     DEEPEST EXISTING ANCESTOR must resolve within the root (so a write cannot land
//     through a symlinked parent that points outside), and the lexical target path is
//     returned for creation.
//
// The root is itself EvalSymlinks-resolved first, so the containment check compares
// real paths on both sides (a symlinked root is handled correctly).
func resolveWithin(root, rel string, mustExist bool) (string, error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		// The registered root is gone/unreadable — fail closed (never fall back to rel).
		return "", err
	}
	rel = strings.TrimSpace(rel)
	if strings.IndexByte(rel, 0) >= 0 {
		return "", errTraversal // NUL byte: reject outright
	}
	if filepath.IsAbs(rel) {
		return "", errTraversal // absolute path: reject (never reinterpret as in-root)
	}
	// REJECT any escaping "..", never clamp it: Clean collapses internal traversal that
	// stays inside (a/b/../f → a/f), but a result that climbs to or above the root
	// (".." / "../x") is refused outright (deny-closed, the contract's "never clamp").
	cleaned := filepath.Clean(filepath.FromSlash(rel))
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", errTraversal
	}
	if cleaned == "." {
		cleaned = ""
	}
	abs := filepath.Join(rootReal, cleaned)
	if !within(rootReal, abs) {
		return "", errTraversal // lexical escape (defense in depth)
	}

	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", errTraversal // unexpected lstat error → deny (never assume safe)
		}
		if mustExist {
			return "", errFileNotExist
		}
		// Create path: verify the deepest existing ancestor resolves within the root.
		anc, aerr := deepestExistingAncestor(abs)
		if aerr != nil {
			return "", errTraversal
		}
		ancReal, aerr := filepath.EvalSymlinks(anc)
		if aerr != nil || !within(rootReal, ancReal) {
			return "", errTraversal
		}
		return abs, nil
	}
	if !within(rootReal, real) {
		return "", errTraversal // symlink (any component) escaped the root
	}
	return real, nil
}

// within reports whether p is the root itself or a descendant of it, comparing
// already-canonical paths. It rejects "..", "../x", and any absolute relativization.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

// deepestExistingAncestor walks up from abs's parent until an existing directory is
// found, returning it (used to validate the create path of a not-yet-existing file).
func deepestExistingAncestor(abs string) (string, error) {
	dir := filepath.Dir(abs)
	for {
		if _, err := os.Lstat(dir); err == nil {
			return dir, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fs.ErrNotExist // reached the filesystem root without an existing dir
		}
		dir = parent
	}
}

// underAllowedSubpath reports whether the resolved real path lies within at least one
// of the workspace's allowlisted subpaths. An empty allowlist means the whole root is
// exposed.
func underAllowedSubpath(rootReal string, allow []string, real string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, sub := range allow {
		base := filepath.Join(rootReal, filepath.FromSlash(filepath.Clean("/"+sub)))
		if within(base, real) {
			return true
		}
	}
	return false
}

// fileEntry is one filesystem entry returned by list/stat (metadata only — never
// content). Path is the workspace-relative slash path (the API's addressing key).
type fileEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Type      string `json:"type"` // file | dir | symlink | other
	Size      int64  `json:"size"`
	Mode      string `json:"mode"`
	ModTime   string `json:"mtime"`
	IsSymlink bool   `json:"is_symlink,omitempty"`
}

const (
	ftFile    = "file"
	ftDir     = "dir"
	ftSymlink = "symlink"
	ftOther   = "other"
)

// listDir reads one directory level (NOT recursive) under the resolved absolute dir,
// returning entries sorted by name. Symlinks are reported as symlinks WITHOUT being
// followed (a listing never leaks a link target's content). rootReal+relDir build the
// workspace-relative path of each entry.
func listDir(absDir, rootReal string) ([]fileEntry, error) {
	des, err := os.ReadDir(absDir)
	if err != nil {
		return nil, err
	}
	out := make([]fileEntry, 0, len(des))
	for _, de := range des {
		info, ierr := de.Info()
		if ierr != nil {
			continue // entry vanished between ReadDir and Info; skip rather than fail the page
		}
		abs := filepath.Join(absDir, de.Name())
		rel, _ := filepath.Rel(rootReal, abs)
		out = append(out, entryFromInfo(de.Name(), filepath.ToSlash(rel), info))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// entryFromInfo projects an os.FileInfo to a fileEntry (Lstat semantics: a symlink is
// reported as such, never followed).
func entryFromInfo(name, relPath string, info os.FileInfo) fileEntry {
	e := fileEntry{
		Name:    name,
		Path:    relPath,
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		e.Type, e.IsSymlink = ftSymlink, true
	case info.IsDir():
		e.Type = ftDir
	case info.Mode().IsRegular():
		e.Type = ftFile
	default:
		e.Type = ftOther
	}
	return e
}
