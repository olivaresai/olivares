// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchThreadsRefusesTruncationAtMaxPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/sesn_1/threads" {
			t.Errorf("path = %q, want thread-list endpoint", r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "100" {
			t.Errorf("limit = %q, want 100", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"sthr_1"}],"next_page":"p2"}`))
	}))
	defer srv.Close()

	cl := newClient(config{baseURL: srv.URL, maxPages: 1}, srv.Client())
	threads, err := cl.fetchThreads(context.Background(), "sesn_1")
	if err == nil || !strings.Contains(err.Error(), "max_pages") {
		t.Fatalf("paging past the bound must error, got %v", err)
	}
	if threads != nil {
		t.Fatalf("truncation must not return partial threads, got %+v", threads)
	}
}

func TestFetchThreadsAcceptsCompleteLastAllowedPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"sthr_complete"}],"next_page":""}`))
	}))
	defer srv.Close()

	cl := newClient(config{baseURL: srv.URL, maxPages: 1}, srv.Client())
	threads, err := cl.fetchThreads(context.Background(), "sesn_1")
	if err != nil {
		t.Fatalf("complete page at max_pages must succeed: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != "sthr_complete" {
		t.Fatalf("threads = %+v, want the complete page", threads)
	}
}
