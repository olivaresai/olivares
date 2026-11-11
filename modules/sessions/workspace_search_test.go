// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
)

// Non-ASCII data the search must handle: runes counted as runes, and a folder name git
// would quote unless told not to.

const searchAccent = "é" // language-data: one two-byte rune, test data rather than prose

const searchNestedPath = "ñandú/needle-tracked.txt" // language-data: a non-ASCII folder name, test data

const searchAccentLine = "ñandú needle\n" // language-data: non-ASCII text before a match, test data

// searchFixture is one tenant behind the real routes, so each test below proves a rule
// of the search on the request a console sends.
type searchFixture struct {
	h      *harness
	m      *Module
	admin  string
	tenant model.TenantID
}

func newSearchFixture(t *testing.T, opts ...Option) searchFixture {
	t.Helper()
	m := New(opts...)
	h := newHarness(t, m)
	admin := h.adminLogin()
	return searchFixture{h: h, m: m, admin: admin, tenant: h.createOrg(admin, "acme")}
}

func (f searchFixture) register(t *testing.T, root string, body map[string]any) string {
	t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	body["root_path"], body["name"] = root, filepath.Base(root)
	r := f.h.doJSON("POST", "/v1/m/sessions/workspaces", f.admin, body, tenantHdr(f.tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("register = %d %s", r.code, r.raw)
	}
	return r.body["workspace_ref"].(string)
}

func (f searchFixture) get(t *testing.T, ref string, query url.Values) resp {
	t.Helper()
	return f.h.do("GET", "/v1/m/sessions/workspaces/"+ref+"/files/search?"+query.Encode(), f.admin, tenantHdr(f.tenant))
}

// search asks for q in the mode named and fails on anything but a 200.
func (f searchFixture) search(t *testing.T, ref, q, in string) resp {
	t.Helper()
	r := f.get(t, ref, url.Values{"q": {q}, "in": {in}})
	if r.code != http.StatusOK {
		t.Fatalf("search %q in %s = %d %s", q, in, r.code, r.raw)
	}
	return r
}

func searchMatches(t *testing.T, r resp) []map[string]any {
	t.Helper()
	raw, ok := r.body["matches"].([]any)
	if !ok {
		t.Fatalf("matches is not a list: %s", r.raw)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(map[string]any))
	}
	return out
}

func searchPaths(t *testing.T, r resp) []string {
	t.Helper()
	paths := []string{}
	for _, match := range searchMatches(t, r) {
		paths = append(paths, match["path"].(string))
	}
	return paths
}

func wantSearchPaths(t *testing.T, r resp, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := searchPaths(t, r); !slices.Equal(got, want) {
		t.Fatalf("paths = %q, want %q (%s)", got, want, r.raw)
	}
}

func searchLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestFilesSearchRefusesAMalformedRequest(t *testing.T) {
	f := newSearchFixture(t)
	ref := f.register(t, t.TempDir(), nil)
	for name, query := range map[string]url.Values{
		"no query":    {},
		"only spaces": {"q": {"   "}},
		"too long":    {"q": {strings.Repeat("a", 201)}},
		"unknown in":  {"q": {"a"}, "in": {"regex"}},
	} {
		if r := f.get(t, ref, query); r.code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, r.code, r.raw)
		}
	}
	r := f.get(t, ref, url.Values{"q": {"  " + strings.Repeat(searchAccent, 200) + " "}})
	if r.code != http.StatusOK || r.body["query"] != strings.Repeat(searchAccent, 200) || r.body["in"] != "name" {
		t.Fatalf("200 runes after trimming, default mode = %d %s", r.code, r.raw)
	}
}

// A link is never a match and never a way out, and a file outside the allowed subpaths
// does not exist as far as the search says.
func TestFilesSearchStaysInTheJail(t *testing.T) {
	f := newSearchFixture(t)
	outside := t.TempDir()
	fixtureWrite(t, outside, "out-needle.txt", "needle outside\n")

	root := t.TempDir()
	fixtureWrite(t, root, "real-needle.txt", "needle inside\n")
	searchLink(t, filepath.Join(outside, "out-needle.txt"), filepath.Join(root, "link-needle.txt"))
	searchLink(t, outside, filepath.Join(root, "linked-dir"))
	searchLink(t, "real-needle.txt", filepath.Join(root, "alias-needle.txt"))
	ref := f.register(t, root, nil)

	wantSearchPaths(t, f.search(t, ref, "needle", "name"), "real-needle.txt")
	wantSearchPaths(t, f.search(t, ref, "needle", "content"), "real-needle.txt")

	// A link swapped in after the listing, which no request can stage.
	jail, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = jail.Close() }()
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	ws := &resolvedWorkspace{rootReal: rootReal}
	if file, _, err := openSearchFile(ws, jail, "link-needle.txt"); err == nil {
		_ = file.Close()
		t.Fatal("opened a link that leads out of the folder")
	}
	if file, _, err := openSearchFile(ws, jail, "alias-needle.txt"); err == nil {
		_ = file.Close()
		t.Fatal("opened a link that stays inside the folder: a link is never a match")
	}

	limited := t.TempDir()
	fixtureWrite(t, limited, "allowed/in-needle.txt", "needle allowed\n")
	fixtureWrite(t, limited, "denied/in-needle.txt", "needle denied\n")
	limitedRef := f.register(t, limited, map[string]any{"allow_subpaths": []string{"allowed"}})
	for _, in := range []string{"name", "content"} {
		r := f.search(t, limitedRef, "needle", in)
		wantSearchPaths(t, r, "allowed/in-needle.txt")
		if strings.Contains(r.raw, "denied") || r.body["truncated"] != false {
			t.Fatalf("a file outside the allowed subpaths showed in the %s search: %s", in, r.raw)
		}
	}
}

// The listing and the open are two moments: a folder the listing admitted can be
// swapped for a link before a file below it is opened. A link that stays inside the
// folder is one os.Root follows, so the file is read only where the listing found it:
// a link at any name of its path, the folders above it included, gives nothing.
func TestFilesSearchReadsNothingThroughASwappedFolder(t *testing.T) {
	f := newSearchFixture(t)
	root := t.TempDir()
	fixtureWrite(t, root, "allowed/slot/in.txt", "needle allowed\n")
	fixtureWrite(t, root, "denied/in.txt", "needle denied\n")
	ref := f.register(t, root, map[string]any{"allow_subpaths": []string{"allowed"}, "dlp_mode": "off"})
	ws, err := f.m.resolveWorkspace(context.Background(), f.tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	jail, err := os.OpenRoot(ws.rootReal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = jail.Close() }()
	files, cut := searchableFiles(context.Background(), ws, jail)
	if !slices.Equal(files, []string{"allowed/slot/in.txt"}) || cut {
		t.Fatalf("listed %q, cut %v", files, cut)
	}
	search := func() fileSearchResponse {
		out := fileSearchResponse{Matches: []fileSearchMatch{}}
		f.m.searchFileContents(context.Background(), ws, jail, files, "needle", searchDefaultLimit, &out)
		return out
	}
	if fileScopeSupported {
		if out := search(); len(out.Matches) != 1 || out.Matches[0].Text != "needle allowed" {
			t.Fatalf("the admitted file before the swap = %+v", out.Matches)
		}
	}

	// The folder's writer swaps the admitted folder for a link to the denied one.
	slot := filepath.Join(ws.rootReal, "allowed", "slot")
	if err := os.RemoveAll(slot); err != nil {
		t.Fatal(err)
	}
	searchLink(t, filepath.Join("..", "denied"), slot)
	if out := search(); len(out.Matches) != 0 {
		t.Fatalf("read a file outside the allowed subpaths through a swapped folder: %+v", out.Matches)
	}
}

// A listing names files while the folder's writer can swap a folder for a link, so a
// name is kept only if the folder that holds it is where the name says. Git is made to
// answer here as a listing that ran while allowed/slot was a link to ../denied: it names
// a denied file below the allowed subpath, and a name search must not show it.
func TestFilesSearchListsNoNameThroughALinkedFolder(t *testing.T) {
	f := newSearchFixture(t)
	root := t.TempDir()
	fixtureWrite(t, root, "allowed/kept-needle.txt", "needle\n")
	fixtureWrite(t, root, "denied/private-needle.txt", "needle\n")
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	searchLink(t, filepath.Join("..", "denied"), filepath.Join(root, "allowed", "slot"))
	ref := f.register(t, root, map[string]any{"allow_subpaths": []string{"allowed"}, "dlp_mode": "off"})

	bin := t.TempDir()
	git := "#!/bin/sh\ncase \" $* \" in *' ls-files '*) printf 'allowed/kept-needle.txt\\0allowed/slot/private-needle.txt\\0' ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(git), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, in := range []string{"name", "content"} {
		r := f.search(t, ref, "needle", in)
		if strings.Contains(r.raw, "private") {
			t.Fatalf("the %s search named a file outside the allowed subpaths: %s", in, r.raw)
		}
		wantSearchPaths(t, r, "allowed/kept-needle.txt")
	}
}

// The walk reads a folder's names, then enters each folder among them; by then that
// folder may be a link. Every folder is read from where it is named, so a folder that
// became a link to ../denied after its parent was read gives none of denied's names.
func TestFilesSearchWalksNoFolderSwappedForALink(t *testing.T) {
	f := newSearchFixture(t)
	root := t.TempDir()
	fixtureWrite(t, root, "allowed/slot/in.txt", "\n")
	fixtureWrite(t, root, "denied/private.txt", "\n")
	ref := f.register(t, root, map[string]any{"allow_subpaths": []string{"allowed"}})
	ws, err := f.m.resolveWorkspace(context.Background(), f.tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	jail, err := os.OpenRoot(ws.rootReal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = jail.Close() }()
	walk := func() []string {
		got := []string{}
		walkFolder(context.Background(), ws, jail, "allowed/slot", func(rel string) bool {
			got = append(got, rel)
			return true
		})
		return got
	}
	if got := walk(); fileScopeSupported && !slices.Equal(got, []string{"allowed/slot/in.txt"}) {
		t.Fatalf("the folder before the swap gave %q", got)
	}

	slot := filepath.Join(ws.rootReal, "allowed", "slot")
	if err := os.RemoveAll(slot); err != nil {
		t.Fatal(err)
	}
	searchLink(t, filepath.Join("..", "denied"), slot)
	if got := walk(); len(got) != 0 {
		t.Fatalf("the walk listed through a folder swapped for a link: %q", got)
	}
}

// Git says what a repository ignores, and nothing the repository configured runs.
func TestFilesSearchListsWhatGitWouldTrack(t *testing.T) {
	f := newSearchFixture(t)
	root := t.TempDir()
	gitFixture(t, root, "init", "-q", "-b", "main")
	fixtureWrite(t, root, ".gitignore", "ignored-needle.txt\n")
	fixtureWrite(t, root, "ignored-needle.txt", "needle\n")
	fixtureWrite(t, root, "kept-needle.txt", "needle\n")
	fixtureWrite(t, root, searchNestedPath, "needle\n")
	fixtureWrite(t, root, "node_modules/dep-needle.txt", "needle\n")

	sentinel := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "plant.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+sentinel+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, root, "config", "core.fsmonitor", script)
	gitFixture(t, root, "config", "core.hooksPath", filepath.Dir(script))
	ref := f.register(t, root, nil)

	r := f.search(t, ref, "needle", "name")
	wantSearchPaths(t, r, searchNestedPath, "kept-needle.txt", "node_modules/dep-needle.txt")
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the folder's own repository configuration ran: %v", err)
	}
	for _, match := range searchPaths(t, f.search(t, ref, "HEAD", "name")) {
		t.Fatalf("a file inside .git was listed: %s", match)
	}

	// Without git the same folder is walked: .gitignore has no say and .git stays closed.
	t.Run("git is missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		wantSearchPaths(t, f.search(t, ref, "needle", "name"), searchNestedPath, "kept-needle.txt", "ignored-needle.txt")
		for _, match := range searchPaths(t, f.search(t, ref, "HEAD", "name")) {
			t.Fatalf("a file inside .git was listed by the walk: %s", match)
		}
	})

	plain := t.TempDir()
	fixtureWrite(t, plain, "src/kept-needle.txt", "needle\n")
	for _, dir := range []string{"node_modules", ".venv", "venv", "__pycache__", ".cache"} {
		fixtureWrite(t, plain, "src/"+dir+"/skipped-needle.txt", "needle\n")
	}
	wantSearchPaths(t, f.search(t, f.register(t, plain, nil), "needle", "name"), "src/kept-needle.txt")
}

func TestFilesSearchRanksNames(t *testing.T) {
	f := newSearchFixture(t)
	root := t.TempDir()
	for _, rel := range []string{"FOO", "foo.go", "foo1.c", "z/foobar.go", "barfoo.go", "foo-dir/x.go", "f/o/o.txt", "bar.txt"} {
		fixtureWrite(t, root, rel, "\n")
	}
	ref := f.register(t, root, nil)

	r := f.search(t, ref, "foo", "name")
	wantSearchPaths(t, r, "FOO", "foo.go", "foo1.c", "z/foobar.go", "barfoo.go", "foo-dir/x.go", "f/o/o.txt")
	first := searchMatches(t, r)[0]
	if first["name"] != "FOO" {
		t.Fatalf("name = %v", first["name"])
	}
	for _, field := range []string{"line", "column", "text"} {
		if _, present := first[field]; present {
			t.Fatalf("a name match carries %s: %s", field, r.raw)
		}
	}
	wantSearchPaths(t, f.search(t, ref, "foo-DIR/X", "name"), "foo-dir/x.go")
}

func TestFilesSearchFindsLiteralLines(t *testing.T) {
	f := newSearchFixture(t)
	root := t.TempDir()
	fixtureWrite(t, root, "a.txt", "first\nThe NEEDLE here\r\nnope\n")
	fixtureWrite(t, root, "b.txt", searchAccentLine)
	fixtureWrite(t, root, "bin.dat", "needle\x00\n")
	fixtureWrite(t, root, "bad.txt", "needle \xff\n")
	fixtureWrite(t, root, "late-nul.txt", strings.Repeat("x", 9000)+"\x00 needle\n")
	fixtureWrite(t, root, "too-big.txt", "needle\n"+strings.Repeat("x", 1<<20))
	fixtureWrite(t, root, "at-limit.txt", "needle\n"+strings.Repeat("x", 1<<20-7))
	ref := f.register(t, root, nil)

	r := f.search(t, ref, "needle", "content")
	wantSearchPaths(t, r, "a.txt", "at-limit.txt", "b.txt", "late-nul.txt")
	if r.body["truncated"] != false || r.body["in"] != "content" {
		t.Fatalf("response: %s", r.raw)
	}
	byPath := map[string]map[string]any{}
	for _, match := range searchMatches(t, r) {
		byPath[match["path"].(string)] = match
	}
	a := byPath["a.txt"]
	if a["line"] != float64(2) || a["column"] != float64(5) || a["text"] != "The NEEDLE here" || a["name"] != "a.txt" {
		t.Fatalf("match in a.txt = %v", a)
	}
	if b := byPath["b.txt"]; b["column"] != float64(7) {
		t.Fatalf("the column counts characters: %v", b)
	}
	wantSearchPaths(t, f.search(t, ref, "n.edle", "content"))

	long := t.TempDir()
	fixtureWrite(t, long, "lines.txt", strings.Repeat("needle\n", 25))
	fixtureWrite(t, long, "wide.txt", strings.Repeat(searchAccent, 1000)+"needle"+strings.Repeat(searchAccent, 1000)+"\n")
	longRef := f.register(t, long, nil)
	r = f.search(t, longRef, "needle", "content")
	matches := searchMatches(t, r)
	if len(matches) != 21 || r.body["truncated"] != true {
		t.Fatalf("got %d matches, truncated %v, want 20 lines of the file and the wide line", len(matches), r.body["truncated"])
	}
	for i, match := range matches[:20] {
		if match["path"] != "lines.txt" || match["line"] != float64(i+1) {
			t.Fatalf("match %d = %v, want path then line order", i, match)
		}
	}
	wide := matches[20]
	text := wide["text"].(string)
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) != 240 || !strings.Contains(text, "needle") || strings.ContainsAny(text, "\r\n") || wide["column"] != float64(1001) {
		t.Fatalf("wide line = %d runes, column %v: %q", utf8.RuneCountInString(text), wide["column"], text)
	}

	small := t.TempDir()
	fixtureWrite(t, small, "over.txt", "needle needle needle\n")
	fixtureWrite(t, small, "under.txt", "needle\n")
	wantSearchPaths(t, f.search(t, f.register(t, small, map[string]any{"max_read_bytes": 16}), "needle", "content"), "under.txt")
}

func TestFilesSearchHoldsBackWhatDLPDenies(t *testing.T) {
	var classified atomic.Int64
	f := newSearchFixture(t, WithClassifier(ClassifierFunc(func(text string) ([]SensitivityHit, error) {
		classified.Add(1)
		if strings.Contains(text, "BROKEN") {
			return nil, errors.New("classifier down")
		}
		return fakeClassifier{trigger: "SECRET"}.Classify(text)
	})))
	root := t.TempDir()
	fixtureWrite(t, root, "clean.txt", "needle\n")
	fixtureWrite(t, root, "secret.txt", "needle beside SECRET\n")
	fixtureWrite(t, root, "broken.txt", "needle beside BROKEN\n")
	fixtureWrite(t, root, "other.txt", "SECRET without the word\n")
	ref := f.register(t, root, map[string]any{"dlp_mode": "deny"})

	r := f.search(t, ref, "needle", "content")
	wantSearchPaths(t, r, "clean.txt")
	if strings.Contains(r.raw, "SECRET") || strings.Contains(r.raw, "BROKEN") || strings.Contains(r.raw, "withheld") {
		t.Fatalf("denied files must show nothing, not even a count: %s", r.raw)
	}
	if got := classified.Load(); got != 3 {
		t.Fatalf("classified %d files, want only the 3 that matched", got)
	}
	classified.Store(0)
	r = f.search(t, ref, "needle", "name")
	if classified.Load() != 0 {
		t.Fatalf("a name search reads no content: %d calls, %s", classified.Load(), r.raw)
	}
}

// A caller who may not read a file learns nothing of it from a search: a folder where
// DLP refuses many matching files answers byte for byte as one where it refuses none,
// in count, in paging and in whether the answer was cut.
func TestFilesSearchSaysNothingOfWhatDLPWithholds(t *testing.T) {
	f := newSearchFixture(t, WithClassifier(fakeClassifier{trigger: "SECRET"}))
	none, many := t.TempDir(), t.TempDir()
	for _, root := range []string{none, many} {
		fixtureWrite(t, root, "clean.txt", "needle\n")
	}
	for i := range 30 {
		fixtureWrite(t, many, fmt.Sprintf("a-%02d.txt", i), "needle beside SECRET\n")
		fixtureWrite(t, many, fmt.Sprintf("z-%02d.txt", i), "needle beside SECRET\n")
	}
	body := func() map[string]any { return map[string]any{"dlp_mode": "deny"} }
	noneRef, manyRef := f.register(t, none, body()), f.register(t, many, body())

	for _, query := range []url.Values{
		{"q": {"needle"}, "in": {"content"}},
		{"q": {"needle"}, "in": {"content"}, "limit": {"1"}},
	} {
		want, got := f.get(t, noneRef, query), f.get(t, manyRef, query)
		if want.code != http.StatusOK || got.code != want.code || got.raw != want.raw {
			t.Fatalf("%s: with nothing withheld = %d %s; with 60 files withheld = %d %s", query.Encode(), want.code, want.raw, got.code, got.raw)
		}
		wantSearchPaths(t, got, "clean.txt")
	}
}

func TestFilesSearchStopsAtItsBounds(t *testing.T) {
	f := newSearchFixture(t)
	root := t.TempDir()
	for i := range 20001 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%05d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ref := f.register(t, root, nil)

	r := f.search(t, ref, "f20000", "name")
	wantSearchPaths(t, r)
	if r.body["truncated"] != true {
		t.Fatalf("the file ceiling must say so: %s", r.raw)
	}
	r = f.search(t, ref, "f00000", "name")
	wantSearchPaths(t, r, "f00000")
	if r.body["truncated"] != true {
		t.Fatalf("a cut list stays cut for a query it answers: %s", r.raw)
	}
	r = f.get(t, ref, url.Values{"q": {"f0"}})
	if got := len(searchMatches(t, r)); got != 50 || r.body["truncated"] != true {
		t.Fatalf("default limit gave %d, truncated %v", got, r.body["truncated"])
	}
	r = f.get(t, ref, url.Values{"q": {"f0"}, "limit": {"999"}})
	if got := len(searchMatches(t, r)); got != 200 || r.body["truncated"] != true {
		t.Fatalf("limit ceiling gave %d, truncated %v", got, r.body["truncated"])
	}

	// A client that left ends the walk, and the partial answer says so.
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	opened, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()
	if files, cut := searchableFiles(gone, &resolvedWorkspace{rootReal: root}, opened); len(files) != 0 || !cut {
		t.Fatalf("cancelled walk listed %d files, cut %v", len(files), cut)
	}

	few := t.TempDir()
	for _, rel := range []string{"a-needle", "b-needle", "c-needle"} {
		fixtureWrite(t, few, rel, "needle\n")
	}
	fewRef := f.register(t, few, nil)
	r = f.get(t, fewRef, url.Values{"q": {"needle"}, "in": {"content"}, "limit": {"2"}})
	wantSearchPaths(t, r, "a-needle", "b-needle")
	if r.body["truncated"] != true {
		t.Fatalf("hitting the limit must say so: %s", r.raw)
	}
	r = f.get(t, fewRef, url.Values{"q": {"needle"}, "limit": {"3"}})
	if len(searchMatches(t, r)) != 3 || r.body["truncated"] != false {
		t.Fatalf("a limit that is not hit must not say so: %s", r.raw)
	}

	// 257 sparse files of 1 MiB pass the read budget before the last file is reached.
	heavy := t.TempDir()
	for i := range 257 {
		file, err := os.Create(filepath.Join(heavy, fmt.Sprintf("a%03d", i)))
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(1 << 20); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	}
	fixtureWrite(t, heavy, "zz.txt", "needle\n")
	r = f.search(t, f.register(t, heavy, nil), "needle", "content")
	wantSearchPaths(t, r)
	if r.body["truncated"] != true {
		t.Fatalf("the read budget must say so: %s", r.raw)
	}
}

func TestFilesSearchIsAuditedWithoutItsQuery(t *testing.T) {
	f := newSearchFixture(t, WithClassifier(fakeClassifier{trigger: "SECRET"}))
	root := t.TempDir()
	fixtureWrite(t, root, "secret.txt", "needle beside SECRET\n")
	ref := f.register(t, root, map[string]any{"dlp_mode": "label"})
	audits := func() []acctAuditedMeta { return acctAuditsOf(t, f.m, f.tenant, "sessions.workspace.search") }

	f.search(t, ref, "needle", "content")
	events := audits()
	if len(events) != 1 {
		t.Fatalf("one search made %d audit entries", len(events))
	}
	meta := fmt.Sprint(events[0].meta)
	if events[0].meta["path"] != "" || !strings.Contains(meta, "secret.credential") || strings.Contains(meta, "needle") || strings.Contains(meta, "SECRET") {
		t.Fatalf("audit = %v, want the classes and neither the query nor the content", events[0].meta)
	}

	f.search(t, ref, "secret", "name")
	events = audits()
	if len(events) != 2 || events[1].meta["sensitivity"] != nil {
		t.Fatalf("a name search reads no content, audit = %+v", events)
	}
}
