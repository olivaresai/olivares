// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

func TestMain_OnlyTheTwoBootModesTakeAnArgument(t *testing.T) {
	for args, want := range map[string]runMode{
		"":                    modeServe,
		"--boot-load":         modeBootLoad,
		"--guard":             modeGuard,
		"--boot-load --guard": modeServe,
		"--guard --guard":     modeServe,
		"--help":              modeServe,
		"boot-load":           modeServe,
		"--boot-load=1":       modeServe,
	} {
		if got := modeOf(strings.Fields(args)); got != want {
			t.Errorf("%q runs mode %d, want %d", args, got, want)
		}
	}
}

func TestHelper_ActsWaitForTheirAdoption(t *testing.T) {
	ctx := context.Background()
	id := strings.Repeat("ab", 16)
	opened := 0
	open := func() (*firewall.Owner, error) { opened++; return nil, errors.New("no host in this test") }
	h := helperWith(false, open)
	if h.Name != firewall.HelperName || h.NewRequest == nil || len(h.Rules) == 0 {
		t.Fatalf("the helper is %+v", h)
	}
	if _, ok := h.NewRequest().(*firewall.Request); !ok {
		t.Fatal("the helper's document is not the firewall request")
	}
	for _, op := range []string{"apply", "confirm", "revert"} {
		resp := h.Perform(ctx, helperschema.Peer{}, &firewall.Request{Op: op, OperationID: id})
		if resp.Result != helperschema.ResultRefused || resp.Code != firewall.CodeActNotAdopted {
			t.Errorf("%s before adoption: %+v", op, resp)
		}
	}
	if opened != 0 {
		t.Fatal("an act reached the owner before its adoption")
	}
	// status is a read: it reaches the owner, and an owner that cannot open is a failure.
	if resp := h.Perform(ctx, helperschema.Peer{}, &firewall.Request{Op: "status"}); resp.Result != helperschema.ResultFailed || opened != 1 {
		t.Errorf("status: %+v, opened %d", resp, opened)
	}
	adopted := helperWith(true, open)
	if resp := adopted.Perform(ctx, helperschema.Peer{}, &firewall.Request{Op: "revert", OperationID: id}); resp.Result != helperschema.ResultFailed || opened != 2 {
		t.Errorf("an adopted act: %+v, opened %d", resp, opened)
	}
}
