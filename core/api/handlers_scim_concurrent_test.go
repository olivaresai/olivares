// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Several IdP threads POST the same new userName at once. Exactly one request
// creates the account and answers 201. Every other request is told the
// userName is taken, in one of the two designed ways: 409 uniqueness when the
// route's existence check or the unique index refuses it, or 200 with the
// stored resource when the create itself finds the account already held by a
// member of this tenant (auth.SCIMProvisionUser). No request fails on the
// server, and the race leaves one account with one membership.
//
// On PostgreSQL the losers pass the existence check before the winner commits
// and reach the INSERT behind the directory writer lock, so the unique index is
// the last word: a unique violation that escaped the store's conflict mapping
// answers 500 here. SQLite serializes every writer on one connection, so its
// losers see the committed row and never reach the INSERT. A lost
// unique-to-conflict mapping therefore shows only on PostgreSQL.

const (
	// scimConcurrentCreates is the number of requests racing for one userName.
	scimConcurrentCreates = 8
	// scimConcurrentRounds is the number of userNames raced on one store.
	scimConcurrentRounds = 4
)

// scimCreateAnswer is what one racing create was told.
type scimCreateAnswer struct {
	code     int
	id       string
	scimType string
	raw      string
}

// scimAddressState is what the store holds for a raced address once its round
// is over: the accounts with that address and, when there is exactly one, its
// memberships.
type scimAddressState struct {
	accounts    int
	memberships []model.Membership
}

func TestSCIMConcurrentDuplicateCreateAnswersConflict(t *testing.T) {
	for _, engine := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(engine), func(t *testing.T) {
			h := newHarnessOptsFromStoreSource(t, harnessStoreSource{open: scimConcurrentStore(engine)}, nil)
			ctx := context.Background()
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "acme")
			super, err := h.authr.Authenticate(ctx, admin)
			if err != nil {
				t.Fatal(err)
			}
			token := h.scimToken(super, tenant)

			userNames := make([]string, scimConcurrentRounds)
			rounds := make([][]scimCreateAnswer, scimConcurrentRounds)
			states := make([]scimAddressState, scimConcurrentRounds)
			for r := range rounds {
				userNames[r] = fmt.Sprintf("race-%d@acme.example", r)
				rounds[r] = raceSCIMCreates(t, h, token, userNames[r], scimConcurrentCreates)
				states[r] = readSCIMAddressState(t, h.st, auth.SCIMUserNameKey(userNames[r]))
				t.Logf("scim-concurrent-census engine=%s round=%d %s", engine, r, scimCensus(rounds[r]))
			}

			// No racing create fails on the server, and the race leaves exactly
			// one account, the one the 201 names.
			t.Run("no_server_error", func(t *testing.T) {
				for r, answers := range rounds {
					var created []scimCreateAnswer
					for _, a := range answers {
						switch {
						case a.code >= http.StatusInternalServerError:
							t.Errorf("round %d: a racing create answered %d: %s", r, a.code, a.raw)
						case a.code == http.StatusCreated:
							created = append(created, a)
						case a.code != http.StatusOK && a.code != http.StatusConflict:
							t.Errorf("round %d: a racing create answered %d, want 201, 200 or 409: %s", r, a.code, a.raw)
						}
					}
					if len(created) != 1 {
						t.Errorf("round %d: %d racing creates answered 201, want exactly one (%s)", r, len(created), scimCensus(answers))
						continue
					}
					stored, found, err := h.authr.SCIMFindMember(ctx, tenant, "email", auth.SCIMUserNameKey(userNames[r]))
					if err != nil || !found {
						t.Errorf("round %d: member lookup = found %t, err %v; want the created account", r, found, err)
						continue
					}
					if got := stored.ID.String(); got != created[0].id {
						t.Errorf("round %d: stored member %s, want the account the 201 named, %s", r, got, created[0].id)
					}
					for _, a := range answers {
						if a.code == http.StatusOK && a.id != created[0].id {
							t.Errorf("round %d: a 200 named account %q, want the created account %q", r, a.id, created[0].id)
						}
					}
				}
			})

			// Each round answers with the designed responses: one 201 naming the
			// account it created, and for every other request either a 200 naming
			// that same resource or a 409 with scimType uniqueness.
			t.Run("designed_responses", func(t *testing.T) {
				for r, answers := range rounds {
					var created []scimCreateAnswer
					for _, a := range answers {
						if a.code == http.StatusCreated {
							created = append(created, a)
						}
					}
					if len(created) != 1 || created[0].id == "" {
						t.Errorf("round %d: %d racing creates answered 201, want exactly one naming its resource (%s)", r, len(created), scimCensus(answers))
						continue
					}
					for _, a := range answers {
						switch a.code {
						case http.StatusCreated:
						case http.StatusOK:
							if a.id != created[0].id {
								t.Errorf("round %d: a 200 named resource %q, want the created resource %q", r, a.id, created[0].id)
							}
						case http.StatusConflict:
							if a.scimType != scim.TypeUniqueness {
								t.Errorf("round %d: a 409 carried scimType %q, want %q: %s", r, a.scimType, scim.TypeUniqueness, a.raw)
							}
						default:
							t.Errorf("round %d: a racing create answered %d, want 201, 200 or 409: %s", r, a.code, a.raw)
						}
					}
				}
			})

			// Each round leaves one account for the address, and that account
			// holds one membership, in this tenant.
			t.Run("one_account_and_membership", func(t *testing.T) {
				for r, state := range states {
					if state.accounts != 1 {
						t.Errorf("round %d: %d accounts hold %s, want one", r, state.accounts, userNames[r])
						continue
					}
					if len(state.memberships) != 1 || state.memberships[0].TargetTenantID != tenant {
						tenants := make([]model.TenantID, 0, len(state.memberships))
						for _, m := range state.memberships {
							tenants = append(tenants, m.TargetTenantID)
						}
						t.Errorf("round %d: the account holding %s has memberships in %v, want one, in %s", r, userNames[r], tenants, tenant)
					}
				}
			})
		})
	}
}

// scimConcurrentStore opens the store a race runs on. The PostgreSQL store
// allows one connection per racing request, so every loser can hold a
// transaction open while the winner commits.
func scimConcurrentStore(engine store.Engine) harnessStoreOpener {
	if engine != store.EnginePostgres {
		return defaultHarnessStore
	}
	return func(t *testing.T) store.Store {
		t.Helper()
		dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		t.Cleanup(cancel)
		st, err := sqlstore.Open(ctx, store.Config{
			Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin,
			MaxConns: scimConcurrentCreates,
		}, nil)
		if err != nil {
			t.Fatalf("open postgres store: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if err := st.System(ctx, func(sys store.SystemScope) error {
			_, err := sys.EnsureSystemTenant(ctx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return st
	}
}

// raceSCIMCreates sends n creates of userName through the HTTP handler at the
// same moment and returns what each one was told.
func raceSCIMCreates(t *testing.T, h *harness, token, userName string, n int) []scimCreateAnswer {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"schemas": []string{scim.SchemaUser}, "userName": userName, "active": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := h.srv.Handler()
	start := make(chan struct{})
	answers := make([]scimCreateAnswer, n)
	var wg sync.WaitGroup
	for i := range answers {
		req := httptest.NewRequest(http.MethodPost, "/v1/scim/v2/Users", bytes.NewReader(body))
		req.Header.Set("Content-Type", scim.ContentType)
		req.Header.Set("Authorization", "Bearer "+token)
		req.RemoteAddr = "10.0.0.1:1234"
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			<-start
			handler.ServeHTTP(rec, req)
			var resource struct {
				ID       string `json:"id"`
				SCIMType string `json:"scimType"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &resource)
			answers[i] = scimCreateAnswer{code: rec.Code, id: resource.ID, scimType: resource.SCIMType, raw: rec.Body.String()}
		}()
	}
	close(start)
	wg.Wait()
	return answers
}

// readSCIMAddressState reads, in one view, the accounts holding address and the
// memberships of the account when exactly one holds it.
func readSCIMAddressState(t *testing.T, st store.Store, address string) scimAddressState {
	t.Helper()
	ctx := context.Background()
	var out scimAddressState
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		users, _, err := as.Users().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "email", Op: model.OpEq, Value: address},
		}, Limit: 10})
		if err != nil {
			return err
		}
		out.accounts = len(users)
		if len(users) != 1 {
			return nil
		}
		out.memberships, _, err = as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: users[0].ID.String()},
		}, Limit: 10})
		return err
	}); err != nil {
		t.Fatalf("read the accounts holding %s: %v", address, err)
	}
	return out
}

// scimCensus counts racing answers by status, in the form the census log line
// and the failure messages share.
func scimCensus(answers []scimCreateAnswer) string {
	var created, found, conflict, server, other int
	for _, a := range answers {
		switch {
		case a.code == http.StatusCreated:
			created++
		case a.code == http.StatusOK:
			found++
		case a.code == http.StatusConflict:
			conflict++
		case a.code >= http.StatusInternalServerError:
			server++
		default:
			other++
		}
	}
	return fmt.Sprintf("requests=%d 201=%d 200=%d 409=%d 5xx=%d other=%d",
		len(answers), created, found, conflict, server, other)
}
