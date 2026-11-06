// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"net/http"

	"github.com/olivaresai/olivares/core/runtime/hostmetrics"
)

// HostMetricsService reads the host the engine runs on. A nil service, or one that
// returns hostmetrics.ErrUnavailable, answers 501.
type HostMetricsService interface {
	HostStatus(context.Context) (hostmetrics.Status, error)
}

// hostStatusSchema is the OpenAPI schema of hostmetrics.Status.
func hostStatusSchema(obj func(...any) map[string]any, arr func(...any) []any) map[string]any {
	bytes := func(desc string) map[string]any {
		return obj("type", "integer", "format", "int64", "minimum", 0, "description", desc)
	}
	text := obj("type", "string")
	pct := obj("type", "number", "minimum", 0, "maximum", 100)
	when := obj("type", "string", "format", "date-time")
	list := func(item map[string]any) map[string]any { return obj("type", "array", "items", item) }
	return obj("type", "object",
		"required", arr("collected_at", "load", "cpu_percent", "memory", "swap", "filesystems", "session_folders", "history"),
		"properties", obj(
			"collected_at", when,
			"load", obj("type", "object", "required", arr("one", "five", "fifteen"), "properties", obj(
				"one", obj("type", "number"), "five", obj("type", "number"), "fifteen", obj("type", "number"))),
			"cpu_percent", obj("type", "number", "minimum", 0, "maximum", 100, "description", "Average CPU use since the last one-minute sample."),
			"memory", obj("type", "object", "required", arr("total_bytes", "used_bytes", "available_bytes"), "properties", obj(
				"total_bytes", bytes("Physical memory."), "used_bytes", bytes("Total minus available."), "available_bytes", bytes("Memory available without swapping."))),
			"swap", obj("type", "object", "required", arr("total_bytes", "used_bytes"), "properties", obj(
				"total_bytes", bytes("Zero on a host without swap."), "used_bytes", bytes("Swap in use."))),
			"filesystems", list(obj("type", "object", "required", arr("mount", "device", "type", "total_bytes", "used_bytes"), "properties", obj(
				"mount", text, "device", text, "type", text, "total_bytes", bytes("Size of the filesystem."), "used_bytes", bytes("Bytes in use.")))),
			"session_folders", obj("type", "object", "required", arr("root", "measured_at", "total_bytes", "folders", "truncated"), "properties", obj(
				"root", text, "measured_at", when, "total_bytes", bytes("Allocated bytes of every session folder."),
				"folders", list(obj("type", "object", "required", arr("name", "bytes"), "properties", obj(
					"name", obj("type", "string", "description", "The session folder's name under root."), "bytes", bytes("Allocated bytes of the folder.")))),
				"truncated", obj("type", "boolean", "description", "True when the measurement stopped at its entry or time bound, so the sizes are a floor."))),
			"history", obj("type", "array", "description", "The last 24 hours at one sample a minute, oldest first.", "items",
				obj("type", "object", "required", arr("at", "cpu_percent", "load_1", "memory_used_bytes", "swap_used_bytes"), "properties", obj(
					"at", when, "cpu_percent", pct, "load_1", obj("type", "number"),
					"memory_used_bytes", bytes("Memory in use."), "swap_used_bytes", bytes("Swap in use."))))))
}

// handleHostMetrics returns the load, CPU, memory, swap and filesystem use of the
// host, the disk use of each session folder and the last 24 hours of samples, for a
// system administrator.
func (s *Server) handleHostMetrics(w http.ResponseWriter, r *http.Request, _ ModuleContext) {
	if s.hostMetrics == nil {
		s.writeError(w, r, hostmetrics.ErrUnavailable)
		return
	}
	st, err := s.hostMetrics.HostStatus(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, st)
}
