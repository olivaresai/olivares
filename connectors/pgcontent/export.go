// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgcontent

import (
	"fmt"
	"strconv"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
)

// pgExport is the static row snapshot the export mode parses — a dump an operator
// produces out-of-band (never presented as live Mode() reports "export"). The
// same declarative config maps each row to a Document, so export and live modes share
// identical governance semantics.
type pgExport struct {
	Schema string           `json:"schema,omitempty"`
	Table  string           `json:"table,omitempty"`
	Rows   []map[string]any `json:"rows"`
}

// parseExport reads the configured export file(s) and maps each row to a Document
// using the declarative config. It is read-only and bounded (content.ReadJSON caps
// the file size).
func (sc *sourceConfig) parseExport() ([]contentsource.Document, error) {
	files, err := content.ExportFiles(sc.exportPath, ".json")
	if err != nil {
		return nil, err
	}
	var out []contentsource.Document
	for _, f := range files {
		var exp pgExport
		if err := content.ReadJSON(f, &exp); err != nil {
			return nil, err
		}
		for _, raw := range exp.Rows {
			r := stringifyRow(raw)
			// A row missing every key column would collapse to the same empty DocID;
			// skip it rather than emit an ambiguous document.
			if !hasAnyKey(sc, r) {
				continue
			}
			out = append(out, sc.toDocument(r))
		}
	}
	return out, nil
}

// hasAnyKey reports whether the row carries at least one non-empty key column value.
func hasAnyKey(sc *sourceConfig, r row) bool {
	for _, c := range sc.keyColumns {
		if r[c] != "" {
			return true
		}
	}
	return false
}

// stringifyRow converts a decoded JSON row (mixed types) into the string-valued row
// the document mapping consumes, so export and live rows are shaped identically.
func stringifyRow(raw map[string]any) row {
	r := make(row, len(raw))
	for k, v := range raw {
		r[k] = stringifyValue(v)
	}
	return r
}

// stringifyValue renders one column value as a string. null → "".
func stringifyValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		// JSON numbers decode to float64; render integers without a trailing ".0".
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}
