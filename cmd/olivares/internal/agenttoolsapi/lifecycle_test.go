// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleRoutesKeepHostAuthorityAndDenyReadOnly(t *testing.T) {
	m, err := New(context.Background(), toolinstall.NewEngine(toolinstall.NewCatalog(), toolinstall.EngineOptions{}), filepath.Join(t.TempDir(), "tools"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	reg := &registrar{}
	m.APIRoutes(reg)
	for _, target := range []string{"DELETE /releases", "GET /pins", "PUT /pins"} {
		var handler api.ModuleHandler
		for _, rt := range reg.routes {
			if rt.method+" "+rt.pattern == target {
				handler = rt.handler
			}
		}
		if handler == nil {
			t.Fatalf("missing lifecycle route %s", target)
		}
		method := strings.Split(target, " ")[0]
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(method, "/", strings.NewReader(`{}`)), api.ModuleContext{})
		if w.Code != 403 {
			t.Fatalf("unprivileged %s = %d", target, w.Code)
		}
		if method != "GET" {
			w = httptest.NewRecorder()
			ctx := auth.WithStepUpSource(context.Background(), func(context.Context) (string, error) { return auth.StepUpNone, nil })
			handler(w, httptest.NewRequest(method, "/", strings.NewReader(`{}`)).WithContext(ctx), api.ModuleContext{Principal: auth.Principal{Kind: auth.KindUser, Superadmin: true, AAL: auth.AAL1}})
			if w.Code != 409 || !strings.Contains(w.Body.String(), "read_only") {
				t.Fatalf("read-only %s = %d %s", target, w.Code, w.Body.String())
			}
		}
	}
}
