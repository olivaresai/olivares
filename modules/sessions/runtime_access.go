// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"slices"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// changeRunAccess replaces an idle process; its confinement is never mutated.
// Resume revalidates the profile, template, caller and launch policy as usual.
func (m *Module) changeRunAccess(ctx context.Context, tenant model.TenantID, ref, actor, actorKind, agentIdentity, mode string, asks callerAsks) (runDTO, error) {
	if !validPermissionModes[mode] {
		return runDTO{}, badRequest("unknown permission_mode")
	}
	if needsRunAdministration(mode) {
		allowed := asks.runUnrestricted != nil && asks.runUnrestricted()
		if err := refuseUnrestrictedFor(mode, allowed); err != nil {
			return runDTO{}, err
		}
	}
	release, err := m.rt.lockRunContext(ctx, liveKey(tenant, ref))
	if err != nil {
		return runDTO{}, err
	}
	defer release()
	rec, err := m.loadRun(ctx, tenant, ref)
	if err != nil {
		return runDTO{}, err
	}
	if err := m.refuseLegacyControlUnderWork(ctx, tenant, rec); err != nil {
		return runDTO{}, err
	}
	if rec.String(colClaudeSessionID) == "" {
		return runDTO{}, conflictErr("this tool has no conversation to resume; start a new session with this access")
	}
	if _, err := m.accessTemplate(ctx, tenant, rec.String(colTemplateID), mode, asks); err != nil {
		return runDTO{}, err
	}
	if rec.String(colState) == stateRunning {
		if m.queueHold(rec) != "" {
			return runDTO{}, conflictErr("wait for the current turn to finish before applying access")
		}
		if _, _, err := m.stopRunLoaded(ctx, tenant, ref, actor, actorKind, "access changed for the next turn", rec); err != nil {
			return runDTO{}, err
		}
	}
	return m.resumeRunLocked(ctx, tenant, ref, actor, actorKind, agentIdentity, false, asks, mode)
}

// The built-in access preset is a user choice unless the caller's standing requires it.
// Custom and required templates remain policy.
func (m *Module) accessTemplate(ctx context.Context, tenant model.TenantID, id, mode string, asks callerAsks) (string, error) {
	if id != "" {
		tpl, err := m.loadTemplate(ctx, tenant, id)
		if err != nil {
			return "", err
		}
		required := false
		if tpl.Builtin && tpl.Name == EditsAndCommandsTemplate && asks.standing != nil {
			st, err := asks.standing()
			if err != nil {
				return "", m.errStandardsUnreadable(err)
			}
			refs, err := m.rt.Standards.AssignedTo(ctx, tenant, st.principal, st.departments, assignedRequired)
			if err != nil {
				return "", m.errStandardsUnreadable(err)
			}
			required = slices.Contains(refs, id)
		}
		if !tpl.Builtin || tpl.Name != EditsAndCommandsTemplate || required {
			terms := templateTerms(tpl.Body)
			required := terms.permissionMode
			if len(terms.allowedTools) > 0 {
				if mode != permModeDontAsk {
					return "", forbiddenErr("this template requires its tool allowlist; its access cannot be changed")
				}
				required = permModeDontAsk
			}
			if required != "" && permissionRank(mode) > permissionRank(required) {
				return "", forbiddenErr("the session template forbids this access")
			}
			return id, nil
		}
	}
	if mode != permModeDontAsk {
		return "", nil
	}
	m.ensureBuiltins(ctx, tenant)
	var found string
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(templateKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colTplName, EditsAndCommandsTemplate), model.Filter{Column: colTplBuiltin, Op: model.OpEq, Value: true}}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return conflictErr("the edits and commands preset is unavailable")
		}
		found = rows[0].String(model.ColID)
		return nil
	})
	return found, err
}
