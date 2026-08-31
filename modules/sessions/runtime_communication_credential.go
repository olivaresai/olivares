// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// runtimeCredentials is the dual authority injected into one process. Tokens
// stay in this short-lived value only until Runner.Launch copies the environment;
// the run row receives the handles, deadlines, and exact binding, never Token.
type runtimeCredentials struct {
	work          WorkSessionCredential
	communication CommunicationSessionCredential
	launchID      model.ID
}

type runtimeCredentialRecoveryContextKey struct{}

func (m *Module) runtimeData(ctx context.Context) api.ModuleData {
	if _, recovering := ctx.Value(runtimeCredentialRecoveryContextKey{}).(struct{}); recovering &&
		m.recoveryData != nil {
		return m.recoveryData
	}
	return m.data
}

func runtimeCredentialRecovery(ctx context.Context) bool {
	_, ok := ctx.Value(runtimeCredentialRecoveryContextKey{}).(struct{})
	return ok
}

func (m *Module) communicationCredentialRevoker(
	ctx context.Context,
) CommunicationSessionCredentialSource {
	if runtimeCredentialRecovery(ctx) && m.recoveryData != nil {
		return m.rt.recoveryCommunicationSessionCreds
	}
	return m.rt.communicationSessionCreds
}

func (c runtimeCredentials) complete(now time.Time) bool {
	return !c.launchID.IsZero() && !c.work.ID.IsZero() && !c.communication.ID.IsZero() &&
		!c.work.Expired(now) && !c.communication.Expired(now) &&
		!c.communication.WorkspaceID.IsZero() && c.work.SessionRef != "" &&
		c.communication.SessionRef != "" && c.work.RunRef != "" &&
		c.communication.RunRef != "" && c.work.ClaimFence > 0 &&
		c.communication.ClaimFence > 0
}

// credentialSourceError preserves error classification for errors.Is/As while
// making Error safe to log or return through an unexpected mapper. Credential
// providers are outside this package and may include a bearer in their error.
type credentialSourceError struct {
	operation string
	cause     error
}

func (e *credentialSourceError) Error() string { return "sessions: " + e.operation + " failed" }
func (e *credentialSourceError) Unwrap() error { return e.cause }

func secretSafeCredentialError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &credentialSourceError{operation: operation, cause: err}
}

// ensureRuntimeCredentialWiring is called before any mint or durable launch
// reservation. K3 is opt-in for standalone module users, but once enabled it is
// indivisible: both purpose issuers must exist or the launch returns 503.
func (m *Module) ensureRuntimeCredentialWiring() error {
	if !m.rt.communicationCredentialsEnabled {
		return nil
	}
	if m.rt.communicationSessionCreds == nil || m.rt.workSessionCreds == nil {
		return &runErr{
			status: http.StatusServiceUnavailable,
			msg:    "session communication credentials are not available; launch denied",
		}
	}
	return nil
}

// mintRuntimeCredentials emits communication BEFORE work. The communication
// issuer rejects a stale fence before the work issuer can supersede a current
// work bearer; reversing this order lets a delayed launcher revoke live work
// authority and only then discover that its Claim generation is obsolete.
func (m *Module) mintRuntimeCredentials(
	ctx context.Context,
	tenant model.TenantID,
	runRef, agentRef string,
	lease Lease,
) (runtimeCredentials, error) {
	if !m.rt.communicationCredentialsEnabled {
		work, err := m.maybeMintWorkSession(ctx, tenant, runRef, agentRef, lease)
		return runtimeCredentials{work: work}, err
	}
	if err := m.ensureRuntimeCredentialWiring(); err != nil {
		return runtimeCredentials{}, err
	}
	request, err := m.communicationCredentialRequest(ctx, tenant, runRef, agentRef, lease)
	if err != nil {
		return runtimeCredentials{}, err
	}
	communication, err := m.maybeMintCommunicationSession(ctx, request)
	if err != nil {
		return runtimeCredentials{}, err
	}
	work, workErr := m.maybeMintWorkSession(ctx, tenant, runRef, agentRef, lease)
	if workErr == nil && !work.NotAfter.After(m.now().Add(maxRuntimeSessionCredentialTTL)) {
		return runtimeCredentials{work: work, communication: communication}, nil
	}
	if workErr == nil {
		workErr = forbiddenErr("work-session credential lifetime exceeds 30 minutes (fail-closed)")
	}
	workRevokeErr := m.revokeWorkSessionCredential(
		ctx, work.ID, workSessionCredentialRequest(tenant, runRef, agentRef, lease),
	)
	revokeErr := m.revokeCommunicationSessionCredential(
		ctx, communication.ID, communicationCredentialRequest(communication),
	)
	return runtimeCredentials{}, errors.Join(
		workErr,
		wrapCredentialCompensation("revoke work credential after invalid work mint", workRevokeErr),
		wrapCredentialCompensation("revoke communication credential after work mint failed", revokeErr),
	)
}

// communicationCredentialRequest proves the exact runtime tuple from module
// state. The Claim is checked before the issuer side effect, then checked again
// transactionally when the handles are persisted before spawn.
func (m *Module) communicationCredentialRequest(
	ctx context.Context,
	tenant model.TenantID,
	runRef, agentRef string,
	lease Lease,
) (CommunicationSessionCredentialRequest, error) {
	if lease.SID == "" || lease.Holder == "" || lease.Fence < 1 ||
		!validRuntimeUUIDv7(runRef) || !validCanonicalSID(lease.SID) {
		return CommunicationSessionCredentialRequest{}, forbiddenErr(
			"communication credential requires an exact live session Claim",
		)
	}
	if err := m.Authority(ctx, tenant, lease.SID, lease.Holder, lease.Fence); err != nil {
		return CommunicationSessionCredentialRequest{}, denyClosedErr(
			"communication credential Claim is not authoritative", err,
		)
	}
	workspaceID, err := m.communicationWorkspace(ctx, tenant, runRef, lease.SID)
	if err != nil {
		return CommunicationSessionCredentialRequest{}, err
	}
	return CommunicationSessionCredentialRequest{
		Tenant: tenant, WorkspaceID: workspaceID, SessionRef: lease.SID,
		RunRef: runRef, AgentRef: agentRef, ClaimFence: lease.Fence,
	}, nil
}

// communicationWorkspace resolves the authorization workspace of the canonical
// identity and proves that the operated run alias resolves to that same SID. A
// NULL identity workspace means the tenant default, matching the established
// SessionWorkParticipant rule. Filesystem workspace_ref never participates.
func (m *Module) communicationWorkspace(
	ctx context.Context,
	tenant model.TenantID,
	runRef, sid string,
) (model.ID, error) {
	if m.data == nil {
		return "", &runErr{http.StatusServiceUnavailable, "session identity is not available"}
	}
	var workspaceID model.ID
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		workspaceID, err = communicationWorkspaceWithin(ctx, sc, runRef, sid)
		return err
	})
	if err != nil {
		return "", denyClosedErr("communication credential workspace is not provable", err)
	}
	if !validRuntimeUUIDv7(workspaceID.String()) {
		return "", forbiddenErr("communication credential workspace is invalid (fail-closed)")
	}
	return workspaceID, nil
}

func communicationWorkspaceWithin(
	ctx context.Context,
	sc store.Scope,
	runRef, sid string,
) (model.ID, error) {
	alias, found, err := findAlias(ctx, sc, ProviderOperated, runRef)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("sessions: operated run alias is absent")
	}
	resolved, err := resolveMerge(ctx, sc, alias.String(colAliasSID))
	if err != nil {
		return "", err
	}
	if resolved != sid {
		return "", fmt.Errorf("sessions: operated run alias does not match the live Claim")
	}
	identity, found, err := findIdentity(ctx, sc, resolved)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("sessions: canonical session identity is absent")
	}
	if raw := identity.String(colIDWorkspaceID); raw != "" {
		workspaceID, err := model.ParseID(raw)
		if err != nil {
			return "", err
		}
		if _, err := sc.Workspaces().Get(ctx, workspaceID); err != nil {
			return "", err
		}
		return workspaceID, nil
	}
	workspace, err := sc.DefaultWorkspace(ctx)
	if err != nil {
		return "", err
	}
	return workspace.ID, nil
}

func validRuntimeUUIDv7(raw string) bool {
	id, err := uuid.Parse(raw)
	return err == nil && id.Version() == 7 && id.String() == raw
}

func guardRuntimeLaunch(launchID model.ID) func(model.Record) error {
	return func(record model.Record) error {
		if launchID.IsZero() || record.String(colRuntimeLaunchID) != launchID.String() {
			return conflictErr("runtime launch reservation changed")
		}
		return nil
	}
}

type runtimeRecoveryGeneration struct {
	launchID, sid, holder, agentRef string
	fence                           int64
}

func runtimeRecoveryGenerationOf(record model.Record) runtimeRecoveryGeneration {
	return runtimeRecoveryGeneration{
		launchID: record.String(colRuntimeLaunchID),
		sid:      record.String(colRunClaimSID),
		holder:   record.String(colClaimHolder),
		fence:    record.Int(colClaimFence),
		agentRef: record.String(colRunAgentRef),
	}
}

// sameRuntimeRecoveryGeneration compares every durable field that identifies the
// launch authority being retired. Recovery deliberately ignores credential
// handles here: a writer that was already in flight may have persisted them after
// the page snapshot, and those newly durable handles must be picked up and
// revoked. A change to the generation binding itself is different: recovery must
// restart from that newer snapshot so it releases the right Claim before touching
// any of its credentials.
func sameRuntimeRecoveryGeneration(expected runtimeRecoveryGeneration, current model.Record) bool {
	return expected == runtimeRecoveryGenerationOf(current)
}

// guardRuntimeRecoveryTerminal is evaluated inside the same transaction that
// publishes the terminal state, including on its OCC retry. Besides fencing the
// launch generation, it refuses to terminalize if a late writer stamped a Claim
// binding or persisted either credential after recovery's last read/revoke pass.
func guardRuntimeRecoveryTerminal(
	snapshot runtimeRecoveryGeneration,
	workspaceID string,
) func(model.Record) error {
	return func(current model.Record) error {
		if !sameRuntimeRecoveryGeneration(snapshot, current) ||
			current.String(colCommunicationWorkspaceID) != workspaceID {
			return conflictErr("runtime recovery generation changed")
		}
		if current.String(colWorkCredentialID) != "" ||
			current.String(colCommunicationCredentialID) != "" {
			return conflictErr("runtime credentials changed during recovery")
		}
		return nil
	}
}

func (m *Module) assertRuntimeLaunchID(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	launchID model.ID,
) error {
	if m.data == nil {
		return &runErr{http.StatusServiceUnavailable, "session runtime store is not available"}
	}
	return m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		record, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		if record.String(colState) != statePending {
			return conflictErr("runtime launch reservation is no longer pending")
		}
		return guardRuntimeLaunch(launchID)(record)
	})
}

func (m *Module) assertRuntimeIncarnation(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	launchID model.ID,
) error {
	if m.data == nil {
		return &runErr{http.StatusServiceUnavailable, "session runtime store is not available"}
	}
	return m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		record, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		return guardRuntimeLaunch(launchID)(record)
	})
}

const maxRuntimeSessionCredentialTTL = 30 * time.Minute

func (m *Module) maybeMintCommunicationSession(
	ctx context.Context,
	req CommunicationSessionCredentialRequest,
) (CommunicationSessionCredential, error) {
	if m.rt.communicationSessionCreds == nil {
		return CommunicationSessionCredential{}, &runErr{
			http.StatusServiceUnavailable, "communication credential issuer is not available",
		}
	}
	credential, err := m.rt.communicationSessionCreds.Mint(ctx, req)
	if err != nil {
		revokeErr := m.revokeCommunicationSessionCredential(ctx, credential.ID, req)
		return CommunicationSessionCredential{}, errors.Join(denyClosedErr(
			"communication-session credential unavailable",
			secretSafeCredentialError("communication credential mint", err),
		), wrapCredentialCompensation("revoke communication credential returned with mint error", revokeErr))
	}
	if credential.ID.IsZero() || credential.Token == "" || credential.Expired(m.now()) ||
		credential.NotAfter.After(m.now().Add(maxRuntimeSessionCredentialTTL)) ||
		credential.Tenant != req.Tenant || credential.WorkspaceID != req.WorkspaceID ||
		credential.SessionRef != req.SessionRef || credential.RunRef != req.RunRef ||
		credential.AgentRef != req.AgentRef || credential.ClaimFence != req.ClaimFence {
		revokeErr := m.revokeCommunicationSessionCredential(ctx, credential.ID, req)
		return CommunicationSessionCredential{}, errors.Join(
			forbiddenErr("invalid communication-session credential (fail-closed)"),
			wrapCredentialCompensation("revoke invalid communication credential", revokeErr),
		)
	}
	return credential, nil
}

func communicationCredentialRequest(
	credential CommunicationSessionCredential,
) CommunicationSessionCredentialRequest {
	return CommunicationSessionCredentialRequest{
		Tenant: credential.Tenant, WorkspaceID: credential.WorkspaceID,
		SessionRef: credential.SessionRef, RunRef: credential.RunRef,
		AgentRef: credential.AgentRef, ClaimFence: credential.ClaimFence,
	}
}

func (m *Module) revokeCommunicationSessionCredential(
	ctx context.Context,
	id model.ID,
	expected CommunicationSessionCredentialRequest,
) error {
	if id.IsZero() {
		return nil
	}
	source := m.communicationCredentialRevoker(ctx)
	if source == nil {
		return &runErr{http.StatusServiceUnavailable, "communication credential issuer is not available"}
	}
	if err := source.Revoke(context.WithoutCancel(ctx), id, expected); err != nil {
		m.warnf("sessions: could not revoke communication-session credential",
			"credential_id", id.String())
		return secretSafeCredentialError("communication credential revoke", err)
	}
	return nil
}

func wrapCredentialCompensation(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("sessions: compensation %s: %w", operation, err)
}

// setRuntimeCredentialStamp persists only non-sensitive recovery material.
func setRuntimeCredentialStamp(rec model.Record, lease Lease, credentials runtimeCredentials) {
	setOrNull(rec, colRunClaimSID, lease.SID)
	if credentials.communication.WorkspaceID.IsZero() {
		rec[colCommunicationWorkspaceID] = nil
	} else {
		rec[colCommunicationWorkspaceID] = credentials.communication.WorkspaceID.String()
	}
	setCredentialHandle(rec, colWorkCredentialID, colWorkCredentialExpiresAt,
		credentials.work.ID, credentials.work.NotAfter)
	setCredentialHandle(rec, colCommunicationCredentialID, colCommunicationExpiresAt,
		credentials.communication.ID, credentials.communication.NotAfter)
	if credentials.launchID.IsZero() {
		rec[colRuntimeLaunchID] = nil
	} else {
		rec[colRuntimeLaunchID] = credentials.launchID.String()
	}
}

func setCredentialHandle(rec model.Record, idColumn, expiryColumn string, id model.ID, expiry time.Time) {
	if id.IsZero() {
		rec[idColumn], rec[expiryColumn] = nil, nil
		return
	}
	rec[idColumn] = id.String()
	if expiry.IsZero() {
		rec[expiryColumn] = nil
		return
	}
	rec[expiryColumn] = model.NewTimestamp(expiry).String()
}

func credentialExpiry(rec model.Record, column string) time.Time {
	raw := rec.String(column)
	if raw == "" {
		return time.Time{}
	}
	stamp, err := model.ParseTimestamp(raw)
	if err != nil {
		return time.Time{}
	}
	return stamp.Time()
}

// runtimeCredentialsFromRecord reconstructs both exact revoke requests from one
// durable run row. Invalid historical/tampered values are left intact so each
// issuer can refuse them and the recovery path retains the handle for retry.
func runtimeCredentialsFromRecord(tenant model.TenantID, rec model.Record) runtimeCredentials {
	commonSID := rec.String(colRunClaimSID)
	runRef := rec.String(colRunRef)
	agentRef := rec.String(colRunAgentRef)
	fence := rec.Int(colClaimFence)
	return runtimeCredentials{
		launchID: model.ID(rec.String(colRuntimeLaunchID)),
		work: WorkSessionCredential{
			ID: model.ID(rec.String(colWorkCredentialID)), Tenant: tenant,
			SessionRef: commonSID, RunRef: runRef, AgentRef: agentRef,
			ClaimFence: fence, NotAfter: credentialExpiry(rec, colWorkCredentialExpiresAt),
		},
		communication: CommunicationSessionCredential{
			ID: model.ID(rec.String(colCommunicationCredentialID)), Tenant: tenant,
			WorkspaceID: model.ID(rec.String(colCommunicationWorkspaceID)),
			SessionRef:  commonSID, RunRef: runRef, AgentRef: agentRef,
			ClaimFence: fence, NotAfter: credentialExpiry(rec, colCommunicationExpiresAt),
		},
	}
}

type runtimeCredentialRevocation struct {
	workRevoked          bool
	communicationRevoked bool
	err                  error
}

// revokeRuntimeCredentialSet attempts both revocations independently. When a
// run row exists it clears only handles whose issuer confirmed revocation; a
// failed handle remains durable for restart/cleanup retry.
func (m *Module) revokeRuntimeCredentialSet(
	ctx context.Context,
	credentials runtimeCredentials,
	persist bool,
) runtimeCredentialRevocation {
	result := runtimeCredentialRevocation{}
	workID := credentials.work.ID
	communicationID := credentials.communication.ID

	workErr := m.revokeWorkSessionCredential(ctx, workID, WorkSessionCredentialRequest{
		Tenant: credentials.work.Tenant, SessionRef: credentials.work.SessionRef,
		RunRef: credentials.work.RunRef, AgentRef: credentials.work.AgentRef,
		ClaimFence: credentials.work.ClaimFence,
	})
	communicationErr := m.revokeCommunicationSessionCredential(
		ctx, communicationID, communicationCredentialRequest(credentials.communication),
	)
	result.workRevoked = !workID.IsZero() && workErr == nil
	result.communicationRevoked = !communicationID.IsZero() && communicationErr == nil

	var clearErr error
	if persist && (result.workRevoked || result.communicationRevoked) {
		tenant, runRef := credentials.work.Tenant, credentials.work.RunRef
		if tenant.IsZero() || runRef == "" {
			tenant, runRef = credentials.communication.Tenant, credentials.communication.RunRef
		}
		clearErr = m.clearStoredRuntimeCredentialHandles(
			ctx, tenant, runRef, workID, communicationID,
			result.workRevoked, result.communicationRevoked,
		)
		if clearErr != nil {
			// A successful issuer call with an uncleared durable handle is still a
			// recovery obligation. Keep both in-memory handles too so shutdown can retry.
			result.workRevoked = false
			result.communicationRevoked = false
		}
	}
	result.err = errors.Join(
		wrapCredentialCompensation("revoke work-session credential", workErr),
		wrapCredentialCompensation("revoke communication-session credential", communicationErr),
		wrapCredentialCompensation("clear revoked runtime credential handles", clearErr),
	)
	return result
}

func (m *Module) clearStoredRuntimeCredentialHandles(
	ctx context.Context,
	tenant model.TenantID,
	runRef string,
	workID, communicationID model.ID,
	clearWork, clearCommunication bool,
) error {
	data := m.runtimeData(ctx)
	if data == nil || (!clearWork && !clearCommunication) {
		return nil
	}
	attempt := func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		record, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		changed := false
		if clearWork && record.String(colWorkCredentialID) == workID.String() {
			record[colWorkCredentialID], record[colWorkCredentialExpiresAt] = nil, nil
			changed = true
		}
		if clearCommunication && record.String(colCommunicationCredentialID) == communicationID.String() {
			record[colCommunicationCredentialID], record[colCommunicationExpiresAt] = nil, nil
			changed = true
		}
		if !changed {
			return nil
		}
		_, err = repo.Update(ctx, record)
		return err
	}
	err := data.Mutate(context.WithoutCancel(ctx), tenant, attempt)
	if errors.Is(err, store.ErrConflict) {
		err = data.Mutate(context.WithoutCancel(ctx), tenant, attempt)
	}
	return err
}

func (m *Module) revokeLiveRuntimeCredentials(ctx context.Context, lr *liveRun) error {
	if lr == nil {
		return nil
	}
	lr.mu.Lock()
	credentials := runtimeCredentials{
		work: WorkSessionCredential{
			ID: lr.workCredentialID, Tenant: lr.tenant, SessionRef: lr.claim.SID,
			RunRef: lr.runRef, AgentRef: lr.agentRef, ClaimFence: lr.claim.Fence,
			NotAfter: lr.workCredentialNotAfter,
		},
		communication: CommunicationSessionCredential{
			ID: lr.communicationCredentialID, Tenant: lr.tenant,
			WorkspaceID: lr.communicationWorkspaceID, SessionRef: lr.claim.SID,
			RunRef: lr.runRef, AgentRef: lr.agentRef, ClaimFence: lr.claim.Fence,
			NotAfter: lr.communicationCredentialNotAfter,
		},
	}
	lr.mu.Unlock()
	var durableLookupErr error
	if credentials.work.ID.IsZero() || credentials.communication.ID.IsZero() {
		// The in-memory pair is an acceleration, not the recovery source of
		// truth. If one side was lost/corrupted, reload the durable pair under the
		// exact live incarnation so failure handling still withdraws both grants.
		record, err := m.loadRun(ctx, lr.tenant, lr.runRef)
		if err != nil {
			durableLookupErr = err
		} else if credentials.work.ID.IsZero() && credentials.communication.ID.IsZero() &&
			record.String(colWorkCredentialID) == "" &&
			record.String(colCommunicationCredentialID) == "" {
			// A prior synchronous revoke already cleared both live and durable
			// handles. Finalize may since have cleared runtime_launch_id, so this
			// idempotent retry must not misclassify the terminal row as a foreign
			// incarnation. If either durable handle reappears, the exact binding
			// checks below still apply and fail closed.
			return nil
		} else if record.String(colRuntimeLaunchID) != lr.launchID.String() ||
			record.String(colRunClaimSID) != lr.claim.SID ||
			record.String(colClaimHolder) != lr.claim.Holder ||
			record.Int(colClaimFence) != lr.claim.Fence ||
			record.String(colRunAgentRef) != lr.agentRef ||
			record.String(colCommunicationWorkspaceID) != lr.communicationWorkspaceID.String() {
			durableLookupErr = conflictErr(
				"durable runtime credential binding no longer matches live process",
			)
		} else {
			durable := runtimeCredentialsFromRecord(lr.tenant, record)
			if credentials.work.ID.IsZero() {
				credentials.work = durable.work
			}
			if credentials.communication.ID.IsZero() {
				credentials.communication = durable.communication
			}
		}
	}
	result := m.revokeRuntimeCredentialSet(ctx, credentials, true)
	lr.mu.Lock()
	if result.workRevoked && lr.workCredentialID == credentials.work.ID {
		lr.workCredentialID = ""
		lr.workCredentialNotAfter = time.Time{}
	}
	if result.communicationRevoked &&
		lr.communicationCredentialID == credentials.communication.ID {
		lr.communicationCredentialID = ""
		lr.communicationCredentialNotAfter = time.Time{}
	}
	lr.mu.Unlock()
	return errors.Join(
		result.err,
		wrapCredentialCompensation("load missing durable runtime credential", durableLookupErr),
	)
}

func (m *Module) revokeStoredRuntimeCredentials(
	ctx context.Context,
	tenant model.TenantID,
	record model.Record,
) error {
	credentials := runtimeCredentialsFromRecord(tenant, record)
	return m.revokeRuntimeCredentialSet(ctx, credentials, true).err
}

// validateRuntimeCredentialStampWithin is the last pre-spawn binding check. It
// runs in the same transaction as the durable handles, after authorizedMutate
// has placed the exact Claim holder/fence in the OCC write set.
func validateRuntimeCredentialStampWithin(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	runRef, agentRef string,
	lease Lease,
	credentials runtimeCredentials,
) error {
	if credentials.work.ID.IsZero() && credentials.communication.ID.IsZero() {
		return nil
	}
	if credentials.work.Tenant != tenant || credentials.work.SessionRef != lease.SID ||
		credentials.work.RunRef != runRef || credentials.work.AgentRef != agentRef ||
		credentials.work.ClaimFence != lease.Fence {
		return fmt.Errorf("sessions: work credential binding changed before launch")
	}
	if credentials.communication.ID.IsZero() {
		return nil // the legacy work-only runtime, not K3
	}
	workspaceID, err := communicationWorkspaceWithin(ctx, sc, runRef, lease.SID)
	if err != nil {
		return err
	}
	if credentials.communication.Tenant != tenant ||
		credentials.communication.WorkspaceID != workspaceID ||
		credentials.communication.SessionRef != lease.SID ||
		credentials.communication.RunRef != runRef ||
		credentials.communication.AgentRef != agentRef ||
		credentials.communication.ClaimFence != lease.Fence {
		return fmt.Errorf("sessions: communication credential binding changed before launch")
	}
	return nil
}

func (m *Module) persistResumeRuntimeCredentials(
	ctx context.Context,
	tenant model.TenantID,
	runRef, agentRef string,
	lease Lease,
	credentials runtimeCredentials,
) error {
	if m.rt.communicationCredentialsEnabled && !credentials.complete(m.now()) {
		return &runErr{http.StatusServiceUnavailable, "complete dual runtime credentials are required before launch"}
	}
	return m.authorizedMutate(ctx, tenant, lease, func(sc store.Scope) error {
		if err := validateRuntimeCredentialStampWithin(
			ctx, sc, tenant, runRef, agentRef, lease, credentials,
		); err != nil {
			return err
		}
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		record, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		if record.String(colState) != statePending {
			return errIllegalTransition
		}
		if record.String(colRuntimeLaunchID) != credentials.launchID.String() {
			return conflictErr("runtime launch reservation changed")
		}
		if record.String(colWorkCredentialID) != "" ||
			record.String(colCommunicationCredentialID) != "" {
			return conflictErr("runtime credential reservation is already occupied")
		}
		setClaimStamp(record, lease)
		setOrNull(record, colRunAgentRef, agentRef)
		setRuntimeCredentialStamp(record, lease, credentials)
		_, err = repo.Update(ctx, record)
		return err
	})
}

func (m *Module) stampResumeClaimReservation(
	ctx context.Context,
	tenant model.TenantID,
	runRef, agentRef string,
	lease Lease,
	launchID model.ID,
) error {
	return m.authorizedMutate(ctx, tenant, lease, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		record, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		if record.String(colState) != statePending {
			return errIllegalTransition
		}
		if err := guardRuntimeLaunch(launchID)(record); err != nil {
			return err
		}
		if m.rt.communicationCredentialsEnabled {
			workspaceID, err := communicationWorkspaceWithin(ctx, sc, runRef, lease.SID)
			if err != nil {
				return err
			}
			record[colCommunicationWorkspaceID] = workspaceID.String()
		}
		setClaimStamp(record, lease)
		setOrNull(record, colRunClaimSID, lease.SID)
		setOrNull(record, colRunAgentRef, agentRef)
		_, err = repo.Update(ctx, record)
		return err
	})
}

type liveRuntimeCredentialSnapshot struct {
	tenant                        model.TenantID
	runRef, agentRef              string
	claim                         Lease
	launchID                      model.ID
	workspaceID                   model.ID
	workID, communicationID       model.ID
	workUntil, communicationUntil time.Time
}

func snapshotLiveRuntimeCredentials(lr *liveRun) liveRuntimeCredentialSnapshot {
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return liveRuntimeCredentialSnapshot{
		tenant: lr.tenant, runRef: lr.runRef, agentRef: lr.agentRef,
		claim: lr.claim, launchID: lr.launchID, workspaceID: lr.communicationWorkspaceID,
		workID: lr.workCredentialID, communicationID: lr.communicationCredentialID,
		workUntil:          lr.workCredentialNotAfter,
		communicationUntil: lr.communicationCredentialNotAfter,
	}
}

func (m *Module) persistRenewedRuntimeCredentialExpiries(
	ctx context.Context,
	snapshot liveRuntimeCredentialSnapshot,
	workUntil, communicationUntil time.Time,
) error {
	return m.authorizedMutate(ctx, snapshot.tenant, snapshot.claim, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		record, err := findRunRec(ctx, repo, snapshot.runRef)
		if err != nil {
			return err
		}
		if record.String(colRuntimeLaunchID) != snapshot.launchID.String() ||
			record.String(colRunClaimSID) != snapshot.claim.SID ||
			record.String(colClaimHolder) != snapshot.claim.Holder ||
			record.Int(colClaimFence) != snapshot.claim.Fence ||
			record.String(colRunAgentRef) != snapshot.agentRef ||
			record.String(colCommunicationWorkspaceID) != snapshot.workspaceID.String() ||
			record.String(colWorkCredentialID) != snapshot.workID.String() ||
			record.String(colCommunicationCredentialID) != snapshot.communicationID.String() {
			return fmt.Errorf("sessions: durable runtime credential binding no longer matches live process")
		}
		workspaceID, err := communicationWorkspaceWithin(ctx, sc, snapshot.runRef, snapshot.claim.SID)
		if err != nil || workspaceID != snapshot.workspaceID {
			if err != nil {
				return err
			}
			return fmt.Errorf("sessions: communication workspace changed while process was live")
		}
		record[colWorkCredentialExpiresAt] = model.NewTimestamp(workUntil).String()
		record[colCommunicationExpiresAt] = model.NewTimestamp(communicationUntil).String()
		_, err = repo.Update(ctx, record)
		return err
	})
}

func (m *Module) startRuntimeCredentialHeartbeat(lr *liveRun) {
	if !m.rt.communicationCredentialsEnabled || m.rt.credentialHeartbeatInterval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	lr.mu.Lock()
	if lr.finalized || lr.credentialHeartbeatCancel != nil {
		lr.mu.Unlock()
		cancel()
		return
	}
	lr.credentialHeartbeatCancel = cancel
	lr.mu.Unlock()
	go func() {
		ticker := time.NewTicker(m.rt.credentialHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.renewLaunchClaim(ctx, lr)
			}
		}
	}()
}

func (m *Module) stopRuntimeCredentialHeartbeat(lr *liveRun) {
	if lr == nil {
		return
	}
	lr.mu.Lock()
	cancel := lr.credentialHeartbeatCancel
	lr.credentialHeartbeatCancel = nil
	lr.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Module) renewDualRuntimeCredentials(ctx context.Context, lr *liveRun) {
	lr.mu.Lock()
	if lr.finalized || lr.stopRequested || lr.runtimeHeartbeatRunning {
		lr.mu.Unlock()
		return
	}
	lr.runtimeHeartbeatRunning = true
	lr.mu.Unlock()
	defer func() {
		lr.mu.Lock()
		lr.runtimeHeartbeatRunning = false
		lr.mu.Unlock()
	}()

	if err := m.assertRuntimeIncarnation(ctx, lr.tenant, lr.runRef, lr.launchID); err != nil {
		m.terminateForRuntimeCredentialFailure(lr, "runtime incarnation lost")
		return
	}
	if _, err := m.Heartbeat(
		ctx, lr.tenant, lr.claim.SID, lr.claim.Holder, lr.claim.Fence, 0,
	); err != nil {
		m.warnf("sessions: launch Claim heartbeat failed; stopping process", "run_ref", lr.runRef)
		m.terminateForRuntimeCredentialFailure(lr, "runtime Claim heartbeat failed")
		return
	}

	snapshot := snapshotLiveRuntimeCredentials(lr)
	now := m.now()
	if snapshot.workID.IsZero() || snapshot.communicationID.IsZero() ||
		snapshot.workspaceID.IsZero() || m.rt.workSessionCreds == nil ||
		m.rt.communicationSessionCreds == nil {
		m.terminateForRuntimeCredentialFailure(lr, "runtime credential pair is incomplete")
		return
	}
	if snapshot.workUntil.Sub(now) > workSessionCredentialRenewWindow &&
		snapshot.communicationUntil.Sub(now) > workSessionCredentialRenewWindow {
		return
	}

	workUntil, workErr := m.rt.workSessionCreds.Renew(
		context.WithoutCancel(ctx), snapshot.workID, WorkSessionCredentialRequest{
			Tenant: snapshot.tenant, SessionRef: snapshot.claim.SID,
			RunRef: snapshot.runRef, AgentRef: snapshot.agentRef,
			ClaimFence: snapshot.claim.Fence,
		},
	)
	communicationUntil, communicationErr := m.rt.communicationSessionCreds.Renew(
		context.WithoutCancel(ctx), snapshot.communicationID,
		CommunicationSessionCredentialRequest{
			Tenant: snapshot.tenant, WorkspaceID: snapshot.workspaceID,
			SessionRef: snapshot.claim.SID, RunRef: snapshot.runRef,
			AgentRef: snapshot.agentRef, ClaimFence: snapshot.claim.Fence,
		},
	)
	workErr = secretSafeCredentialError("work credential renew", workErr)
	communicationErr = secretSafeCredentialError("communication credential renew", communicationErr)
	validatedAt := m.now()
	if workErr == nil && (!workUntil.After(validatedAt) || !workUntil.After(snapshot.workUntil) ||
		workUntil.After(validatedAt.Add(maxRuntimeSessionCredentialTTL))) {
		workErr = errors.New("sessions: work credential renewal returned an invalid deadline")
	}
	if communicationErr == nil && (!communicationUntil.After(validatedAt) ||
		!communicationUntil.After(snapshot.communicationUntil) ||
		communicationUntil.After(validatedAt.Add(maxRuntimeSessionCredentialTTL))) {
		communicationErr = errors.New("sessions: communication credential renewal returned an invalid deadline")
	}
	if workErr != nil || communicationErr != nil {
		m.warnf("sessions: dual runtime credential renewal failed; stopping process",
			"run_ref", lr.runRef)
		m.terminateForRuntimeCredentialFailure(lr, "runtime credential renewal failed")
		return
	}
	if err := m.persistRenewedRuntimeCredentialExpiries(
		context.WithoutCancel(ctx), snapshot, workUntil, communicationUntil,
	); err != nil {
		m.warnf("sessions: renewed runtime credential deadlines could not be persisted; stopping process",
			"run_ref", lr.runRef)
		m.terminateForRuntimeCredentialFailure(lr, "runtime credential renewal persistence failed")
		return
	}
	lr.mu.Lock()
	if lr.launchID == snapshot.launchID && lr.workCredentialID == snapshot.workID &&
		lr.communicationCredentialID == snapshot.communicationID && !lr.stopRequested {
		lr.workCredentialNotAfter = workUntil
		lr.communicationCredentialNotAfter = communicationUntil
	}
	lr.mu.Unlock()
}

// terminateForRuntimeCredentialFailure runs outside the bridge output loop.
// Process.Stop may wait for output pumps to drain; keeping the bridge available
// avoids the Stop<->pump deadlock while preserving stop-before-revoke ordering.
func (m *Module) terminateForRuntimeCredentialFailure(lr *liveRun, reason string) {
	lr.mu.Lock()
	if lr.finalized || lr.stopRequested {
		lr.mu.Unlock()
		return
	}
	lr.stopRequested = true
	lr.stopReason = reason
	lr.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*m.rt.waitDelay+30*time.Second)
		defer cancel()
		var stopErr error
		if lr.proc != nil {
			stopErr = secretSafeCredentialError("session process stop after credential failure", lr.proc.Stop(ctx))
		}
		lr.cancel()
		revokeErr := m.revokeLiveRuntimeCredentials(ctx, lr)
		if stopErr != nil || revokeErr != nil {
			m.warnf("sessions: runtime credential failure compensation incomplete",
				"run_ref", lr.runRef)
		}
	}()
}

// recoverLocalRuntimeProcess is stricter than ordinary post-launch teardown.
// A failed Stop during promotion must retain both the live handle and durable
// incarnation so the next promotion retries the same child; otherwise a later
// scan could see a terminal row with no handles and incorrectly admit a leader
// while the old process was still alive.
func (m *Module) recoverLocalRuntimeProcess(ctx context.Context, lr *liveRun) error {
	lr.stopDeadline()
	m.stopRuntimeCredentialHeartbeat(lr)
	stopErr := secretSafeCredentialError(
		"session process promotion-recovery stop",
		lr.proc.Stop(context.WithoutCancel(ctx)),
	)
	// Revocation is independent of Stop success and always follows the attempt.
	revokeErr := m.revokeLiveRuntimeCredentials(ctx, lr)
	if stopErr == nil {
		lr.cancel()
		m.rt.dropLive(lr.tenant, lr.runRef)
	}
	return errors.Join(
		wrapCredentialCompensation("stop local process during promotion recovery", stopErr),
		revokeErr,
	)
}

// RecoverRuntimeCredentials is called by the composition root for every tenant
// while assuming leadership and before that leader serves lifecycle traffic. It
// retries both durable handles independently. It also evicts a durable launch
// reservation even when a crash happened before either handle was written; a
// failed handle remains stored and the row remains non-terminal for the next
// promotion/recovery attempt.
func (m *Module) RecoverRuntimeCredentials(ctx context.Context, tenant model.TenantID) error {
	if !m.rt.communicationCredentialsEnabled {
		return nil
	}
	data := m.recoveryData
	if data == nil {
		data = m.data
	}
	if data == nil {
		return &runErr{http.StatusServiceUnavailable, "session runtime store is not available"}
	}
	ctx = context.WithValue(ctx, runtimeCredentialRecoveryContextKey{}, struct{}{})
	var recoveryErrs []error
	query := model.Query{Limit: 200}
	seenCursors := map[string]struct{}{}
	const maxRecoveryPages = 100_000
	pages := 0
	for {
		pages++
		if pages > maxRecoveryPages {
			return errors.Join(errors.Join(recoveryErrs...), errors.New(
				"sessions: runtime credential recovery exceeded the pagination limit",
			))
		}
		var (
			records []model.Record
			page    model.Page
		)
		if err := data.View(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			var listErr error
			records, page, listErr = repo.List(ctx, query)
			return listErr
		}); err != nil {
			return errors.Join(errors.Join(recoveryErrs...), err)
		}
		for _, record := range records {
			state := record.String(colState)
			hasHandles := record.String(colWorkCredentialID) != "" ||
				record.String(colCommunicationCredentialID) != ""
			hasReservation := record.String(colRuntimeLaunchID) != "" &&
				(state == statePending || state == stateRunning)
			if !hasHandles && !hasReservation {
				continue
			}
			var recoveryErr error
			if state == statePending || state == stateRunning {
				// A quick demotion/re-promotion can leave this same Module holding
				// the old process. Recovery is a process boundary too: remove it from
				// attach visibility, stop/cancel it, and only then revoke authority.
				if live, ok := m.rt.getLive(tenant, record.String(colRunRef)); ok {
					recoveryErr = m.recoverLocalRuntimeProcess(ctx, live)
					if recoveryErr != nil {
						recoveryErrs = append(recoveryErrs, fmt.Errorf(
							"recover local runtime process for run %s: %w",
							record.String(colRunRef), recoveryErr,
						))
						continue
					}
				}
				fresh, loadErr := m.loadRun(ctx, tenant, record.String(colRunRef))
				if loadErr != nil {
					recoveryErr = errors.Join(recoveryErr, loadErr)
				} else {
					record = fresh
					state = record.String(colState)
				}
			}
			var err error
			if state == statePending || state == stateRunning {
				_, err = m.reconcileTerminal(
					ctx, tenant, record.String(colRunRef), record,
					model.ActorSystem, model.ActorSystem,
				)
			} else {
				err = m.revokeStoredRuntimeCredentials(ctx, tenant, record)
			}
			err = errors.Join(recoveryErr, err)
			if err != nil {
				recoveryErrs = append(recoveryErrs, fmt.Errorf(
					"recover runtime credentials for run %s: %w", record.String(colRunRef), err,
				))
			}
		}
		if !page.HasMore {
			break
		}
		if page.Cursor == "" {
			return errors.Join(errors.Join(recoveryErrs...), errors.New(
				"sessions: runtime credential recovery returned an empty continuation cursor",
			))
		}
		if _, repeated := seenCursors[page.Cursor]; repeated {
			return errors.Join(errors.Join(recoveryErrs...), errors.New(
				"sessions: runtime credential recovery returned a repeated continuation cursor",
			))
		}
		seenCursors[page.Cursor] = struct{}{}
		query.Cursor = page.Cursor
	}
	return errors.Join(recoveryErrs...)
}
