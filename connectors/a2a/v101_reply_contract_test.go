// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readV101ReplyFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "v1.0.1", name))
	if err != nil {
		t.Fatalf("read v1.0.1 fixture %q: %v", name, err)
	}
	return raw
}

func TestV101SendMessageResponseFixtures(t *testing.T) {
	t.Run("task", func(t *testing.T) {
		var envelope jsonrpcResponse
		if err := json.Unmarshal(readV101ReplyFixture(t, "send-message-task.json"), &envelope); err != nil {
			t.Fatal(err)
		}
		result, err := resultToTask(envelope.Result)
		if err != nil {
			t.Fatal(err)
		}
		if result.ResultKind != "task" || result.TaskID != "task-v101-1" ||
			result.ContextID != "context-v101-1" || result.State != TaskStateWorking ||
			result.Terminal || result.Interrupt || len(result.MessageParts) != 0 {
			t.Fatalf("Task fixture projection = %+v", result)
		}
	})

	t.Run("direct Message and Parts", func(t *testing.T) {
		var envelope jsonrpcResponse
		if err := json.Unmarshal(readV101ReplyFixture(t, "send-message-direct-message.json"), &envelope); err != nil {
			t.Fatal(err)
		}
		result, err := resultToTask(envelope.Result)
		if err != nil {
			t.Fatal(err)
		}
		if result.ResultKind != "message" || result.MessageID != "message-v101-1" ||
			result.ContextID != "context-v101-1" || !result.Terminal ||
			len(result.MessageDigest) != 64 || len(result.MessageParts) != 4 {
			t.Fatalf("Message fixture projection = %+v", result)
		}
		parts := result.MessageParts
		if parts[0].Kind != "text" || parts[0].Text != "completed" ||
			parts[1].Kind != "data" || !strings.HasPrefix(parts[1].Reference, "a2a-part:") ||
			parts[2].Kind != "file" || parts[2].Reference != "artifact:report-v101-1" ||
			parts[3].Kind != "file" || !strings.HasPrefix(parts[3].Reference, "a2a-part:") {
			t.Fatalf("Part fixture projection = %+v", parts)
		}
	})
}

func TestV101StreamResponseReplyFixtures(t *testing.T) {
	tests := []struct {
		name       string
		fixture    string
		kind       ReplyEventKind
		messageID  string
		artifactID string
		parts      int
		final      bool
	}{
		{"Message", "stream-message.json", ReplyEventMessage, "message-stream-v101-1", "", 1, true},
		{"Artifact", "stream-artifact-update.json", ReplyEventArtifact, "", "artifact-v101-1", 3, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event, ok, err := streamEventFromJSON(readV101ReplyFixture(t, test.fixture))
			if err != nil || !ok || event.Reply == nil {
				t.Fatalf("stream fixture: event=%+v ok=%v err=%v", event, ok, err)
			}
			if event.Reply.Kind != test.kind || event.Reply.MessageID != test.messageID ||
				event.Reply.ArtifactID != test.artifactID || len(event.Reply.Parts) != test.parts ||
				event.Final != test.final || len(event.Reply.Digest) != 64 {
				t.Fatalf("stream reply projection = %+v", event)
			}
			if test.kind == ReplyEventMessage && event.Reply.TaskID != "task-v101-1" {
				t.Fatalf("Message task lineage = %+v", event.Reply)
			}
			for _, part := range event.Reply.Parts {
				if len(part.Digest) != 64 {
					t.Fatalf("part lacks canonical digest: %+v", part)
				}
			}
		})
	}
}
