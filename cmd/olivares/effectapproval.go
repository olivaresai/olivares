// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/modules/governance/effectgate"
)

type effectOutcome = effectgate.Outcome

const effectRefused = effectgate.Refused
const effectAllowed = effectgate.Allowed
const effectPending = effectgate.Pending
const effectReplay = effectgate.Replay
const effectUnspendable = effectgate.Unspendable
const effectForeign = effectgate.Foreign
const effectUnshown = effectgate.Unshown

type effectStep = effectgate.Step

const stepOpen = effectgate.Open
const stepRegister = effectgate.Register
const stepWait = effectgate.Wait
const stepRecheck = effectgate.Recheck
const stepSpend = effectgate.Spend

type effectAnswer = effectgate.Answer
type approvalSpend = effectgate.ApprovalSpend

const spendOnce = effectgate.SpendOnce
const reuseInWindow = effectgate.ReuseInWindow

type gatedEffect = effectgate.GatedEffect
type effectBridge = effectgate.Bridge

func gateEffect(ctx context.Context, bridge effectBridge, e gatedEffect) (effectAnswer, error) {
	return effectgate.Gate(ctx, bridge, e)
}

type heldEffect = effectgate.HeldEffect
type effectQueue = effectgate.Queue

func holdEffect(ctx context.Context, queue effectQueue, e heldEffect) (effectAnswer, error) {
	return effectgate.Hold(ctx, queue, e)
}
func newSingleUseConsumerID() string { return effectgate.NewSingleUseConsumerID() }
