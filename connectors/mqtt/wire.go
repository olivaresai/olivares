// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// This file is the REAL, hand-rolled MQTT 5.0 client over net.Conn / tls.Conn —
// STDLIB ONLY, no third-party (paho) client. It implements just enough of the
// protocol to OBSERVE: dial, CONNECT, SUBSCRIBE to the observation topic filters,
// then read PUBLISH packets, sending PINGREQ on keepalive. The framing primitives
// (the Variable Byte Integer codec and the PUBLISH parser) are pure and live here so
// wire_test.go exercises them offline — proving the hand-rolled framing is real
// rather than mocked at a high level.
//
// MQTT 5.0 control packets used:
//
//	CONNECT(1) / CONNACK(2)      connection setup
//	SUBSCRIBE(8) / SUBACK(9)     register the observation topic filters
//	PUBLISH(3)                   the observed traffic (read-only)
//	PINGREQ(12) / PINGRESP(13)   keepalive
//	DISCONNECT(14)               clean close
//
// A fixed header is one byte (packet type<<4 | flags) followed by a Variable Byte
// Integer remaining-length. All UTF-8 strings are a 2-byte big-endian length prefix
// followed by the bytes.

// MQTT 5.0 control packet types (high nibble of the fixed-header byte).
const (
	pktCONNECT    = 1
	pktCONNACK    = 2
	pktPUBLISH    = 3
	pktSUBSCRIBE  = 8
	pktSUBACK     = 9
	pktPINGREQ    = 12
	pktPINGRESP   = 13
	pktDISCONNECT = 14
)

const protocolVersion5 = 5

// MQTT 5 property identifiers used here. Properties are a VBI-length-prefixed block;
// each property is a 1-byte identifier followed by a typed value.
const (
	propContentType  = 0x03 // UTF-8 string
	propUserProperty = 0x26 // two UTF-8 strings (key, value)
)

// ----------------------------------------------------------------------------
// Variable Byte Integer codec (MQTT 5 §1.5.5): 7 data bits per byte, the high bit
// (0x80) is the continuation flag. Encodes 0..268_435_455 in 1..4 bytes.
// ----------------------------------------------------------------------------

// errVBIOverflow is returned when a Variable Byte Integer exceeds its 4-byte limit
// (a malformed frame), so the reader fails fast rather than looping.
var errVBIOverflow = errors.New("mqtt: malformed variable byte integer")

// encodeVBI appends the Variable Byte Integer encoding of n to dst and returns it.
// n must be in [0, 268435455]; callers only encode remaining-lengths and property
// lengths, which are bounded by the (already capped) frame size.
func encodeVBI(dst []byte, n int) []byte {
	if n < 0 {
		n = 0
	}
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80 // more bytes follow
		}
		dst = append(dst, b)
		if n == 0 {
			return dst
		}
	}
}

// decodeVBI decodes a Variable Byte Integer from b starting at off, returning the
// value and the number of bytes consumed. It is the pure counterpart of encodeVBI.
func decodeVBI(b []byte, off int) (value, consumed int, err error) {
	multiplier := 1
	for i := 0; i < 4; i++ {
		if off+i >= len(b) {
			return 0, 0, io.ErrUnexpectedEOF
		}
		digit := b[off+i]
		value += int(digit&0x7f) * multiplier
		if digit&0x80 == 0 {
			return value, i + 1, nil
		}
		multiplier *= 128
	}
	return 0, 0, errVBIOverflow
}

// readVBI reads a Variable Byte Integer from r byte-by-byte (the framing reader does
// not know the length in advance). It mirrors decodeVBI for the streaming case.
func readVBI(r io.ByteReader) (int, error) {
	value := 0
	multiplier := 1
	for i := 0; i < 4; i++ {
		digit, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		value += int(digit&0x7f) * multiplier
		if digit&0x80 == 0 {
			return value, nil
		}
		multiplier *= 128
	}
	return 0, errVBIOverflow
}

// ----------------------------------------------------------------------------
// UTF-8 string + property encoding helpers.
// ----------------------------------------------------------------------------

// appendString appends an MQTT UTF-8 string (2-byte BE length + bytes).
func appendString(dst []byte, s string) []byte {
	var lp [2]byte
	binary.BigEndian.PutUint16(lp[:], uint16(len(s)))
	dst = append(dst, lp[:]...)
	return append(dst, s...)
}

// readString reads an MQTT UTF-8 string from b at off, returning the string and the
// total bytes consumed (2 + length).
func readString(b []byte, off int) (string, int, error) {
	if off+2 > len(b) {
		return "", 0, io.ErrUnexpectedEOF
	}
	n := int(binary.BigEndian.Uint16(b[off:]))
	start := off + 2
	if start+n > len(b) {
		return "", 0, io.ErrUnexpectedEOF
	}
	return string(b[start : start+n]), 2 + n, nil
}

// ----------------------------------------------------------------------------
// PUBLISH parser (pure): extract the topic, the User Properties and the Content
// Type from a PUBLISH variable header + properties. The payload is returned as
// opaque bytes (NEVER read for content). QoS comes from the fixed-header flags.
// ----------------------------------------------------------------------------

// parsePublish decodes a PUBLISH packet's variable header and properties into a
// minimal-data Publish. flags is the low nibble of the fixed header (carrying the
// QoS in bits 1-2); body is the remaining-length bytes (everything after the fixed
// header). It reads ONLY the topic, the MQTT 5 User Properties and the Content Type
// — the payload is kept opaque and never interpreted. A malformed packet is an
// error, not a guess.
func parsePublish(flags byte, body []byte) (Publish, error) {
	off := 0
	topic, n, err := readString(body, off)
	if err != nil {
		return Publish{}, fmt.Errorf("mqtt: publish topic: %w", err)
	}
	off += n

	qos := (flags >> 1) & 0x03
	if qos > 0 {
		// Packet Identifier (2 bytes) is present only for QoS 1/2.
		if off+2 > len(body) {
			return Publish{}, fmt.Errorf("mqtt: publish packet id: %w", io.ErrUnexpectedEOF)
		}
		off += 2
	}

	// Properties: a VBI length prefix followed by that many property bytes.
	propLen, c, err := decodeVBI(body, off)
	if err != nil {
		return Publish{}, fmt.Errorf("mqtt: publish property length: %w", err)
	}
	off += c
	propEnd := off + propLen
	if propEnd > len(body) {
		return Publish{}, fmt.Errorf("mqtt: publish properties overrun")
	}

	pub := Publish{Topic: topic}
	for off < propEnd {
		id := body[off]
		off++
		switch id {
		case propContentType:
			ct, cn, serr := readString(body, off)
			if serr != nil {
				return Publish{}, fmt.Errorf("mqtt: content type: %w", serr)
			}
			pub.ContentType = ct
			off += cn
		case propUserProperty:
			k, kn, serr := readString(body, off)
			if serr != nil {
				return Publish{}, fmt.Errorf("mqtt: user property key: %w", serr)
			}
			off += kn
			v, vn, serr := readString(body, off)
			if serr != nil {
				return Publish{}, fmt.Errorf("mqtt: user property value: %w", serr)
			}
			off += vn
			if pub.UserProps == nil {
				pub.UserProps = map[string]string{}
			}
			pub.UserProps[k] = v
		default:
			// Any other PUBLISH property (Payload Format Indicator, Message Expiry,
			// Topic Alias, Response Topic, Correlation Data, Subscription Identifier).
			// We must skip its value to stay frame-aligned without reading payload.
			adv, serr := skipProperty(id, body, off)
			if serr != nil {
				return Publish{}, serr
			}
			off += adv
		}
	}
	// The payload is everything after the properties block. It is kept opaque and
	// NEVER read for content (docs/SECURITY-HARDENING.md); the observer ignores it entirely.
	pub.Payload = body[propEnd:]
	return pub, nil
}

// skipProperty advances past a PUBLISH property value we do not consume, returning
// the number of bytes its value occupies. Only the property types that can appear in
// a PUBLISH are handled; an unrecognized id is rejected (malformed), never guessed.
func skipProperty(id byte, body []byte, off int) (int, error) {
	switch id {
	case 0x01, 0x0B: // Payload Format Indicator (1 byte) / Subscription Identifier (VBI)
		if id == 0x01 {
			return 1, nil
		}
		_, c, err := decodeVBI(body, off)
		return c, err
	case 0x02: // Message Expiry Interval (4 bytes)
		return 4, nil
	case 0x23: // Topic Alias (2 bytes)
		return 2, nil
	case 0x08, 0x09: // Response Topic (UTF-8) / Correlation Data (binary, 2-byte len)
		if off+2 > len(body) {
			return 0, io.ErrUnexpectedEOF
		}
		n := int(binary.BigEndian.Uint16(body[off:]))
		return 2 + n, nil
	default:
		return 0, fmt.Errorf("mqtt: unsupported publish property 0x%02x", id)
	}
}

// ----------------------------------------------------------------------------
// CONNECT / SUBSCRIBE packet builders (pure): produce the wire bytes for the
// observation handshake. They are unit-tested by round-tripping their lengths.
// ----------------------------------------------------------------------------

// buildConnect builds an MQTT 5.0 CONNECT packet for a clean, observe-only session.
// Variable header: protocol name "MQTT", version 5, connect flags, keepalive, an
// EMPTY properties block (single 0x00), then the client id (and optional
// username/password). The credentials are placed only on the wire, never logged.
func buildConnect(clientID, username, password string, keepalive time.Duration) []byte {
	ka := int(keepalive.Seconds())
	if ka < 0 {
		ka = 0
	}
	if ka > 0xFFFF {
		ka = 0xFFFF
	}

	var connectFlags byte = 0x02 // Clean Start
	if username != "" {
		connectFlags |= 0x80
	}
	if password != "" {
		connectFlags |= 0x40
	}

	var vh []byte
	vh = appendString(vh, "MQTT")     // protocol name
	vh = append(vh, protocolVersion5) // protocol version 5
	vh = append(vh, connectFlags)     // connect flags
	var kab [2]byte                   // keepalive (seconds)
	binary.BigEndian.PutUint16(kab[:], uint16(ka))
	vh = append(vh, kab[:]...)
	vh = append(vh, 0x00) // empty CONNECT properties block (VBI length = 0)

	// Payload: client id, then username/password when present.
	var pl []byte
	pl = appendString(pl, clientID)
	if username != "" {
		pl = appendString(pl, username)
	}
	if password != "" {
		pl = appendString(pl, password)
	}

	return frame(pktCONNECT, 0, append(vh, pl...))
}

// buildSubscribe builds an MQTT 5.0 SUBSCRIBE packet for the observation topic
// filters at QoS 0 (a passive observer needs no acknowledgement back-pressure). The
// fixed-header flags for SUBSCRIBE are reserved as 0b0010. packetID identifies the
// SUBACK. Each filter carries a one-byte subscription-options field (QoS 0).
func buildSubscribe(packetID uint16, filters []string) []byte {
	var vh []byte
	var idb [2]byte
	binary.BigEndian.PutUint16(idb[:], packetID)
	vh = append(vh, idb[:]...)
	vh = append(vh, 0x00) // empty SUBSCRIBE properties block

	for _, f := range filters {
		vh = appendString(vh, f)
		vh = append(vh, 0x00) // subscription options: QoS 0, no-local off, etc.
	}
	return frame(pktSUBSCRIBE, 0x02, vh)
}

// frame wraps a variable-header+payload in an MQTT fixed header: (type<<4 | flags),
// then the remaining length as a Variable Byte Integer, then the bytes.
func frame(pktType, flags byte, rest []byte) []byte {
	out := []byte{pktType<<4 | flags}
	out = encodeVBI(out, len(rest))
	return append(out, rest...)
}

// ----------------------------------------------------------------------------
// The real connection: dial, handshake, and a PUBLISH read loop.
// ----------------------------------------------------------------------------

// maxRemainingLen caps an accepted remaining-length so a malformed or hostile frame
// cannot drive an unbounded allocation (the observer only needs small control/topic
// frames; a 16 MiB ceiling is generous for any real Sparkplug/MQTT control surface).
const maxRemainingLen = 16 << 20

// conn is the real hand-rolled MQTT 5 client. It owns the net.Conn, a buffered
// reader, and a write mutex (the keepalive PINGREQ and the read loop both touch the
// connection). It satisfies the mqttClient seam.
type conn struct {
	nc        net.Conn
	r         *bufio.Reader
	wmu       sync.Mutex
	keepalive time.Duration
	nextPing  time.Time
}

var _ mqttClient = (*conn)(nil)

// dialClient dials the broker, performs the MQTT 5 CONNECT and SUBSCRIBE handshake,
// and returns a ready-to-read client. TLS is used when the config selected it (the
// secure default for OT/IoT). A handshake failure returns an error so Gather can
// surface it to the engine for backoff.
func dialClient(c config) (mqttClient, error) {
	d := net.Dialer{Timeout: c.timeout}
	var nc net.Conn
	var err error
	if c.useTLS {
		nc, err = tls.DialWithDialer(&d, "tcp", c.host, c.tls)
	} else {
		nc, err = d.DialContext(context.Background(), "tcp", c.host)
	}
	if err != nil {
		return nil, fmt.Errorf("mqtt: dial %s: %w", c.host, err)
	}

	cn := &conn{nc: nc, r: bufio.NewReader(nc), keepalive: c.keepalive}

	if c.timeout > 0 {
		_ = nc.SetWriteDeadline(time.Now().Add(c.timeout))
	}
	if err := cn.write(buildConnect(c.clientID, c.username, c.password, c.keepalive)); err != nil {
		nc.Close()
		return nil, err
	}
	if c.timeout > 0 {
		_ = nc.SetReadDeadline(time.Now().Add(c.timeout))
	}
	if err := cn.expect(pktCONNACK); err != nil {
		nc.Close()
		return nil, fmt.Errorf("mqtt: connack: %w", err)
	}

	if err := cn.write(buildSubscribe(1, c.topics)); err != nil {
		nc.Close()
		return nil, err
	}
	if err := cn.expect(pktSUBACK); err != nil {
		nc.Close()
		return nil, fmt.Errorf("mqtt: suback: %w", err)
	}

	// Clear deadlines; the read loop manages its own keepalive-bounded deadlines.
	_ = nc.SetReadDeadline(time.Time{})
	_ = nc.SetWriteDeadline(time.Time{})
	cn.nextPing = time.Now().Add(cn.keepalive)
	return cn, nil
}

// write sends a complete packet under the write mutex.
func (c *conn) write(p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.nc.Write(p)
	if err != nil {
		return fmt.Errorf("mqtt: write: %w", err)
	}
	return nil
}

// expect reads one packet and asserts its type, draining its body. It is used for
// the CONNACK/SUBACK handshake replies whose contents the observer does not need.
func (c *conn) expect(want byte) error {
	pktType, _, body, err := readPacket(c.r)
	if err != nil {
		return err
	}
	if pktType != want {
		return fmt.Errorf("mqtt: expected packet 0x%x, got 0x%x", want, pktType)
	}
	_ = body
	return nil
}

// Read returns the next observed PUBLISH, sending a PINGREQ when the keepalive
// window elapses and skipping non-PUBLISH control packets (PINGRESP, etc.). It
// honors ctx by bounding each blocking read with a deadline derived from the
// keepalive, so a canceled ctx unblocks the read promptly. Only PUBLISH packets
// yield a Publish; everything else is consumed and the loop continues.
func (c *conn) Read(ctx context.Context) (Publish, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Publish{}, err
		}
		// Keepalive: send a PINGREQ when due so the broker does not drop us.
		if c.keepalive > 0 && !time.Now().Before(c.nextPing) {
			if err := c.write([]byte{pktPINGREQ << 4, 0x00}); err != nil {
				return Publish{}, err
			}
			c.nextPing = time.Now().Add(c.keepalive)
		}
		// Bound the blocking read so ctx cancellation and the next ping are honored.
		deadline := c.readDeadline(ctx)
		_ = c.nc.SetReadDeadline(deadline)

		pktType, flags, body, err := readPacket(c.r)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				// Deadline hit: re-check ctx/keepalive and read again.
				continue
			}
			return Publish{}, err
		}
		if pktType != pktPUBLISH {
			// PINGRESP and any other control packet: ignore, keep observing.
			continue
		}
		pub, perr := parsePublish(flags, body)
		if perr != nil {
			return Publish{}, perr
		}
		return pub, nil
	}
}

// readDeadline computes the next read deadline: the sooner of the ctx deadline (when
// set) and the keepalive ping time, with a short floor so a canceled ctx is noticed
// quickly even when no traffic flows.
func (c *conn) readDeadline(ctx context.Context) time.Time {
	floor := time.Now().Add(1 * time.Second)
	d := floor
	if c.keepalive > 0 && c.nextPing.Before(d) {
		d = c.nextPing
	}
	if dl, ok := ctx.Deadline(); ok && dl.Before(d) {
		d = dl
	}
	return d
}

// Close sends a DISCONNECT (best-effort) and closes the connection.
func (c *conn) Close() error {
	_ = c.write(frame(pktDISCONNECT, 0, []byte{0x00})) // reason Normal + empty props
	return c.nc.Close()
}

// readPacket reads one MQTT control packet: the fixed-header byte, the Variable Byte
// Integer remaining-length, then exactly that many body bytes. It returns the packet
// type (high nibble), the flags (low nibble) and the body. A remaining-length beyond
// maxRemainingLen is rejected to bound allocation.
func readPacket(r *bufio.Reader) (pktType, flags byte, body []byte, err error) {
	fh, err := r.ReadByte()
	if err != nil {
		return 0, 0, nil, err
	}
	rem, err := readVBI(r)
	if err != nil {
		return 0, 0, nil, err
	}
	if rem < 0 || rem > maxRemainingLen {
		return 0, 0, nil, fmt.Errorf("mqtt: remaining length %d out of bounds", rem)
	}
	body = make([]byte, rem)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, 0, nil, err
	}
	return fh >> 4, fh & 0x0f, body, nil
}
