// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// erasureStoreOn opens a core-only store on engineName for the erasure adapter.
func erasureStoreOn(t *testing.T, engineName string) store.Store {
	t.Helper()
	if engineName != "postgres" {
		return erasureTestStore(t)
	}
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	st, err := coreengine.Open(ctx, store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 8,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		_, e := sys.EnsureSystemTenant(ctx)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestTenantErasureAnonymizesNoAccountWithoutCustodyEvidence: a tenant's erasure
// request always removes the tenant's own hold on the account, and never
// anonymizes a global account the tenant holds no custody evidence for. Until
// the deployment can prove that nothing outside the tenant relies on an account,
// not even custody evidence is enough: an account the tenant created keeps its
// global record too.
func TestTenantErasureAnonymizesNoAccountWithoutCustodyEvidence(t *testing.T) {
	for _, engineName := range consentEngines {
		t.Run(engineName, func(t *testing.T) {
			ctx := context.Background()
			st := erasureStoreOn(t, engineName)
			tenant := mkTenant(t, st, "erasure-t")
			a := &accountEraserAdapter{log: slog.Default()}
			a.use(st, auth.NewAuthenticator(st, nil))

			for _, tc := range []struct {
				name    string
				email   string
				custody model.CredentialCustody
			}{
				{"an account of unknown origin", "legacy@erasure.example", model.CustodyLegacy},
				{"an account the deployment created", "deployment@erasure.example", model.CustodyDeployment},
				{"an account the tenant created", "custodied@erasure.example", model.CustodyTenant},
			} {
				t.Run(tc.name, func(t *testing.T) {
					user := seedUser(t, st, tc.email, false, tenant)
					if tc.custody != model.CustodyLegacy {
						if err := st.AuthMutate(ctx, func(as store.AuthScope) error {
							u, err := as.Users().Get(ctx, user.ID)
							if err != nil {
								return err
							}
							u.CredentialCustody = tc.custody
							if tc.custody == model.CustodyTenant {
								u.CustodyTenantID = tenant
							}
							_, err = as.Users().Update(ctx, u)
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}
					out, err := a.EraseAccount(ctx, tenant, []string{tc.email}, "user:dpo", "user")
					if err != nil {
						t.Fatal(err)
					}
					if out.Erased != 0 {
						t.Errorf("the tenant's erasure anonymized %s: %+v", tc.name, out)
					}
					if !strings.Contains(out.Detail, "deployment's erasure ceremony") {
						t.Errorf("the receipt does not say the global record waits for the deployment: %q", out.Detail)
					}
					if err := st.AuthView(ctx, func(as store.AuthScope) error {
						u, err := as.Users().Get(ctx, user.ID)
						if err != nil {
							return err
						}
						if u.Email != tc.email || u.Status != model.StatusActive {
							t.Errorf("the global account was changed: %q %q", u.Email, u.Status)
						}
						ms, _, err := as.Memberships().List(ctx, model.Query{Filters: []model.Filter{
							eqFilter("user_id", user.ID.String()), eqFilter("target_tenant_id", tenant.String()),
						}})
						if err != nil {
							return err
						}
						if len(ms) != 0 {
							t.Errorf("the tenant's own membership survived its erasure")
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

// TestAnOffboardedUsersNamedGrantsStopMatchingInTheTenant: the exclusion an
// offboard writes refuses the account in the tenant before any grant, so a
// user-subject grant that named it stops matching at once, even for a principal
// that is no longer a member.

// TestTheCustodianReadmitsItsOwnOffboardedAccount: the tenant that created an
// account re-admits it after its retirement completes, and only then; another
// tenant cannot, and joins it only by consent.
func TestTheCustodianReadmitsItsOwnOffboardedAccount(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		user := e.onboard(e.tT, "custodied@consent.test", "viewer")
		e.scimDelete(e.tT, user)

		again := map[string]any{"email": "custodied@consent.test", "role": "viewer", "mode": "password", "password": "another-pass-1"}
		if r := e.do("POST", "/v1/onboard", e.admin, e.tT, again); r.code != http.StatusConflict || r.errorCode() != "retirement_pending" {
			t.Errorf("re-admission before the retirement completed = %d %s, want 409 retirement_pending", r.code, r.raw)
		}
		if e.memberOf(user, e.tT) {
			t.Fatalf("a refused re-admission created the membership")
		}

		e.runPump()
		rec, found := e.record(user, e.tT)
		if !found || rec.RetirementState != model.RetirementRetired {
			t.Fatalf("the retirement record after the pump = %+v (found %t), want retired", rec, found)
		}
		if r := e.do("POST", "/v1/onboard", e.admin, e.tT, again); r.code != http.StatusCreated {
			t.Errorf("the custodian's re-admission after retirement = %d %s, want 201", r.code, r.raw)
		}
		if !e.memberOf(user, e.tT) {
			t.Errorf("the custodian's re-admission created no membership")
		}
		if rec, _ := e.record(user, e.tT); rec.RetirementState != model.RetirementLifted {
			t.Errorf("the re-admitted account's record = %q, want lifted", rec.RetirementState)
		}
		sess := e.login("custodied@consent.test", consentMemberPassword)
		if code := e.actsIn(sess, e.tT); code != http.StatusOK {
			t.Errorf("the re-admitted account acting in its tenant = %d, want 200", code)
		}
		if r := e.do("POST", "/v1/onboard", e.admin, e.tB, again); r.code != http.StatusAccepted {
			t.Errorf("another tenant's onboarding of the custodied account = %d %s, want 202", r.code, r.raw)
		}
	})
}

// fakeSMTP is a minimal SMTP relay that records what it is handed.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	rcpt []string
	data []string
}

func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSMTP{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	reply := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	reply("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			reply("250 fake")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO"):
			addr := strings.Trim(strings.TrimSpace(line[len("RCPT TO:"):]), "<>")
			s.mu.Lock()
			s.rcpt = append(s.rcpt, strings.ToLower(addr))
			s.mu.Unlock()
			reply("250 ok")
		case cmd == "DATA":
			reply("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(l, "\r\n") == "." {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.data = append(s.data, b.String())
			s.mu.Unlock()
			reply("250 queued")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func (s *fakeSMTP) received() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.rcpt...), append([]string(nil), s.data...)
}

// writeNotifyConfig writes the operator's destination file with one email
// destination named invitations, scoped as tenants says (nil omits the key).
func writeNotifyConfig(t *testing.T, relay *fakeSMTP, tenants []string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(relay.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{
		"name": "invitations", "kind": "email",
		"config": map[string]string{
			"smtp_host": "127.0.0.1", "smtp_port": port, "from": "invitations@deployment.test",
			"insecure_no_tls": "true",
		},
	}
	if tenants != nil {
		spec["tenants"] = tenants
	}
	b, err := json.Marshal([]any{spec})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "notify.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// consoleOrigin is the console address the operator declares in these cases.
const consoleOrigin = "https://console.consent.test"

// TestATenantCannotRouteOrRedirectTheInvitationChannel: the invitation mailer
// delivers only through a destination the operator scoped to no tenant, only to
// the invited address, with a link to the console address the operator
// declared whatever host the inviting request names, and leaves nothing a tenant
// can see or replay.
func TestATenantCannotRouteOrRedirectTheInvitationChannel(t *testing.T) {
	for _, engineName := range consentEngines {
		t.Run(engineName, func(t *testing.T) {
			relay := startFakeSMTP(t)
			t.Setenv("OLIVARES_NOTIFY_CONFIG", writeNotifyConfig(t, relay, []string{}))
			t.Setenv("OLIVARES_INVITE_MAIL_DESTINATION", "invitations")
			e := bootConsentEstateAt(t, engineName, consoleOrigin)

			// The inviter names a host of its own, directly and as a forwarded host.
			r := e.doWith("POST", "/v1/onboard", e.admin, e.tT, map[string]any{
				"email": "invitee@mailer.test", "role": "viewer", "mode": "invite",
			}, map[string]string{
				"Host": "attacker.example", "X-Forwarded-Host": "attacker.example", "X-Forwarded-Proto": "https",
			})
			if r.code != http.StatusCreated {
				t.Fatalf("invite = %d %s, want 201", r.code, r.raw)
			}
			if strings.Contains(r.raw, "token") || strings.Contains(r.raw, "accept_url") {
				t.Errorf("the invitation response carries redemption material: %s", r.raw)
			}
			eventually(t, "the invitation mail", func() bool {
				rcpt, _ := relay.received()
				return len(rcpt) > 0
			})
			rcpt, data := relay.received()
			if len(rcpt) != 1 || rcpt[0] != "invitee@mailer.test" {
				t.Errorf("the invitation went to %v, want only the invited address", rcpt)
			}
			if len(data) != 1 || !strings.Contains(data[0], consoleOrigin+"/accept-invite#token=") {
				t.Errorf("the mailed invitation carries no link to the declared console address")
			}
			if len(data) == 1 && strings.Contains(data[0], "attacker.example") {
				t.Errorf("the mailed invitation links to the host the inviting request named")
			}

			// A tenant cannot address the destination, list it or replay it.
			dests := e.do("GET", "/v1/m/notify/destinations", e.admin, e.tT, nil)
			if strings.Contains(dests.raw, "invitations") {
				t.Errorf("a tenant sees the invitation destination: %s", dests.raw)
			}
			route := e.do("POST", "/v1/m/notify/routes", e.admin, e.tT, map[string]any{
				"name": "steal", "destination": "invitations", "enabled": true,
			})
			if route.code == http.StatusCreated {
				if id, _ := route.body["id"].(string); id != "" {
					if test := e.do("POST", "/v1/m/notify/routes/"+id+"/test", e.admin, e.tT, nil); test.code < 400 {
						t.Errorf("a tenant route delivered through the invitation destination: %d %s", test.code, test.raw)
					}
				}
			}
			outbox := e.do("GET", "/v1/m/notify/outbox", e.admin, e.tT, nil)
			if strings.Contains(outbox.raw, "invitee@mailer.test") || strings.Contains(outbox.raw, "invitations") {
				t.Errorf("the tenant outbox holds the invitation: %s", outbox.raw)
			}
			if rcpt, _ := relay.received(); len(rcpt) != 1 {
				t.Errorf("the relay received %d messages, want only the invitation", len(rcpt))
			}
		})
	}

	// A destination any tenant may address is refused as the invitation channel,
	// and so is a deployment that declared no console address to link to: the
	// invitation is refused before anything is written or sent.
	for _, tc := range []struct {
		name    string
		tenants []string
		console string
	}{
		{"unscoped destination", nil, consoleOrigin},
		{"no declared console address", []string{}, ""},
	} {
		for _, engineName := range consentEngines {
			t.Run(engineName+"/"+tc.name, func(t *testing.T) {
				relay := startFakeSMTP(t)
				t.Setenv("OLIVARES_NOTIFY_CONFIG", writeNotifyConfig(t, relay, tc.tenants))
				t.Setenv("OLIVARES_INVITE_MAIL_DESTINATION", "invitations")
				e := bootConsentEstateAt(t, engineName, tc.console)
				r := e.doWith("POST", "/v1/onboard", e.admin, e.tT, map[string]any{
					"email": "refused@mailer.test", "role": "viewer", "mode": "invite",
				}, map[string]string{"Host": "attacker.example", "X-Forwarded-Host": "attacker.example"})
				if r.code != http.StatusConflict || r.errorCode() != "invite_delivery_unavailable" {
					t.Errorf("invite = %d %s, want 409 invite_delivery_unavailable", r.code, r.raw)
				}
				if strings.Contains(r.raw, "token") || strings.Contains(r.raw, "attacker.example") {
					t.Errorf("the refusal carries redemption material or the requested host: %s", r.raw)
				}
				if _, found := e.userByEmail("refused@mailer.test"); found {
					t.Errorf("a refused invitation created the account")
				}
				if rcpt, _ := relay.received(); len(rcpt) != 0 {
					t.Errorf("an invitation went out: %v", rcpt)
				}
			})
		}
	}
}
