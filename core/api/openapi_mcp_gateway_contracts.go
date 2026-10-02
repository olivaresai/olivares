// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package api

func addMCPGatewayContracts(paths, schemas map[string]any) {
	ref := func(name string) map[string]any { return oaObj("$ref", "#/components/schemas/"+name) }
	str := oaObj("type", "string")
	list := func(item map[string]any) map[string]any { return oaObj("type", "array", "items", item) }
	object := func(props map[string]any, required ...string) map[string]any {
		return oaObj("type", "object", "additionalProperties", false, "properties", props, "required", required)
	}
	version := oaObj("type", "integer", "minimum", 0, "description", "Current tenant configuration version; stale writes return 409.")
	schemas["MCPGatewayTrust"] = object(oaObj("resource", str, "issuer", str, "jwks_url", str, "jwks", oaObj("type", "object", "description", "Public asymmetric JWKS only; private keys are rejected.")), "resource", "issuer")
	schemas["MCPGatewayToolPolicy"] = object(oaObj("name", str, "required_scope", str, "destructive", oaObj("type", "boolean")), "name", "required_scope", "destructive")
	serverProps := oaObj("name", str, "transport", oaObj("type", "string", "enum", []string{"stdio", "streamable_http"}), "url", oaObj("type", "string", "format", "uri", "description", "Exact HTTPS endpoint without credentials, query or fragment."), "credential_ref", oaObj("type", "string", "description", "Own tenant store:mcp/<name> reference; empty means no upstream authentication. Never a value or global fallback."), "egress_cidrs", list(str), "trust", ref("MCPGatewayTrust"), "allowed_tools", list(ref("MCPGatewayToolPolicy")), "enabled", oaObj("type", "boolean"))
	serverProps["command"] = oaObj("type", "string", "description", "Executable on the engine node. No shell is used. Infer stdio when supplied; omit URL. Runs as the engine user in the session folder with the session runner; process confinement follows the runner's reported state. MCP does not add network egress confinement.")
	serverProps["args"] = list(str)
	serverProps["args"].(map[string]any)["description"] = "Existing script and file arguments receive exact read/execute grants without opening their parents. Existing named code directories are read-only. The session folder is the only writable user directory; the runner supplies a private home and temp. Engine protected files remain denied; managed code below data/tools is readable."
	serverProps["env"] = oaObj("type", "object", "additionalProperties", str, "description", "Public environment values only; credentials belong in env_secret_refs. HOME, TMPDIR, TMP and TEMP are reserved: the runner supplies private child directories, removed at exit.")
	serverProps["env_secret_refs"] = oaObj("type", "object", "additionalProperties", str, "description", "Environment names mapped to own-tenant store:mcp/ references. Values resolve at launch and never enter arguments or logs.")
	serverProps["allowed_tools"] = oaObj("type", "array", "items", ref("MCPGatewayToolPolicy"), "description", "Explicit administrator-authored tool authority. Enabling with this field omitted gives every tested tool tools:call scope and requires approval, regardless of server annotations. Only an explicit policy can allow a call without approval. An explicit empty array permits no tools.")
	schemas["MCPGatewayServerInput"] = object(serverProps, "name")
	readProps := oaObj()
	for k, v := range serverProps {
		readProps[k] = v
	}
	readProps["id"] = str
	readProps["proposed_allow"] = oaObj("type", "array", "items", str, "maxItems", 128, "readOnly", true, "description", "Registered tool names whose current successful catalogue declares read-only behavior. A proposal for administrator review, never authority: Test observes declarations without proving tool safety. Empty when no current proposal exists.")
	readProps["probe"] = object(oaObj("state", str, "tested_at", str, "tools", list(object(oaObj("name", str, "fingerprint", str, "read_only", oaObj("type", "boolean")), "name", "fingerprint")),
		"reason", oaObj("type", "string", "enum", []string{"dns", "tls", "timeout", "connection_refused", "http_status", "process_start", "process_exit"},
			"description", "Why an unreachable test failed. Local servers report process_start or process_exit and a redacted one-line detail; remote failures report their connection class or HTTP status."),
		"detail", oaObj("type", "string", "maxLength", 512, "description", "One redacted stderr line or OS error explaining a local server failure. Also present in the server's stored status. Credentials and terminal control characters are removed."),
		"http_status", oaObj("type", "integer", "minimum", 400, "maximum", 599, "description", "The HTTP status the server answered, with reason http_status.")), "state", "tools")
	schemas["MCPGatewayServer"] = object(readProps, "id", "name", "transport", "url", "trust", "enabled", "probe", "proposed_allow", "egress_cidrs", "allowed_tools")
	schemas["MCPGatewaySnapshot"] = object(oaObj("version", version, "source", oaObj("type", "string", "enum", []string{"file", "store"}), "read_only", oaObj("type", "boolean"), "session_tools", oaObj("type", "boolean"), "session_endpoint", oaObj("type", "string", "description", "One /session/mcp endpoint aggregates the session’s enabled servers and Olivares work tools. It accepts the launch-issued session credential and calls internal module APIs with the resolved principal; it never forwards that bearer into the admin API. Message and communication tools are not exposed in 26.10.1. Server tool aliases are stable and server-scoped."), "servers", list(ref("MCPGatewayServer")), "governance", oaObj("type", "object", "additionalProperties", str)), "version", "source", "read_only", "session_tools", "session_endpoint", "servers", "governance")
	schemas["MCPGatewayServerWrite"] = object(oaObj("version", version, "server", ref("MCPGatewayServerInput")), "version", "server")
	schemas["MCPGatewayVersion"] = object(oaObj("version", version), "version")
	schemas["MCPGatewaySessionWrite"] = object(oaObj("version", version, "enabled", oaObj("type", "boolean")), "version", "enabled")
	operation := func(id, description, bodyName string, created bool) map[string]any {
		responses := oaObj("200", oaJSONRespSchema("Current reference-only configuration", ref("MCPGatewaySnapshot")))
		if created {
			responses = oaObj("201", oaJSONRespSchema("Disabled server added", ref("MCPGatewaySnapshot")))
		}
		for _, code := range []string{"400", "401", "403", "404", "409", "503"} {
			responses[code] = oaJSONRespSchema("Admission, source ownership, version or availability refusal", ref("Error"))
		}
		op := oaObj("operationId", id, "x-stability", "stable", "summary", description, "description", description, "tags", []string{"console"}, "security", []any{oaObj("bearerAuth", []any{})}, "x-required-permission", "tenant:admin", "responses", responses, "parameters", []any{oaParam("X-Olivares-Tenant", "header", "Authenticated target tenant; workspace-confined credentials are refused.", false, oaObj("type", "string", "format", "uuid"))})
		if bodyName != "" {
			op["requestBody"] = oaObj("required", true, "content", oaObj("application/json", oaObj("schema", ref(bodyName))))
			op["x-required-aal"] = 3
		}
		return op
	}
	paths["/v1/console/mcp-gateway"] = oaObj("get", operation("getMCPGateway", "Read the effective MCP gateway configuration and governance.", "", false))
	paths["/v1/console/mcp-gateway/servers"] = oaObj("post", operation("addMCPGatewayServer", "Add a disabled upstream using tenant secret references (AAL3).", "MCPGatewayServerWrite", true))
	item := oaObj("put", operation("updateMCPGatewayServer", "Update or explicitly enable a tested upstream (AAL3).", "MCPGatewayServerWrite", false), "delete", operation("removeMCPGatewayServer", "Remove an upstream from the tenant gateway (AAL3).", "MCPGatewayVersion", false))
	item["parameters"] = []any{oaParam("id", "path", "Server identifier in this tenant.", true, oaObj("type", "string", "format", "uuid"))}
	paths["/v1/console/mcp-gateway/servers/{id}"] = item
	paths["/v1/console/mcp-gateway/servers/{id}/test"] = oaObj("parameters", item["parameters"], "post", operation("testMCPGatewayServer", "Initialize and list tools without calling them; record a bounded verdict (AAL3).", "MCPGatewayVersion", false))
	paths["/v1/console/mcp-gateway/session-tools"] = oaObj("put", operation("setMCPGatewaySessionTools", "Set the default-off session MCP switch for this tenant (AAL3).", "MCPGatewaySessionWrite", false))
	// The same sealed manager has two explicit scopes; tenant mode serves mcp/ handles only.
	secretItem := paths["/v1/console/secrets"].(map[string]any)
	secretItem["parameters"] = []any{oaParam("scope", "query", "Omit for superadmin global secrets; tenant selects own tenant mcp/ handles and requires tenant admin. Writes require AAL3 in either scope.", false, oaObj("type", "string", "enum", []string{"tenant"})), oaParam("X-Olivares-Tenant", "header", "Target tenant in explicit tenant mode.", false, oaObj("type", "string", "format", "uuid"))}
}
