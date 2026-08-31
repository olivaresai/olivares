// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeapi

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestJudge_CoTForcedSchemaAndVerbosityControl proves the bias mitigations are
// actually in the wire request: the structured-outputs schema lists "analysis" BEFORE
// "score" (reasoning-before-verdict — generation is autoregressive, property order is
// the forcing), and the system rubric carries the explicit verbosity control.
func TestJudge_CoTForcedSchemaAndVerbosityControl(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"POST /v1/messages": `{"id":"msg_3","type":"message","role":"assistant","model":"claude-opus-4-8",
			"content":[{"type":"text","text":"{\"analysis\":\"reasoned first\",\"score\":0.8,\"passed\":true,\"reason\":\"ok\"}"}],
			"usage":{"input_tokens":50,"output_tokens":10}}`,
	}}
	inf := newInf(d, model.GatewayDirect)
	res, err := inf.Judge(context.Background(), JudgeInput{Output: "the answer", Criterion: "is it correct"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if !res.Passed || res.Score != 0.8 || res.Reason != "ok" {
		t.Errorf("verdict = %+v", res)
	}
	// The default model (claude-opus-4-8) supports structured outputs, so the schema
	// rides output_config — with analysis ordered before score.
	ai := strings.Index(d.lastBody, `"analysis"`)
	si := strings.Index(d.lastBody, `"score"`)
	if ai < 0 || si < 0 || ai > si {
		t.Errorf("schema must order analysis before score (CoT-forcing): analysis@%d score@%d body=%s", ai, si, d.lastBody)
	}
	if !strings.Contains(d.lastBody, "length is not quality") {
		t.Errorf("system rubric missing the verbosity control: %s", d.lastBody)
	}
}

// TestParseJudgeVerdict_DropsAnalysis proves the forced reasoning is consumed in
// flight and never surfaces in the result (minimal-data).
func TestParseJudgeVerdict_DropsAnalysis(t *testing.T) {
	v, err := parseJudgeVerdict(`{"analysis":"long chain of reasoning","score":0.5,"passed":false,"reason":"short"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.Reason != "short" || v.Score != 0.5 {
		t.Errorf("verdict = %+v", v)
	}
}

// TestJudgePair_WinnerMappingAndPrompt exercises the ordered pairwise call: both
// candidates ride the prompt in presentation order, the position-neutrality and
// verbosity instructions are present, and "1"/"2"/"tie" map to the position labels.
func TestJudgePair_WinnerMappingAndPrompt(t *testing.T) {
	d := &routeDoer{routes: map[string]string{
		"POST /v1/messages": `{"id":"msg_4","type":"message","role":"assistant","model":"claude-opus-4-8",
			"content":[{"type":"text","text":"{\"analysis\":\"compared\",\"winner\":\"2\",\"reason\":\"second is correct\"}"}],
			"usage":{"input_tokens":60,"output_tokens":12}}`,
	}}
	inf := newInf(d, model.GatewayDirect)
	res, err := inf.JudgePair(context.Background(), JudgePairInput{
		Criterion: "correctness", OutputFirst: "alpha-out", OutputSecond: "beta-out",
	})
	if err != nil {
		t.Fatalf("JudgePair: %v", err)
	}
	if res.Winner != PairWinnerSecond || res.Reason != "second is correct" {
		t.Errorf("pair verdict = %+v", res)
	}
	if !strings.Contains(d.lastBody, "alpha-out") || !strings.Contains(d.lastBody, "beta-out") {
		t.Errorf("pair prompt missing candidates: %s", d.lastBody)
	}
	if !strings.Contains(d.lastBody, "do not favor either position") {
		t.Errorf("pair rubric missing the position control: %s", d.lastBody)
	}
	if !strings.Contains(d.lastBody, "Length is not quality") {
		t.Errorf("pair rubric missing the verbosity control: %s", d.lastBody)
	}
}

// TestParsePairVerdict_Validation proves tie mapping, prose tolerance and the
// fail-closed rejection of an unknown winner (never a guess).
func TestParsePairVerdict_Validation(t *testing.T) {
	v, err := parsePairVerdict(`prose {"analysis":"x","winner":"tie","reason":"equal"} trailing`)
	if err != nil || v.Winner != PairWinnerTie {
		t.Errorf("tie parse = %+v err=%v", v, err)
	}
	v, err = parsePairVerdict(`{"winner":"1","reason":"r"}`)
	if err != nil || v.Winner != PairWinnerFirst {
		t.Errorf("first parse = %+v err=%v", v, err)
	}
	if _, err := parsePairVerdict(`{"winner":"both","reason":"r"}`); err == nil {
		t.Error("want error on unknown winner")
	}
	if _, err := parsePairVerdict("no json"); err == nil {
		t.Error("want error on missing verdict")
	}
}

// TestJudgePair_NotConfiguredFailsClosed mirrors the pointwise guard.
func TestJudgePair_NotConfiguredFailsClosed(t *testing.T) {
	inf := &Inference{}
	if _, err := inf.JudgePair(context.Background(), JudgePairInput{}); err != ErrNotConfigured {
		t.Errorf("want ErrNotConfigured, got %v", err)
	}
}
