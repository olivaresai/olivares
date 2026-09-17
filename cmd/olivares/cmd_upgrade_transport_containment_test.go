// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func TestUpgradeWrappedSafeCauseDoesNotExposeOuterURL(t *testing.T) {
	endpoint := withUserinfo("https://updates.example/stable/manifest.json", diagUser, diagPassword)
	inner := wrapUpgradeTransport(endpoint, context.Canceled)
	outer := &url.Error{Op: "Get", URL: endpoint, Err: inner}
	err := wrapUpgradeTransport(endpoint, outer)
	assertNoLeak(t, "", err.Error())
	var got *url.Error
	if !errors.As(err, &got) || got != outer || !errors.Is(err, context.Canceled) {
		t.Fatal("safe display must preserve the complete original error chain")
	}
}

type diagnosticFailureBody struct {
	err    error
	closed bool
}

func (b *diagnosticFailureBody) Read([]byte) (int, error) { return 0, b.err }
func (b *diagnosticFailureBody) Close() error {
	b.closed = true
	return nil
}

func TestUpgradeBodyReadFailureDoesNotExposeTransportText(t *testing.T) {
	for _, gated := range []bool{false, true} {
		name := "public"
		if gated {
			name = "gated"
		}
		t.Run(name, func(t *testing.T) {
			cause := &diagHostileCause{msg: diagDecoy + diagESC}
			body := &diagnosticFailureBody{err: cause}
			client := &http.Client{Transport: diagRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: r}, nil
			})}
			endpoint := withUserinfo("https://updates.example", diagUser, diagPassword)
			var err error
			if gated {
				_, _, _, err = gatedGet(context.Background(), client, endpoint, "fixture-token", "/download", nil)
			} else {
				_, err = httpGet(context.Background(), client, endpoint+"/stable/manifest.json")
			}
			if err == nil {
				t.Fatal("failed body read must not succeed")
			}
			assertNoLeak(t, "", err.Error())
			var got *diagHostileCause
			if !errors.As(err, &got) || got != cause || !body.closed {
				t.Fatal("body failure must preserve its cause and close the response body")
			}
		})
	}
}
