// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// A real TCP MQTT 5 fixture. It checks CONNECT and the one exact SUBSCRIBE,
// then supplies the retained sensor measurement to the production reader.
func temperatureBroker(t *testing.T, payload []byte) string {
	return temperatureBrokerReplies(t, payload, []byte{0x20, 0x03, 0x00, 0x00, 0x00}, []byte{0x90, 0x04, 0x00, 0x01, 0x00, 0x00})
}

func temperatureBrokerReplies(t *testing.T, payload, connack, suback []byte) string {
	return temperatureBrokerTopic(t, payload, connack, suback, "plant/temperature")
}

func temperatureBrokerTopic(t *testing.T, payload, connack, suback []byte, publishTopic string) string {
	return temperatureBrokerOrder(t, payload, connack, suback, publishTopic, false)
}

func temperatureBrokerOrder(t *testing.T, payload, connack, suback []byte, publishTopic string, early bool) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() {
		_ = l.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("MQTT fixture did not stop")
		}
	})
	go func() {
		defer close(done)
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = l.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		r := bufio.NewReader(c)
		kind, _, body, err := readPacket(r)
		if err != nil || kind != 1 || !bytes.Contains(body, []byte("MQTT")) {
			t.Errorf("temperature reader did not CONNECT: kind=%d err=%v", kind, err)
			return
		}
		_, _ = c.Write(connack)
		kind, _, body, err = readPacket(r)
		if err != nil && connack[3] != 0 {
			return // A refused connection must stop before subscribing.
		}
		if err != nil || kind != 8 || !bytes.Contains(body, []byte("plant/temperature")) {
			t.Errorf("temperature reader did not subscribe to the configured sensor: kind=%d err=%v", kind, err)
			return
		}
		if !early {
			_, _ = c.Write(suback)
		}
		// MQTT 5 PUBLISH: topic's two-byte length, topic, empty properties, payload.
		topic := []byte(publishTopic)
		packet := binary.BigEndian.AppendUint16(nil, uint16(len(topic)))
		packet = append(packet, topic...)
		packet = append(packet, 0)
		packet = append(packet, payload...)
		_, _ = c.Write(frame(3, 1, packet))
		if early {
			_, _ = c.Write(suback)
		}
		// Wait for the reader to close, without leaving a fixture goroutine behind.
		_, _ = r.ReadByte()
	}()
	return "tcp://" + l.Addr().String()
}

func TestTemperatureReaderEarlyPublicationWaitsForSuccessfulAck(t *testing.T) {
	fresh := `{"celsius":21.75,"observed_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	for _, test := range []struct {
		name, payload, topic string
		suback               []byte
		accepted             bool
	}{
		{"successful ACK", fresh, "plant/temperature", []byte{0x90, 4, 0, 1, 0, 0}, true},
		{"refused ACK", fresh, "plant/temperature", []byte{0x90, 4, 0, 1, 0, 0x87}, false},
		{"missing ACK", fresh, "plant/temperature", nil, false},
		{"wrong packet identifier", fresh, "plant/temperature", []byte{0x90, 4, 0, 2, 0, 0}, false},
		{"stale value", `{"celsius":21.75,"observed_at":"2000-01-01T00:00:00Z"}`, "plant/temperature", []byte{0x90, 4, 0, 1, 0, 0}, false},
		{"oversized value", strings.Repeat("x", 4097), "plant/temperature", []byte{0x90, 4, 0, 1, 0, 0}, false},
		{"other topic", fresh, "another/sensor", []byte{0x90, 4, 0, 1, 0, 0}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, err := NewTemperatureReader(sdk.Config{Settings: map[string]string{
				"broker": temperatureBrokerOrder(t, []byte(test.payload), []byte{0x20, 3, 0, 0, 0}, test.suback, test.topic, true),
				"topic":  "plant/temperature", "timeout": "100ms",
			}})
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			got, err := reader.Read(t.Context())
			if (err == nil) != test.accepted || test.accepted && got.Celsius != 21.75 || time.Since(started) > time.Second {
				t.Fatalf("early publication escaped ACK/freshness/deadline checks: value=%v error=%v", got.Celsius, err)
			}
		})
	}
}

func TestTemperatureReaderIgnoresOtherTopicsUnderDeadline(t *testing.T) {
	payload := []byte(`{"celsius":99,"observed_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`)
	broker := temperatureBrokerTopic(t, payload, []byte{0x20, 0x03, 0, 0, 0}, []byte{0x90, 4, 0, 1, 0, 0}, "another/sensor")
	reader, err := NewTemperatureReader(sdk.Config{Settings: map[string]string{"broker": broker, "topic": "plant/temperature", "timeout": "100ms"}})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := reader.Read(t.Context()); err == nil || time.Since(started) > time.Second {
		t.Fatalf("other topic escaped or the read deadline did not stop it: %v", err)
	}
}

func TestTemperatureReaderHonorsBrokerRefusals(t *testing.T) {
	payload := []byte(`{"celsius":21.75,"observed_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`)
	for _, test := range []struct {
		name            string
		connack, suback []byte
	}{
		{"connection unauthorized", []byte{0x20, 0x03, 0x00, 0x87, 0x00}, []byte{0x90, 0x04, 0x00, 0x01, 0x00, 0x00}},
		{"subscription unauthorized", []byte{0x20, 0x03, 0x00, 0x00, 0x00}, []byte{0x90, 0x04, 0x00, 0x01, 0x00, 0x87}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, err := NewTemperatureReader(sdk.Config{Settings: map[string]string{
				"broker": temperatureBrokerReplies(t, payload, test.connack, test.suback), "topic": "plant/temperature",
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Read(t.Context()); err == nil {
				t.Fatal("a sensor value escaped after the broker refused access")
			}
		})
	}
}

func TestTemperatureReaderFreshSensorValues(t *testing.T) {
	observed := time.Now().Add(-time.Second).UTC().Truncate(time.Millisecond)
	payload, err := json.Marshal(map[string]any{"celsius": 21.75, "observed_at": observed.Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewTemperatureReader(sdk.Config{Settings: map[string]string{
		"broker": temperatureBroker(t, payload), "topic": "plant/temperature",
	}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := reader.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if value.Celsius != 21.75 || !value.ObservedAt.Equal(observed) || !value.ReceivedAt.After(observed) {
		t.Fatalf("sensor value or measurement time changed: %+v", value)
	}
	// A second read opens a new connection; a dead broker must not return a cache.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := reader.Read(ctx); err == nil {
		t.Fatal("broker outage returned the previous measurement")
	}
}

func TestTemperatureReaderRefusesInvalidOrOldMeasurements(t *testing.T) {
	for _, test := range []struct {
		name, payload string
	}{
		{"old retained value", `{"celsius":21.75,"observed_at":"2000-01-01T00:00:00Z"}`},
		{"future value", `{"celsius":21.75,"observed_at":"2100-01-01T00:00:00Z"}`},
		{"missing time", `{"celsius":21.75}`},
		{"missing value", `{"observed_at":"2000-01-01T00:00:00Z"}`},
		{"null value", `{"celsius":null,"observed_at":"2000-01-01T00:00:00Z"}`},
		{"non-finite value", `{"celsius":1e999,"observed_at":"2000-01-01T00:00:00Z"}`},
		{"malformed secret-bearing data", "PRIVATE-SENSOR-CANARY"},
		{"oversized payload", strings.Repeat("x", 4097)},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader, err := NewTemperatureReader(sdk.Config{Settings: map[string]string{
				"broker": temperatureBroker(t, []byte(test.payload)), "topic": "plant/temperature",
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Read(t.Context()); err == nil || strings.Contains(err.Error(), "PRIVATE-SENSOR-CANARY") {
				t.Fatalf("invalid sensor value was accepted or reflected: %v", err)
			}
		})
	}
}

func TestTemperatureMCPReadsOnlyTheConfiguredSensor(t *testing.T) {
	payload := `{"celsius":21.75,"observed_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	reader, err := NewTemperatureReader(sdk.Config{Settings: map[string]string{
		"broker": temperatureBroker(t, []byte(payload)), "topic": "plant/temperature",
	}})
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_temperature","arguments":{"topic":"other/sensor"}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_temperature","arguments":{}}}
`)
	var output bytes.Buffer
	if err := reader.ServeTemperatureMCP(t.Context(), input, &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("notifications or requests produced the wrong number of responses: %d", len(lines))
	}
	var listing struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Annotations map[string]bool `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(lines[1]), &listing) != nil || len(listing.Result.Tools) != 1 ||
		listing.Result.Tools[0].Name != "read_temperature" || !listing.Result.Tools[0].Annotations["readOnlyHint"] {
		t.Fatal("MCP catalogue did not declare the read-only sensor tool")
	}
	var denial struct {
		Error struct{ Code int } `json:"error"`
	}
	if json.Unmarshal([]byte(lines[2]), &denial) != nil || denial.Error.Code != -32602 {
		t.Fatal("a caller-selected topic was not refused before reading")
	}
	var response struct {
		Result struct {
			Value   Temperature `json:"structuredContent"`
			IsError bool        `json:"isError"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(lines[3]), &response) != nil || response.Result.IsError || response.Result.Value.Celsius != 21.75 {
		t.Fatal("MCP did not release the configured fresh value")
	}
}

func TestTemperatureReaderConfigurationDoesNotReflectSecrets(t *testing.T) {
	for _, settings := range []map[string]string{
		{"broker": "tcp://fixture:1883", "topic": "plant/#"},
		{"broker": "tcp://user:PRIVATE-CONFIG-CANARY@fixture:1883", "topic": "plant/temperature"},
		{"broker": "PRIVATE-CONFIG-CANARY://fixture:1883", "topic": "plant/temperature"},
		{"broker": "tcp://fixture:1883", "topic": "plant/temperature", "timeout": "PRIVATE-CONFIG-CANARY"},
		{"broker": "tcp://fixture:1883", "topic": "plant/temperature", "timeout": "1m"},
	} {
		if _, err := NewTemperatureReader(sdk.Config{Settings: settings}); err == nil || strings.Contains(err.Error(), "PRIVATE-CONFIG-CANARY") {
			t.Fatalf("invalid configuration was accepted or reflected: %v", err)
		}
	}
}
