// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package redteam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestListResultsDrainsAllStorePages(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "results-pages")
	runID := model.NewID()
	const occurredAt = "2026-08-23T00:00:00.000000000Z"

	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(resultKind)
		if err != nil {
			return err
		}
		for i := 0; i < listCap+1; i++ {
			if _, err := repo.Create(ctx, model.Record{
				colRunRef:     runID.String(),
				colProbeID:    fmt.Sprintf("probe-%04d", i),
				colFamily:     familyInjection,
				colOutcome:    string(OutcomeRefused),
				colSeverity:   string(model.SeverityLow),
				colOccurredAt: occurredAt,
			}); err != nil {
				return fmt.Errorf("create result %d: %w", i, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed results: %v", err)
	}

	r := h.do(
		http.MethodGet,
		"/v1/m/redteam/runs/"+runID.String()+"/results",
		admin,
		nil,
		tenantHdr(tenant),
	)
	if r.code != http.StatusOK {
		t.Fatalf("list results = %d %s", r.code, r.raw)
	}

	var got listResponse[resultDTO]
	if err := json.Unmarshal([]byte(r.raw), &got); err != nil {
		t.Fatalf("decode results: %v", err)
	}
	if len(got.Items) != listCap+1 {
		t.Fatalf("results = %d, want %d", len(got.Items), listCap+1)
	}
	if got.HasMore {
		t.Fatal("has_more = true, want false after the handler drains every store page")
	}
	if got.Cursor != "" {
		t.Fatalf("cursor = %q, want empty after the handler drains every store page", got.Cursor)
	}

	assertResult := func(position int, probeID string) {
		t.Helper()
		result := got.Items[position]
		if result.ID == "" {
			t.Fatalf("result %d has an empty id", position)
		}
		want := resultDTO{
			ID:         result.ID,
			RunRef:     runID.String(),
			ProbeID:    probeID,
			Family:     familyInjection,
			Outcome:    string(OutcomeRefused),
			Severity:   string(model.SeverityLow),
			OccurredAt: occurredAt,
		}
		if result != want {
			t.Fatalf("result %d = %+v, want %+v", position, result, want)
		}
	}
	assertResult(0, "probe-0000")
	assertResult(listCap, fmt.Sprintf("probe-%04d", listCap))
}
