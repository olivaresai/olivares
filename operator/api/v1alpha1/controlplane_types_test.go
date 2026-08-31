// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package v1alpha1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSpecValidate exhaustively pins the structural-validation contract the
// controller and the admission CEL rules share. The sqlite+replicas>1 case is
// deliberately VALID here (it is a safe clamp, not an impossible spec).
func TestSpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    ControlPlaneSpec
		wantErr bool
	}{
		{
			name: "sqlite single replica",
			spec: ControlPlaneSpec{Image: "img", Engine: EngineSQLite, Replicas: 1},
		},
		{
			name: "sqlite replicas>1 is valid (clamped, not rejected)",
			spec: ControlPlaneSpec{Image: "img", Engine: EngineSQLite, Replicas: 5},
		},
		{
			name: "empty engine defaults to sqlite and is valid",
			spec: ControlPlaneSpec{Image: "img", Replicas: 3},
		},
		{
			name:    "postgres without postgres block",
			spec:    ControlPlaneSpec{Image: "img", Engine: EnginePostgres, Replicas: 1},
			wantErr: true,
		},
		{
			name:    "postgres with empty dsnSecret",
			spec:    ControlPlaneSpec{Image: "img", Engine: EnginePostgres, Replicas: 1, Postgres: &PostgresSpec{DSNSecret: "  "}},
			wantErr: true,
		},
		{
			name: "postgres single replica with DSN is valid (no audit key needed)",
			spec: ControlPlaneSpec{Image: "img", Engine: EnginePostgres, Replicas: 1, Postgres: &PostgresSpec{DSNSecret: "pg"}},
		},
		{
			name:    "postgres HA without audit key",
			spec:    ControlPlaneSpec{Image: "img", Engine: EnginePostgres, Replicas: 3, Postgres: &PostgresSpec{DSNSecret: "pg"}},
			wantErr: true,
		},
		{
			name: "postgres HA with DSN + audit key is valid",
			spec: ControlPlaneSpec{Image: "img", Engine: EnginePostgres, Replicas: 3, Postgres: &PostgresSpec{DSNSecret: "pg"}, AuditSigningKeySecret: "audit"},
		},
		{
			// pg_dump keeps row_security=off and aborts as the NOBYPASSRLS app
			// role under FORCE RLS: a backup spec without the admin DSN is refused
			// rather than materialized into a CronJob whose every run fails.
			name:    "postgres backup without adminDsnKey",
			spec:    ControlPlaneSpec{Image: "img", Engine: EnginePostgres, Replicas: 1, Postgres: &PostgresSpec{DSNSecret: "pg"}, Backup: &BackupSpec{Schedule: "0 3 * * *", KEKSecret: "kek"}},
			wantErr: true,
		},
		{
			name: "postgres backup with adminDsnKey is valid",
			spec: ControlPlaneSpec{Image: "img", Engine: EnginePostgres, Replicas: 1, Postgres: &PostgresSpec{DSNSecret: "pg", AdminDSNKey: "admin"}, Backup: &BackupSpec{Schedule: "0 3 * * *", KEKSecret: "kek"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

// TestCRDShipsCELRules asserts the shipped CRD carries the admission CEL rules —
// the apiserver-native, certless equivalent of the chart's render-time `fail`
// guards. (Evaluation itself is enforced by a real apiserver/CEL; here we pin
// that the rules ship so a regeneration never silently drops them.)
func TestCRDShipsCELRules(t *testing.T) {
	path := filepath.Join("..", "..", "config", "crd", "ops.olivares.ai_controlplanes.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	crd := string(b)
	if !strings.Contains(crd, "x-kubernetes-validations:") {
		t.Fatal("CRD is missing x-kubernetes-validations (admission CEL rules)")
	}
	// Collapse YAML line-folding (long rules wrap across lines) before matching.
	normalized := strings.Join(strings.Fields(crd), " ")
	for _, want := range []string{
		"has(self.postgres) && size(self.postgres.dsnSecret) > 0",
		"has(self.auditSigningKeySecret) && size(self.auditSigningKeySecret) > 0",
		"has(self.postgres.adminDsnKey) && size(self.postgres.adminDsnKey) > 0",
	} {
		if !strings.Contains(normalized, want) {
			t.Errorf("CRD CEL rules missing expected expression %q", want)
		}
	}
}
