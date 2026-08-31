// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
)

var workTokenRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

var (
	workStatuses         = setOf("draft", "ready", "active", "blocked", "review", "completed", "failed", "canceled")
	terminalWorkStatuses = setOf("completed", "failed", "canceled")
	workPriorities       = setOf("p0", "p1", "p2", "p3")
	workOwnerKinds       = setOf("user", "agent", "session")
	workProvenanceKinds  = setOf("human", "workflow", "a2a", "mcp", "migration", "system")
	acceptanceStates     = setOf("pending", "passed", "failed", "waived")
	decisionOperations   = setOf("set", "supersede", "revoke")
)

func setOf(v ...string) map[string]bool {
	out := make(map[string]bool, len(v))
	for _, s := range v {
		out[s] = true
	}
	return out
}

type workError struct {
	status  int
	code    string
	verdict AssessmentVerdict
	cause   error
	// field nombra el campo del comando que provocó el rechazo, en el vocabulario del
	// LLAMANTE (`blocked_code`, no `Code`). Vacío cuando el rechazo no es de un campo.
	field string
}

func (e *workError) Error() string {
	if e.cause != nil {
		return e.code + ": " + e.cause.Error()
	}
	return e.code
}

func (e *workError) Unwrap() error { return e.cause }

func broken(status int, code string) error {
	return &workError{status: status, code: code, verdict: VerdictBroken}
}

// brokenField es `broken` que además dice QUÉ CAMPO sobra o falta.
//
// ⛔ POR QUÉ EXISTE, medido el 2026-08-25 conduciendo el kernel con un CLI de tercero.
// `item.block` sin `blocked_code` contestaba `invalid_command` y NADA MÁS. Un agente
// competente gastó TRES viajes deduciéndolo: validó y falló, probó `item.unblock` para
// distinguir «comando desconocido» de «comando mal formado» —recibió `illegal_transition`,
// o sea el comando SÍ existía— y sólo entonces buscó el campo que faltaba. Y la asimetría
// era la peor posible: el camino de ÉXITO sí nombraba su esquema (`work_command_v1`) y el
// de FALLO no nombraba nada.
//
// ⛔ Y EL NOMBRE VA EN EL VOCABULARIO DE QUIEN LLAMA, NO EN EL DE LA STRUCT.
// `normalizeWorkCommand` mapea `blocked_code` -> `Code` para `item.block` y
// `terminal_code` -> `Code` para `item.fail`/`item.cancel`. Un error que dijera «code»
// mandaría al llamante a la bandera equivocada: exactamente el defecto que esto arregla,
// con una capa más de disfraz.
func brokenField(status int, code, field string) error {
	return &workError{status: status, code: code, verdict: VerdictBroken, field: field}
}

// fieldCheck es un predicado CON NOMBRE. firstBadField devuelve el nombre del primero que
// falla, o "" si todos pasan.
//
// ⛔ Los predicados se evalúan TODOS (no hay cortocircuito como en `||`). Es seguro aquí
// porque son funciones puras sobre campos ya presentes; ninguna indexa una rodaja que otra
// condición guarde. Donde eso pasaba —`cmd.Acceptance[0]`— la guarda ya estaba en un `if`
// aparte y se ha dejado como estaba.
type fieldCheck struct {
	field string
	ok    bool
}

func firstBadField(checks ...fieldCheck) string {
	for _, c := range checks {
		if !c.ok {
			return c.field
		}
	}
	return ""
}

func unknown(code string, cause error) error {
	return &workError{status: 503, code: code, verdict: VerdictUnknown, cause: cause}
}

func asWorkError(err error) *workError {
	var we *workError
	if errors.As(err, &we) {
		return we
	}
	return nil
}

func boundedToken(s string, max int) bool {
	return len(s) >= 1 && len(s) <= max && utf8.ValidString(s) && workTokenRE.MatchString(s)
}

func boundedText(s string, min, max int) bool {
	return len(s) >= min && len(s) <= max && utf8.ValidString(s)
}

func decodeHash(s string, required bool) ([]byte, error) {
	if s == "" && !required {
		return nil, nil
	}
	s = strings.TrimPrefix(s, "sha256:")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != sha256.Size {
		return nil, brokenField(400, "invalid_command", "provenance_hash|evidence_hash (sha256 de 32 bytes, con o sin prefijo «sha256:»)")
	}
	return b, nil
}

func hashBytes(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

func hexHash(b []byte) string { return hex.EncodeToString(b) }

// canonicalJSON is the kernel's constrained JCS boundary. It rejects trailing
// input and non-integral numbers, disables HTML rewriting and relies on
// encoding/json's lexical map-key ordering. Work command documents contain no
// floating-point fields, avoiding the only JCS number-normalisation ambiguity.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var normalized any
	if err := dec.Decode(&normalized); err != nil {
		return nil, err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("work canonical json: trailing input")
	}
	if err := rejectFractionalNumbers(normalized); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalized); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), nil
}

func rejectFractionalNumbers(v any) error {
	switch x := v.(type) {
	case json.Number:
		if _, err := x.Int64(); err != nil {
			return fmt.Errorf("work canonical json: non-integral number")
		}
	case []any:
		for _, item := range x {
			if err := rejectFractionalNumbers(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, item := range x {
			if err := rejectFractionalNumbers(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRefs(refs []ContextRef) error {
	if len(refs) > 64 {
		return brokenField(400, "invalid_command", "context_refs (maximo 64)")
	}
	for _, ref := range refs {
		if bad := firstBadField(
			fieldCheck{"context_refs[].kind", boundedToken(ref.Kind, 64)},
			fieldCheck{"context_refs[].ref", boundedText(ref.Ref, 1, 512)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
		if _, err := decodeHash(ref.Hash, false); err != nil {
			return err
		}
	}
	b, err := canonicalJSON(refs)
	if err != nil || len(b) > 16*1024 {
		return brokenField(400, "invalid_command", "context_refs (el conjunto excede 16 KiB serializado)")
	}
	return nil
}

func validateAcceptanceInput(in AcceptanceInput, evaluating bool) error {
	// ⛔ ESTE AYUDANTE ESTABA FUERA DE LA GUARDA DE COMPLETITUD, y por eso conservaba
	// rechazos mudos que un contraste alcanzo desde `acceptance.add` con una clave mal
	// formada. La guarda escaneaba SOLO el cuerpo de `validateCommandSyntax`: un test que
	// mide un trozo del camino declara limpio el resto. Ahora cubre el fichero entero.
	if bad := firstBadField(
		fieldCheck{"criterion_id (no se envia al CREAR un criterio)", in.ID.IsZero()},
		fieldCheck{"criterion_key (bandera --criterion-key)", in.Key == "" || boundedToken(in.Key, 64)},
		fieldCheck{"ordinal", in.Ordinal >= 0},
		fieldCheck{"statement (bandera --statement)", in.Statement == "" || boundedText(in.Statement, 1, 4*1024)},
	); bad != "" {
		return brokenField(400, "invalid_command", bad)
	}
	if !evaluating {
		if bad := firstBadField(
			fieldCheck{"state (al crear solo vale «pending» o vacio)", in.State == "" || in.State == "pending"},
			fieldCheck{"evidence_ref (no se envia al crear)", in.EvidenceRef == ""},
			fieldCheck{"evidence_hash (no se envia al crear)", in.EvidenceHash == ""},
			fieldCheck{"waiver_decision_id (no se envia al crear)", in.WaiverDecisionID.IsZero()},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	} else {
		// Evaluation changes custody state/evidence only. Criterion definition is
		// frozen once execution begins and cannot ride this PATCH implicitly.
		if bad := firstBadField(
			fieldCheck{"criterion_key (la definicion esta congelada al evaluar)", in.Key == ""},
			fieldCheck{"ordinal (congelado al evaluar)", in.Ordinal == 0},
			fieldCheck{"statement (congelado al evaluar)", in.Statement == ""},
			fieldCheck{"required (congelado al evaluar)", !in.Required},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
		if bad := firstBadField(
			fieldCheck{"state (bandera --state)", acceptanceStates[in.State]},
			fieldCheck{"evidence_ref (no vale con state=pending)", in.State != "pending" || in.EvidenceRef == ""},
			fieldCheck{"evidence_hash (no vale con state=pending)", in.State != "pending" || in.EvidenceHash == ""},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
		if (in.State == "passed" || in.State == "failed") && !boundedText(in.EvidenceRef, 1, 512) {
			return brokenField(422, "acceptance_incomplete", fldEvidenceRef)
		}
		if in.State == "passed" {
			if _, err := decodeHash(in.EvidenceHash, true); err != nil {
				return brokenField(422, "acceptance_incomplete", fldEvidenceHash)
			}
		}
		if in.State == "waived" && in.WaiverDecisionID.IsZero() {
			return brokenField(422, "acceptance_incomplete", fldWaiverDecision)
		}
		if in.State != "waived" && !in.WaiverDecisionID.IsZero() {
			return brokenField(400, "invalid_command", "waiver_decision_id (solo con state=waived)")
		}
	}
	return nil
}

// validWorkOwnerRef bounds an owner ref and, for a SESSION owner, pins its shape
// to a canonical sid at the door.
//
// The shape check is the cheap half of the fix for the defect that made
// owner_kind="session" unreachable: a bare uuid and a canonical sid are
// indistinguishable strings once the "osn_" prefix is gone, and treating one as
// the other is exactly what sent a canonical sid to a core model.Session lookup.
// Refusing an unprefixed owner_ref here means the ambiguity cannot be created in
// the first place, rather than being resolved differently by each reader.
func validWorkOwnerRef(kind, ref string) bool {
	if !boundedText(ref, 1, 512) {
		return false
	}
	if kind == "session" {
		return validCanonicalSID(ref)
	}
	return true
}

// ⛔ LOS NOMBRES DE CAMPO CON EXPLICACION VAN EN CONSTANTES, Y NO ES ESTILO.
//
// Escribi `criterion_key` con DOS redacciones distintas en dos sitios —«(o acceptance[0].key)» y
// «(bandera --criterion-key, o acceptance[].key)»— y el test lo cazo al primer intento. Un nombre
// que se teclea dos veces es un hecho en dos sitios, y un hecho en dos sitios DERIVA. Los cortos
// (`work_item_id`, `fence`) se dejan literales: no tienen nada que divergir.
const (
	fldCriterionKey = "criterion_key (bandera --criterion-key, o acceptance[].key)"
	fldStatement    = "statement (bandera --statement, o acceptance[].statement)"
	fldAcceptance1  = "acceptance (exactamente un criterio)"

	// Los seis de abajo salian MUDOS (`broken`, sin campo) hasta 2026-08-25: el camino de
	// aceptacion contestaba `acceptance_incomplete` para CINCO causas distintas, asi que
	// nombrar el codigo no decia nada. Vocabulario del LLAMANTE: la bandera del CLI
	// (cmd_work.go:100-127) mas el nombre JSON, que es lo unico que quien llama puede teclear.
	fldEvidenceRef     = "evidence_ref (bandera --evidence-ref; obligatorio con state=passed|failed)"
	fldEvidenceHash    = "evidence_hash (bandera --evidence-hash; obligatorio con state=passed)"
	fldWaiverDecision  = "waiver_decision_id (bandera --waiver-decision-id; obligatorio con state=waived)"
	fldAcceptanceRange = "acceptance (entre 1 y 64 criterios)"
	fldCriterionDup    = "criterion_key (bandera --criterion-key; duplicada en este mismo comando)"
	fldRequiredOne     = "required (bandera --required; al menos un criterio obligatorio)"
	fldOwnerRef        = "owner_ref (el id del participante, sin prefijo: `<uuid>`, no `user:<uuid>`)"
)

func validateCommandSyntax(cmd WorkCommand) error {
	cmd = normalizeWorkCommand(cmd)
	if cmd.Command == "" || len(cmd.Command) > 64 {
		return brokenField(400, "invalid_command", "command")
	}
	switch cmd.Command {
	case "item.create":
		if bad := firstBadField(
			fieldCheck{"workspace_id", !cmd.WorkspaceID.IsZero()},
			fieldCheck{"work_kind", boundedToken(cmd.WorkKind, 64)},
			fieldCheck{"title", boundedText(cmd.Title, 1, 256)},
			fieldCheck{"brief_md", boundedText(cmd.BriefMD, 1, 64*1024)},
			fieldCheck{"priority", workPriorities[cmd.Priority]},
			fieldCheck{"owner_kind", workOwnerKinds[cmd.OwnerKind]},
			fieldCheck{"owner_ref", validWorkOwnerRef(cmd.OwnerKind, cmd.OwnerRef)},
			fieldCheck{"provenance_kind", workProvenanceKinds[cmd.ProvenanceKind]},
			fieldCheck{"provenance_ref", boundedText(cmd.ProvenanceRef, 1, 512)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
		if err := validateRefs(cmd.ContextRefs); err != nil {
			return err
		}
		if _, err := decodeHash(cmd.ProvenanceHash, false); err != nil {
			return err
		}
		required := 0
		if len(cmd.Acceptance) == 0 || len(cmd.Acceptance) > 64 {
			return brokenField(422, "acceptance_incomplete", fldAcceptanceRange)
		}
		seen := map[string]bool{}
		for _, criterion := range cmd.Acceptance {
			if bad := firstBadField(
				// ⛔ EL NOMBRE DEL LLAMANTE, NO EL DE LA RODAJA ANIDADA. Un contraste refuto
				// «acceptance[].key»: esa grafia no existe para quien llama. El CLI ofrece
				// `--criterion-key` (cmd_work.go:100) y `normalizeWorkCommand` la vuelca en
				// `Acceptance[0]`, asi que quien omite la bandera y lee «acceptance[].key»
				// busca una bandera que NO EXISTE. Es el mismo defecto que este cambio
				// arregla para `blocked_code`, cometido una capa mas abajo.
				fieldCheck{fldCriterionKey, criterion.Key != ""},
				fieldCheck{fldStatement, criterion.Statement != ""},
			); bad != "" {
				return brokenField(400, "invalid_command", bad)
			}
			if err := validateAcceptanceInput(criterion, false); err != nil {
				return err
			}
			if seen[criterion.Key] {
				return brokenField(409, "acceptance_duplicate", fldCriterionDup)
			}
			seen[criterion.Key] = true
			if criterion.Required {
				required++
			}
		}
		if required == 0 {
			return brokenField(422, "acceptance_incomplete", fldRequiredOne)
		}
		if cmd.DueAt != "" {
			if _, err := model.ParseTimestamp(cmd.DueAt); err != nil {
				return brokenField(400, "invalid_command", "due_at")
			}
		}
	case "item.update":
		if cmd.WorkItemID.IsZero() {
			return brokenField(400, "invalid_command", "work_item_id")
		}
		if cmd.Title == "" && cmd.BriefMD == "" && cmd.Priority == "" && cmd.ContextRefs == nil && cmd.DueAt == "" {
			// Nada que actualizar: el campo que falta es CUALQUIERA de estos cinco, y decirlo
			// asi es mas util que elegir uno.
			return brokenField(400, "invalid_command", "title|brief_md|priority|context_refs|due_at (al menos uno)")
		}
		if cmd.Title != "" && !boundedText(cmd.Title, 1, 256) {
			return brokenField(400, "invalid_command", "title")
		}
		if cmd.BriefMD != "" && !boundedText(cmd.BriefMD, 1, 64*1024) {
			return brokenField(400, "invalid_command", "brief_md")
		}
		if cmd.Priority != "" && !workPriorities[cmd.Priority] {
			return brokenField(400, "invalid_command", "priority")
		}
		if cmd.ContextRefs != nil {
			if err := validateRefs(cmd.ContextRefs); err != nil {
				return err
			}
		}
		if cmd.DueAt != "" {
			if _, err := model.ParseTimestamp(cmd.DueAt); err != nil {
				return brokenField(400, "invalid_command", "due_at")
			}
		}
	case "item.ready", "item.unblock", "item.submit", "item.complete", "item.archive":
		if cmd.WorkItemID.IsZero() {
			return brokenField(400, "invalid_command", "work_item_id")
		}
	case "item.block", "item.fail", "item.cancel":
		// ⛔ EL NOMBRE DEPENDE DEL COMANDO, y por eso no vale una constante.
		// `normalizeWorkCommand` acepta `blocked_code` para `item.block` y `terminal_code`
		// para `item.fail`/`item.cancel`, y los vuelca los dos sobre `Code`. Decir «code» a
		// secas mandaria a la bandera equivocada a quien uso la que el CLI le ofrece.
		codeField, reasonField := "terminal_code (o code)", "terminal_reason (o reason)"
		if cmd.Command == "item.block" {
			codeField, reasonField = "blocked_code (o code)", "blocked_reason (o reason)"
		}
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{codeField, boundedToken(cmd.Code, 64)},
			fieldCheck{reasonField, boundedText(cmd.Reason, 1, 2*1024)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	case "item.assign":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"owner_kind", workOwnerKinds[cmd.OwnerKind]},
			fieldCheck{"owner_ref", validWorkOwnerRef(cmd.OwnerKind, cmd.OwnerRef)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	case "dependency.add":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"depends_on_id", !cmd.DependsOnID.IsZero()},
			fieldCheck{"depends_on_id (no puede ser el propio work_item_id)", cmd.WorkItemID != cmd.DependsOnID},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	case "dependency.remove":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"target_id (o dependency_id)", !cmd.TargetID.IsZero()},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	case "acceptance.add":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{fldAcceptance1, len(cmd.Acceptance) == 1},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
		// La guarda de arriba es la que hace seguro indexar aqui.
		if bad := firstBadField(
			fieldCheck{fldCriterionKey, cmd.Acceptance[0].Key != ""},
			fieldCheck{fldStatement, cmd.Acceptance[0].Statement != ""},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
		if err := validateAcceptanceInput(cmd.Acceptance[0], false); err != nil {
			return err
		}
	case "acceptance.evaluate":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"criterion_id", !cmd.CriterionID.IsZero()},
			fieldCheck{fldAcceptance1, len(cmd.Acceptance) == 1},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
		if err := validateAcceptanceInput(cmd.Acceptance[0], true); err != nil {
			return err
		}
	case "acceptance.update":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"criterion_id", !cmd.CriterionID.IsZero()},
			fieldCheck{fldAcceptance1, len(cmd.Acceptance) == 1},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
		input := cmd.Acceptance[0]
		// The criterion key is immutable. Definition PATCH replaces the three
		// editable fields as one unambiguous document while the item is draft.
		if bad := firstBadField(
			fieldCheck{"criterion_key (inmutable: no se envia en un update)", input.Key == ""},
			fieldCheck{"ordinal", input.Ordinal >= 0},
			fieldCheck{"statement", boundedText(input.Statement, 1, 4*1024)},
			fieldCheck{"state (no editable aqui)", input.State == ""},
			fieldCheck{"evidence_ref (no editable aqui)", input.EvidenceRef == ""},
			fieldCheck{"evidence_hash (no editable aqui)", input.EvidenceHash == ""},
			fieldCheck{"waiver_decision_id (no editable aqui)", input.WaiverDecisionID.IsZero()},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	case "decision.set", "decision.supersede", "decision.revoke":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"decision_key", boundedToken(cmd.DecisionKey, 128)},
			fieldCheck{"authority_ref", boundedText(cmd.AuthorityRef, 1, 512)},
			fieldCheck{"statement_md (o statement)", boundedText(cmd.StatementMD, 1, 16*1024)},
			fieldCheck{"rationale_md", boundedText(cmd.RationaleMD, 1, 16*1024)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
		if cmd.Command != "decision.revoke" {
			if bad := firstBadField(
				fieldCheck{"subject_kind", boundedText(cmd.SubjectKind, 1, 64)},
				fieldCheck{"subject_ref", boundedText(cmd.SubjectRef, 1, 512)},
			); bad != "" {
				return brokenField(400, "invalid_command", bad)
			}
		}
		if cmd.Command == "decision.revoke" && cmd.DecisionID.IsZero() {
			return brokenField(400, "invalid_command", "decision_id")
		}
	case "lease.acquire", "lease.takeover":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"holder_sid", validCanonicalSID(cmd.HolderSID)},
			fieldCheck{"ttl_seconds", validWorkLeaseTTL(cmd.TTLSeconds)},
			fieldCheck{"fence (obligatorio en lease.takeover)", cmd.Command != "lease.takeover" || cmd.Fence >= 1},
			fieldCheck{"holder_run_ref", cmd.HolderRunRef == "" || boundedText(cmd.HolderRunRef, 1, 512)},
			fieldCheck{"holder_agent_ref", cmd.HolderAgentRef == "" || boundedText(cmd.HolderAgentRef, 1, 512)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
		if cmd.Force {
			if bad := firstBadField(
				fieldCheck{"force (solo en lease.takeover)", cmd.Command == "lease.takeover"},
				fieldCheck{"reason (obligatorio con force)", boundedText(cmd.Reason, 1, 2*1024)},
				fieldCheck{"decision_id (obligatorio con force)", !cmd.DecisionID.IsZero()},
			); bad != "" {
				return brokenField(400, "invalid_command", bad)
			}
		}
	case "lease.renew":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"holder_sid", validCanonicalSID(cmd.HolderSID)},
			fieldCheck{"fence", cmd.Fence >= 1},
			fieldCheck{"ttl_seconds", validWorkLeaseTTL(cmd.TTLSeconds)},
			fieldCheck{"holder_run_ref", cmd.HolderRunRef == "" || boundedText(cmd.HolderRunRef, 1, 512)},
			fieldCheck{"holder_agent_ref", cmd.HolderAgentRef == "" || boundedText(cmd.HolderAgentRef, 1, 512)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	case "lease.release":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"holder_sid", validCanonicalSID(cmd.HolderSID)},
			fieldCheck{"fence", cmd.Fence >= 1},
			fieldCheck{"reason", cmd.Reason == "" || boundedText(cmd.Reason, 1, 2*1024)},
			fieldCheck{"holder_run_ref", cmd.HolderRunRef == "" || boundedText(cmd.HolderRunRef, 1, 512)},
			fieldCheck{"holder_agent_ref", cmd.HolderAgentRef == "" || boundedText(cmd.HolderAgentRef, 1, 512)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	case "lease.revoke":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"fence", cmd.Fence >= 1},
			fieldCheck{"reason", boundedText(cmd.Reason, 1, 2*1024)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	case "lease.clock_rebase":
		if bad := firstBadField(
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"decision_id", !cmd.DecisionID.IsZero()},
			fieldCheck{"evidence_ref", boundedText(cmd.EvidenceRef, 1, 512)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	case "lease.expire":
		if bad := firstBadField(
			fieldCheck{"command (lease.expire es interno: no se acepta del exterior)", cmd.internal},
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"fence", cmd.Fence >= 1},
			fieldCheck{"holder_sid", validCanonicalSID(cmd.HolderSID)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	case "lease.owner_died":
		if bad := firstBadField(
			fieldCheck{"command (lease.owner_died es interno: no se acepta del exterior)", cmd.internal},
			fieldCheck{"work_item_id", !cmd.WorkItemID.IsZero()},
			fieldCheck{"fence", cmd.Fence >= 1},
			fieldCheck{"holder_sid", validCanonicalSID(cmd.HolderSID)},
			fieldCheck{"holder_run_ref", cmd.HolderRunRef == "" || boundedText(cmd.HolderRunRef, 1, 512)},
			fieldCheck{"reason", boundedText(cmd.Reason, 1, 2*1024)},
		); bad != "" {
			return brokenField(400, "invalid_command", bad)
		}
	default:
		// No es un campo que falte: es que el comando no existe. Decirlo asi ahorra el
		// viaje que gasto un CLI de tercero para distinguir «desconocido» de «mal formado».
		return brokenField(400, "invalid_command", "command (comando no reconocido)")
	}
	return nil
}

func validWorkLeaseTTL(seconds int64) bool {
	if seconds == 0 {
		return true
	}
	return seconds >= int64(minWorkLeaseTTL/time.Second) &&
		seconds <= int64(maxWorkLeaseTTL/time.Second)
}

func normalizeWorkCommand(cmd WorkCommand) WorkCommand {
	if cmd.TargetID.IsZero() {
		cmd.TargetID = cmd.DependencyID
	}
	// Omitted and explicitly empty context references are the same semantic
	// command. Persist the canonical JSON array required by both engine guards,
	// never Go's nil-slice JSON null.
	if cmd.Command == "item.create" && cmd.ContextRefs == nil {
		cmd.ContextRefs = []ContextRef{}
	}
	if cmd.ExpectedPlanHash == "" {
		cmd.ExpectedPlanHash = cmd.PlanHash
	}
	switch cmd.Command {
	case "item.block":
		if cmd.Code == "" {
			cmd.Code = cmd.BlockedCode
		}
		if cmd.Reason == "" {
			cmd.Reason = cmd.BlockedReason
		}
	case "item.fail", "item.cancel":
		if cmd.Code == "" {
			cmd.Code = cmd.TerminalCode
		}
		if cmd.Reason == "" {
			cmd.Reason = cmd.TerminalReason
		}
	}
	if len(cmd.Acceptance) == 0 && (cmd.CriterionKey != "" || cmd.State != "" || cmd.Statement != "") {
		cmd.Acceptance = []AcceptanceInput{{
			Key: cmd.CriterionKey, Ordinal: cmd.Ordinal, Statement: cmd.Statement,
			Required: cmd.Required, State: cmd.State, EvidenceRef: cmd.EvidenceRef,
			EvidenceHash: cmd.EvidenceHash, WaiverDecisionID: cmd.WaiverDecisionID,
		}}
	}
	if cmd.StatementMD == "" {
		cmd.StatementMD = cmd.Statement
	}
	return cmd
}

func workCommandEvent(cmd string) string {
	switch cmd {
	case "item.create":
		return "work.item.created"
	case "item.assign":
		return "work.owner.changed"
	case "dependency.add", "dependency.remove":
		return "work.dependency.changed"
	case "acceptance.add", "acceptance.update", "acceptance.evaluate":
		return "work.acceptance.changed"
	case "decision.set", "decision.supersede", "decision.revoke":
		return "work.decision.recorded"
	case "lease.acquire", "lease.renew", "lease.takeover":
		return "work.lease.acquired"
	case "lease.release", "lease.revoke", "lease.expire", "lease.owner_died":
		return "work.lease.ended"
	default:
		return "work.item.transitioned"
	}
}

func aggregateChecks(checks []WorkCheck) (AssessmentVerdict, string) {
	verdict := VerdictClean
	code := "ok"
	for _, check := range checks {
		if check.Verdict == VerdictBroken {
			return VerdictBroken, check.Name
		}
		if check.Verdict == VerdictUnknown {
			verdict, code = VerdictUnknown, check.Name
		}
	}
	return verdict, code
}
