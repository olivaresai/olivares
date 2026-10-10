// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package api

import "github.com/olivaresai/olivares/core/api/oas"

func addMCPGatewayContracts(paths, schemas map[string]any) {
	ref := func(name string) map[string]any { return oas.Obj("$ref", "#/components/schemas/"+name) }
	str := oas.Obj("type", "string")
	list := func(item map[string]any) map[string]any { return oas.Obj("type", "array", "items", item) }
	object := func(props map[string]any, required ...string) map[string]any {
		return oas.Obj("type", "object", "additionalProperties", false, "properties", props, "required", required)
	}
	version := oas.Obj("type", "integer", "minimum", 0, "description", "Current tenant configuration version; stale writes return 409.")
	schemas["MCPGatewayTrust"] = object(oas.Obj("resource", str, "issuer", str, "jwks_url", str, "jwks", oas.Obj("type", "object", "description", "Public asymmetric JWKS only; private keys are rejected.")), "resource", "issuer")
	schemas["MCPGatewayToolPolicy"] = object(oas.Obj("name", str, "required_scope", str, "destructive", oas.Obj("type", "boolean")), "name", "required_scope", "destructive")
	serverProps := oas.Obj("name", str, "transport", oas.Obj("type", "string", "enum", []string{"stdio", "streamable_http"}), "url", oas.Obj("type", "string", "format", "uri", "description", "Exact HTTPS endpoint without credentials, query or fragment."), "credential_ref", oas.Obj("type", "string", "description", "Own tenant store:mcp/<name> reference; empty means no upstream authentication. Never a value or global fallback."), "egress_cidrs", list(str), "trust", ref("MCPGatewayTrust"), "allowed_tools", list(ref("MCPGatewayToolPolicy")), "enabled", oas.Obj("type", "boolean"))
	serverProps["command"] = oas.Obj("type", "string", "description", "Executable on the engine node. No shell is used. Infer stdio when supplied; omit URL. Runs as the engine user in the session folder with the session runner; process confinement follows the runner's reported state. Without egress_hosts the command keeps the network of its launch.")
	serverProps["egress_hosts"] = oas.Obj("type", "array", "maxItems", 16, "items", str, "description", "Command servers only: the HTTPS hosts (host or host:port, lowercase, port 443 implied) the command may reach, at public addresses only, through a per-launch network namespace and proxy. Any other host, localhost and loopback or private addresses are refused. A host that cannot build that boundary refuses the start. Changing the list withdraws the last test.")
	serverProps["args"] = list(str)
	serverProps["args"].(map[string]any)["description"] = "Existing script and file arguments receive exact read/execute grants without opening their parents. Existing named code directories are read-only. The session folder is the only writable user directory; the runner supplies a private home and temp. Engine protected files remain denied; managed code below data/tools is readable."
	serverProps["env"] = oas.Obj("type", "object", "additionalProperties", str, "description", "Public environment values only; credentials belong in env_secret_refs. HOME, TMPDIR, TMP and TEMP are reserved: the runner supplies private child directories, removed at exit.")
	serverProps["env_secret_refs"] = oas.Obj("type", "object", "additionalProperties", str, "description", "Environment names mapped to own-tenant store:mcp/ references. Values resolve at launch and never enter arguments or logs.")
	serverProps["allowed_tools"] = oas.Obj("type", "array", "items", ref("MCPGatewayToolPolicy"), "description", "Explicit administrator-authored tool authority. Enabling with this field omitted gives every tested tool tools:call scope and requires approval, regardless of server annotations. Only an explicit policy can allow a call without approval. An explicit empty array permits no tools.")
	schemas["MCPGatewayServerInput"] = object(serverProps, "name")
	readProps := oas.Obj()
	for k, v := range serverProps {
		readProps[k] = v
	}
	readProps["id"] = str
	readProps["proposed_allow"] = oas.Obj("type", "array", "items", str, "maxItems", 128, "readOnly", true, "description", "Registered tool names whose current successful catalogue declares read-only behavior. A proposal for administrator review, never authority: Test observes declarations without proving tool safety. Empty when no current proposal exists.")
	readProps["probe"] = object(oas.Obj("state", str, "tested_at", str, "tools", list(object(oas.Obj("name", str, "fingerprint", str, "read_only", oas.Obj("type", "boolean")), "name", "fingerprint")),
		"reason", oas.Obj("type", "string", "enum", []string{"dns", "tls", "timeout", "connection_refused", "http_status", "process_start", "process_exit"},
			"description", "Why an unreachable test failed. Local servers report process_start or process_exit and a redacted one-line detail; remote failures report their connection class or HTTP status."),
		"detail", oas.Obj("type", "string", "maxLength", 512, "description", "One redacted stderr line or OS error explaining a local server failure. Also present in the server's stored status. Credentials and terminal control characters are removed."),
		"http_status", oas.Obj("type", "integer", "minimum", 400, "maximum", 599, "description", "The HTTP status the server answered, with reason http_status.")), "state", "tools")
	schemas["MCPGatewayServer"] = object(readProps, "id", "name", "transport", "url", "trust", "enabled", "probe", "proposed_allow", "egress_cidrs", "allowed_tools")
	schemas["MCPGatewaySnapshot"] = object(oas.Obj("version", version, "source", oas.Obj("type", "string", "enum", []string{"file", "store"}), "read_only", oas.Obj("type", "boolean"), "session_tools", oas.Obj("type", "boolean"), "session_endpoint", oas.Obj("type", "string", "description", "One /session/mcp endpoint aggregates the session’s enabled servers and Olivares work tools. It accepts the launch-issued session credential and calls internal module APIs with the resolved principal; it never forwards that bearer into the admin API. Server tool aliases are stable and server-scoped."), "servers", list(ref("MCPGatewayServer")), "governance", oas.Obj("type", "object", "additionalProperties", str)), "version", "source", "read_only", "session_tools", "session_endpoint", "servers", "governance")
	schemas["MCPGatewayServerWrite"] = object(oas.Obj("version", version, "server", ref("MCPGatewayServerInput")), "version", "server")
	schemas["MCPGatewayVersion"] = object(oas.Obj("version", version), "version")
	schemas["MCPGatewaySessionWrite"] = object(oas.Obj("version", version, "enabled", oas.Obj("type", "boolean")), "version", "enabled")
	operation := func(id, description, bodyName string, created bool) map[string]any {
		responses := oas.Obj("200", oas.JSONRespSchema("Current reference-only configuration", ref("MCPGatewaySnapshot")))
		if created {
			responses = oas.Obj("201", oas.JSONRespSchema("Disabled server added", ref("MCPGatewaySnapshot")))
		}
		for _, code := range []string{"400", "401", "403", "404", "409", "503"} {
			responses[code] = oas.JSONRespSchema("Admission, source ownership, version or availability refusal", ref("Error"))
		}
		op := oas.Obj("operationId", id, "x-stability", "stable", "summary", description, "description", description, "tags", []string{"console"}, "security", []any{oas.Obj("bearerAuth", []any{})}, "x-required-permission", "tenant:admin", "responses", responses, "parameters", []any{oas.Param("X-Olivares-Tenant", "header", "Authenticated target tenant; workspace-confined credentials are refused.", false, oas.Obj("type", "string", "format", "uuid"))})
		if bodyName != "" {
			op["requestBody"] = oas.Obj("required", true, "content", oas.Obj("application/json", oas.Obj("schema", ref(bodyName))))
			op["x-required-aal"] = 3
		}
		return op
	}
	paths["/v1/console/mcp-gateway"] = oas.Obj("get", operation("getMCPGateway", "Read the effective MCP gateway configuration and governance.", "", false))
	paths["/v1/console/mcp-gateway/servers"] = oas.Obj("post", operation("addMCPGatewayServer", "Add a disabled upstream using tenant secret references (AAL3).", "MCPGatewayServerWrite", true))
	item := oas.Obj("put", operation("updateMCPGatewayServer", "Update or explicitly enable a tested upstream (AAL3).", "MCPGatewayServerWrite", false), "delete", operation("removeMCPGatewayServer", "Remove an upstream from the tenant gateway (AAL3).", "MCPGatewayVersion", false))
	item["parameters"] = []any{oas.Param("id", "path", "Server identifier in this tenant.", true, oas.Obj("type", "string", "format", "uuid"))}
	paths["/v1/console/mcp-gateway/servers/{id}"] = item
	paths["/v1/console/mcp-gateway/servers/{id}/test"] = oas.Obj("parameters", item["parameters"], "post", operation("testMCPGatewayServer", "Initialize and list tools without calling them; record a bounded verdict (AAL3).", "MCPGatewayVersion", false))
	paths["/v1/console/mcp-gateway/session-tools"] = oas.Obj("put", operation("setMCPGatewaySessionTools", "Set the default-off session MCP switch for this tenant (AAL3).", "MCPGatewaySessionWrite", false))
	// The same sealed manager has two explicit scopes; tenant mode serves mcp/ handles only.
	secretItem := paths["/v1/console/secrets"].(map[string]any)
	secretItem["parameters"] = []any{oas.Param("scope", "query", "Omit for superadmin global secrets; tenant selects own tenant mcp/ handles and requires tenant admin. Writes require AAL3 in either scope.", false, oas.Obj("type", "string", "enum", []string{"tenant"})), oas.Param("X-Olivares-Tenant", "header", "Target tenant in explicit tenant mode.", false, oas.Obj("type", "string", "format", "uuid"))}
}
