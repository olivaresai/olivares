// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/dr"
)

// drBundleListItem is a DR bundle row as `dr ls -o json` renders it. The offsite
// listing carries size and last-modified; the local one carries the creation
// time recorded in the bundle. Both shapes come from this one type, so the text
// and JSON forms cannot drift.
type drBundleListItem struct {
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes,omitempty"`
	Modified string `json:"modified,omitempty"`
	Created  string `json:"created,omitempty"`
}

// drOffsiteRef identifies one bundle ON the offsite mirror: the object name plus
// the bucket and prefix it landed in. It is ONE type used by `dr push -o json`,
// `dr pull -o json` and the offsite half of `dr backup -o json`, because those
// three commands name the same location and a script that follows a bundle
// off-box and back should not need three parsers to do it. That is also exactly
// what the text forms hide: they print the location as one arrow-joined sentence
// (`… → bucket/prefix`), so recovering the bucket from it means splitting on a
// character that a prefix is allowed to contain.
type drOffsiteRef struct {
	Object string `json:"object"`
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix"`
}

// drPushResult is what `dr push -o json` reports.
type drPushResult struct {
	In      string       `json:"in"`
	Offsite drOffsiteRef `json:"offsite"`
}

// drPullResult is what `dr pull -o json` reports.
//
// Bytes is the size actually written, and it is here because it is the one fact
// the text form could not have printed: the copy's byte count was discarded. A
// pull that produced a SHORT file is a restore that will fail later, at the worst
// possible moment, and a script that compares this against the size `dr ls
// --offsite -o json` reported can catch it now.
type drPullResult struct {
	Offsite drOffsiteRef `json:"offsite"`
	Out     string       `json:"out"`
	Bytes   int64        `json:"bytes"`
}

// Offsite replication + GFS retention CLI surface. A DR bundle that only
// lives on the host it protects is not disaster recovery (3-2-1). These commands
// mirror bundles to an S3-compatible target (AWS S3, Cloudflare R2, MinIO, Wasabi)
// and prune BOTH the local directory and the offsite mirror under a
// Grandfather-Father-Son policy. Credentials are supplied BY REFERENCE (a file or a
// standard AWS env var), never as an inline flag value that would leak into shell
// history or the process table.

// offsiteFlags configure the S3-compatible replication target. The bucket enables
// offsite operations when set; credentials resolve from a file or the standard AWS
// environment variables.
type offsiteFlags struct {
	endpoint   string
	bucket     string
	region     string
	prefix     string
	pathStyle  bool
	akidFile   string
	secretFile string
	tokenFile  string
}

func addOffsiteFlags(cmd *cobra.Command, f *offsiteFlags) {
	cmd.Flags().StringVar(&f.endpoint, "offsite-endpoint", os.Getenv("OLIVARES_DR_OFFSITE_ENDPOINT"), "S3-compatible endpoint for offsite replication (R2/MinIO/Wasabi); empty = AWS S3 from --offsite-region")
	cmd.Flags().StringVar(&f.bucket, "offsite-bucket", os.Getenv("OLIVARES_DR_OFFSITE_BUCKET"), "offsite bucket for DR bundles (set to enable offsite replication)")
	cmd.Flags().StringVar(&f.region, "offsite-region", os.Getenv("OLIVARES_DR_OFFSITE_REGION"), "offsite region (default us-east-1; Cloudflare R2 uses 'auto')")
	cmd.Flags().StringVar(&f.prefix, "offsite-prefix", os.Getenv("OLIVARES_DR_OFFSITE_PREFIX"), "key prefix within the offsite bucket")
	cmd.Flags().BoolVar(&f.pathStyle, "offsite-path-style", false, "force path-style S3 addressing (implied by a custom --offsite-endpoint)")
	cmd.Flags().StringVar(&f.akidFile, "offsite-access-key-id-file", os.Getenv("OLIVARES_DR_OFFSITE_ACCESS_KEY_ID_FILE"), "file holding the offsite access key id (credential by reference; falls back to $AWS_ACCESS_KEY_ID)")
	cmd.Flags().StringVar(&f.secretFile, "offsite-secret-access-key-file", os.Getenv("OLIVARES_DR_OFFSITE_SECRET_ACCESS_KEY_FILE"), "file holding the offsite secret access key (credential by reference; falls back to $AWS_SECRET_ACCESS_KEY)")
	cmd.Flags().StringVar(&f.tokenFile, "offsite-session-token-file", os.Getenv("OLIVARES_DR_OFFSITE_SESSION_TOKEN_FILE"), "optional file holding an STS session token (falls back to $AWS_SESSION_TOKEN)")
}

// configured reports whether an offsite target was supplied (a bucket is the switch).
func (f offsiteFlags) configured() bool { return strings.TrimSpace(f.bucket) != "" }

// client resolves credentials by reference and builds an offsite client.
func (f offsiteFlags) client() (*dr.OffsiteClient, error) {
	akid, err := readSecretRef(f.akidFile, "AWS_ACCESS_KEY_ID")
	if err != nil {
		return nil, fmt.Errorf("offsite access key id: %w", err)
	}
	secret, err := readSecretRef(f.secretFile, "AWS_SECRET_ACCESS_KEY")
	if err != nil {
		return nil, fmt.Errorf("offsite secret access key: %w", err)
	}
	token, err := readSecretRef(f.tokenFile, "AWS_SESSION_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("offsite session token: %w", err)
	}
	return dr.NewOffsiteClient(dr.OffsiteConfig{
		Endpoint:        f.endpoint,
		Bucket:          f.bucket,
		Region:          f.region,
		Prefix:          f.prefix,
		PathStyle:       f.pathStyle,
		AccessKeyID:     akid,
		SecretAccessKey: secret,
		SessionToken:    token,
	})
}

// readSecretRef reads a credential from a file (trimmed) if set, otherwise from the
// named environment variable. It never accepts the value as an inline flag.
func readSecretRef(file, envName string) (string, error) {
	if strings.TrimSpace(file) != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(os.Getenv(envName)), nil
}

// gfsFlags configure a Grandfather-Father-Son retention policy.
type gfsFlags struct {
	daily    int
	weekly   int
	monthly  int
	yearly   int
	keepLast int
}

func addGFSFlags(cmd *cobra.Command, f *gfsFlags) {
	cmd.Flags().IntVar(&f.daily, "gfs-daily", 0, "GFS retention: keep the newest bundle of each of the last N days (0 = tier off)")
	cmd.Flags().IntVar(&f.weekly, "gfs-weekly", 0, "GFS retention: keep the newest bundle of each of the last N ISO weeks")
	cmd.Flags().IntVar(&f.monthly, "gfs-monthly", 0, "GFS retention: keep the newest bundle of each of the last N months")
	cmd.Flags().IntVar(&f.yearly, "gfs-yearly", 0, "GFS retention: keep the newest bundle of each of the last N years")
	cmd.Flags().IntVar(&f.keepLast, "gfs-keep-last", 0, "GFS retention: always keep the N newest bundles regardless of period")
}

func (f gfsFlags) policy() dr.GFSPolicy {
	return dr.GFSPolicy{Daily: f.daily, Weekly: f.weekly, Monthly: f.monthly, Yearly: f.yearly, KeepLast: f.keepLast}
}

func (f gfsFlags) any() bool { return !f.policy().IsZero() }

// localBundleMetas lists *.drbundle files in dir as retention candidates, using each
// file's mtime as its backup instant (cheap and monotone with write order; the
// manifest CreatedAt would require extracting every bundle).
func localBundleMetas(dir string) ([]dr.BundleMeta, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.drbundle"))
	if err != nil {
		return nil, err
	}
	out := make([]dr.BundleMeta, 0, len(matches))
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		out = append(out, dr.BundleMeta{Name: filepath.Base(m), CreatedAt: info.ModTime()})
	}
	return out, nil
}

// retentionOutcome is what ONE retention tier did during a backup, and it exists
// because a bare list of deletions cannot express the case an operator most needs
// to hear about.
//
// A tier reports itself in three states and an empty []string collapses two of
// them:
//
//	ran, deleted nothing   → Deleted empty, Skipped false
//	ran, deleted these     → Deleted non-empty, Skipped false
//	COULD NOT BE ATTEMPTED → Deleted empty, Skipped TRUE
//
// The third is a retention policy the caller asked for and that did not run — the
// local listing failed, or the offsite mirror could not be listed at all (a
// credential or network fault, which is the realistic one). The human pane says so
// in a `warning: … prune skipped` line; before this field the JSON document said
// exactly what it says for "there was nothing to delete", so a scheduled job
// checking that retention is keeping a volume from filling up read a clean answer
// off a tier that never ran.
//
// A per-ENTRY failure (`warning: could not prune X`) is deliberately NOT in here:
// Deleted is already the ground truth for it, because an entry that could not be
// removed is simply absent from the list — which is what happened.
//
// WHAT Skipped DOES NOT COVER, so nobody reads more out of it than it says. The
// LOCAL tier lists with filepath.Glob (localBundleMetas, pruneOldBundles), and Glob
// returns no matches and NO error for a directory that does not exist or cannot be
// read. A local tier that never saw its directory therefore answers "ran, deleted
// nothing" — Skipped is false — and only a malformed pattern reaches its guard.
// Widening it would mean listing with os.ReadDir, which changes what these commands
// PRINT on that path, and the printed form is a contract this lot does not touch.
// The offsite tier has no such hole: cli.List reports its failures.
type retentionOutcome struct {
	Deleted []string
	Skipped bool
}

// applyGFSLocal prunes dir under the GFS policy, never deleting keepName (the bundle
// just written). It is best-effort: a prune failure never fails the backup that
// produced a valid bundle.
//
// It RETURNS what it actually deleted (VER-06), so `dr backup -o json` can report
// what retention did instead of leaving it in prose only. The lines printed to out
// are unchanged, in the same order, for the same events — the return value is added
// information, not a replacement.
//
// THE RETURNED ENTRIES ARE FULL PATHS, and the printed line keeps the bare name.
// They are not the same shape on purpose: this tier's sibling `pruneOldBundles`
// deletes and prints full paths, so returning bare names here would make ONE json
// field (`retention.local`) carry a path under --retain-days and a basename under
// --gfs-*, from the same command, decided by a flag. A script reading
// `.retention.local[]` would then need to know which retention mode produced the
// document before it could use the value, which is the per-command parsing VER-06
// exists to remove. The path is also the more complete of the two — the basename is
// one filepath.Base away, the directory is not recoverable from a name.
func applyGFSLocal(dir, keepName string, policy dr.GFSPolicy, now time.Time, out io.Writer) retentionOutcome {
	metas, err := localBundleMetas(dir)
	if err != nil {
		fmt.Fprintf(out, "warning: local GFS prune skipped (%v)\n", err)
		return retentionOutcome{Skipped: true}
	}
	var pruned []string
	plan := dr.PlanGFS(metas, policy, now)
	for _, b := range plan.Delete {
		if b.Name == keepName {
			continue
		}
		if err := os.Remove(filepath.Join(dir, b.Name)); err != nil {
			fmt.Fprintf(out, "warning: could not prune %s (%v)\n", b.Name, err)
			continue
		}
		fmt.Fprintf(out, "GFS pruned local bundle: %s\n", b.Name)
		pruned = append(pruned, filepath.Join(dir, b.Name))
	}
	return retentionOutcome{Deleted: pruned}
}

// applyGFSOffsite prunes the offsite mirror under the same policy, returning what
// it deleted for the same reason applyGFSLocal does.
//
// These entries are OBJECT names, not paths, because that is what an object is on
// the mirror: `retention.offsite` is a different field from `retention.local` and
// its values are the keys `dr ls --offsite` lists and `dr pull --name` takes.
func applyGFSOffsite(ctx context.Context, cli *dr.OffsiteClient, keepName string, policy dr.GFSPolicy, now time.Time, out io.Writer) retentionOutcome {
	objs, err := cli.List(ctx)
	if err != nil {
		fmt.Fprintf(out, "warning: offsite GFS prune skipped (%v)\n", err)
		return retentionOutcome{Skipped: true}
	}
	metas := make([]dr.BundleMeta, 0, len(objs))
	for _, o := range objs {
		metas = append(metas, dr.BundleMeta{Name: o.Name, CreatedAt: o.LastModified})
	}
	var pruned []string
	plan := dr.PlanGFS(metas, policy, now)
	for _, b := range plan.Delete {
		if b.Name == keepName {
			continue
		}
		if err := cli.Delete(ctx, b.Name); err != nil {
			fmt.Fprintf(out, "warning: could not prune offsite %s (%v)\n", b.Name, err)
			continue
		}
		fmt.Fprintf(out, "GFS pruned offsite bundle: %s\n", b.Name)
		pruned = append(pruned, b.Name)
	}
	return retentionOutcome{Deleted: pruned}
}

// pushBundleOffsite streams a local bundle to the offsite target under its basename.
func pushBundleOffsite(ctx context.Context, cli *dr.OffsiteClient, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	return cli.Put(ctx, filepath.Base(localPath), f, info.Size())
}

// drPushCmd uploads an existing local bundle to the offsite target.
func drPushCmd() *cobra.Command {
	var of offsiteFlags
	var in string
	cmd := &cobra.Command{
		Use:     "push",
		Short:   "Upload an existing DR bundle to the offsite S3/R2 target",
		Long:    "push uploads an existing local DR bundle to the configured S3-compatible offsite bucket and prefix.",
		Example: "  olivares dr push --in ./backup.drbundle --offsite-bucket olivares-dr --offsite-region eu-west-1",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if in == "" {
				return fmt.Errorf("--in is required")
			}
			if !of.configured() {
				return fmt.Errorf("an offsite target is required: pass --offsite-bucket (and endpoint/creds)")
			}
			cli, err := of.client()
			if err != nil {
				return err
			}
			if err := pushBundleOffsite(cmd.Context(), cli, in); err != nil {
				return fmt.Errorf("offsite push: %w", err)
			}
			object := filepath.Base(in)
			return renderOut(cmd, func(out io.Writer) error {
				_, werr := fmt.Fprintf(out, "pushed offsite: %s → %s/%s\n", object, of.bucket, of.prefix)
				return werr
			}, drPushResult{In: in, Offsite: drOffsiteRef{Object: object, Bucket: of.bucket, Prefix: of.prefix}})
		},
	}
	addOffsiteFlags(cmd, &of)
	cmd.Flags().StringVar(&in, "in", "", "local DR bundle to upload (required)")
	_ = cmd.MarkFlagRequired("in")
	return cmd
}

// drPullCmd downloads a bundle from the offsite target for a restore.
func drPullCmd() *cobra.Command {
	var of offsiteFlags
	var name, out string
	cmd := &cobra.Command{
		Use:     "pull",
		Short:   "Download a DR bundle from the offsite S3/R2 target",
		Long:    "pull downloads one named DR bundle from the configured S3-compatible offsite bucket to a local 0600 file.",
		Example: "  olivares dr pull --name backup.drbundle --out ./backup.drbundle --offsite-bucket olivares-dr",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" || out == "" {
				return fmt.Errorf("--name and --out are required")
			}
			if !of.configured() {
				return fmt.Errorf("an offsite target is required: pass --offsite-bucket (and endpoint/creds)")
			}
			cli, err := of.client()
			if err != nil {
				return err
			}
			rc, err := cli.Get(cmd.Context(), name)
			if err != nil {
				return fmt.Errorf("offsite pull: %w", err)
			}
			defer func() { _ = rc.Close() }()
			f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			written, err := io.Copy(f, rc)
			if err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			return renderOut(cmd, func(w io.Writer) error {
				_, werr := fmt.Fprintf(w, "pulled offsite bundle %s → %s\n", name, out)
				return werr
			}, drPullResult{
				Offsite: drOffsiteRef{Object: name, Bucket: of.bucket, Prefix: of.prefix},
				Out:     out,
				Bytes:   written,
			})
		},
	}
	addOffsiteFlags(cmd, &of)
	cmd.Flags().StringVar(&name, "name", "", "offsite bundle name to download (required)")
	cmd.Flags().StringVar(&out, "out", "", "local path to write the bundle to (required)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

// drListCmd lists local bundles, or (with --offsite) the offsite mirror.
func drListCmd() *cobra.Command {
	var of offsiteFlags
	var dir string
	var offsite bool
	cmd := &cobra.Command{
		// Canonical short verb first (`ls`/`rm` across the CLI); the old
		// name stays as an alias so nothing breaks.
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List DR bundles (local, or --offsite for the S3/R2 mirror)",
		Long:    "list shows local DR bundles in a directory, or lists the configured S3-compatible mirror when --offsite is set.",
		Example: "  olivares dr ls --offsite --offsite-bucket olivares-dr",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// E2: one DTO, rendered through renderOut, so `-o json` works.
			// This listing printed its own lines whatever -o said — the whole `dr`
			// family did, which made scripting a restore a parsing exercise.
			items := []drBundleListItem{}
			source := ""
			if offsite {
				if !of.configured() {
					return fmt.Errorf("an offsite target is required: pass --offsite-bucket (and endpoint/creds)")
				}
				cli, err := of.client()
				if err != nil {
					return err
				}
				objs, err := cli.List(cmd.Context())
				if err != nil {
					return err
				}
				source = "offsite"
				for _, o := range objs {
					items = append(items, drBundleListItem{
						Name: o.Name, Bytes: o.Size, Modified: o.LastModified.UTC().Format(time.RFC3339),
					})
				}
			} else {
				if dir == "" {
					base, derr := resolveDataDir("")
					if derr != nil {
						return derr
					}
					dir = filepath.Join(base, "backups")
				}
				metas, err := localBundleMetas(dir)
				if err != nil {
					return err
				}
				source = dir
				for _, b := range metas {
					items = append(items, drBundleListItem{
						Name: b.Name, Created: b.CreatedAt.UTC().Format(time.RFC3339),
					})
				}
			}
			return renderOut(cmd, func(w io.Writer) error {
				if len(items) == 0 {
					if offsite {
						_, err := fmt.Fprintln(w, "(no offsite bundles)")
						return err
					}
					_, err := fmt.Fprintf(w, "(no bundles in %s)\n", source)
					return err
				}
				for _, it := range items {
					if offsite {
						if _, err := fmt.Fprintf(w, "%s\t%d bytes\t%s\n", it.Name, it.Bytes, it.Modified); err != nil {
							return err
						}
						continue
					}
					if _, err := fmt.Fprintf(w, "%s\t%s\n", it.Name, it.Created); err != nil {
						return err
					}
				}
				return nil
			}, items)
		},
	}
	addOffsiteFlags(cmd, &of)
	cmd.Flags().StringVar(&dir, "dir", "", "local backup directory (default <data-dir>/backups)")
	cmd.Flags().BoolVar(&offsite, "offsite", false, "list the offsite mirror instead of the local directory")
	return cmd
}
