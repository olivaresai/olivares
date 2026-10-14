// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// commandsAnsweringProc answers the engine's `initialize` request the way the
// bridge hands it over: straight to the waiter.
type commandsAnsweringProc struct {
	*previewTestProc
	lr       *liveRun
	commands string
	asked    int
}

func (p *commandsAnsweringProc) Send(_ context.Context, line []byte) error {
	p.asked++
	var req struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(line, &req); err != nil {
		return err
	}
	p.lr.takeCommandsResponse(claudeInitializeAnswer(req.RequestID, p.commands, catalogAgents))
	return nil
}

// adminToken logs the fixture's administrator in again: the one-time setup already ran.
func (f *previewFixture) adminToken(t *testing.T) string {
	t.Helper()
	r := f.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"}, nil)
	token, _ := r.body["token"].(string)
	if r.code != http.StatusOK || token == "" {
		t.Fatalf("admin login = %d %s", r.code, r.raw)
	}
	return token
}

// endingProc is a session whose process ends when the engine writes to it.
type endingProc struct {
	*previewTestProc
	lr *liveRun
}

func (p *endingProc) Send(context.Context, []byte) error {
	close(p.lr.finalizedCh)
	return nil
}

// ownLaunch gives the fixture's run what a real launch has: a launch id on its row and a
// durable Claim. The read that asks the tool proves that authority before it writes, so the
// fixture has to hold it for real; nothing in the module is relaxed for a test.
func (f *previewFixture) ownLaunch(t *testing.T, lr *liveRun) {
	t.Helper()
	ctx := context.Background()
	sid, err := f.m.ResolveSession(ctx, f.tenant, SessionBinding{
		Provider: providerDriverClaude, ExternalID: f.runRef, WorkspaceID: f.workspace,
	})
	if err != nil {
		t.Fatalf("resolve session identity: %v", err)
	}
	launch := model.NewID()
	// Claim derives its workspace from the canonical session identity, just as on launch.
	if err := mutateRunForWorkTest(f.m, f.tenant, f.runRef, func(row model.Record) {
		row[colRuntimeLaunchID], row[colRunClaimSID] = launch.String(), sid
	}); err != nil {
		t.Fatalf("record the launch: %v", err)
	}
	lease, err := f.m.Claim(context.Background(), f.tenant, sid, "commands-route-test", time.Hour)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	lr.launchID, lr.claim = launch, lease
}

// GET /runs/{ref}/commands asks the live tool through the session's stdin, as an
// interrupt does, so it needs the run's write permission: a reader is refused before
// anything is written, and no one outside the organization learns the session exists.
func TestRunCommandsRoute(t *testing.T) {
	f := newPreviewFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	lr, ok := f.m.rt.getLive(f.tenant, f.runRef)
	if !ok {
		t.Fatal("fixture run is not live")
	}
	f.ownLaunch(t, lr)
	lr.proc = &commandsAnsweringProc{previewTestProc: &previewTestProc{pid: 4712}, lr: lr, commands: catalogCommands}
	path := "/v1/m/sessions/runs/" + f.runRef + "/commands"

	admin := f.adminToken(t)

	t.Run("a workspace editor gets the tool's catalog", func(t *testing.T) {
		r := f.do("GET", path, f.editor, tenantHdr(f.tenant))
		if r.code != http.StatusOK {
			t.Fatalf("GET = %d %s, want 200", r.code, r.raw)
		}
		if r.body["tool"] != "claude" || r.body["reported"] != true {
			t.Fatalf("body = %s, want tool claude, reported true", r.raw)
		}
		cmds, _ := r.body["commands"].([]any)
		found := map[string]map[string]any{}
		for _, c := range cmds {
			e := c.(map[string]any)
			found[e["name"].(string)] = e
		}
		if e := found["compact"]; e == nil || e["source"] != "built-in" || e["kind"] != "command" || e["argument_hint"] == "" {
			t.Fatalf("compact = %v in %s", e, r.raw)
		}
		if e := found["Explore"]; e == nil || e["kind"] != "agent" {
			t.Fatalf("Explore = %v in %s", e, r.raw)
		}
		if got := r.header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q, want no-store", got)
		}
	})

	t.Run("asking the tool writes to the session, so a reader who may not write is refused", func(t *testing.T) {
		viewerEmail := "commands-viewer@test.io"
		made := f.doJSON("POST", "/v1/users", admin, map[string]any{
			"email": viewerEmail, "password": "memberpass1",
			"tenant": f.tenant.String(), "role": auth.RoleViewer, "workspace_id": f.workspace.String(),
		}, nil)
		if made.code != http.StatusCreated {
			t.Fatalf("create viewer = %d %s", made.code, made.raw)
		}
		login := f.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": viewerEmail, "password": "memberpass1"}, nil)
		viewer, _ := login.body["token"].(string)
		if viewer == "" {
			t.Fatalf("viewer login = %d %s", login.code, login.raw)
		}
		before := lr.proc.(*commandsAnsweringProc).asked
		if r := f.do("GET", path, viewer, tenantHdr(f.tenant)); r.code != http.StatusForbidden {
			t.Fatalf("viewer GET = %d %s, want 403", r.code, r.raw)
		}
		if after := lr.proc.(*commandsAnsweringProc).asked; after != before {
			t.Fatalf("a refused reader made the engine write %d control request(s) to the session", after-before)
		}
	})

	t.Run("another organization cannot learn the session exists", func(t *testing.T) {
		other := f.createOrg(admin, "commands-other")
		if r := f.do("GET", path, admin, tenantHdr(other)); r.code != http.StatusNotFound {
			t.Fatalf("GET from another tenant = %d %s, want 404", r.code, r.raw)
		}
	})

	t.Run("no credential is a 401", func(t *testing.T) {
		if r := f.do("GET", path, "", tenantHdr(f.tenant)); r.code != http.StatusUnauthorized {
			t.Fatalf("GET without a token = %d %s, want 401", r.code, r.raw)
		}
	})

	t.Run("an unknown session is a 404", func(t *testing.T) {
		unknown := "/v1/m/sessions/runs/" + model.NewID().String() + "/commands"
		if r := f.do("GET", unknown, f.editor, tenantHdr(f.tenant)); r.code != http.StatusNotFound {
			t.Fatalf("GET unknown = %d %s, want 404", r.code, r.raw)
		}
	})

	t.Run("an editor outside the claim workspace is refused as interrupt is", func(t *testing.T) {
		var otherWorkspace model.ID
		if err := f.st.Mutate(t.Context(), f.tenant, func(sc store.Scope) error {
			ws, err := sc.Workspaces().Create(t.Context(), model.Workspace{
				Name: "Other commands workspace", Slug: "other-commands", Status: model.StatusActive,
			})
			otherWorkspace = ws.ID
			return err
		}); err != nil {
			t.Fatal(err)
		}
		outside := f.member(t, admin, f.tenant, "commands-outside@test.io", otherWorkspace)
		before := lr.proc.(*commandsAnsweringProc).asked
		interrupt := f.do("POST", "/v1/m/sessions/runs/"+f.runRef+"/interrupt", outside, tenantHdr(f.tenant))
		commands := f.do("GET", path, outside, tenantHdr(f.tenant))
		if interrupt.code != http.StatusNotFound || commands.code != interrupt.code || commands.raw != interrupt.raw {
			t.Fatalf("outside workspace: commands = %d %s, interrupt = %d %s, want identical 404 refusals",
				commands.code, commands.raw, interrupt.code, interrupt.raw)
		}
		if after := lr.proc.(*commandsAnsweringProc).asked; after != before {
			t.Fatalf("outside workspace sent %d control requests, want none", after-before)
		}
	})

	t.Run("an editor whose launch lost its claim is refused as interrupt is", func(t *testing.T) {
		lease := lr.claim
		if err := f.m.Release(t.Context(), f.tenant, lease.SID, lease.Holder, lease.Fence); err != nil {
			t.Fatalf("release launch claim: %v", err)
		}
		t.Cleanup(func() {
			next, err := f.m.Claim(context.Background(), f.tenant, lease.SID, lease.Holder, time.Hour)
			if err != nil {
				t.Fatalf("restore launch claim: %v", err)
			}
			lr.claim = next
		})
		// The successful read above populated the cache. Force a refresh to exercise
		// assertRunAuthority at the stdin boundary, rather than just serve that cache.
		lr.commands.dirty.Store(true)
		before := lr.proc.(*commandsAnsweringProc).asked
		interrupt := f.do("POST", "/v1/m/sessions/runs/"+f.runRef+"/interrupt", f.editor, tenantHdr(f.tenant))
		commands := f.do("GET", path, f.editor, tenantHdr(f.tenant))
		if interrupt.code != http.StatusForbidden || commands.code != interrupt.code || commands.raw != interrupt.raw {
			t.Fatalf("released claim: commands = %d %s, interrupt = %d %s, want identical 403 refusals",
				commands.code, commands.raw, interrupt.code, interrupt.raw)
		}
		if after := lr.proc.(*commandsAnsweringProc).asked; after != before {
			t.Fatalf("released claim sent %d control requests, want none", after-before)
		}
	})

	t.Run("a provider with an owned driver is not asked with Claude's request", func(t *testing.T) {
		owned := &commandsAnsweringProc{previewTestProc: &previewTestProc{pid: 4712}, lr: lr, commands: catalogCommands}
		f.m.rt.dropLive(f.tenant, f.runRef)
		f.m.rt.putLive(&liveRun{tenant: f.tenant, runRef: f.runRef, transport: TransportStreamJSON, proc: owned, ring: newOutputRing(8, 1024), session: &codexSession{}})
		r := f.do("GET", path, f.editor, tenantHdr(f.tenant))
		cmds, isList := r.body["commands"].([]any)
		if r.code != http.StatusOK || r.body["reported"] != false || !isList || len(cmds) != 0 {
			t.Fatalf("GET on a driver session = %d %s, want 200, reported false, an empty list", r.code, r.raw)
		}
		if owned.asked != 0 {
			t.Fatalf("the driver session's process was sent %d Claude control request(s), want none", owned.asked)
		}
	})

	t.Run("a finished session still registered answers reported=false and is not written to", func(t *testing.T) {
		ended := &commandsAnsweringProc{previewTestProc: &previewTestProc{pid: 4712}, commands: catalogCommands}
		gone := &liveRun{tenant: f.tenant, runRef: f.runRef, transport: TransportStreamJSON, proc: ended, ring: newOutputRing(8, 1024), finalized: true}
		ended.lr = gone
		f.m.rt.dropLive(f.tenant, f.runRef)
		f.m.rt.putLive(gone)
		r := f.do("GET", path, f.editor, tenantHdr(f.tenant))
		if r.code != http.StatusOK || r.body["reported"] != false || r.body["tool"] != "claude" || ended.asked != 0 {
			t.Fatalf("GET on a finished session = %d %s after %d request(s), want 200, reported false, nothing written", r.code, r.raw, ended.asked)
		}
	})

	t.Run("a session that ends while it is asked reports nothing, not its old catalog", func(t *testing.T) {
		ending := &endingProc{previewTestProc: &previewTestProc{pid: 4712}}
		old := &liveRun{tenant: f.tenant, runRef: f.runRef, transport: TransportStreamJSON, proc: ending, ring: newOutputRing(8, 1024), finalizedCh: make(chan struct{}), launchID: lr.launchID, claim: lr.claim}
		ending.lr = old
		// It reported once, long ago, and a change is pending: the next read asks.
		old.commands.items, old.commands.ok, old.commands.at = []toolCommand{{Name: "old", Kind: "command"}}, true, time.Now().Add(-time.Hour)
		f.m.rt.dropLive(f.tenant, f.runRef)
		f.m.rt.putLive(old)
		r := f.do("GET", path, f.editor, tenantHdr(f.tenant))
		cmds, _ := r.body["commands"].([]any)
		if r.code != http.StatusOK || r.body["reported"] != false || len(cmds) != 0 || r.body["stale"] == true {
			t.Fatalf("GET on a session that ended meanwhile = %d %s, want 200, reported false, an empty list", r.code, r.raw)
		}
	})

	t.Run("a session that is not live reports nothing, in a body the console can say in one line", func(t *testing.T) {
		f.m.rt.dropLive(f.tenant, f.runRef)
		r := f.do("GET", path, f.editor, tenantHdr(f.tenant))
		cmds, isList := r.body["commands"].([]any)
		if r.code != http.StatusOK || r.body["reported"] != false || !isList || len(cmds) != 0 {
			t.Fatalf("GET not live = %d %s, want 200, reported false, an empty list", r.code, r.raw)
		}
	})
}

// Only Claude Code driven by stream-json frames has its catalog read here; a
// provider with an owned protocol driver answers reported=false until its reader exists.
func TestClaudeFrameRun(t *testing.T) {
	frames := model.Record{colTransport: string(TransportStreamJSON)}
	for name, tc := range map[string]struct {
		lr   *liveRun
		rec  model.Record
		want bool
	}{
		"no profile driver recorded is a Claude run": {&liveRun{}, frames, true},
		"claude profile":          {&liveRun{}, model.Record{colTransport: string(TransportStreamJSON), colRunProfileDriver: providerDriverClaude}, true},
		"codex profile":           {&liveRun{}, model.Record{colTransport: string(TransportStreamJSON), colRunProfileDriver: providerDriverCodex}, false},
		"an owned driver session": {&liveRun{session: &codexSession{}}, frames, false},
		"another transport":       {&liveRun{}, model.Record{colTransport: string(TransportRemoteControl)}, false},
	} {
		if got := claudeFrameRun(tc.lr, tc.rec); got != tc.want {
			t.Errorf("%s: claudeFrameRun = %v, want %v", name, got, tc.want)
		}
	}
}
