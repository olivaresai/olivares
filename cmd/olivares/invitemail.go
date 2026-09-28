// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/emailtemplate"
	"github.com/olivaresai/olivares/core/webaddr"
	"github.com/olivaresai/olivares/sdk"
)

// inviteMailDestinationEnv names the notification destination invitations are
// mailed through. It must be a destination the operator's notification file
// (OLIVARES_NOTIFY_CONFIG) scopes to no tenant, with `"tenants": []`: the
// invitation channel belongs to the deployment, so no tenant can route to it,
// list it, test it or replay what it carried.
const inviteMailDestinationEnv = "OLIVARES_INVITE_MAIL_DESTINATION"

// errInviteNotRendered is what a send that could not build its message answers.
// It names no value: the link it would have carried holds the token.
var errInviteNotRendered = errors.New("invitations: the invitation message could not be rendered")

// inviteMailer mails an invitation to the invitee alone, through the
// deployment's own destination, with a link to the deployment's own console.
type inviteMailer struct {
	dispatch    *connectorDispatcher
	destination string
	// console is the address the operator declared for the console
	// (--public-url or OLIVARES_PUBLIC_URL). It is the only origin an invitation
	// link ever carries: no request header chooses it.
	console webaddr.Address
}

// SendInvite implements api.InviteSender. It renders the invitation template
// with the link to the console's accept page, the token in the link's fragment,
// which browsers never send in a request or a Referer header. The deployment's
// email destination sends text, so the template's text body is the message.
func (m inviteMailer) SendInvite(ctx context.Context, email, token string, expiresAt time.Time) error {
	if m.console.IsZero() || token == "" {
		return errInviteNotRendered
	}
	msg, err := emailtemplate.RenderInvite(emailtemplate.DefaultLocale, emailtemplate.Invite{
		AcceptURL: m.console.Origin + "/accept-invite#token=" + token, ExpiresAt: expiresAt,
	})
	if err != nil {
		return errInviteNotRendered
	}
	return m.dispatch.DeliverSystem(ctx, m.destination, email, sdk.Notification{
		Type: "auth.invitation", Title: msg.Subject, Body: msg.Text, Time: time.Now().UTC(),
	})
}

// newInviteSender returns the invitation mailer when the operator named a
// destination provisioned for the deployment alone and declared the console's
// address, and nil otherwise: invite mode then answers that delivery is
// unavailable and writes nothing. A named destination some tenant may address is
// refused with a warning, never used, and so is a destination with no declared
// console address to link to.
func newInviteSender(getenv func(string) string, dispatch *connectorDispatcher, console webaddr.Address, log *slog.Logger) api.InviteSender {
	name := strings.TrimSpace(getenv(inviteMailDestinationEnv))
	if name == "" || dispatch == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	if !dispatch.systemDestination(name) {
		log.Warn("invitations: the named destination is not scoped to no tenant (\"tenants\": []), so invitations are not mailed and invite mode is unavailable",
			"env", inviteMailDestinationEnv, "destination", name)
		return nil
	}
	if console.IsZero() {
		log.Warn("invitations: no console address is declared (--public-url or OLIVARES_PUBLIC_URL), so an invitation link cannot be built and invite mode is unavailable",
			"env", inviteMailDestinationEnv)
		return nil
	}
	return inviteMailer{dispatch: dispatch, destination: name, console: console}
}

// notifyDispatcherOf returns the module set's notification dispatcher, or nil.
func notifyDispatcherOf(set moduleSet) *connectorDispatcher {
	if set.deferredSecrets == nil {
		return nil
	}
	return set.deferredSecrets.notify
}
