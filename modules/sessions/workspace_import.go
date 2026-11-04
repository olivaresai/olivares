// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
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

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/model"
)

// Why a folder a tool used cannot be registered now. The values are the console's
// vocabulary (FolderCandidateSkip in web/src/features/agentops/types.ts).
const (
	importSkipMissing    = "missing"
	importSkipUnreadable = "unreadable"
	importSkipOutside    = "outside_allowed_roots"
	importSkipRegistered = "already_registered"
)

const (
	// A folder is preselected when it was used in the last importActiveWindow, in at
	// least importActiveConversations conversations.
	importActiveWindow        = 30 * 24 * time.Hour
	importActiveConversations = 3
	// importMaxRollouts bounds the Codex records read (newest first) and
	// importRolloutHead the bytes read of each one: its first line names the folder.
	// ponytail: a Codex history older than the newest 5000 sessions is not offered; raise it when someone has more.
	importMaxRollouts = 5000
	importRolloutHead = 1 << 20
)

var errNoHome = errors.New("no home directory to read")

// importableFolder is one folder a tool already worked in, from that tool's own records.
type importableFolder struct {
	Path          string    `json:"path"`
	Tool          string    `json:"tool"`
	LastUsed      time.Time `json:"last_used"`
	Conversations int       `json:"conversations"`
	Preselected   bool      `json:"preselected"`
	Skip          string    `json:"skip,omitempty"`
}

// importTool says whether a tool's records could be read. A tool with no records at all
// is not listed.
type importTool struct {
	Tool     string `json:"tool"`
	Name     string `json:"name"`
	Readable bool   `json:"readable"`
}

type folderCandidates struct {
	Tools   []importTool       `json:"tools"`
	Folders []importableFolder `json:"folders"`
}

// folderUse is what a tool's records say about one folder.
type folderUse struct {
	conversations int
	last          time.Time
}

func (u *folderUse) add(modified time.Time) {
	u.conversations++
	if modified.After(u.last) {
		u.last = modified
	}
}

// folderDiscovery lists the folders the tools already used. It reads only each tool's
// records of project paths and dates: a directory listing for Claude Code, the first
// line of a Codex session for its working folder. It opens no credential and no
// conversation. Home is the account's home (nil: it cannot be read) and Host the
// machine, to tell whether a folder still exists.
type folderDiscovery struct {
	Home       fs.FS
	Host       fs.FS
	Now        time.Time
	Protect    []string
	Registered func(path string) (bool, error)
}

var importTools = []struct {
	tool, name string
	read       func(folderDiscovery, context.Context) (map[string]*folderUse, error)
}{
	{"claude", "Claude Code", folderDiscovery.claudeFolders},
	{"codex", "Codex", folderDiscovery.codexFolders},
}

func (d folderDiscovery) run(ctx context.Context) (folderCandidates, error) {
	out := folderCandidates{Tools: []importTool{}, Folders: []importableFolder{}}
	for _, tool := range importTools {
		uses, err := map[string]*folderUse(nil), errNoHome
		if d.Home != nil {
			uses, err = tool.read(d, ctx)
		}
		if cerr := ctx.Err(); cerr != nil {
			return folderCandidates{}, cerr
		}
		if d.Home != nil && errors.Is(err, fs.ErrNotExist) {
			continue // the tool never ran for this account
		}
		out.Tools = append(out.Tools, importTool{Tool: tool.tool, Name: tool.name, Readable: err == nil})
		for folder, use := range uses {
			skip, err := d.skipReason(folder)
			if err != nil {
				return folderCandidates{}, err
			}
			out.Folders = append(out.Folders, importableFolder{
				Path: folder, Tool: tool.tool, LastUsed: use.last, Conversations: use.conversations, Skip: skip,
				Preselected: skip == "" && use.conversations >= importActiveConversations && !use.last.Before(d.Now.Add(-importActiveWindow)),
			})
		}
	}
	sort.Slice(out.Folders, func(i, j int) bool {
		a, b := out.Folders[i], out.Folders[j]
		if !a.LastUsed.Equal(b.LastUsed) {
			return a.LastUsed.After(b.LastUsed)
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Tool < b.Tool
	})
	return out, nil
}

// skipReason is why a folder cannot be registered now ("" when it can). It is the same
// reach rule the registration applies: a folder that overlaps a protected engine folder
// is outside what a session may reach.
func (d folderDiscovery) skipReason(folder string) (string, error) {
	rel := strings.TrimPrefix(path.Clean(folder), "/")
	if rel == "" {
		rel = "."
	}
	// Stat never blocks, so only a directory is opened: a named pipe would block for good.
	info, err := fs.Stat(d.Host, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return importSkipMissing, nil
		}
		return importSkipUnreadable, nil
	}
	if !info.IsDir() {
		return importSkipMissing, nil
	}
	f, err := d.Host.Open(rel)
	if err != nil {
		return importSkipUnreadable, nil
	}
	f.Close()
	for _, protected := range d.Protect {
		if pathsOverlap(folder, protected) {
			return importSkipOutside, nil
		}
	}
	registered, err := d.Registered(folder)
	if err != nil {
		return "", err
	}
	if registered {
		return importSkipRegistered, nil
	}
	return "", nil
}

// claudeFolders reads ~/.claude/projects: one directory per folder Claude Code worked
// in, one *.jsonl per conversation. Only the listing and the modification times are
// read; a conversation file is never opened.
func (d folderDiscovery) claudeFolders(ctx context.Context) (map[string]*folderUse, error) {
	const root = ".claude/projects"
	projects, err := fs.ReadDir(d.Home, root)
	if err != nil {
		return nil, err
	}
	uses := map[string]*folderUse{}
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !project.IsDir() || !strings.HasPrefix(project.Name(), "-") {
			continue
		}
		files, err := fs.ReadDir(d.Home, path.Join(root, project.Name()))
		if err != nil {
			continue // this project's records cannot be listed: it is not offered
		}
		folder := d.claudeFolder(strings.TrimPrefix(project.Name(), "-"))
		for _, file := range files {
			info, err := file.Info()
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".jsonl") || err != nil {
				continue
			}
			if uses[folder] == nil {
				uses[folder] = &folderUse{}
			}
			uses[folder].add(info.ModTime())
		}
	}
	return uses, nil
}

// claudeFolder is the folder a Claude Code project directory names. Claude Code writes
// the folder with every character that is not a letter or digit turned into "-", so
// the name alone is ambiguous ("my-app", "my_app" and "my/app" are one name). The
// directories that exist decide. A folder that is gone is the longest prefix that
// still exists, then the rest with every "-" as a separator, and it is reported missing.
func (d folderDiscovery) claudeFolder(encoded string) string {
	parts := strings.Split(encoded, "-")
	for n := len(parts); n > 0; n-- {
		if rel, ok := d.decodeUnder(".", strings.Join(parts[:n], "-")); ok {
			return path.Join(append([]string{"/", rel}, parts[n:]...)...)
		}
	}
	return "/" + strings.Join(parts, "/")
}

func (d folderDiscovery) decodeUnder(dir, rest string) (string, bool) {
	entries, err := fs.ReadDir(d.Host, dir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&fs.ModeSymlink == 0 {
			continue
		}
		name := encodeClaudeName(entry.Name())
		child := path.Join(dir, entry.Name())
		switch {
		case rest == name:
			return child, true
		case strings.HasPrefix(rest, name+"-"):
			if found, ok := d.decodeUnder(child, rest[len(name)+1:]); ok {
				return found, true
			}
		}
	}
	return "", false
}

// encodeClaudeName is how Claude Code writes one path component into a project name.
func encodeClaudeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("--") // two UTF-16 units
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// codexFolders reads ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl: the first line of each
// names the folder the session ran in. Only that first JSON value is decoded, and only
// its working folder is kept.
func (d folderDiscovery) codexFolders(ctx context.Context) (map[string]*folderUse, error) {
	const root = ".codex/sessions"
	type rollout struct {
		path     string
		modified time.Time
	}
	var rollouts []rollout
	err := fs.WalkDir(d.Home, root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil // a directory that cannot be listed adds nothing
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		// Only a regular file is opened: opening a named pipe would block for good.
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "rollout-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			rollouts = append(rollouts, rollout{p, info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The path carries the date, so the newest sessions sort last.
	sort.Slice(rollouts, func(i, j int) bool { return rollouts[i].path > rollouts[j].path })
	if len(rollouts) > importMaxRollouts {
		rollouts = rollouts[:importMaxRollouts]
	}
	uses := map[string]*folderUse{}
	for _, r := range rollouts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		folder := d.rolloutFolder(r.path)
		if folder == "" {
			continue
		}
		if uses[folder] == nil {
			uses[folder] = &folderUse{}
		}
		uses[folder].add(r.modified)
	}
	return uses, nil
}

// rolloutFolder is the working folder a Codex session record names ("" when it names
// none, as the older record format does).
func (d folderDiscovery) rolloutFolder(name string) string {
	f, err := d.Home.Open(name)
	if err != nil {
		return ""
	}
	defer f.Close()
	var first struct {
		Payload struct {
			Cwd string `json:"cwd"`
		} `json:"payload"`
	}
	if json.NewDecoder(io.LimitReader(f, importRolloutHead)).Decode(&first) != nil || !path.IsAbs(first.Payload.Cwd) {
		return ""
	}
	return path.Clean(first.Payload.Cwd)
}

// sessionsFolderCandidatesResponses publishes the answer of the folders the tools used.
func sessionsFolderCandidatesResponses(method, pattern string) (map[string]any, bool) {
	if method != http.MethodGet || pattern != "/workspace-candidates" {
		return nil, false
	}
	text := oas.Obj("type", "string")
	tool := sessionsClosureClosedObject(oas.Obj("tool", text, "name", text, "readable", oas.Obj("type", "boolean", "description", "False when the tool's records exist but could not be read.")), "tool", "name", "readable")
	folder := sessionsClosureClosedObject(oas.Obj(
		"path", text, "tool", text,
		"last_used", sessionsTimestampSchema(),
		"conversations", oas.Obj("type", "integer", "minimum", 0),
		"preselected", oas.Obj("type", "boolean", "description", "Used in the last 30 days in at least 3 conversations, and not skipped."),
		"skip", oas.Obj("type", "string", "enum", oas.Enum(importSkipMissing, importSkipUnreadable, importSkipOutside, importSkipRegistered), "description", "Why the folder cannot be registered now; absent when it can."),
	), "path", "tool", "last_used", "conversations", "preselected")
	responses := api.GenericModuleResponses()
	responses["200"] = oas.JSONRespSchema("The folders the tools already worked in, and which tools' records could be read", sessionsClosureClosedObject(oas.Obj(
		"tools", oas.Obj("type", "array", "items", tool),
		"folders", oas.Obj("type", "array", "items", folder),
	), "tools", "folders"))
	return responses, true
}

// handleWorkspaceCandidates lists the folders the tools of the engine's own account already worked in, read from the tools' own records of project paths and dates (never a credential or conversation text), each with the tool, the last use, the number of conversations, whether it is preselected (used in the last 30 days in at least 3 conversations) and the reason it cannot be registered. It only reads: nothing is registered until the administrator registers a folder through the folder registration.
func (m *Module) handleWorkspaceCandidates(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.Data == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("workspace registry not available"))
		return
	}
	d := folderDiscovery{
		Host: os.DirFS("/"), Now: time.Now(), Protect: m.rt.confineProtect,
		Registered: func(folder string) (bool, error) {
			q := model.Query{Limit: 1, Filters: []model.Filter{
				{Column: colWsRootPath, Op: model.OpEq, Value: resolvedPath(folder)},
				{Column: colWsState, Op: model.OpEq, Value: wsActive},
			}}
			found, err := m.listWorkspaces(r.Context(), mc.Tenant, q)
			return len(found.Items) > 0, err
		},
	}
	if home := engineUserHome(); home != "" {
		d.Home = os.DirFS(home)
	}
	out, err := d.run(r.Context())
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
