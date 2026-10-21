// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSessionIdleSettingsPersistAndDefaultOff(t *testing.T) {
	_, settings := bootForSettings(t)
	initial, err := settings.SessionIdleSettings(t.Context())
	if err != nil || initial.IdleStopMinutes != 0 {
		t.Fatalf("fresh=%+v err=%v", initial, err)
	}
	if err := settings.writeModules(t.Context(), operator(t), "test.modules", []string{"eventing"}); err != nil {
		t.Fatal(err)
	}
	for _, minutes := range []int{-1, 1, 5, 10081} {
		if _, err := settings.SaveSessionIdleSettings(t.Context(), operator(t), api.SessionIdleSettings{IdleStopMinutes: minutes}); err == nil {
			t.Fatalf("accepted %d", minutes)
		}
	}
	choice := api.SessionIdleSettings{IdleStopMinutes: 6}
	if _, err := settings.SaveSessionIdleSettings(t.Context(), operator(t), choice); err != nil {
		t.Fatal(err)
	}
	again := newProductSettings(settings.st, settings.dataDir)
	got, err := again.SessionIdleSettings(t.Context())
	if err != nil || got != choice {
		t.Fatalf("restarted=%+v err=%v", got, err)
	}
	doc, _, err := settings.load(t.Context())
	if err != nil || doc.Modules == nil || len(doc.Modules.Selected) != 1 || doc.Modules.Selected[0] != "eventing" {
		t.Fatal("idle setting replaced existing deployment state")
	}
	if _, err := settings.SaveSessionIdleSettings(t.Context(), operator(t), api.SessionIdleSettings{}); err != nil {
		t.Fatal(err)
	}
	off, err := again.SessionIdleSettings(t.Context())
	if err != nil || off.IdleStopMinutes != 0 {
		t.Fatal("could not turn policy off without restart")
	}
}

type idleSettingsAuditFailureStore struct {
	store.Store
	drop bool
}

func (s idleSettingsAuditFailureStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthMutate(ctx, func(sc store.AuthScope) error { return fn(idleSettingsAuditFailureScope{AuthScope: sc, drop: s.drop}) })
}

type idleSettingsAuditFailureScope struct {
	store.AuthScope
	drop bool
}

func (s idleSettingsAuditFailureScope) Audit() store.AuditLog {
	return idleSettingsAuditFailureLog{AuditLog: s.AuthScope.Audit(), drop: s.drop}
}

type idleSettingsAuditFailureLog struct {
	store.AuditLog
	drop bool
}

func (l idleSettingsAuditFailureLog) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	if l.drop {
		return model.AuditEvent{}, nil
	}
	return model.AuditEvent{}, store.ErrAuditSpoolFull
}

func TestSessionIdleSettingsFailedAuditKeepsThePreviousChoice(t *testing.T) {
	for _, drop := range []bool{false, true} {
		t.Run(fmt.Sprint(drop), func(t *testing.T) {
			_, settings := bootForSettings(t)
			before := api.SessionIdleSettings{IdleStopMinutes: 6}
			if _, err := settings.SaveSessionIdleSettings(t.Context(), operator(t), before); err != nil {
				t.Fatal(err)
			}
			original := settings.st
			settings.st = idleSettingsAuditFailureStore{Store: original, drop: drop}
			_, err := settings.SaveSessionIdleSettings(t.Context(), operator(t), api.SessionIdleSettings{IdleStopMinutes: 12})
			if !errors.Is(err, api.ErrSessionIdleSettingsUnavailable) || err.Error() != api.ErrSessionIdleSettingsUnavailable.Error() {
				t.Fatalf("unclassified audit refusal: %v", err)
			}
			if !drop && !errors.Is(err, store.ErrAuditSpoolFull) {
				t.Fatal("lost audit failure classification")
			}
			settings.st = original
			after, err := settings.SessionIdleSettings(t.Context())
			if err != nil || after != before {
				t.Fatal("failed audit changed the idle policy")
			}
		})
	}
}

func TestSessionIdleSettingsWiredAPIUsesTheSavedInstanceChoice(t *testing.T) {
	eng, settings := bootForSettings(t)
	call := func(method, path string, body any, token string) (int, map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		eng.api.Handler().ServeHTTP(out, req)
		var response map[string]any
		_ = json.Unmarshal(out.Body.Bytes(), &response)
		return out.Code, response
	}
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	code, _ := call("POST", "/v1/setup", map[string]any{"token": setup, "email": "idle-admin@test.invalid", "password": "idle-test-password-1"}, "")
	if code != 201 {
		t.Fatalf("setup status=%d", code)
	}
	code, login := call("POST", "/v1/auth/login", map[string]any{"email": "idle-admin@test.invalid", "password": "idle-test-password-1"}, "")
	if code != 200 {
		t.Fatalf("login status=%d", code)
	}
	token, _ := login["token"].(string)
	code, initial := call("GET", "/v1/system/session-idle", nil, token)
	if code != 200 || initial["idle_stop_minutes"] != float64(0) {
		t.Fatalf("default settings status=%d", code)
	}
	code, _ = call("PUT", "/v1/system/session-idle", api.SessionIdleSettings{IdleStopMinutes: 6}, token)
	if code != 200 {
		t.Fatalf("save settings status=%d", code)
	}
	saved, err := settings.SessionIdleSettings(t.Context())
	if err != nil || saved.IdleStopMinutes != 6 {
		t.Fatal("wired API did not persist the instance policy")
	}
	// An empty body names no minutes: refused, and the saved choice is untouched.
	code, _ = call("PUT", "/v1/system/session-idle", map[string]any{}, token)
	if code != 400 {
		t.Fatalf("empty body status=%d, want 400", code)
	}
	if kept, err := settings.SessionIdleSettings(t.Context()); err != nil || kept.IdleStopMinutes != 6 {
		t.Fatalf("an empty body changed the saved policy: %+v err=%v", kept, err)
	}
}
