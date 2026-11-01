// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/model"
)

// A SESSION'S MANAGED CREDENTIALS (Business).
//
// A launch may name up to CredentialMaxNames managed credentials in credentials: an SSH
// key or certificate, a database login, an API account. The Business edition's broker
// holds the secret in the engine and gives the child a way to USE it, never the secret:
// an SSH agent socket, a database proxy socket, a local reverse proxy. What the child
// receives is the location of those helpers (a closed set of variables, validated here)
// and the files it may read, nothing else. The broker revokes everything when the run ends.
//
// Community wires no broker: a launch that names a credential answers 501
// business_required and starts nothing. Who may: tenant administration, like secret_env
// and git_read, asked again on resume.
//
// ponytail: the helpers live in the engine process. A session that holds a credential is
// launched with ConfinementRequired, so the engine's memory is out of its reach (Landlock)
// and it does not start on a host that cannot confine it. Landlock does not mediate
// connecting to a unix socket, and the kernel shows every socket path and loopback port to
// every process of the network namespace, so the helpers cannot be hidden from another run's
// session: each one serves only the run's own processes (the child and what it started),
// which the broker learns through Bind once the child has started. Upgrade trigger: a helper
// per operating-system user, or the sockets in a namespace of their own.

// SessionCredentialBroker is the Business edition's hook into a launch. The composition
// root binds it; nil means this edition has no managed credentials. The context bounds
// only the call: the helpers it starts live until Release, not until the context ends.
// A broker's helpers serve only the run's own processes: a path, a port or a name is no
// protection, since the kernel shows them to the whole network namespace, so each helper checks
// who connects against the process the launch reports through Bind.
type SessionCredentialBroker interface {
	BrokerSessionCredentials(ctx context.Context, req CredentialBrokerRequest) (BrokeredCredentials, error)
}

// CredentialBrokerRequest is everything the broker is told about a launch: the run, the
// credential names the launch asked for, and whether the child's network reaches only its
// provider (a helper that needs the child to dial a host cannot work there). No
// environment value, token or argv reaches it.
type CredentialBrokerRequest struct {
	Tenant   model.TenantID
	RunRef   string
	Names    []string
	Confined bool
}

// BrokeredCredentials is what a launch gets from the broker, and none of it is a secret.
type BrokeredCredentials struct {
	// Env locates the helpers. Only the names credentialEnvName allows are accepted.
	Env []EnvVar
	// Read are the files (a generated ssh configuration, a known_hosts) a confined child
	// may read. Sockets need no grant here; Landlock does not mediate connecting to one.
	Read []string
	// Withhold are secret values the output redactor strips from what the plane stores and
	// streams, in case a remote service echoes one back. The values are held for this
	// launch only and never persisted.
	Withhold []string
	// Detail is the ledger's line: which credentials, of which kind, never a value.
	Detail string
	// Bind is called once with the process id of the child, as soon as the runner has
	// started it (LaunchSpec.OnStart). Until it is called the helpers serve nobody; after it
	// they serve that process and its descendants and no other process. A success with no
	// Bind is refused: its helpers could not tell the run's processes from any other.
	Bind func(pid int)
	// Release runs when the run ends or the launch fails: it closes the helpers and revokes
	// what the broker issued. It must be safe to call once and must not block for long. The core
	// cannot see a failed revoke: the broker logs its own failures.
	Release func()
}

// CredentialRefusal is a launch the broker declines with a reason the caller can act on
// (an unknown name, a kind the child's network cannot use). The reason names no secret.
type CredentialRefusal struct{ Reason string }

func (e *CredentialRefusal) Error() string { return e.Reason }

// CredentialMaxNames is how many managed credentials one launch may name. The
// CLI states the same bound to a person (cmd/olivares), so it is the one number.
const CredentialMaxNames = 8

const (
	// credentialBrokerTimeout bounds one broker call; it dials no host for the child.
	credentialBrokerTimeout = 30 * time.Second
	// credentialRedactName keys the brokered values in the output redactor; no variable has it.
	credentialRedactName = "\x00credential"
	credentialMark       = "credential"
)

// engineCredentialMinterConfig is an input of the engine (the Enterprise upstream credential
// minter) that shares the broker's prefix; a broker never sets it on a child.
const engineCredentialMinterConfig = "OLIVARES_CREDENTIAL_MINTER_CONFIG"

var credentialNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// credentialEnvName is the closed set of variables a broker may set on the child: where
// ssh finds its agent, where psql finds its service file, and the broker's own
// OLIVARES_CREDENTIAL_ locators. The provider families, the homes, the loader and exec
// names and the engine's secrets stay out of its reach (reservedSecretEnvName).
func credentialEnvName(name string) bool {
	return (name == "SSH_AUTH_SOCK" || name == "PGSERVICEFILE" ||
		(strings.HasPrefix(name, "OLIVARES_CREDENTIAL_") && name != engineCredentialMinterConfig)) &&
		validEnvName(name)
}

// validateCredentialNames checks the names of a launch's credentials.
func validateCredentialNames(names []string) error {
	if len(names) > CredentialMaxNames {
		return badRequest(fmt.Sprintf("credentials names more than %d credentials", CredentialMaxNames))
	}
	for i, name := range names {
		if !credentialNamePattern.MatchString(name) || slices.Contains(names[:i], name) {
			return badRequest("credentials has an invalid or duplicate name (lowercase letters, digits, '.', '_' and '-', at most 64)")
		}
	}
	return nil
}

// refuseCredentialsFor checks a launch's credentials before anything durable: the shape,
// then the edition, then who may.
func (m *Module) refuseCredentialsFor(p CreateRunParams) error {
	if len(p.Credentials) == 0 {
		return nil
	}
	if err := validateCredentialNames(p.Credentials); err != nil {
		return err
	}
	if m.rt.CredentialBroker == nil {
		return &codedRunErr{&runErr{http.StatusNotImplemented, "Giving a session a managed credential requires Olivares Business."}, "business_required"}
	}
	if !p.MayUseSecretEnv {
		return forbiddenErr("giving a session a managed credential needs tenant administration")
	}
	return nil
}

// configureCredentials asks the broker for this launch's helpers, adds the locations to
// the child's environment and ties their release to the run. A refusal or an outage
// refuses the launch: a session that asked for a credential never starts without it.
func (m *Module) configureCredentials(ctx, runCtx context.Context, tenant model.TenantID, runRef string, p *CreateRunParams, spec *LaunchSpec) error {
	if len(p.Credentials) == 0 {
		return nil
	}
	if err := m.refuseCredentialsFor(*p); err != nil {
		return err
	}
	// A container (or sandbox) child has its own filesystem: the helpers' sockets and files are
	// not in it, so the session would start without the access it asked for.
	if spec.Isolation != "" && spec.Isolation != IsolationNative {
		return &runErr{http.StatusUnprocessableEntity,
			"credentials: a session in a container cannot reach the engine's helpers; start it with native isolation"}
	}
	// The secrets are in the engine's memory and the helpers' sockets are files of the engine's
	// user: only confinement keeps the child out of both. Like a read-only session, a session
	// that holds a credential does not start where this host cannot confine it.
	spec.ConfinementRequired = true
	outage := func(cause error) error {
		m.warnf("sessions: the managed credentials could not be brokered", "run_ref", runRef, "names", p.Credentials, "err", redactErr(cause))
		return &runErr{http.StatusServiceUnavailable, "the managed credentials could not be prepared; the launch is denied"}
	}
	// A result that breaks the contract is declined (422), not an outage: a queued resume retries a
	// 503 on every poll, and the same bad result would come back each time. The log names the class
	// of the breach, never the value the broker named.
	breach := func(cause error) error {
		m.warnf("sessions: the broker's result breaks its contract", "run_ref", runRef, "names", p.Credentials, "cause", redactErr(cause))
		return &runErr{http.StatusUnprocessableEntity, "the managed credentials could not be prepared; the launch is denied"}
	}
	bctx, cancel := context.WithTimeout(ctx, credentialBrokerTimeout)
	got, err := m.rt.CredentialBroker.BrokerSessionCredentials(bctx, CredentialBrokerRequest{
		Tenant: tenant, RunRef: runRef, Names: slices.Clone(p.Credentials), Confined: spec.NetworkPolicy != nil,
	})
	cancel()
	release := func() {
		if got.Release == nil {
			return
		}
		// A panic in a broker's release must not end the engine or leave the launch unanswered.
		// The log names its class, never the value the broker put in it.
		defer func() {
			if r := recover(); r != nil && m.log != nil {
				m.log.Error("sessions: a credential broker's release panicked; its helpers may not be revoked", "run_ref", runRef, "panic", fmt.Sprintf("%T", r))
			}
		}()
		got.Release()
	}
	// On an error the broker has already undone what it started, so a release comes back
	// empty; one that does come back is run all the same.
	if err != nil {
		release()
	}
	var refusal *CredentialRefusal
	// A refusal that is a nil pointer is a contract break with no reason: not an outage to
	// retry (the same bad result would come back), and never a value for the redaction to read.
	if errors.As(err, &refusal) && refusal == nil {
		return breach(errRefusalUnnamed)
	}
	addon, operation, unlicensed := license.AddonRefusal(err)
	switch {
	case unlicensed:
		// A Business broker whose licence does not cover managed credentials: the add-on
		// refusal, not an outage a queued resume would retry.
		return &codedRunErr{&runErr{http.StatusForbidden, api.AddonRefusalMessage(addon, operation)}, "addon_requires_license"}
	case errors.As(err, &refusal):
		reason := redactErr(refusal)
		m.warnf("sessions: the managed credentials were refused", "run_ref", runRef, "names", p.Credentials, "reason", reason)
		return &runErr{http.StatusUnprocessableEntity, "credentials: " + reason}
	case err != nil:
		return outage(err)
	}
	// A success that names no variable leaves the child nothing to find a helper with.
	if len(got.Env) == 0 {
		release()
		return breach(errors.New("the broker returned no helper location"))
	}
	// Helpers the launch cannot revoke would outlive the run: refuse them rather than start with them.
	if got.Release == nil {
		return breach(errors.New("the broker returned helpers it cannot release"))
	}
	// Helpers that cannot be bound to the run would serve any process that finds them.
	if got.Bind == nil {
		release()
		return breach(errors.New("the broker returned no way to bind its helpers to the run's processes"))
	}
	var named []string
	for _, e := range got.Env {
		// One variable, once: from the closed set, and neither the launch's own nor another of the broker's.
		if !credentialEnvName(e.Name) || !envValueFits(e.Value) || slices.Contains(named, e.Name) ||
			slices.ContainsFunc(spec.Env, func(have EnvVar) bool { return have.Name == e.Name }) {
			release()
			return breach(errors.New("the broker returned a variable it may not set"))
		}
		named = append(named, e.Name)
	}
	// Each file a confined child may read is checked on its own: one bad element refuses the list.
	// The child is granted the file that was checked, where it really is.
	var granted []string
	for _, path := range got.Read {
		real, err := m.credentialReadPath(path, runRef)
		if err != nil {
			release()
			if errors.Is(err, errReadUnchecked) {
				return outage(err)
			}
			return breach(err)
		}
		granted = append(granted, real)
	}
	context.AfterFunc(runCtx, release)
	spec.OnStart = got.Bind
	spec.Env = append(spec.Env, got.Env...)
	for _, path := range granted {
		spec.AllowRead(path)
	}
	p.credentialWithhold = withholdableLines(got.Withhold)
	p.credentialDetail = got.Detail
	return nil
}

// credentialReadPath is the file a confined child may be granted for the broker's path, where it
// really is, or why it may not be granted. A granted file is a regular file of its own: a
// directory would grant its whole tree, a link would grant what it points at, and a hard link
// would grant the file it is a second name for; the last name is the file itself, and folders on
// the way are followed to where the file really is. Inside a protected folder only this run's own
// directory in the engine data directory is allowed. The error names the rule, never the path.
func (m *Module) credentialReadPath(path, runRef string) (string, error) {
	if strings.ContainsRune(path, 0) || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return "", errors.New("a file to read that is not an absolute, clean path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", credentialStatErr(err)
	}
	if !info.Mode().IsRegular() || !singleLink(info) {
		return "", errors.New("a file to read that is not a regular file of its own")
	}
	// The file is judged where it really is: the engine never resolves its data directory, so the
	// broker's own path may run through a link, and the child is granted the resolved one.
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errReadUnchecked
	}
	if m.rt.credentialDataDir != "" && inBrokerRunDir(real, m.rt.credentialDataDir, runRef) {
		return real, nil
	}
	for _, protected := range m.rt.confineProtect {
		if pathsOverlap(real, protected) {
			return "", errors.New("a file to read inside a protected folder")
		}
	}
	return real, nil
}

// errReadUnchecked is a file the core could not check for a reason of the system, not of the
// broker's result: an outage, retried like one. Its text names no path.
var errReadUnchecked = errors.New("a file to read could not be checked")

// errRefusalUnnamed replaces a refusal that is a nil pointer: no reason to act on, no value for
// the redaction to read. It is the broker's own contract break, declined like one.
var errRefusalUnnamed = errors.New("a credential refusal with no reason")

// brokerPathErr is a path the broker named that cannot be resolved by its own fault: it does not
// exist, runs through a file, loops through links, is too long, or is one the engine's own user
// has no permission to reach. That is a breach, not an outage.
func brokerPathErr(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENAMETOOLONG) ||
		errors.Is(err, fs.ErrPermission)
}

// credentialStatErr classifies a failed stat of a path the broker named, with no filesystem, so a
// table can pin the class under any user. A fault of the storage is an outage; everything else is
// the broker's own fault, and a stat the engine's own user cannot do is a class of its own: the
// file may be a regular file of its own, and the log must not say otherwise.
func credentialStatErr(err error) error {
	if !brokerPathErr(err) {
		return errReadUnchecked
	}
	if errors.Is(err, fs.ErrPermission) {
		return errors.New("a file to read the engine's own user cannot stat")
	}
	return errors.New("a file to read that is not a regular file of its own")
}

// inBrokerRunDir reports whether path, already resolved, is inside the run directory the broker
// makes under the data directory, DataDir/run/<run ref>: the layout every per-run directory of the
// engine shares. A run ref that is not one path segment could name another directory, so it grants
// nothing.
func inBrokerRunDir(path, dataDir, runRef string) bool {
	if runRef == "" || runRef == "." || runRef == ".." || strings.ContainsAny(runRef, `/\`) {
		return false
	}
	dir := filepath.Join(resolvedPath(dataDir), "run", runRef) + string(filepath.Separator)
	return strings.HasPrefix(path, dir)
}

// withholdableLines are the strings the output redactor strips for a broker's values: each
// value, or each line of a multi-line one (a private key), that is at least minSecretValue
// long. The output is framed line by line, and a shorter string would blank ordinary words
// (the short last line of a PEM key). The helper still keeps those out of the child's reach.
func withholdableLines(values []string) []string {
	var out []string
	for _, value := range values {
		lines := []string{value}
		if strings.ContainsAny(value, "\r\n") {
			lines = secretLines(value)
		}
		for _, line := range lines {
			if len(line) >= minSecretValue && !slices.Contains(out, line) {
				out = append(out, line)
			}
		}
	}
	return out
}

// encodeCredentialNames is the run row's value: the names, as JSON, empty for none.
func encodeCredentialNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	raw, _ := json.Marshal(names)
	return string(raw)
}

// decodeCredentialNames reads a run row's names; an unreadable value is an error so a
// resume never relaunches without a credential the run was given. A value that is there
// but names nothing ("[]", "null") is as unreadable as one that is not JSON: a launch
// without credentials stores nothing at all (encodeCredentialNames).
func decodeCredentialNames(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil, unreadableNames("not a JSON list")
	}
	if len(names) == 0 {
		return nil, unreadableNames("an empty list")
	}
	return names, nil
}

// unreadableCredentialNames is the 409 for a stored credential list that cannot be read. The
// caller's message stays fixed; cause is the class of the damage, for the log.
type unreadableCredentialNames struct {
	*runErr
	cause string
}

func (e *unreadableCredentialNames) Unwrap() error { return e.runErr }

// unreadableNames is a conflict, not a bare error: a queued resume retries what it cannot classify,
// and a stored value that is damaged stays damaged.
func unreadableNames(cause string) error {
	return &unreadableCredentialNames{runErr: conflictErr("this session's stored credentials cannot be read; start a new session"), cause: cause}
}

// warnUnreadableNames logs the class of a stored credential list that cannot be read, with the run
// it belongs to.
func (m *Module) warnUnreadableNames(runRef string, err error) {
	var unreadable *unreadableCredentialNames
	if errors.As(err, &unreadable) {
		m.warnf("sessions: a run's stored credential list cannot be read", "run_ref", runRef, "cause", unreadable.cause)
	}
}

// storedCredentialNames reads a run row's names for the API; an unreadable value reads as none
// rather than failing a list, and the log says why.
func (m *Module) storedCredentialNames(rec model.Record) []string {
	names, err := decodeCredentialNames(rec.String(colRunCredentials))
	m.warnUnreadableNames(rec.String(colRunRef), err)
	return names
}
