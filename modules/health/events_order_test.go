// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestListEventsNewestFirstPagesEveryEventOnce: with ?order=desc a limited read starts at
// the newest event, and its cursor walks every older one exactly once, ties on the same
// instant included. Without ?order the read stays oldest first.
func TestListEventsNewestFirstPagesEveryEventOnce(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	t0 := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	at := []time.Time{t0, t0.Add(time.Minute), t0.Add(time.Minute), t0.Add(time.Minute), t0.Add(2 * time.Minute)}
	var ids []string // in creation order, so ids ascend
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		for _, when := range at {
			rec, err := repo.Create(context.Background(), model.Record{
				colEvSubjectKind: "agent", colEvSubjectRef: "agent-7", colEvState: stateDown, colEvPrevState: stateHealthy,
				colEvLatency: int64(-1), colEvCause: causeReport, colEvOccurredAt: model.NewTimestamp(when).String(),
			})
			if err != nil {
				return err
			}
			ids = append(ids, rec.String(model.ColID))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newest := []string{ids[4], ids[3], ids[2], ids[1], ids[0]}

	read := func(query url.Values) resp {
		t.Helper()
		return h.do("GET", "/v1/m/health/events?"+query.Encode(), viewer, nil, tenantHdr(tenant))
	}
	var got []string
	q := url.Values{"order": {"desc"}, "limit": {"2"}}
	for page := 0; ; page++ {
		r := read(q)
		if r.code != http.StatusOK || page > len(newest) {
			t.Fatalf("page %d = %d %s", page, r.code, r.raw)
		}
		for _, it := range listItems(r) {
			got = append(got, strOf(it.(map[string]any)["id"]))
		}
		cursor, _ := r.body["cursor"].(string)
		if r.body["has_more"] != true {
			break
		}
		if cursor == "" {
			t.Fatalf("page %d has more and no cursor: %s", page, r.raw)
		}
		q.Set("cursor", cursor)
	}
	if len(got) != len(newest) {
		t.Fatalf("order=desc read %v, want %v", got, newest)
	}
	for i := range newest {
		if got[i] != newest[i] {
			t.Fatalf("order=desc read %v, want %v", got, newest)
		}
	}

	if r := read(url.Values{"limit": {"1"}}); r.code != http.StatusOK || len(listItems(r)) != 1 || strOf(listItems(r)[0].(map[string]any)["id"]) != ids[0] {
		t.Errorf("default order first event = %d %s, want the oldest %s", r.code, r.raw, ids[0])
	}
	for _, bad := range []url.Values{{"order": {"newest"}}, {"order": {"desc"}, "cursor": {"nope"}}} {
		if r := read(bad); r.code != http.StatusBadRequest {
			t.Errorf("events?%s = %d, want 400", bad.Encode(), r.code)
		}
	}
}
