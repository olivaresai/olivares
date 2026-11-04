// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// recordFS is a filesystem a test hands to the discovery: it remembers every name
// opened and refuses the denied ones, so the test can say what was never read.
type recordFS struct {
	fs.FS
	deny   map[string]bool
	opened []string
}

func (r *recordFS) Open(name string) (fs.File, error) {
	r.opened = append(r.opened, name)
	if r.deny[name] {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return r.FS.Open(name)
}

func importFile(mod time.Time, data string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data), ModTime: mod}
}

func codexRollout(cwd string) string {
	return `{"type":"session_meta","payload":{"cwd":"` + cwd + `","base_instructions":"SECRET"}}` + "\n" +
		`{"type":"message","text":"CONVERSATION"}` + "\n"
}

func TestDiscoverFolders(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-48 * time.Hour)
	old := now.Add(-90 * 24 * time.Hour)

	// The machine: folders the tools worked in, the person's home, another account's home.
	machine := fstest.MapFS{}
	for _, dir := range []string{
		"srv/work/api", "srv/work/old", "srv/work/few", "srv/my-app", "srv/locked",
		"srv/work/registered", "var/lib/olivares/data", "srv/codex-only", "srv/bob/private",
	} {
		machine[dir+"/main.go"] = importFile(recent, "package main")
	}
	machine["home/ana/.claude/.credentials.json"] = importFile(recent, "TOKEN")
	machine["home/ana/.codex/auth.json"] = importFile(recent, "TOKEN")
	claude := func(project string, chats int, mod time.Time) {
		for i := 0; i < chats; i++ {
			machine["home/ana/.claude/projects/"+project+"/c"+string(rune('a'+i))+".jsonl"] = importFile(mod, "CONVERSATION")
		}
	}
	claude("-srv-work-api", 4, recent)
	claude("-srv-my-app", 3, recent)
	claude("-srv-work-old", 5, old)
	claude("-srv-work-few", 2, recent)
	claude("-srv-gone-project", 3, recent)
	claude("-srv-locked", 3, recent)
	claude("-var-lib-olivares-data", 3, recent)
	claude("-srv-work-registered", 3, recent)
	claude("-srv-my-app-deleted-copy", 3, recent)
	for i, cwd := range []string{"/srv/work/api", "/srv/work/api", "/srv/work/api", "/srv/codex-only"} {
		machine["home/ana/.codex/sessions/2026/11/01/rollout-"+string(rune('a'+i))+".jsonl"] = importFile(recent, codexRollout(cwd))
	}
	// A named pipe named like a record: opening it would block for good.
	machine["home/ana/.codex/sessions/2026/11/01/rollout-pipe.jsonl"] = &fstest.MapFile{Mode: fs.ModeNamedPipe, ModTime: recent}
	machine["home/ana/.codex/sessions/2026/10/30/rollout-old-format.jsonl"] = importFile(recent, `{"id":"x","instructions":"no cwd"}`+"\n")
	machine["home/bob/.claude/projects/-srv-bob-private/a.jsonl"] = importFile(recent, "CONVERSATION")

	anaHome, err := fs.Sub(machine, "home/ana")
	if err != nil {
		t.Fatal(err)
	}
	discovery := func(home, host fs.FS) folderDiscovery {
		return folderDiscovery{
			Home: home, Host: host, Now: now,
			Protect: []string{"/var/lib/olivares"},
			Registered: func(p string) (bool, error) {
				return p == "/srv/work/registered", nil
			},
		}
	}

	t.Run("lists what each tool used and preselects the active ones", func(t *testing.T) {
		home := &recordFS{FS: anaHome}
		host := &recordFS{FS: machine, deny: map[string]bool{"srv/locked": true}}
		got, err := discovery(home, host).run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		rows := map[string]importableFolder{}
		for _, f := range got.Folders {
			rows[f.Tool+":"+f.Path] = f
		}
		want := []struct {
			key         string
			chats       int
			skip        string
			preselected bool
			lastUsed    time.Time
		}{
			{"claude:/srv/work/api", 4, "", true, recent},
			{"codex:/srv/work/api", 3, "", true, recent},
			{"claude:/srv/my-app", 3, "", true, recent},
			{"claude:/srv/work/old", 5, "", false, old},
			{"claude:/srv/work/few", 2, "", false, recent},
			{"codex:/srv/codex-only", 1, "", false, recent},
			{"claude:/srv/gone/project", 3, importSkipMissing, false, recent},
			// The folder is gone but its parent /srv/my-app is not: only the missing part is split.
			{"claude:/srv/my-app/deleted/copy", 3, importSkipMissing, false, recent},
			{"claude:/srv/locked", 3, importSkipUnreadable, false, recent},
			{"claude:/var/lib/olivares/data", 3, importSkipOutside, false, recent},
			{"claude:/srv/work/registered", 3, importSkipRegistered, false, recent},
		}
		if len(got.Folders) != len(want) {
			t.Fatalf("folders = %d rows, want %d: %+v", len(got.Folders), len(want), got.Folders)
		}
		for _, w := range want {
			row, ok := rows[w.key]
			if !ok {
				t.Errorf("%s: not listed", w.key)
				continue
			}
			if row.Conversations != w.chats || row.Skip != w.skip || row.Preselected != w.preselected || !row.LastUsed.Equal(w.lastUsed) {
				t.Errorf("%s = %+v, want chats=%d skip=%q preselected=%v last=%v", w.key, row, w.chats, w.skip, w.preselected, w.lastUsed)
			}
		}
		if len(got.Tools) != 2 || !got.Tools[0].Readable || !got.Tools[1].Readable {
			t.Errorf("tools = %+v, want claude and codex readable", got.Tools)
		}
		for _, f := range got.Folders {
			if strings.Contains(f.Path, "bob") {
				t.Errorf("another account's folder listed: %s", f.Path)
			}
		}
		for _, name := range home.opened {
			switch {
			case strings.HasSuffix(name, ".credentials.json"), strings.HasSuffix(name, "auth.json"):
				t.Errorf("a credential file was opened: %s", name)
			case strings.HasSuffix(name, "rollout-pipe.jsonl"):
				t.Errorf("a named pipe was opened: %s", name)
			case strings.HasPrefix(name, ".claude/projects/") && strings.HasSuffix(name, ".jsonl"):
				t.Errorf("a Claude Code conversation was opened: %s", name)
			}
		}
		for _, name := range host.opened {
			if strings.HasPrefix(name, "home/bob") {
				t.Errorf("another account's home was opened: %s", name)
			}
		}
	})

	t.Run("says so when a tool's records cannot be read", func(t *testing.T) {
		home := &recordFS{FS: anaHome, deny: map[string]bool{".claude/projects": true}}
		got, err := discovery(home, machine).run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		readable := map[string]bool{}
		for _, tool := range got.Tools {
			readable[tool.Tool] = tool.Readable
		}
		if readable["claude"] || !readable["codex"] {
			t.Errorf("tools = %+v, want claude unreadable and codex readable", got.Tools)
		}
		for _, f := range got.Folders {
			if f.Tool == "claude" {
				t.Errorf("a folder came from the unreadable tool: %+v", f)
			}
		}
	})

	t.Run("a tool that never ran for this person is not listed", func(t *testing.T) {
		nobody, err := fs.Sub(machine, "srv")
		if err != nil {
			t.Fatal(err)
		}
		got, err := discovery(nobody, machine).run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Tools) != 0 || len(got.Folders) != 0 {
			t.Errorf("got %+v, want nothing", got)
		}
	})

	t.Run("no home to read means every tool is unreadable", func(t *testing.T) {
		got, err := discovery(nil, machine).run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Folders) != 0 || len(got.Tools) != 2 {
			t.Fatalf("got %+v, want two unreadable tools and no folders", got)
		}
		for _, tool := range got.Tools {
			if tool.Readable {
				t.Errorf("tool %s readable without a home", tool.Tool)
			}
		}
	})
}
