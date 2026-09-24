// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// matrixSchema names the only matrix format this binary reads.
const matrixSchema = "cedar-measure/matrix/v1"

// Operation names. A child performs exactly one of them inside the measured
// interval.
const (
	opParse     = "parse"
	opCompile   = "compile"
	opAuthorize = "authorize"
	opInspect   = "inspect"
	opWalk      = "walk"
)

// expected holds the hand-derived values a result must equal. Each operation
// checks only its own fields (see compare).
type expected struct {
	Steps         int      `json:"steps"`
	Depth         int      `json:"depth,omitempty"`
	TypeLen       int      `json:"type_len,omitempty"`
	Annotations   int      `json:"annotations,omitempty"`
	Nodes         int      `json:"nodes,omitempty"`
	Reasons       []string `json:"reasons,omitempty"`
	Errors        int      `json:"errors"`
	Discriminates bool     `json:"discriminates,omitempty"`
}

// child is one measured process of the matrix.
type child struct {
	Ordinal     int      `json:"ordinal"`
	ID          string   `json:"id"`
	Cell        string   `json:"cell"`
	Op          string   `json:"op"`
	Point       int      `json:"point"`
	InputBytes  int      `json:"input_bytes"`
	InputSHA256 string   `json:"input_sha256"`
	Expected    expected `json:"expected"`
}

// lowPoint is a small input whose exact text and results were written by hand.
type lowPoint struct {
	Cell     string   `json:"cell"`
	Point    int      `json:"point"`
	Text     string   `json:"text"`
	Expected expected `json:"expected"`
}

type matrix struct {
	Schema      string     `json:"schema"`
	RequestTime int64      `json:"request_time"`
	Children    []child    `json:"children"`
	LowPoints   []lowPoint `json:"low_points"`
}

func loadMatrix(path string) (*matrix, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m matrix
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Schema != matrixSchema {
		return nil, fmt.Errorf("matrix schema %q is not %q", m.Schema, matrixSchema)
	}
	return &m, nil
}

func (m *matrix) find(id string) (child, bool) {
	for _, c := range m.Children {
		if c.ID == id {
			return c, true
		}
	}
	return child{}, false
}
