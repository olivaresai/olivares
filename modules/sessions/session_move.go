// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Moving a session to another profile of the same tool (#1845).
//
// A stopped session is bound to the profile it was launched under, and resume
// refuses any other home on purpose (revalidateStoredProfile): the conversation
// lives in that home, and a different account must never be a silent change. A move
// is the explicit, recorded act that changes the binding:
//
//  1. the tool's own state for THIS conversation is carried to the target home
//     (claude_session_state.go): the transcript and its subagent and tool-result
//     files, never a login or another conversation;
//  2. the run row, the managed live row and the ledger move together in one
//     transaction, so the session keeps its id, its history and its audit chain;
//  3. nothing starts. The next resume is the ordinary one, so the target profile's
//     policy, the budgets and the approvals apply to it exactly as they would to
//     any launch under that profile.
//
// The files go first and the binding second. If the binding fails the target holds
// an extra copy and the session is exactly where it was; the other order would
// leave a session bound to a home that cannot resume it.

// eventMoved is the ledger event of a move. Its detail names both profiles by their
// opaque reference: the ledger is append-only, and an account name may be an email
// address that must not be sealed into it.
const eventMoved = "moved"

// moveDetail is the ledger detail of a move; the console reads both references
// back from it.
func moveDetail(from, to string, carried claudeStateCopy) string {
	return fmt.Sprintf("moved from %s to %s: %d files, %d bytes", from, to, carried.Files, carried.Bytes)
}

// moveRunRequest is the POST /runs/{ref}/move body. Only the reference: the homes,
// driver and authorization are resolved by the server from the profile row.
type moveRunRequest struct {
	ProviderProfileRef string `json:"provider_profile_ref"`
}

// moveRun moves the stopped run runRef to the profile targetRef. It never starts a
// process and never mints a credential.
func (m *Module) moveRun(ctx context.Context, tenant model.TenantID, runRef, targetRef, actor, actorKind string) (runDTO, error) {
	targetRef = strings.TrimSpace(targetRef)
	if !validProfileRef(targetRef) {
		return runDTO{}, badRequest("provider_profile_ref names the profile to move the session to")
	}
	// Serialized with stop, resume and cleanup on this run; a caller that is gone
	// does not wait for it.
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, runRef))
	if err != nil {
		return runDTO{}, err
	}
	defer release()
	rec, err := m.loadRun(ctx, tenant, runRef)
	if err != nil {
		return runDTO{}, err
	}
	if err := m.refuseLegacyControlUnderWork(ctx, tenant, rec); err != nil {
		return runDTO{}, err
	}
	state := rec.String(colState)
	if state != stateStopped && state != stateFailed {
		return runDTO{}, conflictErr("session must be stopped to move (state=" + state + ")")
	}
	if err := m.refuseUncollectedProcess(tenant, runRef, state, "move"); err != nil {
		return runDTO{}, err
	}
	src := storedProfileSnapshot(rec)
	if src.ProfileID == "" {
		return runDTO{}, conflictErr("legacy session has no proven provider home and cannot be moved")
	}
	if src.Driver != providerDriverClaude {
		// Only Claude Code's state layout is known here, and no other tool's
		// conversation is read by this runtime, so there is nothing to carry.
		return runDTO{}, &runErr{http.StatusUnprocessableEntity,
			"only Claude Code sessions can be moved; this session's tool keeps its conversation another way"}
	}
	// What a session printed may hold what its vault secrets, repository
	// credential and package registry tokens were. The target profile may reach another provider endpoint, and
	// the next resume sends it the whole history: it is refused like a handoff.
	if err := m.refuseSecretBearing(ctx, tenant, rec, "moved"); err != nil {
		return runDTO{}, err
	}
	if err := m.provenSourceHome(ctx, tenant, src); err != nil {
		return runDTO{}, err
	}
	// The target gets every check a launch under it gets: active, this environment,
	// an operable driver, homes that still resolve, an authorized source, and no
	// foreign tool login.
	dst, _, err := m.resolveLaunchProfile(ctx, tenant, targetRef)
	if err != nil {
		return runDTO{}, err
	}
	if dst.ProfileID == src.ProfileID {
		return runDTO{}, conflictErr("the session already runs under this profile")
	}
	if dst.Driver != src.Driver {
		return runDTO{}, conflictErr("the target profile belongs to another tool; use a handoff to continue in it")
	}

	var carried claudeStateCopy
	if sid := rec.String(colClaudeSessionID); sid != "" {
		if err := m.refuseBoundScopedAlias(ctx, tenant, dst, sid, rec.String(colRunClaimSID)); err != nil {
			return runDTO{}, err
		}
		carried, err = copyClaudeSessionState(ctx, src.ConfigHome, dst.ConfigHome, sid)
		if err != nil {
			return runDTO{}, err
		}
	}
	// One transaction: the binding on the run row, the managed live row and the
	// ledger event. The guard re-reads the row, so a move that lost a race to
	// another move commits nothing. A binding that does not commit leaves no files
	// this move made in the target home.
	if _, err := m.transition(ctx, tenant, runRef, transitionInput{
		event: eventMoved, detail: moveDetail(src.ProfileID, dst.ProfileID, carried),
		actor: actor, actorKind: actorKind,
		// A failed session keeps the reason it failed; a move is not a new outcome.
		keepReason: true,
		guard: func(cur model.Record) error {
			if cur.String(colRunProfileID) != src.ProfileID {
				return conflictErr("the session's profile changed while it was being moved")
			}
			return nil
		},
		// The limit was the old account's; the next turn finds out about the new one.
		mutate: func(cur model.Record) { setProfileSnapshot(cur, &dst); cur[colRunUsageLimit] = nil },
		within: func(ctx context.Context, sc store.Scope, _ model.Record) error {
			return retargetManagedLive(ctx, sc, runRef, dst.ProfileID)
		},
	}); err != nil {
		removeClaudeStateFiles(dst.ConfigHome, carried.created)
		return runDTO{}, err
	}
	return m.getRun(ctx, tenant, runRef)
}

// refuseSecretBearing refuses a session that was given vault secrets, a repository
// credential or a package registry token, for an act that carries its stored
// conversation somewhere new. The stored values are read fail-closed: one that
// cannot be decoded refuses too.
func (m *Module) refuseSecretBearing(ctx context.Context, tenant model.TenantID, rec model.Record, did string) error {
	if err := refuseGivenSecrets(rec, did); err != nil {
		return err
	}
	if m.usedRegistryTokens(ctx, tenant, rec) {
		return conflictErr("this session used a package registry token; " +
			"what it printed cannot be proven free of it, so it is not " + did + " to another account or tool")
	}
	return nil
}

// givenSecrets reports whether the run was given vault secrets or a repository
// credential. A value that cannot be decoded counts as given.
func givenSecrets(rec model.Record) bool {
	secrets, err := decodeSecretEnv(rec.String(colRunSecretEnv))
	return err != nil || len(secrets) != 0 || rec.String(colRunGitRead) != ""
}

// refuseGivenSecrets refuses a session that was given vault secrets or a repository
// credential. Current vault values cannot redact what an earlier generation of the
// session printed, and a read token expires, so these are refused live or not.
func refuseGivenSecrets(rec model.Record, did string) error {
	if givenSecrets(rec) {
		return conflictErr("this session was given vault secrets or a repository credential; " +
			"what it printed cannot be proven free of them, so it is not " + did + " to another account or tool")
	}
	return nil
}

// provenSourceHome proves that the home a stored snapshot names is the one its
// profile registered on THIS node, before the engine reads a conversation from it.
// The run row alone is not proof of a path. A disabled or retired profile is still
// a valid source: leaving a profile is a reason to move or hand off.
func (m *Module) provenSourceHome(ctx context.Context, tenant model.TenantID, src ProviderHomeSnapshot) error {
	env, err := m.localEnvironment()
	if err != nil {
		return err
	}
	if src.EnvironmentRef != env {
		return &runErr{http.StatusConflict, "the session ran in another execution environment; its conversation is not on this node"}
	}
	profile, err := m.GetProfile(ctx, tenant, src.ProfileID)
	if err != nil {
		return err
	}
	if profile.ConfigHome != src.ConfigHome || profile.UserHome != src.UserHome ||
		profile.Driver != src.Driver || profile.EnvironmentRef != src.EnvironmentRef {
		return &runErr{http.StatusConflict, "the run's persisted home does not match its profile; refusing to read from an unproven home"}
	}
	return revalidateHome("config_home", src.ConfigHome)
}

// refuseBoundScopedAlias refuses a move whose conversation id is already bound,
// under the target profile, to a different Olivares session: the id would then
// resolve to two sessions there.
func (m *Module) refuseBoundScopedAlias(ctx context.Context, tenant model.TenantID, dst ProviderHomeSnapshot, sid, claimSID string) error {
	alias, found, err := m.LookupScopedAlias(ctx, tenant, dst.ProfileID, dst.Driver, sid)
	if err != nil {
		return err
	}
	if found && alias.SID != claimSID {
		return conflictErr("this conversation id is already bound to another session under the target profile")
	}
	return nil
}

// retargetManagedLive points the plane's own live row of runRef at the profile the
// run now belongs to, so observations, usage and the live view attribute the
// session to the account it runs under. A run with no managed row has nothing to
// move. The row's scope is unchanged: it keys on the canonical session, not on the
// profile.
func retargetManagedLive(ctx context.Context, sc store.Scope, runRef, profileID string) error {
	repo, err := sc.Ext(liveKind)
	if err != nil {
		return err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colLiveRunRef, runRef)}, Limit: 1})
	if err != nil || len(recs) == 0 {
		return err
	}
	recs[0][colLiveProfileID] = profileID
	_, err = repo.Update(ctx, recs[0])
	return err
}
