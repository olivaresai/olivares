// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package testsupport loads pre-edition stored policies for downstream enforcement
// tests. Product packages do not import it; authoring journeys use the Business API.
package testsupport

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type reloader interface {
	ReloadActivePDP(context.Context, model.TenantID) error
}

// SeedCedar uses the real store and reload path, never a successful authoring HTTP
// response. Every write advances the real authorization generation.
func SeedCedar(t testing.TB, st store.Store, tenant model.TenantID, source string, module any) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		epochs := sc.(store.AuthorizationEpochStore)
		current, err := epochs.ReadAuthorizationEpoch(ctx)
		if err != nil {
			return err
		}
		if _, err := epochs.BumpAuthorizationEpoch(ctx, current); err != nil {
			return err
		}
		repo, err := sc.Ext(model.Kind("governance.policy_revision"))
		if err != nil {
			return err
		}
		next := map[string]int64{"cedar": 1, "cedar-activation": 1}
		q := model.Query{Limit: 1000}
		for {
			rows, page, err := repo.List(ctx, q)
			if err != nil {
				return err
			}
			for _, row := range rows {
				surface := row.String("surface")
				if row.Int("revision") >= next[surface] {
					next[surface] = row.Int("revision") + 1
				}
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			q.Cursor = page.Cursor
		}
		for _, surface := range []string{"cedar", "cedar-activation"} {
			content := source
			if surface == "cedar-activation" {
				content = strconv.FormatInt(next["cedar"], 10)
			}
			if _, err := repo.Create(ctx, model.Record{"surface": surface, "revision": next[surface], "content": content, "author": "upgrade-fixture", "validated": true, "active": true, "note": "stored before upgrade"}); err != nil {
				return err
			}
		}
		freshness, err := sc.Ext(model.Kind("governance.policy_freshness"))
		if err != nil {
			return err
		}
		rows, _, err := freshness.List(ctx, model.Query{Limit: 2})
		if err != nil {
			return err
		}
		stamp := model.NewTimestamp(time.Now()).String()
		if len(rows) == 0 {
			_, err = freshness.Create(ctx, model.Record{"refreshed_at": stamp, "max_staleness": "", "adopted_revision": "", "adopted_created_at": nil})
		} else if rows[0].String("adopted_revision") == "" {
			rows[0]["refreshed_at"] = stamp
			_, err = freshness.Update(ctx, rows[0])
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	gov, ok := module.(reloader)
	if !ok {
		t.Fatal("stored-policy fixture requires the real PDP reloader")
	}
	if err := gov.ReloadActivePDP(ctx, tenant); err != nil {
		t.Fatal(err)
	}
}
