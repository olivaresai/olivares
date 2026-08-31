// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package amqp

import (
	"context"
	"fmt"
	"time"

	goamqp "github.com/Azure/go-amqp"
)

// This file is the ONLY place go-amqp is touched, so the connector's observation
// logic (observe.go) and lifecycle (amqp.go) run offline in CI behind the receiver/
// sender seams. It maps the connector config onto go-amqp dial/SASL/TLS options and
// converts a go-amqp *Message into the connector-neutral Message (metadata only — the
// body is never copied across the seam, so it cannot leak into an edge).

// defaultReceiverFactory dials the broker, opens a session and attaches a receiver to
// the dedicated observation address. It is the Source's newReceiver default.
func defaultReceiverFactory(c config) (receiver, error) {
	conn, sess, err := dialSession(c)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	r, err := sess.NewReceiver(ctx, c.observationAddress, &goamqp.ReceiverOptions{
		// Attach as a non-destructive observer of a dedicated tee queue. We settle
		// (accept) explicitly after observing, so use the default second settlement
		// mode; nothing here drains the application's own production queue.
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("amqp: attach receiver to %q: %w", c.observationAddress, err)
	}
	return &realReceiver{conn: conn, sess: sess, r: r, timeout: c.timeout}, nil
}

// defaultSenderFactory dials the broker, opens a session and attaches a sender to the
// egress address. It is the Output's newSender default.
func defaultSenderFactory(c config) (sender, error) {
	conn, sess, err := dialSession(c)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	snd, err := sess.NewSender(ctx, c.egressAddress, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("amqp: attach sender to %q: %w", c.egressAddress, err)
	}
	return &realSender{conn: conn, sess: sess, s: snd, timeout: c.timeout}, nil
}

// dialSession is the pure config→dial mapping: it builds the SASL/TLS connection
// options from config and opens a connection and a session. amqp.Dial parses the addr
// scheme itself (amqps:// negotiates TLS using the supplied TLSConfig; amqp:// is
// plaintext). The SASL secret stays in the option closure, never logged.
func dialSession(c config) (*goamqp.Conn, *goamqp.Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	conn, err := goamqp.Dial(ctx, c.addr, connOptions(c))
	if err != nil {
		return nil, nil, fmt.Errorf("amqp: dial %s: %w", c.namespaceRef, err)
	}
	sess, err := conn.NewSession(ctx, nil)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("amqp: open session: %w", err)
	}
	return conn, sess, nil
}

// connOptions maps the connector config onto go-amqp ConnOptions: the SASL mechanism
// (ANONYMOUS when requested or when no user is set, otherwise PLAIN) and the TLS
// config (used by amqps://). This is kept separate so the mapping is small and the
// security choices are obvious in review.
func connOptions(c config) *goamqp.ConnOptions {
	opts := &goamqp.ConnOptions{TLSConfig: c.tls}
	switch {
	case c.saslAnonymous || c.saslUser == "":
		opts.SASLType = goamqp.SASLTypeAnonymous()
	default:
		opts.SASLType = goamqp.SASLTypePlain(c.saslUser, c.saslPass)
	}
	return opts
}

// realReceiver wraps a go-amqp connection/session/receiver and adapts go-amqp
// *Message to the connector-neutral Message (metadata only).
type realReceiver struct {
	conn    *goamqp.Conn
	sess    *goamqp.Session
	r       *goamqp.Receiver
	timeout time.Duration
	last    *goamqp.Message // the in-flight go-amqp message, settled by Accept
}

func (rr *realReceiver) Receive(ctx context.Context) (Message, error) {
	m, err := rr.r.Receive(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	rr.last = m
	return toNeutral(m), nil
}

// Accept settles the in-flight message on the observation link. It is bounded by the
// connector timeout so a stuck broker cannot block shutdown indefinitely.
func (rr *realReceiver) Accept(ctx context.Context, _ Message) error {
	if rr.last == nil {
		return nil
	}
	actx, cancel := context.WithTimeout(ctx, rr.timeout)
	defer cancel()
	err := rr.r.AcceptMessage(actx, rr.last)
	rr.last = nil
	return err
}

func (rr *realReceiver) Close() {
	if rr.r != nil {
		ctx, cancel := context.WithTimeout(context.Background(), rr.timeout)
		_ = rr.r.Close(ctx)
		cancel()
	}
	if rr.conn != nil {
		rr.conn.Close()
	}
}

// realSender wraps a go-amqp connection/session/sender for egress.
type realSender struct {
	conn    *goamqp.Conn
	sess    *goamqp.Session
	s       *goamqp.Sender
	timeout time.Duration
}

func (rs *realSender) Send(ctx context.Context, out OutMessage) error {
	msg := goamqp.NewMessage(out.Body)
	if out.ContentType != "" {
		ct := out.ContentType
		if msg.Properties == nil {
			msg.Properties = &goamqp.MessageProperties{}
		}
		msg.Properties.ContentType = &ct
	}
	if len(out.AppProps) > 0 {
		msg.ApplicationProperties = make(map[string]any, len(out.AppProps))
		for k, v := range out.AppProps {
			msg.ApplicationProperties[k] = v
		}
	}
	sctx, cancel := context.WithTimeout(ctx, rs.timeout)
	defer cancel()
	return rs.s.Send(sctx, msg, nil)
}

func (rs *realSender) Close() {
	if rs.s != nil {
		ctx, cancel := context.WithTimeout(context.Background(), rs.timeout)
		_ = rs.s.Close(ctx)
		cancel()
	}
	if rs.conn != nil {
		rs.conn.Close()
	}
}

// toNeutral copies ONLY the framing metadata from a go-amqp message into the neutral
// Message. The body sections (Data/Value/Sequence) are deliberately NOT read — the
// neutral struct has no place to put them, so a payload cannot cross the seam
// (docs/SECURITY-HARDENING.md). Application-property values that are not strings are dropped (never
// decoded), keeping the CloudEvents binding recognition string-only.
func toNeutral(m *goamqp.Message) Message {
	out := Message{}
	if p := m.Properties; p != nil {
		if p.To != nil {
			out.To = *p.To
		}
		if p.Subject != nil {
			out.Subject = *p.Subject
		}
		out.MessageID = idToString(p.MessageID)
		if len(p.UserID) > 0 {
			out.UserID = string(p.UserID)
		}
		if p.GroupID != nil {
			out.GroupID = *p.GroupID
		}
	}
	if len(m.ApplicationProperties) > 0 {
		out.AppProps = make(map[string]string, len(m.ApplicationProperties))
		for k, v := range m.ApplicationProperties {
			if s, ok := v.(string); ok {
				out.AppProps[k] = s
			}
		}
	}
	return out
}

// idToString renders an AMQP message-id (uint64, UUID, []byte, or string) to a
// string. A nil id yields "". The id is a non-sensitive correlation handle, not body
// content.
func idToString(id any) string {
	switch v := id.(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}
