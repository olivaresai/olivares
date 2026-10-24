// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package egress

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// The egress proxy links github.com/rs/xid (through smokescreen), whose init reads a machine id. Up to v1.2.1 it reads
// /sys/class/dmi/id/product_uuid, which the appliance's policy denies to the product and to first boot (lab, the 0.5
// candidate: avc denied { read } name="id" for olivares_t and olivares_firstboot_t; dev#2992). v1.6.0 reads
// /etc/machine-id first and the DMI file only when that is empty (measured in its hostid_linux.go).
func TestTheLinkedXIDReadsTheMachineIDBeforeDMI(t *testing.T) {
	// The workspace builds with the highest requirement among its modules, and a module built alone with its own; the
	// two modules that link the egress proxy each require v1.6.0 or later, or not at all.
	for _, gomod := range []string{"../../go.mod", "../../../cmd/olivares/go.mod"} {
		data, err := os.ReadFile(gomod)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "github.com/rs/xid" && olderThan(fields[1], 1, 6, 0) {
				t.Errorf("%s requires github.com/rs/xid %s, which reads /sys/class/dmi/id/product_uuid at every start; require v1.6.0 or later", gomod, fields[1])
			}
		}
	}
}

// olderThan reports whether version vX.Y.Z is older than major.minor.patch; an unreadable version counts as older.
func olderThan(version string, major, minor, patch int) bool {
	parts := strings.SplitN(strings.TrimPrefix(strings.SplitN(version, "-", 2)[0], "v"), ".", 3)
	if len(parts) != 3 {
		return true
	}
	got := make([]int, 3)
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return true
		}
		got[i] = n
	}
	for i, want := range []int{major, minor, patch} {
		if got[i] != want {
			return got[i] < want
		}
	}
	return false
}
