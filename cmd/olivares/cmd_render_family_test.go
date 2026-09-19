// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/internal/termrender"
)

// This file holds the two assertions every command family gets when its output
// moves onto the renderer.
//
// THE GOLDEN pins the plain form as BYTES. A test that looks a column up by name
// survives the defect it was written for: the first version of the tool-detect
// test did exactly that, and it passed while rows were being rebuilt from
// non-empty values only — that is, while the original misalignment was back.
//
// THE PROPERTY pins that colour is additive: stripping SGR from the coloured form
// yields the plain form byte for byte. The renderer asserts this about its own
// primitives; this file asserts it about the DATA each command builds, which is
// where a caller can still get it wrong — a cell padded before it is painted puts
// its trailing spaces inside the escape sequence, where the plain form has none.
//
// Both need the command's data separated from the writing of it, which is why the
// families expose a `…Fields`/`…Table` builder. A renderer built for a buffer
// never colours, so a property about colour cannot be asserted through a function
// that owns its own renderer.

// plainOf renders draw with colour off and no terminal width, which is what a
// pipe gets.
func plainOf(draw func(*termrender.Renderer)) string {
	var b bytes.Buffer
	off := false
	draw(termrender.New(&b, termrender.Options{Color: &off}))
	return b.String()
}

// richOf renders draw with colour on at a stated width, which is what a terminal
// gets. The width is explicit so the case decides between the column form and the
// record form instead of inheriting the environment's COLUMNS.
func richOf(draw func(*termrender.Renderer), width int) string {
	var b bytes.Buffer
	on := true
	draw(termrender.New(&b, termrender.Options{Color: &on, Width: width}))
	return b.String()
}

// assertRichIsPlainPlusColour is the property, at one stated width.
func assertRichIsPlainPlusColour(t *testing.T, name string, width int, draw func(*termrender.Renderer)) {
	t.Helper()
	var b bytes.Buffer
	off := false
	draw(termrender.New(&b, termrender.Options{Color: &off, Width: width}))
	plain := b.String()
	rich := richOf(draw, width)
	if stripped := termrender.StripANSI(rich); stripped != plain {
		t.Errorf("%s at width %d: stripping colour did not give the plain form.\nplain:\n%q\nstripped:\n%q",
			name, width, plain, stripped)
	}
	if width > 0 && !strings.Contains(rich, "\x1b[") {
		t.Errorf("%s at width %d: the coloured form carries no escape sequence, so the property "+
			"above compared two identical strings and proved nothing", name, width)
	}
}

func providerFixture() map[string]any {
	return map[string]any{
		"provider_ref": "prv_01J8ABCDEFGHJKMNPQRSTVWXYZ",
		"display_name": "Anthropic",
		"kind":         "anthropic",
		"base_url":     "https://api.anthropic.com",
		"key_hint":     "…0000",
		"state":        "active",
		"probe_state":  "ok",
		"models":       []any{"claude-opus-5", "claude-sonnet-5"},
	}
}

func profileFixture() map[string]any {
	return map[string]any{
		"profile_ref":     "ppf_01J8ABCDEFGHJKMNPQRSTVWXYZ",
		"display_name":    "Claude",
		"driver":          "claude",
		"environment_ref": "env_01J8ABCDEFGHJKMNPQRSTVWXYZ",
		"state":           "active",
		"auth_source":     "provider_account_home",
	}
}

func TestProviderRecordPlainGolden(t *testing.T) {
	got := plainOf(func(r *termrender.Renderer) { r.Fields(providerRecordFields(providerFixture(), "")) })
	want := "PROVIDER    prv_01J8ABCDEFGHJKMNPQRSTVWXYZ\n" +
		"NAME        Anthropic\n" +
		"KIND        anthropic\n" +
		"ENDPOINT    https://api.anthropic.com\n" +
		"KEY         …0000\n" +
		"STATE       active\n" +
		"CONNECTION  tested: the provider answered and accepted this credential\n" +
		"MODELS      2 (claude-opus-5, claude-sonnet-5)\n"
	if got != want {
		t.Errorf("the provider record's plain form changed.\n got: %q\nwant: %q", got, want)
	}
}

func TestProviderTablePlainGolden(t *testing.T) {
	got := plainOf(func(r *termrender.Renderer) { r.Table(providerTable([]map[string]any{providerFixture()})) })
	want := "PROVIDER                        NAME       KIND       KEY    STATE   CONNECTION\n" +
		"prv_01J8ABCDEFGHJKMNPQRSTVWXYZ  Anthropic  anthropic  …0000  active  ok\n"
	if got != want {
		t.Errorf("the provider table's plain form changed.\n got: %q\nwant: %q", got, want)
	}
}

// TestProviderTableEmptyStateNamesTheNextCommand pins the empty case, which is the
// case a new operator meets first. A list that prints nothing is indistinguishable
// from a command that did nothing.
func TestProviderTableEmptyStateNamesTheNextCommand(t *testing.T) {
	var b bytes.Buffer
	if err := printProviderTable(&b, nil); err != nil {
		t.Fatalf("printProviderTable: %v", err)
	}
	got := b.String()
	want := "No providers registered.\n" +
		"next: olivares provider add --kind anthropic --name \"Anthropic\" < key.txt\n"
	if got != want {
		t.Errorf("the empty provider list changed.\n got: %q\nwant: %q", got, want)
	}
}

func TestProfileRecordPlainGolden(t *testing.T) {
	got := plainOf(func(r *termrender.Renderer) { r.Fields(profileRecordFields(profileFixture())) })
	want := "PROFILE          ppf_01J8ABCDEFGHJKMNPQRSTVWXYZ\n" +
		"NAME             Claude\n" +
		"DRIVER           claude\n" +
		"ENVIRONMENT      env_01J8ABCDEFGHJKMNPQRSTVWXYZ\n" +
		"STATE            active\n" +
		"AUTH SOURCE      provider_account_home\n" +
		"PROVIDER         none (the host's own credential variables decide)\n" +
		"TOOLS            not declared — sessions launch with NO built-in tools (deny-closed); declare them with `agent profile update --tools`\n" +
		"PERMISSION MODE  not declared (each launch keeps its own)\n"
	if got != want {
		t.Errorf("the profile record's plain form changed.\n got: %q\nwant: %q", got, want)
	}
}

// TestProviderFamilyRichIsPlainPlusColour is the property, over every shape this
// family renders and at both widths that matter: wide enough for the column form,
// and narrow enough to force the record fallback.
func TestProviderFamilyRichIsPlainPlusColour(t *testing.T) {
	rows := []map[string]any{providerFixture(), {
		"provider_ref": "prv_01J8ZYXWVUTSRQPNMKJHGFEDCB",
		"display_name": "OpenAI",
		"kind":         "openai",
		"key_hint":     "…1111",
		"state":        "active",
		"probe_state":  "refused",
	}}
	cases := []struct {
		name string
		draw func(*termrender.Renderer)
	}{
		{"provider record", func(r *termrender.Renderer) { r.Fields(providerRecordFields(providerFixture(), "ppf_01")) }},
		{"provider table", func(r *termrender.Renderer) { r.Table(providerTable(rows)) }},
		{"profile record", func(r *termrender.Renderer) { r.Fields(profileRecordFields(profileFixture())) }},
		{"profile table", func(r *termrender.Renderer) { r.Table(profileTable([]map[string]any{profileFixture()})) }},
		{"deploy record", func(r *termrender.Renderer) {
			r.Fields(deployFields("claude", nil, profileFixture(), deployHomes{config: "/c", user: "/u"}))
		}},
	}
	for _, c := range cases {
		for _, width := range []int{200, 40} {
			assertRichIsPlainPlusColour(t, c.name, width, c.draw)
		}
	}
}

// TestProviderTableRecordFallbackKeepsEveryIdentifierWhole is the reason the table
// primitive has a record form at all: a provider reference an operator has to copy
// is never cut, whatever the terminal is.
func TestProviderTableRecordFallbackKeepsEveryIdentifierWhole(t *testing.T) {
	narrow := richOf(func(r *termrender.Renderer) {
		r.Table(providerTable([]map[string]any{providerFixture()}))
	}, 40)
	if !strings.Contains(termrender.StripANSI(narrow), "prv_01J8ABCDEFGHJKMNPQRSTVWXYZ") {
		t.Errorf("a 40-column terminal lost the provider reference:\n%s", termrender.StripANSI(narrow))
	}
	if strings.Contains(narrow, "…\n") {
		t.Errorf("something truncated a cell; no primitive may:\n%s", termrender.StripANSI(narrow))
	}
}

// TestNoFamilyLeadsWithTheTransportEnvelope walks the per-family error
// constructors and asserts each one leads with what happened, not with the
// envelope around it.
//
// MEASURED on 2026-09-18 by walking the first hour, verbatim from the terminal:
//
//	request failed: HTTP 422: {"error":{"code":"invalid_argument","message":
//	"set auth_source to provider_account_home or managed_injection"}}
//
// The engine had written a usable sentence and the CLI printed the envelope
// around it. "request failed" is the least informative part of that line and it
// came first; what to do about it was inside two levels of JSON. One family was
// repaired when that was measured and five kept the old shape, each with its own
// copy of the same Errorf. They share one now.
//
// Nothing is dropped: the status a script logged and the code a support
// conversation quotes are still in the line, as the parenthetical they belong in.
func TestNoFamilyLeadsWithTheTransportEnvelope(t *testing.T) {
	body := []byte(`{"error":{"code":"invalid_argument","message":"set auth_source to provider_account_home"}}`)
	cases := []struct {
		family string
		err    error
	}{
		{"work", workHTTPError(422, body)},
		{"modelstack", modelstackHTTPError(modelstackResult{Status: 422, Raw: body})},
		{"datalane", datalaneHTTPError("sources", 404, body)},
		{"bootstrap", bootstrapHTTPError(422, body)},
	}
	for _, c := range cases {
		got := c.err.Error()
		if strings.Contains(got, "request failed: HTTP") {
			t.Errorf("%s still leads with the transport envelope: %s", c.family, got)
		}
		if !strings.Contains(got, "set auth_source to provider_account_home") {
			t.Errorf("%s dropped the sentence the engine wrote: %s", c.family, got)
		}
		if !strings.Contains(got, "HTTP 4") {
			t.Errorf("%s dropped the status a script logs: %s", c.family, got)
		}
	}
}

// TestGovernanceTablesPlainGolden pins the governance family's plain form as
// bytes, including the empty case each list now has. Before this, an empty
// kill-switch list printed a header row and nothing under it, and an operator
// could not tell "nothing is engaged" from "the query did not run".
func TestGovernanceTablesPlainGolden(t *testing.T) {
	var b bytes.Buffer
	if err := writeKillSwitchTable(&b, nil); err != nil {
		t.Fatalf("writeKillSwitchTable: %v", err)
	}
	if got, want := b.String(), "no kill switch is engaged\n"; got != want {
		t.Errorf("the empty kill-switch list changed.\n got: %q\nwant: %q", got, want)
	}

	b.Reset()
	if err := writeApprovalTable(&b, []cliApproval{{
		ID: "apr_01", Action: "agent.deploy", SubjectRef: "agt_01", Status: "pending",
		RiskTier: "high", ApproveCount: 1, RejectCount: 0, RequiredApprovals: 2,
		RequestedBy: "usr_01", ExpiresAt: "2026-09-19T00:00:00Z",
	}}); err != nil {
		t.Fatalf("writeApprovalTable: %v", err)
	}
	got := b.String()
	want := "ID      ACTION        SUBJECT  STATUS   TIER  VOTES       REQUESTED BY  EXPIRES               ESCALATED\n" +
		"apr_01  agent.deploy  agt_01   pending  high  1+/0- of 2  usr_01        2026-09-19T00:00:00Z  no\n"
	if got != want {
		t.Errorf("the approval table's plain form changed.\n got: %q\nwant: %q", got, want)
	}
}

// TestGovernanceRichIsPlainPlusColour is the property over the governance family,
// which is the one that colours a cell by a lifecycle status.
func TestGovernanceRichIsPlainPlusColour(t *testing.T) {
	switches := []cliKillSwitch{
		{ID: "ks_01", ScopeKind: "tenant", ScopeRef: "ten_01", Status: "engaged", Source: "operator", EngagedBy: "usr_01"},
		{ID: "ks_02", ScopeKind: "agent", AgentExternalID: "agt_09", Status: "released", Source: "guardian"},
	}
	for _, width := range []int{200, 40} {
		// writeKillSwitchTable owns its renderer, so the property is asserted on the
		// same Table it builds — which is where a caller can get it wrong.
		assertRichIsPlainPlusColour(t, "kill switches", width, func(r *termrender.Renderer) {
			r.Table(killSwitchTable(switches))
		})
	}
}
