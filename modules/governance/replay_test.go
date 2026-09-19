// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/sdk"
)

// Monday allow → Tuesday revoke → Wednesday reconstruct Monday from the ledger.
const (
	replayMonday     = "2026-09-14T10:00:00.000000000Z"
	replayMondayAsk  = "2026-09-14T12:00:00.000000000Z"
	replayTuesday    = "2026-09-15T10:00:00.000000000Z"
	replayTuesdayAsk = "2026-09-15T12:00:00.000000000Z"
)

const (
	cedarAllow = "permit(principal, action, resource);"
	cedarDeny  = "forbid(principal, action, resource);"
)

func replayQuestion() sdk.AccessQuestion {
	return sdk.AccessQuestion{
		SchemaVersion:    sdk.AccessEvidenceSchemaVersion,
		ActorRef:         "agent-7",
		PrincipalRef:     "agent-7",
		SourceInstance:   "pg-prod-1",
		ResourceKind:     "postgres.table",
		ResourceRef:      "public.customers",
		Action:           "SELECT",
		ActionVocabulary: "postgres.sql.v1",
	}
}

func replayAppend(t *testing.T, eventType, sourceEventID, occurredAt string) store.AccessEvidenceAppend {
	t.Helper()
	return store.AccessEvidenceAppend{
		Envelope: sdk.AccessEvidenceEnvelope{
			SchemaVersion:    sdk.AccessEvidenceSchemaVersion,
			ProducerInstance: "replay-test",
			SourceEventID:    sourceEventID,
			EventType:        eventType,
			AdapterVersion:   "1.0.0",
			OccurredAt:       occurredAt,
		},
		Actor:     "replay-test",
		ActorKind: model.ActorSystem,
	}
}

func openReplayStore(t *testing.T, dsn string) store.Store {
	t.Helper()
	st, err := engine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, func(store.ExtensionRegistry) error { return nil })
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func provisionReplayTenant(t *testing.T, st store.Store) model.TenantID {
	t.Helper()
	var tenant model.TenantID
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(context.Background()); e != nil {
			return e
		}
		org, e := sys.CreateOrg(context.Background(), model.Org{Name: "replay", Slug: "replay", Status: model.StatusActive})
		if e != nil {
			return e
		}
		tenant = org.TenantID
		return nil
	}); err != nil {
		t.Fatalf("provision tenant: %v", err)
	}
	return tenant
}

func retainReplayArtifact(t *testing.T, st store.Store, tenant model.TenantID, source, body, occurredAt string) model.PolicyArtifact {
	t.Helper()
	var out model.PolicyArtifact
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		res, err := sc.AccessEvidence().RetainPolicyArtifact(context.Background(), store.PolicyArtifactAppend{
			AccessEvidenceAppend: replayAppend(t, sdk.EventTypePolicyArtifact, source, occurredAt),
			Artifact: sdk.PolicyArtifactContent{
				SchemaVersion:      sdk.AccessEvidenceSchemaVersion,
				AuthorityID:        "olivares.governance",
				Surface:            "cedar",
				Engine:             "cedar-3",
				ArtifactDigest:     sdk.ArtifactContentDigest([]byte(body)),
				DigestAlgorithm:    sdk.ArtifactDigestAlgorithm,
				Origin:             sdk.OriginLocalAuthoritative,
				Availability:       sdk.AvailabilityRetained,
				Content:            body,
				ContentBytes:       int64(len(body)),
				GovernanceSurface:  "cedar",
				GovernanceRevision: 1,
			},
		})
		out = res.Record
		return err
	})
	if err != nil {
		t.Fatalf("retain artifact %s: %v", source, err)
	}
	return out
}

func recordReplayDecision(t *testing.T, st store.Store, tenant model.TenantID, source, occurredAt string, artifact model.PolicyArtifact, outcome sdk.AccessDecisionOutcome) model.AuthorizationDecision {
	t.Helper()
	var out model.AuthorizationDecision
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		res, err := sc.AccessEvidence().AppendAuthorizationDecision(context.Background(), store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: replayAppend(t, sdk.EventTypeAuthorizationDecision, source, occurredAt),
			Decision: sdk.AuthorizationDecisionContent{
				SchemaVersion:      sdk.AccessEvidenceSchemaVersion,
				Question:           replayQuestion(),
				Purpose:            sdk.PurposeLiveAuthorization,
				Evaluator:          "cedar",
				EvaluatorVersion:   "3.1.0",
				Outcome:            outcome,
				Disposition:        sdk.DecisionDisposition(outcome),
				ReasonCode:         "policy." + string(outcome),
				AuthorizationPoint: "replay.test",
				ReplayCompleteness: sdk.ReplayComplete,
				Inputs: []sdk.AccessDependency{{
					Kind: sdk.DependencyPolicyArtifact, Ref: artifact.ID.String(),
					Digest: artifact.Artifact.ArtifactDigest, Required: true,
				}},
			},
		})
		out = res.Record
		return err
	})
	if err != nil {
		t.Fatalf("record decision %s: %v", source, err)
	}
	return out
}

func parseReplayInstant(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := sdk.ParseEvidenceTime(s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return ts
}

func TestMondayAllowTuesdayRevokeWednesdayReconstructsFromLedger(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "three-day.db")
	st := openReplayStore(t, dsn)
	tenant := provisionReplayTenant(t, st)

	mondayArt := retainReplayArtifact(t, st, tenant, "art-monday", cedarAllow, replayMonday)
	mondayDec := recordReplayDecision(t, st, tenant, "dec-monday", replayMonday, mondayArt, sdk.AccessOutcomeAllow)
	if !mondayDec.PolicyVersionKnown() || mondayDec.Decision.PolicyVersionID != mondayArt.ID.String() {
		t.Fatalf("monday decision did not stamp policy_version_id: %+v", mondayDec.Decision)
	}

	tuesdayArt := retainReplayArtifact(t, st, tenant, "art-tuesday", cedarDeny, replayTuesday)
	tuesdayDec := recordReplayDecision(t, st, tenant, "dec-tuesday", replayTuesday, tuesdayArt, sdk.AccessOutcomeDeny)
	if tuesdayDec.Decision.PolicyVersionID == mondayDec.Decision.PolicyVersionID {
		t.Fatal("tuesday must record a different policy version than monday")
	}

	m := governance.New()
	m.UseData(api.NewModuleData(st))

	q := replayQuestion()
	mondayAsk := parseReplayInstant(t, replayMondayAsk)
	got, err := m.Reconstruct(context.Background(), tenant, governance.ReconstructRequest{
		At: mondayAsk, Principal: q.ActorRef, Resource: q.ResourceRef,
		ResourceKind: q.ResourceKind, SourceInstance: q.SourceInstance,
		Action: q.Action, ActionVocabulary: q.ActionVocabulary,
	})
	if err != nil {
		t.Fatalf("wednesday reconstruct of monday: %v", err)
	}
	if got.UsedLivePolicy {
		t.Fatal("reconstruction consulted the live policy")
	}
	if got.Status != governance.ReconstructReconstructed || got.Outcome != sdk.AccessOutcomeAllow {
		t.Fatalf("monday reconstruction = %+v, want reconstructed allow", got)
	}
	if got.PolicyVersionID != mondayArt.ID.String() {
		t.Fatalf("monday reconstruction used %q, want monday artifact %q", got.PolicyVersionID, mondayArt.ID)
	}
	if got.DecisionID != mondayDec.ID {
		t.Fatalf("selected decision %q, want monday %q", got.DecisionID, mondayDec.ID)
	}

	tuesdayAsk := parseReplayInstant(t, replayTuesdayAsk)
	gotTue, err := m.Reconstruct(context.Background(), tenant, governance.ReconstructRequest{
		At: tuesdayAsk, Principal: q.ActorRef, Resource: q.ResourceRef,
		ResourceKind: q.ResourceKind, SourceInstance: q.SourceInstance,
		Action: q.Action, ActionVocabulary: q.ActionVocabulary,
	})
	if err != nil {
		t.Fatalf("wednesday reconstruct of tuesday: %v", err)
	}
	if gotTue.UsedLivePolicy {
		t.Fatal("tuesday reconstruction consulted the live policy")
	}
	if gotTue.Outcome != sdk.AccessOutcomeDeny {
		t.Fatalf("tuesday reconstruction = %+v, want deny", gotTue)
	}
	if gotTue.PolicyVersionID != tuesdayArt.ID.String() {
		t.Fatalf("tuesday reconstruction used %q, want tuesday artifact %q", gotTue.PolicyVersionID, tuesdayArt.ID)
	}
}

func TestReconstructDoesNotUseLivePolicyAfterRevoke(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "not-live.db")
	st := openReplayStore(t, dsn)
	tenant := provisionReplayTenant(t, st)
	allowArt := retainReplayArtifact(t, st, tenant, "art-allow", cedarAllow, replayMonday)
	recordReplayDecision(t, st, tenant, "dec-allow", replayMonday, allowArt, sdk.AccessOutcomeAllow)
	_ = retainReplayArtifact(t, st, tenant, "art-deny-now", cedarDeny, replayTuesday)

	m := governance.New()
	m.UseData(api.NewModuleData(st))
	q := replayQuestion()
	got, err := m.Reconstruct(context.Background(), tenant, governance.ReconstructRequest{
		At: parseReplayInstant(t, replayMondayAsk), Principal: q.ActorRef,
		Resource: q.ResourceRef, ResourceKind: q.ResourceKind, SourceInstance: q.SourceInstance,
		Action: q.Action, ActionVocabulary: q.ActionVocabulary,
	})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if got.Outcome != sdk.AccessOutcomeAllow || got.UsedLivePolicy {
		t.Fatalf("after a later deny artifact was retained, monday must still reconstruct allow from the ledger, got %+v", got)
	}
}

func TestReconstructNamesMissingDecision(t *testing.T) {
	st := openReplayStore(t, filepath.Join(t.TempDir(), "missing.db"))
	tenant := provisionReplayTenant(t, st)
	m := governance.New()
	m.UseData(api.NewModuleData(st))
	got, err := m.Reconstruct(context.Background(), tenant, governance.ReconstructRequest{
		At: parseReplayInstant(t, replayMondayAsk), Principal: "nobody",
		Resource: "r", ResourceKind: "k", Action: "read",
	})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if !got.CouldNotReconstruct || got.Missing != governance.MissingAuthorizationDecision {
		t.Fatalf("want COULD NOT RECONSTRUCT missing authorization_decision, got %+v", got)
	}
	if got.ReasonCode != governance.CouldNotReconstruct {
		t.Fatalf("reason = %q, want %q", got.ReasonCode, governance.CouldNotReconstruct)
	}
}

func TestReconstructNamesMissingArtifactContent(t *testing.T) {
	st := openReplayStore(t, filepath.Join(t.TempDir(), "absent.db"))
	tenant := provisionReplayTenant(t, st)
	var artifact model.PolicyArtifact
	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		res, err := sc.AccessEvidence().RetainPolicyArtifact(context.Background(), store.PolicyArtifactAppend{
			AccessEvidenceAppend: replayAppend(t, sdk.EventTypePolicyArtifact, "art-digest-only", replayMonday),
			Artifact: sdk.PolicyArtifactContent{
				SchemaVersion:   sdk.AccessEvidenceSchemaVersion,
				AuthorityID:     "remote",
				Surface:         "cedar",
				Engine:          "cedar-3",
				ArtifactDigest:  sdk.ArtifactContentDigest([]byte("forbid(principal, action, resource);")),
				DigestAlgorithm: sdk.ArtifactDigestAlgorithm,
				Origin:          sdk.OriginRemoteAuthenticatedSnapshot,
				Availability:    sdk.AvailabilityAbsent,
			},
		})
		artifact = res.Record
		return err
	})
	if err != nil {
		t.Fatalf("retain digest-only artifact: %v", err)
	}
	var decision model.AuthorizationDecision
	err = st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		res, err := sc.AccessEvidence().AppendAuthorizationDecision(context.Background(), store.AuthorizationDecisionAppend{
			AccessEvidenceAppend: replayAppend(t, sdk.EventTypeAuthorizationDecision, "dec-absent", replayMonday),
			Decision: sdk.AuthorizationDecisionContent{
				SchemaVersion:      sdk.AccessEvidenceSchemaVersion,
				Question:           replayQuestion(),
				Purpose:            sdk.PurposeLiveAuthorization,
				Evaluator:          "cedar",
				EvaluatorVersion:   "3.1.0",
				Outcome:            sdk.AccessOutcomeAllow,
				Disposition:        sdk.DispositionAllow,
				ReasonCode:         "policy.allow",
				AuthorizationPoint: "replay.test",
				ReplayCompleteness: sdk.ReplayIncomplete,
				Inputs: []sdk.AccessDependency{{
					Kind: sdk.DependencyPolicyArtifact, Ref: artifact.ID.String(),
					Digest: artifact.Artifact.ArtifactDigest, Required: true,
				}},
			},
		})
		decision = res.Record
		return err
	})
	if err != nil {
		t.Fatalf("record incomplete decision: %v", err)
	}

	m := governance.New()
	m.UseData(api.NewModuleData(st))
	got, err := m.Reconstruct(context.Background(), tenant, governance.ReconstructRequest{DecisionID: decision.ID})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if !got.CouldNotReconstruct || got.Missing != governance.MissingPolicyArtifactContent {
		t.Fatalf("want missing policy_artifact.content, got %+v", got)
	}
}

func TestReconstructIsDeterministicAcrossTwoStores(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "two-proc.db")
	st := openReplayStore(t, dsn)
	tenant := provisionReplayTenant(t, st)
	art := retainReplayArtifact(t, st, tenant, "art", cedarDeny, replayMonday)
	recordReplayDecision(t, st, tenant, "dec", replayMonday, art, sdk.AccessOutcomeDeny)
	if err := st.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	q := replayQuestion()
	req := governance.ReconstructRequest{
		At: parseReplayInstant(t, replayMondayAsk), Principal: q.ActorRef,
		Resource: q.ResourceRef, ResourceKind: q.ResourceKind, SourceInstance: q.SourceInstance,
		Action: q.Action, ActionVocabulary: q.ActionVocabulary,
	}

	run := func(name string) governance.ReconstructResult {
		t.Helper()
		s := openReplayStore(t, dsn)
		m := governance.New()
		m.UseData(api.NewModuleData(s))
		got, err := m.Reconstruct(context.Background(), tenant, req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.UsedLivePolicy {
			t.Fatalf("%s used live policy", name)
		}
		return got
	}
	a := run("process-a")
	b := run("process-b")
	aj, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal a: %v", err)
	}
	bj, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	if string(aj) != string(bj) {
		t.Fatalf("two independent reconstructions diverged:\n%s\n%s", aj, bj)
	}
	if a.Outcome != sdk.AccessOutcomeDeny {
		t.Fatalf("expected deny, got %+v", a)
	}
}

const (
	replayHelperDSNEnv    = "OLIVARES_REPLAY_HELPER_DSN"
	replayHelperTenantEnv = "OLIVARES_REPLAY_HELPER_TENANT"
)

// TestHelperProcessReconstructAcrossOSProcesses is a helper entry point, not a
// control. In an ordinary run it does nothing; it is re-executed by name, with
// the ledger path in the environment, by the case below.
func TestHelperProcessReconstructAcrossOSProcesses(t *testing.T) {
	dsn := os.Getenv(replayHelperDSNEnv)
	if dsn == "" {
		t.Skip("helper process entry point: it runs only when this binary is re-executed by TestReconstructIsDeterministicAcrossTwoOSProcesses")
	}
	tenant := model.TenantID(os.Getenv(replayHelperTenantEnv))
	if tenant.IsZero() {
		fmt.Fprintf(os.Stderr, "replay helper: tenant is empty\n")
		os.Exit(1)
	}
	st, err := engine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
	}, func(store.ExtensionRegistry) error { return nil })
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay helper: open store: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = st.Close() }()
	m := governance.New()
	m.UseData(api.NewModuleData(st))
	q := replayQuestion()
	got, err := m.Reconstruct(context.Background(), tenant, governance.ReconstructRequest{
		At: parseReplayInstant(t, replayMondayAsk), Principal: q.ActorRef,
		Resource: q.ResourceRef, ResourceKind: q.ResourceKind, SourceInstance: q.SourceInstance,
		Action: q.Action, ActionVocabulary: q.ActionVocabulary,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "replay helper: reconstruct: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(got); err != nil {
		fmt.Fprintf(os.Stderr, "replay helper: encode: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestReconstructIsDeterministicAcrossTwoOSProcesses(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "two-os-proc.db")
	st := openReplayStore(t, dsn)
	tenant := provisionReplayTenant(t, st)
	art := retainReplayArtifact(t, st, tenant, "art", cedarDeny, replayMonday)
	recordReplayDecision(t, st, tenant, "dec", replayMonday, art, sdk.AccessOutcomeDeny)
	if err := st.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	q := replayQuestion()
	req := governance.ReconstructRequest{
		At: parseReplayInstant(t, replayMondayAsk), Principal: q.ActorRef,
		Resource: q.ResourceRef, ResourceKind: q.ResourceKind, SourceInstance: q.SourceInstance,
		Action: q.Action, ActionVocabulary: q.ActionVocabulary,
	}

	parentStore := openReplayStore(t, dsn)
	m := governance.New()
	m.UseData(api.NewModuleData(parentStore))
	parent, err := m.Reconstruct(context.Background(), tenant, req)
	if err != nil {
		t.Fatalf("parent reconstruct: %v", err)
	}
	if parent.UsedLivePolicy {
		t.Fatal("parent reconstruction consulted the live policy")
	}
	if err := parentStore.Close(); err != nil {
		t.Fatalf("close parent store: %v", err)
	}

	// Compared bytes are the JSON document POST /decisions/replay writes
	// (encoding/json Encoder, trailing newline included). ReconstructResult
	// has no wall-clock generated-at and no process id; nothing is excluded.
	var parentBuf bytes.Buffer
	if err := json.NewEncoder(&parentBuf).Encode(parent); err != nil {
		t.Fatalf("encode parent reconstruction: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestHelperProcessReconstructAcrossOSProcesses$",
		"-test.count=1",
		"-test.timeout=2m",
	)
	cmd.Env = append(os.Environ(),
		replayHelperDSNEnv+"="+dsn,
		replayHelperTenantEnv+"="+string(tenant),
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	childOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper process: %v\nstderr:\n%s\nstdout:\n%s", err, stderr.String(), childOut)
	}
	if !bytes.Equal(parentBuf.Bytes(), childOut) {
		t.Fatalf("parent and helper-process reconstructions diverged:\nparent: %s\nchild:  %s", parentBuf.Bytes(), childOut)
	}
	if parent.Outcome != sdk.AccessOutcomeDeny {
		t.Fatalf("expected deny, got %+v", parent)
	}
}

func TestReconstructByDecisionID(t *testing.T) {
	st := openReplayStore(t, filepath.Join(t.TempDir(), "by-id.db"))
	tenant := provisionReplayTenant(t, st)
	art := retainReplayArtifact(t, st, tenant, "art", cedarAllow, replayMonday)
	dec := recordReplayDecision(t, st, tenant, "dec", replayMonday, art, sdk.AccessOutcomeAllow)
	m := governance.New()
	m.UseData(api.NewModuleData(st))
	got, err := m.Reconstruct(context.Background(), tenant, governance.ReconstructRequest{DecisionID: dec.ID})
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	if got.Status != governance.ReconstructReconstructed || got.Outcome != sdk.AccessOutcomeAllow {
		t.Fatalf("by-id = %+v", got)
	}
	if got.DecisionID != dec.ID {
		t.Fatalf("decision id = %q, want %q", got.DecisionID, dec.ID)
	}
}
