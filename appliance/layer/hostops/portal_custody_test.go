// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/portal/localsession"
)

var portalSandboxLines = []string{
	"DynamicUser=no", "User=olivares-portal", "SupplementaryGroups=olivares-appliance olivares-lifecycle",
	"RemoveIPC=yes", "PrivateTmp=yes", "ProtectSystem=strict", "ProtectHome=yes", "RestrictSUIDSGID=yes", "NoNewPrivileges=yes",
	"RuntimeDirectory=olivares-portal-receipts", "RuntimeDirectoryMode=0700", "RuntimeDirectoryPreserve=no",
	"StateDirectory=olivares-portal/operations", "StateDirectoryMode=0700",
}

const portalParentDeclaration = "d /var/lib/olivares-portal 0755 root root - -"

func portalCustodyContract(unit, tmpfiles string) error {
	lines := strings.Split(unit, "\n")
	for _, want := range portalSandboxLines {
		found := false
		for _, line := range lines {
			if line == want {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("missing %s", want)
		}
	}
	if !strings.Contains("\n"+tmpfiles, "\n"+portalParentDeclaration+"\n") {
		return fmt.Errorf("missing root-owned parent")
	}
	if strings.Contains(unit, "/var/lib/private") || strings.Contains(tmpfiles, "/var/lib/private") {
		return fmt.Errorf("private backing is forbidden")
	}
	return nil
}

func TestPortal_StaticAccountAndProtectedOperationLeaf(t *testing.T) {
	unit, err := os.ReadFile("../portal/units/olivares-portal.service.d/30-host-operations.conf")
	if err != nil {
		t.Fatal(err)
	}
	tmpfiles, err := os.ReadFile("../portal/units/tmpfiles.d/olivares-hostops.conf")
	if err != nil {
		t.Fatal(err)
	}
	if err := portalCustodyContract(string(unit), string(tmpfiles)); err != nil {
		t.Fatal(err)
	}
	if localsession.OperationDirectory != "/var/lib/olivares-portal/operations" {
		t.Fatal("operation path changed")
	}
}

func TestPortal_StaticCustodyControlsRejectEachMissingSandboxSetting(t *testing.T) {
	unit := "[Service]\n" + strings.Join(portalSandboxLines, "\n") + "\n"
	tmpfiles := portalParentDeclaration + "\n"
	if err := portalCustodyContract(unit, tmpfiles); err != nil {
		t.Fatal(err)
	}
	for _, line := range portalSandboxLines {
		mutant := strings.Replace(unit, line+"\n", "", 1)
		if err := portalCustodyContract(mutant, tmpfiles); err == nil {
			t.Fatalf("missing setting accepted: %s", line)
		}
	}
	if err := portalCustodyContract(strings.Replace(unit, "DynamicUser=no", "DynamicUser=yes", 1), tmpfiles); err == nil {
		t.Fatal("dynamic identity accepted")
	}
	if err := portalCustodyContract(unit, strings.Replace(tmpfiles, "root root", "olivares-portal olivares-portal", 1)); err == nil {
		t.Fatal("portal-owned parent accepted")
	}
}

func TestPortal_StaticAccountAndSupplementaryGroupsAreDeclared(t *testing.T) {
	data, err := os.ReadFile("../portal/units/sysusers.d/olivares-hostops.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`u olivares-portal - "Olivares portal" / /usr/sbin/nologin`,
		"g olivares-appliance -", "g olivares-lifecycle -",
	} {
		if !strings.Contains("\n"+string(data), "\n"+want+"\n") {
			t.Fatalf("missing identity declaration: %s", want)
		}
	}
}
