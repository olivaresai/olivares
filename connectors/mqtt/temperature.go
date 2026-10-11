// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/sdk"
)

// Temperature is a sensor measurement, not a topology observation. It is released
// only to the caller; the source observer and event bus never receive its payload.
type Temperature struct {
	Celsius    float64   `json:"celsius"`
	ObservedAt time.Time `json:"observed_at"`
	ReceivedAt time.Time `json:"received_at"`
}

// TemperatureReader reads one configured MQTT topic. Each call opens a new clean
// session, so an outage cannot return a previously cached sensor measurement.
type TemperatureReader struct {
	cfg     config
	topic   string
	maxAge  time.Duration
	timeout time.Duration
}

// NewTemperatureReader uses the existing MQTT broker/TLS configuration plus an
// exact topic and max_age (default 30s). timeout bounds the entire read, at most 5s.
func NewTemperatureReader(cfg sdk.Config) (*TemperatureReader, error) {
	topic := cfg.Get("topic")
	if topic == "" || len(topic) > 65535 || !utf8.ValidString(topic) || strings.ContainsAny(topic, "+#\x00") {
		return nil, errors.New("mqtt temperature: configure one exact topic")
	}
	if u, err := url.Parse(cfg.Get("broker")); err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("mqtt temperature: invalid broker configuration")
	}
	maxAge, timeout := 30*time.Second, 5*time.Second
	for key, dst := range map[string]*time.Duration{"max_age": &maxAge, "timeout": &timeout} {
		if raw := cfg.Get(key); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil || d <= 0 {
				return nil, errors.New("mqtt temperature: invalid freshness or timeout configuration")
			}
			*dst = d
		}
	}
	if timeout > 5*time.Second {
		return nil, errors.New("mqtt temperature: timeout must be at most 5s")
	}
	c, err := loadConfig(cfg)
	if err != nil {
		// Configuration errors can contain broker URLs or TLS paths. Do not reflect
		// them through the adapter's protocol or diagnostic output.
		return nil, errors.New("mqtt temperature: invalid broker or TLS configuration")
	}
	if len(c.username) > 65535 || len(c.password) > 65535 {
		return nil, errors.New("mqtt temperature: invalid credential configuration")
	}
	c.topics = []string{topic}
	return &TemperatureReader{cfg: c, topic: topic, maxAge: maxAge, timeout: timeout}, nil
}

// Read returns a fresh producer-timestamped measurement. Old retained messages,
// missing timestamps and future measurements are errors; receipt never refreshes them.
func (r *TemperatureReader) Read(ctx context.Context) (Temperature, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	c, pending, err := r.connect(ctx)
	if err != nil {
		return Temperature{}, errors.New("mqtt temperature: broker unavailable")
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.nc.Close() })
	defer stop()
	for {
		var pub Publish
		err := ctx.Err()
		if err == nil {
			if pending != nil {
				pub, pending = *pending, nil
			} else {
				pub, err = c.Read(ctx)
			}
		}
		if err != nil {
			return Temperature{}, errors.New("mqtt temperature: sensor unavailable")
		}
		if pub.Topic != r.topic {
			continue
		}
		if len(pub.Payload) > 4096 {
			return Temperature{}, errors.New("mqtt temperature: invalid sensor measurement")
		}
		var payload struct {
			Celsius    *float64 `json:"celsius"`
			ObservedAt string   `json:"observed_at"`
		}
		if json.Unmarshal(pub.Payload, &payload) != nil || payload.Celsius == nil {
			return Temperature{}, errors.New("mqtt temperature: invalid sensor measurement")
		}
		observed, err := time.Parse(time.RFC3339Nano, payload.ObservedAt)
		if err != nil {
			return Temperature{}, errors.New("mqtt temperature: invalid measurement time")
		}
		received := time.Now().UTC()
		if observed.After(received) || received.Sub(observed) > r.maxAge {
			return Temperature{}, errors.New("mqtt temperature: measurement is stale or future-dated")
		}
		return Temperature{Celsius: *payload.Celsius, ObservedAt: observed.UTC(), ReceivedAt: received}, nil
	}
}

// Reuse the source's MQTT framing, packet builders and connection reader. This
// handshake adds one whole-call deadline and checks broker refusal reason codes.
func (r *TemperatureReader) connect(ctx context.Context) (*conn, *Publish, error) {
	nc, err := (&net.Dialer{}).DialContext(ctx, "tcp", r.cfg.host)
	if err != nil {
		return nil, nil, err
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
			return nil, nil, err
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
		return nil, nil, err
	}
	packets := [][]byte{
		buildConnect("olivares-temperature-"+hex.EncodeToString(id[:]), r.cfg.username, r.cfg.password, r.cfg.keepalive),
		buildSubscribe(1, []string{r.topic}),
	}
	var pending *Publish
	for i, packet := range packets {
		if err := c.write(packet); err != nil {
			_ = nc.Close()
			return nil, nil, err
		}
		kind, flags, body, err := readPacket(c.r)
		// MQTT 5 section 3.8.4 permits PUBLISH before SUBACK. Retain at most
		// one matching payload, and release it only after a successful ACK.
		for i == 1 && err == nil && kind == pktPUBLISH {
			pub, parseErr := parsePublish(flags, body)
			if parseErr != nil || len(pub.Payload) > 4096 {
				_ = nc.Close()
				return nil, nil, errors.New("mqtt temperature: invalid early publication")
			}
			if pending == nil && pub.Topic == r.topic {
				pending = &Publish{Topic: r.topic, Payload: append([]byte(nil), pub.Payload...)}
			}
			kind, flags, body, err = readPacket(c.r)
		}
		valid := err == nil && flags == 0
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
			return nil, nil, errors.New("mqtt temperature: broker refused subscription")
		}
	}
	return c, pending, nil
}
