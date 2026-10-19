// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"time"
)

// What Codex says about a limit (app-server schema, codex-cli 0.162.1): the account's
// windows arrive as account/rateLimits/updated, and a turn the limit refused ends as a
// failed turn/completed whose error is typed usageLimitExceeded or rateLimitExceeded.

const (
	codexErrorUsageLimit = "usageLimitExceeded"
	codexErrorRateLimit  = "rateLimitExceeded"
)

// codexRateLimitsNotification is the sparse update: a window the update leaves out
// keeps its last value.
type codexRateLimitsNotification struct {
	RateLimits struct {
		Primary   *codexRateLimitWindow `json:"primary"`
		Secondary *codexRateLimitWindow `json:"secondary"`
	} `json:"rateLimits"`
}

type codexRateLimitWindow struct {
	UsedPercent int64 `json:"usedPercent"`
	ResetsAt    int64 `json:"resetsAt"`
}

// observeRateLimits keeps the latest value of each window.
func (s *codexSession) observeRateLimits(params json.RawMessage) {
	var n codexRateLimitsNotification
	if json.Unmarshal(params, &n) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.RateLimits.Primary != nil {
		s.limitWindows[0] = *n.RateLimits.Primary
	}
	if n.RateLimits.Secondary != nil {
		s.limitWindows[1] = *n.RateLimits.Secondary
	}
}

// exhaustedResetsAt is when the windows that are used up all lift: the latest reset
// among them. A window that ended before the turn did is stale. Zero when none is
// used up, or Codex named no time.
func (s *codexSession) exhaustedResetsAt(turnEnded time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest int64
	for _, w := range s.limitWindows {
		if w.UsedPercent >= 100 && w.ResetsAt > latest && w.ResetsAt < limitEpochMax && time.Unix(w.ResetsAt, 0).After(turnEnded) {
			latest = w.ResetsAt
		}
	}
	if latest == 0 {
		return time.Time{}
	}
	return time.Unix(latest, 0).UTC()
}

// reportTurnLimit tells the runtime how a completed turn ended: on a limit, or fine.
// A failed turn of another cause and an interrupted turn say nothing. It reads the
// notification on its own, so a field of the turn that Codex shapes differently one
// day costs this fact and nothing else.
func (s *codexSession) reportTurnLimit(params json.RawMessage) {
	var n struct {
		Turn struct {
			Status string `json:"status"`
			Error  *struct {
				CodexErrorInfo json.RawMessage `json:"codexErrorInfo"`
			} `json:"error"`
			CompletedAt int64 `json:"completedAt"`
		} `json:"turn"`
	}
	if json.Unmarshal(params, &n) != nil {
		return
	}
	switch n.Turn.Status {
	case "completed":
		if s.cfg.OnTurnOK != nil {
			s.cfg.OnTurnOK()
		}
	case "failed":
		if s.cfg.OnLimit == nil || n.Turn.Error == nil {
			return
		}
		var info string
		if json.Unmarshal(n.Turn.Error.CodexErrorInfo, &info) != nil {
			return // an object-shaped cause is not a limit
		}
		switch info {
		case codexErrorUsageLimit:
			ended := time.Now()
			if n.Turn.CompletedAt > 0 {
				ended = time.Unix(n.Turn.CompletedAt, 0)
			}
			s.cfg.OnLimit(limitKindUsage, s.exhaustedResetsAt(ended))
		case codexErrorRateLimit:
			s.cfg.OnLimit(limitKindRate, time.Time{})
		}
	}
}
