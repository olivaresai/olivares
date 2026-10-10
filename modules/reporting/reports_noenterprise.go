//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package reporting

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/core/api"
	"log/slog"
	"net/http"
	"time"
)

var errReportsUnavailable = errors.New("on-demand reports require Business Compliance Packs")

type Engine struct{}
type Cache struct{}

func NewEngine(*slog.Logger) *Engine      { return nil }
func NewCache(*slog.Logger) *Cache        { return nil }
func (*Cache) Close()                     {}
func (*Cache) invalidateTenant(string)    {}
func PDFAvailable() bool                  { return false }
func ValidateCustomTemplate(string) error { return errReportsUnavailable }
func (m *Module) handleListReports(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeNotWired(w, "on-demand reports")
}
func (m *Module) handleGenerateReport(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	writeNotWired(w, "on-demand reports")
}
func (m *Module) renderScheduled(context.Context, api.ModuleContext, ScheduleConfig, time.Time) ([]byte, error) {
	return nil, errReportsUnavailable
}
