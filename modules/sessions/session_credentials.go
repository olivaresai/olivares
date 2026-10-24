// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// A SESSION'S MANAGED CREDENTIALS (Business).
//
// A launch may name up to credentialMaxNames managed credentials in credentials: an SSH
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
	// what the broker issued. It must be safe to call once and must not block for long.
	Release func()
}

// CredentialRefusal is a launch the broker declines with a reason the caller can act on
// (an unknown name, a kind the child's network cannot use). The reason names no secret.
type CredentialRefusal struct{ Reason string }

func (e *CredentialRefusal) Error() string { return e.Reason }

const (
	credentialMaxNames = 8
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
	if len(names) > credentialMaxNames {
		return badRequest(fmt.Sprintf("credentials names more than %d credentials", credentialMaxNames))
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
	unavailable := func(cause error) error {
		m.warnf("sessions: the managed credentials could not be brokered", "run_ref", runRef, "names", p.Credentials, "err", redactErr(cause))
		return &runErr{http.StatusServiceUnavailable, "the managed credentials could not be prepared; the launch is denied"}
	}
	bctx, cancel := context.WithTimeout(ctx, credentialBrokerTimeout)
	got, err := m.rt.CredentialBroker.BrokerSessionCredentials(bctx, CredentialBrokerRequest{
		Tenant: tenant, RunRef: runRef, Names: slices.Clone(p.Credentials), Confined: spec.NetworkPolicy != nil,
	})
	cancel()
	release := func() {
		if got.Release != nil {
			got.Release()
		}
	}
	// On an error the broker has already undone what it started, so a release comes back
	// empty; one that does come back is run all the same.
	if err != nil {
		release()
	}
	var refusal *CredentialRefusal
	switch {
	case errors.As(err, &refusal):
		m.warnf("sessions: the managed credentials were refused", "run_ref", runRef, "names", p.Credentials, "reason", refusal.Reason)
		return &runErr{http.StatusUnprocessableEntity, "credentials: " + refusal.Reason}
	case err != nil:
		return unavailable(err)
	}
	// A success that names no variable leaves the child nothing to find a helper with.
	if len(got.Env) == 0 {
		release()
		return unavailable(errors.New("the broker returned no helper location"))
	}
	// Helpers that cannot be bound to the run would serve any process that finds them.
	if got.Bind == nil {
		release()
		return unavailable(errors.New("the broker returned no way to bind its helpers to the run's processes"))
	}
	var named []string
	for _, e := range got.Env {
		// One variable, once: from the closed set, and neither the launch's own nor another of the broker's.
		if !credentialEnvName(e.Name) || !envValueFits(e.Value) || slices.Contains(named, e.Name) ||
			slices.ContainsFunc(spec.Env, func(have EnvVar) bool { return have.Name == e.Name }) {
			release()
			return unavailable(fmt.Errorf("the broker returned a variable it may not set: %q", e.Name))
		}
		named = append(named, e.Name)
	}
	// The files a confined child may read are the broker's own: an absolute, clean path, never
	// the root or one that climbs out of where it starts.
	for _, path := range got.Read {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			release()
			return unavailable(fmt.Errorf("the broker offered a file to read that is not an absolute, clean path: %q", path))
		}
	}
	context.AfterFunc(runCtx, release)
	spec.OnStart = got.Bind
	spec.Env = append(spec.Env, got.Env...)
	for _, path := range got.Read {
		spec.AllowRead(path)
	}
	p.credentialWithhold = withholdableLines(got.Withhold)
	p.credentialDetail = got.Detail
	return nil
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
	if err := json.Unmarshal([]byte(raw), &names); err != nil || len(names) == 0 {
		// A conflict, not a bare error: a queued resume retries what it cannot classify, and a
		// stored value that is damaged stays damaged.
		return nil, conflictErr("this session's stored credentials cannot be read; start a new session")
	}
	return names, nil
}

// storedCredentialNames reads a run row's names for the API; an unreadable value reads
// as none rather than failing a list.
func storedCredentialNames(rec model.Record) []string {
	names, _ := decodeCredentialNames(rec.String(colRunCredentials))
	return names
}
