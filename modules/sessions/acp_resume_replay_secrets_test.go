// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// The replay of a resumed ACP tool is held back for a run that was given vault
// secrets or a repository read credential, as it is for a registry token
// (acp_resume_replay_registry_test.go): the resumed launch's redactor holds the
// secrets' current values and its own read token, so a value an earlier launch
// printed (a secret rotated since, the earlier launch's read token) passes it.

const (
	deployKeyV1 = "dk_V1_earlier_launch_value_0123456789"
	deployKeyV2 = "dk_V2_rotated_value_9876543210"
)

var deployKeyEnv = []SecretEnvRef{{Env: "DEPLOY_KEY", Secret: "env/deploy-key"}}

// createGiven is the journey's first launch with the run given vault secrets
// and/or a repository read credential.
func (j *replayJourney) createGiven(conversation string, secrets []SecretEnvRef, gitRead string) {
	j.t.Helper()
	j.tool.fixture(j.t, j.prof, conversation, "", 0)
	dto, err := createProfiledTestRun(j.t, j.m, context.Background(), j.tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: j.prof.Ref,
		EnvAllow:           []string{"FIXTURE_MARKER"},
		SecretEnv:          secrets, GitRead: gitRead, MayUseSecretEnv: true,
	})
	if err != nil {
		j.t.Fatalf("create: %v", err)
	}
	j.runRef = dto.RunRef
	j.t.Cleanup(func() {
		_, _ = j.m.stopRun(context.Background(), j.tenant, dto.RunRef, "user:u1", model.ActorUser)
	})
}

// resumeAdministered is the resume of a person who may give the run its secrets.
func (j *replayJourney) resumeAdministered(conversation, replayText string, replayed int) {
	j.t.Helper()
	j.tool.fixture(j.t, j.prof, conversation, replayText, replayed)
	if _, err := j.m.resumeRunAs(context.Background(), j.tenant, j.runRef, "user:u1", model.ActorUser, "", true); err != nil {
		j.t.Fatalf("resume: %v", err)
	}
}

// requireReplayHeld checks what an operator reads after a resume: the replay is
// counted as history by the driver, withheld from the ring and the recorded I/O
// but for one notice, and the answer to the resume is still published.
func (j *replayJourney) requireReplayHeld(replayed int, leaked ...string) {
	j.t.Helper()
	if got := j.historyReplayed(); got != replayed {
		j.t.Fatalf("history updates = %d, want the %d the tool replayed", got, replayed)
	}
	ring, recorded := j.streamed()
	for what, text := range map[string]string{"attach stream": ring, "recorded I/O": recorded} {
		for _, value := range leaked {
			if strings.Contains(text, value) {
				j.t.Fatalf("the %s holds %q an earlier launch printed: %s", what, value, text)
			}
		}
		// One notice says what was held back and why, so an empty history is not
		// mistaken for a lost one.
		if got := strings.Count(text, `"code":"history_withheld"`); got != 1 {
			j.t.Fatalf("the %s holds %d history_withheld notices, want 1: %s", what, got, text)
		}
		if !strings.Contains(text, "used a vault secret, a repository credential, a managed credential or a package registry token") {
			j.t.Fatalf("the %s notice does not say why the history was held back: %s", what, text)
		}
	}
	if after := ring[strings.Index(ring, "history_withheld"):]; !strings.Contains(after, `"result"`) {
		j.t.Fatalf("the answer to the resume is not in the attach stream after the notice: %s", ring)
	}
}

// A secret rotated between two launches: the value the earlier launch printed is
// not the one the resumed launch's redactor holds.
func TestACPResume_ReplayedHistoryOfARotatedVaultSecretIsNotPublished(t *testing.T) {
	for _, tool := range acpReplayTools() {
		t.Run(tool.name, func(t *testing.T) {
			j := newReplayJourney(t, tool, false)
			j.vault.values[j.tenant.String()+"|env/deploy-key"] = deployKeyV1
			j.createGiven("conv-secret", deployKeyEnv, "")
			j.stop()
			j.vault.values[j.tenant.String()+"|env/deploy-key"] = deployKeyV2
			j.resumeAdministered("conv-secret", "the deploy key is "+deployKeyV1, 2)

			j.requireReplayHeld(2, deployKeyV1, deployKeyV2)
			// What the resumed session says from now on is shown as ever.
			j.expectLiveReply()
		})
	}
}

// The repository read token is minted per launch, so the resumed launch's
// redactor holds its own token and not the one the first launch printed.
func TestACPResume_ReplayedHistoryOfAnEarlierGitReadTokenIsNotPublished(t *testing.T) {
	for _, tool := range acpReplayTools() {
		t.Run(tool.name, func(t *testing.T) {
			j := newReplayJourney(t, tool, false)
			src := newFakeGitRead("https://github.com/acme/widgets.git")
			j.m.GitRead, j.m.GitReadDataDir = src, t.TempDir()
			if tool.confined {
				// A confined session reaches only its provider: no token is minted, and
				// the launch says why, so there is no earlier token to replay.
				j.tool.fixture(t, j.prof, "conv-git", "", 0)
				_, err := createProfiledTestRun(t, j.m, context.Background(), j.tenant, CreateRunParams{
					Transport: TransportStreamJSON, Isolation: IsolationNative, Actor: "user:u1", ActorKind: model.ActorUser,
					ProviderProfileRef: j.prof.Ref, EnvAllow: []string{"FIXTURE_MARKER"}, GitRead: gitReadBinding, MayUseSecretEnv: true,
				})
				if err == nil || !strings.Contains(err.Error(), "git_read") || len(src.minted()) != 0 {
					t.Fatalf("confined git_read = %v, mints %v; want a named refusal and no token", err, src.minted())
				}
				return
			}
			j.createGiven("conv-git", nil, gitReadBinding)
			j.stop()
			src.awaitRelease(t)
			first := gitReadCanary + "-1"
			j.resumeAdministered("conv-git", "git said "+first, 2)

			if got := src.minted(); len(got) != 2 {
				t.Fatalf("mints = %v, want one for each launch", got)
			}
			j.requireReplayHeld(2, first, gitReadCanary+"-2")
			j.expectLiveReply()
		})
	}
}

// The hold does not depend on a rotation: a run given secrets cannot show that
// the value its earlier launch printed is the one the vault holds now.
func TestACPResume_ReplayedHistoryOfAnUnrotatedVaultSecretIsHeldToo(t *testing.T) {
	for _, tool := range acpReplayTools() {
		t.Run(tool.name, func(t *testing.T) {
			j := newReplayJourney(t, tool, false)
			j.vault.values[j.tenant.String()+"|env/deploy-key"] = deployKeyV1
			j.createGiven("conv-unrotated", deployKeyEnv, "")
			j.stop()
			j.resumeAdministered("conv-unrotated", "the deploy key is "+deployKeyV1, 1)

			j.requireReplayHeld(1, deployKeyV1)
			j.expectLiveReply()
		})
	}
}

// A run given secrets that was never resumed keeps publishing its live output,
// with the secret withheld by name. Its launch is the first, so its redactor holds
// every value the transcript can hold.
func TestACPResume_ASessionGivenSecretsNeverResumedKeepsItsLiveOutput(t *testing.T) {
	for _, tool := range acpReplayTools() {
		t.Run(tool.name, func(t *testing.T) {
			j := newReplayJourney(t, tool, false)
			j.vault.values[j.tenant.String()+"|env/deploy-key"] = deployKeyV1
			j.createGiven("conv-given-first", deployKeyEnv, "")
			if err := j.m.sendTextInput(context.Background(), j.tenant, j.runRef, "key is "+deployKeyV1); err != nil {
				t.Fatalf("input: %v", err)
			}
			lr := j.live()
			if !lr.redactsEveryEarlierValue {
				t.Fatal("a first launch of a run given secrets redacts every value its transcript can hold")
			}
			waitFor(t, "the live reply in the stream", func() bool {
				return countACPUpdates(lr, "agent_message_chunk", "live output") == 1
			})
			ring, _ := j.streamed()
			if strings.Contains(ring, deployKeyV1) || !strings.Contains(ring, "[secret env/deploy-key]") ||
				strings.Contains(ring, "history_withheld") {
				t.Fatalf("a first launch publishes its own output with the secret withheld by name: %s", ring)
			}
		})
	}
}
