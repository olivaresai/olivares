// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package openlineage

import (
	"encoding/json"
	"strings"
	"time"
)

// runEvent is the subset of an OpenLineage RunEvent the connector reads
// (https://openlineage.io/docs/spec/object-model). Only the fields needed to
// build the access edge are declared: the run's facets, schema/column-level
// lineage, SQL and any job payload are deliberately NOT parsed — the connector
// emits the edge, never the body (docs/SECURITY-HARDENING.md). run.runId is also not read: the
// edge is keyed by (origin, resource, mode, source, eventTime), not by run.
type runEvent struct {
	EventType string    `json:"eventType"` // START | RUNNING | COMPLETE | FAIL | ABORT | OTHER
	EventTime string    `json:"eventTime"` // ISO-8601 date-time
	Job       job       `json:"job"`
	Inputs    []dataset `json:"inputs"`
	Outputs   []dataset `json:"outputs"`
}

// job identifies the pipeline job that ran (the non-human identity the edge is
// attributed to). namespace + name is the job's OpenLineage natural key.
type job struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// dataset identifies an input or output dataset of the run. namespace + name is
// the dataset's OpenLineage natural key.
type dataset struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// parseEvent parses one newline-delimited RunEvent JSON object. It returns
// ok=false for a line that is not valid JSON.
func parseEvent(line []byte) (runEvent, bool) {
	var ev runEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return runEvent{}, false
	}
	return ev, true
}

// isComplete reports whether an event is a terminal COMPLETE that should be
// emitted. To avoid double-counting the START and COMPLETE of one run, only
// COMPLETE is emitted; an empty eventType is also accepted (an event shipped
// without the field is treated as the completed/sole event for its run). Every
// other state (START, RUNNING, FAIL, ABORT, OTHER) is skipped.
func isComplete(eventType string) bool {
	switch strings.ToUpper(strings.TrimSpace(eventType)) {
	case "COMPLETE", "":
		return true
	default:
		return false
	}
}

// jobRef is the job's OpenLineage natural key, "namespace/name". It returns ""
// when neither component is present (no usable origin).
func jobRef(j job) string {
	return joinRef(j.Namespace, j.Name)
}

// datasetRef is the dataset's OpenLineage natural key, "namespace/name". It
// returns "" when neither component is present (no usable resource).
func datasetRef(d dataset) string {
	return joinRef(d.Namespace, d.Name)
}

// joinRef joins an OpenLineage namespace and name into "namespace/name",
// tolerating a missing component (returns the other alone, or "" if both empty).
func joinRef(namespace, name string) string {
	ns := strings.TrimSpace(namespace)
	n := strings.TrimSpace(name)
	switch {
	case ns == "" && n == "":
		return ""
	case ns == "":
		return n
	case n == "":
		return ns
	default:
		return ns + "/" + n
	}
}

// olTimeLayouts are the timestamp formats OpenLineage emits in eventTime: an
// ISO-8601 date-time, in practice RFC3339 with a 'Z' (e.g. "2020-12-09T23:37:31.081Z")
// or a numeric offset (e.g. "2025-10-24T15:08:00.001+10:00"). RFC3339[Nano]
// covers both; the offset forms are normalized to UTC.
var olTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
}

// parseTime parses an OpenLineage eventTime and normalizes it to UTC, returning
// ok=false if no layout matches. ObservedAt always comes from the event's own
// clock (the dedup natural key), never time.Now().
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range olTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
