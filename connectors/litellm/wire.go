// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package litellm

import (
	"bytes"
	"encoding/json"
)

// The wire types are a TOLERANT, DOCUMENTED view of LiteLLM's management responses
// (see doc.go for the verified field list). Every field is optional.

type litellmExport struct {
	Keys  []litellmKey  `json:"keys"`
	Teams []litellmTeam `json:"teams"`
	Users []litellmUser `json:"users"`
}

type litellmKey struct {
	KeyAlias       string   `json:"key_alias"`
	Token          string   `json:"token"`
	Spend          float64  `json:"spend"`
	MaxBudget      *float64 `json:"max_budget"`
	BudgetDuration string   `json:"budget_duration"`
	Models         []string `json:"models"`
	TeamID         string   `json:"team_id"`
	UserID         string   `json:"user_id"`
	Blocked        bool     `json:"blocked"`
}

type litellmTeam struct {
	TeamID    string   `json:"team_id"`
	TeamAlias string   `json:"team_alias"`
	Spend     float64  `json:"spend"`
	MaxBudget *float64 `json:"max_budget"`
	Models    []string `json:"models"`
}

type litellmUser struct {
	UserID    string   `json:"user_id"`
	Spend     float64  `json:"spend"`
	MaxBudget *float64 `json:"max_budget"`
	Models    []string `json:"models"`
}

// decodeExport parses a LiteLLM export blob into keys/teams/users. It accepts a
// combined object, a bare array of key objects, or JSON-lines (one key per line).
// Decoding is ELEMENT-WISE tolerant: a single malformed/type-mismatched entry (e.g.
// one key with "max_budget":"unlimited") skips only that entry, never the whole file
// — a batch decode over the entire blob would fail-open to zero data indistinguishable
// from "offline". A malformed blob yields an empty export (the caller skips the file).
func decodeExport(data []byte) litellmExport {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return litellmExport{}
	}
	switch trimmed[0] {
	case '{':
		// A combined object: decode the arrays into raw elements so each is tolerated
		// independently. Falls through to a single-object / JSON-lines read otherwise.
		var raw struct {
			Keys  []json.RawMessage `json:"keys"`
			Teams []json.RawMessage `json:"teams"`
			Users []json.RawMessage `json:"users"`
		}
		if json.Unmarshal(trimmed, &raw) == nil && (len(raw.Keys) > 0 || len(raw.Teams) > 0 || len(raw.Users) > 0) {
			return litellmExport{
				Keys:  decodeKeys(raw.Keys),
				Teams: decodeTeams(raw.Teams),
				Users: decodeUsers(raw.Users),
			}
		}
		return litellmExport{Keys: decodeKeyStream(trimmed)}
	case '[':
		var raws []json.RawMessage
		if json.Unmarshal(trimmed, &raws) == nil {
			return litellmExport{Keys: decodeKeys(raws)}
		}
	}
	return litellmExport{}
}

func decodeKeys(raws []json.RawMessage) []litellmKey {
	var out []litellmKey
	for _, r := range raws {
		var k litellmKey
		if json.Unmarshal(r, &k) == nil {
			out = append(out, k)
		}
	}
	return out
}

func decodeTeams(raws []json.RawMessage) []litellmTeam {
	var out []litellmTeam
	for _, r := range raws {
		var tm litellmTeam
		if json.Unmarshal(r, &tm) == nil {
			out = append(out, tm)
		}
	}
	return out
}

func decodeUsers(raws []json.RawMessage) []litellmUser {
	var out []litellmUser
	for _, r := range raws {
		var u litellmUser
		if json.Unmarshal(r, &u) == nil {
			out = append(out, u)
		}
	}
	return out
}

// decodeKeyStream reads one-key-object-per-line (JSON-lines), or a single object.
func decodeKeyStream(data []byte) []litellmKey {
	var out []litellmKey
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var k litellmKey
		if json.Unmarshal(line, &k) == nil && (k.KeyAlias != "" || k.Token != "" || k.UserID != "" || k.TeamID != "") {
			out = append(out, k)
		}
	}
	return out
}
