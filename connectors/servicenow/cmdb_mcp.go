// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package servicenow

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
)

// ServeCMDBMCP exposes one configured resource over stdio. Register the optional
// executable in the existing managed MCP gateway; it owns admission and audit.
func (r *CMDBReader) ServeCMDBMCP(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	encoder := json.NewEncoder(output)
	initialized := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.JSONRPC != "2.0" {
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32600, "message": "Invalid request"}}); err != nil {
				return err
			}
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		fail := func(code int, message string) { response["error"] = map[string]any{"code": code, "message": message} }
		switch request.Method {
		case "initialize":
			initialized = true
			response["result"] = map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "olivares-servicenow-cmdb", "version": "0.1.0"}}
		case "ping":
			response["result"] = map[string]any{}
		case "tools/list":
			if !initialized {
				fail(-32600, "Initialize the adapter first")
				break
			}
			response["result"] = map[string]any{"tools": []any{map[string]any{
				"name": "read_cmdb_resource", "description": "Read current allowlisted metadata for the configured CMDB resource.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
				"outputSchema": map[string]any{"type": "object", "required": []string{"sys_id", "name", "sys_class_name", "updated_at", "collected_at"}, "properties": map[string]any{
					"sys_id": map[string]string{"type": "string"}, "name": map[string]string{"type": "string"}, "sys_class_name": map[string]string{"type": "string"},
					"updated_at": map[string]string{"type": "string", "format": "date-time"}, "collected_at": map[string]string{"type": "string", "format": "date-time"}}},
				"annotations": map[string]bool{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": true},
			}}}
		case "tools/call":
			var params struct {
				Name      string                     `json:"name"`
				Arguments map[string]json.RawMessage `json:"arguments"`
			}
			if !initialized || json.Unmarshal(request.Params, &params) != nil || params.Name != "read_cmdb_resource" || len(params.Arguments) != 0 {
				fail(-32602, "Use read_cmdb_resource with empty arguments")
				break
			}
			item, err := r.Read(ctx)
			result := map[string]any{"isError": err != nil}
			text := ""
			if err != nil {
				text = err.Error()
			} else {
				encoded, err := json.Marshal(item)
				if err != nil {
					return err
				}
				text = string(encoded)
				result["structuredContent"] = item
			}
			result["content"] = []any{map[string]string{"type": "text", "text": text}}
			response["result"] = result
		default:
			fail(-32601, "Method not found")
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	return scanner.Err()
}
