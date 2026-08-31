// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// ArchiveSink is where archive objects (segments, manifests, keys.json) are
// durably written. The WORM sink is connectors/s3archive (S3
// object-lock), adapted by the composition root; DirSink below is the
// directory sink for tests, air-gapped exports and the CLI. A sink must be
// safe for sequential reuse; the archival loop never writes one key from two
// goroutines.
type ArchiveSink interface {
	// Put writes one object. Implementations verify body against
	// opts.ContentSHA256 when set (fail-closed: a corrupt buffer must not be
	// sealed), apply the requested retention/lock when the substrate supports
	// it, and report honestly in the receipt what was verified.
	Put(ctx context.Context, key string, body []byte, opts ArchivePutOptions) (ArchiveReceipt, error)
}

// ArchivePutOptions carry per-object integrity and immutability requests.
type ArchivePutOptions struct {
	// ContentSHA256 is the hex SHA-256 of body ("" skips the check).
	ContentSHA256 string
	// RetainUntil requests object-lock retention until the instant; the zero
	// time defers to the sink/bucket default.
	RetainUntil time.Time
	// LegalHold requests an object-lock legal hold (independent of retention).
	LegalHold bool
}

// ArchiveReceipt is the sink's non-secret write attestation, recorded in the
// segment anchor event (AnchorSegment) as custody evidence.
type ArchiveReceipt struct {
	// Location is where the object landed (a URL, bucket/key, or file path).
	Location string
	// ETag and VersionID are the substrate's object identifiers ("" when the
	// substrate has none).
	ETag      string
	VersionID string
	// LockMode is the immutability mode actually applied ("" when none).
	LockMode string
	// RetainUntil is the retention applied (zero when none).
	RetainUntil time.Time
	// LockVerified is true only when the sink CONFIRMED the lock after writing
	// (e.g. s3archive's verify-after-write HEAD). A sink that cannot enforce a
	// lock reports false — it never claims immutability it does not have.
	LockVerified bool
}

// DirSink writes archive objects under a root directory, then drops write
// permission (0o444) on each file. This is the filelog WORM posture (docs/SECURITY-HARDENING.md
// §5): the copy is truly immutable only when the substrate is (a WORM share, a
// write-once medium, an append-only volume) — the chmod prevents accidental
// overwrites, not a root attacker, and the receipt honestly reports
// LockVerified=false. Re-putting a key with identical content succeeds
// (idempotent recovery, mirroring s3archive's re-PUT semantics); different
// content for an existing key is refused.
type DirSink struct {
	root string
}

// NewDirSink creates (if needed) the root directory and returns a sink over it.
func NewDirSink(root string) (*DirSink, error) {
	if root == "" {
		return nil, fmt.Errorf("audit: dir sink: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("audit: dir sink: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("audit: dir sink: %w", err)
	}
	return &DirSink{root: abs}, nil
}

// Root returns the sink's absolute root directory.
func (s *DirSink) Root() string { return s.root }

// Put writes one object as a read-only file under the root. Keys are
// slash-separated relative paths (SegmentKey et al.); anything absolute or
// escaping the root is rejected.
func (s *DirSink) Put(_ context.Context, key string, body []byte, opts ArchivePutOptions) (ArchiveReceipt, error) {
	clean := path.Clean(key)
	if key == "" || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return ArchiveReceipt{}, fmt.Errorf("audit: dir sink: invalid key %q", key)
	}
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if opts.ContentSHA256 != "" && !strings.EqualFold(opts.ContentSHA256, got) {
		return ArchiveReceipt{}, fmt.Errorf("audit: dir sink: content sha256 mismatch for %q", key)
	}
	target := filepath.Join(s.root, filepath.FromSlash(clean))
	if existing, err := os.ReadFile(target); err == nil {
		// Idempotent recovery: a retried Put of the same bytes is a success; a
		// DIFFERENT body for an existing key is exactly what WORM must refuse.
		esum := sha256.Sum256(existing)
		if esum == sum {
			return s.receipt(target, got, opts), nil
		}
		return ArchiveReceipt{}, fmt.Errorf("audit: dir sink: %q already exists with different content", key)
	} else if !os.IsNotExist(err) {
		return ArchiveReceipt{}, fmt.Errorf("audit: dir sink: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return ArchiveReceipt{}, fmt.Errorf("audit: dir sink: %w", err)
	}
	// Write to a temp name then rename, so a reader never sees a partial
	// object; chmod before the rename so the visible file is born read-only.
	tmp, err := os.CreateTemp(filepath.Dir(target), ".put-*")
	if err != nil {
		return ArchiveReceipt{}, fmt.Errorf("audit: dir sink: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return ArchiveReceipt{}, fmt.Errorf("audit: dir sink: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return ArchiveReceipt{}, fmt.Errorf("audit: dir sink: %w", err)
	}
	if err := os.Chmod(tmpName, 0o444); err != nil {
		_ = os.Remove(tmpName)
		return ArchiveReceipt{}, fmt.Errorf("audit: dir sink: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return ArchiveReceipt{}, fmt.Errorf("audit: dir sink: %w", err)
	}
	return s.receipt(target, got, opts), nil
}

// receipt builds the honest DirSink receipt: the file path and digest, with no
// lock claim (the chmod is not an enforced lock) and no retention echo.
func (s *DirSink) receipt(target, sha string, _ ArchivePutOptions) ArchiveReceipt {
	return ArchiveReceipt{Location: target, ETag: sha, LockVerified: false}
}

// Compile-time proof DirSink satisfies the sink seam.
var _ ArchiveSink = (*DirSink)(nil)
