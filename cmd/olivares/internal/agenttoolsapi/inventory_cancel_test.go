// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type inventoryCancelData struct {
	api.ScopedData
	cancel context.CancelFunc
}

func (d inventoryCancelData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	err := d.ScopedData.Mutate(ctx, fn)
	if err == nil {
		d.cancel()
	}
	return err
}

func TestInventoryCanceledReadIsAbandoned(t *testing.T) {
	m, err := New(t.Context(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	st := auditStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mc := api.ModuleContext{Data: inventoryCancelData{api.NewScopedData(st, model.SystemTenantID), cancel}}
	w := httptest.NewRecorder()
	m.inventory(w, httptest.NewRequest(http.MethodGet, "/inventory", nil).WithContext(ctx), mc)
	if w.Code != http.StatusRequestTimeout || strings.Contains(w.Body.String(), "tool_install_failed") {
		t.Fatalf("canceled read = %d %s, want request abandoned", w.Code, w.Body.String())
	}
}

func TestInventoryCompletedReadRetainsInstallerStatus(t *testing.T) {
	m, err := New(t.Context(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	m.root = "relative-root"
	st := auditStore(t)
	w := httptest.NewRecorder()
	m.inventory(w, httptest.NewRequest(http.MethodGet, "/inventory", nil), api.ModuleContext{Data: api.NewScopedData(st, model.SystemTenantID)})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("completed invalid read = %d %s", w.Code, w.Body.String())
	}
}
