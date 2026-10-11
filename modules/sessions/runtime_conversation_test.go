// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestAttachHTTPConversationHistoryRequiresFreshOptIn(t *testing.T) {
	h, _, admin, tenant := attachHTTPHarness(t)
	ref := launchHTTPRun(t, h, admin, tenant, map[string]any{"transport": "stream-json"})
	if _, err := h.m.stopRun(t.Context(), tenant, ref, "user:u1", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(t.Context(), repo, ref)
		if err != nil {
			return err
		}
		rec[colRunProfileDriver], rec[colClaudeSessionID] = providerDriverCodex, "root"
		// A history refusal is still a history event. It must not delay or
		// add events to a legacy attach, and must remain explicit for opt-in.
		rec[colRunGitRead] = "approved-binding:owner/repository"
		_, err = repo.Update(t.Context(), rec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?from=0", "?from=0&history=0", "?from=0&history=true", "?from=1&history=1", "?from=0&history=1"} {
		res := h.do(http.MethodGet, "/v1/m/sessions/runs/"+ref+"/attach"+query, admin, tenantHdr(tenant))
		wantHistory := query == "?from=0&history=1"
		if res.code != http.StatusOK || strings.Contains(res.raw, "event: history") != wantHistory {
			t.Errorf("attach %q status=%d history=%v; want history=%v", query, res.code, strings.Contains(res.raw, "event: history"), wantHistory)
		}
		if wantHistory && !strings.Contains(res.raw, "historical secret redaction cannot be proven") {
			t.Error("opted-in history lost its explicit redaction refusal")
		}
	}
}

type conversationPeer struct {
	*fakeProc
	t       *testing.T
	thread  any
	methods []string
}

func (p *conversationPeer) Send(_ context.Context, line []byte) error {
	var request struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Params struct {
			ThreadID     string `json:"threadId"`
			IncludeTurns bool   `json:"includeTurns"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &request); err != nil {
		return err
	}
	p.methods = append(p.methods, request.Method)
	var result any = map[string]any{}
	switch request.Method {
	case "initialize":
	case "initialized":
		return nil
	case "thread/read":
		if request.Params.ThreadID != "root" || !request.Params.IncludeTurns {
			p.t.Error("reader changed thread identity or omitted turns")
		}
		result = map[string]any{"thread": p.thread}
	default:
		p.t.Errorf("conversation reader sent mutating or authentication method %q", request.Method)
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false) // Native JSON does not use Go's HTML expansion.
	if err := encoder.Encode(map[string]any{"id": request.ID, "result": result}); err != nil {
		return err
	}
	p.out <- OutputFrame{Stream: streamStdout, Data: data.Bytes()}
	return nil
}

func TestCodexConversationReaderRefusesUnprovenOrPartialHistory(t *testing.T) {
	for _, test := range []struct {
		name    string
		thread  map[string]any
		wantErr bool
	}{
		{"root", map[string]any{"id": "root", "turns": []any{}, "path": "private-thread-path", "provider": "private-provider"}, false},
		{"wrong thread", map[string]any{"id": "foreign", "turns": []any{}}, true},
		{"subagent", map[string]any{"id": "root", "parentThreadId": "parent", "turns": []any{}}, true},
		{"agent role", map[string]any{"id": "root", "agentRole": "worker", "turns": []any{}}, true},
		{"no turns", map[string]any{"id": "root"}, true},
		{"summary only", map[string]any{"id": "root", "turns": []any{map[string]any{"id": "turn", "items": []any{}, "itemsView": "summary"}}}, true},
		{"oversize", map[string]any{"id": "root", "turns": []any{}, "extra": strings.Repeat("x", maxOutputLine)}, true},
		{"HTML expansion", map[string]any{"id": "root", "turns": []any{map[string]any{"id": "turn", "items": []any{map[string]any{"type": "agentMessage", "text": strings.Repeat("<", maxOutputLine/5)}}}}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			peer := &conversationPeer{fakeProc: &fakeProc{out: make(chan OutputFrame, 4), stopped: make(chan struct{})}, t: t, thread: test.thread}
			line, err := readCodexConversation(t.Context(), peer, "root")
			if (err != nil) != test.wantErr {
				t.Fatalf("reader err=%v; want error=%v", err, test.wantErr)
			}
			if strings.Contains(line, "private-") {
				t.Fatal("reader disclosed thread metadata")
			}
			if !reflect.DeepEqual(peer.methods, []string{"initialize", "initialized", "thread/read"}) {
				t.Fatalf("methods=%v", peer.methods)
			}
		})
	}
}

func TestConversationHistoryDoesNotReadSecretsOrUnprovenHomes(t *testing.T) {
	runner := &fakeRunner{}
	m := New(WithRunner(runner), WithProviderDriver(NewCodexDriver()))
	for _, rec := range []model.Record{
		{colRunSecretEnv: `[{"env":"SECRET","secret":"env/old"}]`},
		{colRunSecretEnv: `invalid`},
		{colRunGitRead: "approved-binding:owner/repository"},
	} {
		rec[colRunProfileDriver], rec[colClaudeSessionID] = providerDriverCodex, "root"
		line, err := m.conversationHistory(t.Context(), model.TenantID(model.NewID()), rec)
		if err == nil || line != "" || !strings.Contains(err.Error(), "redaction") {
			t.Fatalf("line=%q err=%v", line, err)
		}
	}
	line, err := m.conversationHistory(t.Context(), model.TenantID(model.NewID()), model.Record{
		colRunProfileDriver: providerDriverCodex, colClaudeSessionID: "root",
	})
	if err == nil || line != "" {
		t.Fatalf("unproven profile read: line=%q err=%v", line, err)
	}
	if len(runner.procs) != 0 {
		t.Fatal("reader spawned before secret/home proof")
	}
}
