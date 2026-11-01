// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package finops

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// Community appends no column to the FOCUS export: the edition seam that lets
// Business add x_ columns must leave this file exactly as it was.
func TestFocusExportAddsNoColumnWithoutAnEdition(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 1500, baseTime))
	m.ingest(t, tenant, mkCost("openai", "gpt-5", "", 20, 8, 2500, baseTime))

	rec := httptest.NewRecorder()
	m.handleExport(rec, httptest.NewRequest(http.MethodGet, "/spend/export", nil), api.ModuleContext{Data: tsScopedData{st: st, tenant: tenant}})
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d %s", rec.Code, rec.Body.String())
	}
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v\n%s", err, rec.Body.String())
	}
	if len(rows) != 3 {
		t.Fatalf("got %d records, want a header and two samples:\n%s", len(rows), rec.Body.String())
	}
	if strings.Join(rows[0], ",") != strings.Join(focusColumns, ",") {
		t.Errorf("header = %v, want exactly the FOCUS columns", rows[0])
	}
	for i, row := range rows[1:] {
		if len(row) != len(focusColumns) {
			t.Errorf("row %d has %d cells, want %d", i, len(row), len(focusColumns))
		}
	}
}
