// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type federationAuthorityWriter interface {
	PutConfigIdPWithAuthority(context.Context, RouteMutationAuthorization, Request, string, FederationConfigInput, *int64, string) (FederationConfigView, error)
}

type secretAuthorityWriter interface {
	PutWithAuthority(context.Context, RouteMutationAuthorization, Request, string, string, string, string) (SecretView, error)
}

// This sealer exercises scoped ciphertext flow. The authority tests do not
// replace or qualify the deployment's encryption/key-custody implementation.
type configurationAuthoritySealer struct{}

func (configurationAuthoritySealer) Seal(_ context.Context, scope model.TenantID, value []byte) (string, error) {
	return scope.String() + ":" + base64.StdEncoding.EncodeToString(value), nil
}

func (configurationAuthoritySealer) Open(_ context.Context, scope model.TenantID, ciphertext string) ([]byte, error) {
	value, ok := strings.CutPrefix(ciphertext, scope.String()+":")
	if !ok {
		return nil, errors.New("fixture ciphertext scope mismatch")
	}
	return base64.StdEncoding.DecodeString(value)
}

type configurationAuthorityFixture struct {
	*principalEvidenceFixture
	fed *FederationService
	sec *SecretStore
}

func newConfigurationAuthorityFixture(t *testing.T) *configurationAuthorityFixture {
	t.Helper()
	f := newPrincipalEvidenceFixture(t)
	// The shared fixture's clock decorator is deliberately synthetic for other
	// tests. These protected writes use the actual store clock and producers.
	f.st.wrap = nil
	f.a = NewAuthenticator(f.raw, nil)
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		if err := prepareUserAuthorityWrite(f.ctx, as, f.user.ID); err != nil {
			return err
		}
		member, err := as.Memberships().Get(f.ctx, f.member.ID)
		if err != nil {
			return err
		}
		member.Role = RoleAdmin
		_, err = as.Memberships().Update(f.ctx, member)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return &configurationAuthorityFixture{principalEvidenceFixture: f,
		fed: NewFederationService(f.raw, configurationAuthoritySealer{}, nil, NoFederation{}, nil),
		sec: NewSecretStore(f.raw, configurationAuthoritySealer{})}
}

func (f *configurationAuthorityFixture) issue(t *testing.T, kind string, lifetime time.Duration) (RouteMutationAuthorization, Request) {
	t.Helper()
	caller, cancel := context.WithTimeout(f.ctx, lifetime)
	t.Cleanup(cancel)
	principal, err := f.a.ResolvePrincipalScope(caller, f.sessionRef(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	role, member := principal.RoleIn(f.tenant)
	if !member || role != RoleAdmin {
		t.Fatal("the actual principal has no tenant admin grant")
	}
	permission := Permission("installed:" + kind + ":admin")
	request := Request{Principal: principal, Tenant: f.tenant, Permission: permission, Resource: ResourceFor(permission)}
	// The installed native default authorizer consumes the real reconstructed
	// principal. No test permit, scoped contribution or policy proof is supplied.
	authority, err := NewAuthorizer(nil).AuthorizeRouteMutation(caller, request)
	if err != nil {
		t.Fatal(err)
	}
	return authority, request
}

func configurationCandidate() FederationConfigInput {
	return FederationConfigInput{Protocol: ProtocolOIDC, OIDCIssuer: "https://provider.example", OIDCClientID: "native-fixture", OIDCClientSecret: "native-config-secret-fixture", ClaimedDomains: []string{"corp.example"}}
}

func (f *configurationAuthorityFixture) put(t *testing.T, kind string, authority RouteMutationAuthorization, request Request, expected *int64, reason string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	if kind == "federation" {
		writer, ok := any(f.fed).(federationAuthorityWriter)
		if !ok {
			t.Fatal("native federation producer has no protected mutation variant")
		}
		_, err := writer.PutConfigIdPWithAuthority(ctx, authority, request, "corp", configurationCandidate(), expected, reason)
		return err
	}
	writer, ok := any(f.sec).(secretAuthorityWriter)
	if !ok {
		t.Fatal("native secret producer has no protected mutation variant")
	}
	_, err := writer.PutWithAuthority(ctx, authority, request, "credential", "native-config-secret-fixture", "fixture description", reason)
	return err
}

func (f *configurationAuthorityFixture) effectAudits(t *testing.T) ([]model.AuditEvent, []map[string]any) {
	t.Helper()
	var events []model.AuditEvent
	var metadata []map[string]any
	if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
		walker, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("actual ledger lacks canonical reader")
		}
		return walker.WalkCanonical(f.ctx, 1, func(event model.AuditEvent, canonical string, _ []byte) error {
			if event.Action != "federation.config.update" && event.Action != "secret.put" {
				return nil
			}
			var meta map[string]any
			if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
				return err
			}
			if strings.Contains(canonical, "native-config-secret-fixture") {
				t.Fatal("native effect audit disclosed submitted secret")
			}
			events = append(events, event)
			metadata = append(metadata, meta)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return events, metadata
}

func TestConfigurationAuthorityVariantsUseRealOwnerAndSingleReasonAudit(t *testing.T) {
	for _, kind := range []string{"federation", "secret"} {
		t.Run(kind, func(t *testing.T) {
			f := newConfigurationAuthorityFixture(t)
			authority, request := f.issue(t, kind, 5*time.Second)
			create := int64(0)
			if err := f.put(t, kind, authority, request, &create, "approved fixture revision"); err != nil {
				t.Fatal(err)
			}
			events, metadata := f.effectAudits(t)
			if len(events) != 1 || len(metadata) != 1 || metadata[0]["reason"] != "approved fixture revision" || events[0].Actor != "user:"+f.user.ID.String() || events[0].TargetID.IsZero() || events[0].Seq < 1 {
				t.Fatalf("actual native effect audit = %+v / %+v", events, metadata)
			}
			if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
				if kind == "federation" {
					row, err := as.FederationConfigs().Get(f.ctx, events[0].TargetID)
					if err == nil && (row.TargetTenantID != request.Tenant || row.Alias != "corp" || row.OIDCClientSecretSealed == "") {
						t.Fatal("native federation write changed owner or lost sealed credential")
					}
					return err
				}
				row, err := as.Secrets().Get(f.ctx, events[0].TargetID)
				if err == nil && (row.Scope != request.Tenant || row.Name != "credential" || row.ValueSealed == "") {
					t.Fatal("native secret write changed owner or lost ciphertext")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func (f *configurationAuthorityFixture) assertNoEffect(t *testing.T) {
	t.Helper()
	if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
		configs, _, err := as.FederationConfigs().List(f.ctx, model.Query{})
		if err != nil {
			return err
		}
		secrets, _, err := as.Secrets().List(f.ctx, model.Query{})
		if err != nil {
			return err
		}
		if len(configs) != 0 || len(secrets) != 0 {
			t.Fatalf("refused mutation left owner rows: configs=%d secrets=%d", len(configs), len(secrets))
		}
		domains, _, err := as.FederationDomainClaims().List(f.ctx, model.Query{})
		if err == nil && len(domains) != 0 {
			t.Fatal("refused mutation left derived domain claims")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	events, _ := f.effectAudits(t)
	if len(events) != 0 {
		t.Fatalf("refused mutation committed %d native effect audits", len(events))
	}
}

func TestConfigurationAuthorityRefusesChangedOrWithdrawnEvidence(t *testing.T) {
	for _, kind := range []string{"federation", "secret"} {
		for _, change := range []string{"missing", "tenant", "zero_tenant", "wildcard_tenant", "noncanonical_tenant", "principal", "resource", "permission", "read", "expired", "revoked", "disabled", "membership"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				f := newConfigurationAuthorityFixture(t)
				lifetime := 5 * time.Second
				if change == "expired" {
					lifetime = 250 * time.Millisecond
				}
				authority, request := f.issue(t, kind, lifetime)
				switch change {
				case "missing":
					authority = RouteMutationAuthorization{}
				case "tenant":
					request.Tenant = model.SystemTenantID
				case "zero_tenant":
					request.Tenant = ""
				case "wildcard_tenant":
					request.Tenant = "*"
				case "noncanonical_tenant":
					request.Tenant = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
				case "principal":
					request.Principal.UserID = model.NewID()
				case "resource":
					request.Resource.ID = model.NewID().String()
				case "permission":
					request.Permission = Permission("installed:" + kind + ":write")
				case "read":
					request.Permission = Permission("installed:" + kind + ":read")
					request.Resource = ResourceFor(request.Permission)
					var err error
					ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
					defer cancel()
					authority, err = NewAuthorizer(nil).AuthorizeRouteMutation(ctx, request)
					if err != nil {
						t.Fatal(err)
					}
				case "expired":
					metadata, err := authority.MetadataFor(engineInstant(t, f.principalEvidenceFixture), request)
					if err != nil {
						t.Fatal(err)
					}
					time.Sleep(time.Until(metadata.FreshUntil.Add(time.Millisecond)))
				case "revoked":
					if err := f.a.RevokeSession(f.ctx, request.Principal, f.session.ID); err != nil {
						t.Fatal(err)
					}
				case "disabled", "membership":
					if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
						if err := prepareUserAuthorityWrite(f.ctx, as, f.user.ID); err != nil {
							return err
						}
						if change == "membership" {
							return as.Memberships().Delete(f.ctx, f.member.ID)
						}
						user, err := as.Users().Get(f.ctx, f.user.ID)
						if err != nil {
							return err
						}
						user.Status = model.StatusInactive
						_, err = as.Users().Update(f.ctx, user)
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				if err := f.put(t, kind, authority, request, nil, "refusal fixture"); err == nil {
					t.Fatal("changed or withdrawn authority admitted a native effect")
				}
				f.assertNoEffect(t)
			})
		}
	}
}

func TestConfigurationAuthorityRequiresBoundedTextReason(t *testing.T) {
	invalid := []struct{ name, reason string }{
		{"missing", ""}, {"blank", "  "}, {"overlong", strings.Repeat("x", 513)},
		{"invalid_utf8", string([]byte{0xff})}, {"newline", "operator\nreason"},
		{"nul", "operator\x00reason"}, {"del", "operator\x7freason"}, {"unicode_control", "operator\u0085reason"},
	}
	for _, kind := range []string{"federation", "secret"} {
		t.Run(kind, func(t *testing.T) {
			f := newConfigurationAuthorityFixture(t)
			authority, request := f.issue(t, kind, 5*time.Second)
			for _, tc := range invalid {
				t.Run(tc.name, func(t *testing.T) {
					if err := f.put(t, kind, authority, request, nil, tc.reason); !errors.Is(err, ErrBadMutationReason) {
						t.Fatalf("invalid reason refusal = %v", err)
					}
					f.assertNoEffect(t)
				})
			}
			// The bound is bytes, and valid multi-byte textual reasons remain intact.
			reason := strings.Repeat("é", 256)
			if err := f.put(t, kind, authority, request, nil, reason); err != nil {
				t.Fatal(err)
			}
			_, metadata := f.effectAudits(t)
			if len(metadata) != 1 || metadata[0]["reason"] != reason {
				t.Fatal("valid boundary reason changed in native audit")
			}
		})
	}
}

// These wrappers remove optional capabilities or intercept the actual ledger
// inside the real owner's AuthMutate. They never manufacture authority or time.
type configurationFaultStore struct {
	store.Store
	wrap func(store.AuthScope) store.AuthScope
}

func (s configurationFaultStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error { return fn(s.wrap(as)) })
}

type configurationNoCapabilities struct{ store.AuthScope }
type configurationClockOnly struct {
	store.AuthScope
	clock store.TransactionClock
}

func (s configurationClockOnly) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

type configurationAuditScope struct {
	store.AuthScope
	clock   store.TransactionClock
	barrier store.AuthTenantAuthorityBarrier
	audit   store.AuditLog
}

func (s configurationAuditScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}
func (s configurationAuditScope) LockAuthTenantAuthority(ctx context.Context, tenant model.TenantID, bundle store.AuthoritySnapshotBundle) error {
	return s.barrier.LockAuthTenantAuthority(ctx, tenant, bundle)
}
func (s configurationAuditScope) Audit() store.AuditLog { return s.audit }

type configurationFaultAudit struct {
	store.AuditLog
	append func(context.Context, model.AuditDraft) (model.AuditEvent, error)
}

func (a configurationFaultAudit) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	return a.append(ctx, draft)
}

func TestConfigurationAuthorityUnavailableCollaboratorsRollback(t *testing.T) {
	unavailable := errors.New("actual effect audit unavailable")
	for _, kind := range []string{"federation", "secret"} {
		for _, failure := range []string{"clock", "barrier", "audit_error", "audit_dropped", "final_horizon"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				f := newConfigurationAuthorityFixture(t)
				lifetime := 5 * time.Second
				if failure == "final_horizon" {
					lifetime = 350 * time.Millisecond
				}
				authority, request := f.issue(t, kind, lifetime)
				metadata, err := authority.MetadataFor(engineInstant(t, f.principalEvidenceFixture), request)
				if err != nil {
					t.Fatal(err)
				}
				appended := 0
				wrapped := configurationFaultStore{Store: f.raw, wrap: func(as store.AuthScope) store.AuthScope {
					if failure == "clock" {
						return configurationNoCapabilities{as}
					}
					clock := as.(store.TransactionClock)
					if failure == "barrier" {
						return configurationClockOnly{as, clock}
					}
					actual := as.Audit()
					return configurationAuditScope{as, clock, as.(store.AuthTenantAuthorityBarrier), configurationFaultAudit{actual, func(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
						appended++
						if failure == "audit_error" {
							return model.AuditEvent{}, unavailable
						}
						if failure == "audit_dropped" {
							return model.AuditEvent{}, nil
						}
						event, err := actual.Append(ctx, draft)
						if err != nil {
							return event, err
						}
						time.Sleep(time.Until(metadata.FreshUntil.Add(time.Millisecond)))
						return event, nil
					}}}
				}}
				f.fed = NewFederationService(wrapped, configurationAuthoritySealer{}, nil, NoFederation{}, nil)
				f.sec = NewSecretStore(wrapped, configurationAuthoritySealer{})
				err = f.put(t, kind, authority, request, nil, "actual owner rollback fixture")
				if err == nil {
					t.Fatal("unavailable or expired native effect admitted")
				}
				if failure == "audit_error" && !errors.Is(err, unavailable) {
					t.Fatalf("native audit failure was hidden: %v", err)
				}
				if strings.HasPrefix(failure, "audit_") || failure == "final_horizon" {
					if appended != 1 {
						t.Fatalf("expected one actual provisional audit attempt, got %d (%v)", appended, err)
					}
				}
				f.assertNoEffect(t)
			})
		}
	}
}

func (f *configurationAuthorityFixture) federationRow(t *testing.T) model.FederationConfig {
	t.Helper()
	row, found, err := f.fed.loadConfigByAlias(f.ctx, f.tenant, "corp")
	if err != nil || !found {
		t.Fatalf("actual owner row missing: found=%v err=%v", found, err)
	}
	return row
}

func TestConfigurationAuthorityFederationExpectedVersion(t *testing.T) {
	f := newConfigurationAuthorityFixture(t)
	authority, request := f.issue(t, "federation", 5*time.Second)
	absent, missing, negative := int64(0), int64(1), int64(-1)
	if err := f.put(t, "federation", authority, request, &missing, "missing owner"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("positive expected version on missing owner = %v", err)
	}
	if err := f.put(t, "federation", authority, request, &negative, "invalid version"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("negative expected version = %v", err)
	}
	f.assertNoEffect(t)
	if err := f.put(t, "federation", authority, request, &absent, "create owner"); err != nil {
		t.Fatal(err)
	}
	initial := f.federationRow(t)
	wrong := initial.Version + 1
	for _, expected := range []*int64{&absent, &wrong} {
		if err := f.put(t, "federation", authority, request, expected, "stale owner"); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("owner version mismatch = %v", err)
		}
		if row := f.federationRow(t); row.Version != initial.Version {
			t.Fatal("CAS refusal changed owner version")
		}
		events, _ := f.effectAudits(t)
		if len(events) != 1 {
			t.Fatal("CAS refusal appended an effect audit")
		}
	}
	if err := f.put(t, "federation", authority, request, &initial.Version, "exact owner"); err != nil {
		t.Fatal(err)
	}
	if row := f.federationRow(t); row.Version != initial.Version+1 {
		t.Fatal("exact CAS did not advance owner once")
	}
	// nil deliberately preserves the legacy producer's ordinary optimistic update.
	if err := f.put(t, "federation", authority, request, nil, "ordinary OCC"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationAuthorityFederationSameCASTwoWritersOneWinner(t *testing.T) {
	f := newConfigurationAuthorityFixture(t)
	authority, request := f.issue(t, "federation", 5*time.Second)
	create := int64(0)
	if err := f.put(t, "federation", authority, request, &create, "initial owner"); err != nil {
		t.Fatal(err)
	}
	initial := f.federationRow(t)
	second, secondRequest := f.issue(t, "federation", 5*time.Second)
	start := make(chan struct{})
	var wait sync.WaitGroup
	errs := make([]error, 2)
	reasons := []string{"first competing revision", "second competing revision"}
	proofs := []RouteMutationAuthorization{authority, second}
	requests := []Request{request, secondRequest}
	for i := range errs {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			<-start
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			candidate := configurationCandidate()
			candidate.OIDCClientID = reasons[i]
			_, errs[i] = f.fed.PutConfigIdPWithAuthority(ctx, proofs[i], requests[i], "corp", candidate, &initial.Version, reasons[i])
		}(i)
	}
	close(start)
	wait.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatalf("two same-CAS writers committed: %v", errs)
			}
			winner = i
		} else if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("same-CAS loser = %v", err)
		}
	}
	if winner == -1 {
		t.Fatalf("no same-CAS winner: %v", errs)
	}
	row := f.federationRow(t)
	events, metadata := f.effectAudits(t)
	if row.Version != initial.Version+1 || row.OIDCClientID != reasons[winner] || len(events) != 2 || metadata[1]["reason"] != reasons[winner] {
		t.Fatalf("same-CAS owner/audit disagrees: owner=%+v audits=%+v", row, metadata)
	}
	// A later caller may load the newer row before Put. The original client CAS
	// still refuses under the owner lock; the outside read cannot upgrade it.
	if err := f.put(t, "federation", authority, request, &initial.Version, "late same-CAS retry"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("sequential stale CAS = %v", err)
	}
	events, _ = f.effectAudits(t)
	if len(events) != 2 {
		t.Fatal("stale retry appended native effect audit")
	}
}

func TestConfigurationAuthorityLegacyWritesKeepTheirAuditAndSecretSemantics(t *testing.T) {
	for _, kind := range []string{"federation", "secret"} {
		t.Run(kind, func(t *testing.T) {
			f := newConfigurationAuthorityFixture(t)
			_, request := f.issue(t, kind, 5*time.Second)
			if kind == "federation" {
				if _, err := f.fed.PutConfigIdP(f.ctx, request.Principal, f.tenant, "corp", configurationCandidate()); err != nil {
					t.Fatal(err)
				}
				initial := f.federationRow(t)
				candidate := configurationCandidate()
				candidate.OIDCClientSecret = ""
				if _, err := f.fed.PutConfigIdP(f.ctx, request.Principal, f.tenant, "corp", candidate); err != nil {
					t.Fatal(err)
				}
				if f.federationRow(t).OIDCClientSecretSealed != initial.OIDCClientSecretSealed {
					t.Fatal("legacy blank secret did not preserve ciphertext")
				}
			} else {
				if _, err := f.sec.Put(f.ctx, request.Principal, f.tenant, "credential", "native-config-secret-fixture", "initial"); err != nil {
					t.Fatal(err)
				}
				if _, err := f.sec.Put(f.ctx, request.Principal, f.tenant, "credential", "", "updated description"); err != nil {
					t.Fatal(err)
				}
				clear, err := f.sec.Resolve(f.ctx, f.tenant, "credential")
				if err != nil || string(clear) != "native-config-secret-fixture" {
					t.Fatalf("legacy blank secret changed actual value: %v", err)
				}
			}
			events, metadata := f.effectAudits(t)
			if len(events) != 2 {
				t.Fatalf("legacy effects = %d", len(events))
			}
			for _, meta := range metadata {
				if _, exists := meta["reason"]; exists {
					t.Fatal("legacy audit acquired protected-variant reason")
				}
			}
		})
	}
}

func TestConfigurationAuthorityMissingProducerOrSealerRefuses(t *testing.T) {
	for _, kind := range []string{"federation", "secret"} {
		for _, missing := range []string{"producer", "store", "sealer"} {
			t.Run(kind+"/"+missing, func(t *testing.T) {
				f := newConfigurationAuthorityFixture(t)
				authority, request := f.issue(t, kind, 5*time.Second)
				if kind == "federation" {
					switch missing {
					case "producer":
						f.fed = nil
					case "store":
						f.fed = NewFederationService(nil, configurationAuthoritySealer{}, nil, NoFederation{}, nil)
					case "sealer":
						f.fed = NewFederationService(f.raw, nil, nil, NoFederation{}, nil)
					}
				} else {
					switch missing {
					case "producer":
						f.sec = nil
					case "store":
						f.sec = NewSecretStore(nil, configurationAuthoritySealer{})
					case "sealer":
						f.sec = NewSecretStore(f.raw, nil)
					}
				}
				if err := f.put(t, kind, authority, request, nil, "unavailable producer fixture"); err == nil {
					t.Fatal("unavailable native producer or sealing collaborator admitted")
				}
				f.assertNoEffect(t)
			})
		}
	}
}
