// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && !cgo

package confine

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const sysAdminBit = 1 << 21

// capEff is the effective capability set of the process leader.
func capEff() string {
	status, _ := os.ReadFile("/proc/self/status")
	for _, line := range strings.Split(string(status), "\n") {
		if value, ok := strings.CutPrefix(line, "CapEff:"); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// A process that mounted must not keep CAP_SYS_ADMIN: the network supervisor
// serves requests the tool feeds it. Without cgo the runtime changes every thread,
// so the leader (which /proc reports) shows it.
func TestSysAdminIsGoneOnceTheMountsAreMade(t *testing.T) {
	if os.Getenv("OLV_TEST_DROP_SYSADMIN") == "1" {
		before := capEff()
		if err := dropSysAdmin(); err != nil {
			os.Stderr.WriteString(err.Error())
			os.Exit(2)
		}
		os.Stdout.WriteString(before + " " + capEff())
		os.Exit(0)
	}
	if err := probeMounts(); err != nil {
		t.Skip("this host gives no user and mount namespace: " + err.Error())
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestSysAdminIsGoneOnceTheMountsAreMade$")
	cmd.Env = append(os.Environ(), "OLV_TEST_DROP_SYSADMIN=1")
	enterMountNamespace(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v: %s", err, out)
	}
	var before, after uint64
	fields := strings.Fields(string(out))
	if len(fields) != 2 {
		t.Fatalf("helper output %q", out)
	}
	for i, dst := range []*uint64{&before, &after} {
		for _, c := range fields[i] {
			*dst = *dst<<4 + uint64(strings.IndexRune("0123456789abcdef", c))
		}
	}
	if before&sysAdminBit == 0 || after&sysAdminBit != 0 {
		t.Fatalf("CapEff before=%x after=%x: want CAP_SYS_ADMIN held, then gone", before, after)
	}
}
