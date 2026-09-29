// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package answers_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/answers"
)

const (
	rootAnchor = `"source": "file",`
	hostAnchor = `"network": { "mode": "dhcp" },`
)

// withFields adds root and host fields to the fixture document.
func withFields(valid, root, host string) string {
	if root != "" {
		valid = strings.Replace(valid, rootAnchor, rootAnchor+" "+root+",", 1)
	}
	if host != "" {
		valid = strings.Replace(valid, hostAnchor, hostAnchor+" "+host+",", 1)
	}
	return valid
}

func planOf(t *testing.T, input string) map[string]any {
	t.Helper()
	p, err := answers.Build(strings.NewReader(input))
	if err != nil {
		t.Fatalf("valid answers refused: %v", err)
	}
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var plan map[string]any
	if err := json.Unmarshal(b, &plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestAnswersV1_PortalFieldsAreOptionalAndUnknownFieldsRefused(t *testing.T) {
	valid := fixture(t)
	for _, anchor := range []string{rootAnchor, hostAnchor} {
		if strings.Count(valid, anchor) != 1 {
			t.Fatalf("fixture anchor %q changed", anchor)
		}
	}
	base := planOf(t, valid)

	t.Run("a document without the fields plans as before", func(t *testing.T) {
		var input map[string]any
		if err := json.Unmarshal([]byte(valid), &input); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(base["answers"], input) {
			t.Fatalf("the plan no longer echoes the document unchanged: %v", base["answers"])
		}
		operations := []any{
			map[string]any{"owner": "cloud-init", "action": "wait-for-completion", "state": "pending"},
			map[string]any{"owner": "cloud-init", "action": "verify-host-settings", "state": "pending"},
			map[string]any{"owner": "olivares", "action": "generate-product-config", "state": "pending"},
			map[string]any{"owner": "olivares", "action": "initialize-storage", "state": "pending"},
		}
		prerequisites := []any{
			"host-and-product-adapters-unimplemented", "instance-identity-unverified",
			"firewall-policy-unverified", "protected-setup-delivery-and-expiry-unimplemented",
			"service-start-and-health-unverified",
		}
		if !reflect.DeepEqual(base["operations"], operations) || !reflect.DeepEqual(base["pending_prerequisites"], prerequisites) {
			t.Fatalf("the plan's operations or prerequisites changed: %v", base)
		}
		if base["schema_version"] != "appliance-plan/v1" || base["state"] != "planned" || base["executable"] != false {
			t.Fatalf("the plan header changed: %v", base)
		}
	})

	t.Run("the fields are accepted, normalized and change no operation", func(t *testing.T) {
		plan := planOf(t, withFields(valid,
			`"portal": {"enabled": true, "listen": "management"}`,
			`"management_interfaces": ["eth1", "eth0"]`))
		got := plan["answers"].(map[string]any)
		if want := map[string]any{"enabled": true, "listen": "management"}; !reflect.DeepEqual(got["portal"], want) {
			t.Errorf("portal = %v, want %v", got["portal"], want)
		}
		host := got["host"].(map[string]any)
		if want := []any{"eth0", "eth1"}; !reflect.DeepEqual(host["management_interfaces"], want) {
			t.Errorf("management_interfaces = %v, want sorted %v", host["management_interfaces"], want)
		}
		if !reflect.DeepEqual(plan["operations"], base["operations"]) || !reflect.DeepEqual(plan["pending_prerequisites"], base["pending_prerequisites"]) {
			t.Errorf("the fields changed the planned work: %v", plan)
		}
	})

	for _, tc := range []struct{ name, root, host string }{
		{"portal disabled alone", `"portal": {"enabled": false}`, ""},
		{"portal listen local alone", `"portal": {"listen": "local"}`, ""},
		{"portal empty", `"portal": {}`, ""},
		{"interfaces alone", "", `"management_interfaces": ["enp1s0", "br-mgmt.10", "wg_0"]`},
		{"sixteen interfaces", "", `"management_interfaces": ["e0","e1","e2","e3","e4","e5","e6","e7","e8","e9","e10","e11","e12","e13","e14","e15"]`},
		{"fifteen-byte interface", "", `"management_interfaces": ["abcdefghijklmno"]`},
	} {
		t.Run("accept "+tc.name, func(t *testing.T) { planOf(t, withFields(valid, tc.root, tc.host)) })
	}

	t.Run("the 64 KiB bound holds with the fields present", func(t *testing.T) {
		document := withFields(valid, `"portal": {"enabled": true, "listen": "management"}`, `"management_interfaces": ["eth0"]`)
		padded := document + strings.Repeat(" ", 64*1024-len(document))
		planOf(t, padded)
		reject(t, padded+" ", "$")
	})

	portal := `"portal": {"enabled": true, "listen": "management"}`
	interfaces := `"management_interfaces": ["eth0"]`
	for _, tc := range []struct{ name, root, host, path string }{
		{"unknown portal field", `"portal": {"enabled": true, "DO-NOT-PRINT": true}`, "", "$.portal"},
		{"unknown host field beside interfaces", "", interfaces + `, "management_addresses": ["DO-NOT-PRINT"]`, "$.host"},
		{"unknown root field beside portal", portal + `, "portals": {}`, interfaces, "$"},
		{"duplicate portal key", `"portal": {"enabled": false, "enabled": true}`, "", "$.portal"},
		{"duplicate portal object", `"portal": {}, "portal": {"enabled": true}`, "", "$"},
		{"duplicate interfaces key", "", interfaces + `, "management_interfaces": ["eth1"]`, "$.host"},
		{"null portal", `"portal": null`, "", "$.portal"},
		{"null enabled", `"portal": {"enabled": null}`, "", "$.portal.enabled"},
		{"string enabled", `"portal": {"enabled": "true"}`, "", "$.portal.enabled"},
		{"boolean listen", `"portal": {"listen": true}`, "", "$.portal.listen"},
		{"null interfaces", "", `"management_interfaces": null`, "$.host.management_interfaces"},
		{"string interfaces", "", `"management_interfaces": "eth0"`, "$.host.management_interfaces"},
		{"numeric interface", "", `"management_interfaces": [1]`, "$.host.management_interfaces[0]"},
		{"empty interfaces", "", `"management_interfaces": []`, "$.host.management_interfaces"},
		{"duplicate interface", "", `"management_interfaces": ["eth0", "eth0"]`, "$.host.management_interfaces"},
		{"seventeen interfaces", "", `"management_interfaces": ["e0","e1","e2","e3","e4","e5","e6","e7","e8","e9","e10","e11","e12","e13","e14","e15","e16"]`, "$.host.management_interfaces"},
		{"management without interfaces", portal, "", "$.portal.listen"},
		{"listen every IPv4 address", `"portal": {"listen": "0.0.0.0"}`, interfaces, "$.portal.listen"},
		{"listen every IPv6 address", `"portal": {"listen": "::"}`, interfaces, "$.portal.listen"},
		{"listen all", `"portal": {"listen": "all"}`, interfaces, "$.portal.listen"},
		{"listen empty", `"portal": {"listen": ""}`, interfaces, "$.portal.listen"},
		{"listen address", `"portal": {"listen": "DO-NOT-PRINT:9443"}`, interfaces, "$.portal.listen"},
	} {
		t.Run("refuse "+tc.name, func(t *testing.T) { reject(t, withFields(valid, tc.root, tc.host), tc.path) })
	}

	for _, name := range []string{"", ".", "..", "-eth0", "_eth0", "eth0 x", "eth/0", "eth0:1", "eth0%1", "abcdefghijklmnop", "$(DO-NOT-PRINT)", "é0"} {
		t.Run("refuse interface "+strings.ReplaceAll(name, "DO-NOT-PRINT", "redacted"), func(t *testing.T) {
			host := `"management_interfaces": [` + jsonString(name) + `]`
			reject(t, withFields(valid, "", host), "$.host.management_interfaces[0]")
		})
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
