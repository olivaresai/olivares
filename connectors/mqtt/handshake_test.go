// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// This broker speaks MQTT 5 framing over TCP rather than replacing mqttClient.
func TestDialRejectsFailedMQTTHandshake(t *testing.T) {
	cases := []struct {
		name            string
		connack, suback []byte
		wantError       bool
	}{
		{"accepted", []byte{0, 0, 0}, []byte{0, 1, 0, 0}, false},
		{"authentication refused", []byte{0, 0x86, 0}, nil, true},
		{"malformed CONNACK", []byte{0}, nil, true},
		{"invalid clean-start session", []byte{1, 0, 0}, nil, true},
		{"CONNACK property overrun", []byte{0, 0, 1}, nil, true},
		{"CONNACK trailing payload", []byte{0, 0, 0, 0}, nil, true},
		{"accepted properties", []byte{0, 0, 4, 0x1f, 0, 1, 'x'}, []byte{0, 1, 4, 0x1f, 0, 1, 'x', 0}, false},
		{"SUBACK property overrun", []byte{0, 0, 0}, []byte{0, 1, 8, 0}, true},
		{"unexpected granted QoS", []byte{0, 0, 0}, []byte{0, 1, 0, 1}, true},
		{"subscription refused", []byte{0, 0, 0}, []byte{0, 1, 0, 0x87}, true},
		{"wrong packet identifier", []byte{0, 0, 0}, []byte{0, 2, 0, 0}, true},
		{"missing reason", []byte{0, 0, 0}, []byte{0, 1, 0}, true},
		{"extra reason", []byte{0, 0, 0}, []byte{0, 1, 0, 0, 0}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				nc, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer nc.Close()
				_ = nc.SetDeadline(time.Now().Add(2 * time.Second))
				r := bufio.NewReader(nc)
				if _, _, _, err := readPacket(r); err != nil {
					done <- err
					return
				}
				if _, err := nc.Write(frame(pktCONNACK, 0, tc.connack)); err != nil {
					done <- err
					return
				}
				if tc.suback != nil {
					if _, _, _, err := readPacket(r); err != nil {
						done <- err
						return
					}
					_, err = nc.Write(frame(pktSUBACK, 0, tc.suback))
				}
				done <- err
			}()
			client, err := dialClient(config{host: listener.Addr().String(), clientID: "canary-observer", topics: []string{"fixture/#"}, keepalive: time.Second, timeout: time.Second})
			if client != nil {
				_ = client.Close()
			}
			if (err != nil) != tc.wantError {
				t.Errorf("handshake rejection=%v, want %v", err != nil, tc.wantError)
			}
			if tc.suback == nil && (err == nil || !strings.Contains(err.Error(), "connack")) {
				t.Error("CONNACK failure was not classified before subscribing")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A refused CONNACK sorts into the class the operator acts on: credentials and authorization,
// or an unavailable or busy broker. Each class has one fixed sentence.
func TestDialSortsCONNACKRefusalsByClass(t *testing.T) {
	cases := []struct {
		reason  byte
		want    error
		refused bool // errors.Is(err, ErrBrokerRefused): the matcher every caller of the sentinel already uses
	}{
		{0x86, ErrBrokerCredentials, true},
		{0x87, ErrBrokerCredentials, true},
		{0x88, ErrBrokerUnavailable, false},
		{0x89, ErrBrokerUnavailable, false},
		{0x97, ErrBrokerUnavailable, false},
		{0x80, ErrBrokerRefused, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("reason 0x%02x", tc.reason), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				nc, err := listener.Accept()
				if err != nil {
					return
				}
				defer nc.Close()
				_ = nc.SetDeadline(time.Now().Add(2 * time.Second))
				r := bufio.NewReader(nc)
				_, _, _, _ = readPacket(r)
				_, _ = nc.Write(frame(pktCONNACK, 0, []byte{0, tc.reason, 0}))
			}()
			client, err := dialClient(config{host: listener.Addr().String(), clientID: "canary-observer", topics: []string{"fixture/#"}, keepalive: time.Second, timeout: time.Second})
			if client != nil {
				_ = client.Close()
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if got := errors.Is(err, ErrBrokerRefused); got != tc.refused {
				t.Fatalf("errors.Is(%v, ErrBrokerRefused) = %v, want %v", err, got, tc.refused)
			}
			if want := fmt.Sprintf("reason 0x%02x", tc.reason); !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not carry %q", err, want)
			}
		})
	}
}
