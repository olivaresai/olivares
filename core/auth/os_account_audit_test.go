// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestOSAccountBindingConfiguredDegradeRefusesAdmissionAndRevoke(t *testing.T) {
	for _, stage := range []string{"begin", "intent", "revoke"} {
		t.Run(stage, func(t *testing.T) {
			f := newCredentialBindingFixture(t)
			n := &osAccountNative{uid: 1501, login: "native-degrade"}
			b := osBindings(f, n)
			admin := osAdminSession(f)
			subject, _, _ := f.session(f.userA, nil)
			var ceremony OSAccountCeremony
			if stage != "begin" {
				var err error
				ceremony, err = b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
				if err != nil {
					t.Fatal(err)
				}
				if stage == "revoke" {
					if _, err = b.Complete(f.deadline(), subject, ceremony.ID, []byte("secret")); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := f.st.Close(); err != nil {
				t.Fatal(err)
			}
			degraded, err := sqlstore.Open(f.ctx, store.Config{Engine: store.EngineSQLite, DSN: f.dsn, Debug: true,
				AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = degraded.Close() })
			// Retain the real ceremony owner while changing only the actual store's
			// configured budget. No fake Append result exercises this path.
			f.st, f.a.st = degraded, degraded
			before := n.checks
			switch stage {
			case "begin":
				ceremony, err = b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
				if !ceremony.ID.IsZero() {
					t.Fatal("dropped begin audit published a ceremony")
				}
			case "intent":
				_, err = b.Complete(f.deadline(), subject, ceremony.ID, []byte("secret"))
			case "revoke":
				err = b.Revoke(f.deadline(), admin, f.tenant, f.userA.ID)
			}
			if !errors.Is(err, ErrCredentialBindingUnavailable) || n.checks != before {
				t.Fatalf("degraded %s: err=%v PAM=%d, want unavailable and %d", stage, err, n.checks, before)
			}
			if stage == "revoke" {
				ref, _, err := b.Resolve(f.deadline(), n.uid, n.login)
				if err != nil || pinnedRevision(ref) != pinnedRevision(subject.credentialRef) {
					t.Fatalf("dropped revoke changed the current binding: %v", err)
				}
			}
		})
	}
}

// Keep the native transaction capabilities while intercepting only one audit
// action. All reads, writes, proof locks and rollback remain the real store's.
type osAccountAuditScope struct {
	configurationAuditScope
	store.AuthCredentialBindingScope
}

func TestOSAccountBindingAuditDropAndErrorRollback(t *testing.T) {
	stages := []struct{ name, action string }{
		{"begin", "auth.os_account.begin"}, {"intent", "auth.os_account.verify.intent"},
		{"create_admin", "auth.os_account.admin_authorized"}, {"create_subject", "auth.os_account.subject_control"},
		{"succeed_admin", "auth.os_account.admin_authorized"}, {"succeed_subject", "auth.os_account.subject_control"},
		{"refused", "auth.os_account.verify.refused"}, {"revoke", "auth.os_account.revoke"},
	}
	for _, stage := range stages {
		for _, failure := range []string{"drop", "error"} {
			t.Run(stage.name+"/"+failure, func(t *testing.T) {
				f := newCredentialBindingFixture(t)
				n := &osAccountNative{uid: 1502, login: "native-audit"}
				b := osBindings(f, n)
				admin := osAdminSession(f)
				subject, _, _ := f.session(f.userA, nil)
				begin := func() OSAccountCeremony {
					c, err := b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
					if err != nil {
						t.Fatal(err)
					}
					return c
				}
				if stage.name == "revoke" || stage.name == "succeed_admin" || stage.name == "succeed_subject" {
					if _, err := b.Complete(f.deadline(), subject, begin().ID, []byte("secret")); err != nil {
						t.Fatal(err)
					}
					if stage.name != "revoke" {
						subject, _, _ = f.session(f.userA, nil)
					}
				}
				var ceremony OSAccountCeremony
				if stage.name != "begin" && stage.name != "revoke" {
					ceremony = begin()
				}
				read := func() []model.CredentialBinding {
					var rows []model.CredentialBinding
					if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
						bindings, err := osAccountStore(as)
						if err != nil {
							return err
						}
						owners, err := bindings.OSAccountOwner(f.ctx, n.uid)
						if err != nil {
							return err
						}
						current, err := bindings.Current(f.ctx, f.tenant, CredentialBindingOSAccount, f.userA.ID)
						rows = append(owners, current...)
						return err
					}); err != nil {
						t.Fatal(err)
					}
					return rows
				}
				before := read()
				pamBefore, attempts := n.checks, 0
				unavailable := errors.New("OS account audit writer failed")
				f.a.st = configurationFaultStore{Store: f.st, wrap: func(as store.AuthScope) store.AuthScope {
					actual := as.Audit()
					return osAccountAuditScope{configurationAuditScope{as, as.(store.TransactionClock), as.(store.AuthTenantAuthorityBarrier),
						configurationFaultAudit{actual, func(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
							if draft.Action != stage.action {
								return actual.Append(ctx, draft)
							}
							attempts++
							if failure == "error" {
								return model.AuditEvent{}, unavailable
							}
							return model.AuditEvent{}, nil
						}}}, as.(store.AuthCredentialBindingScope)}
				}}
				n.accountDeny = stage.name == "refused"
				secret := []byte("secret")
				var err error
				switch stage.name {
				case "begin":
					ceremony, err = b.Begin(f.deadline(), admin, f.tenant, f.userA.ID, n.login)
					if !ceremony.ID.IsZero() {
						t.Fatal("missing begin evidence published a ceremony")
					}
				case "revoke":
					err = b.Revoke(f.deadline(), admin, f.tenant, f.userA.ID)
				default:
					_, err = b.Complete(f.deadline(), subject, ceremony.ID, secret)
					if !reflect.DeepEqual(secret, make([]byte, len(secret))) {
						t.Fatal("refused completion retained the password")
					}
				}
				if err == nil || attempts != 1 || (failure == "drop" && !errors.Is(err, ErrCredentialBindingUnavailable)) ||
					(failure == "error" && !errors.Is(err, unavailable)) {
					t.Fatalf("missing %s proof: err=%v attempts=%d", stage.action, err, attempts)
				}
				wantPAM := pamBefore + 1
				if stage.name == "begin" || stage.name == "intent" || stage.name == "revoke" {
					wantPAM = pamBefore
				}
				if n.checks != wantPAM || !reflect.DeepEqual(before, read()) {
					t.Fatalf("audit failure admitted PAM or changed binding: PAM=%d want=%d", n.checks, wantPAM)
				}
			})
		}
	}
}
