// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type ceremonyClock struct{ now time.Time }

func (c *ceremonyClock) Now() model.Timestamp { return model.NewTimestamp(c.now) }

// The scheduling/failure seam wraps a real transaction. It does not supply a
// verified certificate, manufacture a row, or replace commit/rollback.
type ceremonySeamStore struct {
	store.Store
	before     func()
	beforeRead func()
	failAudit  bool
	failUpdate bool
}

func (s *ceremonySeamStore) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	if s.before != nil {
		s.before()
	}
	return s.Store.AuthMutate(ctx, func(as store.AuthScope) error {
		if s.failAudit || s.failUpdate {
			return fn(ceremonyScope{AuthScope: as, failAudit: s.failAudit, failUpdate: s.failUpdate})
		}
		return fn(as)
	})
}

func (s *ceremonySeamStore) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	if s.beforeRead != nil {
		s.beforeRead()
	}
	return s.Store.AuthView(ctx, fn)
}

type ceremonyScope struct {
	store.AuthScope
	failAudit  bool
	failUpdate bool
}

func (s ceremonyScope) Audit() store.AuditLog {
	if s.failAudit {
		return ceremonyAudit{AuditLog: s.AuthScope.Audit()}
	}
	return s.AuthScope.Audit()
}
func (s ceremonyScope) Sessions() store.Repository[model.AuthSession] {
	if s.failUpdate {
		return ceremonySessions{Repository: s.AuthScope.Sessions()}
	}
	return s.AuthScope.Sessions()
}

type ceremonySessions struct {
	store.Repository[model.AuthSession]
}

func (ceremonySessions) Update(context.Context, model.AuthSession) (model.AuthSession, error) {
	return model.AuthSession{}, errors.New("injected ceremony update failure")
}

type ceremonyAudit struct{ store.AuditLog }

func (ceremonyAudit) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	return model.AuditEvent{}, errors.New("injected ceremony audit failure")
}

func ceremonyCertificate(t *testing.T, email string, now time.Time) (*PIVConfig, []*x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test authority"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "test user"},
		EmailAddresses: []string{email}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err = x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	// No OCSP URL or network. The explicit laboratory policy permits unknown
	// revocation; chain, signature, time, usage and subject verification are real.
	return &PIVConfig{Roots: pool, AllowOCSPUnknown: true}, []*x509.Certificate{cert}
}

func TestAuthenticationCeremonyPersistsVerifiedInstantSQLite(t *testing.T) {
	ceremonyPersistence(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
}
func TestAuthenticationCeremonyPersistsVerifiedInstantPostgres(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skip("Postgres qualification requires its assigned CI database")
	}
	dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
	ceremonyPersistence(t, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin, MaxConns: 4})
}

func ceremonyPersistence(t *testing.T, cfg store.Config) {
	f := newPrincipalEvidenceFixtureConfig(t, cfg)
	var sibling model.AuthSession
	if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
		var err error
		sibling, err = as.Sessions().Create(f.ctx, model.AuthSession{
			UserID: f.user.ID, Selector: "unrelated-ceremony-session", SecretHash: []byte("test hash"),
			ExpiresAt: f.session.ExpiresAt, AAL: AAL1, AMR: []string{"pwd"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	instant := f.session.CreatedAt.Time().Add(time.Minute).Truncate(time.Second).Add(123456789 * time.Nanosecond)
	canonical := instant.UTC().Truncate(time.Microsecond)
	clock := &ceremonyClock{now: instant}
	wrapped := &ceremonySeamStore{Store: f.st, before: func() { clock.now = instant.Add(7 * time.Minute) }}
	a := NewAuthenticator(wrapped, clock)
	p, err := a.Authenticate(f.ctx, f.sessRaw)
	if err != nil {
		t.Fatal(err)
	}
	wrapped.beforeRead = func() { clock.now = instant.Add(6 * time.Minute) }
	config, peer := ceremonyCertificate(t, f.user.Email, instant)
	row, status, err := a.ElevatePIVSession(f.ctx, p, config, peer)
	if err != nil || !status.chainOK {
		t.Fatalf("verified PIV ceremony: %v", err)
	}
	if row.AALAuthenticatedAt == nil || !row.AALAuthenticatedAt.Time().Equal(canonical) ||
		row.AALExpiresAt == nil || !row.AALExpiresAt.Time().Equal(canonical.Add(StepUpTTL)) {
		t.Fatal("a wait changed the event, or UTC microsecond precision was not fixed at capture")
	}
	if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
		stored, err := as.Sessions().Get(f.ctx, row.ID)
		if err != nil {
			return err
		}
		if stored.AALAuthenticatedAt == nil || stored.AALAuthenticatedAt.String() != row.AALAuthenticatedAt.String() || stored.AAL != AAL3 {
			t.Fatal("verified witness did not survive repository reload")
		}
		other, err := as.Sessions().Get(f.ctx, sibling.ID)
		if err != nil {
			return err
		}
		if other.AAL != AAL1 || other.AALAuthenticatedAt != nil || other.AALExpiresAt != nil {
			t.Fatal("ceremony elevated a sibling session of the same user")
		}
		found := false
		if err := as.Audit().Walk(f.ctx, 1, func(e model.AuditEvent) error {
			if e.Action == "auth.stepup" && e.TargetID == row.ID {
				found = true
			}
			return nil
		}); err != nil {
			return err
		}
		if !found {
			t.Fatal("elevation has no committed audit")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wrapped.beforeRead, wrapped.before = nil, nil
	f.a, f.now = a, clock.now
	f.hooks.now = model.NewTimestamp(clock.now)
	_, evidence := freshnessResolved(t, f)
	if evidence.AAL != AAL3 || !evidence.AuthenticatedAt.Equal(canonical) || !evidence.ObservedAt.Equal(clock.now) {
		t.Fatal("resolver substituted its observation for the ceremony")
	}
}

func TestAuthenticationCeremonyRefusalAndAuditRollback(t *testing.T) {
	for _, test := range []string{"wrong subject", "untrusted certificate", "invalid signature", "audit failure", "update failure", "existing witness audit failure", "wrong session owner", "expired while waiting"} {
		t.Run(test, func(t *testing.T) {
			f := newPrincipalEvidenceFixture(t)
			instant := f.session.CreatedAt.Time().Add(time.Minute)
			clock := &ceremonyClock{now: instant}
			oldStamp := model.NewTimestamp(instant.Add(-30 * time.Second))
			oldExpiry := model.NewTimestamp(oldStamp.Time().Add(StepUpTTL))
			if test == "existing witness audit failure" {
				freshnessSession(t, f, func(s *model.AuthSession) {
					s.AAL, s.AMR, s.AALAuthenticatedAt, s.AALExpiresAt = AAL3, []string{"piv"}, &oldStamp, &oldExpiry
				})
			}
			wrapped := &ceremonySeamStore{Store: f.st,
				failAudit:  test == "audit failure" || test == "existing witness audit failure",
				failUpdate: test == "update failure"}
			if test == "expired while waiting" {
				wrapped.before = func() { clock.now = f.session.ExpiresAt.Time() }
			}
			a := NewAuthenticator(wrapped, clock)
			actor, err := a.Authenticate(f.ctx, f.sessRaw)
			if err != nil {
				t.Fatal(err)
			}
			email := f.user.Email
			if test == "wrong subject" {
				email = "other@example.test"
			}
			cfg, cert := ceremonyCertificate(t, email, instant)
			if test == "untrusted certificate" {
				cfg.Roots = x509.NewCertPool()
			}
			if test == "invalid signature" {
				cert[0].Signature[0] ^= 1
			}
			if test == "wrong session owner" {
				if err := f.raw.AuthMutate(f.ctx, func(as store.AuthScope) error {
					u, err := as.Users().Create(f.ctx, model.User{Email: "other@example.test", Status: model.StatusActive})
					actor.UserID = u.ID
					return err
				}); err != nil {
					t.Fatal(err)
				}
				cfg, cert = ceremonyCertificate(t, "other@example.test", instant)
			}
			if _, _, err := a.ElevatePIVSession(f.ctx, actor, cfg, cert); err == nil {
				t.Fatal("invalid or uncommitted ceremony succeeded")
			}
			if err := f.raw.AuthView(f.ctx, func(as store.AuthScope) error {
				s, err := as.Sessions().Get(f.ctx, f.session.ID)
				if err == nil {
					if test == "existing witness audit failure" {
						if s.AAL != AAL3 || s.AALAuthenticatedAt == nil || !s.AALAuthenticatedAt.Time().Equal(oldStamp.Time()) || s.AALExpiresAt == nil || !s.AALExpiresAt.Time().Equal(oldExpiry.Time()) {
							t.Fatal("failed renewal changed the previous ceremony")
						}
					} else if s.AAL != AAL1 || s.AALAuthenticatedAt != nil || s.AALExpiresAt != nil {
						t.Fatal("refusal left durable assurance")
					}
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
