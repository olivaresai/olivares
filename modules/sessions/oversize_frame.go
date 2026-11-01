// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// A tool writes one record per line, and some records are big: a tool_result that
// holds a whole file or a screenshot, a long command output. The pump does not carry
// a line over its limit (maxOutputLine): the ring, the recorder and every attach
// client keep frames, so a giant one would be kept many times. It does not end the
// session either. The line is replaced by one compact frame, and the next line is
// read as usual.
//
// The compact frame keeps what the line says it is (the small top-level values the
// tool wrote before its payload: Claude's type, subtype and session_id, JSON-RPC's
// jsonrpc, id and method) and adds olivares_oversize with the line's size in bytes
// and its SHA-256, so the record can be matched in the tool's own transcript. A
// JSON-RPC answer (an id and no method) becomes an error answer, so the request that
// waits for it fails plainly instead of reading an empty result as success.

// oversizeMarker names the field the compact frame adds; a handoff that must read
// the tool's whole output refuses a frame that holds it (session_handoff_tools.go).
const oversizeMarker = `"olivares_oversize":{`

const (
	oversizeKeptValue = 1 << 10 // the longest top-level value kept
	oversizeKeptTotal = 4 << 10 // the most the kept values may add up to
)

// oversizeFrame is the record that takes the place of an output line over the limit.
// prefix is the start of the line, size and sum describe the whole line.
func oversizeFrame(prefix []byte, size int, sum []byte) []byte {
	var kept bytes.Buffer
	var hasID, hasMethod, hasType bool
	prefix = prefix[:min(len(prefix), overflowHead)]
	d := json.NewDecoder(bytes.NewReader(prefix))
	if tok, err := d.Token(); err == nil && tok == json.Delim('{') {
		for d.More() {
			tok, err := d.Token()
			if err != nil {
				break
			}
			key, _ := tok.(string)
			var value json.RawMessage
			if d.Decode(&value) != nil || d.InputOffset() == int64(len(prefix)) {
				break // the payload runs past the prefix: a number cut there would read as another
			}
			if len(value) == 0 || value[0] == '{' || value[0] == '[' || len(value) > oversizeKeptValue || key == "olivares_oversize" {
				continue
			}
			quoted, _ := json.Marshal(key)
			if kept.Len()+len(quoted)+len(value)+2 > oversizeKeptTotal {
				break
			}
			kept.Write(quoted)
			kept.WriteByte(':')
			kept.Write(value)
			kept.WriteByte(',')
			hasID = hasID || key == "id"
			hasMethod = hasMethod || key == "method"
			hasType = hasType || key == "type"
		}
	}
	out := append([]byte{'{'}, kept.Bytes()...)
	if hasID && !hasMethod && !hasType {
		out = append(out, `"error":{"code":-32603,"message":"the answer was over the session output line limit and was left out"},`...)
	}
	out = append(out, oversizeMarker+`"bytes":`...)
	out = strconv.AppendInt(out, int64(size), 10)
	out = append(out, `,"sha256":"`...)
	out = hex.AppendEncode(out, sum)
	return append(out, `"}}`...)
}
