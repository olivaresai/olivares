// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// WebAuthn (FIDO2) ceremonies for the privileged AAL3 login.
// The browser runs navigator.credentials.create/get (panel); this side
// issues the challenge, verifies attestation/assertion INCLUDING the user-
// verification flag, persists only public verifier material, and elevates the
// calling session through ElevateSession on success. Verification is delegated
// to github.com/go-webauthn/webauthn (maintained W3C WebAuthn implementation);
// no NIST/FIPS conformance is claimed (target standards only, docs/SECURITY-HARDENING.md).
//
// Fail-closed invariants:
//   - both ceremonies require an AUTHENTICATED session principal — there is no
//     anonymous/discoverable path, so options never leak whether a user exists;
//   - a challenge is single-use and expires (anti-replay): it is consumed on
//     the FIRST finish attempt and re-begin is required after any failure;
//   - no valid ceremony -> no elevation, and the AAL claim is never inflated;
//   - challenges, attestation blobs and keys are never logged.

// WebAuthn ceremony errors.
var (
	// ErrNoWebAuthnCredential means the user has no registered authenticator
	// yet, so an authentication ceremony cannot start (register first).
	ErrNoWebAuthnCredential = errors.New("auth: no registered webauthn credential for this user")
	// ErrWebAuthnVerification means a ceremony failed verification (bad
	// attestation/assertion, missing user verification, replayed or expired
	// challenge, or a sign-count regression suggesting a cloned authenticator).
	// Deliberately coarse: the ledger records the category, the client only
	// learns that verification failed.
	ErrWebAuthnVerification = errors.New("auth: webauthn verification failed")
	// ErrLastWebAuthnCredential means the user attempted to delete their only
	// registered authenticator. The operation is denied to prevent credential
	// lockout: without any registered credential the user cannot step up to
	// AAL3, and recovery requires an operator intervention.
	ErrLastWebAuthnCredential = errors.New("auth: cannot delete the last registered webauthn credential")
)

// webauthnCeremonyTTL bounds a pending challenge: begin-to-finish longer than
// this requires a fresh begin. It is also the timeout advertised to the client.
const webauthnCeremonyTTL = 5 * time.Minute

// WebAuthnRP identifies the relying party for a ceremony. The composition root
// may pin it via configuration; otherwise the API derives it per request from
// the (proxy-aware) external URL, and the library still verifies the client
// data origin against Origins.
type WebAuthnRP struct {
	// ID is the RP ID (a registrable domain suffix of the origin, no scheme).
	ID string
	// DisplayName is the human-readable RP name shown by the authenticator.
	DisplayName string
	// Origins are the exact web origins allowed to complete ceremonies.
	Origins []string
}

// newWebAuthn builds the verifier for one relying party. User verification is
// REQUIRED on both ceremonies (the AAL3 target demands it; finish re-checks the
// flag), attestation conveyance is requested as direct (verified when provided;
// "none" responses still register — the attestation record is persisted either
// way), and challenge expiry is enforced server-side.
func newWebAuthn(rp WebAuthnRP) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:                   rp.ID,
		RPDisplayName:          rp.DisplayName,
		RPOrigins:              rp.Origins,
		AttestationPreference:  protocol.PreferDirectAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{UserVerification: protocol.VerificationRequired},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: webauthnCeremonyTTL, TimeoutUVD: webauthnCeremonyTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: webauthnCeremonyTTL, TimeoutUVD: webauthnCeremonyTTL},
		},
	})
}

// waUser adapts a local user + its registered credentials to webauthn.User.
type waUser struct {
	user  model.User
	creds []webauthn.Credential
}

func (u waUser) WebAuthnID() []byte                         { return []byte(u.user.ID.String()) }
func (u waUser) WebAuthnName() string                       { return u.user.Email }
func (u waUser) WebAuthnDisplayName() string                { return u.user.DisplayName }
func (u waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// ceremonyKind separates the two pending-challenge namespaces.
type ceremonyKind string

const (
	ceremonyRegister ceremonyKind = "register"
	ceremonyLogin    ceremonyKind = "login"
)

// ceremonyStore holds pending WebAuthn challenges server-side between begin and
// finish, keyed by (kind, session credential id): in-memory, mutex-guarded,
// SINGLE-USE and TTL-swept — the same posture as the SSO flow store. A node
// restart drops pending ceremonies (the operator just re-begins); a challenge
// can never be replayed because take removes it before verification runs.
type ceremonyStore struct {
	mu  sync.Mutex
	m   map[string]ceremonyEntry
	now func() time.Time
}

type ceremonyEntry struct {
	session webauthn.SessionData
	expires time.Time
}

func newCeremonyStore(now func() time.Time) *ceremonyStore {
	return &ceremonyStore{m: map[string]ceremonyEntry{}, now: now}
}

func ceremonyKey(kind ceremonyKind, credID model.ID) string {
	return string(kind) + ":" + credID.String()
}

// put stores a pending ceremony, replacing any prior one for the same key (a
// re-begin invalidates the previous challenge) and sweeping expired entries.
func (c *ceremonyStore) put(kind ceremonyKind, credID model.ID, s webauthn.SessionData) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if now.After(e.expires) {
			delete(c.m, k)
		}
	}
	c.m[ceremonyKey(kind, credID)] = ceremonyEntry{session: s, expires: now.Add(webauthnCeremonyTTL)}
}

// take consumes a pending ceremony: it is removed BEFORE verification, so a
// challenge can be attempted exactly once (anti-replay).
func (c *ceremonyStore) take(kind ceremonyKind, credID model.ID) (webauthn.SessionData, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := ceremonyKey(kind, credID)
	e, ok := c.m[k]
	if !ok {
		return webauthn.SessionData{}, false
	}
	delete(c.m, k)
	if c.now().After(e.expires) {
		return webauthn.SessionData{}, false
	}
	return e.session, true
}

// ceremonies lazily builds the authenticator's challenge store (so embedders
// that never touch WebAuthn pay nothing and existing constructors are unchanged).
// webAuthnUserLockKey serializes credential registration PER USER. It carries the
// user id so the estate does not funnel every registration through one lock, and
// it is spelled in one place so the two sides of a race cannot drift onto two
// different keys — a lock on the wrong key serializes nothing while looking like
// it does.
func webAuthnUserLockKey(user model.ID) string { return "auth.webauthn:" + user.String() }

func (a *Authenticator) ceremonies() *ceremonyStore {
	a.ceremonyOnce.Do(func() {
		a.ceremonyPending = newCeremonyStore(func() time.Time { return a.clock.Now().Time() })
	})
	return a.ceremonyPending
}

// loadWebAuthnUser reads the acting user and its registered credentials. Rows
// whose stored credential record fails to decode are SKIPPED for ceremonies
// (they can no longer verify anything) — registration still excludes their ids.
func loadWebAuthnUser(ctx context.Context, as store.AuthScope, userID model.ID) (waUser, []model.WebAuthnCredential, error) {
	u, err := as.Users().Get(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return waUser{}, nil, ErrUnauthenticated
		}
		return waUser{}, nil, err
	}
	if u.Status != model.StatusActive {
		return waUser{}, nil, ErrUnauthenticated
	}
	rows, _, err := as.WebAuthnCredentials().List(ctx, byEq("user_id", userID.String(), 1000))
	if err != nil {
		return waUser{}, nil, err
	}
	wu := waUser{user: u}
	for _, row := range rows {
		var cred webauthn.Credential
		if jerr := json.Unmarshal(row.Credential, &cred); jerr != nil {
			continue
		}
		wu.creds = append(wu.creds, cred)
	}
	return wu, rows, nil
}

// BeginWebAuthnRegistration issues creation options (a challenge) for the acting
// session's user to register a new authenticator. Existing credentials are
// excluded so an authenticator cannot be registered twice.
//
// Binding rule (800-63B-4 target: bind new authenticators at the assurance they
// will provide): once a user HAS a credential, adding another requires a fresh
// AAL3 session — so a stolen AAL1 password session cannot bind the attacker's
// own key next to the legitimate one and self-elevate. The FIRST credential
// necessarily bootstraps from AAL1 (there is nothing to step up with yet);
// that residual window closes the moment the user enrolls, and the
// registration is ledgered either way.
func (a *Authenticator) BeginWebAuthnRegistration(ctx context.Context, actor Principal, rp WebAuthnRP) (*protocol.CredentialCreation, error) {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return nil, ErrUnauthenticated
	}
	wa, err := newWebAuthn(rp)
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn relying party: %w", err)
	}
	var wu waUser
	var rows []model.WebAuthnCredential
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		wu, rows, err = loadWebAuthnUser(ctx, as, actor.UserID)
		return err
	}); err != nil {
		return nil, err
	}
	if len(rows) > 0 && actor.AAL < AAL3 {
		a.auditStepUpFailure(ctx, actor, "webauthn", "registration_step_up")
		return nil, ErrStepUpRequired
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(rows))
	for _, row := range rows {
		id, derr := base64.RawURLEncoding.DecodeString(row.CredentialID)
		if derr != nil {
			continue
		}
		exclude = append(exclude, protocol.CredentialDescriptor{
			Type: protocol.PublicKeyCredentialType, CredentialID: id,
		})
	}
	creation, session, err := wa.BeginRegistration(wu, webauthn.WithExclusions(exclude))
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn begin registration: %w", err)
	}
	a.ceremonies().put(ceremonyRegister, actor.CredID, *session)
	return creation, nil
}

// FinishWebAuthnRegistration verifies the browser's attestation response against
// the pending challenge and persists the new credential. The challenge is
// consumed before verification (single-use); the attestation statement is
// verified by the library when conveyed; the user-verification flag is required
// independently of the library's session check (defense in depth — registering
// an authenticator that never verified its user could later look AAL3-capable).
func (a *Authenticator) FinishWebAuthnRegistration(ctx context.Context, actor Principal, rp WebAuthnRP, response []byte, name string) error {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return ErrUnauthenticated
	}
	wa, err := newWebAuthn(rp)
	if err != nil {
		return fmt.Errorf("auth: webauthn relying party: %w", err)
	}
	session, ok := a.ceremonies().take(ceremonyRegister, actor.CredID)
	if !ok {
		a.auditStepUpFailure(ctx, actor, "webauthn", "challenge")
		return ErrWebAuthnVerification
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		a.auditStepUpFailure(ctx, actor, "webauthn", "verification")
		return ErrWebAuthnVerification
	}
	var wu waUser
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		wu, _, err = loadWebAuthnUser(ctx, as, actor.UserID)
		return err
	}); err != nil {
		return err
	}
	cred, err := wa.CreateCredential(wu, session, parsed)
	if err != nil {
		a.auditStepUpFailure(ctx, actor, "webauthn", "verification")
		return ErrWebAuthnVerification
	}
	if !cred.Flags.UserVerified {
		a.auditStepUpFailure(ctx, actor, "webauthn", "user_verification")
		return ErrWebAuthnVerification
	}
	blob, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("auth: webauthn credential encode: %w", err)
	}
	credID := base64.RawURLEncoding.EncodeToString(cred.ID)
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		// Re-check the binding rule INSIDE the write transaction: the begin-leg
		// check raced any concurrent first-credential registration.
		//
		// ⛔ AND THE TRANSACTION IS NOT ENOUGH ON POSTGRES. Moving the check in
		// here closes the window on SQLite, whose single writer serializes the
		// two transactions — which is why every SQLite leg of this suite passes
		// either way. Under READ COMMITTED both transactions read ZERO existing
		// credentials and both insert, so a user with no authenticator can have a
		// SECOND one registered without ever satisfying the AAL3 step-up this
		// branch exists to demand. UNIQUE(credential_id) does not backstop it:
		// the two credentials are genuinely different.
		//
		// The lock is per USER, not global: two different users registering at
		// once contend over nothing, and a global key would serialize every
		// registration in the estate behind one another.
		if err := lockAuthTransaction(ctx, as, webAuthnUserLockKey(actor.UserID)); err != nil {
			return err
		}
		existing, _, err := as.WebAuthnCredentials().List(ctx, byEq("user_id", actor.UserID.String(), 1))
		if err != nil {
			return err
		}
		if len(existing) > 0 && actor.AAL < AAL3 {
			return ErrStepUpRequired
		}
		row, err := as.WebAuthnCredentials().Create(ctx, model.WebAuthnCredential{
			UserID: actor.UserID, CredentialID: credID, Credential: blob, Name: name,
		})
		if err != nil {
			return err // duplicate credential id -> store.ErrConflict
		}
		// Ledger self-audit (docs/SECURITY-HARDENING.md): who registered which authenticator —
		// ids and non-sensitive attestation metadata only, never key material.
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "auth.webauthn.register", TargetKind: "core.webauthn_credential", TargetID: row.ID,
			Meta: map[string]any{
				"attestation_format": cred.AttestationFormat,
				"backup_eligible":    cred.Flags.BackupEligible,
			},
		})
		return err
	})
}

// BeginWebAuthnStepUp issues assertion options (a challenge) to elevate the
// acting session. The allow-list is the user's registered credentials; with
// none registered it fails with ErrNoWebAuthnCredential.
func (a *Authenticator) BeginWebAuthnStepUp(ctx context.Context, actor Principal, rp WebAuthnRP) (*protocol.CredentialAssertion, error) {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return nil, ErrUnauthenticated
	}
	wa, err := newWebAuthn(rp)
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn relying party: %w", err)
	}
	var wu waUser
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		wu, _, err = loadWebAuthnUser(ctx, as, actor.UserID)
		return err
	}); err != nil {
		return nil, err
	}
	if len(wu.creds) == 0 {
		return nil, ErrNoWebAuthnCredential
	}
	assertion, session, err := wa.BeginLogin(wu)
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn begin login: %w", err)
	}
	a.ceremonies().put(ceremonyLogin, actor.CredID, *session)
	return assertion, nil
}

// FinishWebAuthnStepUp verifies the browser's assertion against the pending
// challenge and, on success, elevates the calling session to AAL3 (method
// "webauthn") for the step-up freshness window. Deny-closed: a consumed/expired
// challenge, a failed signature, a missing user-verification flag or a
// sign-count regression (cloned-authenticator warning) all refuse elevation —
// and every refusal is ledgered. Returns the elevated session.
func (a *Authenticator) FinishWebAuthnStepUp(ctx context.Context, actor Principal, rp WebAuthnRP, response []byte) (model.AuthSession, error) {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return model.AuthSession{}, ErrUnauthenticated
	}
	wa, err := newWebAuthn(rp)
	if err != nil {
		return model.AuthSession{}, fmt.Errorf("auth: webauthn relying party: %w", err)
	}
	session, ok := a.ceremonies().take(ceremonyLogin, actor.CredID)
	if !ok {
		a.auditStepUpFailure(ctx, actor, "webauthn", "challenge")
		return model.AuthSession{}, ErrWebAuthnVerification
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(response)
	if err != nil {
		a.auditStepUpFailure(ctx, actor, "webauthn", "verification")
		return model.AuthSession{}, ErrWebAuthnVerification
	}
	var wu waUser
	if err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		wu, _, err = loadWebAuthnUser(ctx, as, actor.UserID)
		return err
	}); err != nil {
		return model.AuthSession{}, err
	}
	cred, err := wa.ValidateLogin(wu, session, parsed)
	if err != nil {
		a.auditStepUpFailure(ctx, actor, "webauthn", "verification")
		return model.AuthSession{}, ErrWebAuthnVerification
	}
	// Persist the post-assertion credential state FIRST (updated sign count,
	// flags, clone warning) so a clone signal survives even when elevation is
	// refused right after.
	if err := a.persistWebAuthnCredential(ctx, cred); err != nil {
		return model.AuthSession{}, err
	}
	if cred.Authenticator.CloneWarning {
		// The assertion's sign count regressed: two physical authenticators may
		// share this credential. The signature was otherwise valid — refuse the
		// elevation anyway (fail-closed) and leave the warning on the ledger.
		a.auditStepUpFailure(ctx, actor, "webauthn", "clone_warning")
		return model.AuthSession{}, ErrWebAuthnVerification
	}
	if !cred.Flags.UserVerified {
		a.auditStepUpFailure(ctx, actor, "webauthn", "user_verification")
		return model.AuthSession{}, ErrWebAuthnVerification
	}
	return a.ElevateSession(ctx, actor, "webauthn", AAL3)
}

// persistWebAuthnCredential writes the library's post-assertion credential state
// back to the matching row (sign count, flags, clone warning). The row is
// re-read INSIDE the write transaction so a concurrent valid assertion (two
// sessions of one user) cannot fail a ceremony on a stale-version CAS; the
// stored sign count is kept monotonic — the slower of two assertions never
// regresses the counter the faster one advanced.
func (a *Authenticator) persistWebAuthnCredential(ctx context.Context, cred *webauthn.Credential) error {
	credID := base64.RawURLEncoding.EncodeToString(cred.ID)
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.WebAuthnCredentials().List(ctx, byEq("credential_id", credID, 1))
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return store.ErrNotFound
		}
		row := rows[0]
		var stored webauthn.Credential
		if jerr := json.Unmarshal(row.Credential, &stored); jerr == nil &&
			stored.Authenticator.SignCount > cred.Authenticator.SignCount {
			cred.Authenticator.SignCount = stored.Authenticator.SignCount
		}
		blob, err := json.Marshal(cred)
		if err != nil {
			return fmt.Errorf("auth: webauthn credential encode: %w", err)
		}
		row.Credential = blob
		_, err = as.WebAuthnCredentials().Update(ctx, row)
		return err
	})
}

// ListWebAuthnCredentials returns the acting user's registered authenticators
// (public metadata only — never key material over this path).
func (a *Authenticator) ListWebAuthnCredentials(ctx context.Context, actor Principal) ([]model.WebAuthnCredential, error) {
	if actor.Kind != KindUser || actor.UserID.IsZero() {
		return nil, ErrUnauthenticated
	}
	var rows []model.WebAuthnCredential
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		var lerr error
		rows, _, lerr = as.WebAuthnCredentials().List(ctx, byEq("user_id", actor.UserID.String(), 1000))
		return lerr
	})
	return rows, err
}

// DeleteWebAuthnCredential unregisters one of the acting user's authenticators
// (the stolen/lost-key remediation), ledgered. It requires a FRESH AAL3 session:
// removing a credential is itself a credential-lifecycle act — an AAL1 thief
// who could delete the victim's keys would reopen the first-credential
// bootstrap and bind their own. A user whose only key is lost recovers through
// an operator, not through the password session.
func (a *Authenticator) DeleteWebAuthnCredential(ctx context.Context, actor Principal, id model.ID) error {
	if actor.Kind != KindUser || actor.CredID.IsZero() {
		return ErrUnauthenticated
	}
	if actor.AAL < AAL3 {
		a.auditStepUpFailure(ctx, actor, "webauthn", "unregister_step_up")
		return ErrStepUpRequired
	}
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		row, err := as.WebAuthnCredentials().Get(ctx, id)
		if err != nil {
			return err // ErrNotFound -> 404
		}
		if row.UserID != actor.UserID {
			return store.ErrNotFound // never an other-user existence oracle
		}
		// Last-credential guard: refuse to delete if it would leave zero
		// authenticators — the user would lose the ability to step up.
		all, _, err := as.WebAuthnCredentials().List(ctx, byEq("user_id", actor.UserID.String(), 2))
		if err != nil {
			return err
		}
		if len(all) <= 1 {
			return ErrLastWebAuthnCredential
		}
		if err := as.WebAuthnCredentials().Delete(ctx, id); err != nil {
			return err
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "auth.webauthn.unregister", TargetKind: "core.webauthn_credential", TargetID: id,
		})
		return err
	})
}

// RenameWebAuthnCredential updates the display name of one of the acting
// user's authenticators. It is owner-only (same user) but does NOT require
// AAL3 — renaming is a non-security-sensitive metadata change. Ledgered.
func (a *Authenticator) RenameWebAuthnCredential(ctx context.Context, actor Principal, id model.ID, name string) error {
	if actor.Kind != KindUser || actor.UserID.IsZero() {
		return ErrUnauthenticated
	}
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		row, err := as.WebAuthnCredentials().Get(ctx, id)
		if err != nil {
			return err
		}
		if row.UserID != actor.UserID {
			return store.ErrNotFound
		}
		row.Name = name
		if _, err := as.WebAuthnCredentials().Update(ctx, row); err != nil {
			return err
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "auth.webauthn.rename", TargetKind: "core.webauthn_credential", TargetID: id,
			Meta: map[string]any{"name": name},
		})
		return err
	})
}
