// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

// catalogdocs_test.go (E4b/E4c) — engine↔docs catalog parity. The docs-site
// connector catalog (reference/connectors.md, the EN page — the authoritative one,
// the locales are marked-as-MT derivatives) is CURATED: kinds are grouped by honest
// coverage tier with per-kind judgment notes, which no generator can derive from a
// Descriptor. What CAN be enforced mechanically is set equality: every kind this
// build wires must be named on the page, and every kind-looking table row on the
// page must be wired (or deliberately excluded below). By 2026-07 the page had
// drifted ~37 kinds behind the composition root; this test makes that class of
// drift a build failure instead of a docs audit finding.

const connectorsDocPath = "../../docs-site/src/content/docs/reference/connectors.md"

// wiredCatalogKinds returns every connector kind the default build can wire, from
// the composition root's own switches/maps (AST-parsed — see switchCaseKinds).
func wiredCatalogKinds(t *testing.T) map[string]bool {
	t.Helper()
	kinds := map[string]bool{}
	for _, k := range switchCaseKinds(t, "sources.go", "buildInProcSource") {
		kinds[k] = true
	}
	for _, k := range switchCaseKinds(t, "sources.go", "buildRosterProvider") {
		kinds[k] = true
	}
	for _, k := range switchCaseKinds(t, "sources.go", "buildContentSource") {
		kinds[k] = true
	}
	for _, k := range switchCaseKinds(t, "notifydispatch.go", "buildOutputConnector") {
		kinds[k] = true
	}
	for k := range pluginBinaryForKind {
		kinds[k] = true
	}
	for k := range outputPluginForKind {
		kinds[k] = true
	}
	return kinds
}

// docAliasKinds are wired kind aliases the docs page deliberately does NOT list as
// their own entries — each resolves to the same connector as a canonical kind the
// page does list.
var docAliasKinds = map[string]string{
	"okta":          "alias of idp",
	"entra":         "alias of idp",
	"pg-audit":      "alias of pgaudit",
	"s3-cloudtrail": "alias of s3cloudtrail",
	"pgcontent":     "alias of postgres (content source)",
	"fscontent":     "alias of filesystem (content source)",
	"ping":          "alias of pingone",
}

// TestConnectorDocsListEveryWiredKind: every wired, non-alias kind appears as a
// `kind` code span in the docs catalog page.
func TestConnectorDocsListEveryWiredKind(t *testing.T) {
	doc, err := os.ReadFile(connectorsDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", connectorsDocPath, err)
	}
	spans := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-z0-9_/-]+)`").FindAllStringSubmatch(string(doc), -1) {
		spans[m[1]] = true
	}
	var missing []string
	for kind := range wiredCatalogKinds(t) {
		if _, isAlias := docAliasKinds[kind]; isAlias {
			continue
		}
		if !spans[kind] {
			missing = append(missing, kind)
		}
	}
	sort.Strings(missing)
	for _, kind := range missing {
		t.Errorf("kind %q is wired in this build but absent from %s — add it to the right coverage-tier table (or register a deliberate alias above)", kind, connectorsDocPath)
	}
}

// TestConnectorDocsListNoUnwiredKind: every kind-shaped FIRST-COLUMN code span of
// the page's tables is actually wired — the docs never advertise a connector the
// binary cannot construct. Rows that are legitimately not connector kinds (the MCP
// method table) are excluded explicitly.
func TestConnectorDocsListNoUnwiredKind(t *testing.T) {
	doc, err := os.ReadFile(connectorsDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", connectorsDocPath, err)
	}
	notKinds := map[string]bool{
		// The MCP gateway method-coverage table lists JSON-RPC methods, not kinds.
		"tools/call": true, "resources/read": true, "prompts/get": true,
	}
	wired := wiredCatalogKinds(t)
	rowRe := regexp.MustCompile("(?m)^\\| `([a-z0-9_/-]+)`")
	for _, m := range rowRe.FindAllStringSubmatch(string(doc), -1) {
		kind := m[1]
		if notKinds[kind] || wired[kind] {
			continue
		}
		t.Errorf("docs table row names %q but this build wires no such kind — remove the row or wire the connector (%s)", kind, connectorsDocPath)
	}
}

// TestConnectorDocsOutputsComplete (E4c): the output-destinations paragraph lists
// EXACTLY the kinds buildOutputConnector + outputPluginForKind can wire — 19
// in-process + 3 plugin egress kinds as of (otlplog and s3archive were
// missing; kafka/amqp/cloudqueue became reachable when the plugin-output path was
// wired).
func TestConnectorDocsOutputsComplete(t *testing.T) {
	doc, err := os.ReadFile(connectorsDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", connectorsDocPath, err)
	}
	// The outputs section is delimited by its heading and the next heading.
	sec := regexp.MustCompile(`(?s)## Output destinations.*?\n## `).FindString(string(doc))
	if sec == "" {
		t.Fatalf("could not locate the '## Output destinations' section in %s", connectorsDocPath)
	}
	spans := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-z0-9_/-]+)`").FindAllStringSubmatch(sec, -1) {
		spans[m[1]] = true
	}
	var outputs []string
	outputs = append(outputs, switchCaseKinds(t, "notifydispatch.go", "buildOutputConnector")...)
	for k := range outputPluginForKind {
		outputs = append(outputs, k)
	}
	sort.Strings(outputs)
	for _, kind := range outputs {
		if !spans[kind] {
			t.Errorf("output kind %q is buildable but not listed in the Output destinations section of %s", kind, connectorsDocPath)
		}
	}
}
