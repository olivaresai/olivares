// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// The governed search of a workspace: a file by its name, or a line by its text. It is
// bounded so that it always answers, and a bound that is reached is reported in the
// answer, never as an error.
const (
	searchInName    = "name"
	searchInContent = "content"

	searchDefaultLimit = 50
	searchMaxLimit     = 200
	searchMaxQuery     = 200
	searchDeadline     = 10 * time.Second
	searchMaxFiles     = 20000
	// searchMaxBytes is how much file content one search reads, in total.
	searchMaxBytes int64 = 256 << 20
	// searchMaxFileBytes is the largest file read, further limited by the folder's own.
	searchMaxFileBytes int64 = 1 << 20
	searchMaxPerFile         = 20
	searchMaxText            = 240
	searchBinaryProbe        = 8 << 10
	// searchGitOutputLimit holds the ceiling of files at an ample average path length.
	searchGitOutputLimit = searchMaxFiles * 512
)

// fileSearchMatch is one file (name search), or one line of a file (content search).
type fileSearchMatch struct {
	Path string `json:"path"`
	Name string `json:"name"`
	// Line and Column are 1-based. Column counts characters within the line, not within Text.
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
	Text   string `json:"text,omitempty"`
}

type fileSearchResponse struct {
	Query   string            `json:"query"`
	In      string            `json:"in"`
	Matches []fileSearchMatch `json:"matches"`
	// Truncated is true when a bound or the limit left something unsearched or unreturned.
	Truncated bool `json:"truncated"`
}

// handleSearchFiles finds the files of a workspace by name, or the lines of its text
// files by literal, case-insensitive content.
func (m *Module) handleSearchFiles(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	query := r.URL.Query()
	resp, err := m.searchFiles(r.Context(), mc.Tenant, chi.URLParam(r, "ref"),
		query.Get("q"), query.Get("in"), listQuery(r).Limit, mc.Principal.Actor(), mc.Principal.ActorKind())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// searchFiles answers one search under the workspace's path, size and DLP policy and
// audits it with the classes of what it returned, never the query or the content.
func (m *Module) searchFiles(ctx context.Context, tenant model.TenantID, ref, q, in string, limit int, actor, actorKind string) (fileSearchResponse, error) {
	q = strings.TrimSpace(q)
	if n := utf8.RuneCountInString(q); n == 0 || n > searchMaxQuery || !utf8.ValidString(q) {
		return fileSearchResponse{}, badRequest("q must be 1 to 200 characters of text")
	}
	if in == "" {
		in = searchInName
	}
	if in != searchInName && in != searchInContent {
		return fileSearchResponse{}, badRequest("in must be name or content")
	}
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	limit = min(limit, searchMaxLimit)
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		return fileSearchResponse{}, err
	}
	root, err := os.OpenRoot(ws.rootReal)
	if err != nil {
		return fileSearchResponse{}, mapFSErr(err)
	}
	defer func() { _ = root.Close() }()
	// The audit below keeps the request's context: this one may have just expired.
	bounded, cancel := context.WithTimeout(ctx, searchDeadline)
	defer cancel()

	files, cut := searchableFiles(bounded, ws, root)
	out := fileSearchResponse{Query: q, In: in, Matches: []fileSearchMatch{}, Truncated: cut}
	var returned []SensitivityHit
	if in == searchInName {
		rankFileNames(files, q, limit, &out)
	} else {
		returned = m.searchFileContents(bounded, ws, root, files, q, limit, &out)
	}
	m.auditWorkspaceRead(ctx, tenant, wsMutationInput{op: "search", workspaceID: ws.id, workspaceRef: ref, path: "", actor: actor, actorKind: actorKind, classes: dlpClasses(returned)})
	return out, nil
}

// searchableFiles is the one list both modes read: the regular files of the folder that
// its allowed subpaths admit, in path order, and whether a bound cut the list short.
// Every name comes from a folder read where it is named (readFolder), so a folder the
// folder's writer swaps for a link while the listing runs gives none of its names.
func searchableFiles(ctx context.Context, ws *resolvedWorkspace, root *os.Root) ([]string, bool) {
	var files []string
	cut := false
	// keep reports whether listing goes on.
	keep := func(rel string) bool {
		if !underAllowedSubpath(ws.rootReal, ws.allowSubpaths, filepath.Join(ws.rootReal, filepath.FromSlash(rel))) {
			return true
		}
		if len(files) == searchMaxFiles {
			cut = true
			return false
		}
		files = append(files, rel)
		return true
	}
	names, capped, listed := gitListedFiles(ctx, ws.rootReal)
	cut = capped
	if listed {
		// Git read the folder by its names too: each name is kept only if a read of
		// its own folder holds it as a regular file. Git names a link or a nested
		// repository as an entry of its own.
		folders := map[string]map[string]fs.DirEntry{}
		for _, rel := range names {
			dir := path.Dir(rel)
			entries, read := folders[dir]
			if !read {
				entries = map[string]fs.DirEntry{}
				for _, entry := range readFolder(ws, root, dir) {
					entries[entry.Name()] = entry
				}
				folders[dir] = entries
			}
			if entry := entries[path.Base(rel)]; entry != nil && entry.Type().IsRegular() && !keep(rel) {
				break
			}
		}
	} else if !walkFolder(ctx, ws, root, ".", keep) && ctx.Err() != nil {
		cut = true
	}
	slices.Sort(files)
	return files, cut
}

// walkFolder gives keep the regular files of dir and of the folders below it, in path
// order, and reports whether listing goes on. A folder is entered by reading it where it
// is named, so one that became a link after its parent was read gives nothing; an
// unreadable folder is left out, as it is for git.
func walkFolder(ctx context.Context, ws *resolvedWorkspace, root *os.Root, dir string, keep func(string) bool) bool {
	for _, entry := range readFolder(ws, root, dir) {
		name, rel := entry.Name(), path.Join(dir, entry.Name())
		switch {
		case ctx.Err() != nil:
			return false
		case name == ".git" || entry.IsDir() && (changesSkippedDirs[name] || name == "venv" || name == ".cache"):
		case entry.IsDir():
			if !walkFolder(ctx, ws, root, rel, keep) {
				return false
			}
		case entry.Type().IsRegular() && !keep(rel):
			return false
		}
	}
	return ctx.Err() == nil
}

// readFolder lists dir, in name order, from a descriptor opened where it is named.
func readFolder(ws *resolvedWorkspace, root *os.Root, dir string) []fs.DirEntry {
	folder, err := openWhereNamed(ws, root, dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NONBLOCK)
	if err != nil {
		return nil
	}
	defer func() { _ = folder.Close() }()
	entries, _ := folder.ReadDir(-1)
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries
}

// gitListedFiles lists what git would track, so the folder's .gitignore is honored. The
// folder is the agent's, so git runs on a repository of the engine's own with the folder
// as its work tree: it reads the folder's files and its .gitignore, and no hook, filter
// or configuration the agent could have written. ok is false when there is no .git or
// git did not answer, and the caller walks the folder instead.
//
// ponytail: the files of a nested repository are not listed, as in a turn checkpoint.
// Upgrade trigger: a person searches a folder of nested checkouts and misses a file.
func gitListedFiles(ctx context.Context, rootReal string) (names []string, capped, ok bool) {
	if _, err := os.Lstat(filepath.Join(rootReal, ".git")); err != nil {
		return nil, false, false
	}
	repo, err := os.MkdirTemp("", "olivares-search-")
	if err != nil {
		return nil, false, false
	}
	defer func() { _ = os.RemoveAll(repo) }()
	if _, err := gitOK(ctx, repo, "init", "--quiet", "--bare", "--template=", repo); err != nil {
		return nil, false, false
	}
	out, code, _, err := gitRunCapped(ctx, repo, "", searchGitOutputLimit,
		"--git-dir="+repo, "--work-tree="+rootReal, "-c", "core.bare=false", "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil || code != 0 {
		return nil, false, false
	}
	return strings.Split(out, "\x00"), len(out) >= searchGitOutputLimit, true
}

// rankFileNames orders the files that match: exact name, name prefix, name contains,
// path contains, then the query's characters in order along the path. A query with a
// separator can only match the last two.
func rankFileNames(files []string, q string, limit int, out *fileSearchResponse) {
	type ranked struct {
		rel        string
		rank, size int
	}
	q = strings.ToLower(q)
	var found []ranked
	for _, rel := range files {
		if rank, ok := fileNameRank(strings.ToLower(rel), q); ok {
			found = append(found, ranked{rel, rank, utf8.RuneCountInString(rel)})
		}
	}
	slices.SortFunc(found, func(a, b ranked) int {
		return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.size, b.size), cmp.Compare(a.rel, b.rel))
	})
	if len(found) > limit {
		found, out.Truncated = found[:limit], true
	}
	for _, f := range found {
		out.Matches = append(out.Matches, fileSearchMatch{Path: f.rel, Name: path.Base(f.rel)})
	}
}

func fileNameRank(lowerPath, q string) (int, bool) {
	base := path.Base(lowerPath)
	switch {
	case base == q:
		return 0, true
	case strings.HasPrefix(base, q):
		return 1, true
	case strings.Contains(base, q):
		return 2, true
	case strings.Contains(lowerPath, q):
		return 3, true
	}
	remaining := q
	for _, r := range lowerPath {
		if next, size := utf8.DecodeRuneInString(remaining); r == next {
			remaining = remaining[size:]
		}
	}
	return 4, remaining == ""
}

// searchFileContents fills out with the matching lines of files and returns the DLP hits
// of the files it answered from. A file is classified only once it has matched, which
// bounds the cost. A file DLP refuses answers as one that does not match: no line, no
// count and no cut, so the answer is the same whether DLP refused none or many.
func (m *Module) searchFileContents(ctx context.Context, ws *resolvedWorkspace, root *os.Root, files []string, q string, limit int, out *fileSearchResponse) []SensitivityHit {
	var returned []SensitivityHit
	needle := strings.ToLower(q)
	maxFile := min(ws.maxReadBytes, searchMaxFileBytes)
	var spent int64
	for _, rel := range files {
		if ctx.Err() != nil {
			out.Truncated = true
			break
		}
		f, size, err := openSearchFile(ws, root, rel)
		if err != nil {
			continue
		}
		if size > maxFile {
			_ = f.Close()
			continue
		}
		if spent+size > searchMaxBytes {
			_ = f.Close()
			out.Truncated = true
			break
		}
		data, err := io.ReadAll(io.LimitReader(f, maxFile+1))
		_ = f.Close()
		spent += int64(len(data))
		if err != nil || int64(len(data)) > maxFile || !searchableText(data) {
			continue
		}
		lines, more := matchingLines(string(data), needle)
		if len(lines) == 0 {
			continue
		}
		hits, deny := m.classifyContent(ctx, ws.dlpMode, data)
		if deny {
			continue
		}
		if len(out.Matches) == limit {
			out.Truncated = true
			break
		}
		returned = append(returned, hits...)
		if room := limit - len(out.Matches); len(lines) > room {
			lines, more = lines[:room], true
		}
		out.Truncated = out.Truncated || more
		for _, line := range lines {
			line.Path, line.Name = rel, path.Base(rel)
			out.Matches = append(out.Matches, line)
		}
	}
	return returned
}

// openSearchFile opens a listed file where it is named, so neither a link at its name
// nor a folder above it swapped for one leads to another file. The file must also be
// the one the name holds and not a link, and the open does not wait on a FIFO the agent
// planted there.
func openSearchFile(ws *resolvedWorkspace, root *os.Root, rel string) (*os.File, int64, error) {
	f, err := openWhereNamed(ws, root, rel, os.O_RDONLY|syscall.O_NONBLOCK)
	if err != nil {
		return nil, 0, err
	}
	opened, statErr := f.Stat()
	named, lstatErr := root.Lstat(rel)
	if err := errors.Join(statErr, lstatErr); err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	if !opened.Mode().IsRegular() || !named.Mode().IsRegular() || !os.SameFile(opened, named) {
		_ = f.Close()
		return nil, 0, fs.ErrInvalid
	}
	return f, opened.Size(), nil
}

// searchableText refuses what is not text: a NUL early in the file, or invalid UTF-8.
func searchableText(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), searchBinaryProbe)], 0) < 0 && utf8.Valid(data)
}

// matchingLines returns the first match of each line, in order, and whether the file has
// more than the per-file ceiling. strings.ToLower maps character for character, so a
// position in the lowered line counts the same characters as in the line itself.
func matchingLines(text, needle string) (found []fileSearchMatch, more bool) {
	if !strings.Contains(strings.ToLower(text), needle) {
		return nil, false
	}
	for number, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		lowered := strings.ToLower(line)
		at := strings.Index(lowered, needle)
		if at < 0 {
			continue
		}
		if len(found) == searchMaxPerFile {
			return found, true
		}
		column := utf8.RuneCountInString(lowered[:at])
		found = append(found, fileSearchMatch{Line: number + 1, Column: column + 1, Text: searchLineText(line, column, utf8.RuneCountInString(needle))})
	}
	return found, false
}

// searchLineText cuts a line to searchMaxText characters around the match, on character
// boundaries, and with no carriage return left inside it.
func searchLineText(line string, column, width int) string {
	runes := []rune(strings.ReplaceAll(line, "\r", " "))
	if len(runes) <= searchMaxText {
		return string(runes)
	}
	start := max(0, min(column-(searchMaxText-width)/2, len(runes)-searchMaxText))
	return string(runes[start : start+searchMaxText])
}
