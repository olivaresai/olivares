//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vault

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

func TestCommunityVaultActuatorUnavailable(t *testing.T) {
	const token = "edition-local-vault-probe"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/identity/entity/id/ent-edition-probe" || r.Header.Get("X-Vault-Token") != token || r.Header.Get("X-Vault-Namespace") != "edition-probe" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var body struct {
			Disabled bool `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Disabled {
			http.Error(w, "invalid entity update", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	a := NewActuator(server.URL, token, "edition-probe", server.Client())
	if a == nil {
		t.Fatal("Community must return a nonnil refusing actuator")
	}
	if a.Capabilities() != nil {
		t.Error("Community must advertise no lifecycle capabilities")
	}
	receipt, err := a.Disable(context.Background(), identitysource.ActuationRequest{Ref: "ent-edition-probe", Kind: "vault_entity"})
	if err == nil || !strings.Contains(err.Error(), "Business") {
		t.Error("disable must return an explicit Business-unavailable error")
	}
	if !reflect.DeepEqual(receipt, identitysource.ActuationReceipt{}) {
		t.Error("unavailable disable returned an actuation receipt")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("Community performed %d lifecycle HTTP requests, want none", got)
	}
	req := identitysource.ActuationRequest{Ref: "ent-edition-probe", Kind: "vault_entity", TargetRef: "approle:edition-probe", CredentialRefs: []string{"accessor-edition-probe"}}
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
