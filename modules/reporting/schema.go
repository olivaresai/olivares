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
// schema-parity gate enforces). The community build simply never WIRES a provider
// over these tables (newReportScheduler/Branding/CustomTemplates return nil), so
// the tables stay empty there — the feature is gated by wiring, not by schema.

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
			{Name: colSchedReportType, Kind: model.KindText, Indexed: true},
			{Name: colSchedFormat, Kind: model.KindText},
			{Name: colSchedCron, Kind: model.KindText},
			{Name: colSchedFramework, Kind: model.KindText, Nullable: true},
			{Name: colSchedTeam, Kind: model.KindText, Nullable: true},
			{Name: colSchedLocale, Kind: model.KindText, Nullable: true},
			{Name: colSchedEnabled, Kind: model.KindBool},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  scheduleRunKind,
		Table: scheduleRunTbl,
		Fields: []model.FieldSpec{
			{Name: colRunScheduleID, Kind: model.KindText, Indexed: true},
			{Name: colRunReportType, Kind: model.KindText},
			{Name: colRunFormat, Kind: model.KindText},
			{Name: colRunRanAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colRunStatus, Kind: model.KindText},
			{Name: colRunError, Kind: model.KindText, Nullable: true},
			{Name: colRunOutput, Kind: model.KindBytes, Nullable: true},
		},
	}); err != nil {
		return err
	}

	if err := reg.Register(model.EntityDescriptor{
		Kind:  brandingKind,
		Table: brandingTable,
		Fields: []model.FieldSpec{
			{Name: colBrandLogo, Kind: model.KindText, Nullable: true},
			{Name: colBrandPrimary, Kind: model.KindText, Nullable: true},
			{Name: colBrandSecondary, Kind: model.KindText, Nullable: true},
			{Name: colBrandFooter, Kind: model.KindText, Nullable: true},
			{Name: colBrandCompany, Kind: model.KindText, Nullable: true},
		},
	}); err != nil {
		return err
	}

	return reg.Register(model.EntityDescriptor{
		Kind:  templateKind,
		Table: templateTable,
		Fields: []model.FieldSpec{
			{Name: colTmplReportType, Kind: model.KindText, Indexed: true},
			{Name: colTmplHTML, Kind: model.KindText},
		},
	})
}
