// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
	"github.com/olivaresai/olivares/appliance/layer/portal/helperclient"
)

func TestConsole_PowerMintsAnOperationIDPerRequest(t *testing.T) {
	dir := socketDir(t)
	helper := startPowerHelper(t, dir, `{"result": "refused", "code": "pidfd_unproven"}`+"\n")
	console := signedIn(NewConsole(auth.State{Mode: auth.Unavailable}).WithHelper(helperclient.Client{Dir: dir, Owner: uint32(os.Getuid())}))
	power := powerPosition(t)
	code, out := session(t, console, power, "reboot", power, "reboot", power, "shutdown", "q")
	if code != 0 {
		t.Fatalf("the console exited %d:\n%s", code, out)
	}
	ids := helper.operations()
	if len(ids) != 3 {
		t.Fatalf("the helper was asked %d times, want 3", len(ids))
	}
	for _, id := range ids {
		if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
			t.Fatalf("an operation id %q is not 128 bits in lowercase hexadecimal", id)
		}
	}
	if sorted := slices.Compact(slices.Sorted(slices.Values(ids))); len(sorted) != 3 {
		t.Fatalf("the console reused an operation id across confirmations: %q", ids)
	}
	if strings.Count(out, "refused reboot (pidfd_unproven)") != 2 || strings.Contains(out, "power helper performed") {
		t.Fatalf("the refusal before the guest proof is not stated as the helper answered it:\n%s", out)
	}
}

// verbPosition is the menu number of v.
func verbPosition(t *testing.T, v auth.Verb) string {
	t.Helper()
	i := slices.Index(Verbs(), v)
	if i < 0 {
		t.Fatalf("the console does not offer %s", v)
	}
	return strconv.Itoa(i + 1)
}
