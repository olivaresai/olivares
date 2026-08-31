// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
)

// withoutTasksCapability projects an initialize/server-discover result onto
// the capabilities this gateway can actually honor when no durable task store
// is wired. It never fabricates a capability and leaves the original bytes
// untouched when Tasks was not advertised.
func withoutTasksCapability(raw json.RawMessage) (json.RawMessage, error) {
	if _, err := decodeStrictJSON(raw); err != nil {
		return nil, fmt.Errorf("strict capability result: %w", err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, fmt.Errorf("capability result must be an object")
	}
	rawCapabilities, ok := result["capabilities"]
	if !ok {
		return raw, nil
	}
	var capabilities map[string]json.RawMessage
	if err := json.Unmarshal(rawCapabilities, &capabilities); err != nil || capabilities == nil {
		return nil, fmt.Errorf("capabilities must be an object")
	}
	changed := false
	if _, exists := capabilities["tasks"]; exists {
		delete(capabilities, "tasks")
		changed = true
	}
	if rawExtensions, exists := capabilities["extensions"]; exists {
		var extensions map[string]json.RawMessage
		if err := json.Unmarshal(rawExtensions, &extensions); err != nil || extensions == nil {
			return nil, fmt.Errorf("capabilities.extensions must be an object")
		}
		if _, advertised := extensions[extensionTasks]; advertised {
			delete(extensions, extensionTasks)
			changed = true
			encoded, err := json.Marshal(extensions)
			if err != nil {
				return nil, err
			}
			capabilities["extensions"] = encoded
		}
	}
	if !changed {
		return raw, nil
	}
	encodedCapabilities, err := json.Marshal(capabilities)
	if err != nil {
		return nil, err
	}
	result["capabilities"] = encodedCapabilities
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}
