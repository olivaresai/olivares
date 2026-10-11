// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

// conversationHistory uses the tool's persisted conversation, not a second
// transcript store. The caller has already authorized and audited the attach.
// It never resumes a thread, sends a turn, or opens a provider credential.
func (m *Module) conversationHistory(ctx context.Context, tenant model.TenantID, rec model.Record) (line string, err error) {
	threadID := rec.String(colClaudeSessionID)
	if rec.String(colRunProfileDriver) != providerDriverCodex || threadID == "" {
		return "", nil
	}
	secrets, decodeErr := decodeSecretEnv(rec.String(colRunSecretEnv))
	if decodeErr != nil || len(secrets) != 0 || rec.String(colRunGitRead) != "" {
		// Current vault values cannot redact secrets from earlier generations
		// after rotation, nor expired Git read tokens from earlier launches.
		// The already-redacted live ring remains available.
		return "", errors.New("stored conversation is unavailable because historical secret redaction cannot be proven")
	}
	home, _, err := m.revalidateStoredProfile(ctx, tenant, rec)
	if err != nil {
		return "", errors.New("stored conversation is unavailable because its provider home is no longer authorized")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	args := NewCodexDriver().LaunchArgs(DriverLaunch{})
	args = append([]string{"-c", "features.api_key_model_discovery=false"}, args...)
	spec := LaunchSpec{Program: m.driverProgram(NewCodexDriver()), Args: args,
		Dir: home.UserHome, Env: toSessionEnv(cliruntime.HomeEnv(home.Driver, home.UserHome, home.ConfigHome)),
		Isolation: IsolationNative, WaitDelay: time.Second,
		Confinement: m.sessionConfinement("", &home, PresetNone), NetworkPolicy: &egress.Policy{Offline: true}}
	proc, err := m.rt.Runner.Launch(ctx, spec)
	if err != nil {
		return "", errors.New("stored conversation reader could not be started")
	}
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if proc.Stop(stopCtx) != nil {
			line, err = "", errors.New("stored conversation reader could not be stopped")
		}
	}()
	line, err = readCodexConversation(ctx, proc, threadID)
	if err != nil {
		return "", errors.New("stored conversation could not be read; live output remains available where bridged")
	}
	if _, _, err = m.revalidateStoredProfile(ctx, tenant, rec); err != nil {
		return "", errors.New("stored conversation provider home authorization changed during the read")
	}
	return line, nil
}

func readCodexConversation(ctx context.Context, proc Process, threadID string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	conn := newRPCConn(proc.Send, false)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				conn.close(ctx.Err())
				return
			case frame, ok := <-proc.Output():
				if !ok {
					conn.close(errors.New("conversation output ended"))
					return
				}
				if frame.Stream == streamStdout {
					conn.deliver(frame.Data)
				}
			}
		}
	}()
	defer func() { cancel(); conn.close(errors.New("conversation reader closed")); <-done }()
	if _, err := conn.call(ctx, codexMethodInitialize, codexInitializeParams{ClientInfo: codexClientInfo{Name: "olivares-conversation", Version: "1"}}, 10*time.Second); err != nil {
		return "", err
	}
	if err := conn.notify(ctx, codexMethodInitialized, nil); err != nil {
		return "", err
	}
	raw, err := conn.call(ctx, "thread/read", struct {
		ThreadID     string `json:"threadId"`
		IncludeTurns bool   `json:"includeTurns"`
	}{threadID, true}, 10*time.Second)
	if err != nil {
		return "", err
	}
	var response struct {
		Thread struct {
			codexThread
			Turns []json.RawMessage `json:"turns"`
		} `json:"thread"`
	}
	if len(raw) > maxOutputLine || json.Unmarshal(raw, &response) != nil || response.Thread.ID != threadID || response.Thread.ParentThreadID != nil || response.Thread.AgentRole != nil || response.Thread.Turns == nil {
		return "", errors.New("invalid stored root conversation")
	}
	for _, rawTurn := range response.Thread.Turns {
		var turn struct {
			ID        string                         `json:"id"`
			Items     api.JSONArray[json.RawMessage] `json:"items"`
			ItemsView string                         `json:"itemsView"`
		}
		if json.Unmarshal(rawTurn, &turn) != nil || turn.ID == "" || turn.Items == nil || (turn.ItemsView != "" && turn.ItemsView != "full") {
			return "", errors.New("stored conversation is incomplete")
		}
	}
	// Exclude path, authentication, provider configuration and other thread
	// metadata. Only native turn items enter the existing conversation inspector.
	data, err := json.Marshal(struct {
		Type     string            `json:"type"`
		ThreadID string            `json:"thread_id"`
		Turns    []json.RawMessage `json:"turns"`
	}{"olivares_conversation", threadID, response.Thread.Turns})
	if len(data) > maxOutputLine {
		return "", errors.New("stored conversation exceeded its display response bound")
	}
	return string(data), err
}
