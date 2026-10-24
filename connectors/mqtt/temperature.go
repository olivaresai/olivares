// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mqtt

import (
	"context"
	"encoding/json"
	"errors"
	"time"

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
	reader *PublishReader
	maxAge time.Duration
}

// NewTemperatureReader uses the existing MQTT broker/TLS configuration plus an
// exact topic and max_age (default 30s). timeout bounds the entire read, at most 5s.
func NewTemperatureReader(cfg sdk.Config) (*TemperatureReader, error) {
	c, err := readerConfig(cfg, cfg.Get("topic"), "mqtt temperature")
	if err != nil {
		return nil, err
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
	return &TemperatureReader{maxAge: maxAge, reader: &PublishReader{cfg: c, topic: cfg.Get("topic"),
		maxPayload: DefaultPublishPayload, timeout: timeout, clientPrefix: "olivares-temperature-"}}, nil
}

// Read returns a fresh producer-timestamped measurement. Old retained messages,
// missing timestamps and future measurements are errors; receipt never refreshes them.
func (r *TemperatureReader) Read(ctx context.Context) (Temperature, error) {
	pub, err := r.reader.Read(ctx)
	switch {
	case errors.Is(err, ErrNoPublish), errors.Is(err, ErrConnectionLost):
		return Temperature{}, errors.New("mqtt temperature: sensor unavailable")
	case errors.Is(err, ErrPayloadTooLarge):
		return Temperature{}, errors.New("mqtt temperature: invalid sensor measurement")
	case errors.Is(err, ErrBrokerCredentials):
		return Temperature{}, errors.New("mqtt temperature: broker refused the credentials or authorization")
	case errors.Is(err, ErrBrokerRefused):
		return Temperature{}, errors.New("mqtt temperature: broker refused the connection or subscription")
	case err != nil:
		return Temperature{}, errors.New("mqtt temperature: broker unavailable")
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
