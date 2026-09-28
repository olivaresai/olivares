// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package netguard owns NetworkManager changes and their durable uncertainty.
package netguard

import (
	"errors"
	"strings"
	"time"
)

const ProfileInterface = "org.freedesktop.NetworkManager.Settings.Connection"
const CallNotificationAfter = 10 * time.Second

// Method identifies an effect class, including the two different Update2 fences.
type Method string

const (
	CheckpointCreate   Method = "checkpoint_create"
	UpdateInMemory     Method = "update_in_memory"
	Reapply            Method = "reapply"
	UpdateToDisk       Method = "update_to_disk"
	CheckpointDestroy  Method = "checkpoint_destroy"
	CheckpointRollback Method = "checkpoint_rollback"
	ReloadConnections  Method = "reload_connections"
	LoadConnections    Method = "load_connections"
	RestoreActivation  Method = "restore_activation"
)

// ProcessIdentity is the original process, never a replacement owning the same name.
type ProcessIdentity struct {
	Unique    string `json:"unique_name"`
	BootID    string `json:"boot_id"`
	PID       int    `json:"pid"`
	StartTime uint64 `json:"proc_start_time_ticks"`
}

// ProcessFacts must come from the credential ProcessFD and host procfs while it lives.
// A cached numeric ProcessID cannot establish FromCredentials.
type ProcessFacts struct {
	FromCredentials, WholeProcess, Alive, Visible bool
	PID, TGID                                     int
	StartTime                                     uint64
	BootID, Unique                                string
}

func IdentifyProcess(f ProcessFacts) (ProcessIdentity, error) {
	if !f.FromCredentials || !f.WholeProcess || !f.Alive || !f.Visible || f.PID <= 0 || f.PID != f.TGID || f.StartTime == 0 || f.BootID == "" || !strings.HasPrefix(f.Unique, ":") {
		return ProcessIdentity{}, errors.New("network_process_identity_unavailable")
	}
	return ProcessIdentity{Unique: f.Unique, BootID: f.BootID, PID: f.PID, StartTime: f.StartTime}, nil
}

// Death distinguishes unprovable visibility from a demonstrated exit.
type Death uint8

const (
	DeathUnknown Death = iota
	DeathAlive
	DeathProven
)

// CallRecord is fsynced before the transport is allowed to send its message.
// ArgumentsDigest contains no raw settings or secret material.
type CallRecord struct {
	EffectBootID         string          `json:"effect_boot_id,omitempty"`
	BusID                string          `json:"bus_id"`
	ConnectionGeneration string          `json:"connection_generation"`
	Caller               string          `json:"caller_unique_name"`
	Serial               uint32          `json:"serial"`
	Target               ProcessIdentity `json:"target"`
	Method               Method          `json:"method"`
	Object               string          `json:"object"`
	ArgumentsDigest      string          `json:"arguments_digest"`
	OperationID          string          `json:"operation_id"`
	WindowGeneration     string          `json:"window_generation"`
	Attempt              uint32          `json:"attempt"`
	SentAt               time.Duration   `json:"intent_boottime_ns"`
	Settled              bool            `json:"settled"`
	Success              bool            `json:"success"`
	Settlement           string          `json:"settlement,omitempty"`
	EffectKnownAt        time.Duration   `json:"effect_known_boottime_ns,omitempty"`
}

// Reply is an incoming raw message observed on the same private connection. Transport
// cancellation and godbus Call.Err are deliberately not a Reply.
type Reply struct {
	BusID, ConnectionGeneration, Sender string
	ReplySerial                         uint32
	Completed, Success                  bool
	Event                               string
	Body                                []any
}

func (c CallRecord) SettledBy(r Reply, death Death) bool {
	return death == DeathProven || r.Completed && c.Serial != 0 && r.ReplySerial == c.Serial && r.Sender == c.Target.Unique && r.BusID == c.BusID && r.ConnectionGeneration == c.ConnectionGeneration
}
func (c CallRecord) NotificationDue(now time.Duration) bool {
	return !c.Settled && now >= c.SentAt+CallNotificationAfter
}

// CanOvertake grants only empty-settings Reapply its applied-version fence. No
// settings, persistence, checkpoint or restoration call can be overtaken while pending.
func CanOvertake(c CallRecord, sentVersion, currentVersion uint64) bool {
	return c.Method == Reapply && sentVersion > 0 && currentVersion > sentVersion
}

func ProfileVersion(v any) (uint64, error) {
	n, ok := v.(uint64)
	if !ok || n == 0 {
		return 0, errors.New("profile_version_unavailable")
	}
	return n, nil
}
func VersionBracket(before, after uint64) error {
	if before == 0 || after == 0 {
		return errors.New("profile_version_unavailable")
	}
	if before != after {
		return errors.New("profile_version_changed")
	}
	return nil
}
