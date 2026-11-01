// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/cron"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type drProtection struct {
	LocalTarget string     `json:"local_target,omitempty"`
	VerifyCron  string     `json:"verify_cron,omitempty"`
	RPOHours    int        `json:"rpo_hours,omitempty"`
	Offsite     *drOffsite `json:"offsite,omitempty"`
}

type drVerificationResult struct {
	LastVerify       string `json:"last_verify,omitempty"`
	LastVerifyStatus string `json:"last_verify_status,omitempty"`
	LastVerifyError  string `json:"last_verify_error,omitempty"`
}

type drOffsite struct {
	Endpoint      string `json:"endpoint,omitempty"`
	Bucket        string `json:"bucket"`
	Region        string `json:"region,omitempty"`
	Prefix        string `json:"prefix,omitempty"`
	CredentialRef string `json:"credential_ref"`
	GFS           drGFS  `json:"gfs"`
}

type drGFS struct {
	Daily    int `json:"daily"`
	Weekly   int `json:"weekly"`
	Monthly  int `json:"monthly"`
	Yearly   int `json:"yearly"`
	KeepLast int `json:"keep_last"`
}

func validateDRProtection(req drScheduleRequest) error {
	if req.LocalTarget != nil && !validDRLocalTarget(*req.LocalTarget) {
		return errors.New("local_target must name a managed backup disk")
	}
	if req.VerifyCron != nil && strings.TrimSpace(*req.VerifyCron) != "" {
		if _, err := cron.Parse(strings.TrimSpace(*req.VerifyCron)); err != nil {
			return fmt.Errorf("invalid verify_cron: %w", err)
		}
	}
	if req.RPOHours != nil && (*req.RPOHours < 1 || *req.RPOHours > 8760) {
		return errors.New("rpo_hours must be between 1 and 8760")
	}
	o := req.Offsite
	if o == nil || o.Bucket == "" {
		return nil
	}
	name, ok := strings.CutPrefix(o.CredentialRef, "store:")
	if !ok || auth.ValidateSecretName(name) != "" {
		return errors.New("offsite credentials need a store:<name> reference")
	}
	if o.Endpoint != "" {
		u, err := url.Parse(o.Endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("offsite endpoint must be HTTPS without credentials, query or fragment")
		}
	}
	for _, n := range []int{o.GFS.Daily, o.GFS.Weekly, o.GFS.Monthly, o.GFS.Yearly, o.GFS.KeepLast} {
		if n < 0 || n > 3650 {
			return errors.New("offsite GFS counts must be between 0 and 3650")
		}
	}
	return nil
}

// Every disk the product mounts for backups, whether or not the schedule names it.
var drManagedBundleGlob = "/mnt/olivares-backups/*/*.drbundle"

// The recovery secret is generated on the data volume, never baked into an image
// or included among the sealed signing keys. A link publishes it without replacing
// an existing key; an interrupted write cannot publish a partial secret.
func (s *Server) prepareApplianceRecoveryKey(allowCreate bool) error {
	path := s.drSvc.cfg.PassphraseFile
	if !s.drSvc.autoRecoveryKey {
		_, err := s.drSchedulePassphrase()
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return errors.New("appliance recovery key must be a regular file with mode 0600")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(key) != 32 {
			return errors.New("appliance recovery key must encode 32 bytes")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if !allowCreate {
		return errors.New("appliance recovery key is missing; restore the original key")
	}
	// Existing bundles must remain recoverable with their original key.
	for _, pattern := range []string{filepath.Join(s.drSvc.cfg.BackupDir, "*.drbundle"), drManagedBundleGlob} {
		bundles, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		if len(bundles) != 0 {
			return errors.New("recovery key is missing while backups exist; restore the original key")
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generate recovery key: %w", err)
	}
	f, err := os.CreateTemp(s.drSvc.cfg.DataDir, ".dr-recovery-*")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if _, err = f.WriteString(base64.StdEncoding.EncodeToString(raw)); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Link(f.Name(), path); err != nil {
		return fmt.Errorf("publish recovery key: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// replicateScheduledBackup copies the bundle offsite and applies the offsite GFS
// policy. It returns the bucket's Object Lock state (see drSchedule.OffsiteLock),
// "" when no client was built, alongside the copy's outcome.
func (s *Server) replicateScheduledBackup(ctx context.Context, o *drOffsite, path string, now time.Time) (string, error) {
	if s.secretStore == nil {
		return "", errSecretStoreUnavailable
	}
	raw, err := s.secretStore.Resolve(ctx, auth.GlobalSecretScope, strings.TrimPrefix(o.CredentialRef, "store:"))
	if err != nil {
		return "", fmt.Errorf("resolve offsite credential: %w", err)
	}
	var credentials struct {
		AccessKeyID     string `json:"access_key_id"`
		SecretAccessKey string `json:"secret_access_key"`
		SessionToken    string `json:"session_token"`
	}
	if json.Unmarshal(raw, &credentials) != nil {
		return "", errors.New("offsite credential must contain access_key_id and secret_access_key JSON fields")
	}
	client, err := dr.NewOffsiteClient(dr.OffsiteConfig{Endpoint: o.Endpoint, Bucket: o.Bucket, Region: o.Region, Prefix: o.Prefix, AccessKeyID: credentials.AccessKeyID, SecretAccessKey: credentials.SecretAccessKey, SessionToken: credentials.SessionToken})
	if err != nil {
		return "", err
	}
	lock := offsiteLockState(ctx, client)
	f, err := os.Open(path)
	if err != nil {
		return lock, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return lock, err
	}
	name := filepath.Base(path)
	if err := client.Put(ctx, name, f, info.Size()); err != nil {
		return lock, err
	}
	p := dr.GFSPolicy{Daily: o.GFS.Daily, Weekly: o.GFS.Weekly, Monthly: o.GFS.Monthly, Yearly: o.GFS.Yearly, KeepLast: o.GFS.KeepLast}
	if p.IsZero() {
		return lock, nil
	}
	metas, err := client.RetentionBundles(ctx)
	if err != nil {
		return lock, err
	}
	for _, old := range dr.PlanGFS(metas, p, now).Delete {
		if old.Name == name {
			continue
		}
		if err := client.Delete(ctx, old.Name); err != nil {
			// A write-only credential is refused here by design: record the attempt,
			// since a delete from this server is what a stolen credential would try.
			s.auditOffsiteDeleteFailed(ctx, o, old.Name, err)
			return lock, fmt.Errorf("offsite retention: %w", err)
		}
	}
	return lock, nil
}

// offsiteLockState names the bucket's Object Lock for drSchedule.OffsiteLock.
func offsiteLockState(ctx context.Context, client *dr.OffsiteClient) string {
	st, err := client.LockStatus(ctx)
	switch {
	case err != nil:
		return "unknown"
	case !st.Enabled:
		return "none"
	case st.DefaultMode == "":
		return "no_default"
	default:
		return strings.ToLower(st.DefaultMode)
	}
}

func (s *Server) auditOffsiteDeleteFailed(ctx context.Context, o *drOffsite, object string, cause error) {
	err := s.st.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: drScheduleActor, ActorKind: "system", Action: "dr.offsite.delete_failed", Meta: map[string]any{"bucket": o.Bucket, "prefix": o.Prefix, "object": object, "error": cause.Error()}})
		return err
	})
	if err != nil {
		s.log.Error("dr: could not audit a failed offsite delete", "object", object, "err", err)
	}
}

func (s *Server) runDueVerification(ctx context.Context, now time.Time) (bool, error) {
	now = now.UTC()
	sched := s.drSvc.scheduleSnapshot()
	if sched.VerifyCron == "" {
		return false, nil
	}
	spec, err := cron.Parse(sched.VerifyCron)
	if err != nil {
		return false, fmt.Errorf("stored verify_cron is invalid: %w", err)
	}
	var last time.Time
	if sched.LastVerify != "" {
		last, err = time.Parse(time.RFC3339, sched.LastVerify)
		if err != nil {
			return false, fmt.Errorf("stored verification instant is invalid: %w", err)
		}
	}
	if !spec.DueSince(last, now) {
		return false, nil
	}
	sched.LastVerify = now.Format(time.RFC3339)
	sched.LastVerifyStatus = drJobRunning
	sched.LastVerifyError = ""
	if err := s.saveDRSchedule(ctx, sched); err != nil {
		return false, fmt.Errorf("claim verification: %w", err)
	}
	s.drSvc.setSchedule(func(d *drSchedule) { *d = sched })
	verifyErr := s.verifyScheduledBundles(ctx)
	sched.LastVerifyStatus = drJobCompleted
	if verifyErr != nil {
		sched.LastVerifyStatus = drJobFailed
		sched.LastVerifyError = verifyErr.Error()
	}
	if err := s.persistDRSchedule(ctx, sched, "dr.verify."+sched.LastVerifyStatus); err != nil {
		sched.LastVerifyStatus = drJobFailed
		sched.LastVerifyError = "verification result could not be audited or persisted"
		s.drSvc.setSchedule(func(d *drSchedule) { *d = sched })
		// If only the audit append failed, retain a durable failure rather than a
		// permanently running claim. This never certifies an unaudited restore test.
		saveErr := s.saveDRSchedule(ctx, sched)
		return false, errors.Join(verifyErr, fmt.Errorf("record verification: %w", err), saveErr)
	}
	s.drSvc.setSchedule(func(d *drSchedule) { *d = sched })
	return verifyErr == nil, verifyErr
}

func (s *Server) verifyScheduledBundles(ctx context.Context) error {
	pass, err := s.drSchedulePassphrase()
	if err != nil {
		return err
	}
	name := s.drSvc.scheduleSnapshot().LastBundle
	if name == "" {
		return errors.New("no completed scheduled backup to verify")
	}
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".drbundle") {
		return errors.New("stored scheduled bundle name is invalid")
	}
	// Manual backups may use different operator-held passphrases. The unattended
	// restore test checks the last scheduled restore point with its configured key.
	if err := s.verifyScheduledBundle(ctx, s.drSvc.bundlePath(name), pass); err != nil {
		return fmt.Errorf("verify scheduled backup %s: %w", name, err)
	}
	return nil
}

func (s *Server) verifyScheduledBundle(ctx context.Context, path, pass string) error {
	pin, err := s.drSvc.openBackupDirectory()
	if err != nil {
		return err
	}
	defer pin.Close()
	if filepath.Dir(path) == pin.Target {
		path = filepath.Join(pin.Path, filepath.Base(path))
	}
	work, err := os.MkdirTemp(pin.Path, "verify-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(work); err != nil {
			s.log.Error("dr: verification cleanup failed", "err", err)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	m, kdf, err := dr.ExtractBundle(f, work)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	cipher, err := dr.OpenCipher([]byte(pass), kdf)
	if err != nil {
		return err
	}
	if err := dr.VerifyBundleIntegrity(work, m, kdf, cipher, false); err != nil {
		return err
	}
	report, err := verifyBundleScratch(ctx, work, m, cipher, s.drSvc.cfg.RegisterSchema)
	if err != nil {
		return err
	}
	if !report.OK {
		return fmt.Errorf("ledger continuity failed: %s", strings.Join(report.Problems, "; "))
	}
	return nil
}
