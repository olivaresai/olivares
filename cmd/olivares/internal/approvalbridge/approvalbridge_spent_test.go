// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package approvalbridge

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// A spent approval must not hide an unspent one behind it. The governed API lists approved
// grants in pages, so the unspent grant may sit on a later page than the spent ones: the
// scan has to read on past them, through the JSON the service credential sees.
func TestFindReusableReadsPastSpentGrantsOnEarlierPages(t *testing.T) {
	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	tenant := model.TenantID("tenant-spent-grants")
	cred := ServiceCred{Tenant: tenant, TenantStr: tenant.String(), Token: "synthetic-proposer", ExpiresIn: 3600}
	b := NewWithCreds(map[model.TenantID]ServiceCred{tenant: cred}, func() time.Time { return base }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	subject := EncodeSubjectRef("workspace:w1", "plan-1")
	decided := model.NewTimestamp(base.Add(-time.Minute)).String()
	row := func(id string, consumed bool) string {
		return fmt.Sprintf(`{"id":%q,"subject_kind":"sessions.run","subject_ref":%q,"status":"approved","decided_at":%q,"consumed":%t}`, id, subject, decided, consumed)
	}
	b.UseHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		switch {
		case q.Get("status") == "pending":
			io.WriteString(w, `{"items":[],"has_more":false}`)
		case q.Get("status") == "approved" && q.Get("cursor") == "":
			io.WriteString(w, `{"items":[`+row("spent-1", true)+`,`+row("spent-2", true)+`],"has_more":true,"cursor":"page-2"}`)
		case q.Get("status") == "approved" && q.Get("cursor") == "page-2":
			io.WriteString(w, `{"items":[`+row("unspent-3", false)+`],"has_more":false}`)
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		mode reuse
		want string
	}{
		{"a caller that may re-spend finds the first grant", reuseApproved, "spent-1"},
		{"a caller under a fresh consumer reads past the spent ones", reuseUnspent, "unspent-3"},
		{"a caller that reuses no approved grant finds none", reuseNone, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref, view, err := b.findReusable(ctx, cred, "sessions.run.launch", "sessions.run", subject, tc.mode)
			if err != nil || ref != tc.want {
				t.Fatalf("found %q (%v), want %q", ref, err, tc.want)
			}
			if tc.want != "" && view.Consumed != (tc.want != "unspent-3") {
				t.Fatalf("view.Consumed = %v for %s", view.Consumed, tc.want)
			}
		})
	}
}
