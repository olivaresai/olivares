// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSCIMCreateExternalIdentityConflictEnvelope(t *testing.T) {
	const usersURL = "/v1/scim/v2/Users"
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			h := newHarnessOptsFromStoreSource(t, harnessStoreSource{open: scimConcurrentStore(engine)}, nil)
			ctx := context.Background()
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "identity-owner")
			other := h.createOrg(admin, "identity-other")
			super, err := h.authr.Authenticate(ctx, admin)
			if err != nil {
				t.Fatal(err)
			}
			token := h.scimToken(super, tenant)
			otherToken := h.scimToken(super, other)
			for _, tc := range []struct {
				name, stored, incoming string
				foreign, omitted       bool
				want                   int
			}{
				{name: "different", stored: "idp-9", incoming: "idp-other", want: http.StatusConflict},
				{name: "stored-absent", incoming: "idp-9", want: http.StatusConflict},
				{name: "case-different", stored: "IdP-9", incoming: "idp-9", want: http.StatusConflict},
				{name: "whitespace-different", stored: "idp-9", incoming: " idp-9 ", want: http.StatusConflict},
				{name: "whitespace-explicit", incoming: " \t ", want: http.StatusConflict},
				{name: "whitespace-preserved", stored: " idp-9 ", incoming: "idp-9", want: http.StatusConflict},
				{name: "foreign-exact", stored: "idp-9", incoming: "idp-9", foreign: true, want: http.StatusConflict},
				{name: "exact", stored: "IdP-9", incoming: "IdP-9", want: http.StatusOK},
				{name: "optional", stored: "idp-9", omitted: true, want: http.StatusOK},
			} {
				t.Run(tc.name, func(t *testing.T) {
					request := map[string]any{"userName": tc.name + "@example.test", "externalId": tc.stored, "displayName": "Original", "active": true}
					encode := func() string {
						b, err := json.Marshal(request)
						if err != nil {
							t.Fatal(err)
						}
						return string(b)
					}
					seed := h.scim("POST", usersURL, token, encode())
					if seed.code != http.StatusCreated {
						t.Fatalf("seed create = %d %s", seed.code, seed.raw)
					}
					id, ok := seed.body["id"].(string)
					if !ok || id == "" {
						t.Fatal("seed create returned no id")
					}
					if tc.stored != "" && seed.body["externalId"] != tc.stored {
						t.Fatal("POST changed the explicit external identity bytes")
					}
					// A duplicate member and a foreign identity collision must expose
					// the same error body, without id, externalId or Location.
					duplicate := h.scim("POST", usersURL, token, encode())
					if duplicate.code != http.StatusConflict || duplicate.body["scimType"] != scim.TypeUniqueness {
						t.Fatalf("duplicate control = %d %s", duplicate.code, duplicate.raw)
					}
					caller := token
					if tc.foreign {
						caller = otherToken
					} else {
						if deleted := h.scim("DELETE", usersURL+"/"+id, token, ""); deleted.code != http.StatusNoContent {
							t.Fatalf("offboard = %d %s", deleted.code, deleted.raw)
						}
						// Complete only the fixture's real retirement record. The
						// create must now exercise custodian readmission, not the
						// already-member shortcut or pending-retirement refusal.
						if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
							rows, _, err := as.TenantExclusions().List(ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: id}}, Limit: 2})
							if err != nil {
								return err
							}
							if len(rows) != 1 {
								t.Fatal("offboard fixture lacks one retirement record")
							}
							rec := rows[0]
							rec.RetirementState = model.RetirementRetired
							rec.NextAttemptAt = nil
							_, err = as.TenantExclusions().Update(ctx, rec)
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}
					request["externalId"] = tc.incoming
					request["displayName"] = "Replacement"
					if tc.omitted {
						delete(request, "externalId")
					}
					got := h.scim("POST", usersURL, caller, encode())
					if got.code != tc.want || got.hdr.Get("Location") != "" {
						t.Fatalf("create = %d %s, Location %q; want %d without Location", got.code, got.raw, got.hdr.Get("Location"), tc.want)
					}
					if tc.want == http.StatusConflict {
						if !reflect.DeepEqual(got.body, duplicate.body) {
							t.Fatalf("collision disclosed a different envelope: %s; duplicate: %s", got.raw, duplicate.raw)
						}
						if visible := h.scim("GET", usersURL+"/"+id, caller, ""); visible.code != http.StatusNotFound {
							t.Fatalf("refused create exposed or readmitted account: %d %s", visible.code, visible.raw)
						}
					} else if got.body["id"] != id || got.body["externalId"] != tc.stored || got.body["displayName"] != "Original" {
						t.Fatalf("legitimate readmission changed the identity: %s", got.raw)
					}
				})
			}
		})
	}
}
