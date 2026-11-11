// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A session's turns, over the real routes: each message is a turn, the list says
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
	write("a.txt", "x\nv1\n")
	write("keep/b.txt", "b1")

	say("first", 1)
	if r := rewind("1", "start"); r.code != http.StatusConflict {
		t.Fatalf("rewind while the turn runs = %d %s, want 409", r.code, r.raw)
	}
	write("a.txt", "x\nv2\n")
	write("new.txt", "n\n")
	endTurn()

	// One turn so far: it and the whole session count the same lines.
	counts := func(list []any) map[string][2]any {
		out := map[string][2]any{}
		for _, f := range list {
			f := f.(map[string]any)
			out[f["path"].(string)] = [2]any{f["added"], f["removed"]}
		}
		return out
	}
	one := h.do("GET", base+"/checkpoints", admin, tenantHdr(tenant))
	oneTurns, _ := one.body["turns"].([]any)
	sessionFiles, _ := one.body["files"].([]any)
	want := map[string][2]any{"a.txt": {1.0, 1.0}, "new.txt": {1.0, 0.0}}
	if len(oneTurns) != 1 || !maps.Equal(counts(oneTurns[0].(map[string]any)["files"].([]any)), want) ||
		!maps.Equal(counts(sessionFiles), want) || one.body["truncated"] != false {
		t.Fatalf("checkpoints after one turn = %s, want a.txt +1 -1 and new.txt +1 in the turn and the session", one.raw)
	}

	say("second", 2)
	write("a.txt", "x\nv3\n")
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
	if got := []string{read("a.txt"), read("new.txt"), read("keep/b.txt")}; got[0] != "x\nv1\n" || got[1] != "<missing>" || got[2] != "b1" {
		t.Fatalf("after the rewind a.txt, new.txt, keep/b.txt = %q, want v1, missing, b1", got)
	}
	// Undo: the folder as the second turn left it.
	if r := rewind("2", "end"); r.code != http.StatusOK {
		t.Fatalf("undo = %d %s", r.code, r.raw)
	}
	if got := []string{read("a.txt"), read("new.txt"), read("keep/b.txt")}; got[0] != "x\nv3\n" || got[1] != "n\n" || got[2] != "<missing>" {
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

func TestLineTurnCheckpointsAndRewind(t *testing.T) {
	h, fr, admin, tenant := attachHTTPHarness(t, WithCheckpointRoot(t.TempDir()))
	fr.initSID = "line-checkpoint-session"
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json"})
	proc := fr.lastProc()
	base := "/v1/m/sessions/runs/" + ref
	run := h.do("GET", base, admin, tenantHdr(tenant))
	folder := run.body["workspace_path"].(string)
	file := filepath.Join(folder, "note.txt")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	send := func(body map[string]any, sent int) {
		t.Helper()
		if r := h.doJSON("POST", base+"/input", admin, body, tenantHdr(tenant)); r.code != http.StatusAccepted {
			t.Fatalf("input = %d %s", r.code, r.raw)
		}
		waitFor(t, "input reached the child", func() bool { return proc.sentCount() == sent })
	}
	line := func(text string) map[string]any {
		return map[string]any{"line": `{"type":"user","message":{"role":"user","content":"` + text + `"}}`}
	}
	write("before")
	sent := 0
	for i, text := range []string{"first", "second", "third"} {
		body := line(text)
		// The text route also encodes a Claude user frame; it must keep only one checkpoint.
		if i == 1 {
			body = map[string]any{"text": text}
		}
		sent++
		send(body, sent)
		if i == 0 {
			sent++
			send(line("steer"), sent)
		}
		write(text)
		if i == 0 {
			proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(turnResultFrame)}
		}
		proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(turnResultFrame)}
		waitFor(t, "turn ended", func() bool {
			q := h.do("GET", base+"/queue", admin, tenantHdr(tenant))
			return q.code == http.StatusOK && q.body["hold"] == nil
		})
	}
	list := h.do("GET", base+"/checkpoints", admin, tenantHdr(tenant))
	turns, _ := list.body["turns"].([]any)
	if list.code != http.StatusOK || list.body["available"] != true || len(turns) != 3 {
		t.Fatalf("checkpoints = %d %s, want 3 turns", list.code, list.raw)
	}
	for i, text := range []string{"first", "second", "third"} {
		if turns[i].(map[string]any)["prompt"] != text {
			t.Fatalf("turn %d prompt = %v, want %q", i+1, turns[i], text)
		}
	}
	if r := h.doJSON("POST", base+"/checkpoints/1/rewind", admin, map[string]any{"to": "start"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("rewind = %d %s", r.code, r.raw)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "before" {
		t.Fatalf("rewound file = %q, %v; want before", got, err)
	}
}

// «Edit from here» on a session in its own worktree (ruling 45): a fork at its second
// message starts in a new worktree that holds the files of that moment, the source's
// checkpoint before that message, not the registered folder's checkout. Only the source's
// launcher forks it; anyone else, like a missing session or turn, gets 404.
func TestForkOfAWorktreeSessionStartsAtItsCheckpoint(t *testing.T) {
	testForkStartsAtTheCheckpoint(t, true)
}

// The same fork of a session that ran in the registered folder itself: the fork gets its own
// worktree at that message, and the shared folder is left as it is, never rewound.
func TestForkOfARegisteredFolderSessionLeavesTheFolder(t *testing.T) {
	testForkStartsAtTheCheckpoint(t, false)
}

func testForkStartsAtTheCheckpoint(t *testing.T, sourceInWorktree bool) {
	repo := gitRepoFixture(t)
	cpRoot := t.TempDir()
	fr := &fakeRunner{initSID: "sess-fork-source"}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(fr), WithCredentialSource(staticCred()), WithCheckpointRoot(cpRoot))
	if err := m.UseSessionWorktrees(t.TempDir(), ""); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "fork")
	wr := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": repo, "name": "repo"}, tenantHdr(tenant))
	ws, _ := wr.body["workspace_ref"].(string)
	if wr.code != http.StatusCreated || ws == "" {
		t.Fatalf("workspace = %d %s", wr.code, wr.raw)
	}
	create := func(extra map[string]any) resp {
		t.Helper()
		body := map[string]any{"provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant), "transport": "stream-json", "permission_mode": "default",
			"isolation": "native", "workspace_ref": ws, "worktree": true}
		for k, v := range extra {
			body[k] = v
		}
		return h.doJSON("POST", "/v1/m/sessions/runs", admin, body, tenantHdr(tenant))
	}
	src := create(map[string]any{"name": "source", "worktree": sourceInWorktree})
	ref, _ := src.body["run_ref"].(string)
	folder, _ := src.body["workspace_path"].(string)
	if src.code != http.StatusCreated || folder == "" || (folder == repo) == sourceInWorktree {
		t.Fatalf("source in a worktree = %d %s", src.code, src.raw)
	}
	proc := fr.lastProc()
	base := "/v1/m/sessions/runs/" + ref
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(folder, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	say := func(text string, sent int) {
		t.Helper()
		if r := h.doJSON("POST", base+"/input", admin, map[string]any{"text": text}, tenantHdr(tenant)); r.code != http.StatusAccepted {
			t.Fatalf("message %q = %d %s", text, r.code, r.raw)
		}
		waitFor(t, "the message reached the child", func() bool { return proc.sentCount() == sent })
	}
	endTurn := func() {
		t.Helper()
		proc.out <- OutputFrame{Stream: streamStdout, Data: []byte(turnResultFrame)}
		waitFor(t, "the turn ended", func() bool {
			q := h.do("GET", base+"/queue", admin, tenantHdr(tenant))
			return q.code == http.StatusOK && q.body["hold"] == nil
		})
	}
	say("first", 1)
	write("a.txt", "after the first message\n")
	write("b.txt", "made by the first message\n")
	endTurn()
	say("second", 2)
	write("a.txt", "after the second message\n")
	if err := os.Remove(filepath.Join(folder, "b.txt")); err != nil {
		t.Fatal(err)
	}
	write("c.txt", "made by the second message\n")
	endTurn()

	fr.initSID = "sess-fork"
	fork := create(map[string]any{"name": "fork", "from_checkpoint": map[string]any{"run": ref, "turn": 2}})
	forked, _ := fork.body["workspace_path"].(string)
	if fork.code != http.StatusCreated || forked == "" || forked == folder || forked == repo {
		t.Fatalf("fork at the second message = %d %s, want a new worktree", fork.code, fork.raw)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(forked, name))
		if err != nil {
			return "<missing>"
		}
		return string(b)
	}
	if a, b, c := read("a.txt"), read("b.txt"), read("c.txt"); a != "after the first message\n" || b != "made by the first message\n" || c != "<missing>" {
		t.Fatalf("the fork holds a=%q b=%q c=%q, want the files as the second message found them", a, b, c)
	}
	if a, err := os.ReadFile(filepath.Join(folder, "a.txt")); err != nil || string(a) != "after the second message\n" {
		t.Fatalf("the source's folder holds a=%q (%v) after the fork, want it untouched", a, err)
	}
	if _, err := os.Stat(filepath.Join(folder, "c.txt")); err != nil {
		t.Fatalf("the source's folder lost c.txt to the fork: %v", err)
	}

	// Another person's session answers as a missing one, and so does a turn it never had.
	rec, err := m.loadRun(context.Background(), tenant, ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		from map[string]any
	}{
		{"a missing session", map[string]any{"run": "no-such-run", "turn": 1}},
		{"a turn it never had", map[string]any{"run": ref, "turn": 9}},
	} {
		if r := create(map[string]any{"from_checkpoint": c.from}); r.code != http.StatusNotFound {
			t.Errorf("fork from %s = %d %s, want 404", c.name, r.code, r.raw)
		}
	}
	// Each answer the console acts on has its own code, so it never reads a message.
	if r := create(map[string]any{"from_checkpoint": map[string]any{"run": ref, "turn": 9}}); answeredCode(r.body) != "checkpoint_not_found" {
		t.Errorf("fork from a turn it never had answers %s, want code checkpoint_not_found", r.raw)
	}
	turnsFile := filepath.Join(cpRoot, tenant.String(), ref, checkpointIndexFile)
	raw, err := os.ReadFile(turnsFile)
	if err != nil {
		t.Fatal(err)
	}
	var turns []checkpointTurn
	if err := json.Unmarshal(raw, &turns); err != nil || len(turns) < 2 {
		t.Fatalf("turns %s: %v", raw, err)
	}
	turns[1].Start = strings.Repeat("a", len(turns[1].Start)) // a tree the repository no longer has
	if err := writeCheckpointTurns(filepath.Dir(turnsFile), turns); err != nil {
		t.Fatal(err)
	}
	if r := create(map[string]any{"from_checkpoint": map[string]any{"run": ref, "turn": 2}}); r.code != http.StatusInternalServerError || answeredCode(r.body) != "checkpoint_files_gone" {
		t.Errorf("fork from a pruned checkpoint = %d %s, want 500 checkpoint_files_gone", r.code, r.raw)
	}
	plain := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir(), "name": "plain"}, tenantHdr(tenant))
	pr, _ := plain.body["workspace_ref"].(string)
	if r := create(map[string]any{"workspace_ref": pr}); r.code != http.StatusUnprocessableEntity || answeredCode(r.body) != "not_a_git_repository" {
		t.Errorf("a worktree in a folder that is not a Git repository = %d %s, want 422 not_a_git_repository", r.code, r.raw)
	}
	if err := m.Data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec[colRunLauncherUserID] = model.NewID().String()
		_, err = runs.Update(context.Background(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r := create(map[string]any{"from_checkpoint": map[string]any{"run": ref, "turn": 2}}); r.code != http.StatusNotFound {
		t.Fatalf("fork of another person's session = %d %s, want 404", r.code, r.raw)
	}
}

// answeredCode is the code of an error answer, which the API nests under "error".
func answeredCode(body map[string]any) any {
	e, _ := body["error"].(map[string]any)
	return e["code"]
}
