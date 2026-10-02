// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SessionStopEpoch reads the existing stop history for one session's agent.
// Every matching engagement changes the epoch permanently, including after
// re-enable. An active stop refuses mint/use. No separate state is persisted.
func (m *Module) SessionStopEpoch(ctx context.Context, tenant model.TenantID, agentRef string) (string, error) {
	if m.data == nil {
		return "", errNoData
	}
	var ids []string
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		rows, err := listAll(ctx, repo)
		if err != nil {
			return err
		}
		for _, row := range rows {
			matches := row.String(colKSScopeKind) == ksScopeEstate
			if row.String(colKSScopeKind) == ksScopeAgent && agentRef != "" {
				matches = row.String(colKSScopeRef) == agentRef || row.String(colKSAgentID) == agentRef || row.String(colKSAgentExternal) == agentRef
			}
			if !matches {
				continue
			}
			if row.String(colKSStatus) == ksStatusActive {
				return errors.New("session authority revoked by an active kill switch")
			}
			ids = append(ids, row.String(model.ColID))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte("session-stop-history-v1\x00" + strings.Join(ids, "\x00")))
	return hex.EncodeToString(sum[:]), nil
}
