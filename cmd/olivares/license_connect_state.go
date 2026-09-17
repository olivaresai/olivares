// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/license/connectv1"
	"github.com/olivaresai/olivares/core/secure"
)

// license_connect_state.go is the connected client's durable state (connect-v1 contract §6).
//
// Everything lives under the SELECTED data directory, in <data-dir>/connect (0700):
//
//	identity.key       the deployment's Ed25519 PoP key (0600, created exclusively, never rewritten)
//	identity.next.key  the proposed key during rotation, recovery or reactivation (0600)
//	state.json         endpoint, commercial identifiers, binding and the pending operation (0600)
//	ota.token          the last download bearer (0600); never printed
//	.lock              the invocation lease
//
// The pending operation — its exact body bytes, digest, route, target, epoch, signer role and
// Idempotency-Key — is persisted and fsynced BEFORE any request is sent, so a crash, timeout or
// lost response is retried as the same operation with a fresh challenge. A second environment
// uses another data directory and therefore another identity and binding. Copying the whole
// directory copies the identity: that clone is the same principal until rotation or revocation.
//
// A key transition (rotate, recover, reactivate) whose answer verified is recorded in state.json
// as its completion BEFORE identity.next.key replaces identity.key. Until that record is durable
// the pending step and both identity files stay, so the step can be repeated; once it is durable
// the transition finishes locally and nothing is sent again (connectRun.commitKeyTransition). A
// record that is only visible is not taken as durable: every promotion is preceded by publishing
// the record, with every sync succeeding, in the same call (connectRun.completeKeyTransition).

const (
	connectDirName          = "connect"
	connectLockFileName     = ".lock"
	connectIdentityFileName = "identity.key"
	connectNextKeyFileName  = "identity.next.key"
	connectStateFileName    = "state.json"
	connectTokenFileName    = "ota.token"

	connectStateSchema    = "olivares.connect.client.v1"
	connectIdentitySchema = "olivares.connect.identity.v1"

	// connectStateMaxBytes bounds state.json; a pending body is at most connectv1.BodyMax.
	connectStateMaxBytes = 64 << 10
)

var (
	errConnectBusy        = errors.New("another connected-client operation holds this data directory")
	errConnectStateUnsafe = errors.New("the connected-client state is not owner-only regular files")
	errConnectNoState     = errors.New("this data directory has no connected-client state; run `olivares license connect start` first")
)

func connectDir(dataDir string) string { return filepath.Join(dataDir, connectDirName) }

// connectBinding is the deployment this identity is bound to.
type connectBinding struct {
	DeploymentID string `json:"deployment_id"`
	PopKID       string `json:"pop_kid"`
	BindingEpoch int64  `json:"binding_epoch"`
	Slot         *int64 `json:"slot,omitempty"`
	// Status is "active" or "deleted". A deleted binding keeps its identity and history.
	Status string `json:"status"`
}

// connectApprovalRequest is an owner-approval request this client created.
type connectApprovalRequest struct {
	// Operation is bind, recover, reactivate or delete.
	Operation          string `json:"operation"`
	RequestID          string `json:"request_id"`
	ApprovalURL        string `json:"approval_url"`
	TargetDeploymentID string `json:"target_deployment_id,omitempty"`
	// SignerRole names the identity file whose key made the request: current or next.
	SignerRole string `json:"signer_role"`
	CreatedAt  string `json:"created_at"`
}

// connectPendingOp is one persisted protocol operation.
type connectPendingOp struct {
	// Intent is the user operation this step belongs to: bind, refresh, rotate, recover,
	// reactivate or delete. A command resumes only a pending operation of its own intent.
	Intent string `json:"intent"`
	// Phase is the client step: request, complete, refresh, rotate or delete.
	Phase string `json:"phase"`
	// Operation is the challenge operation name.
	Operation    string `json:"operation"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Target       string `json:"target"`
	BindingEpoch int64  `json:"binding_epoch"`
	// SignerRole is current or next: the identity that signs the primary proof.
	SignerRole string `json:"signer_role"`
	// NewKeyProof adds the proposed key's second proof over the same message (normal rotation).
	NewKeyProof    bool   `json:"new_key_proof,omitempty"`
	Body           string `json:"body"`
	BodySHA256     string `json:"body_sha256"`
	IdempotencyKey string `json:"idempotency_key"`
	CreatedAt      string `json:"created_at"`
	// Attempts counts sends of this exact operation, for status and diagnostics.
	Attempts int `json:"attempts"`
}

func (p *connectPendingOp) bodyBytes() ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(p.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: the pending operation body does not decode", errConnectStateUnsafe)
	}
	if connectv1.BodyDigest(b) != p.BodySHA256 {
		return nil, fmt.Errorf("%w: the pending operation body does not match its digest", errConnectStateUnsafe)
	}
	return b, nil
}

// connectCompletion is a verified key transition whose local completion is owed: the answer to the
// pending rotate, recover or reactivate step verified and its credential is installed; the identity
// promotion and the new binding remain. While it is present the pending step is kept and never sent.
type connectCompletion struct {
	// Intent is rotate, recover or reactivate, and equals the pending step's intent.
	Intent       string `json:"intent"`
	DeploymentID string `json:"deployment_id"`
	// PopKID is the key the service bound: the proposed identity's KID.
	PopKID       string `json:"pop_kid"`
	BindingEpoch int64  `json:"binding_epoch"`
	Slot         *int64 `json:"slot,omitempty"`
	VerifiedAt   string `json:"verified_at"`
}

// connectLastResult records the last accepted credential without its bytes.
type connectLastResult struct {
	Phase            string `json:"phase"`
	CredentialSerial string `json:"credential_serial"`
	CredentialSHA256 string `json:"credential_sha256"`
	Version          string `json:"version,omitempty"`
	OTAExp           int64  `json:"ota_exp,omitempty"`
	// EffectiveUntil is the refresh PLANNING boundary derived from the signed grants active at install
	// (connectRefreshBoundary), not a summary of whether every line remains valid.
	EffectiveUntil string `json:"effective_until,omitempty"`
	At             string `json:"at"`
}

type connectState struct {
	Schema     string                  `json:"schema"`
	Endpoint   string                  `json:"endpoint"`
	Provider   string                  `json:"provider"`
	BusinessID string                  `json:"business_id"`
	HolderID   string                  `json:"holder_id"`
	LicenseID  string                  `json:"license_id"`
	Purpose    string                  `json:"purpose"`
	Parent     string                  `json:"parent,omitempty"`
	Label      string                  `json:"label"`
	Channel    string                  `json:"channel"`
	Binding    *connectBinding         `json:"binding,omitempty"`
	Request    *connectApprovalRequest `json:"request,omitempty"`
	Pending    *connectPendingOp       `json:"pending,omitempty"`
	Completion *connectCompletion      `json:"completion,omitempty"`
	Last       *connectLastResult      `json:"last,omitempty"`
}

// connectStore is an open, leased connected-client directory. Every mutation of the directory
// happens while the lease is held.
type connectStore struct {
	dataDir string
	dir     string
	lease   *os.File
	now     func() time.Time
	random  io.Reader
}

// openConnectStore creates (0700) and leases the connect directory of dataDir.
func openConnectStore(dataDir string, now func() time.Time) (*connectStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("the connected client needs a data directory")
	}
	if err := secure.EnsureDataDir(dataDir); err != nil {
		return nil, err
	}
	dir := connectDir(dataDir)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", dir, err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s must be a directory with mode 0700 (found %s %04o)", errConnectStateUnsafe, dir, info.Mode().Type(), info.Mode().Perm())
	}
	lease, err := acquireConnectLease(dir)
	if err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	return &connectStore{dataDir: dataDir, dir: dir, lease: lease, now: now, random: rand.Reader}, nil
}

// Close releases the lease.
func (s *connectStore) Close() error {
	if s == nil || s.lease == nil {
		return nil
	}
	err := s.lease.Close()
	s.lease = nil
	return err
}

func (s *connectStore) path(name string) string { return filepath.Join(s.dir, name) }

// readOwnerOnly reads a regular file that group and others cannot read or write.
func readOwnerOnly(path string, max int64) ([]byte, bool, error) {
	before, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 {
		return nil, true, fmt.Errorf("%w: %s must be a regular 0600 file (found %s %04o)", errConnectStateUnsafe, path, before.Mode().Type(), before.Mode().Perm())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, true, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, true, fmt.Errorf("%w: %s changed while it was opened", errConnectStateUnsafe, path)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, true, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > max {
		return nil, true, fmt.Errorf("%w: %s exceeds %d bytes", errConnectStateUnsafe, path, max)
	}
	return data, true, nil
}

// writeAtomic0600 publishes data at path: a same-directory temp file (0600), write, fsync, close,
// rename, then fsync of the directory. Before the rename the previous file stands; after it the
// new bytes are current even if the directory sync reports an error.
func writeAtomic0600(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", path, err)
	}
	name := tmp.Name()
	discard := func(cause error) error {
		_ = tmp.Close()
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return errors.Join(cause, rerr)
		}
		return cause
	}
	if err := tmp.Chmod(0o600); err != nil {
		return discard(fmt.Errorf("chmod staged %s: %w", path, err))
	}
	if _, err := tmp.Write(data); err != nil {
		return discard(fmt.Errorf("write staged %s: %w", path, err))
	}
	if err := tmp.Sync(); err != nil {
		return discard(fmt.Errorf("sync staged %s: %w", path, err))
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close staged %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("publish %s: %w", path, err)
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open %s for sync: %w", dir, err)
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return fmt.Errorf("sync %s: %w", dir, serr)
	}
	if cerr != nil {
		return fmt.Errorf("close %s: %w", dir, cerr)
	}
	return nil
}

// loadState returns the state, or (nil, nil) when none exists.
func (s *connectStore) loadState() (*connectState, error) {
	data, present, err := readOwnerOnly(s.path(connectStateFileName), connectStateMaxBytes)
	if err != nil || !present {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var st connectState
	if err := dec.Decode(&st); err != nil {
		return nil, fmt.Errorf("%w: %s does not decode: %v", errConnectStateUnsafe, s.path(connectStateFileName), err)
	}
	if st.Schema != connectStateSchema {
		return nil, fmt.Errorf("%w: %s has schema %q", errConnectStateUnsafe, s.path(connectStateFileName), st.Schema)
	}
	return &st, nil
}

// saveState publishes st durably.
func (s *connectStore) saveState(st *connectState) error {
	st.Schema = connectStateSchema
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic0600(s.path(connectStateFileName), append(data, '\n'))
}

// connectIdentity is a loaded PoP key.
type connectIdentity struct {
	KID     string
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

type connectIdentityWire struct {
	Schema    string `json:"schema"`
	KID       string `json:"kid"`
	PublicKey string `json:"public_key"`
	Seed      string `json:"seed"`
}

func (s *connectStore) identityFile(role string) (string, error) {
	switch role {
	case "current":
		return s.path(connectIdentityFileName), nil
	case "next":
		return s.path(connectNextKeyFileName), nil
	}
	return "", fmt.Errorf("unknown identity role %q", role)
}

// loadIdentity reads the identity for role; ok is false when it does not exist.
func (s *connectStore) loadIdentity(role string) (connectIdentity, bool, error) {
	path, err := s.identityFile(role)
	if err != nil {
		return connectIdentity{}, false, err
	}
	data, present, err := readOwnerOnly(path, 4096)
	if err != nil || !present {
		return connectIdentity{}, present, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w connectIdentityWire
	if err := dec.Decode(&w); err != nil || w.Schema != connectIdentitySchema {
		return connectIdentity{}, true, fmt.Errorf("%w: %s is not a connect identity", errConnectStateUnsafe, path)
	}
	seed, err := base64.RawURLEncoding.DecodeString(w.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return connectIdentity{}, true, fmt.Errorf("%w: %s carries no valid seed", errConnectStateUnsafe, path)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	kid, _ := connectv1.KID(pub)
	if connectv1.EncodePublicKey(pub) != w.PublicKey || kid != w.KID {
		return connectIdentity{}, true, fmt.Errorf("%w: %s names a public key its seed does not produce", errConnectStateUnsafe, path)
	}
	return connectIdentity{KID: kid, Public: pub, Private: priv}, true, nil
}

// ensureIdentity loads the identity for role or creates it exclusively. Creation stages a fsynced
// temp file and hard-links it into place, so a concurrent or crashed creator can never leave a
// second key under the same name.
func (s *connectStore) ensureIdentity(role string) (connectIdentity, bool, error) {
	id, present, err := s.loadIdentity(role)
	if err != nil || present {
		return id, false, err
	}
	path, err := s.identityFile(role)
	if err != nil {
		return connectIdentity{}, false, err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(s.random, seed); err != nil {
		return connectIdentity{}, false, fmt.Errorf("draw a PoP key: %w", err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	kid, _ := connectv1.KID(pub)
	data, err := json.MarshalIndent(connectIdentityWire{Schema: connectIdentitySchema, KID: kid,
		PublicKey: connectv1.EncodePublicKey(pub), Seed: base64.RawURLEncoding.EncodeToString(seed)}, "", "  ")
	if err != nil {
		return connectIdentity{}, false, err
	}
	tmp, err := os.CreateTemp(s.dir, ".identity.tmp-*")
	if err != nil {
		return connectIdentity{}, false, fmt.Errorf("stage %s: %w", path, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return connectIdentity{}, false, err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return connectIdentity{}, false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return connectIdentity{}, false, err
	}
	if err := tmp.Close(); err != nil {
		return connectIdentity{}, false, err
	}
	if err := os.Link(name, path); err != nil {
		return connectIdentity{}, false, fmt.Errorf("create %s exclusively: %w", path, err)
	}
	if err := syncDir(s.dir); err != nil {
		return connectIdentity{}, false, err
	}
	return connectIdentity{KID: kid, Public: pub, Private: priv}, true, nil
}

// promoteIdentity makes the identity whose KID is kid the current one. It is the only step that
// destroys a key, its caller runs it only after publishing the verified completion with every sync
// succeeding in the same call, and it can be repeated after a crash at any point:
//
//	identity.next.key holds kid                  rename it over identity.key, then sync the directory
//	no identity.next.key, identity.key holds kid an earlier run renamed it; sync the directory again
//	anything else                                refused, nothing changed
//
// A directory sync error is returned, so the caller never records the new binding over a rename
// that might not survive a power loss.
func (s *connectStore) promoteIdentity(kid string) error {
	next, present, err := s.loadIdentity("next")
	if err != nil {
		return err
	}
	if present {
		if next.KID != kid {
			return fmt.Errorf("%w: the proposed identity is not the key the recorded completion binds", errConnectStateUnsafe)
		}
		if err := os.Rename(s.path(connectNextKeyFileName), s.path(connectIdentityFileName)); err != nil {
			return fmt.Errorf("promote the proposed identity: %w", err)
		}
		return syncDir(s.dir)
	}
	cur, present, err := s.loadIdentity("current")
	if err != nil {
		return err
	}
	if !present || cur.KID != kid {
		return fmt.Errorf("%w: no identity file holds the key the recorded completion binds", errConnectStateUnsafe)
	}
	return syncDir(s.dir)
}

// writeToken persists the download bearer. It is never printed.
func (s *connectStore) writeToken(token string) error {
	return writeAtomic0600(s.path(connectTokenFileName), []byte(token+"\n"))
}

// newPending builds and returns an unsent operation over exact body bytes.
func (s *connectStore) newPending(intent, phase, operation, target string, epoch int64, signerRole string, newKeyProof bool, body []byte) (*connectPendingOp, error) {
	if len(body) > connectv1.BodyMax {
		return nil, fmt.Errorf("the %s request body is %d bytes, above the service's %d-byte bound", phase, len(body), connectv1.BodyMax)
	}
	method, path, err := connectv1.IntendedRoute(operation, target)
	if err != nil {
		return nil, err
	}
	idem, err := connectv1.NewIdempotencyKey(s.random)
	if err != nil {
		return nil, err
	}
	return &connectPendingOp{
		Intent: intent, Phase: phase, Operation: operation, Method: method, Path: path, Target: target,
		BindingEpoch: epoch, SignerRole: signerRole, NewKeyProof: newKeyProof,
		Body: base64.StdEncoding.EncodeToString(body), BodySHA256: connectv1.BodyDigest(body),
		IdempotencyKey: idem, CreatedAt: s.now().UTC().Format(time.RFC3339),
	}, nil
}
