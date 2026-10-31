// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package audit

import "errors"

// ErrBusinessAudit identifies the export, verification and external-archive edition seam.
// Stored ledgers, checkpoints and archive bookkeeping are retained in every edition.
var ErrBusinessAudit = errors.New("audit export, verification and external archives require Business; the signed ledger and dr backup remain available")
