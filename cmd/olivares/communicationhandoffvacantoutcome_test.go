// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The overlap cases in communicationhandoffvacantconcurrent_http_test.go hold the
// tenant write gate and queue two public requests behind it. The gate orders ONE
// transaction at a time, and each public request is several (measured on
// PostgreSQL 16 with log_statement=all: the first ones write nothing, the last
// one is the deciding write, and each takes the gate on its own). So the request
// that queued first at its first transaction is not necessarily the one that
// reaches the deciding transaction first: after the barrier opens, whichever
// request gets back to the gate first wins it, and on a slow host that is a
// matter of timing. What the product guarantees is that exactly one of the two
// wins and the other is refused. These helpers name the winner from what the two
// responses said and refuse a pair that has no winner or two; the callers then
// assert the loser's own refusal and that nothing else was written.

// vacantAcceptAcquireWinner reports whether the accept won the overlap with the
// owner's lease.acquire. The accept winning is its 200 with the acquire refused
// by its own If-Match (412 version_mismatch: the transfer moved the item); the
// acquire winning is its 200 with the accept refused by the changed generation
// (409). Any other pair is not an outcome of this overlap.
func vacantAcceptAcquireWinner(accepted, acquired communicationHTTPTestResponse) (bool, error) {
	switch {
	case accepted.status == http.StatusOK &&
		acquired.status == http.StatusPreconditionFailed &&
		strings.Contains(string(acquired.raw), "version_mismatch"):
		return true, nil
	case acquired.status == http.StatusOK &&
		accepted.status == http.StatusConflict &&
		strings.Contains(string(accepted.raw), `"code":"conflict"`):
		return false, nil
	}
	return false, fmt.Errorf("the overlap has no clean winner: accept=%d %s; acquire=%d %s",
		accepted.status, accepted.raw, acquired.status, acquired.raw)
}

// vacantFreshAccept reports whether the first of two overlapping accepts is the
// one that made the transfer. The winner is the accept answered 200 that is not
// a replay; the other is a replay of it (same key), a conflict (new key, user
// recipient) or an unknown (new key, session recipient). Two fresh accepts are
// two transfers and none is no transfer: both are errors.
func vacantFreshAccept(first, second communicationHTTPTestResponse) (bool, error) {
	a, b := vacantAcceptIsFresh(first), vacantAcceptIsFresh(second)
	switch {
	case a && !b:
		return true, nil
	case b && !a:
		return false, nil
	}
	return false, fmt.Errorf("the overlap has not exactly one fresh accept: first=%d %s; second=%d %s",
		first.status, first.raw, second.status, second.raw)
}

func vacantAcceptIsFresh(response communicationHTTPTestResponse) bool {
	// `"replayed":true` is the only marker; its absence on a 200 is a fresh result.
	return response.status == http.StatusOK &&
		!strings.Contains(string(response.raw), `"replayed":true`)
}

func TestVacantAcceptAcquireWinner(t *testing.T) {
	version := []byte(`{"code":"version_mismatch"}`)
	conflict := []byte(`{"error":{"code":"conflict"}}`)
	cases := []struct {
		name          string
		accepted      communicationHTTPTestResponse
		acquired      communicationHTTPTestResponse
		wantAcceptWon bool
		wantErr       bool
	}{
		{"accept first", communicationHTTPTestResponse{status: 200},
			communicationHTTPTestResponse{status: 412, raw: version}, true, false},
		{"acquire overtakes", communicationHTTPTestResponse{status: 409, raw: conflict},
			communicationHTTPTestResponse{status: 200}, false, false},
		{"both won", communicationHTTPTestResponse{status: 200},
			communicationHTTPTestResponse{status: 200}, false, true},
		{"neither won", communicationHTTPTestResponse{status: 409, raw: conflict},
			communicationHTTPTestResponse{status: 412, raw: version}, false, true},
		{"accept refused by a 409 that is not the conflict", communicationHTTPTestResponse{status: 409, raw: []byte(`{"code":"other"}`)},
			communicationHTTPTestResponse{status: 200}, false, true},
		{"acquire refused as an ineligible owner", communicationHTTPTestResponse{status: 200},
			communicationHTTPTestResponse{status: 422, raw: []byte(`{"code":"owner_ineligible"}`)}, false, true},
		{"accept unknown behind a winning acquire", communicationHTTPTestResponse{status: 503, raw: []byte(`{"code":"evidence_unavailable"}`)},
			communicationHTTPTestResponse{status: 200}, false, true},
		{"acquire refused for another reason", communicationHTTPTestResponse{status: 200},
			communicationHTTPTestResponse{status: 412, raw: []byte(`{"code":"plan_changed"}`)}, false, true},
		{"accept refused for another reason", communicationHTTPTestResponse{status: 503},
			communicationHTTPTestResponse{status: 200}, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			won, err := vacantAcceptAcquireWinner(c.accepted, c.acquired)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want an error: %t", err, c.wantErr)
			}
			if err == nil && won != c.wantAcceptWon {
				t.Fatalf("accept won = %t, want %t", won, c.wantAcceptWon)
			}
		})
	}
}

func TestVacantFreshAccept(t *testing.T) {
	fresh := communicationHTTPTestResponse{status: 200, raw: []byte(`{"replayed":false}`)}
	replay := communicationHTTPTestResponse{status: 200, raw: []byte(`{"replayed":true}`)}
	conflict := communicationHTTPTestResponse{status: 409}
	unknown := communicationHTTPTestResponse{status: 503}
	cases := []struct {
		name          string
		first, second communicationHTTPTestResponse
		firstWon      bool
		wantErr       bool
	}{
		{"first made the transfer, same key", fresh, replay, true, false},
		{"second made the transfer, same key", replay, fresh, false, false},
		{"second overtakes a user recipient", conflict, fresh, false, false},
		{"first made the transfer for a user recipient", fresh, conflict, true, false},
		{"second overtakes a session recipient", unknown, fresh, false, false},
		{"two fresh transfers", fresh, fresh, false, true},
		{"two replays", replay, replay, false, true},
		{"no transfer", conflict, unknown, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			firstWon, err := vacantFreshAccept(c.first, c.second)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want an error: %t", err, c.wantErr)
			}
			if err == nil && firstWon != c.firstWon {
				t.Fatalf("first won = %t, want %t", firstWon, c.firstWon)
			}
		})
	}
}
