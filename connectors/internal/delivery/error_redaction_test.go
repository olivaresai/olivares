// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDeliveryErrorsRedactReflectedCredentials(t *testing.T) {
	const canary = "delivery-canary-opaque"
	d := &stubDoer{responses: []stubResp{{status: http.StatusForbidden, body: `{"code":7,"text":"rejected ` + canary + `"}`}}}
	r, err := New(d, Options{MaxAttempts: 1}).Send(context.Background(), Request{URL: "https://fixture.invalid/hook", Header: map[string]string{"X-API-Key": canary}})
	if err == nil || r.StatusCode != http.StatusForbidden || r.Attempts != 1 {
		t.Fatal("HTTP rejection changed")
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(r.Body, canary) || strings.Contains(string(r.RawBody), canary) {
		t.Fatal("delivery rejection retained synthetic secret")
	}
	var verdict struct {
		Code int `json:"code"`
	}
	if json.Unmarshal([]byte(r.Body), &verdict) != nil || verdict.Code != 7 || !r.BodyComplete {
		t.Fatal("redaction lost the complete protocol status used to classify delivery")
	}
}

func TestDeliverySuccessfulBinaryResponseIsUnchanged(t *testing.T) {
	raw := "\x00\x08\x01Bearer short-token\xff"
	doer := &stubDoer{responses: []stubResp{{status: 200, body: raw}}}
	res, err := New(doer, Options{MaxAttempts: 1}).Send(context.Background(), Request{URL: "https://fixture.invalid/"})
	if err != nil || !res.BodyComplete || string(res.RawBody) != raw {
		t.Fatal("successful protocol response bytes changed")
	}
}

func TestDeliveryErrorRedactsFormCredential(t *testing.T) {
	const canary = "body-only-routing-canary"
	doer := &stubDoer{responses: []stubResp{{status: 403, body: "rejected " + canary}}}
	res, err := New(doer, Options{MaxAttempts: 1}).Send(context.Background(), Request{
		URL: "https://fixture.invalid/", Credentials: []string{canary}, Body: []byte(canary),
	})
	if err == nil || res.StatusCode != 403 {
		t.Fatal("HTTP rejection lost")
	}
	if strings.Contains(err.Error(), canary) || strings.Contains(res.Body, canary) || strings.Contains(string(res.RawBody), canary) {
		t.Fatal("form-only credential retained in diagnostic")
	}
}
