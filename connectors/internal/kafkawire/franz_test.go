// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kafkawire

import "testing"

func TestOptsMapping(t *testing.T) {
	// Seed brokers only.
	opts, err := Opts(Config{Brokers: []string{"b:9092"}})
	if err != nil {
		t.Fatalf("opts: %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("expected only seed-brokers, got %d", len(opts))
	}
	// Each SASL mechanism adds one option.
	for _, m := range []string{"plain", "scram-sha-256", "scram-sha-512"} {
		o, err := Opts(Config{Brokers: []string{"b:9092"}, SASLMech: m, SASLUser: "u", SASLPass: "p"})
		if err != nil {
			t.Fatalf("opts %s: %v", m, err)
		}
		if len(o) != 2 {
			t.Fatalf("mechanism %s should add a sasl opt, got %d", m, len(o))
		}
	}
	if _, err := Opts(Config{Brokers: []string{"b:9092"}, SASLMech: "bogus"}); err == nil {
		t.Fatal("unsupported mechanism must error")
	}
}
