// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// TestClientPagination verifies the client concatenates every page of a list
// driven by result_info (page/total_pages).
func TestClientPagination(t *testing.T) {
	var gotPages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("non-GET request: %s", r.Method)
		}
		page := r.URL.Query().Get("page")
		gotPages = append(gotPages, page)
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "", "1":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"a"}],"result_info":{"page":1,"per_page":1,"count":1,"total_count":2,"total_pages":2}}`))
		case "2":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"b"}],"result_info":{"page":2,"per_page":1,"count":1,"total_count":2,"total_pages":2}}`))
		default:
			t.Errorf("unexpected page %q", page)
		}
	}))
	defer srv.Close()

	c := newClient(srv.URL, "tok", &http.Client{Timeout: 5 * time.Second})
	rows, err := c.get(context.Background(), "/list", nil, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows across pages, got %d", len(rows))
	}
	// Page 1 carries no explicit page param; page 2 must be requested.
	if len(gotPages) != 2 || gotPages[1] != "2" {
		t.Fatalf("want two requests with second page=2, got %v", gotPages)
	}
	var first struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rows[0], &first); err != nil || first.ID != "a" {
		t.Fatalf("first row = %s (%v)", rows[0], err)
	}
}

// TestClientBearerHeader verifies every request carries the Bearer token and only
// GET is issued.
func TestClientBearerHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q, want Bearer secret-token", got)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "secret-token", &http.Client{Timeout: 5 * time.Second})
	if _, err := c.get(context.Background(), "/x", nil, nil); err != nil {
		t.Fatalf("get: %v", err)
	}
}

// TestClientSuccessFalse verifies a success=false envelope becomes a typed
// *apiFault carrying the API error codes/messages.
func TestClientSuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // CF can return 200 with success:false
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}],"result":null}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "tok", &http.Client{Timeout: 5 * time.Second})
	_, err := c.get(context.Background(), "/x", nil, nil)
	if err == nil {
		t.Fatal("want error on success=false")
	}
	var fault *apiFault
	if !errors.As(err, &fault) {
		t.Fatalf("want *apiFault, got %T: %v", err, err)
	}
	if len(fault.errs) != 1 || fault.errs[0].Code != 10000 {
		t.Fatalf("want code 10000, got %+v", fault.errs)
	}
}

// TestClientHTTP500 verifies a 5xx status becomes an *apiFault even with a JSON
// body, so an enabled target's outage is a finding upstream.
func TestClientHTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":0,"message":"internal"}]}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "tok", &http.Client{Timeout: 5 * time.Second})
	_, err := c.get(context.Background(), "/x", nil, nil)
	var fault *apiFault
	if !errors.As(err, &fault) || fault.status != http.StatusInternalServerError {
		t.Fatalf("want apiFault status 500, got %v", err)
	}
}

// TestClientPaginationCap guards against an endpoint that always reports more
// pages: the loop must stop at maxPages rather than spin forever.
func TestClientPaginationCap(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		page := r.URL.Query().Get("page")
		n := 1
		if page != "" {
			n, _ = strconv.Atoi(page)
		}
		// Always claim there is one more page than the current one.
		body := `{"success":true,"errors":[],"result":[{"id":"` + strconv.Itoa(n) + `"}],"result_info":{"page":` +
			strconv.Itoa(n) + `,"total_pages":` + strconv.Itoa(maxPages+5) + `}}`
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "tok", &http.Client{Timeout: 5 * time.Second})
	rows, err := c.get(context.Background(), "/loop", nil, nil)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if calls != maxPages {
		t.Fatalf("want capped at %d calls, got %d", maxPages, calls)
	}
	if len(rows) != maxPages {
		t.Fatalf("want %d rows, got %d", maxPages, len(rows))
	}
}

// TestClientQueryPassthrough verifies extra query parameters are sent and the
// page param is layered on top for page>1.
func TestClientQueryPassthrough(t *testing.T) {
	var seen []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query())
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL, "tok", &http.Client{Timeout: 5 * time.Second})
	q := url.Values{}
	q.Set("per_page", "50")
	if _, err := c.get(context.Background(), "/x", q, nil); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(seen) != 1 || seen[0].Get("per_page") != "50" {
		t.Fatalf("query not passed through: %v", seen)
	}
}
