// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestWorkCommandResultCarriesExactK4AggregateCursor(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()

	create := baseCreateCommand(f, "K4 exact result")
	created, err := f.m.Apply(context.Background(), f.tenant, f.principal, create)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Version != 1 || created.EventSeq != 1 || created.OwnerEpoch != 1 ||
		created.LeaseFence != 0 || created.Status != "draft" {
		t.Fatalf("create result = %+v", created)
	}
	replayedCreate, err := f.m.Apply(context.Background(), f.tenant, f.principal, create)
	if err != nil || !replayedCreate.Replayed {
		t.Fatalf("create replay = %+v, %v", replayedCreate, err)
	}
	replayedCreate.Replayed, created.Replayed = false, false
	if !reflect.DeepEqual(replayedCreate, created) {
		t.Fatalf("create replay changed exact result: first=%+v replay=%+v", created, replayedCreate)
	}

	ready := WorkCommand{
		Command: "item.ready", WorkItemID: created.ResultID,
		ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
		HTTPMethod: http.MethodPost, CommandScope: "workflow:k4:ready",
	}
	transitioned, err := f.m.Apply(context.Background(), f.tenant, f.principal, ready)
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	if transitioned.Version != 2 || transitioned.EventSeq != 2 || transitioned.OwnerEpoch != 1 ||
		transitioned.LeaseFence != 0 || transitioned.Status != "ready" {
		t.Fatalf("ready result = %+v", transitioned)
	}
	replayedReady, err := f.m.Apply(context.Background(), f.tenant, f.principal, ready)
	if err != nil || !replayedReady.Replayed {
		t.Fatalf("ready replay = %+v, %v", replayedReady, err)
	}
	replayedReady.Replayed, transitioned.Replayed = false, false
	if !reflect.DeepEqual(replayedReady, transitioned) {
		t.Fatalf("ready replay changed exact result: first=%+v replay=%+v", transitioned, replayedReady)
	}
}
