// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
)

// handlerRegistrar keeps the handler of each route a module mounts, by "METHOD pattern".
type handlerRegistrar struct{ handlers map[string]api.ModuleHandler }

func (r *handlerRegistrar) Handle(method, pattern string, _ auth.Permission, h api.ModuleHandler) {
	r.handlers[method+" "+pattern] = h
}

func (r *handlerRegistrar) HandleEntity(method, pattern string, p auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	r.Handle(method, pattern, p, h)
}

// failingData is a data handle whose every read fails, so a handler that reaches it answers a
// store error and one that was replaced by the edition refusal answers 501 business_required.
type failingData struct{ api.ScopedData }

func (failingData) View(context.Context, func(store.Scope) error) error {
	return errors.New("no store in this test")
}

// A hold a Business engine stored keeps vetoing a knowledge delete when the engine is composed
// as Community: the hold gate is wired in every edition and reads the store, not the refused
// routes. The hold is written straight into the store, because Community cannot place one.
func TestStoredHoldBlocksKnowledgeKBDeleteInCommunity(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name = "community"
	h := newHarness(t)

	var kb struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/knowledge/kbs", h.adminToken, h.tenantA,
		map[string]any{"name": "legal-kb"}, &kb); code != http.StatusCreated || kb.ID == "" {
		t.Fatalf("create kb = %d (id=%q)", code, kb.ID)
	}
	if code, raw := h.req("POST", "/v1/m/compliance/holds", h.adminToken, h.tenantA,
		map[string]any{"matter_ref": "M-1", "reason": "r", "scope_kind": "tenant"}); code != http.StatusNotImplemented {
		t.Fatalf("Community placed a hold: %d %s", code, raw)
	}
	err := h.st.Mutate(context.Background(), tenantAID(t, h), func(sc store.Scope) error {
		repo, err := sc.Ext("compliance.legal_hold")
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"matter_ref": "M-2026-7", "scope_kind": "tenant", "subject_kind": nil, "subject_ref": nil,
			"reason": "stored by a Business engine", "status": "active", "created_by": "test",
		})
		return err
	})
	if err != nil {
		t.Fatalf("store the hold: %v", err)
	}

	code, raw := h.req("DELETE", "/v1/m/knowledge/kbs/"+kb.ID, h.adminToken, h.tenantA, nil)
	if code != http.StatusLocked || !strings.Contains(string(raw), "legal_hold") {
		t.Fatalf("kb delete under a stored hold in Community = %d %s; want 423 legal_hold", code, raw)
	}
}

// Only Community composes legal holds and erasure off. A Business build that lost the option
// would answer 501 to every hold without a word, so the production module set is built under
// each edition and the hold list route is called.
func TestBuildModulesComposesLegalHoldsByEdition(t *testing.T) {
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	for _, tc := range []struct {
		edition string
		refused bool
	}{
		{"community", true},
		{"enterprise", false},
	} {
		t.Run(tc.edition, func(t *testing.T) {
			thisEdition.name = tc.edition
			dir := t.TempDir()
			priv, _, err := secure.LoadOrCreateSigningKey(filepath.Join(dir, "audit-signing.key"))
			if err != nil {
				t.Fatal(err)
			}
			signer, err := audit.NewSigner(priv)
			if err != nil {
				t.Fatal(err)
			}
			log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
			set, err := buildModules(nil, signer, nil, nil, nil, nil, nil, sourcesConfig{}, EditionConfig{DataDir: dir}, dir, log)
			if err != nil {
				t.Fatalf("build modules: %v", err)
			}
			routes := &handlerRegistrar{handlers: map[string]api.ModuleHandler{}}
			set.compliance.APIRoutes(routes)
			for _, route := range []string{"GET /holds", "GET /erasure", "GET /claude-files"} {
				handler := routes.handlers[route]
				if handler == nil {
					t.Fatalf("%s is not mounted", route)
				}
				rec := httptest.NewRecorder()
				handler(rec, httptest.NewRequest(http.MethodGet, "/", nil), api.ModuleContext{Data: failingData{}})
				refused := rec.Code == http.StatusNotImplemented && strings.Contains(rec.Body.String(), "business_required")
				if refused != tc.refused {
					t.Fatalf("%s %s = %d %s; want business_required refusal=%v", tc.edition, route, rec.Code, rec.Body.String(), tc.refused)
				}
			}
		})
	}
}
