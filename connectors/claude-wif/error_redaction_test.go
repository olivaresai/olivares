// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestActuationErrorRedactsEveryReflectedField(t *testing.T) {
	const canary = "admin-opaque-canary"
	doer := &actuatorDoer{status: 403, body: `{"error":{"type":"admin-opaque-canary","message":"rejected admin-opaque-canary"},"request_id":"admin-opaque-canary"}`, header: http.Header{"Request-Id": {canary}}}
	a := NewActuator("https://fixture.invalid", canary, doer)
	_, err := a.Disable(context.Background(), keyRequest())
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatal("HTTP rejection lost")
	}
	if doer.gotHeader.Get("X-API-Key") != canary {
		t.Fatal("credential not sent")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("actuator diagnostic retained credential")
	}
}

func TestExchangeErrorRedactsCodeAndRequestID(t *testing.T) {
	const canary = "assertion-opaque-canary"
	doer := &exchangeDoer{status: 401, body: `{"error":"assertion-opaque-canary","request_id":"assertion-opaque-canary"}`, header: http.Header{"Request-Id": {canary}}}
	_, err := NewExchanger("https://fixture.invalid", doer).Exchange(context.Background(), canary, testParams())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatal("HTTP rejection lost")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("exchange diagnostic retained assertion")
	}
}
