// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// parseCard decodes Agent Card bytes into both the typed view (field extraction)
// and the generic map (JCS canonicalization for signature verification). The
// generic decode preserves number literals (UseNumber) so the canonical form can
// re-serialize them exactly as the signer did.
func parseCard(data []byte) (rawCard, error) {
	typed, err := decodeCardTyped(data)
	if err != nil {
		return rawCard{}, fmt.Errorf("a2a: decode agent card: %w", err)
	}
	generic, err := decodeGeneric(data)
	if err != nil {
		return rawCard{}, fmt.Errorf("a2a: decode agent card (generic): %w", err)
	}
	return rawCard{card: typed, raw: generic}, nil
}

// decodeGeneric decodes JSON into a generic value with number literals preserved
// (json.Number), so JCS can re-serialize numbers canonically. It requires a JSON
// object at the top level (an Agent Card is an object).
func decodeGeneric(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("a2a: agent card is not a JSON object")
	}
	return obj, nil
}

// cardURL resolves the URL to fetch an agent's card from: a spec URL that already
// points at a JSON document is used as-is; otherwise the well-known card path is
// appended to the agent's base URL (RFC 8615 discovery).
func cardURL(spec agentSpec, wellKnownPath string) string {
	u := strings.TrimSpace(spec.URL)
	if u == "" {
		return ""
	}
	if strings.HasSuffix(u, ".json") {
		return u
	}
	return strings.TrimRight(u, "/") + wellKnownPath
}
