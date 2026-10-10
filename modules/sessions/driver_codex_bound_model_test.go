// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCodexBoundStartCarriesSelectedOrNativeProfileModel(t *testing.T) {
	for _, kind := range []string{ProviderKindOpenAI, ProviderKindOpenAICompatible, ProviderKindOllama} {
		for _, selected := range []string{"selected-model", ""} {
			for _, configured := range []string{"profile-model", ""} {
				t.Run(kind+"/selected="+selected+"/configured="+configured, func(t *testing.T) {
					peer := newCodexPeer(t, func(cfg *DriverSessionConfig) {
						cfg.Model, cfg.WorkDir = selected, "/workspace/fixture"
						cfg.BoundProvider = BoundProvider{Kind: kind, Endpoint: "https://bound.example/v1"}
					})
					done := make(chan error, 1)
					go func() { _, err := peer.session.Handshake(t.Context()); done <- err }()
					id, _, _ := peer.nextRequest()
					peer.reply(id, map[string]any{})
					id, _, _ = peer.nextRequest()
					peer.reply(id, map[string]any{"account": nil, "requiresOpenaiAuth": false})
					if selected == "" {
						id, method, params := peer.nextRequest()
						if method != codexMethodConfigRead || params["includeLayers"] != false || params["cwd"] != "/workspace/fixture" {
							t.Fatalf("native profile model read = %s %v", method, params)
						}
						peer.reply(id, map[string]any{"config": map[string]any{"model": configured}})
					}
					want := selected
					if want == "" {
						want = configured
					}
					if want == "" {
						var refused *runErr
						if err := <-done; !errors.As(err, &refused) || refused.status != http.StatusConflict {
							t.Fatalf("missing bound model was not refused before thread/start: %v", err)
						}
						select {
						case frame := <-peer.frames:
							t.Fatalf("missing model emitted a later frame: %v", frame)
						default:
						}
						return
					}
					id, method, params := peer.nextRequest()
					if method != codexMethodThreadStart || params["model"] != want || params["modelProvider"] != codexBoundProviderID(BoundProvider{Kind: kind}) {
						t.Fatalf("bound start did not carry selected/default model: %s %v", method, params)
					}
					peer.reply(id, map[string]any{"thread": map[string]any{"id": "thread-fixture"}, "modelProvider": codexBoundProviderID(BoundProvider{Kind: kind})})
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestCodexBoundResumeKeepsTheStoredModelWithoutASelection(t *testing.T) {
	peer := newCodexPeer(t, func(cfg *DriverSessionConfig) {
		cfg.ResumeConversationID = "stored-thread"
		cfg.BoundProvider = BoundProvider{Kind: ProviderKindOpenAI, Endpoint: "https://bound.example/v1"}
	})
	done := make(chan error, 1)
	go func() { _, err := peer.session.Handshake(t.Context()); done <- err }()
	id, _, _ := peer.nextRequest()
	peer.reply(id, map[string]any{})
	id, _, _ = peer.nextRequest()
	peer.reply(id, map[string]any{"account": nil, "requiresOpenaiAuth": false})
	id, method, params := peer.nextRequest()
	if method != codexMethodThreadResume || params["model"] != nil || params["modelProvider"] != "olivares_record" {
		t.Fatalf("resume replaced the stored conversation's model: %s %v", method, params)
	}
	peer.reply(id, map[string]any{"thread": map[string]any{"id": "stored-thread"}, "modelProvider": "olivares_record"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCodexProfileModelReplyWithholdsSavedValuesAndNativeErrors(t *testing.T) {
	session := codexDriver{}.OpenSession(DriverSessionConfig{Send: func(context.Context, []byte) error { return nil }}).(*codexSession)
	if err := session.send(t.Context(), []byte(`{"id":3,"method":"config/read","params":{"includeLayers":false}}`)); err != nil {
		t.Fatal(err)
	}
	for _, reply := range []string{
		`{"id":3,"result":{"config":{"model_providers":{"saved":{"experimental_bearer_token":"synthetic-saved-secret"`,
		`{"id":3,"result":{"config":{"model":"profile-model","model_providers":{"saved":{"experimental_bearer_token":"synthetic-saved-secret"}}},"origins":{"secret":"synthetic-saved-secret"}}}`,
		`{"id":3,"error":{"code":-32603,"message":"synthetic-saved-secret","data":"synthetic-saved-secret"}}`,
		`{"id":3,"error":{"code":"malformed","message":{"secret":"synthetic-saved-secret"}}}`,
		`{"id":3,"result":{"config":{"model":{"secret":"synthetic-saved-secret"}}}}`,
	} {
		projected := session.projectProfileModelResponse([]byte(reply))
		if !json.Valid(projected) || strings.Contains(string(projected), "synthetic-saved-secret") {
			t.Fatal("config/read response exposed a saved value or broke the protocol envelope")
		}
		if strings.Contains(reply, `"profile-model"`) && string(projected) != `{"id":3,"result":{"config":{"model":"profile-model"}}}` {
			t.Fatal("model-only projection lost the model or retained other config fields")
		}
	}
	for _, unrelated := range []string{`{"id":4,"result":{"value":"keep"}}`, `{"id":3,"method":"server/request","params":{"value":"keep"}}`} {
		if string(session.projectProfileModelResponse([]byte(unrelated))) != unrelated {
			t.Fatal("config model projection changed an unrelated protocol frame")
		}
	}
}

// A key-bound Codex names its provider "olivares_record" (the alias the engine
// configures), which no price table carries. A turn on an OpenAI record is reported
// under "openai", so it is priced at OpenAI's list price; one on a compatible or
// local server keeps the alias and is never priced as OpenAI.
func TestCodexBoundUsageNamesTheRecordsProvider(t *testing.T) {
	for kind, want := range map[string]string{
		ProviderKindOpenAI:           "openai",
		ProviderKindOpenAICompatible: "olivares_record",
		ProviderKindOllama:           "olivares_ollama",
	} {
		t.Run(kind, func(t *testing.T) {
			reports := make(chan resultUsage, 1)
			peer := newCodexPeer(t, func(cfg *DriverSessionConfig) {
				cfg.Model = "gpt-6-astra"
				cfg.BoundProvider = BoundProvider{Kind: kind, Endpoint: "https://bound.example/v1"}
				cfg.OnUsage = func(u resultUsage, _ bool) { reports <- u }
			})
			done := make(chan error, 1)
			go func() { _, err := peer.session.Handshake(t.Context()); done <- err }()
			id, _, _ := peer.nextRequest()
			peer.reply(id, map[string]any{})
			id, _, _ = peer.nextRequest()
			peer.reply(id, map[string]any{"account": nil, "requiresOpenaiAuth": false})
			id, _, _ = peer.nextRequest()
			alias := codexBoundProviderID(BoundProvider{Kind: kind})
			peer.reply(id, map[string]any{"thread": map[string]any{"id": "thread-bound"}, "model": "gpt-6-astra", "modelProvider": alias})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			peer.notify("thread/tokenUsage/updated", map[string]any{"threadId": "thread-bound", "turnId": "t1",
				"tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 2000, "cachedInputTokens": 500, "outputTokens": 12}}})
			select {
			case u := <-reports:
				if u.Provider != want {
					t.Fatalf("usage provider = %q, want %q", u.Provider, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no usage report")
			}
		})
	}
}
