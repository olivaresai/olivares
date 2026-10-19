// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
)

// Console notification destinations: the authoring surface that lets an administrator
// add, test and remove a Slack or Microsoft Teams destination from the console instead of
// editing the OLIVARES_NOTIFY_CONFIG file. It composes what the runtime already owns:
// the sealed secret store keeps the webhook URL (the definition keeps only a
// `store:<name>` reference), the deployment settings record keeps the definition, and
// the notify dispatcher opens it in the running engine without a restart.
//
// Like connector onboarding and the secret store it is SUPERADMIN-gated (a destination
// receives every alert routed to it) and its writes and test send require the AAL3
// step-up. The webhook URL is never returned. The interface is implemented in the
// composition root, the only place that may build connectors.

// errNotifyDestinationsUnavailable is returned when the surface is not wired.
// Mapped to 501, like the secret store and connector onboarding.
var errNotifyDestinationsUnavailable = errors.New("api: notify destinations unavailable")

// ErrNotifyDestinationInvalid is a destination the console may not save: an unknown
// kind, a bad name, a URL that is not an https webhook of that vendor, a name the
// operator file already owns. The wrapped message is safe to show. Mapped to 400.
var ErrNotifyDestinationInvalid = errors.New("invalid notification destination")

// ErrNotifyDestinationNotFound names no destination the console owns. Mapped to 404.
var ErrNotifyDestinationNotFound = errors.New("notification destination not found")

// ErrNotifyDestinationTestFailed is the safe result of a failed test send: the
// destination did not accept the message. A connector error can embed the URL, so the
// detail is logged at debug and never returned. Mapped to 422.
var ErrNotifyDestinationTestFailed = errors.New("the destination did not accept the test message")

// NotifyDestinationSourceConsole and NotifyDestinationSourceFile say who owns a
// destination: the console edits the first; the second is provisioned in the operator
// file and is listed read-only.
const (
	NotifyDestinationSourceConsole = "console"
	NotifyDestinationSourceFile    = "file"
)

// NotifyDestinations is the console destination surface.
type NotifyDestinations interface {
	// List returns every destination (console- and file-provisioned) and the kinds
	// the console can create. It never returns a webhook URL.
	List(ctx context.Context) (NotifyDestinationList, error)
	// Test sends one real test message through the candidate without saving or
	// wiring it. A blank WebhookURL tests the stored value of that name.
	Test(ctx context.Context, actor auth.Principal, in NotifyDestinationInput) error
	// Put seals a typed webhook URL, stores the definition and opens the destination
	// in the running engine. A blank WebhookURL keeps the stored value.
	Put(ctx context.Context, actor auth.Principal, in NotifyDestinationInput) (NotifyDestination, error)
	// Delete removes a console destination, closes it and deletes its sealed URL.
	Delete(ctx context.Context, actor auth.Principal, name string) error
}

// NotifyDestinationInput is the create/update/test payload.
type NotifyDestinationInput struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	WebhookURL string `json:"webhook_url,omitempty"`
}

// NotifyDestination is the read shape: who owns it, whether it is open in the running
// engine, and the non-secret host its webhook points at. Never the URL.
type NotifyDestination struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Live        bool   `json:"live"`
	WebhookHost string `json:"webhook_host,omitempty"`
}

// NotifyDestinationKind is one kind the console can create.
type NotifyDestinationKind struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// NotifyDestinationList is the list response. SealerAvailable lets the console say
// that saving is disabled instead of failing each write.
type NotifyDestinationList struct {
	Destinations    []NotifyDestination     `json:"destinations"`
	Kinds           []NotifyDestinationKind `json:"kinds"`
	SealerAvailable bool                    `json:"sealer_available"`
}
