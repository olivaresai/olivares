// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestListenDistinguishesTeardownFromTruncation pins the semantics the published
// 2026-07-28 schema introduced with SubscriptionsListenResultResponse: "A
// successful response from the server for a subscriptions/listen request, sent
// when the server tears the subscription down gracefully."
//
// The connector previously dropped that response with a comment asserting "the
// request never resolves in the RC" — true of the release candidate, false of
// the published revision. The consequence was not cosmetic: a deliberate server
// shutdown and a dropped connection reached the caller identically (both nil),
// so nothing could decide whether re-subscribing was correct or a retry storm
// against a server that had already said it was done.
func TestListenDistinguishesTeardownFromTruncation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		teardown bool
		wantErr  error
	}{
		{"graceful teardown ends cleanly", true, nil},
		{"stream cut without teardown is reported", false, ErrSubscriptionTruncated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					ID int64 `json:"id"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"%s\",\"params\":{\"_meta\":{\"%s\":\"1\"}}}\n\n",
					notificationSubscriptionsAcknowledged, metaSubscriptionID)
				if tc.teardown {
					fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{}}\n\n", body.ID)
				}
			}))
			defer srv.Close()

			tr, _ := newStatelessHTTPTransport(serverSpec{Name: "s", URL: srv.URL})
			err := newStatelessClient(tr).Listen(context.Background(), subscriptionFilter{ToolsListChanged: true}, func(subscriptionEvent) {})
			if tc.wantErr == nil && err != nil {
				t.Fatalf("graceful teardown must end cleanly, got %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("truncated stream error = %v, want %v: silently returning nil hides missed notifications", err, tc.wantErr)
			}
		})
	}
}
