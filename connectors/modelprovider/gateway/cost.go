// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import "context"

// Estimate is the pre-flight reservation input. This package does not convert
// tokens to money; the FinOps adapter does that when a CostHook is wired.
type Estimate struct {
	Model     string
	MaxTokens int64
}

// Reservation is the handle returned by CostHook.Reserve. Allowed=false means
// the driver must not call upstream (Denial-of-Wallet).
type Reservation struct {
	Handle  string
	Allowed bool
	Reason  string
}

// CostHook is the FinOps seam: Reserve before the upstream call, Commit with
// observed usage on success, Release on failure or cancel.
//
// When the FinOps admission calls ReserveBudget / CommitReservation /
// ReleaseReservation are present, the composition root adapts them here
// (tenant, spend dimensions and micro-USD stay in that adapter). When they are
// not, use NopCostHook.
type CostHook interface {
	Reserve(ctx context.Context, est Estimate) (Reservation, error)
	Commit(ctx context.Context, res Reservation, actual Usage) error
	Release(ctx context.Context, res Reservation) error
}

// NopCostHook admits every call and records nothing. It is the stub used when
// no FinOps reservation backend is wired.
type NopCostHook struct{}

func (NopCostHook) Reserve(context.Context, Estimate) (Reservation, error) {
	return Reservation{Allowed: true, Handle: "nop"}, nil
}

func (NopCostHook) Commit(context.Context, Reservation, Usage) error { return nil }

func (NopCostHook) Release(context.Context, Reservation) error { return nil }

func hookOrNop(h CostHook) CostHook {
	if h == nil {
		return NopCostHook{}
	}
	return h
}

func estimateOf(req MessageRequest) Estimate {
	return Estimate{Model: req.Model, MaxTokens: int64(req.MaxTokens)}
}

func reserveOrDeny(ctx context.Context, hook CostHook, req MessageRequest) (Reservation, error) {
	res, err := hook.Reserve(ctx, estimateOf(req))
	if err != nil {
		return Reservation{}, mapTransportError(err)
	}
	if !res.Allowed {
		msg := res.Reason
		if msg == "" {
			msg = "budget denied"
		}
		return Reservation{}, &Error{Code: CodeBudgetDenied, HTTPStatus: 402, Message: msg}
	}
	return res, nil
}
