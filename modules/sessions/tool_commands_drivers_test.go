// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type commandCatalogReader interface {
	commandCatalog(context.Context) ([]toolCommand, bool, error)
}

func TestToolCommands_CodexSkillsWire(t *testing.T) {
	p := newCodexPeer(t, func(c *DriverSessionConfig) { c.WorkDir = "/project" })
	reader, ok := p.session.(commandCatalogReader)
	if !ok {
		t.Fatal("Codex does not read its skills/list catalog")
	}
	done := make(chan toolCommandsDTO, 1)
	go func() {
		items, reported, err := reader.commandCatalog(context.Background())
		if err != nil {
			t.Error(err)
		}
		done <- toolCommandsDTO{Reported: reported, Commands: items}
	}()
	id, method, params := p.nextRequest()
	if method != "skills/list" || params["forceReload"] != true {
		t.Fatalf("request = %s %#v", method, params)
	}
	if cwds, _ := params["cwds"].([]any); len(cwds) != 1 || cwds[0] != "/project" {
		t.Fatalf("skill discovery escaped the session cwd: %#v", params)
	}
	p.reply(id, map[string]any{"data": []any{map[string]any{"cwd": "/project", "errors": []any{}, "skills": []any{
		map[string]any{"name": "project-skill", "description": "Project\nreview", "enabled": true, "scope": "repo", "path": "/project/.agents/skills/project-skill/SKILL.md"},
		map[string]any{"name": "disabled", "description": "Hidden", "enabled": false, "scope": "user"},
		map[string]any{"name": "bad name", "description": "Invalid", "enabled": true, "scope": "user"},
	}}}})
	select {
	case got := <-done:
		if !got.Reported || len(got.Commands) != 1 {
			t.Fatalf("catalog = %+v", got)
		}
		c := commandByName(t, got, "project-skill")
		if c.Source != "project" || c.Description != "Project review" {
			t.Fatalf("skill = %+v", c)
		}
		raw, _ := json.Marshal(c)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		if fields["insert_text"] != "$project-skill" {
			t.Fatalf("native invocation missing: %s", raw)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("catalog did not arrive")
	}
}

func TestToolCommands_ACPPushAndRoute(t *testing.T) {
	for _, driver := range []string{providerDriverGemini, providerDriverOpenCode} {
		t.Run(driver, func(t *testing.T) {
			f := newPreviewFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
			lr, _ := f.m.rt.getLive(f.tenant, f.runRef)
			var sess DriverSession
			if driver == providerDriverGemini {
				sess = geminiDriver{}.OpenSession(DriverSessionConfig{})
			} else {
				sess = openCodeDriver{}.OpenSession(DriverSessionConfig{})
			}
			lr.session = sess
			if err := mutateRunForWorkTest(f.m, f.tenant, f.runRef, func(r model.Record) { r[colRunProfileDriver] = driver }); err != nil {
				t.Fatal(err)
			}
			var acp *acpSession
			switch s := sess.(type) {
			case *geminiSession:
				acp = &s.acpSession
			case *openCodeSession:
				acp = &s.acpSession
			}
			acp.sessionID = "ours"
			push := func(id, entries string) {
				sess.Deliver(OutputFrame{Stream: streamStdout, Data: []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"` + id + `","update":{"sessionUpdate":"available_commands_update","availableCommands":` + entries + `}}}`)})
			}
			admin := f.adminToken(t)
			read := func() toolCommandsDTO {
				r := f.do("GET", "/v1/m/sessions/runs/"+f.runRef+"/commands", admin, tenantHdr(f.tenant))
				if r.code != http.StatusOK {
					t.Fatalf("catalog route: %d %s", r.code, r.raw)
				}
				var got toolCommandsDTO
				if err := json.Unmarshal([]byte(r.raw), &got); err != nil {
					t.Fatal(err)
				}
				return got
			}
			if got := read(); got.Reported || len(got.Commands) != 0 {
				t.Fatalf("unreported = %+v", got)
			}
			push("ours", `[{"name":"first","description":"First command","input":{"hint":"target"}}]`)
			got := read()
			if !got.Reported || got.Tool != driver || len(got.Commands) != 1 {
				t.Fatalf("catalog = %+v", got)
			}
			if c := commandByName(t, got, "first"); c.ArgumentHint != "target" || c.Source != "" {
				t.Fatalf("command = %+v", c)
			}
			push("foreign", `[{"name":"foreign","description":"Not ours"}]`)
			if got := read(); len(got.Commands) != 1 || got.Commands[0].Name != "first" {
				t.Fatalf("foreign catalog accepted: %+v", got)
			}
			push("ours", `[{"name":"added","description":"New from the tool"}]`)
			if got := read(); len(got.Commands) != 1 || got.Commands[0].Name != "added" {
				t.Fatalf("push not refreshed: %+v", got)
			}
			push("ours", `[]`)
			if got := read(); !got.Reported || len(got.Commands) != 0 {
				t.Fatalf("empty catalog = %+v", got)
			}
			push("ours", `[{"name":"bad\nname"}]`)
			if _, err := f.m.runToolCommands(context.Background(), f.tenant, f.runRef); statusOf(err) != http.StatusBadGateway {
				t.Fatalf("invalid catalog error = %v", err)
			}
		})
	}
}

func TestToolCommands_CodexRouteCacheRefreshAndFailure(t *testing.T) {
	f := newPreviewFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"})
	lr, _ := f.m.rt.getLive(f.tenant, f.runRef)
	f.ownLaunch(t, lr)
	p := newCodexPeer(t, func(c *DriverSessionConfig) {
		c.WorkDir = "/project"
		c.OnCommandsChanged = func() { lr.commands.dirty.Store(true) }
	})
	lr.session = p.session
	if err := mutateRunForWorkTest(f.m, f.tenant, f.runRef, func(r model.Record) { r[colRunProfileDriver] = providerDriverCodex }); err != nil {
		t.Fatal(err)
	}
	type result struct {
		got toolCommandsDTO
		err error
	}
	read := func() <-chan result {
		done := make(chan result, 1)
		go func() { got, err := f.m.runToolCommands(t.Context(), f.tenant, f.runRef); done <- result{got, err} }()
		return done
	}
	answer := func(id int64, name string) {
		raw, _ := json.Marshal(map[string]any{"id": id, "result": map[string]any{"data": []any{map[string]any{"cwd": "/project", "errors": []any{}, "skills": []any{map[string]any{"name": name, "description": "Native skill", "enabled": true, "scope": "repo", "path": "/private/path"}}}}}})
		if !p.session.(*codexSession).takeCatalogResponse(raw) {
			t.Fatal("catalog response was not withheld from attach and recording")
		}
	}
	done := read()
	id, _, _ := p.nextRequest()
	answer(id, "first")
	first := <-done
	if first.err != nil || !first.got.Reported || first.got.Tool != providerDriverCodex {
		t.Fatalf("first = %+v", first)
	}
	if got := <-read(); got.err != nil || got.got.Commands[0].Name != "first" {
		t.Fatalf("cached = %+v", got)
	}
	select {
	case f := <-p.frames:
		t.Fatalf("cache emitted request: %v", f)
	default:
	}
	p.notify("skills/changed", map[string]any{})
	done = read()
	id, _, _ = p.nextRequest()
	answer(id, "added")
	got := <-done
	if got.err != nil || got.got.Commands[0].Name != "added" {
		t.Fatalf("changed = %+v", got)
	}
	p.notify("skills/changed", map[string]any{})
	done = read()
	id, _, _ = p.nextRequest()
	p.replyError(id, -32603, "native error includes private configuration")
	got = <-done
	if got.err != nil || !got.got.Stale || got.got.Commands[0].Name != "added" {
		t.Fatalf("stale = %+v", got)
	}
	lr.stopRequested = true
	if got := <-read(); got.err != nil || got.got.Reported || len(got.got.Commands) != 0 {
		t.Fatalf("ended = %+v", got)
	}
}

func TestToolCommands_ACPBeforeBindingAndMalformedShape(t *testing.T) {
	s := newACPSession(DriverSessionConfig{}, providerDriverGemini)
	push := func(entries string) {
		s.onNotification(acpNotifySessionUpdate, json.RawMessage(`{"sessionId":"ours","update":{"sessionUpdate":"available_commands_update","availableCommands":`+entries+`}}`))
	}
	push(`[{"name":"early","description":"Before new returns"},{"name":"memory show","description":"Native subcommand"}]`)
	if _, reported, _ := s.commandCatalog(t.Context()); reported {
		t.Fatal("an unbound conversation disclosed a catalog")
	}
	s.sessionID = "ours"
	items, reported, err := s.commandCatalog(t.Context())
	if err != nil || !reported || len(items) != 2 || items[0].Name != "early" || items[1].Name != "memory show" {
		t.Fatalf("early catalog = %+v %v %v", items, reported, err)
	}
	push(`"wrong shape"`)
	if _, _, err := s.commandCatalog(t.Context()); statusOf(err) != http.StatusBadGateway {
		t.Fatalf("malformed update silently kept the previous catalog: %v", err)
	}
}

func TestToolCommands_CodexCatalogWriteSurvivesCallerCancellation(t *testing.T) {
	for _, callerDeadline := range []bool{false, true} {
		t.Run(fmt.Sprint("deadline=", callerDeadline), func(t *testing.T) {
			entered := make(chan context.Context, 1)
			release := make(chan struct{})
			p := newCodexPeer(t, func(c *DriverSessionConfig) {
				c.Send = func(ctx context.Context, _ []byte) error { entered <- ctx; <-release; return ctx.Err() }
			})
			ctx, cancel := context.WithCancel(t.Context())
			if callerDeadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, _, err := p.session.(commandCatalogReader).commandCatalog(ctx); done <- err }()
			writeCtx := <-entered
			if !callerDeadline {
				cancel()
			}
			<-ctx.Done()
			err := writeCtx.Err()
			deadline, bounded := writeCtx.Deadline()
			close(release)
			<-done
			if err != nil {
				t.Fatalf("caller cancellation/deadline interrupted the catalog's stdin write: %v", err)
			}
			remaining := time.Until(deadline)
			if !bounded || remaining > toolCommandsWait || remaining < toolCommandsWait/2 {
				t.Fatalf("catalog write inherited the caller's deadline or has none: %v remaining=%v", bounded, remaining)
			}
		})
	}
}
