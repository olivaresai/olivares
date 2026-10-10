// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"bytes"
	"encoding/json"
)

// UnmarshalJSON accepts the Messages API's text shorthand and presents it to
// governance as the equivalent text block. Existing blocks retain their raw
// fields; the public Go type and the marshaled request shape stay unchanged.
func (m *Message) UnmarshalJSON(data []byte) error {
	type message Message
	wire := struct {
		*message
		Content json.RawMessage `json:"content"`
	}{message: (*message)(m)}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	content := bytes.TrimSpace(wire.Content)
	if len(content) == 0 {
		return nil
	}
	if content[0] == '"' {
		var text string
		if err := json.Unmarshal(content, &text); err != nil {
			return err
		}
		m.Content = []ContentBlock{TextBlock(text)}
		return nil
	}
	return json.Unmarshal(content, &m.Content)
}
