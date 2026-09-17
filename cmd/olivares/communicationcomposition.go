// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// envCommunicationActivation is the REQUESTED K3 configuration. It is
// deliberately separate from effective readiness: "on" asks boot to bind the
// pump witness and enable the dual runtime credential posture before the first
// leadership election, so promotion recovery runs; whether a credential can
// actually be minted is decided per launch from the effective readiness
// conjunction, never from this flag alone.
const envCommunicationActivation = "OLIVARES_COMMUNICATION_ACTIVATION"

// communicationActivationConfig is the parsed operator request.
type communicationActivationConfig struct {
	Requested          bool
	ContentKeyringPath string
	CursorKeyringPath  string
	// CustodyBlockers are the K3-exclusive custody facts that keep a REQUESTED
	// activation from composing the K3 lane: a keyring that is not declared,
	// cannot be opened, or does not load. Each entry names the setting and the
	// cause, never key material. Boot appends load failures; the flag stays
	// requested so the posture is visible instead of silently downgraded.
	CustodyBlockers []string
}

// blockCustody records one custody failure for a setting. The first cause per
// setting wins, so a path that is both blank and unopenable is listed once.
func (c *communicationActivationConfig) blockCustody(setting, cause string) {
	for _, existing := range c.CustodyBlockers {
		if strings.HasPrefix(existing, setting+": ") {
			return
		}
	}
	c.CustodyBlockers = append(c.CustodyBlockers, setting+": "+cause)
}

// loadCommunicationActivationConfig parses the activation request. Only the
// SYNTAX of the request is a configuration error at boot. Requesting activation
// without both custody files declared is recorded as a custody blocker: K3 is
// custody that belongs to K3 alone, so its absence keeps K3 OFF with a visible
// cause (the composition's log, the pump's lane verdict and non-effective
// readiness) while core, K1 and K2 boot and serve. Dynamic facts (leadership,
// store proof, scheduler) remain readiness terms and degrade the same way.
func loadCommunicationActivationConfig(getenv func(string) string) (communicationActivationConfig, error) {
	cfg := communicationActivationConfig{
		ContentKeyringPath: strings.TrimSpace(getenv(envCommunicationContentKeyringFile)),
		CursorKeyringPath:  strings.TrimSpace(getenv(envCommunicationCursorKeyringFile)),
	}
	switch raw := strings.ToLower(strings.TrimSpace(getenv(envCommunicationActivation))); raw {
	case "", "off", "0", "false":
		cfg.Requested = false
	case "on", "1", "true":
		cfg.Requested = true
	default:
		return communicationActivationConfig{}, fmt.Errorf(
			"%s=%q is not a recognized value (on|off)", envCommunicationActivation, raw)
	}
	if cfg.Requested {
		if cfg.ContentKeyringPath == "" {
			cfg.blockCustody(envCommunicationContentKeyringFile, "not set; K3 cannot seal content without operator custody")
		}
		if cfg.CursorKeyringPath == "" {
			cfg.blockCustody(envCommunicationCursorKeyringFile, "not set; K3 cannot sign inbox cursors without operator custody")
		}
	}
	return cfg, nil
}

// communicationComposition holds every real K3 composition object boot binds
// on the sessions module. Every field is a concrete adapter over the boot
// store, the session plane or the governance lifecycle; none is a stub.
type communicationComposition struct {
	activation communicationActivationConfig
	reads      *communicationDirectoryReads
	resolver   *communicationDirectoryResolver
	closure    *communicationGrantClosureResolver
	attestor   sessions.PublicationAudienceAttestor
	store      *communicationStoreProofWitness
	pump       *communicationPumpWitness
	authority  *communicationOutboxAuthority
	cursor     communicationCursorKeyringStatus
}

// bindCommunicationComposition binds the directory resolver, the publication
// attestor, the grant closure, the composite store proof, the mandatory outbox
// authority and, when activation is requested, the dual credential posture and
// (with usable custody) the pump witness. It runs BEFORE the first Leader.Run
// so the existing promotion recovery observes the enabled posture on first
// boot (orphan cleanup), and it never fabricates a cluster fact: the writer
// control is proved from the store, not asserted.
//
// The outbox authority is bound on EVERY boot, requested or not: it is what
// keeps the Apply nudge, the public drain and the periodic pump from claiming
// a K3 event while the lane is not composed or not effective.
func bindCommunicationComposition(
	ctx context.Context,
	activation communicationActivationConfig,
	st store.Store,
	sm *sessions.Module,
	lifecycle workAgentLifecycle,
	guard *communicationGuardStoreWitness,
	cursor *sessions.CommunicationCursorTokenKeyring,
	cursorStatus communicationCursorKeyringStatus,
	sinkBound bool,
	log *slog.Logger,
) (*communicationComposition, error) {
	if st == nil || sm == nil {
		return nil, fmt.Errorf("communication composition requires the store and the sessions module")
	}
	reads := newCommunicationDirectoryReads(st, sm, lifecycle)
	resolver := newCommunicationDirectoryResolver(newDirectoryScopeRunner(st), reads, time.Now)
	closure := newCommunicationGrantClosureResolver(resolver)
	attestor, err := sessions.NewCommunicationPublicationAudienceAttestor(sm, resolver, nil)
	if err != nil {
		return nil, err
	}
	listOrgs := func(ctx context.Context) ([]model.Org, error) {
		var orgs []model.Org
		err := st.System(ctx, func(sys store.SystemScope) error {
			var err error
			orgs, err = sys.ListOrgs(ctx)
			return err
		})
		return orgs, err
	}
	proof := newCommunicationStoreProofWitness(st, guard, sm, listOrgs, st.Leader().IsLeader, time.Now)

	authority := newCommunicationOutboxAuthority(sm)

	sm.UseCommunicationDirectorySnapshotResolver(resolver)
	sm.UseCommunicationPublicationAudienceAttestor(attestor)
	sm.UseCommunicationChannelGrantSubjectClosureResolver(closure)
	sm.UseCommunicationStoreReadinessWitness(proof)
	sm.UseWorkOutboxClaimAuthority(authority)
	if cursor != nil {
		sm.UseCommunicationCursorTokenKeyring(cursor)
	}
	composition := &communicationComposition{
		activation: activation, reads: reads, resolver: resolver, closure: closure,
		attestor: attestor, store: proof, authority: authority, cursor: cursorStatus,
	}
	if !activation.Requested {
		authority.composeLane(nil, nil)
		log.Info("sessions: communication activation not requested; K3 composition bound for readiness reporting only, outbox authority holds K3, credentials and pump lane stay OFF",
			"env", envCommunicationActivation)
		return composition, nil
	}
	// The REQUESTED posture is retained whatever custody says: the dual
	// credential posture is enabled so issuance is decided by effective
	// readiness (503 while not effective, never a silent work-only fallback)
	// and the promotion recovery withdraws orphan bearers on first boot.
	sm.EnableCommunicationSessionCredentials()
	if len(activation.CustodyBlockers) > 0 {
		// Custody that belongs to K3 alone is missing or unusable. The K3 lane is
		// NOT composed (no pump witness, so readiness cannot become effective and
		// the authority refuses every K3 claim with this cause) while core, K1
		// and K2 boot and serve. Nothing is minted, no fallback key is chosen and
		// no durable data is touched; fixing the custody and restarting is the
		// only way forward.
		authority.composeLane(nil, activation.CustodyBlockers)
		log.Error("sessions: communication activation requested but K3 custody is unavailable; K3 lane not composed: zero credential issuance, zero outbox claims, zero effects until custody is fixed; core, K1 and K2 continue",
			"env", envCommunicationActivation, "blockers", activation.CustodyBlockers)
		return composition, nil
	}
	composition.pump = newCommunicationPumpWitness(ctx, st, sinkBound)
	authority.composeLane(composition.pump, nil)
	sm.UseCommunicationPumpReadinessWitness(composition.pump)
	log.Info("sessions: communication activation requested; dual runtime credentials enabled, effective readiness decides issuance",
		"cursor_signing_kid", cursorStatus.SigningKID,
		"cursor_verification_kids", len(cursorStatus.VerificationKIDs),
		"sink_bound", sinkBound)
	return composition, nil
}

// pumpWitness returns the witness to attach to the registered pump, or nil
// when activation was not requested or its custody is unavailable.
func (c *communicationComposition) pumpWitness() *communicationPumpWitness {
	if c == nil {
		return nil
	}
	return c.pump
}

// outboxAuthority returns the module-bound authority the pump reports on.
func (c *communicationComposition) outboxAuthority() *communicationOutboxAuthority {
	if c == nil {
		return nil
	}
	return c.authority
}

// custodyBlockers returns the visible causes that keep a requested activation
// from composing its K3 lane; empty when custody loaded or was not requested.
func (c *communicationComposition) custodyBlockers() []string {
	if c == nil {
		return nil
	}
	return append([]string(nil), c.activation.CustodyBlockers...)
}

// reconcileAndVerify runs the store proof at the promotion barrier.
func (c *communicationComposition) reconcileAndVerify(ctx context.Context) error {
	if c == nil || c.store == nil {
		return store.ErrStoreUnavailable
	}
	return c.store.ReconcileAndVerify(ctx)
}
