// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// runtime_workspace_dir.go gives a session with NO registered workspace a
// directory of its OWN, under the engine's data directory, instead of the
// engine's working directory.
//
// ⛔ WHY IT EXISTS, MEASURED 2026-09-18. A governed session was launched the way
// the console's composer does — no workspace selected —
// and the child's own init frame reported `cwd` equal to the directory the engine
// process had been started from, byte for byte. That is the DEFAULT path, not a
// corner: `launchWorkspaceTarget` returned an empty LaunchSpec.Dir for a run with
// no workspace and the native runner fell back to the process cwd. So a session
// nobody configured read and wrote whatever the operator happened to be sitting
// in when they ran `olivares quickstart`.
//
// ⛔ AND WHAT IT IS NOT: CONFINEMENT. A native launch hands the child a working
// directory, and the child can walk out of it — `..` is not a jail, and this
// module says so in its own workspace contract (templateapply.go says it with all
// the letters about DLP). What this changes is WHERE a session starts and what it
// finds there: an empty, private, 0700 directory per run instead of the engine's
// own tree. Confinement is the isolation posture (container/sandbox) plus the
// tool surface the profile declares (runtime_session_policy.go), and neither is
// claimed here.
//
// The directory is created before the run row commits, recorded on the row so an
// operator can see it by name, and REMOVED when the operator releases the session
// (cleanupRun) — the one on-disk purge this plane can honestly perform, since it
// owns the location.
//
// ⛔ AND "SINCE IT OWNS THE LOCATION" IS A FACT ON THE ROW, NOT AN INFERENCE FROM
// THE PATH. The purge acts only where `workspace_dir_owned` says this module
// created the directory for this run; an operator's registered workspace, which
// may legitimately sit under this same root, is refused because the row never
// says "mine" about it. When ownership cannot be proven the answer is to keep
// the bytes.
//
// ⛔ AND CREATING IT IS NOT ENOUGH EITHER, MEASURED 2026-09-18. A run that named
// no workspace got `<root>/<run_ref>`; the operator then REGISTERED that very
// path as a workspace and worked in it — the console shows it as the run's
// working directory — and releasing the run removed it, files and all, because
// the row still (truthfully) said this module created it. So the rule has two
// halves and the purge checks both: a release removes a directory this module
// created for that run AND that nobody has registered since. The second half is
// a lookup in the tenant's workspace registry, canonicalised the way workspace.go
// canonicalises a root, and it is deny-closed: a registry that cannot be read
// keeps the bytes.
//
// ⛔ AND THE LOOKUP IS ABOUT THE WHOLE TREE, NOT ONE PATH, MEASURED THE SAME DAY.
// The removal is `os.RemoveAll`, so it reaches every descendant; asking only
// whether a workspace is rooted AT the directory left the ordinary case out —
// the operator made `project` inside the directory the run was given, registered
// THAT, and the release took it as a child. The question is "is any workspace of
// this tenant registered AT OR UNDER this directory", compared by path component
// and never by string prefix, and when the registry is too large to read to the
// end the answer is a refusal rather than an assumption.

// sessionWorkspaceDirMode is the permission of a per-session directory: private
// to the service account, like the data directory that contains it.
const sessionWorkspaceDirMode os.FileMode = 0o700

// errNoSessionWorkspaceRoot is the DENY-CLOSED answer when a run carries no
// workspace_ref and the composition root wired no place to put one. 503, like
// every other unwired dependency in this module, and it names the wiring rather
// than the symptom: falling back to the engine's own working directory is the
// defect this file removes, so it is not a fallback.
var errNoSessionWorkspaceRoot = &runErr{
	http.StatusServiceUnavailable,
	"session workspace root is not wired; a session without workspace_ref is deny-closed " +
		"(it would otherwise run in the engine's own working directory)",
}

// dirOwnerFunc answers whether a directory belongs to the user this engine runs
// as, and whether that can be known on this platform at all.
//
// It is reached through the runtime rather than called by name so that a control
// can put the OWNERSHIP condition out of the way and measure another condition on
// its own. That is not a convenience: on an engine running as uid 0 the
// ownership condition admits every root, and the filesystem-root refusal becomes
// the only guard for "/" — the configuration in which it matters most was the
// one nothing measured, because every control reached the ownership condition
// first. A field and not a package variable, so one module's substitution cannot
// race another test's.
type dirOwnerFunc func(os.FileInfo) (owned, known bool)

// dirOwnerCheck is the ownership condition this module uses: the platform's,
// unless a control has substituted one.
func (m *Module) dirOwnerCheck() dirOwnerFunc {
	if m.rt.dirOwner != nil {
		return m.rt.dirOwner
	}
	return dirOwnedByEngineUser
}

// sessionWorkspaceRootConfigured reports whether this node can give a session a
// directory of its own.
func (m *Module) sessionWorkspaceRootConfigured() bool {
	return strings.TrimSpace(m.rt.sessionWorkspaceRoot) != ""
}

// validateSessionWorkspaceRoot answers whether a directory may be used as this
// node's session workspace root — the place whose DIRECT CHILDREN a release is
// allowed to remove recursively. It is checked where the value is WIRED, because
// an unusable root is a configuration mistake and every launch and every release
// under it would otherwise inherit the problem one request at a time.
//
// ⛔ WHY "/" IS THE HEADLINE CASE, MEASURED 2026-09-18. The purge's predicate is
// "the parent of this directory is the root". With the root set to "/", `/etc`
// satisfies it, and `removeRunWorkspaceDir("/nonexistent-probe")` answered true.
// Nothing in the shipped composition root produces that value — it always
// appends `session-workspaces` to the data directory — but the option and its
// late-binding setter are EXPORTED, so the guard belongs on the value and not on
// the one caller that happens to be careful.
//
// The five conditions, and what each one is protecting:
//
//   - EMPTY: nothing is wired. Distinguished from a refusal so the two report
//     different sentences.
//   - RELATIVE: resolved against whatever working directory the engine happens to
//     have, which is the defect this whole file exists to remove.
//   - THE FILESYSTEM ROOT: every top-level directory of the host becomes a
//     candidate for recursive removal.
//   - A SYMBOLIC LINK: the purge compares path STRINGS (filepath.Dir(dir) ==
//     root). A linked last component makes those strings describe a tree the join
//     does not land in, so the comparison stops meaning what it is read to mean.
//     That is the LAST component; the ones above it are RESOLVED instead of
//     refused (`<base>/link-parent/sub` is an ordinary root), because a registered
//     workspace has canonicalised its root since and the two must agree.
//   - NOT OWNED BY THE ENGINE'S USER: a directory this service account does not
//     own is somebody else's, and a release is not entitled to empty it.
//
// A root that does not exist YET is accepted: prepareRunWorkspaceDir creates it,
// and nothing has been removed from a directory that is not there. The
// filesystem conditions are re-checked at purge time (removeRunWorkspaceDir),
// which is the moment they actually decide something.
//
// What comes back is the CANONICAL path (filepath.EvalSymlinks), not the string
// that was wired: every comparison this file makes is between canonical paths,
// so one directory reached by two spellings is one directory.
func validateSessionWorkspaceRoot(root string, owner dirOwnerFunc) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errNoSessionWorkspaceRoot
	}
	if !filepath.IsAbs(root) {
		return "", &runErr{http.StatusServiceUnavailable,
			"session workspace root " + root + " is not an absolute path; name an absolute " +
				"directory under the engine's data directory"}
	}
	clean := filepath.Clean(root)
	if filepath.Dir(clean) == clean {
		return "", &runErr{http.StatusServiceUnavailable,
			"session workspace root " + clean + " is the filesystem root; releasing a session " +
				"removes a direct child of this directory, so name a directory of the engine's " +
				"own instead (the data directory's session-workspaces)"}
	}
	info, err := os.Lstat(clean)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Created on first use, and nothing can have been removed from it yet. Its
		// EXISTING ancestors are resolved even so, so the path this node compares
		// against does not change the moment the directory appears.
		return canonicalRootOfAMissingDir(clean), nil
	case err != nil:
		return "", &runErr{http.StatusServiceUnavailable,
			"session workspace root " + clean + " could not be inspected on this node"}
	case info.Mode()&os.ModeSymlink != 0:
		return "", &runErr{http.StatusServiceUnavailable,
			"session workspace root " + clean + " is a symbolic link; name the directory it " +
				"resolves to, so the path a release compares against is the path it removes from"}
	case !info.IsDir():
		return "", &runErr{http.StatusServiceUnavailable,
			"session workspace root " + clean + " is not a directory"}
	}
	if owned, known := owner(info); known && !owned {
		return "", &runErr{http.StatusServiceUnavailable,
			"session workspace root " + clean + " is not owned by the user this engine runs as; " +
				"a release removes directories under it, so name one this service account owns"}
	}
	// The last component is not a link (refused above), so this resolves the
	// components ABOVE it. A root that cannot be resolved is refused: an
	// unresolvable path cannot be compared against anything.
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", &runErr{http.StatusServiceUnavailable,
			"session workspace root " + clean + " could not be resolved to a real path on this node; " +
				"name a directory whose path this service account can resolve"}
	}
	return real, nil
}

// canonicalRootOfAMissingDir resolves the existing ancestors of a root that has
// not been created yet, so a root under a symlinked parent is compared as the
// same path before and after its first use. A parent that cannot be resolved
// either leaves the cleaned string, which is what the purge would refuse on.
func canonicalRootOfAMissingDir(clean string) string {
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		return clean
	}
	return filepath.Join(parent, filepath.Base(clean))
}

// setSessionWorkspaceRoot is the ONE place the root is admitted, for both the
// Option and the late-binding setter, so the two cannot diverge. An empty value
// is ignored rather than refused: a composition root must not be able to erase a
// configured root by passing "".
func (m *Module) setSessionWorkspaceRoot(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	clean, err := validateSessionWorkspaceRoot(dir, m.dirOwnerCheck())
	if err != nil {
		m.rt.sessionWorkspaceRoot, m.rt.sessionWorkspaceRootRefusal = "", err
		return err
	}
	m.rt.sessionWorkspaceRoot, m.rt.sessionWorkspaceRootRefusal = clean, nil
	return nil
}

// sessionWorkspaceRootErr is the answer a launch gets when this node has no
// usable root: the REASON one was refused when a value was offered and rejected,
// and the generic unwired refusal otherwise.
func (m *Module) sessionWorkspaceRootErr() error {
	if m.rt.sessionWorkspaceRootRefusal != nil {
		return m.rt.sessionWorkspaceRootRefusal
	}
	return errNoSessionWorkspaceRoot
}

// isRunWorkspaceDirName reports whether a directory name is one this module
// MINTS for a run: a canonical UUID, which is what model.NewID produces.
//
// ⛔ IT IS THE PURGE-TIME HALF OF THE RETENTION GUARD, and it exists because
// "its parent is my root" admits every directory an operator ever put there. A
// name is not ownership — that is the row's job (colRunWorkspaceDirOwned) — but
// it is an independent condition that a directory named `etc`, `src` or
// `acme-project` cannot satisfy, and the two together mean a purge has to be
// wrong twice before it removes somebody else's bytes.
func isRunWorkspaceDirName(name string) bool {
	_, err := model.ParseID(name)
	return err == nil
}

// runWorkspaceDirPath is the DETERMINISTIC path of one run's own workspace
// directory: <root>/<run_ref>. Deterministic is what lets a resume prove it
// continues in the same place instead of being handed a new one.
//
// The join is VALIDATED, not assumed: a run reference is minted by this module
// and is a plain id today, but a reference that ever contained a separator would
// place a session's directory outside the root, and a path predicate is cheaper
// than trusting every future producer of a reference.
func (m *Module) runWorkspaceDirPath(runRef string) (string, error) {
	root := strings.TrimSpace(m.rt.sessionWorkspaceRoot)
	if root == "" {
		return "", m.sessionWorkspaceRootErr()
	}
	root = filepath.Clean(root)
	ref := strings.TrimSpace(runRef)
	if ref == "" || ref != filepath.Base(ref) || ref == "." || ref == ".." ||
		strings.ContainsAny(ref, `/\`) || !isRunWorkspaceDirName(ref) {
		return "", badRequest("run reference cannot name a session directory")
	}
	dir := filepath.Join(root, ref)
	if filepath.Dir(dir) != root {
		return "", badRequest("run reference cannot name a session directory")
	}
	return dir, nil
}

// prepareRunWorkspaceDir resolves and CREATES one run's own workspace directory.
// It is called before the run row is persisted, so a node that cannot make the
// directory refuses the launch with nothing durable behind it.
func (m *Module) prepareRunWorkspaceDir(runRef string) (string, error) {
	dir, err := m.runWorkspaceDirPath(runRef)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, sessionWorkspaceDirMode); err != nil {
		return "", &runErr{http.StatusServiceUnavailable,
			"the session's own workspace directory could not be created on this node"}
	}
	// A directory that already existed may carry another mode (an operator's
	// umask, a restore). Bring it to the private mode this plane promises rather
	// than reporting one it did not verify.
	if err := os.Chmod(dir, sessionWorkspaceDirMode); err != nil {
		return "", &runErr{http.StatusServiceUnavailable,
			"the session's own workspace directory could not be made private on this node"}
	}
	return dir, nil
}

// discardEmptyRunWorkspaceDir removes a directory prepared for a launch that
// never committed a row.
//
// os.Remove, NOT RemoveAll, and that is the whole point: it succeeds only on an
// EMPTY directory. If anything is inside it, a child wrote there and the
// directory stops being a leftover — keeping it is the conservative answer and
// the run row that names it is the thing that failed to exist, not the bytes.
func discardEmptyRunWorkspaceDir(dir string) {
	if strings.TrimSpace(dir) == "" {
		return
	}
	_ = os.Remove(dir)
}

// runWorkspaceDirPurge is WHAT a release did about the directory a run worked in.
// Three answers and not a boolean, because "true" was being reported for all of
// them: a missing root made `os.RemoveAll` return nil on a path that was not
// there, and a link planted where the directory had been was unlinked while its
// target survived — and the ledger said "its own workspace directory removed"
// for both. A release is evidence; it has to say which of the three happened.
type runWorkspaceDirPurge int

const (
	// purgeNoneOfOurs: the row does not claim a directory of this run's own — a
	// session that worked in a registered workspace, or one from before the
	// ownership column. There is nothing to purge and nothing to report.
	purgeNoneOfOurs runWorkspaceDirPurge = iota
	// purgeRemoved: the directory this module created was removed, with contents.
	purgeRemoved
	// purgeNothingThere: the path is not a directory of ours any more. Nothing was
	// removed except, at most, the link or file left in its place — which is
	// unlinked without ever being followed.
	purgeNothingThere
	// purgeKept: it may well be ours and it was NOT removed — the registry claims
	// the path, or a condition could not be proven at this moment. Deny-closed.
	purgeKept
)

// runWorkspaceDirKept says WHY a release kept bytes it did not remove. A bare
// "kept" is not evidence: an operator reading the ledger has to be able to tell
// "somebody's project lives there" from "this node could not finish asking",
// because only the first of the two is a steady state — the second is a node to
// go and look at.
type runWorkspaceDirKept int

const (
	// keptUnprovable: a condition of the row or of the path did not hold at this
	// moment — the root no longer validates, the parent does not resolve to it,
	// the entry could not be inspected, or the row names no absolute path.
	keptUnprovable runWorkspaceDirKept = iota
	// keptRegisteredAtThePath: a workspace of this tenant is registered at the
	// directory itself.
	keptRegisteredAtThePath
	// keptRegisteredInside: a workspace of this tenant is registered UNDER the
	// directory. Removing the directory is recursive, so it would take that
	// registration and everything in it.
	keptRegisteredInside
	// keptRegistryUnreadable: the registry could not be read, so nothing about it
	// is known.
	keptRegistryUnreadable
	// keptRegistryUnbounded: the registry was read to this release's bound and had
	// rows after it, so ABSENCE was never established. A walk that stops early
	// proves nothing, and treating it as proof is how the project of an operator
	// whose registration sits on the next page gets removed.
	keptRegistryUnbounded
)

// runWorkspaceDirRelease is one release's answer about that directory: what
// happened, why it was kept when it was, and — when a registration is what kept
// it — which workspace ref holds it, so the ledger can name it instead of going
// silent.
type runWorkspaceDirRelease struct {
	outcome runWorkspaceDirPurge
	kept    runWorkspaceDirKept
	heldBy  string
}

// workspaceRegistryClaim is the registry's answer about one directory: the
// workspace that holds those bytes, whether it is the directory itself or
// something inside it, and whether the search that looked for it ever reached
// the end of the registry.
type workspaceRegistryClaim struct {
	ref    string
	inside bool
	// unbounded: the search stopped at its bound with rows left. The claim says
	// NOTHING about whether a registration exists.
	unbounded bool
}

const (
	// defaultWorkspaceRegistryScanRows is how many of a tenant's registrations one
	// release reads before it stops and keeps the bytes. It is a bound and not a
	// limit on correctness: past it the answer is a refusal, never an assumption.
	defaultWorkspaceRegistryScanRows = 5000
	// workspaceRegistryScanPage is one page of that walk.
	workspaceRegistryScanPage = 500
)

// workspaceRegistryScanBound is how far this node walks a tenant's registry, with
// the default when nothing sensible is configured.
func (m *Module) workspaceRegistryScanBound() int {
	if m.rt.workspaceRegistryScanRows > 0 {
		return m.rt.workspaceRegistryScanRows
	}
	return defaultWorkspaceRegistryScanRows
}

// pathRelation reports whether root IS dir, or sits strictly INSIDE it.
//
// ⛔ IT COMPARES PATH COMPONENTS, NEVER RAW STRINGS, and that is the whole
// reason it is a function. `<root>/run-1` is not an ancestor of `<root>/run-10`,
// but `strings.HasPrefix` says it is — and an operator makes `<run_ref>-backup`
// in one keystroke of tab-completion. Both sides are cleaned first, so a
// trailing separator or a `.` component cannot change the answer.
func pathRelation(root, dir string) (at, under bool) {
	root = filepath.Clean(strings.TrimSpace(root))
	dir = filepath.Clean(strings.TrimSpace(dir))
	if root == "" || root == "." || dir == "" || dir == "." {
		return false, false
	}
	if root == dir {
		return true, false
	}
	return false, strings.HasPrefix(root, dir+string(os.PathSeparator))
}

// canonicalSpellingsOf is the set of paths one directory may be recorded under:
// the cleaned one, and its EvalSymlinks resolution when that differs and can be
// taken. Both are asked for, because "this path cannot be resolved right now" is
// not "this path is not registered". An empty or relative path has no spellings
// at all — a directory a release may remove is absolute or it is nothing.
func canonicalSpellingsOf(dir string) []string {
	clean := filepath.Clean(strings.TrimSpace(dir))
	if clean == "" || clean == "." || !filepath.IsAbs(clean) {
		return nil
	}
	out := []string{clean}
	if real, err := filepath.EvalSymlinks(clean); err == nil && real != clean {
		out = append(out, real)
	}
	return out
}

// registeredWorkspaceAtOrUnder reports which of the tenant's registered
// workspaces holds the bytes under dir — the one rooted AT it, or any one rooted
// INSIDE it — as a workspace ref ("" when none is). It is the second half of the
// retention rule, the half the row cannot answer: a row that truthfully says
// "this module created it" says nothing about who has depended on it since.
//
// ⛔ "AT" IS NOT THE QUESTION, AND ASKING ONLY THAT COST AN OPERATOR THEIR WORK.
// Measured 2026-09-18: a run with no workspace was given `<root>/<run_ref>`, the
// operator made `project` inside it and registered THAT, and releasing the run
// removed the registration with its parent — because the removal is
// `os.RemoveAll` and reaches every descendant, while the question reached one
// path. What a release must rule out is everything the removal would take.
//
// The comparison is on CANONICAL paths, the way workspace.go canonicalises a
// root: registration stores `filepath.EvalSymlinks(root_path)` (workspace.go),
// so the directory is resolved the same way before it is compared, and by
// COMPONENT (pathRelation), never as a string prefix.
//
// ⛔ AND THE DESCENDANT HALF IS A BOUNDED WALK RATHER THAN A PREDICATE, ON
// PURPOSE. This store's operators are equality, ordered comparison and LIKE
// (core/model/filter.go): an ordered range over a path column is only a prefix
// under a byte-ordering collation, which is a property of the deployment and not
// of this code, and LIKE reads `_` and `%` — both legal in a directory name — as
// wildcards. Neither can PROVE absence, and absence is the only answer that
// authorises a recursive removal. So the rows are walked and compared here, up
// to a stated bound, and past that bound the answer is "not proven".
//
// Every registration counts, including a disabled one: a disabled workspace is
// an operator's declared interest in those bytes, not permission to remove them.
func (m *Module) registeredWorkspaceAtOrUnder(ctx context.Context, tenant model.TenantID, dir string) (workspaceRegistryClaim, error) {
	if m == nil || m.data == nil {
		return workspaceRegistryClaim{}, errors.New("the workspace registry is not wired on this node")
	}
	dirs := canonicalSpellingsOf(dir)
	if len(dirs) == 0 {
		return workspaceRegistryClaim{}, errors.New("the directory to check is not an absolute path")
	}
	bound := m.workspaceRegistryScanBound()
	var claim workspaceRegistryClaim
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(workspaceKind)
		if err != nil {
			return err
		}
		// AT the directory first: one bounded equality per spelling. It answers at
		// any registry size, so the registration an operator is most likely to have
		// made never depends on the walk below reaching the end.
		for _, path := range dirs {
			recs, _, err := repo.List(ctx, model.Query{
				Filters: []model.Filter{eq(colWsRootPath, path)}, Limit: 1,
			})
			if err != nil {
				return err
			}
			if len(recs) > 0 {
				claim = workspaceRegistryClaim{ref: recs[0].String(colWsRef)}
				return nil
			}
		}
		// INSIDE it: walked by page, compared by component, bounded.
		scanned, cursor := 0, ""
		for {
			page := workspaceRegistryScanPage
			if rem := bound - scanned; rem < page {
				page = rem
			}
			recs, pg, err := repo.List(ctx, model.Query{Limit: page, Cursor: cursor})
			if err != nil {
				return err
			}
			for _, rec := range recs {
				root := rec.String(colWsRootPath)
				for _, d := range dirs {
					if at, under := pathRelation(root, d); at || under {
						claim = workspaceRegistryClaim{ref: rec.String(colWsRef), inside: under}
						return nil
					}
				}
			}
			scanned += len(recs)
			cursor = pg.Cursor
			if !pg.HasMore {
				// The registry ended: absence is established, and only here.
				return nil
			}
			if cursor == "" || scanned >= bound {
				claim = workspaceRegistryClaim{unbounded: true}
				return nil
			}
		}
	})
	if err != nil {
		return workspaceRegistryClaim{}, err
	}
	return claim, nil
}

// removeOwnRunWorkspaceDir is the ONE seam the release purge goes through, and
// it is DENY-CLOSED: it removes a run's directory only when the row PROVES this
// module created that directory for that run AND no registration of this tenant
// holds anything the removal would reach. It answers whether it removed
// anything, why it did not when it did not, and — when a registration is what
// kept it — which workspace ref holds it, so the ledger can say so by name
// instead of going silent.
//
// ⛔ THE PATH CANNOT ANSWER THE FIRST QUESTION, which is why the proof is a
// column. `workspace_path` carries either a directory this plane created or the
// canonical root of a REGISTERED workspace — the operator's own project — and an
// operator may register one that sits directly under this node's session root.
// Every string predicate available to the purge then reports "its parent is my
// root" about a directory this plane never created. Measured 2026-09-18: with a
// workspace registered at `<root>/acme-project`, releasing a session that named
// it removed the operator's project and the files in it.
//
// ⛔ AND THE COLUMN CANNOT ANSWER THE SECOND, which is why the registry is read
// here: the row is true and the bytes are still somebody else's.
//
// The positional predicate below is KEPT, as another independent condition
// rather than as the answer: a row that says "mine" about a path outside the
// root describes a state this module cannot produce, and a purge that acted on
// it would be trusting a row against the filesystem.
func (m *Module) removeOwnRunWorkspaceDir(ctx context.Context, tenant model.TenantID, rec model.Record) runWorkspaceDirRelease {
	if !rec.Bool(colRunWorkspaceDirOwned) {
		return runWorkspaceDirRelease{outcome: purgeNoneOfOurs}
	}
	dir := rec.String(colRunWorkspacePath)
	if len(canonicalSpellingsOf(dir)) == 0 {
		// A row that claims a directory of this module's own has to name an absolute
		// path. One that does not describes a state this module cannot produce, and
		// nothing is removed on the strength of it.
		return runWorkspaceDirRelease{outcome: purgeKept, kept: keptUnprovable}
	}
	claim, err := m.registeredWorkspaceAtOrUnder(ctx, tenant, dir)
	if err != nil {
		// Deny-closed: a registry this node cannot read cannot clear a directory
		// for removal, so the bytes stay and the operator is told why.
		m.warnf("sessions: the workspace registry could not be read on release, so the session's own workspace directory was kept",
			"err", redactErr(err))
		return runWorkspaceDirRelease{outcome: purgeKept, kept: keptRegistryUnreadable}
	}
	if claim.unbounded {
		// Read to the bound with rows left: the walk proved nothing, and a removal
		// on an unproven absence is the defect, not the refusal.
		m.warnf("sessions: this tenant's workspace registry is larger than one release reads, so the session's own workspace directory was kept",
			"rows_read", m.workspaceRegistryScanBound())
		return runWorkspaceDirRelease{outcome: purgeKept, kept: keptRegistryUnbounded}
	}
	if claim.ref != "" {
		held := keptRegisteredAtThePath
		if claim.inside {
			held = keptRegisteredInside
		}
		return runWorkspaceDirRelease{outcome: purgeKept, kept: held, heldBy: claim.ref}
	}
	return runWorkspaceDirRelease{outcome: m.removeRunWorkspaceDir(dir)}
}

// removeRunWorkspaceDir deletes one session's own workspace directory and its
// contents, and reports which of the three things happened. It is the PATH half
// of the rule and is never the whole of it: callers reach it through
// removeOwnRunWorkspaceDir, which establishes ownership from the row and asks
// the registry first. On its own it can only say "this path is shaped like one
// of mine" — which it says with three conditions rather than one, because
// "parent equals root" admits every directory an operator ever put under the
// root: the root must still pass validateSessionWorkspaceRoot at THIS moment,
// the canonical parent must be that root, and the child's name must be one this
// module mints.
//
// ⛔ AND WHAT IT FINDS THERE IS INSPECTED BEFORE IT IS REMOVED. A directory is
// removed with its contents; anything else at that path — a link swapped in
// between the check and the use, a file — is unlinked WITHOUT being followed
// (os.Remove never follows the last component), and the answer is not "a
// directory was removed", because none was. Measured 2026-09-18: a link planted
// where a session's directory had been was unlinked, its target survived, and
// the release reported the same "removed" as a real purge.
func (m *Module) removeRunWorkspaceDir(dir string) runWorkspaceDirPurge {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return purgeKept
	}
	// Re-validated HERE, not trusted from wiring time: this is the moment the root
	// decides that a recursive removal happens, and between the two moments a
	// directory can be replaced by a link or change owner.
	root, err := validateSessionWorkspaceRoot(m.rt.sessionWorkspaceRoot, m.dirOwnerCheck())
	if err != nil {
		return purgeKept
	}
	dir = filepath.Clean(dir)
	if dir == root {
		return purgeKept
	}
	// CANONICAL on both sides. The root came back resolved from the validation
	// above; resolving the directory's parent here is what makes a row written
	// through another spelling of the same tree — a symlinked parent, a root
	// canonicalised after the row was written — the same directory rather than
	// somebody else's. A parent that cannot be resolved is refused: this is the
	// moment a recursive removal happens, and an unprovable path is not a proof.
	parent, err := filepath.EvalSymlinks(filepath.Dir(dir))
	if err != nil || parent != root {
		return purgeKept
	}
	if !isRunWorkspaceDirName(filepath.Base(dir)) {
		return purgeKept
	}
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Already gone: an operator's own cleanup, a restore, a root that is no
		// longer there. Nothing was removed, and the release says so.
		return purgeNothingThere
	case err != nil:
		m.warnf("sessions: the session's own workspace directory could not be inspected on release",
			"err", redactErr(err))
		return purgeKept
	case !info.IsDir():
		// Not the directory this module created: os.Remove takes the entry itself
		// and never the tree a link points at.
		if err := os.Remove(dir); err != nil {
			m.warnf("sessions: what stood where the session's own workspace directory had been could not be removed on release",
				"err", redactErr(err))
			return purgeKept
		}
		return purgeNothingThere
	}
	if err := os.RemoveAll(dir); err != nil {
		m.warnf("sessions: the session's own workspace directory could not be removed on release",
			"err", redactErr(err))
		return purgeKept
	}
	return purgeRemoved
}
