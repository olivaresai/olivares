// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testBootID stands for this boot's identity as the kernel reports it.
const testBootID = "8d8a1f0c-54c0-4b3e-9d6a-2a1f3b4c5d6e"

// writeBootID writes this boot's identity where the probe reads it, with the kernel's
// trailing newline, and returns its path.
func writeBootID(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "boot_id")
	writeFile(t, path, []byte(testBootID+"\n"), 0o444)
	return path
}

type measuredRow struct {
	Port       string   `json:"port"`
	Interfaces []string `json:"interfaces"`
}

func row(port string, interfaces ...string) measuredRow {
	return measuredRow{Port: port, Interfaces: interfaces}
}

// measurement is the document the firewall owner publishes after it loads a policy.
func measurement(bootID, inputPolicy string, rows ...measuredRow) map[string]any {
	if rows == nil {
		rows = []measuredRow{}
	}
	return map[string]any{
		"schema_version": "olivares-firewall-measurement/v1",
		"boot_id":        bootID,
		"measured_at":    "2026-09-26T12:00:00Z",
		"policy_digest":  "sha256:" + strings.Repeat("ab", 32),
		"input_policy":   inputPolicy,
		"rows":           rows,
	}
}

// writeMeasurement writes document at path, mode 0644, as the firewall owner does.
func writeMeasurement(t *testing.T, path string, document any) {
	t.Helper()
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, append(data, '\n'), 0o644)
}

// probeOn returns a probe of the measurement at dir/measured.json for selection, with this
// boot's identity published in dir.
func probeOn(t *testing.T, dir string, selection Selection) MeasuredFirewall {
	t.Helper()
	bootID := writeBootID(t, dir)
	return MeasuredFirewall{Path: filepath.Join(dir, "measured.json"), BootID: bootID, Selection: selection}
}

func TestFirewallProbe_ReadsThePublishedMeasurementAndRefusesAForeignOwner(t *testing.T) {
	selection := remoteSelection() // eth0

	t.Run("the owner's measurement of this boot is read", func(t *testing.T) {
		dir := t.TempDir()
		probe := probeOn(t, dir, selection)
		writeMeasurement(t, probe.Path, measurement(testBootID, "drop", row("22/tcp", "*"), row("9443/tcp", "eth0")))
		ownedByRoot(t, probe.Path)
		got := probe.MeasureFirewall()
		if !got.Holds || got.condition() != "held" {
			t.Fatalf("a root-owned measurement of this boot with the management row read as %+v", got)
		}
		for _, want := range []string{"input drop", "9443/tcp on eth0", "abababab"} {
			if !strings.Contains(got.Policy.String(), want) {
				t.Errorf("the policy fact %q does not state %q", got.Policy, want)
			}
		}
	})

	unmeasured := func(t *testing.T, got FirewallMeasurement) {
		t.Helper()
		if got.Holds || got.condition() != "unmeasured" || got.Policy.String() != "unmeasured" {
			t.Fatalf("read as %+v (%s), want unmeasured", got, got.condition())
		}
	}

	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"absent", func(*testing.T, string) {}},
		{"owned by another user", func(t *testing.T, path string) {
			writeMeasurement(t, path, measurement(testBootID, "drop", row("9443/tcp", "eth0")))
			ownedByAnotherUser(t, path)
		}},
		{"owned by this service's own user, not root", func(t *testing.T, path string) {
			writeMeasurement(t, path, measurement(testBootID, "drop", row("9443/tcp", "eth0")))
		}},
		{"writable by its group", func(t *testing.T, path string) {
			writeMeasurement(t, path, measurement(testBootID, "drop", row("9443/tcp", "eth0")))
			ownedByRoot(t, path)
			chmod(t, path, 0o664)
		}},
		{"a symbolic link to a root-owned measurement", func(t *testing.T, path string) {
			elsewhere := filepath.Join(t.TempDir(), "measured.json")
			writeMeasurement(t, elsewhere, measurement(testBootID, "drop", row("9443/tcp", "eth0")))
			ownedByRoot(t, elsewhere)
			symlink(t, elsewhere, path)
		}},
		{"not JSON", func(t *testing.T, path string) {
			writeFile(t, path, []byte("tcp dport 9443 accept\n"), 0o644)
			ownedByRoot(t, path)
		}},
		{"with an unknown field", func(t *testing.T, path string) {
			doc := measurement(testBootID, "drop", row("9443/tcp", "eth0"))
			doc["exposure"] = "remote"
			writeMeasurement(t, path, doc)
			ownedByRoot(t, path)
		}},
		{"of another schema", func(t *testing.T, path string) {
			doc := measurement(testBootID, "drop", row("9443/tcp", "eth0"))
			doc["schema_version"] = "olivares-firewall-measurement/v0"
			writeMeasurement(t, path, doc)
			ownedByRoot(t, path)
		}},
		{"of another boot", func(t *testing.T, path string) {
			writeMeasurement(t, path, measurement("00000000-0000-4000-8000-000000000000", "drop", row("9443/tcp", "eth0")))
			ownedByRoot(t, path)
		}},
		{"with a port that is not a port", func(t *testing.T, path string) {
			writeMeasurement(t, path, measurement(testBootID, "drop", row("94430/tcp", "eth0")))
			ownedByRoot(t, path)
		}},
	} {
		t.Run("unmeasured when the measurement is "+tc.name, func(t *testing.T) {
			dir := t.TempDir()
			probe := probeOn(t, dir, selection)
			tc.setup(t, probe.Path)
			unmeasured(t, probe.MeasureFirewall())
			// The public state word and the listen decision both say so.
			status := Snapshot(selection, verifiedCustody(t), probe)
			if status.view(false).Firewall != "unmeasured" || status.Listen.Mode != LocalOnly {
				t.Fatalf("status firewall %q, listen %s", status.view(false).Firewall, status.Listen.Mode)
			}
		})
	}

	t.Run("unmeasured when this boot's identity cannot be read", func(t *testing.T) {
		dir := t.TempDir()
		probe := MeasuredFirewall{Path: filepath.Join(dir, "measured.json"), BootID: filepath.Join(dir, "no-boot-id"), Selection: selection}
		writeMeasurement(t, probe.Path, measurement(testBootID, "drop", row("9443/tcp", "eth0")))
		ownedByRoot(t, probe.Path)
		unmeasured(t, probe.MeasureFirewall())
	})

	t.Run("the probe only reads", func(t *testing.T) {
		dir := t.TempDir()
		probe := probeOn(t, dir, selection)
		probe.MeasureFirewall()
		if _, err := os.Lstat(probe.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the probe created the measurement it reads: %v", err)
		}
	})
}

func TestPortalListen_RemoteOnlyWithAMeasuredManagementRow(t *testing.T) {
	custody := verifiedCustody(t)
	for _, tc := range []struct {
		name       string
		interfaces []string
		policy     string
		rows       []measuredRow
		remote     bool
		reason     string
	}{
		{"the 9443 row on the selected interface", []string{"eth0"}, "drop", []measuredRow{row("22/tcp", "*"), row("9443/tcp", "eth0")}, true, ""},
		{"the 9443 row on both selected interfaces", []string{"eth1", "eth0"}, "drop", []measuredRow{row("9443/tcp", "eth1", "eth0")}, true, ""},
		{"the 9443 row on another interface", []string{"eth0"}, "drop", []measuredRow{row("9443/tcp", "eth1")}, false, "firewall prerequisite is not held"},
		{"the 9443 row on one of two selected interfaces", []string{"eth0", "eth1"}, "drop", []measuredRow{row("9443/tcp", "eth0")}, false, "firewall prerequisite is not held"},
		{"the 9443 row on every interface", []string{"eth0"}, "drop", []measuredRow{row("9443/tcp", "*")}, false, "firewall prerequisite is not held"},
		{"a second 9443 row beside the management one", []string{"eth0"}, "drop", []measuredRow{row("9443/tcp", "eth0"), row("9443/tcp", "eth1")}, false, "firewall prerequisite is not held"},
		{"no 9443 row", []string{"eth0"}, "drop", []measuredRow{row("22/tcp", "*")}, false, "firewall prerequisite is not held"},
		{"an input policy that accepts", []string{"eth0"}, "accept", []measuredRow{row("9443/tcp", "eth0")}, false, "firewall prerequisite is not held"},
		{"no measurement", []string{"eth0"}, "", nil, false, "firewall prerequisite is unmeasured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := remoteSelection()
			selection.ManagementInterfaces = tc.interfaces
			dir := t.TempDir()
			probe := probeOn(t, dir, selection)
			if tc.policy != "" {
				writeMeasurement(t, probe.Path, measurement(testBootID, tc.policy, tc.rows...))
				ownedByRoot(t, probe.Path)
			}
			d := Snapshot(selection, custody, probe).Listen
			if tc.remote {
				if d.Mode != Remote || len(d.Reasons) != 0 {
					t.Fatalf("every prerequisite held and the row is measured: %+v", d)
				}
				for _, name := range tc.interfaces {
					if !d.Serves(bound("192.0.2.10", name)) {
						t.Fatalf("a socket bound to the selected %s is not served: %+v", name, d)
					}
				}
				return
			}
			if d.Mode != LocalOnly || !slices.Equal(d.Reasons, []string{tc.reason}) {
				t.Fatalf("want local only because %q: %+v", tc.reason, d)
			}
			assertNotRemote(t, d)
		})
	}
}
