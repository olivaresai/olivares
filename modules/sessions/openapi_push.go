// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/api/oas"
)

// The published contract of the caller's own Web Push devices (push.go). Closed objects,
// because the handlers write exactly these fields: a console builds the subscription body
// from a browser's PushSubscription, and the id it gets back is the one the delete takes.

func sessionsPushRoute(method, pattern string) bool {
	switch method + " " + pattern {
	case http.MethodGet + " /push/key", http.MethodPost + " /push/subscriptions", http.MethodDelete + " /push/subscriptions/{id}":
		return true
	}
	return false
}

// sessionsPushSubscribeSchema is the body of POST /push/subscriptions: the part of a
// browser's PushSubscription that PushSubscription.toJSON() calls endpoint and keys.
func sessionsPushSubscribeSchema() map[string]any {
	return sessionsClosureClosedObject(oas.Obj(
		"endpoint", oas.Obj("type", "string", "minLength", 1, "maxLength", 2048,
			"description", "The address of the browser's push service for this device. Only https on port 443 and only a name, never an address: a push service is not an internal host."),
		"keys", sessionsClosureClosedObject(oas.Obj(
			"p256dh", oas.Obj("type", "string", "description", "The browser's P-256 public key, 65 bytes uncompressed, base64url."),
			"auth", oas.Obj("type", "string", "description", "The browser's 16-byte authentication secret, base64url."),
		), "p256dh", "auth"),
	), "endpoint", "keys")
}

func sessionsPushResponses(method, pattern string) (map[string]any, bool) {
	if !sessionsPushRoute(method, pattern) {
		return nil, false
	}
	resp := api.GenericModuleResponses()
	switch method + " " + pattern {
	case http.MethodGet + " /push/key":
		resp["200"] = oas.JSONRespSchema("The application server key a browser subscribes with (PushManager.subscribe applicationServerKey), base64url.",
			sessionsClosureClosedObject(oas.Obj("public_key", oas.Obj("type", "string")), "public_key"))
		resp["503"] = oas.JSONResp("this server sends no Web Push")
	case http.MethodPost + " /push/subscriptions":
		resp["200"] = oas.JSONRespSchema("The device is registered, or was already: the same browser registering again updates its keys. id is the hex SHA-256 of the endpoint, the id the delete takes. At most 10 devices per person; the oldest leaves for an eleventh.",
			sessionsClosureClosedObject(oas.Obj("id", oas.Obj("type", "string", "pattern", "^[0-9a-f]{64}$")), "id"))
		resp["400"] = oas.JSONResp("the endpoint is not one this server will push to, or a key is not what a browser gives")
		resp["403"] = oas.JSONResp("the caller is not a signed-in person, or lacks sessions:run:read")
		resp["503"] = oas.JSONResp("this server sends no Web Push")
	default:
		resp["204"] = oas.Obj("description", "the device is not registered any more: it was removed, never registered, or belongs to someone else (idempotent)")
	}
	return resp, true
}
