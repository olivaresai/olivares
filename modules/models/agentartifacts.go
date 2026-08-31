// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// AGENT-ARTIFACT supply chain in the AIBOM (CUR-7). The four artifact
// classes agents execute without governance — Agent Skills (SKILL.md,
// agentskills.io), `.mcpb` desktop extensions, MCP App ui:// templates
// (SEP-1865) and AGENTS.md instruction files — become first-class, governable
// registry entries here, and a dedicated CycloneDX 1.6 AGENT-SUPPLY-CHAIN BOM
// inventories them with provenance + posture verdict as HONEST properties
// (claim-vs-verified, the dataset/admission idiom).
//
// This is deliberately a SEPARATE BOM from the per-owned-model AIBOM
// (aibom.go): Skills/.mcpb/templates/AGENTS.md are tenant-estate artifacts, not
// the lineage of one model — gluing them into a model's AIBOM would fabricate
// provenance (aibom.go's own rule: fields we do not have are OMITTED, never
// fabricated). The seal reuses the machinery (canonical content hash +
// ledger anchor + append-only models.aibom record under the "agent-artifacts"
// subject), so there is exactly one tamper-evidence pattern in the module.
//
// MINIMAL-DATA (docs/SECURITY-HARDENING.md): an artifact record carries a NAME + class +
// provenance label + content hash + posture verdict — never skill bodies,
// manifests or instruction text. The posture verdict mirrors what the
// connector scanners graded (skill_posture / mcpb_posture / mcp_app /
// instructions_posture findings); `posture_scanned=false` is an honest "not
// scanned", never a fake grade.

import (
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const agentArtifactKind model.Kind = "models.agent_artifact"
const agentArtifactTable = "models_agent_artifact"

const (
	colAAClass       = "artifact_class"
	colAAName        = "name"
	colAAVersion     = "artifact_version"
	colAAProvenance  = "provenance"
	colAASource      = "source_ref"
	colAAContentHash = "content_hash"
	colAAContentAlg  = "content_alg"
	colAAGrade       = "posture_grade"
	colAAIssues      = "posture_issues"
	colAAScanned     = "posture_scanned"
	colAAVerified    = "verified"
	colAAAttestedBy  = "attested_by"
	colAAAttestedAt  = "attested_at"
	colAANote        = "note"
)

// The four artifact classes of CUR-7 (closed set).
const (
	artifactClassSkill    = "skill"
	artifactClassMCPB     = "mcpb_extension"
	artifactClassTemplate = "mcp_app_template"
	artifactClassAgentsMD = "agents_md"
)

var artifactClasses = set(artifactClassSkill, artifactClassMCPB, artifactClassTemplate, artifactClassAgentsMD)

// artifactCDXType maps each artifact class to its CycloneDX 1.6 component type
// (all values are bom-1.6.schema.json enum members): a skill is a reusable
// instruction LIBRARY, a desktop extension is an installable APPLICATION, a
// ui:// template and an AGENTS.md are governed FILEs.
var artifactCDXType = map[string]string{
	artifactClassSkill:    "library",
	artifactClassMCPB:     "application",
	artifactClassTemplate: "file",
	artifactClassAgentsMD: "file",
}

// postureGrades is the closed grade vocabulary (the A–F scale the
// connector scanners emit).
var postureGrades = set("A", "B", "C", "D", "F")

// agentArtifactBOMRef is the seal-subject ref for the agent-supply-chain BOM.
const agentArtifactBOMRef = "agent-artifacts"

// agentAIBOMKind is the append-only SEAL record for the agent-supply-chain BOM.
// It is a SEPARATE kind from models.aibom on purpose (same column shape, own
// table): modules/compliance counts models.aibom rows BY KIND as "sealed model
// AIBOM" evidence (capabilities.go aibomExtKind) — folding agent-artifact seals
// into that kind would silently inflate model-lineage evidence with non-model
// seals. Two BOM scopes, two seal kinds, one machinery.
const agentAIBOMKind model.Kind = "models.agent_aibom"
const agentAIBOMTable = "models_agent_aibom"

func registerAgentArtifactSchema(reg store.ExtensionRegistry) error {
	// The agent-supply-chain seal mirrors the models.aibom descriptor exactly
	// (the colAI* columns are shared package constants — aibom.go).
	if err := reg.Register(model.EntityDescriptor{
		Kind: agentAIBOMKind, Table: agentAIBOMTable, AppendOnly: true,
		Fields: []model.FieldSpec{
			{Name: colAIOwned, Kind: model.KindText, Indexed: true},
			{Name: colAISerial, Kind: model.KindText},
			{Name: colAIContentHsh, Kind: model.KindText, Indexed: true},
			{Name: colAISpecVer, Kind: model.KindText},
			{Name: colAICompCount, Kind: model.KindInt},
			{Name: colAILedgerSeq, Kind: model.KindInt},
			{Name: colAILedgerHash, Kind: model.KindText, Nullable: true},
			{Name: colAIScopeNote, Kind: model.KindText, Nullable: true},
			{Name: colAIGenBy, Kind: model.KindText, Nullable: true},
			{Name: colAIGenAt, Kind: model.KindText, Nullable: true},
		},
	}); err != nil {
		return err
	}
	return reg.Register(model.EntityDescriptor{
		Kind: agentArtifactKind, Table: agentArtifactTable,
		Fields: []model.FieldSpec{
			{Name: colAAClass, Kind: model.KindText, Indexed: true},
			{Name: colAAName, Kind: model.KindText, Indexed: true},
			{Name: colAAVersion, Kind: model.KindText, Nullable: true},
			{Name: colAAProvenance, Kind: model.KindText, Nullable: true},
			{Name: colAASource, Kind: model.KindText, Nullable: true},
			{Name: colAAContentHash, Kind: model.KindText, Nullable: true},
			{Name: colAAContentAlg, Kind: model.KindText, Nullable: true},
			{Name: colAAGrade, Kind: model.KindText, Nullable: true},
			{Name: colAAIssues, Kind: model.KindInt},
			{Name: colAAScanned, Kind: model.KindBool},
			{Name: colAAVerified, Kind: model.KindBool, Indexed: true},
			{Name: colAAAttestedBy, Kind: model.KindText, Nullable: true},
			{Name: colAAAttestedAt, Kind: model.KindText, Nullable: true},
			{Name: colAANote, Kind: model.KindText, Nullable: true},
		},
		Indexes: []model.IndexSpec{{
			// One registry entry per (class, name) per tenant — tenant-led.
			Name:    "models_agent_artifact_uniq",
			Columns: []string{model.ColTenantID, colAAClass, colAAName},
			Unique:  true,
		}},
	})
}

type agentArtifactDTO struct {
	ID             string `json:"id,omitempty"`
	ArtifactClass  string `json:"artifact_class"`
	Name           string `json:"name"`
	Version        string `json:"version,omitempty"`
	Provenance     string `json:"provenance,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
	ContentHash    string `json:"content_hash,omitempty"`
	ContentAlg     string `json:"content_alg,omitempty"`
	PostureGrade   string `json:"posture_grade,omitempty"`
	PostureIssues  int64  `json:"posture_issues"`
	PostureScanned bool   `json:"posture_scanned"`
	Verified       bool   `json:"verified"`
	AttestedBy     string `json:"attested_by,omitempty"`
	AttestedAt     string `json:"attested_at,omitempty"`
	Note           string `json:"note,omitempty"`
}

func (a *agentArtifactDTO) validate() string {
	a.ArtifactClass = strings.TrimSpace(strings.ToLower(a.ArtifactClass))
	if !artifactClasses[a.ArtifactClass] {
		return "artifact_class must be skill, mcpb_extension, mcp_app_template or agents_md"
	}
	a.Name = trimClamp(a.Name)
	if a.Name == "" {
		return "name is required"
	}
	a.PostureGrade = strings.TrimSpace(strings.ToUpper(a.PostureGrade))
	switch {
	case a.PostureGrade == "":
		// An ungraded artifact must not carry scan claims (honest: a grade
		// without a scan — or a scan count without a grade — is a fabrication).
		if a.PostureScanned {
			return "posture_scanned=true requires a posture_grade (an unscanned artifact carries no verdict)"
		}
		if a.PostureIssues != 0 {
			return "posture_issues requires a posture_grade"
		}
	case !postureGrades[a.PostureGrade]:
		return "posture_grade must be A, B, C, D or F (the scale)"
	default:
		a.PostureScanned = true // a grade IS a scan verdict
	}
	if a.PostureIssues < 0 {
		return "posture_issues must be >= 0"
	}
	if a.ContentAlg == "" && a.ContentHash != "" {
		a.ContentAlg = "sha256"
	}
	return ""
}

func (a agentArtifactDTO) toRecord(actor, at string) model.Record {
	return model.Record{
		colAAClass: a.ArtifactClass, colAAName: a.Name, colAAVersion: trimClamp(a.Version),
		colAAProvenance: trimClamp(a.Provenance), colAASource: trimClamp(a.SourceRef),
		colAAContentHash: trimClamp(a.ContentHash), colAAContentAlg: trimClamp(a.ContentAlg),
		colAAGrade: a.PostureGrade, colAAIssues: a.PostureIssues, colAAScanned: a.PostureScanned,
		colAAVerified: a.Verified, colAAAttestedBy: actor, colAAAttestedAt: at, colAANote: trimClamp(a.Note),
	}
}

func toAgentArtifactDTO(rec model.Record) agentArtifactDTO {
	return agentArtifactDTO{
		ID: rec.String(model.ColID), ArtifactClass: rec.String(colAAClass), Name: rec.String(colAAName),
		Version: rec.String(colAAVersion), Provenance: rec.String(colAAProvenance), SourceRef: rec.String(colAASource),
		ContentHash: rec.String(colAAContentHash), ContentAlg: rec.String(colAAContentAlg),
		PostureGrade: rec.String(colAAGrade), PostureIssues: rec.Int(colAAIssues), PostureScanned: rec.Bool(colAAScanned),
		Verified: rec.Bool(colAAVerified), AttestedBy: rec.String(colAAAttestedBy), AttestedAt: rec.String(colAAAttestedAt),
		Note: rec.String(colAANote),
	}
}

// --- CRUD handlers -------------------------------------------------------------

func (m *Module) handleListAgentArtifacts(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("artifact_class"); v != "" {
		q.Filters = append(q.Filters, eq(colAAClass, strings.ToLower(v)))
	}
	out := listResponse[agentArtifactDTO]{Items: []agentArtifactDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(agentArtifactKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toAgentArtifactDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (m *Module) handleCreateAgentArtifact(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in agentArtifactDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out agentArtifactDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(agentArtifactKind)
		if err != nil {
			return err
		}
		actor := mc.Principal.Actor()
		at := model.NewTimestamp(time.Now()).String()
		rec, err := repo.Create(r.Context(), in.toRecord(actor, at))
		if err != nil {
			return err
		}
		out = toAgentArtifactDTO(rec)
		return auditOwned(r.Context(), sc, mc, agentArtifactKind, "create", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (m *Module) handleDeleteAgentArtifact(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.deleteExt(w, r, mc, agentArtifactKind)
}

// --- the agent-supply-chain BOM --------------------------------------------------

const agentBOMDisclaimer = "Agent-supply-chain AIBOM: governed inventory of the agent artifacts (Skills, .mcpb extensions, MCP App ui:// templates, AGENTS.md instruction files) registered with this control plane — identity, provenance and posture verdict; NOT the artifacts' content nor a certification (docs/08 §9). Coverage reflects what was REGISTERED; an artifact never registered is not represented."

// buildAgentArtifactBOM assembles the CycloneDX 1.6 BOM of every registered
// agent artifact in the tenant scope. Provenance and the connector-scanner
// posture verdict ride as honest `olivares:artifact:*` properties; an absent
// verdict yields posture_scanned=false, never a fabricated grade.
func buildAgentArtifactBOM(r *http.Request, sc store.Scope) (cdxBOM, error) {
	repo, err := sc.Ext(agentArtifactKind)
	if err != nil {
		return cdxBOM{}, err
	}
	recs, err := listAllExt(r.Context(), repo)
	if err != nil {
		return cdxBOM{}, err
	}
	sortAgentArtifactRecords(recs)

	bom := cdxBOM{
		BOMFormat: "CycloneDX", SpecVersion: "1.6", Version: 1,
		SerialNumber: "urn:uuid:" + model.NewID().String(),
		Metadata: &cdxMetadata{
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Component: &cdxComponent{Type: "application", BOMRef: agentArtifactBOMRef, Name: agentArtifactBOMRef},
			Properties: []cdxProperty{
				{Name: "olivares:aibom:generator", Value: "olivares.models"},
				{Name: "olivares:aibom:disclaimer", Value: agentBOMDisclaimer},
				{Name: "olivares:aibom:scope", Value: "agent-artifact-supply-chain"},
			},
		},
	}
	for _, rec := range recs {
		a := toAgentArtifactDTO(rec)
		comp := cdxComponent{
			Type:   artifactCDXType[a.ArtifactClass],
			BOMRef: "agent-artifact:" + a.ID,
			Name:   a.Name,
		}
		if a.Version != "" {
			comp.Version = a.Version
		}
		if h, ok := cdxHashEntry(a.ContentAlg, a.ContentHash); ok {
			comp.Hashes = []cdxHash{h}
		}
		comp.Properties = []cdxProperty{
			{Name: "olivares:artifact:class", Value: a.ArtifactClass},
			{Name: "olivares:artifact:posture_scanned", Value: boolString(a.PostureScanned)},
			{Name: "olivares:artifact:provenance_verified", Value: boolString(a.Verified)},
		}
		if a.Provenance != "" {
			comp.Properties = append(comp.Properties, cdxProperty{Name: "olivares:artifact:provenance", Value: a.Provenance})
		}
		if a.SourceRef != "" {
			comp.Properties = append(comp.Properties, cdxProperty{Name: "olivares:artifact:source_ref", Value: a.SourceRef})
		}
		if a.PostureScanned {
			comp.Properties = append(comp.Properties,
				cdxProperty{Name: "olivares:artifact:posture_grade", Value: a.PostureGrade},
				cdxProperty{Name: "olivares:artifact:posture_issues", Value: strconv.FormatInt(a.PostureIssues, 10)},
			)
		}
		bom.Components = append(bom.Components, comp)
	}
	return bom, nil
}

// sortAgentArtifactRecords restores the artifact_class/name ordering used by the
// former single-page SQL query after the keyset walk has gathered every id-ordered
// page. Stable sorting preserves id order for duplicate class/name pairs.
func sortAgentArtifactRecords(recs []model.Record) {
	sort.SliceStable(recs, func(i, j int) bool {
		leftClass, rightClass := recs[i].String(colAAClass), recs[j].String(colAAClass)
		if leftClass != rightClass {
			return leftClass < rightClass
		}
		return recs[i].String(colAAName) < recs[j].String(colAAName)
	})
}

// handleGenerateAgentArtifactBOM returns the live agent-supply-chain BOM
// (read-only export, not sealed; read-tier, not audited — observer effect).
func (m *Module) handleGenerateAgentArtifactBOM(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var bom cdxBOM
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		b, err := buildAgentArtifactBOM(r, sc)
		if err != nil {
			return err
		}
		bom = b
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bom)
}

// handleSealAgentArtifactBOM generates the agent-supply-chain BOM, anchors the
// audit-chain head, persists an append-only models.agent_aibom seal and
// self-audits it — the same pattern as handleSealAIBOM on the artifact
// inventory, so it is tamper-evident evidence (and a SEPARATE evidence class
// from the model-lineage seals compliance counts).
func (m *Module) handleSealAgentArtifactBOM(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var (
		seal aibomSealDTO
		bom  cdxBOM
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		b, err := buildAgentArtifactBOM(r, sc)
		if err != nil {
			return err
		}
		hash, err := canonicalAIBOMHash(b)
		if err != nil {
			return err
		}
		head, headOK, err := sc.Audit().Head(r.Context())
		if err != nil {
			return err
		}
		ledgerSeq := int64(0)
		ledgerHash := ""
		if headOK {
			ledgerSeq = head.Seq
			ledgerHash = hex.EncodeToString(head.Hash)
		}
		repo, err := sc.Ext(agentAIBOMKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), model.Record{
			colAIOwned: agentArtifactBOMRef, colAISerial: b.SerialNumber, colAIContentHsh: hash,
			colAISpecVer: b.SpecVersion, colAICompCount: int64(len(b.Components)),
			colAILedgerSeq: ledgerSeq, colAILedgerHash: ledgerHash, colAIScopeNote: agentBOMDisclaimer,
			colAIGenBy: mc.Principal.Actor(), colAIGenAt: time.Now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			return err
		}
		seal, bom = toAIBOMSealDTO(rec), b
		// Self-audit the seal as the NEXT chain event after the head it anchors.
		return auditOwned(r.Context(), sc, mc, agentAIBOMKind, "seal", model.ID(rec.String(model.ColID)))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"seal": seal, "aibom": bom})
}

// handleListAgentArtifactBOMs lists the agent-supply-chain BOM seals.
func (m *Module) handleListAgentArtifactBOMs(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	out := listResponse[aibomSealDTO]{Items: []aibomSealDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(agentAIBOMKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toAIBOMSealDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// boolString renders an honest boolean property value (CycloneDX properties
// are strings).
func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
