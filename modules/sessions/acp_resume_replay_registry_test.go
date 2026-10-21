// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// An ACP tool that is resumed replays its stored conversation as session/update
// frames before it answers the resume. The launch's redactor holds the registry
// tokens that launch opened, so a token an earlier launch printed and the vault
// has since rotated is not in it: published, the replay would put the old token on
// the attach stream, the ring and the recorded I/O in plain text.

// rotatedRegistryToken is what the vault holds after the operator rotates the
// registry secret between the two launches.
const rotatedRegistryToken = "npm_ROTATED_registry_token_9876543210"

type acpReplayTool struct {
	name string
	// harness is the production wiring with the tool's fixture child.
	harness func(t *testing.T, opts ...Option) (*Module, model.TenantID, ProviderProfile)
	launch  func(t *testing.T, m *Module, tenant model.TenantID, prof ProviderProfile) (runDTO, error)
	// fixture configures the tool's peer: its conversation id, what a resume replays
	// and where its record goes.
	fixture func(t *testing.T, prof ProviderProfile, conversation string, replayText string, replayed int)
}

func acpReplayTools() []acpReplayTool {
	return []acpReplayTool{
		{
			name: "grok",
			harness: func(t *testing.T, opts ...Option) (*Module, model.TenantID, ProviderProfile) {
				m, _, tenant, prof := grokHarness(t, AuthSourceAccountHome, opts...)
				return m, tenant, prof
			},
			launch: grokLaunch,
			fixture: func(t *testing.T, prof ProviderProfile, conversation, replayText string, replayed int) {
				setGrokFixture(t, prof, grokFixture{
					SessionID: conversation, ReplayUpdates: replayed, ReplayText: replayText,
					RecordPath: filepath.Join(t.TempDir(), "grok-record.json"),
				})
			},
		},
		{
			name: "opencode",
			harness: func(t *testing.T, opts ...Option) (*Module, model.TenantID, ProviderProfile) {
				m, _, tenant, prof := openCodeHarness(t, AuthSourceAccountHome, opts...)
				return m, tenant, prof
			},
			launch: openCodeLaunch,
			fixture: func(t *testing.T, prof ProviderProfile, conversation, replayText string, replayed int) {
				setOpenCodeFixture(t, prof, openCodeFixture{
					SessionID: conversation, ReplayUpdates: replayed, ReplayText: replayText,
					RecordPath: filepath.Join(t.TempDir(), "opencode-record.json"),
				})
			},
		},
	}
}

// replayJourney is one session of a tool: created, stopped, resumed with the
// tool replaying replayText, and read through what an operator reads (the ring
// that GET /runs/{ref}/attach serves) and the recorded I/O.
type replayJourney struct {
	t        *testing.T
	m        *Module
	tenant   model.TenantID
	prof     ProviderProfile
	vault    *fakeVault
	recorder *capturingRecorder
	tool     acpReplayTool
	runRef   string
}

func newReplayJourney(t *testing.T, tool acpReplayTool, declareFirst bool) *replayJourney {
	t.Helper()
	vault := newFakeVault()
	recorder := &capturingRecorder{}
	m, tenant, prof := tool.harness(t,
		WithProviderSecretVault(vault), WithClaudeHookPEP(t.TempDir(), "/bin/true"), WithRecorder(recorder),
		WithLaunchGate(&recordingLaunchGate{dec: LaunchDecision{Allowed: true, RecordIO: true}}))
	vault.values[tenant.String()+"|"+acmeRegistry.Secret] = registryCanary
	j := &replayJourney{t: t, m: m, tenant: tenant, prof: prof, vault: vault, recorder: recorder, tool: tool}
	if declareFirst {
		j.declareRegistry()
	}
	return j
}

func (j *replayJourney) declareRegistry() {
	j.t.Helper()
	registries, programs := []SessionRegistry{acmeRegistry}, []string{}
	if _, err := j.m.PatchProfile(context.Background(), j.tenant, j.prof.Ref, ProfilePatch{
		SessionRegistries: &registries, SessionPrograms: &programs, MayUseSecretEnv: true,
	}); err != nil {
		j.t.Fatalf("declare the profile's registry: %v", err)
	}
}

func (j *replayJourney) create(conversation string) {
	j.t.Helper()
	j.tool.fixture(j.t, j.prof, conversation, "", 0)
	dto, err := j.tool.launch(j.t, j.m, j.tenant, j.prof)
	if err != nil {
		j.t.Fatalf("create: %v", err)
	}
	j.runRef = dto.RunRef
	j.t.Cleanup(func() {
		_, _ = j.m.stopRun(context.Background(), j.tenant, dto.RunRef, "user:u1", model.ActorUser)
	})
}

func (j *replayJourney) stop() {
	j.t.Helper()
	if _, err := j.m.stopRun(context.Background(), j.tenant, j.runRef, "user:u1", model.ActorUser); err != nil {
		j.t.Fatalf("stop: %v", err)
	}
}

func (j *replayJourney) rotate() {
	j.vault.values[j.tenant.String()+"|"+acmeRegistry.Secret] = rotatedRegistryToken
}

// resume continues the conversation with the tool replaying `replayed` updates
// that each say replayText.
func (j *replayJourney) resume(conversation, replayText string, replayed int) {
	j.t.Helper()
	j.tool.fixture(j.t, j.prof, conversation, replayText, replayed)
	if _, err := j.m.resumeRun(context.Background(), j.tenant, j.runRef, "user:u1", model.ActorUser, ""); err != nil {
		j.t.Fatalf("resume: %v", err)
	}
}

func (j *replayJourney) live() *liveRun {
	j.t.Helper()
	lr, ok := j.m.rt.getLive(j.tenant, j.runRef)
	if !ok {
		j.t.Fatal("the run is not live")
	}
	return lr
}

// streamed is every frame the ring (the attach stream) and the recorder hold.
func (j *replayJourney) streamed() (ring, recorded string) {
	j.t.Helper()
	var r, rec strings.Builder
	for _, f := range j.live().ring.readFrom(0).frames {
		r.Write(f.Data)
		r.WriteByte('\n')
	}
	j.recorder.mu.Lock()
	defer j.recorder.mu.Unlock()
	for _, f := range j.recorder.frames {
		rec.Write(f.Data)
		rec.WriteByte('\n')
	}
	return r.String(), rec.String()
}

func (j *replayJourney) historyReplayed() int {
	j.t.Helper()
	counts, ok := j.live().session.(interface{ updateCounts() acpUpdateCounts })
	if !ok {
		j.t.Fatal("the live session keeps no update counts")
	}
	return counts.updateCounts().history
}

// A token an earlier launch printed, replayed after the secret was rotated, is not
// published: not to the attach stream and not to the recorded I/O. Live output of
// the resumed session is unchanged.
func TestACPResume_ReplayedHistoryOfAnEarlierRegistryTokenIsNotPublished(t *testing.T) {
	for _, tool := range acpReplayTools() {
		t.Run(tool.name, func(t *testing.T) {
			j := newReplayJourney(t, tool, true)
			j.create("conv-replay")
			j.stop()
			j.rotate()
			j.resume("conv-replay", "npm whoami //npm.acme.example/:_authToken="+registryCanary, 2)

			// The bridge asks the live session whether it is replaying; a driver that
			// does not answer is published as if there were no replay.
			if _, ok := j.live().session.(replayingSession); !ok {
				t.Fatalf("the live %s session cannot say whether it is replaying", tool.name)
			}
			// The tool did replay, and the driver classified it as history.
			if got := j.historyReplayed(); got != 2 {
				t.Fatalf("history updates = %d, want the 2 the tool replayed", got)
			}
			ring, recorded := j.streamed()
			// One line says that the history was held back, in the stream and in the
			// record, so an empty conversation is not taken for a lost one.
			for what, text := range map[string]string{"attach stream": ring, "recorded I/O": recorded} {
				if got := strings.Count(text, `"code":"history_withheld"`); got != 1 {
					t.Fatalf("the %s holds %d history_withheld notices, want 1: %s", what, got, text)
				}
			}
			if strings.Contains(ring, registryCanary) {
				t.Fatalf("the attach stream holds the earlier launch's registry token: %s", ring)
			}
			if strings.Contains(recorded, registryCanary) {
				t.Fatalf("the recorded I/O holds the earlier launch's registry token: %s", recorded)
			}
			// The tool's answer to the resume is not part of the replay: it follows the
			// notice in the stream.
			if after := ring[strings.Index(ring, "history_withheld"):]; !strings.Contains(after, `"result"`) {
				t.Fatalf("the answer to the resume is not in the attach stream after the notice: %s", ring)
			}

			// What the resumed session says from now on is shown as ever.
			j.expectLiveReply()
		})
	}
}

// expectLiveReply sends a message and waits for the tool's reply in the attach
// stream and in the recorded I/O.
func (j *replayJourney) expectLiveReply() {
	j.t.Helper()
	if err := j.m.sendTextInput(context.Background(), j.tenant, j.runRef, "continue"); err != nil {
		j.t.Fatalf("input: %v", err)
	}
	lr := j.live()
	waitFor(j.t, "the live reply in the stream", func() bool {
		return countACPUpdates(lr, "agent_message_chunk", "live output") == 1
	})
	if _, recorded := j.streamed(); !strings.Contains(recorded, "live output") {
		j.t.Fatalf("the live reply is in the attach stream and not in the recorded I/O: %s", recorded)
	}
}

// Every resume of the session is held, not only the first: the row keeps the tokens
// of all earlier launches.
func TestACPResume_ASecondResumeHoldsItsReplayToo(t *testing.T) {
	for _, tool := range acpReplayTools() {
		t.Run(tool.name, func(t *testing.T) {
			j := newReplayJourney(t, tool, true)
			j.create("conv-twice")
			j.stop()
			j.rotate()
			j.resume("conv-twice", "first replay "+registryCanary, 1)
			j.stop()
			j.resume("conv-twice", "second replay "+registryCanary, 2)

			// The stream is the live launch's own; the record holds every launch's, so
			// it says it once for each resume.
			ring, recorded := j.streamed()
			for what, want := range map[string]struct {
				text    string
				notices int
			}{"attach stream": {ring, 1}, "recorded I/O": {recorded, 2}} {
				if strings.Contains(want.text, registryCanary) || strings.Contains(want.text, "replay "+registryCanary) {
					t.Fatalf("the %s holds a replay: %s", what, want.text)
				}
				if got := strings.Count(want.text, `"code":"history_withheld"`); got != want.notices {
					t.Fatalf("the %s holds %d notices, want %d: %s", what, got, want.notices, want.text)
				}
			}
			j.expectLiveReply()
		})
	}
}

// A session that was never resumed keeps its live output, redacted as before.
func TestACPResume_ASessionNeverResumedKeepsItsLiveOutput(t *testing.T) {
	for _, tool := range acpReplayTools() {
		t.Run(tool.name, func(t *testing.T) {
			j := newReplayJourney(t, tool, true)
			j.create("conv-first")
			// The person's own message is published through the launch's redactor.
			if err := j.m.sendTextInput(context.Background(), j.tenant, j.runRef, "token is "+registryCanary); err != nil {
				t.Fatalf("input: %v", err)
			}
			lr := j.live()
			waitFor(t, "the live reply in the stream", func() bool {
				return countACPUpdates(lr, "agent_message_chunk", "live output") == 1
			})
			ring, _ := j.streamed()
			if strings.Contains(ring, registryCanary) || !strings.Contains(ring, "[secret "+acmeRegistry.Secret+"]") {
				t.Fatalf("a launch's own token is withheld by name in its stream: %s", ring)
			}
		})
	}
}

// A resume that no earlier launch preceded with a registry token is covered by its
// own redactor: the replay is shown, with the current token withheld by name.
func TestACPResume_ReplayIsShownWhenNoEarlierLaunchUsedAToken(t *testing.T) {
	for _, tool := range acpReplayTools() {
		t.Run(tool.name, func(t *testing.T) {
			j := newReplayJourney(t, tool, false)
			j.create("conv-before-registry")
			j.stop()
			// The profile declares the registry after the session was created: the
			// resume is the first launch that opens the token.
			j.declareRegistry()
			j.resume("conv-before-registry", "the token is "+registryCanary, 2)

			ring, recorded := j.streamed()
			for what, text := range map[string]string{"attach stream": ring, "recorded I/O": recorded} {
				if strings.Contains(text, registryCanary) {
					t.Fatalf("the %s holds the registry token: %s", what, text)
				}
				if got := strings.Count(text, "the token is [secret "+acmeRegistry.Secret+"]"); got != 2 {
					t.Fatalf("the %s shows %d replayed updates with the token withheld by name, want 2: %s", what, got, text)
				}
				if strings.Contains(text, "history_withheld") {
					t.Fatalf("the %s says its history was held back when it was not: %s", what, text)
				}
			}
		})
	}
}
