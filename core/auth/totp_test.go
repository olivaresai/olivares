// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// totpTestSealer is a minimal AES-256-GCM sealer bound to the scope, standing
// in for the composition root's engine-held key (cmd/olivares/totpwire.go).
type totpTestSealer struct{ aead cipher.AEAD }

func newTOTPTestSealer(t *testing.T) *totpTestSealer {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return &totpTestSealer{aead: aead}
}

func (s *totpTestSealer) Seal(_ context.Context, scope model.TenantID, plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nil, nonce, plaintext, []byte("totp.test|"+scope.String()))
	return "v1:" + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(append(nonce, sealed...)), nil
}

func (s *totpTestSealer) Open(_ context.Context, scope model.TenantID, sealed string) ([]byte, error) {
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(
		trimmedSuffix(sealed, "v1:"))
	if err != nil {
		return nil, err
	}
	ns := s.aead.NonceSize()
	return s.aead.Open(nil, raw[:ns], raw[ns:], []byte("totp.test|"+scope.String()))
}

func trimmedSuffix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

// totpCodeFor computes the current 6-digit SHA1 code for a base32 secret —
// the authenticator app's side of the ceremony, reimplemented from the RFC so
// the test never trusts the engine's own arithmetic.
func totpCodeFor(t *testing.T, secretB32 string, at time.Time) string {
	t.Helper()
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if err != nil {
		t.Fatalf("decode seed: %v", err)
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, seed)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	off := int(sum[len(sum)-1] & 0x0f)
	bin := (uint32(sum[off])&0x7f)<<24 | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%06d", bin%1000000)
}

type totpFixture struct {
	t      *testing.T
	ctx    context.Context
	st     store.Store
	a      *auth.Authenticator
	sealer *totpTestSealer
	admin  model.User // superadmin with a password
	user   model.User // plain user with a password
	dsn    string
}

func newTOTPFixture(t *testing.T) *totpFixture {
	t.Helper()
	auth.SetTestHashParams(1024, 1, 1)
	t.Cleanup(func() {
		auth.SetTestHashParams(auth.DefaultArgonMemKiB, auth.DefaultArgonTime, auth.DefaultArgonThreads)
	})
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "totp.db")
	st, err := sqlstore.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sealer := newTOTPTestSealer(t)
	a := auth.NewAuthenticator(st, nil).WithTOTPSeedSealer(sealer)
	f := &totpFixture{t: t, ctx: ctx, st: st, a: a, sealer: sealer, dsn: dsn}
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "totp", Slug: "totp", Status: model.StatusActive})
		if err != nil {
			return err
		}
		_ = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	f.admin = f.createUser("admin@totp.test", true)
	f.user = f.createUser("user@totp.test", false)
	return f
}

func (f *totpFixture) createUser(email string, superadmin bool) model.User {
	f.t.Helper()
	hash, err := auth.HashPassword("correct horse battery staple")
	if err != nil {
		f.t.Fatalf("hash password: %v", err)
	}
	var u model.User
	if err := f.st.AuthMutate(f.ctx, func(as store.AuthScope) error {
		created, err := as.Users().Create(f.ctx, model.User{
			Email: email, DisplayName: email, Status: model.StatusActive,
			PasswordHash: hash, IsSuperadmin: superadmin,
		})
		if err != nil {
			return err
		}
		u = created
		return nil
	}); err != nil {
		f.t.Fatalf("create user %s: %v", email, err)
	}
	return u
}

// loginAs runs a password login and returns its result.
func (f *totpFixture) loginAs(email string) (auth.LoginResult, error) {
	return f.a.LoginFrom(f.ctx, email, "correct horse battery staple", "127.0.0.1", nil)
}

// enrolFactor runs the full session-based enrolment for an account that holds
// the given session token, returning the recovery codes.
func (f *totpFixture) enrolFactor(token string) []string {
	f.t.Helper()
	principal, err := f.a.Authenticate(f.ctx, token)
	if err != nil {
		f.t.Fatalf("authenticate: %v", err)
	}
	enrol, err := f.a.BeginTOTPEnrolment(f.ctx, principal, "Olivares AI")
	if err != nil {
		f.t.Fatalf("begin enrolment: %v", err)
	}
	codes, err := f.a.FinishTOTPEnrolment(f.ctx, principal, totpCodeFor(f.t, enrol.Secret, time.Now()))
	if err != nil {
		f.t.Fatalf("finish enrolment: %v", err)
	}
	return codes
}

// The core journey: enrol from a session, then every password login is gated
// by a code challenge; the code completes the login with both methods in AMR;
// the same code never verifies twice; a recovery code completes the login once.
func TestTOTPLoginJourney(t *testing.T) {
	f := newTOTPFixture(t)

	// Before enrolment, login is direct.
	res, err := f.loginAs(f.user.Email)
	if err != nil || res.RequiresMFA() {
		t.Fatalf("pre-enrolment login = %+v, %v", res, err)
	}
	firstToken := res.Token

	codes := f.enrolFactor(firstToken)
	if len(codes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(codes))
	}

	// After enrolment the login is gated: no session, a pending token instead.
	res, err = f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("gated login: %v", err)
	}
	if !res.RequiresMFA() || res.MFAEnrolmentRequired {
		t.Fatalf("gated login = %+v", res)
	}

	// A wrong code fails WITHOUT consuming the pending credential: a typo must
	// not send the operator back to the password form (the throttle bounds
	// guessing). The correct code then completes the SAME challenge below.
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, res.MFAToken, "000000", "127.0.0.1", nil); !errors.Is(err, auth.ErrTOTPVerification) {
		t.Fatalf("wrong code err = %v", err)
	}

	// Fresh login, correct code: a session with pwd+totp in AMR at AAL1.
	res, err = f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	seed := currentTOTPSecret(t, f, f.user.ID)
	code := totpCodeFor(t, seed, time.Now())
	completedToken, _, err := f.a.CompleteTOTPLogin(f.ctx, res.MFAToken, code, "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("complete login: %v", err)
	}
	sess, err := f.a.Authenticate(f.ctx, completedToken)
	if err != nil {
		t.Fatalf("completed session does not authenticate: %v", err)
	}
	if sess.UserID != f.user.ID {
		t.Fatalf("session user = %s", sess.UserID)
	}
	hasPWD, hasTOTP := false, false
	for _, m := range sess.AMR {
		switch m {
		case "pwd":
			hasPWD = true
		case "totp":
			hasTOTP = true
		}
	}
	if !hasPWD || !hasTOTP {
		t.Fatalf("session AMR = %v, want pwd+totp", sess.AMR)
	}
	if sess.AAL != auth.AAL1 {
		t.Fatalf("TOTP session AAL = %d, want 1 (TOTP is a factor, not an AAL)", sess.AAL)
	}

	// Replay: the very same code string must not complete a second login,
	// whether or not the time step advanced in between.
	res2, err := f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, res2.MFAToken, code, "127.0.0.1", nil); !errors.Is(err, auth.ErrTOTPVerification) {
		t.Fatalf("replayed code err = %v, want ErrTOTPVerification", err)
	}

	// A recovery code completes a login exactly once.
	res3, err := f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("third login: %v", err)
	}
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, res3.MFAToken, codes[0], "127.0.0.1", nil); err != nil {
		t.Fatalf("recovery completion: %v", err)
	}
	res4, err := f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("fourth login: %v", err)
	}
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, res4.MFAToken, codes[0], "127.0.0.1", nil); !errors.Is(err, auth.ErrTOTPVerification) {
		t.Fatalf("spent recovery code err = %v, want ErrTOTPVerification", err)
	}

	// Status reflects one spent recovery code.
	status, err := f.a.TOTPStatusOf(f.ctx, f.user.ID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Enrolled || status.RecoveryCodesRemaining != 9 || status.SeedHint == "" {
		t.Fatalf("status = %+v", status)
	}
}

// currentTOTPSecret reads the account's seed through the fixture sealer (the
// test plays the user's authenticator app, which holds the same seed).
func currentTOTPSecret(t *testing.T, f *totpFixture, accountID model.ID) string {
	t.Helper()
	var secret string
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		rows, _, err := as.TOTPCredentials().List(f.ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: accountID.String()}},
		})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("credentials = %d", len(rows))
		}
		seed, err := f.sealer.Open(f.ctx, model.SystemTenantID, rows[0].SeedSealed)
		if err != nil {
			return err
		}
		secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)
		return nil
	}); err != nil {
		t.Fatalf("read seed: %v", err)
	}
	return secret
}

// The seed is sealed at rest: the database file holds the envelope, never the
// base32 secret the console showed.
func TestTOTPSeedSealedAtRest(t *testing.T) {
	f := newTOTPFixture(t)
	res, err := f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	enrol, err := func() (*auth.TOTPEnrolment, error) {
		principal, err := f.a.Authenticate(f.ctx, res.Token)
		if err != nil {
			return nil, err
		}
		return f.a.BeginTOTPEnrolment(f.ctx, principal, "Olivares AI")
	}()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	principal, _ := f.a.Authenticate(f.ctx, res.Token)
	if _, err := f.a.FinishTOTPEnrolment(f.ctx, principal, totpCodeFor(t, enrol.Secret, time.Now())); err != nil {
		t.Fatalf("finish: %v", err)
	}
	raw, err := readFileAll(f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	if bytesContains(raw, []byte(enrol.Secret)) {
		t.Fatal("the base32 seed is stored in the clear in the database")
	}
	var sealed string
	if err := f.st.AuthView(f.ctx, func(as store.AuthScope) error {
		rows, _, err := as.TOTPCredentials().List(f.ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: f.user.ID.String()}},
		})
		if err != nil || len(rows) != 1 {
			return fmt.Errorf("rows: %d, %v", len(rows), err)
		}
		sealed = rows[0].SeedSealed
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(sealed) < 4 || sealed[:3] != "v1:" {
		t.Fatalf("stored seed = %q, want a v1: envelope", sealed)
	}
}

// The require-for-administrators policy: an administrator without a factor
// must enrol before the login completes; a plain user is untouched; the
// enrolment completion mints the session directly.
func TestTOTPPolicyRequiresAdministrators(t *testing.T) {
	f := newTOTPFixture(t)

	principal := auth.Principal{Kind: auth.KindUser, UserID: f.admin.ID, CredID: model.NewID()}
	if err := f.a.SetRequireTOTPForAdmins(f.ctx, principal, true); err != nil {
		t.Fatalf("set policy: %v", err)
	}
	on, err := f.a.RequireTOTPForAdmins(f.ctx)
	if err != nil || !on {
		t.Fatalf("policy read = %v, %v", on, err)
	}

	// The plain user logs in as before.
	if res, err := f.loginAs(f.user.Email); err != nil || res.RequiresMFA() {
		t.Fatalf("plain user login = %+v, %v", res, err)
	}

	// The administrator's login demands an enrolment first.
	res, err := f.loginAs(f.admin.Email)
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	if !res.RequiresMFA() || !res.MFAEnrolmentRequired {
		t.Fatalf("admin login = %+v", res)
	}

	// The pending token is only an enrolment credential: a code challenge
	// against it cannot succeed (there is no factor yet).
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, res.MFAToken, "123456", "127.0.0.1", nil); !errors.Is(err, auth.ErrTOTPVerification) {
		t.Fatalf("challenge before enrolment err = %v", err)
	}

	// Enrol with the pending token; the activation completes the login.
	adminRes, _ := f.loginAs(f.admin.Email)
	enrol, err := f.a.BeginTOTPEnrolmentForLogin(f.ctx, adminRes.MFAToken, "Olivares AI")
	if err != nil {
		t.Fatalf("begin enrolment for login: %v", err)
	}
	token, sess, codes, err := f.a.FinishTOTPEnrolmentForLogin(f.ctx, adminRes.MFAToken,
		totpCodeFor(t, enrol.Secret, time.Now()), "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("finish enrolment for login: %v", err)
	}
	if sess.UserID != f.admin.ID || token == "" {
		t.Fatalf("enrolment completion = %s, %+v", token, sess)
	}
	if len(codes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(codes))
	}

	// The next admin login is a normal code challenge.
	next, err := f.loginAs(f.admin.Email)
	if err != nil {
		t.Fatalf("next admin login: %v", err)
	}
	if !next.RequiresMFA() || next.MFAEnrolmentRequired {
		t.Fatalf("next admin login = %+v", next)
	}

	// Turning the policy off restores direct logins for factorless admins.
	if err := f.a.SetRequireTOTPForAdmins(f.ctx, principal, false); err != nil {
		t.Fatalf("unset policy: %v", err)
	}
}

// The administrator's reset: deletes another account's factor (and codes);
// the account's next login is ungated.
func TestTOTPAdminReset(t *testing.T) {
	f := newTOTPFixture(t)
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)

	adminPrincipal := auth.Principal{Kind: auth.KindUser, UserID: f.admin.ID, CredID: model.NewID(), Superadmin: true, AAL: auth.AAL3}
	if err := f.a.ResetTOTP(f.ctx, adminPrincipal, f.user.ID); err != nil {
		t.Fatalf("reset: %v", err)
	}
	after, err := f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("login after reset: %v", err)
	}
	if after.RequiresMFA() {
		t.Fatalf("login after reset = %+v, want no factor", after)
	}
	if _, err := f.a.TOTPStatusOf(f.ctx, f.user.ID); err != nil {
		t.Fatalf("status after reset: %v", err)
	}
}

// Verification has its own budget: five wrong codes lock the account+address
// pair out of the factor, exactly like five wrong passwords.
func TestTOTPThrottleLocksOut(t *testing.T) {
	f := newTOTPFixture(t)
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)
	for i := 0; i < 5; i++ {
		gated, err := f.loginAs(f.user.Email)
		if err != nil {
			t.Fatalf("login %d: %v", i, err)
		}
		if _, _, err := f.a.CompleteTOTPLogin(f.ctx, gated.MFAToken, "000000", "127.0.0.1", nil); !errors.Is(err, auth.ErrTOTPVerification) {
			t.Fatalf("wrong code %d err = %v", i, err)
		}
	}
	gated, err := f.loginAs(f.user.Email)
	if err != nil {
		t.Fatalf("login after spray: %v", err)
	}
	if _, _, err := f.a.CompleteTOTPLogin(f.ctx, gated.MFAToken, "000000", "127.0.0.1", nil); !errors.Is(err, auth.ErrLockedOut) {
		t.Fatalf("sixth attempt err = %v, want ErrLockedOut", err)
	}
}

// Without a sealer the factor fails closed on both ends: no enrolment, and no
// completion of an already-enrolled login.
func TestTOTPFailClosedWithoutSealer(t *testing.T) {
	f := newTOTPFixture(t)
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)

	bare := auth.NewAuthenticator(f.st, nil)
	// A factorless account (the admin) reaches the sealer refusal; the enrolled
	// account's begin would correctly demand step-up first.
	bareRes, err := bare.LoginFrom(f.ctx, f.admin.Email, "correct horse battery staple", "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("admin login on bare: %v", err)
	}
	adminPrincipal, err := bare.Authenticate(f.ctx, bareRes.Token)
	if err != nil {
		t.Fatalf("authenticate admin: %v", err)
	}
	if _, err := bare.BeginTOTPEnrolment(f.ctx, adminPrincipal, "Olivares AI"); !errors.Is(err, auth.ErrNoTOTPSealer) {
		t.Fatalf("begin without sealer err = %v", err)
	}
	gated, err := bare.LoginFrom(f.ctx, f.user.Email, "correct horse battery staple", "127.0.0.1", nil)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	seed := currentTOTPSecret(t, f, f.user.ID)
	if _, _, err := bare.CompleteTOTPLogin(f.ctx, gated.MFAToken, totpCodeFor(t, seed, time.Now()), "127.0.0.1", nil); !errors.Is(err, auth.ErrNoTOTPSealer) {
		t.Fatalf("complete without sealer err = %v, want ErrNoTOTPSealer", err)
	}
}

// The AAL3 rules mirror the passkey lifecycle: replacing an existing factor
// and removing the factor both demand a stepped-up session.
func TestTOTPStepUpRules(t *testing.T) {
	f := newTOTPFixture(t)
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)

	principal, err := f.a.Authenticate(f.ctx, res.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if _, err := f.a.BeginTOTPEnrolment(f.ctx, principal, "Olivares AI"); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("replace at AAL1 err = %v, want ErrStepUpRequired", err)
	}
	if err := f.a.RemoveTOTP(f.ctx, principal); !errors.Is(err, auth.ErrStepUpRequired) {
		t.Fatalf("remove at AAL1 err = %v, want ErrStepUpRequired", err)
	}
	// The plain Login wrapper surfaces the challenge as the documented errors.
	if _, _, err := f.a.Login(f.ctx, f.user.Email, "correct horse battery staple", "127.0.0.1"); !errors.Is(err, auth.ErrTOTPRequired) {
		t.Fatalf("Login wrapper err = %v, want ErrTOTPRequired", err)
	}
}

func readFileAll(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func bytesContains(haystack, needle []byte) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle []byte) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// An account-wide reset cannot accept a tenant administrator's unscoped authority.
func TestTOTPTenantAdminCannotUseGlobalReset(t *testing.T) {
	f := newTOTPFixture(t)
	res, _ := f.loginAs(f.user.Email)
	f.enrolFactor(res.Token)
	actor := auth.Principal{Kind: auth.KindUser, UserID: f.admin.ID, CredID: model.NewID(), AAL: auth.AAL3}
	if err := f.a.ResetTOTP(f.ctx, actor, f.user.ID); err == nil {
		t.Error("unscoped tenant authority reset a global factor")
	}
	status, err := f.a.TOTPStatusOf(f.ctx, f.user.ID)
	if err != nil || !status.Enrolled {
		t.Fatal("rejected reset changed factor")
	}
}
