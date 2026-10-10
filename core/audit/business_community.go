// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package audit

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ExportLinked reports whether this build contains the Business export implementation.
const ExportLinked = false

func FormatEvent(model.AuditEvent, Format) (string, error) { return "", ErrBusinessAudit }
func BuildSegment(context.Context, store.AuditLog, model.TenantID, int64, int, int64) (Segment, bool, error) {
	return Segment{}, false, ErrBusinessAudit
}
func ExportSegments(context.Context, store.Store, model.TenantID, ArchiveSink, ExportOptions, func(SegmentResult) error) (ExportReport, error) {
	return ExportReport{}, ErrBusinessAudit
}
func SegmentAnchorDraft(SegmentResult) (model.AuditDraft, error) {
	return model.AuditDraft{}, ErrBusinessAudit
}
func AnchorSegment(context.Context, store.Store, model.TenantID, SegmentResult) (model.AuditEvent, error) {
	return model.AuditEvent{}, ErrBusinessAudit
}
func WriteArchiveKeys(context.Context, ArchiveSink, ArchiveKeys) (ArchiveReceipt, error) {
	return ArchiveReceipt{}, ErrBusinessAudit
}
func VerifyArchiveDir(context.Context, string, ArchiveVerifyOptions) (ArchiveVerifyReport, error) {
	return ArchiveVerifyReport{}, ErrBusinessAudit
}
func LoadArchiveKeys(string) (ArchiveKeys, bool, error) {
	return ArchiveKeys{}, false, ErrBusinessAudit
}
func NewDirSink(string) (*DirSink, error) { return nil, ErrBusinessAudit }
func (*DirSink) Root() string             { return "" }
func (*DirSink) Put(context.Context, string, []byte, ArchivePutOptions) (ArchiveReceipt, error) {
	return ArchiveReceipt{}, ErrBusinessAudit
}
