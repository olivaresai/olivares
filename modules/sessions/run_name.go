// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// maxRunNameRunes bounds a session's name.
const maxRunNameRunes = 200

// checkRunName is the one rule for every write of a session's name: a launch, a
// handoff, a child launch and a rename (run_name_archive.go) all pass through it,
// so a name the rename refuses is refused at creation as well. The name is already
// trimmed. A control, format (zero-width, direction overrides), line or paragraph
// separator or default-ignorable character has no use in a label, and with one a
// name can pass for another in a list.
func checkRunName(name string) error {
	if utf8.RuneCountInString(name) > maxRunNameRunes || strings.IndexFunc(name, invisibleInName) >= 0 {
		return badRequest("name must be at most 200 characters and hold no control or invisible characters")
	}
	return nil
}

// invisibleInName is a character a session's name refuses: a control, format or
// separator character, or one Unicode lists as ignorable or invisible by default
// (Hangul fillers, variation selectors), with which a name can look blank. The
// braille blank (U+2800) is a symbol that draws nothing, so it is refused too.
func invisibleInName(r rune) bool {
	return unicode.IsControl(r) || r == '\u2800' || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp,
		unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector)
}

// A name is held by every session that is not cleaned and carries it: a name
// picks one session, the rule `session start` and `session rename` always kept
// in the CLI. A cleaned session holds nothing, so its name is free again, as the
// CLI's freeSessionName allows. The check reads through the caller's own scope,
// so a workspace-confined writer holds names within its workspace only: probing
// the whole tenant from a confined context would tell it names it cannot see.

// refuseHeldRunName refuses a launch or a handoff under a name a session holds,
// before anything is started or stopped. The write paths repeat the check under
// the run-name lock (refuseHeldRunNameIn), where a racing writer is caught.
func (m *Module) refuseHeldRunName(ctx context.Context, tenant model.TenantID, name string) error {
	held, err := m.runNameHeld(ctx, tenant, name)
	if err != nil {
		return err
	}
	if held {
		return heldRunNameErr(name)
	}
	return nil
}

// runNameHeld reports whether a session that is not cleaned carries name, read
// through the caller's own scope.
func (m *Module) runNameHeld(ctx context.Context, tenant model.TenantID, name string) (held bool, err error) {
	if name == "" {
		return false, nil
	}
	err = m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		held, err = runNameHeldBy(ctx, repo, name, "")
		return err
	})
	return held, err
}

// freeDerivedRunName returns base, or base-2, base-3 … when a session that is not
// cleaned holds it: the rule `session start` keeps for the names it derives, so a
// default the engine makes (a handoff's "Handoff from …") never fails for a second
// use. A name a caller chose is refused, never changed. The write paths still
// check under the run-name lock, so a racing launch is caught there.
func (m *Module) freeDerivedRunName(ctx context.Context, tenant model.TenantID, base string) (string, error) {
	name := base
	for i := 2; ; i++ {
		held, err := m.runNameHeld(ctx, tenant, name)
		if err != nil || !held {
			return name, err
		}
		suffix := fmt.Sprintf("-%d", i)
		stem := []rune(base)
		if room := maxRunNameRunes - len(suffix); len(stem) > room {
			stem = stem[:room]
		}
		name = string(stem) + suffix
	}
}

// refuseHeldRunNameIn refuses name inside a write transaction, under the
// run-name lock: the lock orders the check against the write that follows it
// across every process, so two renames onto one name, or a rename racing a
// launch, cannot both pass. excludeRef drops the session's own row, so keeping
// or re-taking the name it already has is never refused.
func refuseHeldRunNameIn(ctx context.Context, sc store.Scope, tenant model.TenantID, name, excludeRef string) error {
	if name == "" {
		return nil
	}
	locker, ok := sc.(store.TransactionLocker)
	if !ok {
		return &runErr{http.StatusServiceUnavailable, "the engine cannot check session names right now; try again"}
	}
	if err := locker.LockTransaction(ctx, runNameLockKey(tenant, name)); err != nil {
		return err
	}
	repo, err := sc.Ext(runKind)
	if err != nil {
		return err
	}
	held, err := runNameHeldBy(ctx, repo, name, excludeRef)
	if err != nil {
		return err
	}
	if held {
		return heldRunNameErr(name)
	}
	return nil
}

// runNameHeldBy reports whether a session other than excludeRef carries name
// and is not cleaned.
//
// shortcut: the name column has no index, so a free name reads the tenant's runs
// (cleaned rows stay); a launch, rename or handoff runs at human pace, so this
// is fine into the tens of thousands of runs. Index (tenant_id, name) through the
// column descriptor's Indexed flag when a launch's latency shows the scan.
func runNameHeldBy(ctx context.Context, repo store.GenericRepo, name, excludeRef string) (bool, error) {
	filters := []model.Filter{eq(colRunName, name), {Column: colState, Op: model.OpNe, Value: stateCleaned}}
	if excludeRef != "" {
		filters = append(filters, model.Filter{Column: colRunRef, Op: model.OpNe, Value: excludeRef})
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// heldRunNameErr is the refusal for a name a session holds: the sentence the
// CLI's own rename check prints.
func heldRunNameErr(name string) error {
	return &runErr{http.StatusConflict, fmt.Sprintf("Another session is already named %q. Choose another name, or rename that session first.", name)}
}

// runNameLockKey is the transaction lock a name's writes serialize under.
func runNameLockKey(tenant model.TenantID, name string) string {
	return "sessions.run_name:" + tenant.String() + ":" + name
}
