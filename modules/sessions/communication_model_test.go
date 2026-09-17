// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/sha256"
	"fmt"
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

// communicationReadProjection names one production struct that embeds a
// durable communication entity only to present it: a response shape that is
// never registered, migrated or stored. The inventory recognises a projection by
// its EXACT shape — one anonymous embedding of the named durable entity plus
// exactly the listed named fields, each with its type and JSON tag — and fails
// closed on any other shape, so a projection cannot quietly become a second
// entity, hide a new persistence field, or vanish from the census unnoticed.
// This is not a second model manifest: it lists what is deliberately NOT a model.
type communicationReadProjection struct {
	of     string
	fields []communicationReadProjectionField
}

type communicationReadProjectionField struct {
	name, typeName, tag string
}

// communicationReadProjections is the closed set of recognised read projections.
// ChannelCatalogItem is one visible Channel with the caller's own current local
// access bits, kept flat in JSON by embedding Channel (communication_channel_catalog.go).
// It has no descriptor of its own; the catalog index belongs to ChannelGrant.
var communicationReadProjections = map[string]communicationReadProjection{
	"ChannelCatalogItem": {
		of: "Channel",
		fields: []communicationReadProjectionField{
			{name: "MyAccess", typeName: "ChannelCatalogAccess", tag: "`json:\"my_access\"`"},
		},
	},
}

// communicationCollectTypeSpecs adds every named type declared in file to types.
func communicationCollectTypeSpecs(file *ast.File, types map[string]ast.Expr) {
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

// communicationDurableModelInventory classifies every named type in types by the
// communication entity class it embeds, transitively: "mutable" for
// MutableCommunicationEntity, "append_only" for AppendOnlyCommunicationEntity.
// A struct embedding both classes is an error. Every declared read projection
// must exist, must have exactly its declared shape, and must project a type that
// is itself durable; only then is it reported under projected instead of durable.
// No name, prefix or suffix is ever used to exclude a type.
func communicationDurableModelInventory(
	types map[string]ast.Expr,
	projections map[string]communicationReadProjection,
) (durable, projected map[string]string, err error) {
	var classify func(string, map[string]bool) (string, error)
	var classifyExpr func(ast.Expr, map[string]bool) (string, error)
	classifyExpr = func(expression ast.Expr, visiting map[string]bool) (string, error) {
		switch typed := expression.(type) {
		case *ast.Ident:
			return classify(typed.Name, visiting)
		case *ast.StarExpr:
			return classifyExpr(typed.X, visiting)
		case *ast.ParenExpr:
			return classifyExpr(typed.X, visiting)
		default:
			return "", nil
		}
	}
	classify = func(name string, visiting map[string]bool) (string, error) {
		if name == "MutableCommunicationEntity" {
			return "mutable", nil
		}
		if name == "AppendOnlyCommunicationEntity" {
			return "append_only", nil
		}
		if visiting[name] {
			return "", nil
		}
		visiting[name] = true
		defer delete(visiting, name)
		expression, present := types[name]
		if !present {
			return "", nil
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
				candidate, err := classifyExpr(field.Type, visiting)
				if err != nil {
					return "", err
				}
				if candidate != "" {
					if class != "" && class != candidate {
						return "", fmt.Errorf("%s embeds mixed communication entity classes", name)
					}
					class = candidate
				}
			}
			return class, nil
		default:
			return "", nil
		}
	}
	projected = make(map[string]string, len(projections))
	for name, projection := range projections {
		expression, present := types[name]
		if !present {
			return nil, nil, fmt.Errorf("declared read projection %s is not a production type", name)
		}
		structType, ok := expression.(*ast.StructType)
		if !ok {
			return nil, nil, fmt.Errorf("declared read projection %s is not a struct", name)
		}
		ofClass, err := classify(projection.of, map[string]bool{})
		if err != nil {
			return nil, nil, err
		}
		if ofClass == "" {
			return nil, nil, fmt.Errorf("read projection %s projects %s, which is not a durable entity",
				name, projection.of)
		}
		if got, want := len(structType.Fields.List), len(projection.fields)+1; got != want {
			return nil, nil, fmt.Errorf("read projection %s has %d fields, want exactly %d", name, got, want)
		}
		embedded := 0
		named := make(map[string]*ast.Field, len(projection.fields))
		for _, field := range structType.Fields.List {
			if len(field.Names) == 0 {
				ident, ok := field.Type.(*ast.Ident)
				if !ok || ident.Name != projection.of || field.Tag != nil {
					return nil, nil, fmt.Errorf("read projection %s embeds something other than %s",
						name, projection.of)
				}
				embedded++
				continue
			}
			if len(field.Names) != 1 {
				return nil, nil, fmt.Errorf("read projection %s declares a grouped field", name)
			}
			named[field.Names[0].Name] = field
		}
		if embedded != 1 {
			return nil, nil, fmt.Errorf("read projection %s embeds %s %d times, want once", name, projection.of, embedded)
		}
		for _, want := range projection.fields {
			field, ok := named[want.name]
			if !ok {
				return nil, nil, fmt.Errorf("read projection %s lacks field %s", name, want.name)
			}
			ident, ok := field.Type.(*ast.Ident)
			if !ok || ident.Name != want.typeName {
				return nil, nil, fmt.Errorf("read projection %s field %s is not of type %s",
					name, want.name, want.typeName)
			}
			if field.Tag == nil || field.Tag.Value != want.tag {
				return nil, nil, fmt.Errorf("read projection %s field %s does not carry tag %s",
					name, want.name, want.tag)
			}
		}
		projected[name] = projection.of
	}
	durable = make(map[string]string)
	for name := range types {
		if name == "MutableCommunicationEntity" || name == "AppendOnlyCommunicationEntity" {
			continue
		}
		if _, isProjection := projected[name]; isProjection {
			continue
		}
		class, err := classify(name, map[string]bool{})
		if err != nil {
			return nil, nil, err
		}
		if class != "" {
			durable[name] = class
		}
	}
	return durable, projected, nil
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
	// The recognised read projections, and exactly these: a projection that
	// disappears, or a new one that is not declared here, fails this test.
	wantProjected := map[string]string{"ChannelCatalogItem": "Channel"}
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
		communicationCollectTypeSpecs(file, types)
	}
	got, projected, err := communicationDurableModelInventory(types, communicationReadProjections)
	if err != nil {
		t.Fatalf("durable communication model inventory: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("durable communication model inventory = %v, want exactly %v", got, want)
	}
	if !reflect.DeepEqual(projected, wantProjected) {
		t.Fatalf("recognised read projections = %v, want exactly %v", projected, wantProjected)
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

// communicationSyntheticTypes parses one synthetic Go source and returns its
// named types, so the inventory classifier can be exercised on shapes the
// production package must never contain.
func communicationSyntheticTypes(t *testing.T, source string) map[string]ast.Expr {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "synthetic.go", "package sessions\n"+source, 0)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	types := make(map[string]ast.Expr)
	communicationCollectTypeSpecs(file, types)
	return types
}

func TestCommunicationModelInventoryFailsClosedOnDeformedProjectionsAndNewEntities(t *testing.T) {
	t.Parallel()

	const roots = `
type CommunicationEntity struct{ ID string }
type MutableCommunicationEntity struct{ CommunicationEntity }
type AppendOnlyCommunicationEntity struct{ CommunicationEntity }
type Channel struct{ MutableCommunicationEntity }
type Message struct{ MutableCommunicationEntity }
type ChannelCatalogAccess struct{ Read, Write, Admin bool }
`
	const projection = "ChannelCatalogItem"
	projections := map[string]communicationReadProjection{
		projection: communicationReadProjections[projection],
	}

	// Positive control: the exact production shape is recognised as a
	// projection of Channel and leaves the durable census untouched.
	durable, projected, err := communicationDurableModelInventory(communicationSyntheticTypes(t, roots+`
type ChannelCatalogItem struct {
	Channel
	MyAccess ChannelCatalogAccess `+"`json:\"my_access\"`"+`
}
`), projections)
	if err != nil {
		t.Fatalf("exact projection shape: %v", err)
	}
	if want := map[string]string{"Channel": "mutable", "Message": "mutable"}; !reflect.DeepEqual(durable, want) {
		t.Fatalf("durable census with the exact projection = %v, want %v", durable, want)
	}
	if want := map[string]string{projection: "Channel"}; !reflect.DeepEqual(projected, want) {
		t.Fatalf("recognised projections = %v, want %v", projected, want)
	}

	// Every deformation of the declared shape is an error naming the projection,
	// never a silent exclusion and never a silent promotion to a durable entity.
	for _, deformed := range []struct {
		name, source string
	}{
		{"extra persistence field", `
type ChannelCatalogItem struct {
	Channel
	MyAccess ChannelCatalogAccess ` + "`json:\"my_access\"`" + `
	Hidden   string               ` + "`json:\"hidden\"`" + `
}`},
		{"projects another entity", `
type ChannelCatalogItem struct {
	Message
	MyAccess ChannelCatalogAccess ` + "`json:\"my_access\"`" + `
}`},
		{"embeds a second entity", `
type ChannelCatalogItem struct {
	Channel
	Message
	MyAccess ChannelCatalogAccess ` + "`json:\"my_access\"`" + `
}`},
		{"renamed JSON field", `
type ChannelCatalogItem struct {
	Channel
	MyAccess ChannelCatalogAccess ` + "`json:\"access\"`" + `
}`},
		{"untagged access field", `
type ChannelCatalogItem struct {
	Channel
	MyAccess ChannelCatalogAccess
}`},
		{"retyped access field", `
type ChannelCatalogItem struct {
	Channel
	MyAccess string ` + "`json:\"my_access\"`" + `
}`},
		{"missing access field", `
type ChannelCatalogItem struct {
	Channel
}`},
		{"embedded pointer instead of value", `
type ChannelCatalogItem struct {
	*Channel
	MyAccess ChannelCatalogAccess ` + "`json:\"my_access\"`" + `
}`},
		{"not a struct", `
type ChannelCatalogItem Channel`},
		{"declared projection absent from the package", ``},
	} {
		_, _, err := communicationDurableModelInventory(communicationSyntheticTypes(t, roots+deformed.source), projections)
		if err == nil || !strings.Contains(err.Error(), projection) {
			t.Fatalf("%s: inventory error = %v, want an error naming %s", deformed.name, err, projection)
		}
	}

	// A projection may only project a durable entity: declaring one over a plain
	// value type is refused, so the table cannot be used to hide anything else.
	_, _, err = communicationDurableModelInventory(communicationSyntheticTypes(t, roots+`
type ChannelCatalogItem struct {
	ChannelCatalogAccess
	MyAccess ChannelCatalogAccess `+"`json:\"my_access\"`"+`
}
`), map[string]communicationReadProjection{projection: {
		of:     "ChannelCatalogAccess",
		fields: communicationReadProjections[projection].fields,
	}})
	if err == nil || !strings.Contains(err.Error(), "not a durable entity") {
		t.Fatalf("projection of a non-entity: inventory error = %v, want refusal", err)
	}

	// A new entity is always counted, whatever its name looks like: nothing is
	// filtered by an Item/Page/Response suffix, and the exact-match census in the
	// inventory test is what turns such an addition into a review.
	durable, _, err = communicationDurableModelInventory(communicationSyntheticTypes(t, roots+`
type ChannelCatalogItem struct {
	Channel
	MyAccess ChannelCatalogAccess `+"`json:\"my_access\"`"+`
}
type ChannelCatalogShadow struct{ MutableCommunicationEntity }
type CatalogItem struct{ MutableCommunicationEntity }
type CatalogPage struct{ AppendOnlyCommunicationEntity }
type CatalogResponse struct{ ChannelCatalogItem }
`), projections)
	if err != nil {
		t.Fatalf("new entities: %v", err)
	}
	for name, class := range map[string]string{
		"ChannelCatalogShadow": "mutable", "CatalogItem": "mutable", "CatalogPage": "append_only",
		"CatalogResponse": "mutable",
	} {
		if durable[name] != class {
			t.Fatalf("new entity %s classified %q, want %q (census = %v)", name, durable[name], class, durable)
		}
	}

	// Mixed classes remain an error, as before this refinement.
	_, _, err = communicationDurableModelInventory(communicationSyntheticTypes(t, roots+`
type Mixed struct {
	MutableCommunicationEntity
	AppendOnlyCommunicationEntity
}
`), nil)
	if err == nil || !strings.Contains(err.Error(), "mixed") {
		t.Fatalf("mixed entity classes: inventory error = %v, want refusal", err)
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
