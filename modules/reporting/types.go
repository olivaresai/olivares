// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// ReportType identifies a built-in report.
type ReportType string

const (
	ReportComplianceEvidence ReportType = "compliance-evidence"
	ReportAuditSummary       ReportType = "audit-summary"
	ReportFinOps             ReportType = "finops-report"
	ReportAccessReview       ReportType = "access-review"
	ReportExecutiveSummary   ReportType = "executive-summary"
)

// Format is the output format of a generated report.
type Format string

const (
	FormatHTML Format = "html"
	FormatPDF  Format = "pdf"
)

// ReportMeta describes a report type for the listing endpoint.
type ReportMeta struct {
	Type        ReportType `json:"type"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Formats     []Format   `json:"formats"`
}

// ReportParams are the parameters for a report generation request.
type ReportParams struct {
	Type      ReportType
	Format    Format
	From      time.Time
	To        time.Time
	Framework string // compliance-evidence: filter by framework
	Team      string // finops-report: filter by team
	Locale    string // i18n locale (default: en)
}

// ---------- Data source interfaces ----------
// These are wired via Options by the composition root. Each source exposes the
// minimum data the report templates need. The composition root creates adapters
// that bridge these to the real modules (compliance, finops, audit, auth).

// ComplianceData is the data for a compliance-evidence report.
type ComplianceData struct {
	Frameworks []FrameworkReport
	Generated  time.Time
	Disclaimer string
}

// FrameworkReport is one framework's assessment for the report.
type FrameworkReport struct {
	ID        string
	Name      string
	Version   string
	Authority string
	Summary   AssessmentSummary
	Controls  []ControlReport
}

// AssessmentSummary tallies control statuses.
type AssessmentSummary struct {
	Total     int
	Satisfied int
	ByDesign  int
	Partial   int
	Gap       int
	Unmapped  int
}

// ControlReport is one control's assessment for the report.
type ControlReport struct {
	ID          string
	Title       string
	Requirement string
	Status      string // satisfied, by_design, partial, gap, unmapped
	Note        string
}

// ComplianceSource provides compliance data for reports.
type ComplianceSource interface {
	GatherComplianceData(ctx context.Context, tenant model.TenantID, framework string) (ComplianceData, error)
}

// AuditData is the data for an audit-summary report.
type AuditData struct {
	From             time.Time
	To               time.Time
	TotalEvents      int64
	EventsByAction   []ActionCount
	CheckpointOK     bool
	CheckpointCount  int
	FirstBadSeq      int64
	CheckpointReason string
	LedgerHead       int64
}

// ActionCount is a count of audit events by action.
type ActionCount struct {
	Action string
	Count  int64
}

// AuditSource provides audit data for reports. The module gathers audit data
// directly from mc.Data (the request-scoped store) so no composition-root
// adapter is needed.

// FinOpsData is the data for a finops report.
type FinOpsData struct {
	From             time.Time
	To               time.Time
	TotalMicroUSD    int64
	InputTokens      int64
	OutputTokens     int64
	Samples          int
	ByModel          []SpendBucket
	ByProvider       []SpendBucket
	ByTeam           []SpendBucket
	Budgets          []BudgetStatus
	ForecastMicroUSD int64
}

// SpendBucket is a spend breakdown entry.
type SpendBucket struct {
	Key          string
	CostMicroUSD int64
	InputTokens  int64
	OutputTokens int64
	Samples      int
}

// BudgetStatus is a budget's current status.
type BudgetStatus struct {
	ID            string
	Name          string
	Dimension     string
	Key           string
	Period        string
	LimitMicroUSD int64
	SpendMicroUSD int64
	ConsumedPct   int
	Over          bool
}

// FinOpsSource: the module gathers finops data directly from mc.Data.

// AccessData is the data for an access-review report.
type AccessData struct {
	Generated time.Time
	Users     []UserAccess
}

// UserAccess is one user's access information.
type UserAccess struct {
	UserID      string
	DisplayName string
	Email       string
	Roles       []string
	Permissions []string
	LastAccess  time.Time
	Inactive    bool
	Superadmin  bool
}

// AccessSource: the module gathers access data directly from mc.Data.

// ExecutiveData is the data for an executive-summary report.
type ExecutiveData struct {
	Generated         time.Time
	From              time.Time
	To                time.Time
	ComplianceSummary []FrameworkBrief
	TotalSpendUSD     float64
	SpendTrendPct     float64
	ActiveAgents      int64
	ActiveSessions    int64
	ActiveUsers       int64
	FindingsOpen      int64
	FindingsCritical  int64
	AuditIntegrityOK  bool
}

// FrameworkBrief is a one-line compliance summary per framework.
type FrameworkBrief struct {
	Name         string
	SatisfiedPct int
	GapCount     int
}

// ExecutiveSource: the module gathers executive data using mc.Data + compliance.

// ---------- Enterprise add-on interfaces ----------
// These are nil in the community build and wired under -tags enterprise.

// ReportScheduler manages periodic scheduled report generation, including the
// run history the schedule pump records. Implementations persist per
// tenant, validate cron specs at ScheduleReport, cap stored run artifacts and
// prune old runs.
type ReportScheduler interface {
	ScheduleReport(ctx context.Context, tenant model.TenantID, cfg ScheduleConfig) error
	ListSchedules(ctx context.Context, tenant model.TenantID) ([]ScheduleConfig, error)
	DeleteSchedule(ctx context.Context, tenant model.TenantID, id string) error
	// RecordRun persists one schedule execution outcome (ok or failed).
	RecordRun(ctx context.Context, tenant model.TenantID, run ScheduleRun) error
	// ListRuns returns the recorded runs for a schedule id, newest last.
	ListRuns(ctx context.Context, tenant model.TenantID, scheduleID string) ([]ScheduleRun, error)
}

// ScheduleConfig defines a scheduled report.
type ScheduleConfig struct {
	ID         string     `json:"id"`
	ReportType ReportType `json:"report_type"`
	Format     Format     `json:"format"`
	Cron       string     `json:"cron"`
	Framework  string     `json:"framework,omitempty"`
	Team       string     `json:"team,omitempty"`
	Locale     string     `json:"locale,omitempty"`
	Enabled    bool       `json:"enabled"`
}

// BrandingConfig defines custom branding for reports.
type BrandingConfig struct {
	LogoPath       string `json:"logo_path,omitempty"`
	PrimaryColor   string `json:"primary_color,omitempty"`
	SecondaryColor string `json:"secondary_color,omitempty"`
	FooterText     string `json:"footer_text,omitempty"`
	CompanyName    string `json:"company_name,omitempty"`
}

// BrandingProvider retrieves tenant-specific branding.
type BrandingProvider interface {
	GetBranding(ctx context.Context, tenant model.TenantID) (BrandingConfig, error)
	SetBranding(ctx context.Context, tenant model.TenantID, cfg BrandingConfig) error
}

// CustomTemplateProvider manages operator-uploaded report templates.
type CustomTemplateProvider interface {
	GetTemplate(ctx context.Context, tenant model.TenantID, reportType ReportType) (string, bool, error)
	SetTemplate(ctx context.Context, tenant model.TenantID, reportType ReportType, html string) error
	DeleteTemplate(ctx context.Context, tenant model.TenantID, reportType ReportType) error
}
