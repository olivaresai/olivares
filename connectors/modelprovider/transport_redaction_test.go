// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSignedResultsTransportErrorDoesNotExposeCredentials(t *testing.T) {
	const queryCredential = "transport-query-fixture/+?opaque"
	const username = "transport-user-fixture"
	const password = "transport-password-fixture"
	for _, failure := range []string{"connection-refused", "context-canceled", "deadline-exceeded", "malformed-request"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("closed fixture unexpectedly received a request") }))
			client := server.Client()
			server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "context-canceled" {
				cancel()
			}
			if failure == "deadline-exceeded" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				defer deadlineCancel()
			}
			results, err := url.Parse(server.URL + "/results?sig=" + url.QueryEscape(queryCredential))
			if err != nil {
				t.Fatal("invalid fixture URL")
			}
			results.User = url.UserPassword(username, password)
			c := NewInferenceClient(server.URL, client, AuthBearer, "independent-header-fixture", nil)
			rawURL := results.String()
			if failure == "malformed-request" {
				rawURL = strings.Replace(rawURL, results.Host, results.Host+":invalid", 1)
			}
			data, err := c.GetBytes(ctx, rawURL, nil)
			var typed *url.Error
			if data != nil || err == nil || !errors.As(err, &typed) {
				t.Fatal("missing typed transport failure")
			}
			switch failure {
			case "malformed-request":
				if typed.Op != "parse" {
					t.Fatal("request parse failure kind lost")
				}
			case "connection-refused":
				if !errors.Is(err, syscall.ECONNREFUSED) {
					t.Fatal("connection refusal identity lost")
				}
			case "context-canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatal("transport cancellation identity lost")
				}
			case "deadline-exceeded":
				if !errors.Is(err, context.DeadlineExceeded) || !typed.Timeout() {
					t.Fatal("transport timeout identity lost")
				}
			}
			safeURL, parseErr := url.Parse(typed.URL)
			if parseErr != nil || safeURL.User != nil || safeURL.RawQuery != "" {
				t.Fatal("typed transport URL retained confidential components")
			}
			for _, value := range []string{queryCredential, url.QueryEscape(queryCredential), username, password} {
				if strings.Contains(err.Error(), value) {
					t.Fatal("signed-results transport error retained synthetic credentials")
				}
			}
		})
	}
}
