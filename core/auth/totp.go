// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6238 TOTP defaults to HMAC-SHA1 for authenticator-app compatibility (keyed PRF, not a collision-resistant hash)
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"image/png"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TOTP second factor for local accounts (RFC 6238). Doctrine, so the shape of
// this file reads as deliberate:
//
//   - The seed is generated server-side, shown to the user once (QR + base32
//     secret), stored SEALED, and opened only to verify a code. The database
//     never holds raw key material — the same rule as the federation secrets.
//   - A password login that must satisfy a factor never mints a session. It
//     returns a short-lived, single-use pending credential ("olvm_...") bound
//     to the account; only a verified code (or a recovery code) completes the
//     login. Pending state is in-memory (the WebAuthn ceremony doctrine: a node
//     restart drops it and the operator simply logs in again).
//   - TOTP sits OUTSIDE the AAL algebra: the engine defines AAL1 and AAL3 only
//     (a phishing-resistant hardware ceremony), and route_policy rejects AAL 2
//     at boot. A TOTP-verified login is an ordinary AAL1 session whose AMR
//     records both methods ("pwd", "totp"); passkeys/PIV remain the only paths
//     to AAL3. TOTP adds a factor, it never replaces one.
//   - Verification attempts are throttled on the account AND the client
//     address, exactly like password attempts, with their own budget so a
//     locked-out password cannot also exhaust the factor's window.

// TOTPPendingTTL bounds both pending states: a login challenge and an
// enrolment ceremony. Mirrors the WebAuthn ceremony TTL.
// TOTPPendingTTL bounds both pending states: a login challenge and an
// enrolment ceremony. Mirrors the WebAuthn ceremony TTL.
const TOTPPendingTTL = 5 * time.Minute

// totpThrottleFails/totpThrottleWindow are the factor's own verification
// budget, counted separately from the password budget.
const (
	totpThrottleFails  = 5
	totpThrottleWindow = 15 * time.Minute
)

// TOTP defaults (RFC 6238 §5.2 interoperability profile; SHA1 is the one hash
// every authenticator app supports).
const (
	TOTPDefaultAlgorithm = "SHA1"
	TOTPDefaultDigits    = 6
	TOTPDefaultPeriod    = 30
	totpSeedBytes        = 20 // 160-bit seed, RFC 4226 §4 R6
	totpVerifySkew       = 1  // accept ±1 time step of clock drift
	totpRecoveryCodes    = 10
)

// PrefixMFA marks the pending-login credential a factor-gated password login
// returns. Like every credential only SHA-256(secret) is held, in memory, and
// it authorizes nothing but the TOTP endpoints of its own login.
const PrefixMFA = "olvm"

var totpB32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// TOTPSeedSealer seals/opens a TOTP seed at rest, bound to the auth partition's
// scope so a ciphertext cannot be replayed across scopes. The composition root
// implements it over an engine-held key (cmd/olivares); the core never sees key
// material. Same shape as the federation and eventing sealers, with its own
// purpose string so a TOTP seed can never be opened as an SSO secret.
type TOTPSeedSealer interface {
	Seal(ctx context.Context, scope model.TenantID, plaintext []byte) (string, error)
	Open(ctx context.Context, scope model.TenantID, sealed string) ([]byte, error)
}

// ErrNoTOTPSealer is returned when a seed must be sealed or opened but no
// sealer is wired. Enrolment and verification both fail closed on it: a factor
// nobody can open is a lockout, not a factor.
var ErrNoTOTPSealer = errors.New("auth: no TOTP seed sealer wired; cannot store or verify a seed")

var (
	// ErrTOTPRequired is what Login returns when the account has a confirmed
	// factor: the caller must collect a code and CompleteTOTPLogin.
	ErrTOTPRequired = errors.New("auth: a TOTP code is required to finish this login")
	// ErrTOTPEnrolmentRequired is what Login returns when the policy requires
	// administrators to hold a factor and the account has none: the caller must
	// run the enrolment before the login can complete.
	ErrTOTPEnrolmentRequired = errors.New("auth: TOTP enrolment is required before this login can complete")
	// ErrTOTPVerification is a code (or recovery code, or pending token) that
	// did not verify. It carries no detail on purpose — wrong code, unknown
	// pending token and absent factor are indistinguishable to the caller.
	ErrTOTPVerification = errors.New("auth: TOTP verification failed")
	// ErrTOTPNotEnrolled is returned by the finish/remove paths that need a
	// factor the account does not have.
	ErrTOTPNotEnrolled = errors.New("auth: no TOTP factor is enrolled for this account")
)

// --- RFC 4226 / 6238 primitives -----------------------------------------------

// totpHash returns the HMAC constructor for an algorithm name.
func totpHash(algorithm string) (func() hash.Hash, error) {
	switch algorithm {
	case "SHA1":
		return sha1.New, nil
	case "SHA256":
		return sha256.New, nil
	case "SHA512":
		return sha512.New, nil
	}
	return nil, fmt.Errorf("auth: unsupported TOTP algorithm %q", algorithm)
}

// hotp computes the RFC 4226 code for a counter.
func hotp(newHash func() hash.Hash, seed []byte, counter uint64, digits int) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(newHash, seed)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	// Dynamic truncation (RFC 4226 §5.4): low 4 bits of the last byte select
	// the 31-bit window, mask the sign bit, then take the low digits.
	offset := int(sum[len(sum)-1] & 0x0f)
	bin := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 | uint32(sum[offset+2])<<8 | uint32(sum[offset+3])
	const digitsTable = "0123456789"
	out := make([]byte, digits)
	for i := digits - 1; i >= 0; i-- {
		out[i] = digitsTable[bin%10]
		bin /= 10
	}
	return string(out)
}

// constantTimeCodeEquals compares two numeric code strings in constant time.
// Length differs only on malformed input, which reveals nothing about a code.
func constantTimeCodeEquals(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// verifyTOTP checks code against the seed at step, accepting ±skew time steps
// of drift. It returns the step the code matched (for replay bookkeeping) and
// whether it cleared lastUsed — a code from a step at or below the last
// verified one does not verify twice (RFC 6238 §5.2).
func verifyTOTP(newHash func() hash.Hash, seed []byte, code string, digits, period int, step int64, skew int64, lastUsed int64) (int64, bool) {
	if period <= 0 || digits <= 0 || len(code) != digits {
		return 0, false
	}
	// Highest candidate first, so a drifted-forward code records the step it
	// actually matched and maximally advances the replay window.
	for s := step + skew; s >= step-skew; s-- {
		if s <= lastUsed {
			break // older candidates cannot clear the window either
		}
		if constantTimeCodeEquals(code, hotp(newHash, seed, uint64(s), digits)) {
			return s, true
		}
	}
	return 0, false
}

// totpOtpauthURI builds the otpauth:// provisioning URI (the Key URI format)
// every authenticator app scans from the QR.
func totpOtpauthURI(issuer, accountName, secretB32, algorithm string, digits, period int) string {
	label := url.PathEscape(issuer + ":" + accountName)
	q := url.Values{}
	q.Set("secret", secretB32)
	q.Set("issuer", issuer)
	q.Set("algorithm", algorithm)
	q.Set("digits", fmt.Sprintf("%d", digits))
	q.Set("period", fmt.Sprintf("%d", period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// --- pending states (login challenge + enrolment ceremony) -------------------

// PrimaryLoginContinuation carries an EXTERNALLY verified primary login
// through its TOTP leg (the generic continuation seam). The local password
// path passes nil and behaves exactly as before; an installed external
// verifier supplies one, and the pending challenge then remembers the login's
// exact context instead of collapsing it to the account's custody scope.
//
// The completion REVALIDATES — never re-verifies: the original proof is never
// re-run, its horizon is never extended, and what is checked is the CURRENCY
// of everything the proof depended on.
type PrimaryLoginContinuation interface {
	// SessionScope is the tenant the completed session is confined to (the
	// zero value is account scope). It is used verbatim: the completion never
	// re-derives a scope from the account.
	SessionScope() model.TenantID
	// ProofHorizon bounds the original verification's freshness. The pending
	// challenge never outlives it.
	ProofHorizon() time.Time
	// RevalidateCurrent re-checks at completion time that the verifying
	// source is still the current one (slot and configuration generation),
	// that the grant it carried still holds, and that the account's authority
	// is intact. A refusal is deny-closed and the login ends.
	RevalidateCurrent(ctx context.Context, st store.Store) error
}

// SessionTxRevalidator is the OPTIONAL transactional half of a continuation:
// a continuation that also implements it gets its revalidation INSIDE the
// session-minting transaction — after the login capability is taken, before
// any factor activation or credential creation writes, and again after the
// native session and audit writes — so a withdrawal or expiry that lands
// between revalidation and issuance rolls the whole issuance back. The exact
// account id and scope are passed so a swapped account or scope mid-flight is
// itself a refusal. Continuations without it, and every local/OIDC/SAML path,
// are unchanged.
type SessionTxRevalidator interface {
	RevalidateSessionTx(ctx context.Context, as store.AuthScope, exactAccountID model.ID, exactScope model.TenantID) error
}

// txVerifierFor returns the in-transaction revalidation closure for a
// continuation, or nil when the continuation does not implement the optional
// half (nothing is consulted, nothing changes).
func txVerifierFor(ctx context.Context, cont PrimaryLoginContinuation, accountID model.ID, scope model.TenantID) func(store.AuthScope) error {
	rx, ok := cont.(SessionTxRevalidator)
	if !ok || rx == nil {
		return nil
	}
	return func(as store.AuthScope) error {
		return rx.RevalidateSessionTx(ctx, as, accountID, scope)
	}
}

// totpLoginPending is a primary-verified login waiting for its factor. Only
// the SHA-256 of the pending secret is held; the account id, the transport
// peer and the login's context carry forward what CompleteTOTPLogin needs to
// finish it. scope is the session scope the login selected (the local password
// path passes the account's custody scope); continuation is nil on that path
// and the external verifier's context otherwise.
type totpLoginPending struct {
	secretHash   []byte
	accountID    model.ID
	ip           string
	scope        model.TenantID
	continuation PrimaryLoginContinuation
	expires      time.Time
}

// totpEnrolPending is a seed generated for an enrolment that has not been
// confirmed by a code yet. The plaintext seed lives HERE, in memory, and never
// in the database: the stored row only appears once a code proved possession.
type totpEnrolPending struct {
	seed      []byte
	algorithm string
	digits    int
	period    int
	expires   time.Time
}

// totpPendingStore is the single-use, TTL-swept in-memory store for both
// pending states. take removes before use, so neither a login challenge nor an
// enrolment challenge can be replayed.
type totpPendingStore struct {
	mu     sync.Mutex
	logins map[string]*totpLoginPending
	enrol  map[string]*totpEnrolPending
	now    func() time.Time
}

func newTOTPPendingStore(now func() time.Time) *totpPendingStore {
	return &totpPendingStore{
		logins: make(map[string]*totpLoginPending),
		enrol:  make(map[string]*totpEnrolPending),
		now:    now,
	}
}

func (s *totpPendingStore) sweepLocked() {
	now := s.now()
	for k, v := range s.logins {
		if now.After(v.expires) {
			delete(s.logins, k)
		}
	}
	for k, v := range s.enrol {
		if now.After(v.expires) {
			delete(s.enrol, k)
		}
	}
}

func (s *totpPendingStore) putLogin(selector string, p *totpLoginPending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.logins[selector] = p
}

func (s *totpPendingStore) takeLogin(selector string) (*totpLoginPending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	p, ok := s.logins[selector]
	if ok {
		delete(s.logins, selector)
	}
	return p, ok
}

// getLogin reads a pending login WITHOUT consuming it, for the path that
// starts an enrolment off a still-usable login challenge.
func (s *totpPendingStore) getLogin(selector string) (*totpLoginPending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	p, ok := s.logins[selector]
	return p, ok
}

func (s *totpPendingStore) putEnrol(key string, p *totpEnrolPending) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.enrol[key] = p
}

func (s *totpPendingStore) takeEnrol(key string) (*totpEnrolPending, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	p, ok := s.enrol[key]
	if ok {
		delete(s.enrol, key)
	}
	return p, ok
}

// --- results ------------------------------------------------------------------

// LoginResult is the outcome of a password login attempt that may need a
// second factor: either a completed session (Token + Session) or a pending MFA
// challenge (MFAToken) the caller must satisfy. Exactly one of the two is set.
type LoginResult struct {
	// Token is the session credential when the login completed.
	Token string
	// Session is the minted session when the login completed.
	Session model.AuthSession
	// MFAToken is the opaque pending credential when the factor is required.
	MFAToken string
	// MFAEnrolmentRequired switches the challenge from "enter a code" to
	// "enrol a factor first": the policy requires administrators to hold one
	// and this account has none. The MFAToken then authorizes only enrolment.
	MFAEnrolmentRequired bool
}

// RequiresMFA reports whether the login is waiting on its second factor.
func (r LoginResult) RequiresMFA() bool { return r.MFAToken != "" }

// TOTPEnrolment is the provisioning material shown to the user exactly once:
// the otpauth URI (rendered as a QR), the base32 secret for manual entry, and
// the parameters for apps that need them spelled out.
type TOTPEnrolment struct {
	Secret    string `json:"secret"`
	URI       string `json:"uri"`
	Algorithm string `json:"algorithm"`
	Digits    int    `json:"digits"`
	Period    int    `json:"period"`
}

// TOTPStatus is the non-secret view of an account's factor: what it is, when
// it activated, and how many recovery codes remain. No key material, ever.
type TOTPStatus struct {
	Enrolled               bool      `json:"enrolled"`
	Algorithm              string    `json:"algorithm,omitempty"`
	Digits                 int       `json:"digits,omitempty"`
	Period                 int       `json:"period,omitempty"`
	SeedHint               string    `json:"seed_hint,omitempty"`
	ActivatedAt            time.Time `json:"activated_at,omitempty"`
	RecoveryCodesRemaining int       `json:"recovery_codes_remaining"`
}

// --- wiring -------------------------------------------------------------------

// WithTOTPSeedSealer wires the seed sealer once at boot. Without it enrolment
// and verification fail closed (ErrNoTOTPSealer), never silently skip.
func (a *Authenticator) WithTOTPSeedSealer(sealer TOTPSeedSealer) *Authenticator {
	a.totpSealer = sealer
	return a
}

func (a *Authenticator) totpStores() *totpPendingStore {
	a.totpOnce.Do(func() { a.totpPending = newTOTPPendingStore(a.nowFunc()) })
	return a.totpPending
}

// nowFunc adapts the injected clock (nil ⇒ system) for the pending stores.
func (a *Authenticator) nowFunc() func() time.Time {
	return func() time.Time {
		if a.clock == nil {
			return time.Now()
		}
		return a.clock.Now().Time()
	}
}

// --- the login gate ------------------------------------------------------------

// totpGate decides what a password-verified login owes its factor: nothing
// (false, false), a code challenge (true, false), or a forced enrolment
// (true, true). It reads the credential and the policy; a store error is
// returned verbatim (deny-closed).
func (a *Authenticator) totpGate(ctx context.Context, user model.User) (challenge bool, enrol bool, err error) {
	_, has, err := a.totpCredential(ctx, user.ID)
	if err != nil {
		return false, false, err
	}
	if has {
		return true, false, nil
	}
	// No factor: only the administrators-must-enrol policy can demand one.
	required, err := a.totpPolicyRequires(ctx, user)
	if err != nil {
		return false, false, err
	}
	if required {
		return true, true, nil
	}
	return false, false, nil
}

// totpCredential loads the account's confirmed factor, if any.
func (a *Authenticator) totpCredential(ctx context.Context, accountID model.ID) (model.TOTPCredential, bool, error) {
	var cred model.TOTPCredential
	var found bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.TOTPCredentials().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: accountID.String()}},
		})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			cred, found = rows[0], true
		}
		return nil
	})
	return cred, found, err
}

// totpPolicyRequires reports whether THIS user must hold a factor: the
// require-for-administrators policy is on and the account is an administrator
// (a superadmin, or an admin/owner in any tenant).
func (a *Authenticator) totpPolicyRequires(ctx context.Context, user model.User) (bool, error) {
	on, err := a.RequireTOTPForAdmins(ctx)
	if err != nil || !on {
		return false, err
	}
	return a.accountIsAdministrator(ctx, user)
}

func (a *Authenticator) accountIsAdministrator(ctx context.Context, user model.User) (bool, error) {
	if user.IsSuperadmin {
		return true, nil
	}
	admin := false
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Memberships().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: user.ID.String()}},
		})
		if err != nil {
			return err
		}
		for _, m := range rows {
			if m.Role == RoleAdmin || m.Role == RoleOwner {
				admin = true
				break
			}
		}
		return nil
	})
	return admin, err
}

// mintTOTPPending issues the single-use pending credential for a gated login,
// carrying the login's session scope and (for an externally verified primary
// login) its continuation. The pending entry NEVER outlives the original
// proof's horizon: the TTL is capped at it, so a challenge cannot extend what
// the primary verification bounded.
func (a *Authenticator) mintTOTPPending(accountID model.ID, ip string, scope model.TenantID, cont PrimaryLoginContinuation) (string, error) {
	cred, err := NewCredential(PrefixMFA)
	if err != nil {
		return "", err
	}
	expires := a.nowFunc()().Add(TOTPPendingTTL)
	if cont != nil {
		if horizon := cont.ProofHorizon(); !horizon.IsZero() && horizon.Before(expires) {
			expires = horizon
		}
	}
	a.totpStores().putLogin(cred.Selector, &totpLoginPending{
		secretHash:   cred.SecretHash,
		accountID:    accountID,
		ip:           ip,
		scope:        scope,
		continuation: cont,
		expires:      expires,
	})
	return cred.Token, nil
}

// --- completing a gated login ---------------------------------------------------

// CompleteTOTPLogin finishes a factor-gated login with a TOTP code or a
// recovery code. The pending credential is consumed before verification
// (single use — a login that fails its code restarts from the password); the
// replay-window advance (or the recovery-code spend) lands in the same
// transaction as the session, so a proof is never spent without a session and
// a session is never minted without spending the proof.
func (a *Authenticator) CompleteTOTPLogin(ctx context.Context, pendingToken, code, ip string, forwarded []string) (string, model.AuthSession, error) {
	code = ReformatCode(code)
	pending, selector, ok := a.consumeTOTPLoginSelector(pendingToken)
	if !ok {
		return "", model.AuthSession{}, ErrTOTPVerification
	}
	// A WRONG code re-arms the pending credential: a typo must not dump the
	// operator back at the password form. The verification throttle is the
	// bound on guessing (five failures lock the account+address pair), and the
	// entry stays consumed on every path that is not a plain wrong code.
	rearm := func() {
		// An external proof owns the original deadline, including the initial
		// pending TTL. A typo or lockout must not renew that authority. Local
		// password challenges retain their existing sliding retry window.
		if pending.continuation == nil {
			pending.expires = a.nowFunc()().Add(TOTPPendingTTL)
		}
		a.totpStores().putLogin(selector, pending)
	}
	accountKey := "totp:account:" + pending.accountID.String()
	addressKey := "ip:" + a.trustedLoginProxies.clientAddress(ip, forwarded)
	verdict, wait := a.totpThrottle.decide(accountKey, addressKey)
	if verdict == loginRefuse {
		rearm()
		return "", model.AuthSession{}, ErrLockedOut
	}
	outcome := loginAbandoned
	defer func() { a.totpThrottle.record(accountKey, addressKey, verdict, outcome) }()
	if verdict == loginDelay {
		if err := a.totpThrottle.waitOut(ctx, wait); err != nil {
			rearm()
			return "", model.AuthSession{}, err
		}
	}

	var user model.User
	var cred model.TOTPCredential
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, pending.accountID)
		if err != nil {
			return err
		}
		user = u
		rows, _, err := as.TOTPCredentials().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: pending.accountID.String()}},
		})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			cred = rows[0]
		}
		return nil
	}); err != nil {
		return "", model.AuthSession{}, err
	}
	if user.ID.IsZero() || user.Status != model.StatusActive || cred.ID.IsZero() {
		outcome = loginFailed
		a.recordTOTPFailure(ctx, pending.accountID, ip)
		return "", model.AuthSession{}, ErrTOTPVerification
	}

	activate, ok, err := a.totpCompletionActivate(ctx, user, cred, code)
	if err != nil {
		return "", model.AuthSession{}, err
	}
	if !ok {
		outcome = loginFailed
		a.recordTOTPFailure(ctx, pending.accountID, ip)
		rearm()
		return "", model.AuthSession{}, ErrTOTPVerification
	}

	// An externally verified primary login revalidates its context here, just
	// before the mint: the verifying source must still be current, the grant
	// must still hold, the account's authority must be intact. A refusal is
	// deny-closed and consumes the challenge — a withdrawn source is not a
	// typo. The local password path (continuation nil) skips this entirely.
	if pending.continuation != nil {
		if err := pending.continuation.RevalidateCurrent(ctx, a.st); err != nil {
			outcome = loginFailed
			return "", model.AuthSession{}, err
		}
	}

	// The mint records how the PRIMARY factor was verified: "pwd" for the
	// local password path, "external" for a login an installed verifier
	// proved — the AMR then reads the truth of the login, not the shape of
	// the completion.
	method := passwordLogin
	if pending.continuation != nil {
		method = externalLogin
	}
	token, sess, err := a.mintSession(ctx, &loginAttempt{ip: pending.ip}, user, pending.scope,
		"auth.login", method, []string{"totp"}, activate, txVerifierFor(ctx, pending.continuation, pending.accountID, pending.scope))
	if err != nil {
		return "", model.AuthSession{}, err
	}
	outcome = loginSucceeded
	return token, sess, nil
}

// consumeTOTPLoginSelector takes the pending credential (single use on
// success) after checking its secret, returning its selector so a wrong-code
// failure can re-arm it.
func (a *Authenticator) consumeTOTPLoginSelector(token string) (*totpLoginPending, string, bool) {
	prefix, selector, secret, ok := ParseToken(token)
	if !ok || prefix != PrefixMFA {
		return nil, "", false
	}
	pending, ok := a.totpStores().takeLogin(selector)
	if !ok || !SecretMatches(secret, pending.secretHash) {
		return nil, "", false
	}
	return pending, selector, true
}

// totpCompletionActivate verifies a code against the account's factor and, on
// success, returns the activate callback mintSession will run inside its
// transaction: advance the TOTP replay window, or spend the matched recovery
// code. A recovery-code completion is ledgered before the mint (the ledger
// write is best-effort by doctrine, the spend is not).
func (a *Authenticator) totpCompletionActivate(ctx context.Context, user model.User, cred model.TOTPCredential, code string) (func(store.AuthScope) error, bool, error) {
	now := a.nowFunc()()
	step := int64(now.Unix()) / int64(cred.Period)
	newHash, err := totpHash(cred.Algorithm)
	if err != nil {
		return nil, false, err
	}
	seed, err := a.openTOTPSeed(ctx, cred)
	if err != nil {
		return nil, false, err
	}
	if matched, ok := verifyTOTP(newHash, seed, code, cred.Digits, cred.Period, step, totpVerifySkew, cred.LastUsedStep); ok {
		usedStep := matched
		usedCred := cred
		return func(as store.AuthScope) error {
			usedCred.LastUsedStep = usedStep
			_, err := as.TOTPCredentials().Update(ctx, usedCred)
			return err
		}, true, nil
	}
	// Not a TOTP code: try the recovery codes before failing. The spend is
	// deferred into the mint transaction, like the replay-window advance.
	return a.spendTOTPRecoveryCode(ctx, user.ID, code)
}

// spendTOTPRecoveryCode finds the account's first unused recovery code that
// matches code (constant time) and returns the activate callback that spends
// it. ok is false when no unused code matches.
func (a *Authenticator) spendTOTPRecoveryCode(ctx context.Context, accountID model.ID, code string) (func(store.AuthScope) error, bool, error) {
	var rows []model.TOTPRecoveryCode
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		rows, _, err = as.TOTPRecoveryCodes().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: accountID.String()}},
		})
		return err
	}); err != nil {
		return nil, false, err
	}
	for _, row := range rows {
		if row.UsedAt != nil || !SecretMatches(code, row.CodeHash) {
			continue
		}
		spent := row
		usedAt := model.NewTimestamp(a.nowFunc()())
		a.auditTOTP(ctx, "user:"+accountID.String(), "auth.totp.recovery_used", model.Kind("core.totp_credential"), accountID, nil)
		return func(as store.AuthScope) error {
			spent.UsedAt = &usedAt
			_, err := as.TOTPRecoveryCodes().Update(ctx, spent)
			return err
		}, true, nil
	}
	return nil, false, nil
}

// recordTOTPFailure ledgers a failed factor attempt, exactly like a failed
// password: the account sees it, with no key material in the record.
func (a *Authenticator) recordTOTPFailure(ctx context.Context, accountID model.ID, ip string) {
	actor := "user:" + accountID.String()
	if aerr := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		return appendLoginFail(ctx, as, actor, ip)
	}); aerr != nil {
		a.log.Error("auth: recording failed TOTP attempt", "err", aerr)
	}
}

// openTOTPSeed opens the sealed seed, failing closed without a sealer.
func (a *Authenticator) openTOTPSeed(ctx context.Context, cred model.TOTPCredential) ([]byte, error) {
	if a.totpSealer == nil {
		return nil, ErrNoTOTPSealer
	}
	seed, err := a.totpSealer.Open(ctx, model.SystemTenantID, cred.SeedSealed)
	if err != nil {
		return nil, fmt.Errorf("auth: open TOTP seed: %w", err)
	}
	return seed, nil
}

// --- enrolment -----------------------------------------------------------------

// BeginTOTPEnrolment starts a self-service enrolment for the acting session's
// user: it generates a seed and holds it in memory pending confirmation. The
// binding rule mirrors the WebAuthn one — once the account HAS a factor,
// replacing it requires a fresh AAL3 session, so a stolen AAL1 password
// session cannot swap the victim's factor for the attacker's. The FIRST
// factor necessarily bootstraps from AAL1.
func (a *Authenticator) BeginTOTPEnrolment(ctx context.Context, actor Principal, issuer string) (*TOTPEnrolment, error) {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return nil, ErrUnauthenticated
	}
	user, has, err := a.totpUserCredential(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	if err := mayChangeAuthenticators(actor, user); err != nil {
		return nil, err
	}
	if has && !a.stepUpSatisfied(ctx, actor) {
		a.auditStepUpFailure(ctx, actor, "totp", "enrolment_step_up")
		return nil, ErrStepUpRequired
	}
	return a.newTOTPEnrolment(actor.CredID.String(), user, issuer)
}

// BeginTOTPEnrolmentForLogin starts the policy-forced enrolment of a pending
// login (the password is verified; the account owes its first factor). The
// pending credential authorizes this enrolment and nothing else.
func (a *Authenticator) BeginTOTPEnrolmentForLogin(ctx context.Context, pendingToken, issuer string) (*TOTPEnrolment, error) {
	prefix, selector, secret, ok := ParseToken(pendingToken)
	if !ok || prefix != PrefixMFA {
		return nil, ErrTOTPVerification
	}
	pending, ok := a.totpStores().getLogin(selector)
	if !ok || !SecretMatches(secret, pending.secretHash) {
		return nil, ErrTOTPVerification
	}
	// The login stays pending for its completion; the enrolment is keyed by it.
	user, _, err := a.totpUserCredential(ctx, pending.accountID)
	if err != nil {
		return nil, ErrTOTPVerification
	}
	if user.Status != model.StatusActive {
		return nil, ErrTOTPVerification
	}
	return a.newTOTPEnrolment(selector, user, issuer)
}

// totpUserCredential loads the user (store.ErrNotFound when the account does
// not exist) and whether a confirmed factor exists.
func (a *Authenticator) totpUserCredential(ctx context.Context, accountID model.ID) (model.User, bool, error) {
	var user model.User
	var has bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, accountID)
		if err != nil {
			return err
		}
		user = u
		rows, _, err := as.TOTPCredentials().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: accountID.String()}},
		})
		if err != nil {
			return err
		}
		has = len(rows) > 0
		return nil
	})
	return user, has, err
}

// newTOTPEnrolment generates the seed and parks it under key (a session
// credential id, or a pending-login selector).
func (a *Authenticator) newTOTPEnrolment(key string, user model.User, issuer string) (*TOTPEnrolment, error) {
	if a.totpSealer == nil {
		return nil, ErrNoTOTPSealer
	}
	if issuer == "" {
		issuer = "Olivares AI"
	}
	seed := make([]byte, totpSeedBytes)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("auth: read TOTP seed entropy: %w", err)
	}
	secret := totpB32.EncodeToString(seed)
	a.totpStores().putEnrol(key, &totpEnrolPending{
		seed:      seed,
		algorithm: TOTPDefaultAlgorithm,
		digits:    TOTPDefaultDigits,
		period:    TOTPDefaultPeriod,
		expires:   a.nowFunc()().Add(TOTPPendingTTL),
	})
	return &TOTPEnrolment{
		Secret:    secret,
		URI:       totpOtpauthURI(issuer, user.Email, secret, TOTPDefaultAlgorithm, TOTPDefaultDigits, TOTPDefaultPeriod),
		Algorithm: TOTPDefaultAlgorithm,
		Digits:    TOTPDefaultDigits,
		Period:    TOTPDefaultPeriod,
	}, nil
}

// FinishTOTPEnrolment confirms a self-service enrolment: the code must verify
// against the pending seed, and the confirmed factor plus fresh recovery codes
// replace any prior factor in ONE transaction. The recovery codes are returned
// exactly once; only their hashes are stored.
func (a *Authenticator) FinishTOTPEnrolment(ctx context.Context, actor Principal, code string) ([]string, error) {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return nil, ErrUnauthenticated
	}
	pending, ok := a.totpStores().takeEnrol(actor.CredID.String())
	if !ok {
		a.auditStepUpFailure(ctx, actor, "totp", "enrolment_challenge")
		return nil, ErrTOTPVerification
	}
	user, has, err := a.totpUserCredential(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	if err := mayChangeAuthenticators(actor, user); err != nil {
		return nil, err
	}
	if has && !a.stepUpSatisfied(ctx, actor) {
		a.auditStepUpFailure(ctx, actor, "totp", "enrolment_step_up")
		return nil, ErrStepUpRequired
	}
	return a.confirmTOTPEnrolment(ctx, user, pending, code)
}

// FinishTOTPEnrolmentForLogin confirms the policy-forced enrolment of a
// pending login and, because a verified code proves possession, completes the
// login in the same breath: the session is minted with AMR pwd+totp.
func (a *Authenticator) FinishTOTPEnrolmentForLogin(ctx context.Context, pendingToken, code, ip string, forwarded []string) (string, model.AuthSession, []string, error) {
	prefix, selector, secret, ok := ParseToken(pendingToken)
	if !ok || prefix != PrefixMFA {
		return "", model.AuthSession{}, nil, ErrTOTPVerification
	}
	pendingLogin, ok := a.totpStores().takeLogin(selector)
	if !ok || !SecretMatches(secret, pendingLogin.secretHash) {
		return "", model.AuthSession{}, nil, ErrTOTPVerification
	}
	enrol, ok := a.totpStores().takeEnrol(selector)
	if !ok {
		return "", model.AuthSession{}, nil, ErrTOTPVerification
	}
	user, _, err := a.totpUserCredential(ctx, pendingLogin.accountID)
	if err != nil || user.Status != model.StatusActive {
		return "", model.AuthSession{}, nil, ErrTOTPVerification
	}

	// Verify the code against the pending seed BEFORE any write.
	now := a.nowFunc()()
	newHash, err := totpHash(enrol.algorithm)
	if err != nil {
		return "", model.AuthSession{}, nil, err
	}
	step := int64(now.Unix()) / int64(enrol.period)
	matched, ok := verifyTOTP(newHash, enrol.seed, code, enrol.digits, enrol.period, step, totpVerifySkew, 0)
	if !ok {
		a.recordTOTPFailure(ctx, user.ID, ip)
		return "", model.AuthSession{}, nil, ErrTOTPVerification
	}

	// The policy-forced enrolment path revalidates the external context under
	// the same rule as a code challenge, and mints in the login's scope.
	if pendingLogin.continuation != nil {
		if err := pendingLogin.continuation.RevalidateCurrent(ctx, a.st); err != nil {
			return "", model.AuthSession{}, nil, err
		}
	}
	var codes []string
	activate := func(as store.AuthScope) error {
		minted, err := a.writeTOTPFactor(ctx, as, user, enrol, matched)
		if err == nil {
			codes = minted
		}
		return err
	}
	enrolMethod := passwordLogin
	if pendingLogin.continuation != nil {
		enrolMethod = externalLogin
	}
	token, sess, err := a.mintSession(ctx, &loginAttempt{ip: pendingLogin.ip}, user, pendingLogin.scope,
		"auth.login", enrolMethod, []string{"totp"}, activate, txVerifierFor(ctx, pendingLogin.continuation, pendingLogin.accountID, pendingLogin.scope))
	if err != nil {
		return "", model.AuthSession{}, nil, err
	}
	a.auditTOTPActivate(user, enrol)
	return token, sess, codes, nil
}

// confirmTOTPEnrolment is the session-path confirmation: verify, then replace
// the factor and codes transactionally.
func (a *Authenticator) confirmTOTPEnrolment(ctx context.Context, user model.User, pending *totpEnrolPending, code string) ([]string, error) {
	now := a.nowFunc()()
	newHash, err := totpHash(pending.algorithm)
	if err != nil {
		return nil, err
	}
	step := int64(now.Unix()) / int64(pending.period)
	_, ok := verifyTOTP(newHash, pending.seed, code, pending.digits, pending.period, step, totpVerifySkew, 0)
	if !ok {
		// No ledger, no throttle: the pending enrolment is consumed either way
		// (takeEnrol already ran), and every retry generates a FRESH seed, so a
		// wrong activation code cannot be brute-forced — each attempt restarts
		// against different key material.
		return nil, ErrTOTPVerification
	}
	var codes []string
	err = a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		var err error
		// The activation itself does NOT advance the replay window: it is a
		// proof of possession, not a login, and recording its step would refuse
		// the very code the user's app shows for the current window at their
		// next login. The first LOGIN verification records the first step.
		codes, err = a.writeTOTPFactor(ctx, as, user, pending, 0)
		return err
	})
	if err != nil {
		return nil, err
	}
	a.auditTOTPActivate(user, pending)
	return codes, nil
}

// writeTOTPFactor persists the confirmed factor and fresh recovery codes,
// replacing any prior factor, inside the caller's auth transaction. It returns
// the plaintext recovery codes for their one and only display.
func (a *Authenticator) writeTOTPFactor(ctx context.Context, as store.AuthScope, user model.User, pending *totpEnrolPending, usedStep int64) ([]string, error) {
	if a.totpSealer == nil {
		return nil, ErrNoTOTPSealer
	}
	sealed, err := a.totpSealer.Seal(ctx, model.SystemTenantID, pending.seed)
	if err != nil {
		return nil, fmt.Errorf("auth: seal TOTP seed: %w", err)
	}
	// Replace any prior factor and its codes (one factor per account).
	old, _, err := as.TOTPCredentials().List(ctx, model.Query{
		Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: user.ID.String()}},
	})
	if err != nil {
		return nil, err
	}
	for _, row := range old {
		if err := as.TOTPCredentials().Delete(ctx, row.ID); err != nil {
			return nil, err
		}
	}
	oldCodes, _, err := as.TOTPRecoveryCodes().List(ctx, model.Query{
		Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: user.ID.String()}},
	})
	if err != nil {
		return nil, err
	}
	for _, row := range oldCodes {
		if err := as.TOTPRecoveryCodes().Delete(ctx, row.ID); err != nil {
			return nil, err
		}
	}
	activated := model.NewTimestamp(a.nowFunc()())
	if _, err := as.TOTPCredentials().Create(ctx, model.TOTPCredential{
		AccountID:    user.ID,
		SeedSealed:   sealed,
		SeedHint:     fingerprint(totpB32.EncodeToString(pending.seed)),
		Algorithm:    pending.algorithm,
		Digits:       pending.digits,
		Period:       pending.period,
		ConfirmedAt:  &activated,
		LastUsedStep: usedStep,
	}); err != nil {
		return nil, err
	}
	raw, err := newTOTPRecoveryCodes()
	if err != nil {
		return nil, err
	}
	for _, c := range raw {
		if _, err := as.TOTPRecoveryCodes().Create(ctx, model.TOTPRecoveryCode{
			AccountID: user.ID, CodeHash: hashSecret(c),
		}); err != nil {
			return nil, err
		}
	}
	display := make([]string, len(raw))
	for i, c := range raw {
		display[i] = displayRecoveryCode(c)
	}
	return display, nil
}

// auditTOTPActivate ledgers an activation AFTER the transaction that wrote the
// factor committed. It must not run inside a Mutate: the audit append is its
// own transaction, and nesting it deadlocks the single-writer engine.
func (a *Authenticator) auditTOTPActivate(user model.User, pending *totpEnrolPending) {
	a.auditTOTP(context.Background(), "user:"+user.ID.String(), "auth.totp.activate",
		model.Kind("core.totp_credential"), user.ID, map[string]any{
			"algorithm": pending.algorithm, "digits": pending.digits, "period": pending.period,
		})
}

// newTOTPRecoveryCodes mints the single-use recovery codes as 24-char base32
// strings (15 bytes of entropy). Dashes are display-only: displayRecoveryCode
// adds them for reading aloud, every entry path strips them (ReformatCode), and
// the stored hash is of the RAW string.
func newTOTPRecoveryCodes() ([]string, error) {
	codes := make([]string, 0, totpRecoveryCodes)
	for i := 0; i < totpRecoveryCodes; i++ {
		buf := make([]byte, 15)
		if _, err := rand.Read(buf); err != nil {
			return nil, fmt.Errorf("auth: read recovery-code entropy: %w", err)
		}
		codes = append(codes, totpB32.EncodeToString(buf))
	}
	return codes, nil
}

// displayRecoveryCode formats a raw recovery code in four groups of six.
func displayRecoveryCode(raw string) string {
	if len(raw) != 24 {
		return raw
	}
	return raw[0:6] + "-" + raw[6:12] + "-" + raw[12:18] + "-" + raw[18:24]
}

// --- status, self-removal, admin reset -----------------------------------------

// TOTPStatusOf reports the account's factor status (own account by default).
func (a *Authenticator) TOTPStatusOf(ctx context.Context, accountID model.ID) (TOTPStatus, error) {
	return a.totpStatusChecked(ctx, accountID, nil)
}

// TOTPStatusInTenant reports a member's factor only after checking membership
// in the same auth view. The API supplies membership:read authority.
func (a *Authenticator) TOTPStatusInTenant(ctx context.Context, tenant model.TenantID, accountID model.ID) (TOTPStatus, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return TOTPStatus{}, ErrInvalidToken
	}
	return a.totpStatusChecked(ctx, accountID, func(as store.AuthScope) error {
		if _, member, err := membershipOf(ctx, as, accountID, tenant); err != nil {
			return err
		} else if !member {
			return store.ErrNotFound
		}
		return nil
	})
}

func (a *Authenticator) totpStatusChecked(ctx context.Context, accountID model.ID, authorize func(store.AuthScope) error) (TOTPStatus, error) {
	var status TOTPStatus
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		if authorize != nil {
			if err := authorize(as); err != nil {
				return err
			}
		}
		rows, _, err := as.TOTPCredentials().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: accountID.String()}},
		})
		if err != nil {
			return err
		}
		codes, _, err := as.TOTPRecoveryCodes().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: accountID.String()}},
		})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			c := rows[0]
			status.Enrolled = true
			status.Algorithm = c.Algorithm
			status.Digits = c.Digits
			status.Period = c.Period
			status.SeedHint = c.SeedHint
			if c.ConfirmedAt != nil {
				status.ActivatedAt = c.ConfirmedAt.Time()
			}
		}
		for _, c := range codes {
			if c.UsedAt == nil {
				status.RecoveryCodesRemaining++
			}
		}
		return nil
	})
	return status, err
}

// RemoveTOTP deletes the acting user's own factor (and its recovery codes).
// Like removing a passkey it requires AAL3: dropping the second factor from a
// plain password session would let a stolen cookie weaken the account.
func (a *Authenticator) RemoveTOTP(ctx context.Context, actor Principal) error {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return ErrUnauthenticated
	}
	if !a.stepUpSatisfied(ctx, actor) {
		a.auditStepUpFailure(ctx, actor, "totp", "removal_step_up")
		return ErrStepUpRequired
	}
	_, has, err := a.totpUserCredential(ctx, actor.UserID)
	if err != nil {
		return err
	}
	if !has {
		return ErrTOTPNotEnrolled
	}
	return a.deleteTOTPFactor(ctx, actor.Actor(), actor.UserID)
}

// ResetTOTP is the deployment administrator's account-wide recovery path.
// Tenant administrators must use ResetTOTPInTenant with their selected tenant.
// Both paths require AAL3; an unscoped call needs global superadmin authority.
func (a *Authenticator) ResetTOTP(ctx context.Context, actor Principal, accountID model.ID) error {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return ErrUnauthenticated
	}
	if !a.stepUpSatisfied(ctx, actor) {
		return ErrStepUpRequired
	}
	if !actor.Superadmin || !actor.SessionScope().IsZero() {
		return ErrNotTenantGoverned
	}
	_, found, err := a.loadUserByID(ctx, accountID)
	if err != nil {
		return err
	}
	if !found {
		return store.ErrNotFound
	}
	return a.deleteTOTPFactor(ctx, actor.Actor(), accountID)
}

// ResetTOTPInTenant resets a member's account-wide factor only while this
// tenant alone governs the account. Membership and custody are checked under
// the native directory writer lock in the same transaction as the deletion.
// The API supplies membership:write authority; this backstop also requires a
// current AAL3 session and refuses a workspace-confined caller.
func (a *Authenticator) ResetTOTPInTenant(ctx context.Context, actor Principal, tenant model.TenantID, accountID model.ID) error {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return ErrUnauthenticated
	}
	if !a.stepUpSatisfied(ctx, actor) {
		return ErrStepUpRequired
	}
	if tenant.IsZero() || tenant.IsSystem() {
		return ErrInvalidToken
	}
	if _, confined := actor.ConfinedWorkspaceIn(tenant); confined {
		return ErrWorkspaceConfined
	}
	return a.deleteTOTPFactorChecked(ctx, actor.Actor(), accountID, func(as store.AuthScope) error {
		if err := prepareUserAuthorityWrite(ctx, as, accountID); err != nil {
			return err
		}
		if _, member, err := membershipOf(ctx, as, accountID, tenant); err != nil {
			return err
		} else if !member {
			return store.ErrNotFound
		}
		user, err := as.Users().Get(ctx, accountID)
		if err != nil {
			return err
		}
		if actor.Superadmin && actor.SessionScope().IsZero() {
			return nil
		}
		governed, err := tenantGoverned(ctx, as, user, tenant)
		if err != nil {
			return err
		}
		if !governed {
			return ErrNotTenantGoverned
		}
		return nil
	})
}

// loadUserByID reads one user by id (existence check for admin paths).
func (a *Authenticator) loadUserByID(ctx context.Context, accountID model.ID) (model.User, bool, error) {
	var user model.User
	var found bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, accountID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		user, found = u, true
		return nil
	})
	return user, found, err
}

// deleteTOTPFactor removes the factor and its codes transactionally and
// ledgers the removal. actor is the audit actor string ("user:<id>").
func (a *Authenticator) deleteTOTPFactor(ctx context.Context, actor string, accountID model.ID) error {
	return a.deleteTOTPFactorChecked(ctx, actor, accountID, nil)
}

func (a *Authenticator) deleteTOTPFactorChecked(ctx context.Context, actor string, accountID model.ID, authorize func(store.AuthScope) error) error {
	deleted := false
	err := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if authorize != nil {
			if err := authorize(as); err != nil {
				return err
			}
		}
		rows, _, err := as.TOTPCredentials().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: accountID.String()}},
		})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := as.TOTPCredentials().Delete(ctx, row.ID); err != nil {
				return err
			}
			deleted = true
		}
		codes, _, err := as.TOTPRecoveryCodes().List(ctx, model.Query{
			Filters: []model.Filter{{Column: "account_id", Op: model.OpEq, Value: accountID.String()}},
		})
		if err != nil {
			return err
		}
		for _, row := range codes {
			if err := as.TOTPRecoveryCodes().Delete(ctx, row.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if deleted {
		a.auditTOTP(ctx, actor, "auth.totp.reset", model.Kind("core.totp_credential"), accountID, nil)
	}
	return nil
}

// --- the require-for-administrators policy --------------------------------------

// RequireTOTPForAdmins reads the deployment-wide policy from the auth-policy
// singleton. An absent row is the default: the policy is off. Unlike the SSO
// posture knobs (stored openly, enforced by the closed engine), this one is
// enforced by THIS engine — basic account security is Community scope.
func (a *Authenticator) RequireTOTPForAdmins(ctx context.Context) (bool, error) {
	var on bool
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.AuthPolicy().List(ctx, model.Query{Filters: []model.Filter{}})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			on = rows[0].RequireTOTPAdmins
		}
		return nil
	})
	return on, err
}

// SetRequireTOTPForAdmins sets the deployment-wide policy and ledgers the
// change. actor is the audit actor of the setting administrator.
func (a *Authenticator) SetRequireTOTPForAdmins(ctx context.Context, actor Principal, on bool) error {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return ErrUnauthenticated
	}
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.AuthPolicy().List(ctx, model.Query{Filters: []model.Filter{}})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			if _, err := as.AuthPolicy().Create(ctx, model.AuthPolicy{RequireTOTPAdmins: on}); err != nil {
				return err
			}
		} else {
			row := rows[0]
			row.RequireTOTPAdmins = on
			if _, err := as.AuthPolicy().Update(ctx, row); err != nil {
				return err
			}
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "auth.totp.policy", TargetKind: "core.auth_policy",
			Meta: map[string]any{"require_for_admins": on},
		})
		return err
	})
}

// auditTOTP is the factor's ledger helper: semantic events with the acting
// principal, no key material, ids only.
func (a *Authenticator) auditTOTP(ctx context.Context, actor, action string, targetKind model.Kind, targetID model.ID, meta map[string]any) {
	if aerr := a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor, ActorKind: model.ActorUser, Action: action,
			TargetKind: targetKind, TargetID: targetID, Meta: meta,
		})
		return err
	}); aerr != nil {
		a.log.Error("auth: recording TOTP audit event", "err", aerr, "action", action)
	}
}

// ReformatCode normalizes a user-entered code: strips the spaces authenticator
// apps' autofill sometimes adds and the group dashes recovery codes use.
func ReformatCode(code string) string {
	return strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(code))
}

// TOTPQRPNG renders an otpauth URI as a square PNG the console shows beside
// the manual-entry secret. Medium error correction keeps a phone photograph of
// a screen scannable; the default size fits the identity card at 390 px.
func TOTPQRPNG(uri string, size int) ([]byte, error) {
	if size <= 0 {
		size = 240
	}
	code, err := qr.Encode(uri, qr.M, qr.Auto)
	if err != nil {
		return nil, fmt.Errorf("auth: encode TOTP QR: %w", err)
	}
	scaled, err := barcode.Scale(code, size, size)
	if err != nil {
		return nil, fmt.Errorf("auth: scale TOTP QR: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return nil, fmt.Errorf("auth: render TOTP QR: %w", err)
	}
	return buf.Bytes(), nil
}

// CompleteExternalLogin finishes a login whose PRIMARY factor an installed
// external verifier already proved — outside the password and federation
// paths — and which now owes the account's TOTP leg (the continuation seam's
// entry point). The caller has ALREADY prepared (committed) the account: the
// engine never re-verifies the external source here, and the gate decides only
// the factor question. With no factor owed the session is minted directly, in
// the continuation's scope and after its revalidation; with one, the pending
// challenge carries the continuation and CompleteTOTPLogin (or the forced
// enrolment's FinishTOTPEnrolmentForLogin) finishes it under the same rules.
//
// action names the ledger entry ("auth.external.login" is the convention);
// amrExtra lists the external proof's own method names ahead of "totp".
func (a *Authenticator) CompleteExternalLogin(ctx context.Context, user model.User, ip string, cont PrimaryLoginContinuation, action string, amrExtra []string) (LoginResult, error) {
	if cont == nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	if action == "" {
		action = "auth.external.login"
	}
	challenge, enrol, err := a.totpGate(ctx, user)
	if err != nil {
		return LoginResult{}, err
	}
	if challenge {
		pending, err := a.mintTOTPPending(user.ID, ip, cont.SessionScope(), cont)
		if err != nil {
			return LoginResult{}, err
		}
		return LoginResult{MFAToken: pending, MFAEnrolmentRequired: enrol}, nil
	}
	if err := cont.RevalidateCurrent(ctx, a.st); err != nil {
		return LoginResult{}, err
	}
	// mintSession prepends the method itself ("external"), so the extra
	// methods land after it.
	token, sess, err := a.mintSession(ctx, &loginAttempt{ip: ip}, user, cont.SessionScope(), action, externalLogin, amrExtra, nil,
		txVerifierFor(ctx, cont, user.ID, cont.SessionScope()))
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, Session: sess}, nil
}
