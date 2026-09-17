// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// TestUpgradeTransportErrorNamesTheActualRequestMethod: the shared redacted transport diagnostic names
// the request's own method, keeps upgrade's GET wording, and never shows userinfo, path or query.
func TestUpgradeTransportErrorNamesTheActualRequestMethod(t *testing.T) {
	endpoint := withUserinfo("https://licenses.example/connect/refresh?operation=x", diagUser, diagPassword)
	for _, tc := range []struct {
		err  error
		want string
	}{
		{wrapTransportMethod(http.MethodPost, endpoint, context.DeadlineExceeded), "POST https://licenses.example: network error"},
		{wrapTransportMethod(http.MethodDelete, endpoint, context.DeadlineExceeded), "DELETE https://licenses.example: network error"},
		{wrapUpgradeTransport(endpoint, context.DeadlineExceeded), "GET https://licenses.example: network error"},
		{wrapTransportMethod("PO\x1bST", endpoint, context.DeadlineExceeded), "REQUEST https://licenses.example: network error"},
		{wrapTransportMethod("GET "+endpoint, endpoint, context.DeadlineExceeded), "REQUEST https://licenses.example: network error"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Fatalf("diagnostic %q, want %q", got, tc.want)
		}
		assertNoLeak(t, "", tc.err.Error())
		if !errors.Is(tc.err, context.DeadlineExceeded) {
			t.Fatal("the diagnostic dropped the original cause")
		}
	}
	// An already redacted diagnostic is returned as it is, whatever method the outer call names.
	inner := wrapTransportMethod(http.MethodPost, endpoint, context.Canceled)
	if outer := wrapUpgradeTransport(endpoint, inner); outer != inner {
		t.Fatal("a redacted diagnostic was wrapped again")
	}
}
