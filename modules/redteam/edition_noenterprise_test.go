// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package redteam

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
)

type editionRegistrar struct {
	t     *testing.T
	count int
}

func (reg *editionRegistrar) Handle(method, pattern string, _ auth.Permission, h api.ModuleHandler) {
	reg.count++
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "/v1/m/redteam"+strings.ReplaceAll(pattern, "{id}", "id"), strings.NewReader(`{}`))
	h(w, r, api.ModuleContext{})
	if w.Code != http.StatusNotImplemented {
		reg.t.Errorf("%s %s = %d; want 501", method, pattern, w.Code)
	}
}
func (reg *editionRegistrar) HandleEntity(method, pattern string, p auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	reg.Handle(method, pattern, p, h)
}
func TestCommunityRedTeamUnavailable(t *testing.T) {
	reg := &editionRegistrar{t: t}
	New().APIRoutes(reg)
	if reg.count != 9 {
		t.Errorf("registered %d routes; want all 9 published routes", reg.count)
	}
}
