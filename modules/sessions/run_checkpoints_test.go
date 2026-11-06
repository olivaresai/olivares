// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A session's turns (#3433), over the real routes: each message is a turn, the list says
// what each turn changed, and a rewind puts the folder back as it was before a turn, or
// undoes that.
func TestTurnCheckpointsListWhatEachTurnChangedAndRewindTheFolder(t *testing.T) {
	h, fr, admin, tenant := attachHTTPHarness(t, WithCheckpointRoot(t.TempDir()))
	fr.initSID = "checkpoint-session"
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json", "name": "checkpoints"})
	proc := fr.lastProc()
	base := "/v1/m/sessions/runs/" + ref
	run := h.do("GET", base, admin, tenantHdr(tenant))
	folder, _ := run.body["workspace_path"].(string)
	if folder == "" {
		t.Fatalf("run has no folder: %s", run.raw)
	}
	write := func(name, text string) {
		t.Helper()
		p := filepath.Join(folder, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(folder, filepath.FromSlash(name)))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}
	idle := func() {
		t.Helper()
		waitFor(t, "the turn ended", func() bool {
			q := h.do("GET", base+"/queue", admin, tenantHdr(tenant))
			return q.code == http.StatusOK && q.body["hold"] == nil
		})
	}
	say := func(text string, sent int) {
		t.Helper()
		if r := h.doJSON("POST", base+"/input", admin, map[string]any{"text": text}, tenantHdr(tenant)); r.code != http.StatusAccepted {
			t.Fatalf("message %q = %d %s", text, r.code, r.raw)
		}
		waitFor(t, "the message reached the child", func() bool { return proc.sentCount() == sent })
	}
	endTurn := func() {
		proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(turnResultFrame)}
		idle()
	}
	rewind := func(seq, to string) resp {
		return h.doJSON("POST", base+"/checkpoints/"+seq+"/rewind", admin, map[string]any{"to": to}, tenantHdr(tenant))
	}
	files := func(turn any) map[string]string {
		out := map[string]string{}
		for _, f := range turn.(map[string]any)["files"].([]any) {
			f := f.(map[string]any)
			out[f["path"].(string)] = f["status"].(string)
		}
		return out
	}

	// What the folder held before the session did anything.
	write("a.txt", "v1")
	write("keep/b.txt", "b1")

	say("first", 1)
	if r := rewind("1", "start"); r.code != http.StatusConflict {
		t.Fatalf("rewind while the turn runs = %d %s, want 409", r.code, r.raw)
	}
	write("a.txt", "v2")
	write("new.txt", "n")
	endTurn()

	say("second", 2)
	write("a.txt", "v3")
	if err := os.Remove(filepath.Join(folder, "keep", "b.txt")); err != nil {
		t.Fatal(err)
	}
	endTurn()

	list := h.do("GET", base+"/checkpoints", admin, tenantHdr(tenant))
	turns, _ := list.body["turns"].([]any)
	if list.code != http.StatusOK || list.body["available"] != true || len(turns) != 2 {
		t.Fatalf("checkpoints = %d %s, want 2 turns", list.code, list.raw)
	}
	first, second := turns[0].(map[string]any), turns[1].(map[string]any)
	if first["prompt"] != "first" || second["prompt"] != "second" || first["open"] != false || second["open"] != true {
		t.Fatalf("turns = %s, want first (closed) and second (open)", list.raw)
	}
	if got := files(first); len(got) != 2 || got["a.txt"] != "modified" || got["new.txt"] != "added" {
		t.Fatalf("first turn changed %v, want a.txt modified and new.txt added", got)
	}
	if got := files(second); len(got) != 2 || got["a.txt"] != "modified" || got["keep/b.txt"] != "deleted" {
		t.Fatalf("second turn changed %v, want a.txt modified and keep/b.txt deleted", got)
	}

	// Before the first turn: the folder is as the person left it.
	if r := rewind("1", "start"); r.code != http.StatusOK {
		t.Fatalf("rewind to the first turn = %d %s", r.code, r.raw)
	}
	if got := []string{read("a.txt"), read("new.txt"), read("keep/b.txt")}; got[0] != "v1" || got[1] != "<missing>" || got[2] != "b1" {
		t.Fatalf("after the rewind a.txt, new.txt, keep/b.txt = %q, want v1, missing, b1", got)
	}
	// Undo: the folder as the second turn left it.
	if r := rewind("2", "end"); r.code != http.StatusOK {
		t.Fatalf("undo = %d %s", r.code, r.raw)
	}
	if got := []string{read("a.txt"), read("new.txt"), read("keep/b.txt")}; got[0] != "v3" || got[1] != "n" || got[2] != "<missing>" {
		t.Fatalf("after the undo a.txt, new.txt, keep/b.txt = %q, want v3, n, missing", got)
	}

	// The ledger holds both rewinds.
	rewinds := 0
	ctx := context.Background()
	if err := h.m.Data.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			if strings.HasSuffix(ev.Action, ".git-rewind") {
				rewinds++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if rewinds != 2 {
		t.Fatalf("ledger holds %d rewinds, want 2", rewinds)
	}
}
