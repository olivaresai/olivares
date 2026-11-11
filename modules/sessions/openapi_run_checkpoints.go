// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// sessionsRunCheckpointsResponses is the answer of each route of the turn checkpoints
// (run_checkpoints.go). The bodies are closed: the handlers write these keys and nothing
// else.
func sessionsRunCheckpointsResponses(method, pattern string) (map[string]any, bool) {
	resp := api.GenericModuleResponses()
	switch method + " " + pattern {
	case http.MethodGet + " /runs/{ref}/checkpoints":
		resp["200"] = oas.JSONRespSchema("The session's turns, oldest first, each with the files it changed", sessionsRunCheckpointsSchema())
	case http.MethodPost + " /runs/{ref}/checkpoints/{seq}/rewind":
		resp["200"] = oas.JSONRespSchema("The files that were put back or removed", sessionsRunRewindSchema())
		resp["409"] = oas.JSONResp("The session is working on a turn; interrupt it or wait for it to end")
		resp["422"] = oas.JSONResp("Too many files differ to rewind here")
	default:
		return nil, false
	}
	return resp, true
}

func sessionsCheckpointFileSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"path", oas.Obj("type", "string", "description", "The file's path, relative to the session's folder."),
		"status", oas.Obj("type", "string", "enum", oas.Enum("added", "modified", "deleted"), "description", "What the turn did to the file."),
		"added", oas.Obj("type", "integer", "minimum", 0, "description", "Lines added between the two trees; absent for a binary file."),
		"removed", oas.Obj("type", "integer", "minimum", 0, "description", "Lines removed between the two trees; absent for a binary file."),
	), "path", "status")
}

func sessionsRunCheckpointsSchema() map[string]any {
	turn := sessionsClosureClosedObject(oas.Obj(
		"seq", oas.Obj("type", "integer", "minimum", 1, "description", "The turn's number in this session, from 1."),
		"prompt", oas.Obj("type", "string", "description", "The message that started the turn."),
		"clipped", oas.Obj("type", "boolean", "description", "True when prompt is only the start of a longer message."),
		"at", oas.Obj("type", "string", "format", "date-time", "description", "When the turn started."),
		"open", oas.Obj("type", "boolean", "description", "True for the newest turn until the next one starts: its files are the folder's changes so far."),
		"files", oas.Obj("type", "array", "items", sessionsCheckpointFileSchema(), "description", "The files the turn changed."),
		"truncated", oas.Obj("type", "boolean", "description", "True when the turn changed more files than are listed."),
	), "seq", "prompt", "clipped", "at", "open", "files", "truncated")
	return sessionsClosureClosedObject(oas.Obj(
		"available", oas.Obj("type", "boolean", "description", "False when this session's folder cannot be checkpointed; reason says why."),
		"reason", oas.Obj("type", "string", "description", "Why the folder cannot be checkpointed. Absent when it can."),
		"turns", oas.Obj("type", "array", "items", turn, "description", "The session's turns, oldest first."),
		"files", oas.Obj("type", "array", "items", sessionsCheckpointFileSchema(), "description", "What the session's folder differs by now from the start of the first turn: the whole session's changes. Empty when there are no turns or the folder cannot be checkpointed."),
		"truncated", oas.Obj("type", "boolean", "description", "True when the session changed more files than are listed."),
	), "available", "turns", "files", "truncated")
}

func sessionsRunRewindSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"seq", oas.Obj("type", "integer", "description", "The turn the folder was rewound to."),
		"to", oas.Obj("type", "string", "enum", oas.Enum("start", "end"), "description", "Whether the folder was put back as before the turn or as when it ended."),
		"files", oas.Obj("type", "array", "description", "Every file that was put back or removed.", "items", sessionsClosureClosedObject(oas.Obj(
			"path", oas.Obj("type", "string", "description", "The file's path, relative to the session's folder."),
			"action", oas.Obj("type", "string", "enum", oas.Enum("restored", "removed"), "description", "restored: the file is back as it was. removed: it did not exist then."),
		), "path", "action")),
	), "seq", "to", "files")
}

// sessionsRunCheckpointsBody is the request body of a checkpoint route that has one.
func sessionsRunCheckpointsBody(method, pattern string) (sessionsClosureRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
	case http.MethodPost + " /runs/{ref}/checkpoints/{seq}/rewind":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj(
			"to", oas.Obj("type", "string", "enum", oas.Enum("start", "end"),
				"description", "start puts the folder back as it was before the turn; end as it was when the turn ended, which undoes a rewind made after it."),
		), "to")), true
	}
	return sessionsClosureRequestBodyDeclaration{}, false
}
