// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/residency"
	"github.com/olivaresai/olivares/core/store"
)

func TestHealthSummaryAuditSpoolSilentWhenUnconfigured(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()

	r := h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /v1/console/health-summary = %d %s", r.code, r.raw)
	}
	if _, present := r.body["audit_spool"]; present {
		t.Fatalf("unconfigured health summary exposed audit_spool: %s", r.raw)
	}
}

func TestHealthSummaryAuditSpoolThroughResidencyGuard(t *testing.T) {
	const maxBytes = int64(10 << 30)
	h := newHarnessOpts(t, func(o *api.Options) {
		st, err := sqlstore.Open(context.Background(), store.Config{
			Engine:             store.EngineSQLite,
			DSN:                filepath.Join(t.TempDir(), "health-spool.db"),
			AuditSpoolMaxBytes: maxBytes,
			AuditSpoolOnFull:   store.AuditSpoolDegrade,
		}, nil)
		if err != nil {
			t.Fatalf("open configured audit spool store: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if err := st.System(context.Background(), func(sys store.SystemScope) error {
			_, err := sys.EnsureSystemTenant(context.Background())
			return err
		}); err != nil {
			t.Fatalf("ensure system tenant: %v", err)
		}

		reg, err := residency.NewRegistry("eu", []string{"eu"})
		if err != nil {
			t.Fatalf("build residency registry: %v", err)
		}
		guarded := residency.Guard(st, reg, nil)
		o.Store = guarded
		o.Authenticator = auth.NewAuthenticator(guarded, nil)
		o.Residency = reg
	})
	admin := h.adminLogin()

	r := h.do("GET", "/v1/console/health-summary", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("GET /v1/console/health-summary = %d %s", r.code, r.raw)
	}
	spool, ok := r.body["audit_spool"].(map[string]any)
	if !ok {
		t.Fatalf("configured health summary has no audit_spool object: %s", r.raw)
	}
	if spool["mode"] != string(store.AuditSpoolDegrade) || spool["max_bytes"] != float64(maxBytes) {
		t.Fatalf("audit_spool = %v, want mode %q max_bytes %d", spool, store.AuditSpoolDegrade, maxBytes)
	}
}
