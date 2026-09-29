// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"path/filepath"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions/accounthome"
	"github.com/olivaresai/olivares/modules/sessions/accountname"
)

// Provider custody is tenant-wide configuration, not authority of the retiring
// human. These reads preserve homes and reservations. Unknown stored metadata
// invalidates the pass; it is never interpreted as ownership or as absence.
func providerAccountRetirementUnknowns(ctx context.Context, sc store.Scope) ([]string, error) {
	var unknown []string
	for _, kind := range []model.Kind{providerProfileKind, accountHomeOperationKind} {
		repo, err := sc.Ext(kind)
		if err != nil {
			return nil, err
		}
		q := model.Query{Limit: accountNamePageSize}
		for {
			rows, page, err := repo.List(ctx, q)
			if err != nil {
				return nil, err
			}
			invalid := false
			for _, rec := range rows {
				if kind == providerProfileKind {
					// No supported writer assigns owner_ref. Even another real
					// person's id is unknown, not an unrelated owner to ignore.
					if !rec.IsNull(colPPOwnerRef) {
						owner, ok := rec[colPPOwnerRef].(string)
						invalid = !ok || owner != ""
					}
				} else {
					invalid = !knownAccountHomeOperation(rec)
				}
				if invalid {
					break
				}
			}
			if invalid {
				unknown = append(unknown, string(kind))
				break
			}
			if !page.HasMore {
				break
			}
			if page.Cursor == "" {
				return nil, errAccountHomeRetry
			}
			q.Cursor = page.Cursor
		}
	}
	return unknown, nil
}

// The journal's nine strings are consumed only as operation identity, home
// custody, labels and lifecycle state. Nothing resolves one to a human or
// credential. Keep this reader aligned with CreateProviderAccount's writer.
func knownAccountHomeOperation(rec model.Record) bool {
	for _, column := range []string{colHOKey, colHOEnv, colHODriver, colHORequested, colHOName, colHORef, colHORoot, colHOToken, colHOState} {
		if _, ok := rec[column].(string); !ok {
			return false
		}
	}
	driver, err := normalizeDriverKey(rec.String(colHODriver))
	if err != nil || driver != rec.String(colHODriver) || !validAccountRetryKey(rec.String(colHOKey)) || !validProfileRef(rec.String(colHORef)) {
		return false
	}
	if err := accountname.Validate(rec.String(colHOName)); err != nil {
		return false
	}
	if requested := rec.String(colHORequested); requested != "" {
		if err := accountname.Validate(requested); err != nil {
			return false
		}
	}
	root := rec.String(colHORoot)
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return false
	}
	if _, err := model.ParseID(rec.String(colHOToken)); err != nil {
		return false
	}
	if _, err := (accounthome.Custody{Tenant: rec.String(model.ColTenantID), Environment: rec.String(colHOEnv), AccountRef: rec.String(colHORef)}).Relative(); err != nil {
		return false
	}
	state := rec.String(colHOState)
	return state == "reserved" || state == "complete"
}
