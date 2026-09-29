// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

// CAEP 1.0 / RISC 1.0 Security Event Token receiver (OpenID Final, 2025-09-02).
// Maps verified CAEP/RISC events to existing revocation primitives. The wire
// parsing (event URI taxonomy, subject identifier formats) lives in core/api/caep;
// this file owns what only core may do: verify the SET's JWS signature against
// the tenant's configured publisher key, and turn an access-affecting event into
// the same credential-cut primitives SCIM lifecycle uses.
//
// The receiver is deny-closed: a tenant that has not configured a CAEP SET config
// (referencing a publisher from the shared registry) rejects every event.
// core/auth cannot import core/api — the CAEPEventAction type is defined here as a
// mirror of caep.CAEPEventAction; the handler maps between them.

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// orgSettingCAEPSet is the Org.Settings key for the CAEP SET receiver configuration.
const orgSettingCAEPSet = "caep_set"

// ErrCAEPSetDisabled means no CAEP SET publisher is configured for the tenant
// (deny-closed): the receiver will not act on an unverifiable event.
var ErrCAEPSetDisabled = errors.New("auth: CAEP SET receiver not configured for this tenant")

// CAEPEventAction is the access effect the CAEP/RISC receiver applies for an event.
// Defined in core/auth (not imported from core/api/caep) to keep core/auth free of
// the wire layer. The handler maps from caep.CAEPEventAction to this type.
type CAEPEventAction string

const (
	// CAEPSessionRevoke maps to CAEP session-revoked: cut sessions for the subject.
	CAEPSessionRevoke CAEPEventAction = "session_revoke"
	// CAEPTokenRevoke maps to CAEP token-claims-change: revoke tokens (not sessions).
	CAEPTokenRevoke CAEPEventAction = "token_revoke"
	// CAEPCredentialRevoke maps to CAEP credential-change: revoke all credentials.
	CAEPCredentialRevoke CAEPEventAction = "credential_revoke"
	// CAEPDeviceNonCompliant maps to CAEP device-compliance-change: configurable.
	CAEPDeviceNonCompliant CAEPEventAction = "device_noncompliant"
	// CAEPAccountDisable maps to RISC account-disabled: total cut.
	CAEPAccountDisable CAEPEventAction = "account_disable"
	// CAEPCredentialCompromise maps to RISC credential-compromise: total cut.
	CAEPCredentialCompromise CAEPEventAction = "credential_compromise"
	// CAEPIgnore is an acknowledged-but-not-acted-on event.
	CAEPIgnore CAEPEventAction = "ignore"
)

// CAEPSetConfig is the tenant's CAEP SET receiver configuration, persisted in
// Org.Settings["caep_set"]. It references a publisher from the shared publisher
// registry (ConfigurePublisher / PublisherFor).
type CAEPSetConfig struct {
	// Enabled turns the receiver on. The zero value (false) is deny-closed.
	Enabled bool `json:"enabled"`
	// PublisherID references the SETPublisher from the tenant's shared registry.
	// Empty means deny-closed (no publisher resolved).
	PublisherID string `json:"publisher_id"`
	// DeviceNonCompliantAction controls the response to device-compliance-change
	// events: "step_up" degrades session assurance to AAL1 (DegradeSessionAssurance);
	// the default ("" or "revoke") cuts all sessions and tenant-bound tokens.
	DeviceNonCompliantAction string `json:"device_noncompliant_action,omitempty"`
}

// CAEPEventEnvelope is the already-parsed SET a CAEP/RISC handler passes to the
// Authenticator. Wire decode and subject parsing happen in core/api; core/auth
// verifies the JWS signature and applies the access effect. Embeds SETEnvelope so
// verifySETSignature and checkIssuerAudience can consume it directly.
type CAEPEventEnvelope struct {
	SETEnvelope
	// Action is the access effect derived by the handler from the SET's event URI.
	Action CAEPEventAction
	// SubjectEmail is set when the SSF sub_id format is "email".
	SubjectEmail string
	// SubjectExternalID is set when the SSF sub_id format is "iss_sub" (the sub component).
	SubjectExternalID string
	// SubjectUserID is set when the SSF sub_id format is "opaque": either an internal
	// user ID or — for session-revoked events — a session ID. Subject resolution
	// tries it as a user ID first; dispatch tries it as a session ID.
	SubjectUserID string
	// EventPayload is the raw JSON of the event-specific payload (e.g. device compliance).
	EventPayload []byte
}

// CAEPEventResult reports what the receiver did with a verified CAEP/RISC event.
type CAEPEventResult struct {
	// Action is the string representation of the applied CAEPEventAction.
	Action string
	// UserID is the resolved tenant member the action was applied to (zero for ignore).
	UserID model.ID
	// JTI is the SET's jti (for caller correlation).
	JTI string
}

// ConfigureCAEPSet stores (or replaces) the tenant's CAEP SET receiver
// configuration and records the change on the tenant's audit chain. The
// referenced publisher must be configured separately via ConfigurePublisher.
func (a *Authenticator) ConfigureCAEPSet(ctx context.Context, actor Principal, tenant model.TenantID, cfg CAEPSetConfig) error {
	if tenant.IsZero() || tenant.IsSystem() {
		return ErrInvalidToken
	}
	asMap := map[string]any{}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &asMap); err != nil {
		return err
	}
	return a.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		settings := org.Settings
		if settings == nil {
			settings = map[string]any{}
		}
		settings[orgSettingCAEPSet] = asMap
		if _, err := sc.SetOrgSettings(ctx, settings); err != nil {
			return err
		}
		_, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "caep.set.configure", TargetKind: "core.org",
			TargetID: model.ID(tenant.String()),
			Meta:     map[string]any{"publisher_id": cfg.PublisherID},
		})
		return err
	})
}

// caepSetConfigFor returns the tenant's CAEP SET receiver configuration and
// whether one is present in the settings.
func (a *Authenticator) caepSetConfigFor(ctx context.Context, tenant model.TenantID) (CAEPSetConfig, bool, error) {
	if tenant.IsZero() || tenant.IsSystem() {
		return CAEPSetConfig{}, false, ErrInvalidToken
	}
	var cfg CAEPSetConfig
	var present bool
	err := a.st.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		raw, ok := org.Settings[orgSettingCAEPSet]
		if !ok {
			return nil
		}
		present = true
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, &cfg)
	})
	return cfg, present, err
}

// CAEPReceiveEvent verifies a parsed CAEP/RISC SET against the tenant's
// configured publisher and applies its access effect. Deny-closed: no config →
// ErrCAEPSetDisabled. Verifies the JWS signature, checks issuer/audience/freshness,
// suppresses JTI duplicates (any non-nil error from CheckJTIDuplicate is treated
// as a rejection, not just ErrSETJTIDuplicate), resolves the SSF subject to a
// tenant member, and maps the action to revocation primitives.
func (a *Authenticator) CAEPReceiveEvent(ctx context.Context, actor Principal, tenant model.TenantID, env CAEPEventEnvelope) (CAEPEventResult, error) {
	cfg, present, err := a.caepSetConfigFor(ctx, tenant)
	if err != nil {
		return CAEPEventResult{}, err
	}
	if !present || !cfg.Enabled || cfg.PublisherID == "" {
		return CAEPEventResult{}, ErrCAEPSetDisabled
	}

	pub, err := a.PublisherFor(ctx, tenant, cfg.PublisherID)
	if err != nil {
		// Publisher not found in registry → deny-closed.
		return CAEPEventResult{}, ErrCAEPSetDisabled
	}
	if !pub.Enabled || len(pub.Keys) == 0 {
		return CAEPEventResult{}, ErrCAEPSetDisabled
	}

	if err := verifySETSignature(pub, env.SETEnvelope); err != nil {
		_ = a.auditCAEPReject(ctx, actor, tenant, cfg.PublisherID, env.JTI, "signature")
		return CAEPEventResult{}, err
	}

	if err := checkIssuerAudience(pub, env.SETEnvelope, a.clock.Now()); err != nil {
		_ = a.auditCAEPReject(ctx, actor, tenant, cfg.PublisherID, env.JTI, "issuer")
		return CAEPEventResult{}, err
	}

	// Treat ANY non-nil error from CheckJTIDuplicate as a rejection signal (not just
	// ErrSETJTIDuplicate), per Task 2 review guidance.
	if err := a.CheckJTIDuplicate(ctx, pub.ID, env.JTI, pub.MaxAgeSeconds); err != nil {
		_ = a.auditCAEPReject(ctx, actor, tenant, cfg.PublisherID, env.JTI, "jti_duplicate")
		return CAEPEventResult{}, err
	}

	res := CAEPEventResult{Action: string(env.Action), JTI: env.JTI}

	if env.Action == CAEPIgnore || env.Action == "" {
		res.Action = string(CAEPIgnore)
		// Best-effort, exactly as on the accepted path below: the audit failure must
		// not turn an ignored CAEP event into an error to the publisher. Discarded
		// explicitly so the decision is visible rather than inferred from silence.
		_ = a.auditCAEPReceived(ctx, actor, tenant, cfg.PublisherID, env.Action, env.JTI, res.UserID)
		return res, nil
	}

	user, err := a.resolveCAEPSubject(ctx, tenant, env)
	if err != nil {
		_ = a.auditCAEPReject(ctx, actor, tenant, cfg.PublisherID, env.JTI, "subject")
		return CAEPEventResult{}, err
	}
	res.UserID = user.ID

	if err := a.dispatchCAEPAction(ctx, actor, tenant, cfg, env, user.ID); err != nil {
		return CAEPEventResult{}, err
	}

	// Best-effort tenant-chain audit for the received event.
	_ = a.auditCAEPReceived(ctx, actor, tenant, cfg.PublisherID, env.Action, env.JTI, user.ID)
	return res, nil
}

// resolveCAEPSubject resolves the SET's SSF subject to a tenant member.
// Tries SubjectUserID (opaque/user ID), then SubjectEmail, then SubjectExternalID.
// Returns ErrSCIMSetSubject if no match is found (reuses the existing sentinel).
func (a *Authenticator) resolveCAEPSubject(ctx context.Context, tenant model.TenantID, env CAEPEventEnvelope) (model.User, error) {
	if env.SubjectUserID != "" {
		u, err := a.SCIMGetMember(ctx, tenant, model.ID(env.SubjectUserID))
		if err == nil {
			return u, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return model.User{}, err
		}
		// SubjectUserID may be a session ID, not a user ID — fall through.
	}
	// The stored address is the userName read through SCIMUserNameKey, so the
	// subject is read the same way: a spelling the IdP itself provisioned, or
	// any other letter case of it, names the member it was stored as.
	if email := SCIMUserNameKey(env.SubjectEmail); email != "" {
		if u, ok, err := a.SCIMFindMember(ctx, tenant, "email", email); err != nil {
			return model.User{}, err
		} else if ok {
			return u, nil
		}
	}
	if env.SubjectExternalID != "" {
		if u, ok, err := a.SCIMFindMember(ctx, tenant, "external_id", env.SubjectExternalID); err != nil {
			return model.User{}, err
		} else if ok {
			return u, nil
		}
	}
	return model.User{}, ErrSCIMSetSubject
}

// dispatchCAEPAction maps the action to the tenant-bounded primitive the
// action table names: revoke what is scoped to or bound to this tenant, exclude
// the rest from it, or offboard.
func (a *Authenticator) dispatchCAEPAction(ctx context.Context, actor Principal, tenant model.TenantID, cfg CAEPSetConfig, env CAEPEventEnvelope, userID model.ID) error {
	// A tenant's event acts only within that tenant: it revokes the sessions
	// scoped to it and the tokens bound to it, and it excludes the account's
	// account-scope sessions from it. It never revokes a session or token that
	// carries another tenant, and never writes the account's global status.
	switch env.Action {
	case CAEPSessionRevoke:
		return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
			if err := prepareUserAuthorityWrite(ctx, as, userID); err != nil {
				return err
			}
			// For session-revoked, SubjectUserID may carry a session ID rather than a
			// user ID (SSF opaque sub targeting one session). A session scoped to this
			// tenant is revoked; any other session of the account is excluded from it.
			if env.SubjectUserID != "" {
				s, err := as.Sessions().Get(ctx, model.ID(env.SubjectUserID))
				if err == nil && s.UserID == userID && !s.Revoked {
					if s.TenantScope == tenant {
						s.Revoked = true
						if _, err := as.Sessions().Update(ctx, s); err != nil {
							return err
						}
						return auditAct(ctx, as, actor, "caep.session.revoked", "core.auth_session", s.ID)
					}
					if err := excludeSession(ctx, as, actor, userID, s.ID, tenant); err != nil {
						return err
					}
					return auditAct(ctx, as, actor, "caep.session.excluded", "core.auth_session", s.ID)
				}
			}
			if err := revokeTenantTokens(ctx, as, actor, userID, tenant); err != nil {
				return err
			}
			if err := excludeAccountSessions(ctx, as, actor, userID, tenant); err != nil {
				return err
			}
			return auditAct(ctx, as, actor, "caep.session.excluded", "core.user", userID)
		})

	case CAEPTokenRevoke:
		// token-claims-change: revoke tenant-bound tokens only (not sessions).
		return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
			if err := prepareUserAuthorityWrite(ctx, as, userID); err != nil {
				return err
			}
			if err := revokeTenantTokens(ctx, as, actor, userID, tenant); err != nil {
				return err
			}
			return auditAct(ctx, as, actor, "caep.tokens.revoked", "core.user", userID)
		})

	case CAEPCredentialRevoke:
		// credential-change: this tenant's tokens and sessions.
		return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
			if err := prepareUserAuthorityWrite(ctx, as, userID); err != nil {
				return err
			}
			if err := revokeTenantTokens(ctx, as, actor, userID, tenant); err != nil {
				return err
			}
			if err := excludeAccountSessions(ctx, as, actor, userID, tenant); err != nil {
				return err
			}
			return auditAct(ctx, as, actor, "caep.credentials.revoked", "core.user", userID)
		})

	case CAEPDeviceNonCompliant:
		// device-compliance-change: configurable step-up or revoke, in this tenant.
		return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
			if err := prepareUserAuthorityWrite(ctx, as, userID); err != nil {
				return err
			}
			if cfg.DeviceNonCompliantAction == "step_up" {
				if err := degradeScopedSessions(ctx, as, userID, tenant); err != nil {
					return err
				}
				if err := excludeAccountSessionsOnly(ctx, as, actor, userID, tenant); err != nil {
					return err
				}
				return auditAct(ctx, as, actor, "caep.assurance.degraded", "core.user", userID)
			}
			if err := revokeTenantTokens(ctx, as, actor, userID, tenant); err != nil {
				return err
			}
			if err := excludeAccountSessions(ctx, as, actor, userID, tenant); err != nil {
				return err
			}
			return auditAct(ctx, as, actor, "caep.session.excluded", "core.user", userID)
		})

	case CAEPAccountDisable, CAEPCredentialCompromise:
		// RISC account-disabled / credential-compromise: this tenant removes the
		// account, and reports the event for the deployment, whose own ceremony is
		// the only global answer.
		return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
			if _, err := a.scopedOffboard(ctx, as, actor, userID, tenant, "caep."+string(env.Action)); err != nil {
				return err
			}
			return metaAudit(ctx, as, actor, "account.compromise.reported", "core.user", userID, map[string]any{
				"tenant": tenant.String(), "event": string(env.Action),
			})
		})

	default:
		// Unknown action → no-op (deny-closed only for unconfigured; unknown events
		// from a valid, verified publisher are acknowledged but not acted on).
		return nil
	}
}

// degradeScopedSessions lowers the account's elevated sessions scoped to t to
// AAL1.
func degradeScopedSessions(ctx context.Context, as store.AuthScope, user model.ID, t model.TenantID) error {
	sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.Revoked || s.TenantScope != t || s.AAL <= 1 {
			continue
		}
		s.AAL = 1
		s.AALExpiresAt = nil
		s.AALAuthenticatedAt = nil
		if _, err := as.Sessions().Update(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// excludeAccountSessionsOnly excludes every live account-scope session of the
// account from t, and leaves the sessions scoped to t in place.
func excludeAccountSessionsOnly(ctx context.Context, as store.AuthScope, actor Principal, user model.ID, t model.TenantID) error {
	sessions, err := drainList(ctx, as.Sessions().List, byEq("user_id", user.String(), 0))
	if err != nil {
		return err
	}
	for _, s := range sessions {
		if s.Revoked || !s.TenantScope.IsZero() {
			continue
		}
		if err := excludeSession(ctx, as, actor, user, s.ID, t); err != nil {
			return err
		}
	}
	return nil
}

// DegradeSessionAssurance downgrades all non-revoked elevated sessions for the
// user to AAL1 (step-up boundary for device-compliance events). Sessions already
// at AAL ≤ 1 are left untouched; revoked sessions are skipped.
func (a *Authenticator) DegradeSessionAssurance(ctx context.Context, actor Principal, userID model.ID) error {
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := prepareUserAuthorityWrite(ctx, as, userID); err != nil {
			return err
		}
		sessions, _, err := as.Sessions().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "user_id", Op: model.OpEq, Value: userID.String()},
		}, Limit: 1000})
		if err != nil {
			return err
		}
		for _, s := range sessions {
			if s.Revoked || s.AAL <= 1 {
				continue
			}
			s.AAL = 1
			s.AALExpiresAt = nil
			s.AALAuthenticatedAt = nil
			if _, err := as.Sessions().Update(ctx, s); err != nil {
				return err
			}
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "caep.assurance.degraded", TargetKind: "core.user", TargetID: userID,
		})
		return err
	})
}

// auditCAEPReceived records a successfully processed CAEP event to the tenant's
// audit chain. Best-effort: caller ignores the returned error.
func (a *Authenticator) auditCAEPReceived(ctx context.Context, actor Principal, tenant model.TenantID, publisherID string, action CAEPEventAction, jti string, userID model.ID) error {
	return a.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "caep.event.received", TargetKind: "core.user", TargetID: userID,
			Meta: map[string]any{
				"publisher_id": publisherID,
				"action":       string(action),
				"jti":          jti,
			},
		})
		return err
	})
}

// auditCAEPReject records a rejected CAEP event to the tenant's audit chain.
// Best-effort: caller ignores the returned error.
func (a *Authenticator) auditCAEPReject(ctx context.Context, actor Principal, tenant model.TenantID, publisherID, jti, reason string) error {
	return a.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: actor.Actor(), ActorKind: actor.ActorKind(),
			Action: "caep.event.rejected", TargetKind: "core.org",
			TargetID: model.ID(tenant.String()),
			Meta: map[string]any{
				"publisher_id": publisherID,
				"jti":          jti,
				"reason":       reason,
			},
		})
		return err
	})
}
