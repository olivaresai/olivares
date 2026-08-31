// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"
)

func TestClientInitialize(t *testing.T) {
	m := newMockTransport()
	m.reply("initialize", `{"protocolVersion":"2025-06-18","serverInfo":{"name":"srv","version":"1"},"capabilities":{"tools":{}}}`)
	c := newClient(m)

	res, err := c.Initialize(t.Context())
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if res.ProtocolVersion != "2025-06-18" || res.ServerInfo.Name != "srv" {
		t.Errorf("init result = %+v", res)
	}
	if m.version != "2025-06-18" {
		t.Errorf("negotiated version not propagated to transport: %q", m.version)
	}
	if !hasCapability(res.Capabilities, "tools") {
		t.Error("tools capability not parsed")
	}
}

func TestClientListToolsPagination(t *testing.T) {
	m := newMockTransport()
	m.reply("tools/list",
		`{"tools":[{"name":"a"}],"nextCursor":"c2"}`,
		`{"tools":[{"name":"b"}]}`,
	)
	c := newClient(m)
	tools, err := c.ListTools(t.Context())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "a" || tools[1].Name != "b" {
		t.Errorf("pagination failed: %+v", tools)
	}
}

func TestClientListPropagatesRPCError(t *testing.T) {
	m := newMockTransport()
	// No reply scripted for resources/list → mock returns an error.
	c := newClient(m)
	if _, err := c.ListResources(t.Context()); err == nil {
		t.Error("expected error to propagate from transport")
	}
}

func TestClientListPromptsAndTemplates(t *testing.T) {
	m := newMockTransport()
	m.reply("prompts/list", `{"prompts":[{"name":"p1"}]}`)
	m.reply("resources/templates/list", `{"resourceTemplates":[{"uriTemplate":"x://{a}"}]}`)
	c := newClient(m)

	prompts, err := c.ListPrompts(t.Context())
	if err != nil || len(prompts) != 1 || prompts[0].Name != "p1" {
		t.Errorf("prompts = %+v err=%v", prompts, err)
	}
	tpls, err := c.ListResourceTemplates(t.Context())
	if err != nil || len(tpls) != 1 || tpls[0].URITemplate != "x://{a}" {
		t.Errorf("templates = %+v err=%v", tpls, err)
	}
}
