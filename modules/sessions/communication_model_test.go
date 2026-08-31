// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/sha256"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var communicationTestNow = time.Date(2026, 8, 14, 18, 0, 0, 0, time.UTC)

func communicationTestMutable(at time.Time) MutableCommunicationEntity {
	return MutableCommunicationEntity{
		CommunicationEntity: CommunicationEntity{
			ID: model.NewID(), TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID(),
			Version: 1, CreatedAt: at,
		},
		UpdatedAt: at,
	}
}

func communicationTestAppendOnly(at time.Time) AppendOnlyCommunicationEntity {
	entity := communicationTestMutable(at).CommunicationEntity
	return AppendOnlyCommunicationEntity{CommunicationEntity: entity}
}

func communicationTestPayload(t *testing.T) ProtectedPayload {
	t.Helper()
	return communicationTestPayloadForSlot(t, PayloadSlotMessage)
}

func communicationTestPayloadForSlot(t *testing.T, slot ProtectedPayloadSlot) ProtectedPayload {
	t.Helper()
	var content any
	switch slot {
	case PayloadSlotMessage:
		content = MessageContent{
			Subject: "K3",
			Blocks:  []MessageContentBlock{{Type: ContentBlockText, Format: TextMarkdown, Text: "## payload"}},
		}
	case PayloadSlotMessageTerminalReason, PayloadSlotAckNote, PayloadSlotHandoffTerminalReason:
		content = CommunicationReasonContent{Code: "test_reason", Text: "bounded test reason"}
	case PayloadSlotDecisionRequest:
		content = DecisionRequestContent{
			Question: "Proceed?",
			Choices:  []DecisionChoice{{Key: "yes", Label: "Yes"}, {Key: "no", Label: "No"}},
		}
	case PayloadSlotDecisionResponse:
		content = DecisionResponseContent{
			ChoiceKey: "yes",
			Reason:    CommunicationReasonContent{Code: "accepted", Text: "accepted in test"},
		}
	case PayloadSlotHandoff:
		content = HandoffContent{Summary: "Handoff summary", NextAction: "Continue the work"}
	default:
		t.Fatalf("unsupported protected payload test slot %q", slot)
	}
	raw, err := CanonicalProtectedPayloadSlot(slot, content)
	if err != nil {
		t.Fatalf("CanonicalProtectedPayloadSlot(%s): %v", slot, err)
	}
	digest := sha256.Sum256(raw)
	schema, ok := slot.schema()
	if !ok {
		t.Fatalf("missing test schema for slot %q", slot)
	}
	return ProtectedPayload{
		Encoding: PayloadPlainJSON, PlainJSON: raw, Schema: schema,
		Digest: digest[:], ProtectionGeneration: 1,
	}
}

func communicationTestDelivery(state MessageDeliveryState, required bool, due time.Time) MessageDelivery {
	entity := communicationTestMutable(communicationTestNow)
	delivery := MessageDelivery{
		MutableCommunicationEntity: entity,
		MessageID:                  model.NewID(), Recipient: RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		RecipientEpoch: 1, DeliverySeq: 1, Required: required, RouteReasons: []RouteReason{"direct"},
		WakePolicy: WakeNone, State: state, AvailableAt: communicationTestNow,
	}
	if required {
		delivery.AckDueAt = &due
	}
	return delivery
}

func cleanAuthorityEvidence() AuthorityEvidence {
	return AuthorityEvidence{Verdict: VerdictClean, Code: "current", EvidenceRef: "fixture-current"}
}

func TestCommunicationModelInventoryIsExactlyTwentySeven(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"Channel": "mutable", "ChannelGrant": "mutable", "ChannelSubscription": "mutable",
		"ChannelLabelDefinition": "mutable", "ChannelRouteRule": "mutable",
		"CommunicationEndpoint": "mutable", "Message": "mutable", "MessageAudience": "append_only",
		"MessageAudienceRecipient": "append_only", "MessageDelivery": "mutable", "InboxCursor": "mutable",
		"InboxCursorBarrier": "mutable", "MessageAck": "append_only", "CommunicationGuard": "mutable",
		"DecisionRequest": "mutable", "DecisionResponse": "append_only", "Handoff": "mutable",
		"DeliveryDispatch": "mutable", "DeliveryAttempt": "mutable",
		"CommunicationCommandReceipt": "append_only",
		"ProtocolBindingSpec":         "mutable", "ProtocolBinding": "mutable",
		"ProtocolReplayGuard": "append_only", "ProtocolSubscriptionCursor": "mutable",
		"ProtocolSubscriptionEvent": "append_only",
		"storedProtocolBindingSpec": "mutable", "storedProtocolBinding": "mutable",
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate communication model test")
	}
	entries, err := os.ReadDir(filepath.Dir(currentFile))
	if err != nil {
		t.Fatalf("read sessions package: %v", err)
	}
	types := make(map[string]ast.Expr)
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, filepath.Join(filepath.Dir(currentFile), entry.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, spec := range general.Specs {
				typeSpec := spec.(*ast.TypeSpec)
				types[typeSpec.Name.Name] = typeSpec.Type
			}
		}
	}
	var classify func(string, map[string]bool) string
	var classifyExpr func(ast.Expr, map[string]bool) string
	classifyExpr = func(expression ast.Expr, visiting map[string]bool) string {
		switch typed := expression.(type) {
		case *ast.Ident:
			return classify(typed.Name, visiting)
		case *ast.StarExpr:
			return classifyExpr(typed.X, visiting)
		case *ast.ParenExpr:
			return classifyExpr(typed.X, visiting)
		default:
			return ""
		}
	}
	classify = func(name string, visiting map[string]bool) string {
		if name == "MutableCommunicationEntity" {
			return "mutable"
		}
		if name == "AppendOnlyCommunicationEntity" {
			return "append_only"
		}
		if visiting[name] {
			return ""
		}
		visiting[name] = true
		defer delete(visiting, name)
		expression, present := types[name]
		if !present {
			return ""
		}
		switch typed := expression.(type) {
		case *ast.Ident, *ast.StarExpr, *ast.ParenExpr:
			return classifyExpr(expression, visiting)
		case *ast.StructType:
			class := ""
			for _, field := range typed.Fields.List {
				if len(field.Names) != 0 {
					continue
				}
				candidate := classifyExpr(field.Type, visiting)
				if candidate != "" {
					if class != "" && class != candidate {
						t.Fatalf("%s embeds mixed communication entity classes", name)
					}
					class = candidate
				}
			}
			return class
		default:
			return ""
		}
	}
	got := make(map[string]string)
	for name := range types {
		if name == "MutableCommunicationEntity" || name == "AppendOnlyCommunicationEntity" {
			continue
		}
		if class := classify(name, map[string]bool{}); class != "" {
			got[name] = class
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("durable communication model inventory = %v, want exactly %v", got, want)
	}
	mutable, appendOnly := 0, 0
	for _, class := range got {
		if class == "mutable" {
			mutable++
		} else {
			appendOnly++
		}
	}
	if mutable != 20 || appendOnly != 7 {
		t.Fatalf("durable model classes = %d mutable/%d append-only, want 20/7", mutable, appendOnly)
	}
}

func TestProtectedPayloadRequiresExactEnvelopeAndAAD(t *testing.T) {
	t.Parallel()

	plain := communicationTestPayload(t)
	if err := ValidateProtectedPayload(plain); err != nil {
		t.Fatalf("valid plain payload: %v", err)
	}
	mutated := plain
	mutated.DigestKeyVersion = "digest-v1"
	if err := ValidateProtectedPayload(mutated); err == nil {
		t.Fatal("plain payload accepted a keyed digest version")
	}
	mutated = plain
	mutated.PlainJSON = []byte(`{ "blocks": [], "subject": "x" }`)
	mutatedDigest := sha256.Sum256(mutated.PlainJSON)
	mutated.Digest = mutatedDigest[:]
	if err := ValidateProtectedPayload(mutated); err == nil {
		t.Fatal("non-canonical plain JSON accepted")
	}

	sealed := ProtectedPayload{
		Encoding: PayloadSealedV1,
		Sealed:   &SealedPayload{Ciphertext: []byte("ciphertext"), KeyVersion: "seal-v7"},
		Schema:   "communication.message.v1", Digest: bytes.Repeat([]byte{1}, sha256.Size),
		SealKeyVersion: "seal-v7", DigestKeyVersion: "digest-v11", ProtectionGeneration: 3,
	}
	if err := ValidateProtectedPayload(sealed); err != nil {
		t.Fatalf("distinct seal/digest versions rejected: %v", err)
	}
	sealed.Sealed.KeyVersion = "active-key"
	if err := ValidateProtectedPayload(sealed); err == nil {
		t.Fatal("envelope/column key-version mismatch accepted")
	}

	aad := ContentAAD{
		TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID(), ChannelID: model.NewID(),
		EntityKind: "sessions.message", EntityID: model.NewID(), Schema: "communication.message.v1",
		ProtectionGeneration: 3,
	}
	if err := ValidateContentAAD(aad); err != nil {
		t.Fatalf("valid AAD: %v", err)
	}
	if _, found := reflect.TypeOf(ContentAAD{}).FieldByName("KeyVersion"); found {
		t.Fatal("AAD must not contain a key version before Seal chooses one")
	}
}

func TestMessageContentLimitsAndMarkdownHeadingIsPayload(t *testing.T) {
	t.Parallel()

	content := MessageContent{
		Subject: "heading",
		Blocks:  []MessageContentBlock{{Type: ContentBlockText, Format: TextMarkdown, Text: "## still body"}},
	}
	raw, err := CanonicalMessageContent(content)
	if err != nil {
		t.Fatalf("markdown payload: %v", err)
	}
	if !bytes.Contains(raw, []byte("## still body")) {
		t.Fatalf("markdown heading was not preserved: %s", raw)
	}

	content.Blocks[0].Text = strings.Repeat("x", maxMessageTextBytes+1)
	if _, err := CanonicalMessageContent(content); err == nil {
		t.Fatal("oversized text block accepted")
	}
	content.Blocks = make([]MessageContentBlock, maxMessageBlocks+1)
	for i := range content.Blocks {
		content.Blocks[i] = MessageContentBlock{Type: ContentBlockStatus, Code: "ok"}
	}
	if _, err := CanonicalMessageContent(content); err == nil {
		t.Fatal("65 blocks accepted")
	}

	content.Blocks = make([]MessageContentBlock, maxMessageReferences)
	for i := range content.Blocks {
		content.Blocks[i] = MessageContentBlock{
			Type:      ContentBlockReference,
			Reference: &ContentReference{Kind: "artifact", Ref: model.NewID().String()},
		}
	}
	if _, err := CanonicalMessageContent(content); err != nil {
		t.Fatalf("64 references rejected: %v", err)
	}
}

func TestCommunicationPrincipalKeepsServerFactsAndExternalAgentDistinct(t *testing.T) {
	t.Parallel()

	user := CommunicationPrincipal{UserID: model.NewID()}
	if err := ValidateCommunicationPrincipal(user); err != nil {
		t.Fatalf("valid user principal: %v", err)
	}
	mixed := user
	mixed.SessionRunRef = model.NewID().String()
	mixed.SessionFence = 9
	mixed.SessionWorkspaceID = model.NewID()
	if err := ValidateCommunicationPrincipal(mixed); err == nil {
		t.Fatal("user principal accepted session-only facts")
	}

	session := CommunicationPrincipal{
		AgentExternalID: "provider-agent-7", SessionID: "osn_" + model.NewID().String(),
		SessionRunRef: model.NewID().String(), SessionFence: 4, SessionWorkspaceID: model.NewID(),
		PurposeRestricted: true,
	}
	if err := ValidateCommunicationPrincipal(session); err != nil {
		t.Fatalf("valid communication-session principal: %v", err)
	}
	if recipient, ok := CanonicalPrincipalRecipient(session); !ok || recipient.Kind != RecipientSession ||
		recipient.Ref != session.SessionID {
		t.Fatalf("session canonical recipient = %#v, %v", recipient, ok)
	}

	agent := CommunicationPrincipal{AgentExternalID: "provider-agent-7"}
	if err := ValidateCommunicationPrincipal(agent); err != nil {
		t.Fatalf("valid unresolved agent: %v", err)
	}
	if recipient, ok := CanonicalPrincipalRecipient(agent); ok || recipient != (RecipientRef{}) {
		t.Fatalf("external AgentIdentity became a canonical recipient: %#v", recipient)
	}
	if _, found := reflect.TypeOf(CommunicationPrincipal{}).FieldByName("AgentRef"); found {
		t.Fatal("CommunicationPrincipal must not label ExternalID as canonical AgentRef")
	}
}

func TestReadWitnessIsTriStateAndCarriesCanonicalFacts(t *testing.T) {
	t.Parallel()

	entity := EntityRef{
		TenantID: model.TenantID(model.NewID()), Kind: "sessions.message",
		ID: model.NewID(), WorkspaceID: model.NewID(),
	}
	facts := []store.AuthorizationFactRef{
		{Kind: "core.identity", ID: model.NewID(), Version: 2},
		{Kind: "core.agent", ID: model.NewID(), Version: 4},
	}
	principal := CommunicationPrincipal{UserID: model.NewID()}
	witness := ReadWitness{
		Outcome: ReadAllow, Code: "authorized", Entity: entity, Operation: CommunicationRead,
		Principal: principal, ObservedAt: communicationTestNow,
		FreshUntil:     communicationTestNow.Add(time.Minute),
		CorePermission: cleanAuthorityEvidence(), ResourceGuard: cleanAuthorityEvidence(),
		ForbidAbsence: cleanAuthorityEvidence(), Facts: facts,
	}
	if err := ValidateReadWitness(witness); err != nil {
		t.Fatalf("valid ALLOW witness: %v", err)
	}
	canonical, err := CanonicalAuthorizationFacts(facts)
	if err != nil {
		t.Fatalf("canonical facts: %v", err)
	}
	if canonical[0].Kind > canonical[1].Kind {
		t.Fatalf("facts not sorted: %#v", canonical)
	}

	witness.Outcome = ReadDeny
	if err := ValidateReadWitness(witness); err == nil {
		t.Fatal("DENY outcome accepted with all gates clean")
	}
	if _, err := CanonicalAuthorizationFacts([]store.AuthorizationFactRef{facts[0], facts[0]}); err == nil {
		t.Fatal("duplicate authorization fact accepted")
	}
	tooMany := make([]store.AuthorizationFactRef, 65)
	for i := range tooMany {
		tooMany[i] = store.AuthorizationFactRef{Kind: "core.fact", ID: model.NewID(), Version: 1}
	}
	if _, err := CanonicalAuthorizationFacts(tooMany); err == nil {
		t.Fatal("authority locker fact bound exceeded")
	}
}

func TestCanonicalAuthorizationFactsAcceptsOnlyExactAuthorizationEpochKind(t *testing.T) {
	t.Parallel()

	fact := store.AuthorizationFactRef{
		Kind: model.AuthorizationEpochKind, ID: model.NewID(), Version: 7,
	}
	canonical, err := CanonicalAuthorizationFacts([]store.AuthorizationFactRef{fact})
	if err != nil {
		t.Fatalf("canonical authorization epoch: %v", err)
	}
	if len(canonical) != 1 || canonical[0] != fact {
		t.Fatalf("canonical authorization epoch = %#v, want %#v", canonical, fact)
	}
	for _, lookalike := range []model.Kind{
		"core.authorization_epochs",
		"core.Authorization_epoch",
		"authorization_epoch",
	} {
		fact.Kind = lookalike
		if _, err := CanonicalAuthorizationFacts([]store.AuthorizationFactRef{fact}); err == nil {
			t.Fatalf("lookalike authorization epoch kind %q accepted", lookalike)
		}
	}
}

func TestDirectorySnapshotPreservesSelectorRecipientCausality(t *testing.T) {
	t.Parallel()

	scope := DirectoryScopeRef{TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID()}
	selector := AudienceSelector{
		Kind: AudienceUserGroup, Ref: model.NewID().String(), Required: true, WakePolicy: WakeAll,
	}
	recipient := RecipientSnapshot{
		Scope: scope, Recipient: RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		RecipientEpoch: 3, DirectoryEpoch: 9, Eligible: true,
	}
	fact := store.AuthorizationFactRef{Kind: "core.user_group_member", ID: model.NewID(), Version: 5}
	hash, err := CanonicalDirectoryRosterHash(scope, 9, []RecipientSnapshot{recipient})
	if err != nil {
		t.Fatalf("roster hash: %v", err)
	}
	snapshot := DirectorySnapshot{
		Scope: scope, Epoch: 9, Selectors: []AudienceSelector{selector},
		Recipients: []RecipientSnapshot{recipient},
		Contributions: []ResolvedAudienceContribution{{
			SelectorOrdinal: 1, Selector: selector, Recipient: recipient,
			Required: true, WakePolicy: WakeAll, RouteReasons: []RouteReason{"group"},
			CausalKind: CausalUserGroup, CausalRef: selector.Ref, CausalFact: &fact,
		}},
		RosterHash: hash, ObservedAt: communicationTestNow,
		FreshUntil: communicationTestNow.Add(time.Minute),
	}
	if len(snapshot.Contributions) != 1 || snapshot.Contributions[0].CausalFact == nil ||
		snapshot.Contributions[0].Selector != selector {
		t.Fatalf("snapshot lost selector causality: %#v", snapshot)
	}
	if err := ValidateDirectorySnapshotForSelectors(snapshot, []AudienceSelector{selector}); err != nil {
		t.Fatalf("valid snapshot: %v", err)
	}
}

// Compile-time proof that WP-1's nominal aliases use G's single issuer seam.
var (
	_ CommunicationCredentialSpec = CommunicationSessionCredentialRequest{}
	_ CommunicationCredential     = CommunicationSessionCredential{}
)
