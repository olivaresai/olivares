// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package amqp

import "context"

// Message is the connector-neutral view of an AMQP 1.0 message the observer reads.
// It carries ONLY the framing METADATA a minimal-data observer derives edges from
// (the standard Properties section: To/address, Subject, MessageID, UserID, GroupID;
// and the ApplicationProperties section, where a CloudEvents binary binding rides).
// It deliberately holds NO body/Data/Value field — the real client never copies the
// payload into it, so a record's content cannot leak into an edge by construction
// (docs/SECURITY-HARDENING.md). Keeping it library-agnostic also keeps go-amqp confined to the
// real-client file, so the fake in tests is trivial.
type Message struct {
	// To is the message's Properties.to — the node the producer addressed.
	To string
	// Subject is Properties.subject.
	Subject string
	// MessageID is Properties.message-id rendered as a string (any AMQP id type).
	MessageID string
	// UserID is Properties.user-id — the authenticated producing identity, when the
	// broker stamps it.
	UserID string
	// GroupID is Properties.group-id.
	GroupID string
	// AppProps is the application-properties section (string-valued), where a
	// CloudEvents binary binding (cloudEvents_*) is recognized. Values are rendered
	// to strings by the real client; non-string values are dropped, never decoded.
	AppProps map[string]string
}

// receiver is the narrow transport seam the Source consumes through. The real
// implementation (realReceiver, amqpclient.go) wraps a go-amqp *Receiver attached to
// the configured observation address; tests inject a fake that yields canned
// Messages, so the observation path runs offline with no broker and no network.
//
// Receive blocks until a message arrives or ctx is canceled (returning ctx.Err()).
// Accept settles the message on the OBSERVATION queue only — it never touches the
// application's production queue (the observation address is a dedicated tee; see
// doc.go). Close releases the link, session and connection.
type receiver interface {
	Receive(ctx context.Context) (Message, error)
	Accept(ctx context.Context, msg Message) error
	Close()
}

// sender is the narrow transport seam the Output produces through. The real
// implementation wraps a go-amqp *Sender attached to the egress address; tests inject
// a fake that captures the sent message. Send delivers one already-encoded message
// (a CloudEvent in structured or binary form). Close releases the link/session/conn.
type sender interface {
	Send(ctx context.Context, msg OutMessage) error
	Close()
}

// OutMessage is the connector-neutral egress message the Output hands to the sender
// seam: the raw CloudEvent body, the content-type to stamp on the AMQP message, and
// (binary mode only) the cloudEvents_-prefixed application-properties. The real
// client maps it onto a go-amqp *Message; the fake captures it for assertions.
type OutMessage struct {
	// Body is the message application-data (the CloudEvent: a structured JSON
	// document, or the raw data in binary mode).
	Body []byte
	// ContentType is set on the AMQP message's content-type property (the structured
	// CloudEvents media type, or the event's datacontenttype in binary mode).
	ContentType string
	// AppProps are the application-properties to set (the cloudEvents_-prefixed
	// context attributes in binary mode; nil in structured mode).
	AppProps map[string]string
}
