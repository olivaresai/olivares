// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// store_providers.go implements the provider interfaces (ReportScheduler,
// BrandingProvider, CustomTemplateProvider) over the module's own ext schema
// (schema.go). The types are UNEXPORTED with exported constructors; the community
// wire leaves the seams nil (feature gated by wiring), the enterprise wire calls
// the constructors. Each provider is bound to the module's late-bound data handle
// through bindData, which Module.UseData propagates.

// dataBinder is the internal seam Module.UseData uses to hand a wired provider the
// module's data accessor once it exists (the providers are constructed before the
// store).
type dataBinder interface {
	bindData(api.ModuleData)
}

// maxRunsPerSchedule bounds the retained run history per schedule (FIFO prune).
const maxRunsPerSchedule = 50

// ---- scheduler -------------------------------------------------------------------

type storeScheduler struct{ data api.ModuleData }

// NewStoreScheduler returns a store-backed ReportScheduler (wired under the
// enterprise build; the community build leaves the seam nil).
func NewStoreScheduler() ReportScheduler { return &storeScheduler{} }

func (s *storeScheduler) bindData(d api.ModuleData) { s.data = d }

func (s *storeScheduler) ScheduleReport(ctx context.Context, tenant model.TenantID, cfg ScheduleConfig) error {
	if s.data == nil {
		return errors.New("reporting: scheduler has no data handle")
	}
	if _, err := ParseCronSpec(cfg.Cron); err != nil {
		return fmt.Errorf("reporting: invalid cron: %w", err)
	}
	if cfg.Format != FormatPDF {
		cfg.Format = FormatHTML
	}
	return s.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colSchedReportType: string(cfg.ReportType),
			colSchedFormat:     string(cfg.Format),
			colSchedCron:       cfg.Cron,
			colSchedFramework:  nullable(cfg.Framework),
			colSchedTeam:       nullable(cfg.Team),
			colSchedLocale:     nullable(cfg.Locale),
			colSchedEnabled:    cfg.Enabled,
		}
		if cfg.ID != "" {
			if existing, err := repo.Get(ctx, model.ID(cfg.ID)); err == nil {
				for k, v := range rec {
					existing[k] = v
				}
				_, uerr := repo.Update(ctx, existing)
				return uerr
			} else if !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		_, err = repo.Create(ctx, rec)
		return err
	})
}

func (s *storeScheduler) ListSchedules(ctx context.Context, tenant model.TenantID) ([]ScheduleConfig, error) {
	if s.data == nil {
		return nil, errors.New("reporting: scheduler has no data handle")
	}
	var out []ScheduleConfig
	err := s.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 1000})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out = append(out, ScheduleConfig{
				ID:         rec.String(model.ColID),
				ReportType: ReportType(rec.String(colSchedReportType)),
				Format:     Format(rec.String(colSchedFormat)),
				Cron:       rec.String(colSchedCron),
				Framework:  rec.String(colSchedFramework),
				Team:       rec.String(colSchedTeam),
				Locale:     rec.String(colSchedLocale),
				Enabled:    rec.Bool(colSchedEnabled),
			})
		}
		return nil
	})
	return out, err
}

func (s *storeScheduler) DeleteSchedule(ctx context.Context, tenant model.TenantID, id string) error {
	if s.data == nil {
		return errors.New("reporting: scheduler has no data handle")
	}
	return s.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		if err := repo.Delete(ctx, model.ID(id)); err != nil {
			return err
		}
		// Best-effort cascade: drop this schedule's run history.
		runRepo, err := sc.Ext(scheduleRunKind)
		if err != nil {
			return err
		}
		runs, _, err := runRepo.List(ctx, model.Query{Filters: []model.Filter{{Column: colRunScheduleID, Op: model.OpEq, Value: id}}, Limit: 1000})
		if err != nil {
			return err
		}
		for _, run := range runs {
			if derr := runRepo.Delete(ctx, model.ID(run.String(model.ColID))); derr != nil {
				return derr
			}
		}
		return nil
	})
}

func (s *storeScheduler) RecordRun(ctx context.Context, tenant model.TenantID, run ScheduleRun) error {
	if s.data == nil {
		return errors.New("reporting: scheduler has no data handle")
	}
	return s.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleRunKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colRunScheduleID: run.ScheduleID,
			colRunReportType: run.ReportType,
			colRunFormat:     run.Format,
			colRunRanAt:      run.RanAt,
			colRunStatus:     run.Status,
			colRunError:      nullable(run.Error),
			colRunOutput:     run.Output,
		}
		if _, err := repo.Create(ctx, rec); err != nil {
			return err
		}
		return pruneRuns(ctx, repo, run.ScheduleID)
	})
}

func (s *storeScheduler) ListRuns(ctx context.Context, tenant model.TenantID, scheduleID string) ([]ScheduleRun, error) {
	if s.data == nil {
		return nil, errors.New("reporting: scheduler has no data handle")
	}
	var out []ScheduleRun
	err := s.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleRunKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{{Column: colRunScheduleID, Op: model.OpEq, Value: scheduleID}},
			Limit:   1000,
		})
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out = append(out, ScheduleRun{
				ID:         rec.String(model.ColID),
				ScheduleID: rec.String(colRunScheduleID),
				ReportType: rec.String(colRunReportType),
				Format:     rec.String(colRunFormat),
				RanAt:      rec.String(colRunRanAt),
				Status:     rec.String(colRunStatus),
				Error:      rec.String(colRunError),
				Output:     rec.Bytes(colRunOutput),
			})
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].RanAt < out[j].RanAt })
		return nil
	})
	return out, err
}

// pruneRuns keeps only the most recent maxRunsPerSchedule runs for a schedule.
func pruneRuns(ctx context.Context, repo store.GenericRepo, scheduleID string) error {
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{{Column: colRunScheduleID, Op: model.OpEq, Value: scheduleID}},
		Limit:   1000,
	})
	if err != nil {
		return err
	}
	if len(recs) <= maxRunsPerSchedule {
		return nil
	}
	sort.SliceStable(recs, func(i, j int) bool {
		return recs[i].String(colRunRanAt) < recs[j].String(colRunRanAt)
	})
	for _, rec := range recs[:len(recs)-maxRunsPerSchedule] {
		if derr := repo.Delete(ctx, model.ID(rec.String(model.ColID))); derr != nil {
			return derr
		}
	}
	return nil
}

// ---- branding --------------------------------------------------------------------

type storeBranding struct{ data api.ModuleData }

// NewStoreBranding returns a store-backed BrandingProvider (one row per tenant).
func NewStoreBranding() BrandingProvider { return &storeBranding{} }

func (b *storeBranding) bindData(d api.ModuleData) { b.data = d }

func (b *storeBranding) GetBranding(ctx context.Context, tenant model.TenantID) (BrandingConfig, error) {
	if b.data == nil {
		return BrandingConfig{}, errors.New("reporting: branding has no data handle")
	}
	var cfg BrandingConfig
	err := b.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(brandingKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			return nil
		}
		rec := recs[0]
		cfg = BrandingConfig{
			LogoPath:       rec.String(colBrandLogo),
			PrimaryColor:   rec.String(colBrandPrimary),
			SecondaryColor: rec.String(colBrandSecondary),
			FooterText:     rec.String(colBrandFooter),
			CompanyName:    rec.String(colBrandCompany),
		}
		return nil
	})
	return cfg, err
}

func (b *storeBranding) SetBranding(ctx context.Context, tenant model.TenantID, cfg BrandingConfig) error {
	if b.data == nil {
		return errors.New("reporting: branding has no data handle")
	}
	return b.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(brandingKind)
		if err != nil {
			return err
		}
		fields := model.Record{
			colBrandLogo:      nullable(cfg.LogoPath),
			colBrandPrimary:   nullable(cfg.PrimaryColor),
			colBrandSecondary: nullable(cfg.SecondaryColor),
			colBrandFooter:    nullable(cfg.FooterText),
			colBrandCompany:   nullable(cfg.CompanyName),
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) > 0 {
			rec := recs[0]
			for k, v := range fields {
				rec[k] = v
			}
			_, uerr := repo.Update(ctx, rec)
			return uerr
		}
		_, cerr := repo.Create(ctx, fields)
		return cerr
	})
}

// ---- custom templates ------------------------------------------------------------

type storeTemplates struct{ data api.ModuleData }

// NewStoreCustomTemplates returns a store-backed CustomTemplateProvider (one row
// per (tenant, report_type)).
func NewStoreCustomTemplates() CustomTemplateProvider { return &storeTemplates{} }

func (t *storeTemplates) bindData(d api.ModuleData) { t.data = d }

func (t *storeTemplates) GetTemplate(ctx context.Context, tenant model.TenantID, reportType ReportType) (string, bool, error) {
	if t.data == nil {
		return "", false, errors.New("reporting: templates have no data handle")
	}
	var html string
	var found bool
	err := t.data.View(ctx, tenant, func(sc store.Scope) error {
		rec, ok, err := t.findTemplate(ctx, sc, reportType)
		if err != nil || !ok {
			return err
		}
		html = rec.String(colTmplHTML)
		found = true
		return nil
	})
	return html, found, err
}

func (t *storeTemplates) SetTemplate(ctx context.Context, tenant model.TenantID, reportType ReportType, html string) error {
	if t.data == nil {
		return errors.New("reporting: templates have no data handle")
	}
	if err := ValidateCustomTemplate(html); err != nil {
		return fmt.Errorf("reporting: template does not parse: %w", err)
	}
	return t.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(templateKind)
		if err != nil {
			return err
		}
		rec, ok, err := t.findTemplate(ctx, sc, reportType)
		if err != nil {
			return err
		}
		if ok {
			rec[colTmplHTML] = html
			_, uerr := repo.Update(ctx, rec)
			return uerr
		}
		_, cerr := repo.Create(ctx, model.Record{
			colTmplReportType: string(reportType),
			colTmplHTML:       html,
		})
		return cerr
	})
}

func (t *storeTemplates) DeleteTemplate(ctx context.Context, tenant model.TenantID, reportType ReportType) error {
	if t.data == nil {
		return errors.New("reporting: templates have no data handle")
	}
	return t.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(templateKind)
		if err != nil {
			return err
		}
		rec, ok, err := t.findTemplate(ctx, sc, reportType)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no custom template for %s", reportType)
		}
		return repo.Delete(ctx, model.ID(rec.String(model.ColID)))
	})
}

func (t *storeTemplates) findTemplate(ctx context.Context, sc store.Scope, reportType ReportType) (model.Record, bool, error) {
	repo, err := sc.Ext(templateKind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{{Column: colTmplReportType, Op: model.OpEq, Value: string(reportType)}},
		Limit:   1,
	})
	if err != nil {
		return nil, false, err
	}
	if len(recs) == 0 {
		return nil, false, nil
	}
	return recs[0], true, nil
}

// nullable returns nil for an empty string (so the column stores NULL) and the
// string otherwise — keeps optional text columns honestly empty, not "".
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
