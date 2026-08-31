// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestLoadAuditSpoolConfig(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantMax   int64
		wantMode  store.AuditSpoolMode
		wantLog   string
		wantNoLog bool
		wantErr   bool
	}{
		{name: "unset", env: map[string]string{}, wantMode: store.AuditSpoolBlock, wantNoLog: true},
		{name: "valid", env: map[string]string{auditSpoolMaxBytesEnv: "4096", auditSpoolOnFullEnv: "block"}, wantMax: 4096, wantMode: store.AuditSpoolBlock, wantLog: "spool budget enabled"},
		{name: "invalid number", env: map[string]string{auditSpoolMaxBytesEnv: "12x"}, wantMode: store.AuditSpoolBlock, wantErr: true},
		{name: "negative", env: map[string]string{auditSpoolMaxBytesEnv: "-7"}, wantMode: store.AuditSpoolBlock, wantErr: true},
		{name: "invalid mode", env: map[string]string{auditSpoolOnFullEnv: "discard"}, wantMode: store.AuditSpoolBlock, wantLog: "invalid OLIVARES_AUDIT_SPOOL_ON_FULL"},
		{name: "valid degrade", env: map[string]string{auditSpoolMaxBytesEnv: " 8192 ", auditSpoolOnFullEnv: " DeGrAdE "}, wantMax: 8192, wantMode: store.AuditSpoolDegrade, wantLog: "spool budget enabled"},
		{name: "unit suffix mb", env: map[string]string{auditSpoolMaxBytesEnv: "64MB"}, wantMax: 64 << 20, wantMode: store.AuditSpoolBlock, wantLog: "spool budget enabled"},
		{name: "unit suffix gb lowercase", env: map[string]string{auditSpoolMaxBytesEnv: "2gb"}, wantMax: 2 << 30, wantMode: store.AuditSpoolBlock, wantLog: "spool budget enabled"},
		{name: "unit suffix overflow", env: map[string]string{auditSpoolMaxBytesEnv: "9999999999TB"}, wantMode: store.AuditSpoolBlock, wantErr: true},
		{name: "bare suffix", env: map[string]string{auditSpoolMaxBytesEnv: "GB"}, wantMode: store.AuditSpoolBlock, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			getenv := func(key string) string { return tc.env[key] }

			maxBytes, mode, err := loadAuditSpoolConfig(getenv, log)
			if (err != nil) != tc.wantErr {
				t.Fatalf("loadAuditSpoolConfig() error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && (!strings.Contains(err.Error(), auditSpoolMaxBytesEnv) || !strings.Contains(err.Error(), "set but invalid") || !strings.Contains(err.Error(), "refusing to start")) {
				t.Fatalf("loadAuditSpoolConfig() error is not actionable: %v", err)
			}
			if maxBytes != tc.wantMax || mode != tc.wantMode {
				t.Fatalf("loadAuditSpoolConfig() = (%d, %q, %v), want (%d, %q, wantErr=%v)", maxBytes, mode, err, tc.wantMax, tc.wantMode, tc.wantErr)
			}
			if tc.wantNoLog && logs.Len() != 0 {
				t.Fatalf("unexpected log output: %s", logs.String())
			}
			if tc.wantLog != "" && !strings.Contains(logs.String(), tc.wantLog) {
				t.Fatalf("log output %q does not contain %q", logs.String(), tc.wantLog)
			}
		})
	}
}
