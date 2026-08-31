// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// ReplyEventKind is the closed set of A2A stream/push values that carry
// response content rather than Task lifecycle authority.
type ReplyEventKind string

const (
	ReplyEventMessage  ReplyEventKind = "message"
	ReplyEventArtifact ReplyEventKind = "artifact"
)

// ReplyEvent is the bounded connector-neutral projection consumed by the
// composition root. Text has already been reduced to bounded plain text. Data
// and file Parts expose only a sanitized reference plus their canonical hash.
// Digest commits to the complete canonical Message or Artifact wire object.
type ReplyEvent struct {
	Kind       ReplyEventKind
	TaskID     string
	MessageID  string
	ContextID  string
	ArtifactID string
	Parts      []MessageResultPart
	Digest     string
	LastChunk  bool
	Sender     string

	// ReplayID and ReplayExpiresAt are verified JWT claims passed only to the
	// durable composition callback. They must never be logged or persisted raw.
	ReplayID        string    `json:"-"`
	ReplayExpiresAt time.Time `json:"-"`
}

func projectMessageReplyEvent(raw json.RawMessage, sender string) (ReplyEvent, error) {
	var message rpcResult
	if err := json.Unmarshal(raw, &message); err != nil {
		return ReplyEvent{}, fmt.Errorf("a2a: decode reply Message: %w", err)
	}
	result, err := messageReply(message)
	if err != nil {
		return ReplyEvent{}, err
	}
	digest, err := canonicalReplyDigest(raw)
	if err != nil {
		return ReplyEvent{}, err
	}
	return ReplyEvent{
		Kind: ReplyEventMessage, TaskID: result.MessageTaskID, MessageID: result.MessageID,
		ContextID: result.ContextID, Parts: result.MessageParts,
		Digest: digest, Sender: sender,
	}, nil
}

func projectArtifactReplyEvent(raw json.RawMessage, sender string) (ReplyEvent, error) {
	var update struct {
		TaskID    string `json:"taskId"`
		ContextID string `json:"contextId"`
		Artifact  struct {
			ArtifactID string            `json:"artifactId"`
			Parts      []json.RawMessage `json:"parts"`
		} `json:"artifact"`
		LastChunk bool `json:"lastChunk"`
	}
	if err := json.Unmarshal(raw, &update); err != nil {
		return ReplyEvent{}, fmt.Errorf("a2a: decode artifact update: %w", err)
	}
	if !validReplyIdentifier(update.TaskID) || !validReplyIdentifier(update.ContextID) ||
		!validReplyIdentifier(update.Artifact.ArtifactID) {
		return ReplyEvent{}, fmt.Errorf("a2a: artifact update has invalid identifiers")
	}
	parts, err := projectMessageResultParts(update.Artifact.Parts)
	if err != nil {
		return ReplyEvent{}, fmt.Errorf("a2a: artifact update: %w", err)
	}
	digest, err := canonicalReplyDigest(raw)
	if err != nil {
		return ReplyEvent{}, err
	}
	return ReplyEvent{
		Kind: ReplyEventArtifact, TaskID: update.TaskID,
		ContextID: update.ContextID, ArtifactID: update.Artifact.ArtifactID,
		Parts: parts, Digest: digest, LastChunk: update.LastChunk, Sender: sender,
	}, nil
}

func canonicalReplyDigest(raw json.RawMessage) (string, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if len(raw) == 0 || decoder.Decode(&value) != nil {
		return "", fmt.Errorf("a2a: reply value is malformed")
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > maxMessageResultWireBytes {
		return "", fmt.Errorf("a2a: reply value exceeds its canonical bound")
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}
