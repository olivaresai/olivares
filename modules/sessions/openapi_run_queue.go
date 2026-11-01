// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// sessionsRunQueueResponses is the answer of each route of the message queue
// (run_queue.go). The bodies are closed, like the marks': the handlers write these keys
// and nothing else.
func sessionsRunQueueResponses(method, pattern string) (map[string]any, bool) {
	resp := api.GenericModuleResponses()
	switch method + " " + pattern {
	case http.MethodGet + " /runs/{ref}/queue",
		http.MethodPut + " /runs/{ref}/queue",
		http.MethodDelete + " /runs/{ref}/queue/{id}":
		resp["200"] = oas.JSONRespSchema("The session's queue in sending order, and why its first message has not gone", sessionsRunQueueSchema())
	case http.MethodPost + " /runs/{ref}/queue":
		delete(resp, "200")
		resp["201"] = oas.JSONRespSchema("The message that was queued; it is sent at once when the session is not busy", sessionsQueuedMessageSchema())
	case http.MethodPatch + " /runs/{ref}/queue/{id}":
		resp["200"] = oas.JSONRespSchema("The message with its new text", sessionsQueuedMessageSchema())
	case http.MethodPost + " /runs/{ref}/queue/{id}/steer":
		delete(resp, "200")
		resp["202"] = oas.JSONRespSchema("The message was taken out of the queue and written to the session's tool; delivery is not confirmed by this response", sessionsRunInputAcceptedSchema())
		resp["409"] = oas.JSONResp("The session's tool takes no message into a running turn, or the session is not live")
	default:
		return nil, false
	}
	return resp, true
}

func sessionsQueuedMessageSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"id", oas.Obj("type", "string", "description", "The message's id in the queue."),
		"text", oas.Obj("type", "string", "description", "The message as the person wrote it."),
		"queued_by", oas.Obj("type", "string", "description", "The actor who queued it."),
		"queued_at", oas.Obj("type", "string", "format", "date-time", "description", "When it was queued."),
	), "id", "text", "queued_by", "queued_at")
}

func sessionsRunQueueSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"run_ref", oas.Obj("type", "string", "description", "The operated session's stable reference."),
		"items", oas.Obj("type", "array", "items", sessionsQueuedMessageSchema(), "description", "The queued messages, first to be sent first."),
		"hold", oas.Obj("type", "string", "enum", oas.Enum(queueHoldTurn, queueHoldApproval, queueHoldLimit, queueHoldStopped),
			"description", "Why the first message has not gone: turn (the tool is answering; it goes when the turn ends), approval (the session waits for an approval), limit (the account's limit ended the last turn) or stopped (the session stopped, was interrupted or is released). Absent when it goes at once."),
		"can_steer", oas.Obj("type", "boolean", "description", "Whether this session's tool takes a message into a running turn. Where false, the steer route refuses while a turn runs."),
	), "run_ref", "items", "can_steer")
}

// sessionsRunQueueBody is the request body of a queue route that has one.
func sessionsRunQueueBody(method, pattern string) (sessionsClosureRequestBodyDeclaration, bool) {
	switch method + " " + pattern {
	case http.MethodPost + " /runs/{ref}/queue", http.MethodPatch + " /runs/{ref}/queue/{id}":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj(
			"text", oas.Obj("type", "string", "minLength", 1, "maxLength", maxWorkTextInputBytes,
				"description", "The message, as it would be sent with the session's input. Not only whitespace."),
		), "text")), true
	case http.MethodPut + " /runs/{ref}/queue":
		return sessionsClosureBodyDeclaration(true, sessionsClosureClosedObject(oas.Obj(
			"ids", oas.Obj("type", "array", "maxItems", maxQueuedMessages, "items", oas.Obj("type", "string"),
				"description", "The ids of every queued message, once each, in the order they are to be sent."),
		), "ids")), true
	case http.MethodDelete + " /runs/{ref}/queue/{id}", http.MethodPost + " /runs/{ref}/queue/{id}/steer":
		// The path names the message: a body would be a second place to say it.
		return sessionsClosureRequestBodyDeclaration{kind: sessionsClosureBodyless}, true
	}
	return sessionsClosureRequestBodyDeclaration{}, false
}
