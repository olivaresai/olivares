// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// lifecycle.go adds the alert-lifecycle actions (close, acknowledge) to the
// Opsgenie output connector — the half that lets a governed close-loop advance an
// EXISTING alert instead of only ever opening one. The create path
// (POST /v2/alerts) is unchanged; these actions POST to the per-alert
// sub-resources the Alerts API exposes:
//
//	close:        POST /v2/alerts/{identifier}/close
//	acknowledge:  POST /v2/alerts/{identifier}/acknowledge
//
// (VERIFIED primary source, jun-2026, docs.opsgenie.com/docs/alert-api.) The
// {identifier} is qualified by the identifierType query parameter — default "id";
// "alias" to act on the de-dup alias. The request body fields (user/source/note)
// are all optional. Authentication is the same GenieKey header the create path
// uses, and the US/EU host comes from the resolved alerts URL — so no new secret
// and no new region handling. Like create, the body carries only non-sensitive
// Notification fields (minimal data, docs/SECURITY-HARDENING.md) and the api key never reaches a
// log or an error (the shared delivery transport redacts).
//
// Closing is processed ASYNCHRONOUSLY (HTTP 202 Accepted): the 202 means the
// request was accepted, not that the alert is closed. The connector reports the
// transport outcome (2xx vs terminal 4xx) honestly and does not claim closure on
// the 202 alone.

package opsgenie

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
)

// Lifecycle field keys and action values. The action rides the non-secret
// Notification.Fields, mirroring PagerDuty's Fields["event_action"].
const (
	// fieldAction selects the lifecycle action (default create).
	fieldAction = "action"
	// fieldAlias / fieldAlertID name the alert a close/acknowledge acts on.
	fieldAlias   = "alias"
	fieldAlertID = "alert_id"
	// fieldUser optionally records the request owner (≤100 chars; non-secret).
	fieldUser = "user"

	actionCreate      = "create"
	actionClose       = "close"
	actionAcknowledge = "acknowledge"

	// identifierType query values (Opsgenie default is "id").
	idTypeID    = "id"
	idTypeAlias = "alias"

	// maxNoteLen is the Opsgenie hard limit for the lifecycle "note" field.
	maxNoteLen = 25000
	// maxUserLen is the Opsgenie hard limit for the "user"/"source" fields.
	maxUserLen = 100
)

// alertAction resolves the lifecycle action from Fields["action"], defaulting to
// create. An unrecognized value also defaults to create (a notification is, by
// nature, an alert to open) so a typo never silently advances the wrong alert.
func alertAction(n sdk.Notification) string {
	switch strings.ToLower(strings.TrimSpace(n.Fields[fieldAction])) {
	case actionClose:
		return actionClose
	case actionAcknowledge:
		return actionAcknowledge
	default:
		return actionCreate
	}
}

// lifecycleBody is the JSON body of a close/acknowledge request. All fields are
// optional per the API; source identifies the emitting system, note carries the
// non-sensitive reason. An empty body is valid, but source is always set.
type lifecycleBody struct {
	User   string `json:"user,omitempty"`
	Source string `json:"source"`
	Note   string `json:"note,omitempty"`
}

// notifyLifecycle advances an existing alert (close or acknowledge). The alert is
// named by Fields["alert_id"] (identifierType=id) or Fields["alias"]
// (identifierType=alias); WITHOUT either, the action is a terminal configuration
// error — acting on an unspecified alert is never guessed (deny-closed). The
// GenieKey header authenticates the request and never leaks into an error.
func (o *Output) notifyLifecycle(ctx context.Context, n sdk.Notification, action string) error {
	identifier, idType, err := alertIdentifier(n)
	if err != nil {
		return err
	}
	body, err := json.Marshal(lifecycleBody{
		User:   truncate(strings.TrimSpace(n.Fields[fieldUser]), maxUserLen),
		Source: alertSource,
		Note:   truncate(firstNonEmpty(n.Body, n.Title), maxNoteLen),
	})
	if err != nil {
		return fmt.Errorf("opsgenie: marshal %s: %w", action, err)
	}
	res, err := o.deliver.Send(ctx, delivery.Request{
		URL: o.lifecycleURL(identifier, idType, action),
		Header: map[string]string{
			"Authorization": "GenieKey " + o.apiKey,
			"Content-Type":  "application/json",
		},
		Body: body,
	})
	if err != nil {
		// The delivery error carries a status code and a bounded body excerpt,
		// never the credential; wrap it with the action for context.
		return fmt.Errorf("opsgenie: %s alert (status %d): %w", action, res.StatusCode, err)
	}
	return nil
}

// alertIdentifier resolves which alert a lifecycle action targets and the
// identifierType used to qualify it. It prefers an explicit Opsgenie alert id
// (Fields["alert_id"], identifierType=id) over the de-dup alias (Fields["alias"],
// identifierType=alias). A lifecycle action with NEITHER is a terminal error: the
// caller must thread the alert's id or alias — Opsgenie's default identifierType
// is id, so an alias MUST be qualified, and acting on an unspecified alert is
// never guessed.
func alertIdentifier(n sdk.Notification) (identifier, idType string, err error) {
	if id := strings.TrimSpace(n.Fields[fieldAlertID]); id != "" {
		return id, idTypeID, nil
	}
	if alias := strings.TrimSpace(n.Fields[fieldAlias]); alias != "" {
		return alias, idTypeAlias, nil
	}
	return "", "", fmt.Errorf("opsgenie: a %q/%q lifecycle action requires an alert id (Fields[%q]) or alias (Fields[%q]) to act on",
		actionClose, actionAcknowledge, fieldAlertID, fieldAlias)
}

// lifecycleURL builds the per-alert endpoint from the resolved alerts base URL:
// {base}/{identifier}/{action}?identifierType={idType}. The base is the same
// region-resolved (or operator-overridden) /v2/alerts URL the create path uses,
// so US/EU selection is inherited. The identifier is path-escaped and the
// identifierType is the query parameter that tells Opsgenie how to resolve the
// path segment.
func (o *Output) lifecycleURL(identifier, idType, action string) string {
	base := strings.TrimRight(o.alertsURL, "/")
	return base + "/" + url.PathEscape(identifier) + "/" + action + "?identifierType=" + url.QueryEscape(idType)
}
