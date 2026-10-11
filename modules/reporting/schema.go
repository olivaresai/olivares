// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// schema.go registers the persistence entities: scheduled reports, their run
// history, per-tenant branding and operator-uploaded custom templates. The schema
// is registered UNCONDITIONALLY (community and enterprise builds alike) so the two
// editions carry byte-identical schema — an in-place community⇆enterprise swap on
// the same data dir never lands in a partial-upgrade state (the invariant the
// schema-parity gate enforces). The store-backed providers over these tables are
// Business and live outside this tree; the community build WIRES none (the
// reportScheduler/Branding/CustomTemplates edition ports are nil), so the tables
// stay empty there — the feature is gated by wiring, not by schema.

const (
	scheduleKind    model.Kind = "reporting.schedule"
	scheduleTable              = "reporting_schedule"
	scheduleRunKind model.Kind = "reporting.schedule_run"
	scheduleRunTbl             = "reporting_schedule_run" // 22 chars (< 40 cap)
	brandingKind    model.Kind = "reporting.branding"
	brandingTable              = "reporting_branding"
	templateKind    model.Kind = "reporting.template"
	templateTable              = "reporting_template"
)

// reporting.schedule columns.
const (
	colSchedReportType = "report_type"
	colSchedFormat     = "format"
	colSchedCron       = "cron"
	colSchedFramework  = "framework"
	colSchedTeam       = "team"
	colSchedLocale     = "locale"
	colSchedEnabled    = "enabled"
)

// reporting.schedule_run columns.
const (
	colRunScheduleID = "schedule_id"
	colRunReportType = "report_type"
	colRunFormat     = "format"
	colRunRanAt      = "ran_at"
	colRunStatus     = "status"
	colRunError      = "error"
	colRunOutput     = "output"
)

// reporting.branding columns (one row per tenant).
const (
	colBrandLogo      = "logo_path"
	colBrandPrimary   = "primary_color"
	colBrandSecondary = "secondary_color"
	colBrandFooter    = "footer_text"
	colBrandCompany   = "company_name"
)

// reporting.template columns (one row per (tenant, report_type)).
const (
	colTmplReportType = "report_type"
	colTmplHTML       = "html"
)

// Principal declarations shared by more than one column below.
var (
	// pdeclNoneReportType is a built-in report type from a closed set.
	pdeclNoneReportType = model.None("a closed report-type set: api.go:29-34, enterprise.go:180, enterprise.go:322")
	// pdeclNoneBranding is a branding display value, only placed into rendered reports or returned by the branding read.
	pdeclNoneBranding = model.None("a stored branding display value returned by the branding read, never resolved to a principal: enterprise.go:262, enterprise.go:267")
)

var _ interface {
	RegisterSchema(store.ExtensionRegistry) error
} = (*Module)(nil)

// RegisterSchema declares the persistence entities. Additive (new tables
// only); it never alters an existing entity.
func (m *Module) RegisterSchema(reg store.ExtensionRegistry) error {
	if err := reg.Register(model.EntityDescriptor{
		Kind:  scheduleKind,
		Table: scheduleTable,
		Fields: []model.FieldSpec{
			{Name: colSchedReportType, Kind: model.KindText, Indexed: true, Principal: pdeclNoneReportType},
			{Name: colSchedFormat, Kind: model.KindText, Principal: model.None("a closed format set html|pdf: enterprise.go:184-186, types.go:29-30")},
			{Name: colSchedCron, Kind: model.KindText, Principal: model.None("a cron spec, validated before storage: enterprise.go:187, cron.go:24")},
			{Name: colSchedFramework, Kind: model.KindText, Nullable: true, Principal: model.None("a stored framework filter returned in schedule metadata, never resolved to a principal: types.go:228, enterprise.go:167")},
			{Name: colSchedTeam, Kind: model.KindText, Nullable: true, Principal: model.None("a stored team filter returned in schedule metadata, never resolved to a principal: types.go:229, enterprise.go:167")},
			{Name: colSchedLocale, Kind: model.KindText, Nullable: true, Principal: model.None("a stored locale key returned in schedule metadata, never resolved to a principal: types.go:230, enterprise.go:167")},
			{Name: colSchedEnabled, Kind: model.KindBool},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  scheduleRunKind,
		Table: scheduleRunTbl,
		Fields: []model.FieldSpec{
			{Name: colRunScheduleID, Kind: model.KindText, Indexed: true, Principal: model.None("a schedule row id or a reserved digest-cadence id: enterprise.go:461, enterprise.go:435")},
			{Name: colRunReportType, Kind: model.KindText, Principal: model.None("a report type rendered in run metadata, never resolved to a principal: enterprise.go:462, enterprise.go:436, enterprise.go:227")},
			{Name: colRunFormat, Kind: model.KindText, Principal: model.None("a closed format set html|pdf|json: enterprise.go:463, enterprise.go:184-186, enterprise.go:437")},
			{Name: colRunRanAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colRunStatus, Kind: model.KindText, Principal: model.None("a closed status set ok|failed: enterprise.go:440-447, enterprise.go:468-472")},
			{Name: colRunError, Kind: model.KindText, Nullable: true, Principal: model.None("a render or gather error message, listed only: enterprise.go:440-445, enterprise.go:468-470, enterprise.go:225")},
			{Name: colRunOutput, Kind: model.KindBytes, Nullable: true, Principal: model.Scan(model.ClassEvidence)},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  brandingKind,
		Table: brandingTable,
		Fields: []model.FieldSpec{
			{Name: colBrandLogo, Kind: model.KindText, Nullable: true, Principal: pdeclNoneBranding},
			{Name: colBrandPrimary, Kind: model.KindText, Nullable: true, Principal: pdeclNoneBranding},
			{Name: colBrandSecondary, Kind: model.KindText, Nullable: true, Principal: pdeclNoneBranding},
			{Name: colBrandFooter, Kind: model.KindText, Nullable: true, Principal: pdeclNoneBranding},
			{Name: colBrandCompany, Kind: model.KindText, Nullable: true, Principal: pdeclNoneBranding},
		},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  templateKind,
		Table: templateTable,
		Fields: []model.FieldSpec{
			{Name: colTmplReportType, Kind: model.KindText, Indexed: true, Principal: pdeclNoneReportType},
			{Name: colTmplHTML, Kind: model.KindText, Principal: model.None("stored operator-authored report markup returned as bytes, never resolved to a principal: enterprise.go:302, enterprise.go:311-313")},
		},
	})
}
