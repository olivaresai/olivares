// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// replay.go — independent historical reconstruction of an authorization
// decision.
//
// It answers "what did the policy decide then" from the access-evidence
// ledger: the recorded decision, the retained policy artifact that decision
// named, and a re-evaluation of those original inputs. It never reads the
// live PDP, the active authoring revision, or today's resource bindings.
//
// Landscape finding 3: the route-witness PolicyVersion is a maximum of
// independent fact versions, not the authoring revision and not this
// PolicyVersionID. Reconstruction uses the latter.

// ReconstructStatus is the closed set of answers a reconstruction may
// return. Insufficient is an answer, not an HTTP error.
type ReconstructStatus string

const (
	// ReconstructReconstructed: the retained inputs were enough and the
	// re-evaluation matched the recorded outcome (or no outcome was recorded).
	ReconstructReconstructed ReconstructStatus = "reconstructed"
	// ReconstructMismatch: re-evaluation ran and disagreed with the recorded
	// outcome. That is evidence, not a silent correction.
	ReconstructMismatch ReconstructStatus = "mismatch"
	// ReconstructUnsupported: the retained artifact names an evaluator this
	// binary cannot run (for example OPA/Rego, which is authoring-only).
	ReconstructUnsupported ReconstructStatus = "unsupported"
	// ReconstructInsufficient: at least one required fact is missing. Missing
	// names that fact. The CLI prints this as COULD NOT RECONSTRUCT.
	ReconstructInsufficient ReconstructStatus = "insufficient"
)

// CouldNotReconstruct is the operator-facing label for ReconstructInsufficient.
const CouldNotReconstruct = "COULD NOT RECONSTRUCT"

// The named missing facts. They are stable tokens, never prose.
const (
	MissingAuthorizationDecision = "authorization_decision"
	MissingPolicyVersionID       = "policy_version_id"
	MissingPolicyArtifact        = "policy_artifact"
	MissingPolicyArtifactContent = "policy_artifact.content"
	MissingEvaluator             = "evaluator"
	MissingQuestion              = "question"
	MissingAt                    = "at"
)

// ReconstructRequest is one historical question. DecisionID, when set, selects
// that recorded row. Otherwise At + the question fields select the latest
// live_authorization decision for that question at or before At.
type ReconstructRequest struct {
	At               time.Time
	Principal        string
	Resource         string
	ResourceKind     string
	SourceInstance   string
	Action           string
	ActionVocabulary string
	DecisionID       model.ID
}

// ReconstructResult is the answer. UsedLivePolicy is always false: a true
// value would mean the reconstruction consulted today's policy, which this
// path refuses to do.
type ReconstructResult struct {
	Status                ReconstructStatus         `json:"status"`
	CouldNotReconstruct   bool                      `json:"could_not_reconstruct"`
	Missing               string                    `json:"missing,omitempty"`
	Outcome               sdk.AccessDecisionOutcome `json:"outcome,omitempty"`
	RecordedOutcome       sdk.AccessDecisionOutcome `json:"recorded_outcome,omitempty"`
	PolicyVersionID       string                    `json:"policy_version_id,omitempty"`
	PolicyVersionRecorded bool                      `json:"policy_version_recorded"`
	InputsDigest          string                    `json:"inputs_digest,omitempty"`
	DecisionID            model.ID                  `json:"decision_id,omitempty"`
	ArtifactID            model.ID                  `json:"artifact_id,omitempty"`
	Evaluator             string                    `json:"evaluator,omitempty"`
	At                    string                    `json:"at,omitempty"`
	UsedLivePolicy        bool                      `json:"used_live_policy"`
	ReasonCode            string                    `json:"reason_code,omitempty"`
}

func insufficient(missing string) ReconstructResult {
	return ReconstructResult{
		Status:              ReconstructInsufficient,
		CouldNotReconstruct: true,
		Missing:             missing,
		UsedLivePolicy:      false,
		ReasonCode:          CouldNotReconstruct,
	}
}

func unsupported(missing, evaluator string) ReconstructResult {
	return ReconstructResult{
		Status:         ReconstructUnsupported,
		Missing:        missing,
		Evaluator:      evaluator,
		UsedLivePolicy: false,
		ReasonCode:     "evaluator_unsupported",
	}
}

// Reconstruct re-evaluates a past authorization question from the evidence
// ledger. It is a read: it opens a View transaction and writes nothing.
func (m *Module) Reconstruct(ctx context.Context, tenant model.TenantID, req ReconstructRequest) (ReconstructResult, error) {
	if m == nil || m.data == nil {
		return insufficient(MissingAuthorizationDecision), nil
	}
	if tenant.IsZero() || tenant.IsSystem() {
		return insufficient(MissingQuestion), nil
	}

	var out ReconstructResult
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		var rerr error
		out, rerr = reconstructInScope(ctx, sc, tenant, req)
		return rerr
	})
	if err != nil {
		return ReconstructResult{}, err
	}
	if m.log != nil {
		m.log.Info("governance decision reconstruction",
			"tenant", string(tenant),
			"status", string(out.Status),
			"missing", out.Missing,
			"decision_id", out.DecisionID.String(),
			"used_live_policy", out.UsedLivePolicy,
		)
	}
	return out, nil
}

func reconstructInScope(ctx context.Context, sc store.Scope, tenant model.TenantID, req ReconstructRequest) (ReconstructResult, error) {
	ev := sc.AccessEvidence()
	decision, at, ok, missing, err := selectRecordedDecision(ctx, ev, req)
	if err != nil {
		return ReconstructResult{}, err
	}
	if !ok {
		return insufficient(missing), nil
	}

	artifact, artMissing, aerr := loadRecordedArtifact(ctx, ev, decision)
	if aerr != nil {
		return ReconstructResult{}, aerr
	}
	if artMissing != "" {
		res := insufficient(artMissing)
		res.DecisionID = decision.ID
		res.PolicyVersionID = decision.Decision.PolicyVersionID
		res.PolicyVersionRecorded = decision.PolicyVersionKnown()
		res.InputsDigest = decision.Decision.InputsDigest
		res.RecordedOutcome = decision.Decision.Outcome
		res.At = sdk.FormatEvidenceTime(at)
		return res, nil
	}

	engineName := strings.ToLower(strings.TrimSpace(artifact.Artifact.Engine))
	if !cedarEngine(engineName) {
		res := unsupported(MissingEvaluator, artifact.Artifact.Engine)
		res.DecisionID = decision.ID
		res.ArtifactID = artifact.ID
		res.PolicyVersionID = artifact.ID.String()
		res.PolicyVersionRecorded = decision.PolicyVersionKnown()
		res.InputsDigest = decision.Decision.InputsDigest
		res.RecordedOutcome = decision.Decision.Outcome
		res.At = sdk.FormatEvidenceTime(at)
		return res, nil
	}

	outcome, reason, evalErr := evaluateRetainedCedar(artifact.Artifact.Content, tenant, decision.Decision.Question, at)
	if evalErr != nil {
		res := unsupported(MissingEvaluator, artifact.Artifact.Engine)
		res.DecisionID = decision.ID
		res.ArtifactID = artifact.ID
		res.PolicyVersionID = artifact.ID.String()
		res.ReasonCode = "evaluator_compile_or_eval"
		res.At = sdk.FormatEvidenceTime(at)
		return res, nil
	}

	status := ReconstructReconstructed
	if decision.Decision.Outcome != "" && decision.Decision.Outcome != outcome {
		status = ReconstructMismatch
	}
	return ReconstructResult{
		Status:                status,
		Outcome:               outcome,
		RecordedOutcome:       decision.Decision.Outcome,
		PolicyVersionID:       artifact.ID.String(),
		PolicyVersionRecorded: decision.PolicyVersionKnown(),
		InputsDigest:          decision.Decision.InputsDigest,
		DecisionID:            decision.ID,
		ArtifactID:            artifact.ID,
		Evaluator:             artifact.Artifact.Engine,
		At:                    sdk.FormatEvidenceTime(at),
		UsedLivePolicy:        false,
		ReasonCode:            reason,
	}, nil
}

func selectRecordedDecision(
	ctx context.Context,
	ev store.AccessEvidenceRepo,
	req ReconstructRequest,
) (model.AuthorizationDecision, time.Time, bool, string, error) {
	if !req.DecisionID.IsZero() {
		d, err := ev.AuthorizationDecision(ctx, req.DecisionID)
		if err != nil {
			if isNotFound(err) {
				return model.AuthorizationDecision{}, time.Time{}, false, MissingAuthorizationDecision, nil
			}
			return model.AuthorizationDecision{}, time.Time{}, false, "", err
		}
		at := d.OccurredAt.Time()
		if !req.At.IsZero() {
			at = req.At.UTC()
		}
		return d, at, true, "", nil
	}
	if req.At.IsZero() {
		return model.AuthorizationDecision{}, time.Time{}, false, MissingAt, nil
	}
	question, qerr := reconstructQuestion(req)
	if qerr != "" {
		return model.AuthorizationDecision{}, time.Time{}, false, qerr, nil
	}
	digest, derr := question.Digest()
	if derr != nil {
		return model.AuthorizationDecision{}, time.Time{}, false, MissingQuestion, nil
	}
	rows, err := ev.AuthorizationDecisionsForQuestion(ctx, digest)
	if err != nil {
		return model.AuthorizationDecision{}, time.Time{}, false, "", err
	}
	at := req.At.UTC()
	var chosen model.AuthorizationDecision
	found := false
	for _, d := range rows {
		if d.Decision.Purpose != sdk.PurposeLiveAuthorization {
			continue
		}
		occurred := d.OccurredAt.Time()
		if occurred.After(at) {
			continue
		}
		if !found || occurred.After(chosen.OccurredAt.Time()) {
			chosen = d
			found = true
		}
	}
	if !found {
		return model.AuthorizationDecision{}, time.Time{}, false, MissingAuthorizationDecision, nil
	}
	return chosen, at, true, "", nil
}

func reconstructQuestion(req ReconstructRequest) (sdk.AccessQuestion, string) {
	principal := strings.TrimSpace(req.Principal)
	resource := strings.TrimSpace(req.Resource)
	action := strings.TrimSpace(req.Action)
	if principal == "" || action == "" {
		return sdk.AccessQuestion{}, MissingQuestion
	}
	q := sdk.AccessQuestion{
		SchemaVersion:    sdk.AccessEvidenceSchemaVersion,
		ActorRef:         principal,
		PrincipalRef:     principal,
		SourceInstance:   strings.TrimSpace(req.SourceInstance),
		ResourceKind:     strings.TrimSpace(req.ResourceKind),
		ResourceRef:      resource,
		Action:           action,
		ActionVocabulary: strings.TrimSpace(req.ActionVocabulary),
	}
	if !q.Valid() {
		return sdk.AccessQuestion{}, MissingQuestion
	}
	return q, ""
}

func loadRecordedArtifact(ctx context.Context, ev store.AccessEvidenceRepo, decision model.AuthorizationDecision) (model.PolicyArtifact, string, error) {
	ref := strings.TrimSpace(decision.Decision.PolicyVersionID)
	if ref == "" {
		ref = firstRequiredPolicyArtifactRef(decision.Decision.Inputs)
	}
	if ref == "" {
		return model.PolicyArtifact{}, MissingPolicyVersionID, nil
	}
	id, err := model.ParseID(ref)
	if err != nil || id.IsZero() {
		return model.PolicyArtifact{}, MissingPolicyArtifact, nil
	}
	artifact, aerr := ev.PolicyArtifact(ctx, id)
	if aerr != nil {
		if isNotFound(aerr) {
			return model.PolicyArtifact{}, MissingPolicyArtifact, nil
		}
		return model.PolicyArtifact{}, "", aerr
	}
	if !artifact.Reconstructible() {
		return model.PolicyArtifact{}, MissingPolicyArtifactContent, nil
	}
	return artifact, "", nil
}

func firstRequiredPolicyArtifactRef(inputs []sdk.AccessDependency) string {
	for _, in := range inputs {
		if in.Required && in.Kind == sdk.DependencyPolicyArtifact && strings.TrimSpace(in.Ref) != "" {
			return strings.TrimSpace(in.Ref)
		}
	}
	return ""
}

func cedarEngine(name string) bool {
	return name == "cedar" || strings.HasPrefix(name, "cedar-")
}

func evaluateRetainedCedar(content string, tenant model.TenantID, question sdk.AccessQuestion, at time.Time) (sdk.AccessDecisionOutcome, string, error) {
	ce, err := NewCedarEvaluator(content, nil)
	if err != nil {
		return sdk.AccessOutcomeIndeterminate, "", err
	}
	when := at
	if when.IsZero() {
		when = time.Unix(0, 0).UTC()
	}
	ce.now = func() time.Time { return when }
	dec, err := ce.Evaluate(context.Background(), questionToAuthRequest(tenant, question))
	if err != nil {
		return sdk.AccessOutcomeIndeterminate, "", err
	}
	if dec.Allow {
		return sdk.AccessOutcomeAllow, dec.Reason, nil
	}
	return sdk.AccessOutcomeDeny, dec.Reason, nil
}

func questionToAuthRequest(tenant model.TenantID, q sdk.AccessQuestion) auth.Request {
	kind := auth.KindUser
	switch strings.ToLower(strings.TrimSpace(q.PrincipalKind)) {
	case "token", "service", "external":
		kind = auth.KindToken
	}
	cred := model.ID(strings.TrimSpace(q.PrincipalRef))
	if cred == "" {
		cred = model.ID(strings.TrimSpace(q.ActorRef))
	}
	sens := ""
	if q.Context != nil {
		sens = q.Context["sensitivity"]
	}
	return auth.Request{
		Principal:  auth.Principal{Kind: kind, CredID: cred},
		Permission: auth.Permission(q.Action),
		Tenant:     tenant,
		Resource: auth.ResourceAttrs{
			Kind:        q.ResourceKind,
			ID:          q.ResourceRef,
			Sensitivity: sens,
		},
	}
}

// QuestionForReplay is the canonical question a CLI/API caller must match to
// find a recorded decision by time. Extra fields the original producer set
// (issuer, namespace, …) change the digest; callers that only know
// principal/resource/action will not match those rows and must pass
// --decision-id instead. That mismatch is reported as a missing decision,
// never as a live-policy guess.
func QuestionForReplay(principal, sourceInstance, resourceKind, resource, action, actionVocabulary string) sdk.AccessQuestion {
	return sdk.AccessQuestion{
		SchemaVersion:    sdk.AccessEvidenceSchemaVersion,
		ActorRef:         strings.TrimSpace(principal),
		PrincipalRef:     strings.TrimSpace(principal),
		SourceInstance:   strings.TrimSpace(sourceInstance),
		ResourceKind:     strings.TrimSpace(resourceKind),
		ResourceRef:      strings.TrimSpace(resource),
		Action:           strings.TrimSpace(action),
		ActionVocabulary: strings.TrimSpace(actionVocabulary),
	}
}
