// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/sdk"
)

// Limits of a one-publish read.
const (
	// MaxPublishTimeout bounds one whole read: dial, handshake and wait for the publication.
	MaxPublishTimeout = 5 * time.Second
	// DefaultPublishPayload is the payload limit when PublishOptions leaves it out.
	DefaultPublishPayload = 4096
	// MaxPublishPayload is the largest payload limit a caller may ask for.
	MaxPublishPayload = 64 << 10

	// subscribeNoRetained is the SUBSCRIBE option byte for Retain Handling 2: the broker does
	// not send retained messages at subscribe time (MQTT 5, section 3.8.3.1).
	subscribeNoRetained = 0x20
)

// What a read can fail with. The text is fixed: it never carries the broker address, a
// credential, a path or anything a device published. A refused CONNACK adds its reason byte.
var (
	ErrBrokerUnavailable = errors.New("mqtt: broker unavailable")
	ErrTLSHandshake      = errors.New("mqtt: TLS handshake with the broker failed")
	ErrBrokerRefused     = errors.New("mqtt: broker refused the connection or subscription")
	ErrBrokerCredentials = error(credentialsRefusal{})
	ErrNoPublish         = errors.New("mqtt: no publication received")
	ErrConnectionLost    = errors.New("mqtt: connection lost or unusable packet while waiting for a publication")
	ErrPayloadTooLarge   = errors.New("mqtt: publication larger than the limit")
	ErrEarlyPublication  = errors.New("mqtt: invalid early publication")
)

// credentialsRefusal is the refusal an operator fixes in the credentials. It is still a refusal for
// errors.Is, so a caller that sorts on ErrBrokerRefused alone (the Business MQTT reader) is unchanged.
type credentialsRefusal struct{}

func (credentialsRefusal) Error() string {
	return "mqtt: broker refused the credentials or authorization"
}

func (credentialsRefusal) Is(target error) bool { return target == ErrBrokerRefused }

// packetOverhead is what a PUBLISH may carry besides its payload and topic: the packet identifier
// and the properties. A packet declaring more than payload + topic + this is refused unread.
const packetOverhead = 16 << 10

// PublishOptions says what one read waits for.
type PublishOptions struct {
	// Topic is one exact topic. A wildcard, an empty or a non-UTF-8 topic is refused.
	Topic string
	// Timeout bounds the whole read; zero means MaxPublishTimeout, and more is refused.
	Timeout time.Duration
	// MaxPayload is the largest payload accepted, in bytes; zero means DefaultPublishPayload.
	MaxPayload int
	// SkipRetained asks the broker not to send the retained message at subscribe time, so a
	// stored publication cannot answer the read. A broker that sends one anyway is reported
	// by Publish.Retain.
	SkipRetained bool
}

// PublishReader reads the first publication on one exact topic. It is the generic part of
// TemperatureReader: each Read opens a new clean session, subscribes to that one topic and
// closes again, so no connection, subscription or value survives a call. Judging what the
// payload means (a timestamp, a value) belongs to the caller; this type has no MCP entry
// point and no tool of its own.
type PublishReader struct {
	cfg          config
	topic        string
	maxPayload   int
	timeout      time.Duration
	skipRetained bool
	clientPrefix string
}

// NewPublishReader uses the MQTT broker and TLS settings of the source (broker, username,
// password, tls, tls_ca_file, tls_cert_file, tls_key_file) plus the options. Configuration
// errors are fixed sentences: they never echo a broker URL or a path.
func NewPublishReader(cfg sdk.Config, o PublishOptions) (*PublishReader, error) {
	c, err := readerConfig(cfg, o.Topic, "mqtt")
	if err != nil {
		return nil, err
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = MaxPublishTimeout
	}
	payloadLimit := o.MaxPayload
	if payloadLimit == 0 {
		payloadLimit = DefaultPublishPayload
	}
	if timeout < 0 || timeout > MaxPublishTimeout || payloadLimit < 0 || payloadLimit > MaxPublishPayload {
		return nil, errors.New("mqtt: invalid timeout or payload limit")
	}
	return &PublishReader{cfg: c, topic: o.Topic, maxPayload: payloadLimit, timeout: timeout,
		skipRetained: o.SkipRetained, clientPrefix: "olivares-read-"}, nil
}

// readerConfig resolves the broker side of an exact-topic read. label prefixes its fixed
// error sentences.
func readerConfig(cfg sdk.Config, topic, label string) (config, error) {
	if topic == "" || len(topic) > 65535 || !utf8.ValidString(topic) || strings.ContainsAny(topic, "+#\x00") {
		return config{}, errors.New(label + ": configure one exact topic")
	}
	if u, err := url.Parse(cfg.Get("broker")); err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return config{}, errors.New(label + ": invalid broker configuration")
	}
	c, err := loadConfig(cfg)
	if err != nil {
		// Configuration errors can contain broker URLs or TLS paths. Do not reflect
		// them through the adapter's protocol or diagnostic output.
		return config{}, errors.New(label + ": invalid broker or TLS configuration")
	}
	if len(c.username) > 65535 || len(c.password) > 65535 {
		return config{}, errors.New(label + ": invalid credential configuration")
	}
	c.topics = []string{topic}
	return c, nil
}

// Read returns the first publication on the topic. It does not look at the payload beyond
// its size, and it does not decide whether the publication is fresh: that needs a producer
// timestamp, which only the caller knows how to find. A retained publication is returned
// with Retain set.
func (r *PublishReader) Read(ctx context.Context) (Publish, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	c, pending, err := r.connect(ctx)
	if err != nil {
		return Publish{}, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.nc.Close() })
	defer stop()
	for {
		if ctx.Err() != nil {
			return Publish{}, ErrNoPublish
		}
		var pub Publish
		if pending != nil {
			pub, pending = *pending, nil
		} else if pub, err = r.next(ctx, c); err != nil {
			return Publish{}, err
		}
		if pub.Topic != r.topic {
			continue
		}
		if len(pub.Payload) > r.maxPayload {
			return Publish{}, ErrPayloadTooLarge
		}
		return pub, nil
	}
}

// packetLimit is the largest remaining length this reader accepts for any packet.
func (r *PublishReader) packetLimit() int { return r.maxPayload + len(r.topic) + packetOverhead }

// next reads packets until a PUBLISH and parses it. It reads straight from the connection under
// the one deadline connect set for the whole call, never in short slices: a read that gives up in
// the middle of a packet would leave the next read parsing payload bytes as a packet header, and a
// publisher could shape those bytes. PINGRESP and other control packets are skipped; a packet
// declaring more than packetLimit is refused before its body is read.
func (r *PublishReader) next(ctx context.Context, c *conn) (Publish, error) {
	for {
		kind, flags, body, err := readPacketMax(c.r, r.packetLimit())
		var timeout net.Error
		switch {
		case errors.Is(err, errRemainingTooLarge):
			return Publish{}, ErrPayloadTooLarge
		case err != nil && (ctx.Err() != nil || errors.As(err, &timeout) && timeout.Timeout()):
			return Publish{}, ErrNoPublish
		case err != nil:
			return Publish{}, ErrConnectionLost
		}
		if kind != pktPUBLISH {
			continue
		}
		pub, err := parsePublish(flags, body)
		if err != nil {
			return Publish{}, ErrConnectionLost
		}
		return pub, nil
	}
}

// Reuse the source's MQTT framing, packet builders and connection reader. This
// handshake adds one whole-call deadline and checks broker refusal reason codes.
func (r *PublishReader) connect(ctx context.Context) (*conn, *Publish, error) {
	nc, err := (&net.Dialer{}).DialContext(ctx, "tcp", r.cfg.host)
	if err != nil {
		return nil, nil, ErrBrokerUnavailable
	}
	transport := nc
	stop := context.AfterFunc(ctx, func() { _ = transport.Close() })
	defer stop()
	if r.cfg.useTLS {
		tlsConfig := r.cfg.tls.Clone()
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName, _, _ = net.SplitHostPort(r.cfg.host)
		}
		tc := tls.Client(nc, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = nc.Close()
			if ctx.Err() != nil {
				return nil, nil, ErrBrokerUnavailable // the call ran out of time, not a verdict on the certificate
			}
			return nil, nil, ErrTLSHandshake
		}
		nc = tc
	}
	deadline, _ := ctx.Deadline()
	_ = nc.SetDeadline(deadline)
	c := &conn{nc: nc, r: bufio.NewReader(nc), keepalive: r.cfg.keepalive,
		nextPing: time.Now().Add(r.cfg.keepalive)}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		_ = nc.Close()
		return nil, nil, ErrBrokerUnavailable
	}
	var options byte
	if r.skipRetained {
		options = subscribeNoRetained
	}
	packets := [][]byte{
		buildConnect(r.clientPrefix+hex.EncodeToString(id[:]), r.cfg.username, r.cfg.password, r.cfg.keepalive),
		buildSubscribeOptions(1, []string{r.topic}, options),
	}
	var pending *Publish
	for i, packet := range packets {
		if err := c.write(packet); err != nil {
			_ = nc.Close()
			return nil, nil, ErrBrokerUnavailable
		}
		kind, flags, body, err := readPacketMax(c.r, r.packetLimit())
		// MQTT 5 section 3.8.4 permits PUBLISH before SUBACK. Retain at most
		// one matching payload, and release it only after a successful ACK.
		for i == 1 && err == nil && kind == pktPUBLISH {
			pub, parseErr := parsePublish(flags, body)
			if parseErr != nil || len(pub.Payload) > r.maxPayload {
				_ = nc.Close()
				return nil, nil, ErrEarlyPublication
			}
			if pending == nil && pub.Topic == r.topic {
				pending = &Publish{Topic: r.topic, Retain: pub.Retain, Payload: append([]byte(nil), pub.Payload...)}
			}
			kind, flags, body, err = readPacketMax(c.r, r.packetLimit())
		}
		if errors.Is(err, errRemainingTooLarge) {
			_ = nc.Close()
			if i == 1 {
				return nil, nil, ErrEarlyPublication
			}
			return nil, nil, ErrBrokerRefused
		}
		if err != nil {
			_ = nc.Close()
			return nil, nil, ErrBrokerUnavailable
		}
		if i == 0 && kind == pktCONNACK && flags == 0 && len(body) >= 2 && body[1] != 0 {
			_ = nc.Close()
			return nil, nil, connackError(body[1])
		}
		valid := flags == 0
		if i == 0 {
			valid = valid && kind == pktCONNACK && len(body) >= 3 && body[0] == 0 && body[1] == 0
		} else {
			valid = valid && kind == pktSUBACK && len(body) >= 4 && body[0] == 0 && body[1] == 1
			if valid {
				length, consumed, e := decodeVBI(body, 2)
				at := 2 + consumed + length
				valid = e == nil && at == len(body)-1 && body[at] <= 2
			}
		}
		if !valid {
			_ = nc.Close()
			return nil, nil, ErrBrokerRefused
		}
	}
	return c, pending, nil
}
