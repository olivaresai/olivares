// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"testing"
	"time"
)

// TestLockConnConfigSendsNoServerRejectedParams is the regression pin for the
// Staging finding: the lock pool used to append libpq keepalives_* to the
// DSN, but pgx does not implement those client-side — pgconn forwards unknown
// DSN parameters to the server as startup runtime parameters, and EVERY
// PostgreSQL refuses the connection with FATAL 42704 (unrecognized
// configuration parameter "keepalives_interval"), so leader election — and
// with it engine boot — failed against any Postgres reached over the pgx
// driver. Keepalives now live on the dialer; the config the lock pool opens
// with must never carry them as runtime params again, in either DSN form.
func TestLockConnConfigSendsNoServerRejectedParams(t *testing.T) {
	for _, dsn := range []string{
		"postgres://app:pw@127.0.0.1:5432/olivares?sslmode=disable",
		"host=127.0.0.1 port=5432 user=app password=pw dbname=olivares sslmode=disable",
	} {
		cfg, killer, err := lockConnConfig(dsn)
		if err != nil {
			t.Fatalf("lockConnConfig(%q): %v", dsn, err)
		}
		for _, k := range []string{"keepalives", "keepalives_idle", "keepalives_interval", "keepalives_count"} {
			if v, ok := cfg.RuntimeParams[k]; ok {
				t.Errorf("dsn %q: runtime param %s=%q would be sent to the server and rejected (SQLSTATE 42704)", dsn, k, v)
			}
		}
		if cfg.DialFunc == nil {
			t.Errorf("dsn %q: lock config must install the keepalive-tuned dialer", dsn)
		}
		if killer == nil {
			t.Errorf("dsn %q: lock config must return the session-tracking dialer (the resignation kill switch)", dsn)
		}
		// The backend itself must accept both DSN forms without dialing
		// (database/sql opens lazily). No owner DSN: DDL falls back to the
		// app DSN, the pre-split behavior.
		be, err := newPGLockBackend(dsn, "")
		if err != nil {
			t.Fatalf("newPGLockBackend(%q): %v", dsn, err)
		}
		if be.ddlDSN != dsn {
			t.Errorf("ddlDSN = %q, want fallback to the app DSN", be.ddlDSN)
		}
		if err := be.close(); err != nil {
			t.Errorf("close(%q): %v", dsn, err)
		}
	}
}

// TestLockBackendUsesOwnerDSNForDDL pins the second staging finding: under
// the hardened owner/app split the app role has no CREATE on the schema, so the
// one-time epoch-table DDL must run as the owner role. When an owner DSN is
// configured the backend must target it for DDL; the lock pool stays on the app
// DSN.
func TestLockBackendUsesOwnerDSNForDDL(t *testing.T) {
	const (
		appDSN   = "postgres://app:pw@127.0.0.1:5432/olivares?sslmode=disable"
		ownerDSN = "postgres://owner:pw@127.0.0.1:5432/olivares?sslmode=disable"
	)
	be, err := newPGLockBackend(appDSN, ownerDSN)
	if err != nil {
		t.Fatalf("newPGLockBackend: %v", err)
	}
	defer be.close() //nolint:errcheck
	if be.ddlDSN != ownerDSN {
		t.Fatalf("ddlDSN = %q, want the owner DSN for the epoch-table DDL", be.ddlDSN)
	}
}

// TestLockDialerKeepalives pins the dialer-side replacement for the old DSN
// parameters (keepalives=1, idle 5, interval 2, count 3) and that a DSN
// connect_timeout is carried onto the dialer replacing the one pgconn built.
func TestLockDialerKeepalives(t *testing.T) {
	d := lockDialer(7 * time.Second)
	if !d.KeepAliveConfig.Enable {
		t.Fatal("keepalives must be enabled on the lock dialer")
	}
	if d.KeepAliveConfig.Idle != 5*time.Second || d.KeepAliveConfig.Interval != 2*time.Second || d.KeepAliveConfig.Count != 3 {
		t.Fatalf("keepalive tuning = %+v, want idle 5s / interval 2s / count 3", d.KeepAliveConfig)
	}
	if d.Timeout != 7*time.Second {
		t.Fatalf("dialer timeout = %v, want the DSN connect_timeout (7s)", d.Timeout)
	}

	cfg, _, err := lockConnConfig("postgres://app:pw@127.0.0.1:5432/olivares?sslmode=disable&connect_timeout=7")
	if err != nil {
		t.Fatalf("lockConnConfig: %v", err)
	}
	if cfg.ConnectTimeout != 7*time.Second {
		t.Fatalf("ConnectTimeout = %v, want 7s parsed from the DSN", cfg.ConnectTimeout)
	}
}
