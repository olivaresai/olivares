// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package twilio is the Olivares AI output connector that sends an out-of-band SMS via
// Twilio's Programmable Messaging API — a last-resort severe-incident channel for when
// the on-call's chat/email is itself degraded. It implements sdk.OutputConnector over
// the shared reliable-delivery transport (internal/delivery).
//
// SCOPE (nice-to-have): SMS OOB is LARGELY SUBSUMED by
// PagerDuty (which already does SMS/voice escalation), so this connector is a
// nice-to-have and a PROBABLE POST-v1 destination — built behind the interface so it
// is ready, but NOT a v1-blocking dependency. The v1-vs-post-v1 cut is the release
// session's call, not this connector's.
//
// Minimal-data / credential handling (docs/SECURITY-HARDENING.md-3): only the displayable Notification
// text reaches the wire. The Auth Token is the operator credential — declared Secret,
// held in memory only, sent as the HTTP Basic password, and NEVER logged (the delivery
// transport never logs headers). The Account SID appears in the request path (it is an
// account identifier, not a secret like the token). It imports only the SDK and the
// Apache delivery transport, never the engine.
package twilio

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.twilio"

// Default configuration values.
const (
	defaultMaxAttempts = 4
	defaultAPIBase     = "https://api.twilio.com"
	maxBodyLen         = 1600 // Twilio caps a message body at 1600 chars.
)

// Config field keys.
const (
	cfgAccountSID = "account_sid"
	cfgAuthToken  = "auth_token"
	cfgFrom       = "from"
	cfgMsgService = "messaging_service_sid"
	cfgTo         = "to"
	cfgAPIBase    = "api_base"
	cfgMaxAtt     = "max_attempts"

	fieldTo = "to"
)

// Output is the Twilio SMS output connector.
type Output struct {
	accountSID string
	authHeader string // pre-built "Basic ..."; in memory only, never logged
	from       string
	msgService string
	defaultTo  string
	apiBase    string
	maxAtt     int

	client *delivery.Client
	doer   delivery.Doer
}

var _ sdk.OutputConnector = (*Output)(nil)

// New returns a Twilio output connector with default configuration.
func New() *Output {
	return &Output{apiBase: defaultAPIBase, maxAtt: defaultMaxAttempts}
}

// Descriptor returns the connector's self-description and declared configuration.
func (o *Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeOutput,
		Title:       "Twilio SMS (out-of-band)",
		Description: "Sends an out-of-band SMS via Twilio Programmable Messaging for severe incidents (nice-to-have; largely subsumed by PagerDuty; probable post-v1).",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgAccountSID, Type: sdk.FieldString, Required: true, Description: "Twilio Account SID (appears in the request path; an account identifier, not the secret)."},
			{Key: cfgAuthToken, Type: sdk.FieldString, Required: true, Secret: true, Description: "Twilio Auth Token (the secret). Held in memory only, never logged."},
			{Key: cfgFrom, Type: sdk.FieldString, Description: "Sender phone number in E.164 (e.g. +15558675310). Provide this OR messaging_service_sid."},
			{Key: cfgMsgService, Type: sdk.FieldString, Description: "Messaging Service SID (alternative to from)."},
			{Key: cfgTo, Type: sdk.FieldString, Description: "Default recipient in E.164 (override per-notification with Fields[\"to\"])."},
			{Key: cfgAPIBase, Type: sdk.FieldString, Default: defaultAPIBase, Description: "API base URL (override for testing)."},
			{Key: cfgMaxAtt, Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxAttempts), Description: "Total delivery attempts including the first (transient failures only)."},
		},
	}
}

// Open resolves configuration, validates the credential and sender, pre-builds the
// Basic auth header, and builds the delivery client.
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	o.accountSID = strings.TrimSpace(cfg.Get(cfgAccountSID))
	token := cfg.Get(cfgAuthToken)
	if o.accountSID == "" || token == "" {
		return fmt.Errorf("twilio: %s and %s are required", cfgAccountSID, cfgAuthToken)
	}
	o.authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(o.accountSID+":"+token))
	o.from = strings.TrimSpace(cfg.Get(cfgFrom))
	o.msgService = strings.TrimSpace(cfg.Get(cfgMsgService))
	if o.from == "" && o.msgService == "" {
		return fmt.Errorf("twilio: one of %s or %s is required", cfgFrom, cfgMsgService)
	}
	o.defaultTo = strings.TrimSpace(cfg.Get(cfgTo))
	if v := strings.TrimSpace(cfg.Get(cfgAPIBase)); v != "" {
		o.apiBase = strings.TrimRight(v, "/")
	}
	o.maxAtt = cfg.GetInt(cfgMaxAtt, o.maxAtt)

	o.client = delivery.New(o.doer, delivery.Options{MaxAttempts: o.maxAtt})
	return nil
}

// Notify sends one SMS. The recipient comes from Fields["to"] or the configured
// default; a notification with no recipient is a configuration error.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("twilio: Notify called before Open")
	}
	to := o.defaultTo
	if v := strings.TrimSpace(n.Fields[fieldTo]); v != "" {
		to = v
	}
	if to == "" {
		return fmt.Errorf("twilio: no recipient (set the to config or Fields[%q])", fieldTo)
	}

	form := url.Values{}
	form.Set("To", to)
	if o.msgService != "" {
		form.Set("MessagingServiceSid", o.msgService)
	} else {
		form.Set("From", o.from)
	}
	form.Set("Body", messageText(n))

	endpoint := o.apiBase + "/2010-04-01/Accounts/" + url.PathEscape(o.accountSID) + "/Messages.json"
	res, err := o.client.Send(ctx, delivery.Request{
		URL: endpoint,
		Header: map[string]string{
			"Content-Type":  "application/x-www-form-urlencoded",
			"Authorization": o.authHeader,
		},
		Body: []byte(form.Encode()),
	})
	if err != nil {
		// The endpoint is not a secret (it carries the Account SID, an identifier); the
		// Auth Token is only in the Authorization header, which delivery never logs.
		return fmt.Errorf("twilio: send message: %w", err)
	}
	_ = res // 201 Created on success; Twilio signals failures with a non-2xx status
	return nil
}

// Close releases resources; this connector holds none.
func (o *Output) Close(context.Context) error { return nil }

// messageText builds the SMS body: the Title and Body joined, truncated to Twilio's
// limit. A wholly empty notification yields a stable placeholder.
func messageText(n sdk.Notification) string {
	parts := make([]string, 0, 2)
	if n.Title != "" {
		parts = append(parts, n.Title)
	}
	if n.Body != "" {
		parts = append(parts, n.Body)
	}
	s := strings.Join(parts, "\n")
	if s == "" {
		s = "olivares notification"
	}
	r := []rune(s)
	if len(r) > maxBodyLen {
		return string(r[:maxBodyLen])
	}
	return s
}
