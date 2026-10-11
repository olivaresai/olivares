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

// ErrSessionStopActive is what SessionStopEpoch returns while a stop that covers the
// agent is active. It is a decision, not an outage: callers refuse and do not retry.
var ErrSessionStopActive = errors.New("session authority revoked by an active kill switch")

// SessionStopActiveError preserves the stop observed by the epoch read, so callers
// can attribute the refusal without consulting mutable stop state again.
type SessionStopActiveError struct {
	StopID model.ID
}

func (*SessionStopActiveError) Error() string { return ErrSessionStopActive.Error() }
func (*SessionStopActiveError) Unwrap() error { return ErrSessionStopActive }

// SessionStopEpoch reads the existing stop history for one session's agent.
// Every matching engagement changes the epoch permanently, including after
// re-enable. An active stop refuses mint/use. No separate state is persisted.
func (m *Module) SessionStopEpoch(ctx context.Context, tenant model.TenantID, agentRef string) (string, error) {
	if m.data == nil {
		return "", errNoData
	}
	agentRef = strings.TrimSpace(agentRef)
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
		if id, stopped := stopStateFromRows(rows).Stopped(agentRef); stopped {
			return &SessionStopActiveError{StopID: id}
		}
		for _, row := range rows {
			matches := row.String(colKSScopeKind) == ksScopeEstate
			if row.String(colKSScopeKind) == ksScopeAgent && agentRef != "" {
				matches = row.String(colKSScopeRef) == agentRef || row.String(colKSAgentID) == agentRef || row.String(colKSAgentExternal) == agentRef
			}
			if !matches {
				continue
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
