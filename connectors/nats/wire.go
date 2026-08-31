// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Msg is the minimal-data view of one JetStream message the connector observes. The
// Data bytes are present ONLY so the observer can recognize a structured CloudEvents
// JSON document and recover its context attributes (source/type) — Data is NEVER
// emitted or persisted (docs/SECURITY-HARDENING.md). Subject/ReplyTo/Header carry framing the
// observer derives topology and producer attribution from.
type Msg struct {
	// Subject is the message's published subject (NOT the inbox it was delivered to).
	Subject string
	// ReplyTo is the optional reply-to subject from the frame.
	ReplyTo string
	// Header is the NATS/1.0 header block (HMSG only), bare attribute names → value.
	// For a binary-mode CloudEvent the names ARE the bare attribute names (MQTTPrefix
	// == ""), so the CloudEvents binary parser reads them directly.
	Header map[string]string
	// Status is the JetStream control status when the frame is a flow-control/heartbeat
	// or "no messages" status message (e.g. "404", "408", "409"); empty for a real
	// stream message. A status frame carries no observable topology.
	Status string
	// Data is the message payload (used only for CloudEvents recognition; never emitted).
	Data []byte
}

// jsClient is the narrow TRANSPORT SEAM for the JetStream PULL consumer. The real
// implementation (client.go) dials a net.Conn/tls.Conn, performs the NATS handshake
// and drives the durable consumer; a fake in tests yields canned Msgs so the
// observation path runs offline with no network. Next issues one pull request for up
// to batch messages and returns the stream messages that arrived (status/control
// frames are filtered out by the implementation). Close releases the connection.
type jsClient interface {
	Next(ctx context.Context, batch int) ([]Msg, error)
	Close() error
}

// isStatusCode reports whether a JetStream status code means "no messages right now"
// (request expired / no messages / consumer conflict) rather than a real delivery, so
// the pull loop simply re-requests. 404 = no messages, 408 = request timeout/expiry,
// 409 = consumer deleted/exceeded or conflict, 100 = idle heartbeat/flow control.
func isStatusCode(code string) bool {
	switch code {
	case "404", "408", "409", "100", "503":
		return true
	default:
		return false
	}
}

// parseControlLine splits a NATS server protocol control line (already stripped of
// its trailing CRLF) into its verb (uppercased) and the remaining argument string.
func parseControlLine(line string) (verb, args string) {
	line = strings.TrimRight(line, "\r\n")
	if i := strings.IndexByte(line, ' '); i >= 0 {
		return strings.ToUpper(line[:i]), strings.TrimSpace(line[i+1:])
	}
	return strings.ToUpper(line), ""
}

// parseMsgArgs parses the argument list of a MSG control line:
//
//	MSG <subject> <sid> [reply-to] <#bytes>
//
// returning the subject, optional reply-to, and the payload byte count. The reply-to
// token is present only when there are four tokens (subject, sid, reply, size).
func parseMsgArgs(args string) (subject, reply string, n int, err error) {
	f := strings.Fields(args)
	switch len(f) {
	case 3: // subject sid size
		subject = f[0]
		n, err = strconv.Atoi(f[2])
	case 4: // subject sid reply size
		subject, reply = f[0], f[2]
		n, err = strconv.Atoi(f[3])
	default:
		return "", "", 0, fmt.Errorf("nats: malformed MSG args %q", args)
	}
	if err != nil {
		return "", "", 0, fmt.Errorf("nats: bad MSG byte count in %q: %w", args, err)
	}
	return subject, reply, n, nil
}

// parseHMsgArgs parses the argument list of an HMSG control line:
//
//	HMSG <subject> <sid> [reply-to] <#header-bytes> <#total-bytes>
//
// returning the subject, optional reply-to, header byte count and total byte count
// (the payload size is total-header).
func parseHMsgArgs(args string) (subject, reply string, hdrLen, totalLen int, err error) {
	f := strings.Fields(args)
	switch len(f) {
	case 4: // subject sid hdrlen totallen
		subject = f[0]
		hdrLen, err = strconv.Atoi(f[2])
		if err == nil {
			totalLen, err = strconv.Atoi(f[3])
		}
	case 5: // subject sid reply hdrlen totallen
		subject, reply = f[0], f[2]
		hdrLen, err = strconv.Atoi(f[3])
		if err == nil {
			totalLen, err = strconv.Atoi(f[4])
		}
	default:
		return "", "", 0, 0, fmt.Errorf("nats: malformed HMSG args %q", args)
	}
	if err != nil {
		return "", "", 0, 0, fmt.Errorf("nats: bad HMSG byte count in %q: %w", args, err)
	}
	if hdrLen < 0 || totalLen < hdrLen {
		return "", "", 0, 0, fmt.Errorf("nats: inconsistent HMSG lengths hdr=%d total=%d", hdrLen, totalLen)
	}
	return subject, reply, hdrLen, totalLen, nil
}

// parseHeaderBlock parses a NATS/1.0 header block into a status code and a header
// map. The block is:
//
//	NATS/1.0[ <code> [text]]\r\n
//	Name: value\r\n
//	...\r\n
//	\r\n
//
// The inline status (e.g. "NATS/1.0 404 No Messages") is the JetStream control status;
// it is returned separately so the caller can treat 404/408/409 as "no messages".
// Header names are kept as-is so a bare CloudEvents binary attribute name survives.
func parseHeaderBlock(block []byte) (status string, header map[string]string) {
	header = map[string]string{}
	lines := strings.Split(string(block), "\r\n")
	if len(lines) == 0 {
		return "", header
	}
	// First line: version + optional status code/text.
	first := lines[0]
	if strings.HasPrefix(first, "NATS/1.0") {
		rest := strings.TrimSpace(strings.TrimPrefix(first, "NATS/1.0"))
		if rest != "" {
			if i := strings.IndexByte(rest, ' '); i >= 0 {
				status = rest[:i]
			} else {
				status = rest
			}
		}
	}
	for _, l := range lines[1:] {
		if l == "" {
			continue
		}
		if i := strings.IndexByte(l, ':'); i >= 0 {
			name := strings.TrimSpace(l[:i])
			val := strings.TrimSpace(l[i+1:])
			if name != "" {
				header[name] = val
			}
		}
	}
	if len(header) == 0 {
		header = nil
	}
	return status, header
}

// readMsg reads one MSG frame body from r given the parsed control args. It consumes
// exactly n payload bytes followed by the trailing CRLF, returning a Msg. It is the
// pure, offline-testable core that proves the hand-rolled framing is real: given a
// buffer "MSG subj 1 5\r\nhello\r\n" it parses {Subject:"subj", Data:"hello"}.
func readMsg(r *bufio.Reader, args string) (Msg, error) {
	subject, reply, n, err := parseMsgArgs(args)
	if err != nil {
		return Msg{}, err
	}
	payload, err := readBody(r, n)
	if err != nil {
		return Msg{}, err
	}
	return Msg{Subject: subject, ReplyTo: reply, Data: payload}, nil
}

// readHMsg reads one HMSG frame body from r given the parsed control args: the header
// block (hdrLen bytes), then the payload (total-hdr bytes), then the trailing CRLF.
func readHMsg(r *bufio.Reader, args string) (Msg, error) {
	subject, reply, hdrLen, totalLen, err := parseHMsgArgs(args)
	if err != nil {
		return Msg{}, err
	}
	whole, err := readBody(r, totalLen)
	if err != nil {
		return Msg{}, err
	}
	headerBytes := whole[:hdrLen]
	payload := whole[hdrLen:]
	// The header block ends with a blank line (\r\n\r\n); trim a trailing CRLF pair so
	// the parser sees clean lines.
	status, header := parseHeaderBlock(bytes.TrimRight(headerBytes, "\r\n"))
	return Msg{Subject: subject, ReplyTo: reply, Header: header, Status: status, Data: payload}, nil
}

// readBody reads exactly n payload bytes from r and consumes the trailing CRLF the
// NATS protocol appends after every MSG/HMSG body. It returns a freshly allocated
// slice so the bufio buffer can be reused.
func readBody(r *bufio.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	if n > 0 {
		if _, err := readFull(r, buf); err != nil {
			return nil, err
		}
	}
	// Consume the trailing CRLF.
	if _, err := r.Discard(2); err != nil {
		return nil, err
	}
	return buf, nil
}

// readFull reads len(buf) bytes from r, returning an error on a short read. bufio's
// Read may return fewer bytes than requested, so this loops.
func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
