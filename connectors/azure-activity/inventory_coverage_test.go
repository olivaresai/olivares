// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureactivity_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

type coverageCapture struct{ observations []model.Observation }

func (s *coverageCapture) Emit(_ context.Context, o model.Observation) error {
	s.observations = append(s.observations, o)
	return nil
}

// This tracer uses only pre-existing public Gather/Sink APIs so its negative
// compiles before the additive SDK protocol exists.
func TestCoverageTruncationNilNegative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, `{"data":[],"count":0,"totalRecords":1,"resultTruncated":"true","$skipToken":"next"}`)
	}))
	defer srv.Close()
	src := openSource(t, srv.URL, map[string]string{"max_pages": "1", "enable_activity": "false"})
	sink := &coverageCapture{}
	if err := src.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	for _, o := range sink.observations {
		if string(o.ObservationType()) == "inventory_collection_report" {
			return
		}
	}
	t.Fatal("truncated Gather returned nil without a typed inventory collection result")
}

func TestInventoryCoveragePaging(t *testing.T) {
	rowA := `{"id":"/subscriptions/sub-1/resourceGroups/r/providers/Test/things/a","subscriptionId":"sub-1"}`
	rowB := `{"id":"/subscriptions/sub-1/resourceGroups/r/providers/Test/things/b","subscriptionId":"sub-1"}`
	for _, tc := range []struct {
		name    string
		pages   []string
		want    string
		members int
		max     string
	}{
		{"false plus cursor", []string{`{"data":[` + rowA + `],"count":1,"totalRecords":2,"resultTruncated":"false","$skipToken":"next"}`, `{"data":[` + rowB + `],"count":1,"totalRecords":2,"resultTruncated":"false"}`}, "complete", 2, "3"},
		{"empty complete", []string{`{"data":[],"count":0,"totalRecords":0,"resultTruncated":"false"}`}, "complete", 0, "3"},
		{"truncated no cursor", []string{`{"data":[` + rowA + `],"count":1,"totalRecords":2,"resultTruncated":"true"}`}, "partial", 1, "3"},
		{"cap", []string{`{"data":[` + rowA + `],"count":1,"totalRecords":2,"resultTruncated":"false","$skipToken":"next"}`}, "partial", 1, "1"},
		{"repeat cursor", []string{`{"data":[` + rowA + `],"count":1,"totalRecords":3,"resultTruncated":"false","$skipToken":"next"}`, `{"data":[` + rowB + `],"count":1,"totalRecords":3,"resultTruncated":"false","$skipToken":"next"}`}, "partial", 2, "3"},
		{"bad enum", []string{`{"data":[` + rowA + `],"count":1,"totalRecords":1,"resultTruncated":"maybe"}`}, "partial", 1, "3"},
		{"boolean is not string enum", []string{`{"data":[` + rowA + `],"count":1,"totalRecords":1,"resultTruncated":false}`}, "partial", 1, "3"},
		{"missing indicators", []string{`{"data":[` + rowA + `]}`}, "partial", 1, "3"},
		{"null cursor", []string{`{"data":[],"count":0,"totalRecords":0,"resultTruncated":"false","$skipToken":null}`}, "partial", 0, "3"},
		{"mismatched page count", []string{`{"data":[` + rowA + `],"count":0,"totalRecords":1,"resultTruncated":"false"}`}, "partial", 1, "3"},
		{"permission refused", []string{"DENIED"}, "unavailable", 0, "3"},
		{"missing data", []string{`{"count":0,"totalRecords":0,"resultTruncated":"false"}`}, "partial", 0, "3"},
		{"invalid row preserves neighbors", []string{`{"data":[` + rowA + `,{"id":12,"subscriptionId":"sub-1"}],"count":2,"totalRecords":2,"resultTruncated":"false"}`}, "partial", 1, "3"},
		{"outside requested scope", []string{`{"data":[{"id":"/subscriptions/sub-3/providers/test/things/a","subscriptionId":"sub-3"}],"count":1,"totalRecords":1,"resultTruncated":"false"}`}, "partial", 0, "3"},
		{"changing total", []string{`{"data":[` + rowA + `],"count":1,"totalRecords":2,"resultTruncated":"false","$skipToken":"next"}`, `{"data":[` + rowB + `],"count":1,"totalRecords":3,"resultTruncated":"false"}`}, "partial", 2, "3"},
		{"duplicate identity", []string{`{"data":[` + rowA + `,` + rowA + `],"count":2,"totalRecords":2,"resultTruncated":"false"}`}, "partial", 2, "3"},
		{"later provider failure", []string{`{"data":[` + rowA + `],"count":1,"totalRecords":2,"resultTruncated":"false","$skipToken":"next"}`, "FAIL"}, "partial", 1, "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var query string
			var selectors string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Query         string         `json:"query"`
					Subscriptions []string       `json:"subscriptions"`
					Options       map[string]any `json:"options"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.URL.Query().Get("api-version") != "2022-10-01" || !strings.Contains(body.Query, "order by id") || body.Options["resultFormat"] != "objectArray" {
					t.Error("query contract changed")
				}
				if calls > 0 && (query != body.Query || selectors != strings.Join(body.Subscriptions, ",") || body.Options["$skipToken"] != "next") {
					t.Error("continuation changed query/cursor")
				}
				query = body.Query
				selectors = strings.Join(body.Subscriptions, ",")
				if calls >= len(tc.pages) {
					t.Error("unexpected page")
					w.WriteHeader(500)
					return
				}
				page := tc.pages[calls]
				calls++
				if page == "DENIED" {
					w.WriteHeader(403)
					return
				}
				if page == "FAIL" {
					w.WriteHeader(503)
					return
				}
				writeJSON(w, page)
			}))
			defer srv.Close()
			src := openSource(t, srv.URL, map[string]string{"enable_activity": "false", "max_pages": tc.max})
			sink := &coverageCapture{}
			if err := src.Gather(context.Background(), sink); err != nil {
				t.Fatal(err)
			}
			reports, members := 0, 0
			for _, obs := range sink.observations {
				switch v := obs.(type) {
				case model.InventoryCollectionReport:
					reports++
					if v.State != tc.want || v.Count != int64(tc.members) {
						t.Errorf("result %+v, want %s/%d", v, tc.want, tc.members)
					}
				case model.InventoryCollectionMember:
					members++
				}
			}
			if reports != 1 || members != tc.members {
				t.Fatalf("reports=%d members=%d", reports, members)
			}
		})
	}
}

func TestInventoryCoverageUnavailableAndAutoScope(t *testing.T) {
	for _, tc := range []struct {
		name, state, reason string
		cfg                 map[string]string
	}{
		{"offline", "unavailable", "offline", map[string]string{"access_token": ""}},
		{"disabled", "unavailable", "disabled", map[string]string{"enable_inventory": "false"}},
		{"auto discovery", "unknown", "scope_unproven", map[string]string{"subscriptions": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == "/subscriptions" {
					writeJSON(w, `{"value":[{"subscriptionId":"sub-1","state":"Enabled"}]}`)
					return
				}
				writeJSON(w, `{"data":[],"count":0,"totalRecords":0,"resultTruncated":"false"}`)
			}))
			defer srv.Close()
			cfg := map[string]string{"enable_activity": "false"}
			for k, v := range tc.cfg {
				cfg[k] = v
			}
			src := openSource(t, srv.URL, cfg)
			sink := &coverageCapture{}
			if err := src.Gather(context.Background(), sink); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, o := range sink.observations {
				if report, ok := o.(model.InventoryCollectionReport); ok {
					found = true
					if report.State != tc.state || report.Reason != tc.reason || report.FulfilledScope != "" {
						t.Fatalf("unproved scope %+v", report)
					}
				}
			}
			if !found {
				t.Fatal("no result")
			}
			if tc.name == "offline" && calls != 0 {
				t.Fatal("offline contacted provider")
			}
		})
	}
}
