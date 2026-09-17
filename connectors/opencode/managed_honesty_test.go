// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package opencode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// These tests pin what the connector tells an operator about OpenCode's managed
// configuration layer, and what that layer does not contribute to the reported
// posture. Title assertions are deliberate: sdk/model.FindingReport documents the
// Title as the display-safe summary, while the detail travels only as DetailHash.
// A Title assertion is textual; it does not prove how OpenCode behaves at runtime.
// docs/integrations/opencode-managed-config-parity.md maps each caveat to the
// test that pins it, so change the two together.

// writeManagedConfig writes an opencode.jsonc managed file into a fresh directory
// and points the connector's managed-directory lookup at that directory.
func writeManagedConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.jsonc"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_TEST_MANAGED_CONFIG_DIR", dir)
	return dir
}

// writeProjectConfig writes body as a project opencode.json and returns its path.
func writeProjectConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestTheManagedFindingSaysItIsNotALock pins the two caveats in the
// admin_override.present Title: the managed layer is not immutable, and
// OPENCODE_PERMISSION may override it. Without them, an operator who reads
// "managed admin config present" can take a per-key merge for a lock.
func TestTheManagedFindingSaysItIsNotALock(t *testing.T) {
	managedDir := writeManagedConfig(t, `{"permission":{"edit":"ask","bash":"ask"}}`)
	refs := postureRefs(gatherWith(t, map[string]string{
		"project_config_path": fixturePath("hardened.json"),
		"managed_dir":         managedDir,
	}).findings())

	f, ok := refs["admin_override.present"]
	if !ok {
		t.Fatal("no admin_override.present finding with a managed file in place; the Title checks below would assert nothing")
	}
	for _, caveat := range []string{"not immutable", "OPENCODE_PERMISSION"} {
		if !strings.Contains(f.Title, caveat) {
			t.Errorf("admin_override.present Title does not contain %q: %q", caveat, f.Title)
		}
	}
}

// TestTheAbsentManagedFindingSaysWhatItCannotSee pins the qualifier in the
// admin_override.absent Title. The connector reads local files only and cannot
// see remote organization config, so an unqualified "no managed config" would
// report an absence from a place the connector does not look.
func TestTheAbsentManagedFindingSaysWhatItCannotSee(t *testing.T) {
	refs := postureRefs(gatherWith(t, map[string]string{
		"project_config_path": fixturePath("hardened.json"),
	}).findings())

	f, ok := refs["admin_override.absent"]
	if !ok {
		t.Fatal("no admin_override.absent finding with an empty managed directory; the Title check below would assert nothing")
	}
	if !strings.Contains(f.Title, "not visible locally") {
		t.Errorf("admin_override.absent Title does not qualify what the connector cannot see: %q", f.Title)
	}
}

// TestTheRuntimeOverrideCaveatIsEmittedWithEveryBuiltPosture pins that
// permission.runtime_bypass does not depend on the configuration that was read:
// postureFindings appends it outside every condition. Gather can still return
// before emitting it after a read or sink error; these cases do not cover that.
// Each case varies one condition that a guarded append could depend on, and
// checks from the same collection that the condition held.
func TestTheRuntimeOverrideCaveatIsEmittedWithEveryBuiltPosture(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		projectConfig         string
		managed               bool
		wantPermissiveDefault bool
	}{
		{name: "hardened project config", projectConfig: "hardened.json"},
		{name: "permissive project config", projectConfig: "permissive-default.json", wantPermissiveDefault: true},
		{name: "managed config present", projectConfig: "hardened.json", managed: true},
		{name: "no config at all", wantPermissiveDefault: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]string{}
			if tc.projectConfig != "" {
				cfg["project_config_path"] = fixturePath(tc.projectConfig)
			}
			if tc.managed {
				cfg["managed_dir"] = writeManagedConfig(t, `{"permission":{"edit":"ask","bash":"ask"}}`)
			}
			refs := postureRefs(gatherWith(t, cfg).findings())

			if _, got := refs["admin_override.present"]; got != tc.managed {
				t.Fatalf("admin_override.present emitted = %v, want %v; this case does not exercise the condition it names", got, tc.managed)
			}
			if _, got := refs["permission.default"]; got != tc.wantPermissiveDefault {
				t.Fatalf("permission.default emitted = %v, want %v; this case does not exercise the condition it names", got, tc.wantPermissiveDefault)
			}
			f, ok := refs["permission.runtime_bypass"]
			if !ok {
				t.Fatal("permission.runtime_bypass was not emitted")
			}
			if !strings.Contains(f.Title, "OPENCODE_PERMISSION") {
				t.Errorf("permission.runtime_bypass Title does not name OPENCODE_PERMISSION: %q", f.Title)
			}
		})
	}
}

// TestTheManagedPermissionLeafNeverReachesThePosture pins a documented limit:
// detectManagedConfig only checks that the managed file exists, and readLayers
// computes the posture from the supplied global and project paths. The managed
// body below ungates edit when it is read as config, as the control shows. If the
// connector starts reading the managed layer, this test fails and the parity
// document must change with it.
func TestTheManagedPermissionLeafNeverReachesThePosture(t *testing.T) {
	const managedBody = `{"permission":{"edit":"allow","bash":"allow"}}`

	controlRefs := postureRefs(gatherWith(t, map[string]string{
		"project_config_path": writeProjectConfig(t, managedBody),
	}).findings())
	if _, ok := controlRefs["permission.edit"]; !ok {
		t.Fatal("control: the managed body does not ungate edit even when read as project config, so its absence below would prove nothing")
	}

	managedDir := writeManagedConfig(t, managedBody)
	refs := postureRefs(gatherWith(t, map[string]string{
		"project_config_path": fixturePath("hardened.json"),
		"managed_dir":         managedDir,
	}).findings())
	if _, ok := refs["admin_override.present"]; !ok {
		t.Fatal("control: the managed file was not detected, so the absence below would prove nothing")
	}
	if f, ok := refs["permission.edit"]; ok {
		t.Errorf("permission.edit fired: managed file contents reached the reported posture. Title: %q", f.Title)
	}
}

// TestTheManagedShareMCPCredentialAndAutonomyLeavesNeverReachThePosture extends
// the previous limit to the other posture leaves a managed file can set. A
// connector that merged only some managed keys, such as share or mcp, would keep
// the permission test green; this test fails for it.
func TestTheManagedShareMCPCredentialAndAutonomyLeavesNeverReachThePosture(t *testing.T) {
	const managedBody = `{
		"share": "auto",
		"autoupdate": true,
		"experimental": {"continue_loop_on_deny": true},
		"provider": {"anthropic": {"options": {"apiKey": "managed-literal-value"}}},
		"mcp": {"managed-only": {"type": "remote", "url": "https://managed.example.com/mcp"}}
	}`
	leaves := []string{"share.auto", "autoupdate.true", "experimental.continue_loop_on_deny", "provider.apiKey", "mcp.allowlist"}

	control := gatherWith(t, map[string]string{
		"project_config_path": writeProjectConfig(t, managedBody),
	})
	controlRefs := postureRefs(control.findings())
	for _, leaf := range leaves {
		if _, ok := controlRefs[leaf]; !ok {
			t.Fatalf("control: %s does not fire when the managed body is read as project config, so its absence below would prove nothing", leaf)
		}
	}
	if !hasMCPServerEdge(control) {
		t.Fatal("control: the managed body produces no MCP server edge when read as project config, so the edge check below would prove nothing")
	}

	managedDir := writeManagedConfig(t, managedBody)
	sink := gatherWith(t, map[string]string{
		"project_config_path": fixturePath("hardened.json"),
		"managed_dir":         managedDir,
	})
	refs := postureRefs(sink.findings())
	if _, ok := refs["admin_override.present"]; !ok {
		t.Fatal("control: the managed file was not detected, so the absences below would prove nothing")
	}
	for _, leaf := range leaves {
		if f, ok := refs[leaf]; ok {
			t.Errorf("%s fired: managed file contents reached the reported posture. Title: %q", leaf, f.Title)
		}
	}
	if hasMCPServerEdge(sink) {
		t.Error("a managed-only MCP server produced a permitted edge: managed file contents reached the reported edges")
	}
}

func hasMCPServerEdge(sink *captureSink) bool {
	for _, e := range sink.edges() {
		if e.ResourceKind == resourceMCPServer {
			return true
		}
	}
	return false
}

// TestTheAllowlistFindingDeniesBeingAnAllowlist pins the mcp.allowlist Title.
// The authoring API documents Policy.MCPServers as "the governed MCP server
// allowlist", while this finding's detail states that there is no distinct admin
// allowlist surface. The Title must keep that denial visible. The assertion is
// textual: nothing here runs OpenCode or checks which servers it admits.
func TestTheAllowlistFindingDeniesBeingAnAllowlist(t *testing.T) {
	refs := postureRefs(gatherWith(t, map[string]string{
		"project_config_path": fixturePath("remote-local-mcp.json"),
	}).findings())

	f, ok := refs["mcp.allowlist"]
	if !ok {
		t.Fatal("no mcp.allowlist finding although the fixture enables MCP servers; the Title check below would assert nothing")
	}
	if !strings.Contains(f.Title, "without a separate allowlist mechanism") {
		t.Errorf("mcp.allowlist Title does not deny being an allowlist: %q", f.Title)
	}
}

// TestTheProjectPathAliasDoesLetManagedBytesIn characterizes a limit of the
// existence-only managed check. Open stores global_config_path and
// project_config_path as supplied and does not compare them with the managed
// directory, so a project path that names the managed file makes readLayers parse
// that file as project config. The parity document tells operators to keep the
// supplied paths outside the managed directory. If Open starts rejecting or
// canonicalizing such an alias, this test fails: update it together with that
// caveat instead of weakening the assertion.
func TestTheProjectPathAliasDoesLetManagedBytesIn(t *testing.T) {
	managedDir := t.TempDir()
	managedPath := filepath.Join(managedDir, "opencode.json")
	// deny gates both actions, so the gated result below comes only from these bytes.
	if err := os.WriteFile(managedPath, []byte(`{"permission":{"edit":"deny","bash":"deny"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_TEST_MANAGED_CONFIG_DIR", managedDir)
	if got := detectManagedConfig(); !got.present || got.path != managedPath {
		t.Fatalf("control: detectManagedConfig() = %+v, want present at %q; the project path below would not alias the managed file", got, managedPath)
	}

	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"project_config_path": managedPath,
	}}); err != nil {
		t.Fatal(err)
	}
	_, effective, err := s.readLayers()
	if err != nil {
		t.Fatal(err)
	}
	perm := effective.effectivePrimaryPermission()
	if !permissionActionGated(perm, "edit") || !permissionActionGated(perm, "bash") {
		t.Fatal("the managed file named as project_config_path did not reach the effective config; if Open now rejects the alias, update this test and the parity document caveat together")
	}
}
