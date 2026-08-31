// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/modules/voice"
	"github.com/olivaresai/olivares/sdk/event"
)

// envBusConfig selects the distributed event-bus backend. Unset = the
// in-process bus, the default since S02. Set = the path of a JSON file (may be
// CMEK-sealed; it can carry NATS credentials) matching natsbus.Config.
const envBusConfig = "OLIVARES_BUS_CONFIG"

// loadBusConfig resolves the operator's bus backend selection. It belongs to
// the FAIL-BOOT-CLOSED loader family (buildCheckpointKey, not the
// warn-and-degrade one): once the operator declared a distributed bus, any
// error — unreadable file, bad JSON, unknown backend, invalid subject prefix —
// must abort the boot. A node that silently fell back to in-proc would run
// PARTITIONED from the rest of the cluster: publishes that every other node
// sees, subscribers that miss every remote event, no error anywhere (docs/SECURITY-HARDENING.md
// §5 — never a silent gap). nil config = in-proc default (env unset).
func loadBusConfig(getenv func(string) string, log *slog.Logger) (*natsbus.Config, error) {
	path := strings.TrimSpace(getenv(envBusConfig))
	if path == "" {
		return nil, nil
	}
	b, err := readOperatorConfig(path)
	if err != nil {
		return nil, fmt.Errorf("read %s=%s: %w", envBusConfig, path, err)
	}
	var cfg natsbus.Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields() // a typo'd key in a backend selection must not be ignored
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s=%s: %w", envBusConfig, path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	log.Info("event bus: distributed NATS bridge configured (in-proc local fan-out + cross-node at-most-once)",
		"url", cfg.URL, "subject_prefix", cfg.SubjectPrefix)
	return &cfg, nil
}

// busPayloadDecoders extends the bridge's decoder registry with module-owned
// payload types: the codec can re-materialize SDK types itself, but a payload
// struct living under /modules is only importable HERE (the composition root;
// core cannot import modules — license boundary, scripts/check-boundary.sh).
func busPayloadDecoders() map[event.Type]natsbus.PayloadDecoder {
	return map[event.Type]natsbus.PayloadDecoder{
		event.TypeApprovalResolved: func(b []byte) (any, error) {
			var v event.ApprovalResolution
			err := json.Unmarshal(b, &v)
			return v, err
		},
		voice.TypeVoiceTelemetry: func(b []byte) (any, error) {
			var t voice.Telemetry
			err := json.Unmarshal(b, &t)
			return t, err
		},
	}
}
