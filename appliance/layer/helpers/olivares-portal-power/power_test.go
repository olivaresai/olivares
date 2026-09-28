// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

func TestPower_AsksTheServiceManagerWithAFixedArgumentVector(t *testing.T) {
	var ran [][]string
	h := helper(func(_ context.Context, argv []string) error {
		ran = append(ran, argv)
		return nil
	})
	console := helperschema.Peer{UID: 0, Unit: helperschema.RepairConsole.Unit, Attested: true}
	for verb, want := range map[string][]string{
		helperschema.PowerReboot:   {"/usr/bin/systemctl", "reboot"},
		helperschema.PowerShutdown: {"/usr/bin/systemctl", "poweroff"},
	} {
		ran = nil
		response := h.Perform(context.Background(), console, &helperschema.PowerRequest{Verb: verb})
		if response.Result != helperschema.ResultPerformed || len(ran) != 1 || !slices.Equal(ran[0], want) {
			t.Fatalf("%s ran %q and answered %+v, want exactly %q", verb, ran, response, want)
		}
		if strings.Contains(strings.ToLower(response.Detail), "complete") {
			t.Fatalf("an accepted request is reported as completed: %q", response.Detail)
		}
	}
	for _, argv := range powerArgv {
		for _, word := range argv {
			if strings.Contains(word, "sh") && word != "/usr/bin/systemctl" || strings.ContainsAny(word, " ;|&$`") {
				t.Fatalf("the argument vector %q reaches a shell", argv)
			}
		}
	}

	failing := helper(func(context.Context, []string) error { return errors.New("exit status 1") })
	response := failing.Perform(context.Background(), console, &helperschema.PowerRequest{Verb: helperschema.PowerReboot})
	if response.Result != helperschema.ResultFailed || response.Code != helperschema.CodeEffectFailed {
		t.Fatalf("a refused service manager is %+v, want failed", response)
	}
}
