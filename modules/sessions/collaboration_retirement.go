// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sessions

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// retireCollaborationShares invalidates stored authority references while
// preserving the records. The caller has pinned the retiring account first.
func retireCollaborationShares(ctx context.Context, sc store.Scope, user model.ID) error {
	repo, err := sc.Ext("sessions.share")
	if err != nil {
		return err
	}
	for _, column := range []string{"owner_id", "guest_id"} {
		decl, err := declaredColumn(repo, column)
		if err != nil {
			return err
		}
		rows, unknown, err := auth.RowsNaming(ctx, repo, decl, column, user, model.Filter{Column: "revoked", Op: model.OpEq, Value: false})
		if err != nil {
			return err
		}
		if unknown {
			return errors.New("collaboration retirement references are unprovable")
		}
		for _, row := range rows {
			row["revoked"] = true
			if _, err = repo.Update(ctx, row); err != nil {
				return err
			}
			ev, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: "sessions.share.retire", TargetKind: "sessions.share", TargetID: model.ID(row.String("id"))})
			if err != nil {
				return err
			}
			if ev.Seq == 0 {
				return errors.New("collaboration retirement audit unavailable")
			}
		}
	}
	return nil
}
