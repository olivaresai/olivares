// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package opgate

import "testing"

func TestSQLiteDSNWithPragmas(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		pragmas []string
		want    string
	}{
		{
			name:    "plain path",
			dsn:     "/var/lib/olivares/olivares.db",
			pragmas: []string{SQLiteTempStoreMemory},
			want:    "/var/lib/olivares/olivares.db?_pragma=temp_store%282%29",
		},
		{
			name:    "existing query is extended",
			dsn:     "file:/var/lib/olivares/olivares.db?mode=ro",
			pragmas: []string{"busy_timeout(5000)", SQLiteTempStoreMemory},
			want:    "file:/var/lib/olivares/olivares.db?mode=ro&_pragma=busy_timeout%285000%29&_pragma=temp_store%282%29",
		},
		{
			name: "no pragmas leaves the dsn alone",
			dsn:  "/var/lib/olivares/olivares.db",
			want: "/var/lib/olivares/olivares.db",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SQLiteDSNWithPragmas(tt.dsn, tt.pragmas...); got != tt.want {
				t.Fatalf("SQLiteDSNWithPragmas(%q, %v) = %q, want %q", tt.dsn, tt.pragmas, got, tt.want)
			}
		})
	}
}
