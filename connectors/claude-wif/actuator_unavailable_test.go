//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/connectors/identitysource"
)

func TestCommunityAnthropicActuatorUnavailable(t *testing.T) {
	const adminKey = "sk-ant-admin-edition-local-probe"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/organizations/api_keys/apikey_edition_probe" || r.Header.Get("x-api-key") != adminKey {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var body struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Status != "inactive" {
			http.Error(w, "invalid key update", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"apikey_edition_probe","type":"api_key","status":"inactive"}`))
	}))
	defer server.Close()
	a := NewActuator(server.URL, adminKey, server.Client())
	if a == nil {
		t.Fatal("Community must return a nonnil refusing actuator")
	}
	if a.Capabilities() != nil {
		t.Error("Community must advertise no lifecycle capabilities")
	}
	receipt, err := a.Disable(context.Background(), identitysource.ActuationRequest{Ref: "apikey_edition_probe", Kind: "api_key"})
	if err == nil || !strings.Contains(err.Error(), "Business") {
		t.Error("disable must return an explicit Business-unavailable error")
	}
	if !reflect.DeepEqual(receipt, identitysource.ActuationReceipt{}) {
		t.Error("unavailable disable returned an actuation receipt")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("Community performed %d lifecycle HTTP requests, want none", got)
	}
	req := identitysource.ActuationRequest{Ref: "apikey_edition_probe", Kind: "api_key"}
	for _, tc := range []struct {
		name string
		call func(context.Context, identitysource.ActuationRequest) (identitysource.ActuationReceipt, error)
	}{{"restore", a.Restore}, {"finalize", a.Finalize}, {"retire", a.Retire}} {
		t.Run(tc.name, func(t *testing.T) {
			receipt, err := tc.call(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), "Business") || !reflect.DeepEqual(receipt, identitysource.ActuationReceipt{}) {
				t.Error("unavailable lifecycle operation must return Business error and zero receipt")
			}
		})
	}
	credential, err := a.Rotate(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "Business") || !reflect.DeepEqual(credential, identitysource.RotatedCredential{}) {
		t.Error("unavailable rotate must return Business error without credential data")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("Community performed %d lifecycle HTTP requests across all operations, want none", got)
	}
}
