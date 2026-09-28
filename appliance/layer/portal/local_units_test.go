// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestLocalUnits_ProtectSocketLifecycleAndPrivateReceipts(t *testing.T) {
	socket := readUnit(t, "units/olivares-portal-local.socket")
	for key, want := range map[string]string{"ListenStream": "/run/olivares-portal-api/local.sock", "SocketMode": "0666", "SocketUser": "root", "DirectoryMode": "0755", "PassCredentials": "yes", "Accept": "no", "FileDescriptorName": "local", "Service": "olivares-portal.service"} {
		if !slices.Equal(socket.values("Socket", key), []string{want}) {
			t.Errorf("%s=%v", key, socket.values("Socket", key))
		}
	}
	if len(socket.values("Socket", "SocketGroup")) != 0 || len(socket["Install"]) != 0 {
		t.Fatal("local socket adds group admission or install behavior")
	}
	drop := readUnit(t, "units/olivares-portal.service.d/30-host-operations.conf")
	for key, want := range map[string]string{"DynamicUser": "no", "User": "olivares-portal", "RemoveIPC": "yes", "PrivateTmp": "yes", "ProtectSystem": "strict", "ProtectHome": "yes", "RestrictSUIDSGID": "yes", "NoNewPrivileges": "yes", "SupplementaryGroups": "olivares-appliance olivares-lifecycle", "RuntimeDirectory": "olivares-portal-receipts", "RuntimeDirectoryMode": "0700", "RuntimeDirectoryPreserve": "no", "StateDirectory": "olivares-portal/operations", "StateDirectoryMode": "0700"} {
		if !slices.Equal(drop.values("Service", key), []string{want}) {
			t.Errorf("%s=%v", key, drop.values("Service", key))
		}
	}
	for key := range drop["Service"] {
		if !slices.Contains([]string{"DynamicUser", "User", "RemoveIPC", "PrivateTmp", "ProtectSystem", "ProtectHome", "RestrictSUIDSGID", "NoNewPrivileges", "SupplementaryGroups", "RuntimeDirectory", "RuntimeDirectoryMode", "RuntimeDirectoryPreserve", "StateDirectory", "StateDirectoryMode", "Restart", "RestartMode", "RestartSec"}, key) {
			t.Errorf("unexpected service override %s", key)
		}
	}
	data, err := os.ReadFile("units/tmpfiles.d/olivares-hostops.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"d /run/olivares-lifecycle 0750 root olivares-lifecycle - -", "f /run/olivares-lifecycle/lifecycle.lock 0640 root olivares-lifecycle - -", "d /run/olivares-portal-api 0755 root root - -", "d /var/lib/olivares-portal 0755 root root - -"} {
		if !strings.Contains(string(data), line) {
			t.Errorf("missing custody %s", line)
		}
	}
	data, err = os.ReadFile("units/sysusers.d/olivares-hostops.conf")
	if err != nil || !strings.Contains(string(data), "g olivares-lifecycle -") {
		t.Fatal("static lifecycle group missing")
	}
}

// This is a configuration oracle, not a measurement of PID1 or its event loop.
// The guest must exercise a fresh activation with missing credentials and backlog
// on both sockets, then restore the pair without resetting a failed unit.
func TestPortalActivation_TLSLossHasBackoffAndFiniteLimits(t *testing.T) {
	main := readUnit(t, "units/olivares-portal.service")
	service := readUnit(t, "units/olivares-portal.service.d/30-host-operations.conf")
	for key, want := range map[string]string{"Restart": "on-failure", "RestartMode": "direct", "RestartSec": "15s"} {
		if !slices.Equal(service.values("Service", key), []string{want}) {
			t.Errorf("%s=%v", key, service.values("Service", key))
		}
	}
	for key, want := range map[string]string{"StartLimitIntervalSec": "10s", "StartLimitBurst": "5"} {
		if !slices.Equal(service.values("Unit", key), []string{want}) {
			t.Errorf("%s=%v", key, service.values("Unit", key))
		}
	}
	for _, unit := range []unit{main, service} {
		for _, key := range []string{"ExecStartPre", "ExecStartPost", "ExecStop", "ExecStopPost", "RestartForceExitStatus", "RestartPreventExitStatus"} {
			if len(unit.values("Service", key)) != 0 {
				t.Errorf("new lifecycle producer %s requires a revised activation proof", key)
			}
		}
		if len(unit.values("Unit", "OnFailure")) != 0 {
			t.Fatal("failure hook changes activation ownership")
		}
	}
	for _, path := range []string{"units/olivares-portal-local.socket", "units/olivares-portal.socket.d/30-host-operations.conf"} {
		socket := readUnit(t, path)
		for key, want := range map[string]string{"PollLimitIntervalSec": "15s", "PollLimitBurst": "1", "TriggerLimitIntervalSec": "10s", "TriggerLimitBurst": "5"} {
			if !slices.Equal(socket.values("Socket", key), []string{want}) {
				t.Errorf("%s %s=%v", path, key, socket.values("Socket", key))
			}
		}
	}
	for _, path := range []string{"units/olivares-portal.socket", "units/olivares-portal-local.socket"} {
		if len(readUnit(t, path).values("Socket", "ListenStream")) != 1 {
			t.Fatal("the composed activation envelope must be recounted")
		}
	}
	if !slices.Equal(main.values("Service", "LoadCredential"), []string{"tls.crt:/etc/olivares-portal/tls.crt", "tls.key:/etc/olivares-portal/tls.key"}) {
		t.Fatal("TLS delivery became optional")
	}
}
