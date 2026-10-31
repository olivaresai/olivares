// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// AccountUsage is one usage window the tool itself reported for an account, from its
// own structured answer and never from text: Utilization is the percent of the window
// used (Claude Code `get_usage`, Codex `account/rateLimits/read`). A window the tool
// gave no percent for is not listed, so an absent value means unknown, never 0.
// Credit reports instead carry Credit and omit utilization in the API response.
type AccountUsage struct {
	// Window is the stable name of the window: five_hour, seven_day or seven_day:<model>;
	// any other window the tool reports keeps the tool's own name for it (a Claude Code
	// kind, or <n>m for a Codex window of n minutes), so a reader must accept other values.
	Window      string
	Label       string
	Utilization float64
	ResetsAt    *time.Time
	// Credit is independent of utilization. A credit row has no percentage.
	Credit *AccountCredit
	// ObservedAt is when the tool was asked.
	ObservedAt time.Time
}

// AccountCredit is the tool's credit report. Balance is a decimal string, as
// reported by Codex; nil means unknown. It is never inferred from has_credits.
type AccountCredit struct {
	Balance    *string `json:"balance,omitempty"`
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited"`
}

// AccountUsageSource reads the usage windows of one account's own home. wait asks for
// a fresh answer when none is cached (one account's read); without it a cold account
// answers nothing now and the next read has it (the list). Nil means unknown.
type AccountUsageSource func(ctx context.Context, tenant model.TenantID, driver, accountRef string, wait bool) []AccountUsage

// accountUsageWait bounds how long one account's read waits for the tool.
const accountUsageWait = 20 * time.Second

// accountUsageDTO is one window as a reader sees it.
type accountUsageDTO struct {
	Window      string         `json:"window"`
	Label       string         `json:"label"`
	Utilization *float64       `json:"utilization,omitempty"`
	Credit      *AccountCredit `json:"credit,omitempty"`
	ResetsAt    string         `json:"resets_at,omitempty"`
	ObservedAt  string         `json:"observed_at"`
}

// withUsage adds the windows the tool reported for the account. An account whose tool
// reports none (not signed in, an API key, a tool without plan limits, not asked yet)
// carries no usage field at all.
func (m *Module) withUsage(ctx context.Context, a ProviderAccount, dto providerAccountDTO, wait bool) providerAccountDTO {
	if m.rt.AccountUsage == nil || a.State != ProfileActive {
		return dto
	}
	if wait {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, accountUsageWait)
		defer cancel()
	}
	for _, u := range m.rt.AccountUsage(ctx, model.TenantID(a.Tenant), a.Driver, a.Ref, wait) {
		w := accountUsageDTO{Window: u.Window, Label: u.Label, Credit: u.Credit, ObservedAt: u.ObservedAt.UTC().Format(time.RFC3339)}
		if u.Credit == nil {
			w.Utilization = &u.Utilization
		}
		if u.ResetsAt != nil {
			w.ResetsAt = u.ResetsAt.UTC().Format(time.RFC3339)
		}
		dto.Usage = append(dto.Usage, w)
	}
	return dto
}
