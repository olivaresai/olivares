# Managed credentials for sessions

Business. A session can use an SSH key, a database login or an API account without ever
holding the secret. The engine keeps the secret and runs a helper for the length of the
session; the session is told only where the helper is.

A Community engine answers `501 business_required` to a launch that names a credential.

## Store a credential

A credential is one JSON document sealed as `credential/<name>` in the tenant's secrets
(the console's Secrets, or this API). Names use lowercase letters, digits, `.`, `_` and `-`.
Writing it needs tenant administration and step-up:

```sh
curl -X PUT "$OLIVARES_URL/v1/console/secrets?scope=tenant" -H "Authorization: Bearer $TOKEN" \
  -H "X-Olivares-Tenant: $TENANT" -H 'Content-Type: application/json' \
  -d '{"name":"credential/prod-db","value":"{\"kind\":\"postgres\",\"host\":\"db.example.com\",\"database\":\"app\",\"user\":\"app_ro\",\"password\":\"...\"}"}'
```

The value is one of:

| kind | fields |
| --- | --- |
| `ssh` | `host`, `port` (22), `user`, `known_hosts` (required: the host key lines in OpenSSH format, `[host]:port` for a port other than 22; the host keys are the only ones a login is bound to, a `@revoked` key is refused and a `@cert-authority` line is ignored), and one of `private_key` (an unprotected OpenSSH or PEM key) or `ca_private_key` with `ttl_seconds` (30 to 3600, default 300) |
| `postgres` | `host`, `port` (5432), `database`, `user`, `password`, `sslmode` (`verify-full` by default, or `verify-ca`, `require`, `disable`) and, for a server behind a private CA, `root_cert` (PEM) |
| `http` | `base_url` (`https`, or any scheme for a loopback address), `header` (default `Authorization`) and `value` |

Unknown fields are refused. `env/` secrets cannot name a credential, so the only way a session
reaches one is through its helper.

## Give it to a session

Name the credentials when the session starts (at most 8). The caller needs tenant
administration, as for `secret_env`; resuming a session asks again and starts fresh helpers.
From the CLI:

```sh
olivares session start . --credential prod-db --credential build-host
```

The console's New session offers the tenant's credential names under More options, and a
scripted launch names them the same way (`olivares agent session create --credential
<name>`). The API field:

```json
{"provider_profile_ref": "...", "credentials": ["prod-db", "build-host", "billing-api"]}
```

## What the session sees

| kind | helper | how the session uses it |
| --- | --- | --- |
| `ssh` | an ssh-agent socket (`SSH_AUTH_SOCK`) that signs and cannot be asked for the key, and cannot be changed | `ssh -F "$OLIVARES_CREDENTIAL_SSH_CONFIG" build-host` |
| `postgres` | a unix socket that logs in to the server for the client | `PGSERVICEFILE` is set, so `psql "service=prod-db"` |
| `http` | a local proxy that adds the account's header | `curl "$OLIVARES_CREDENTIAL_BILLING_API_URL/invoices"` |

With `ca_private_key` the agent holds a key made for the session and its certificate, valid for
`ttl_seconds`, for the one account in `user`. The engine renews it (same key, a new window)
while the session runs and stops when it ends, so a short `ttl_seconds` costs a long session
nothing and a finished session keeps access for at most that long. The target needs the CA in
`TrustedUserCAKeys` and the account in `AuthorizedPrincipalsFile`. The agent signs a login for
the credential's `user` only, so the key is not turned into another account. A login is signed only
for a session bound to a host key that the credential's `known_hosts` names: OpenSSH 8.9 and later
bind the session before they sign, and a host with any other key is refused, even where the same CA
is trusted. The host keys in known_hosts are the trust: a host name is not checked, so a key listed for one name is trusted wherever it is presented. A client that does not bind the session (including OpenSSH older than 8.9 and
other clients without this extension) gets no signature, since the credential then names no host it can use. The
`ssh_config` and `known_hosts` the helper writes are the client's own files; the engine checks the
host against its own copy of the credential's `known_hosts`.

The secret is in no environment variable, no file the session can read, no run read, no launch
event (which names the credentials and their kinds) and no stored output (a value a remote
service echoes back is withheld there).

## When the session stops

The helpers stop, their sockets and files are removed and the engine drops the secrets.
That ends the session's access through the engine at once: new connections through a helper
fail, an open database connection is cut and the proxy asks the server to cancel what it was running (best effort, within three seconds), and
the API helper stops answering. An ssh login that a process of the session had already made and
kept running after the session ended stays up until it ends, because the engine does not carry
its traffic. It does not revoke a long-lived key
or password at the target: for ssh use `ca_private_key`, and the certificate expires on its
own; for a database or an API, rotate the password or token when the sealed value should change.

## Limits

- The database role, the API token's scope and the target's account decide what the session may
  do. Give a credential the narrowest account that does the job. A PostgreSQL role can change
  its own password (`ALTER ROLE ... PASSWORD`), which would leave the session a login that works
  without the helper after it ends: restrict the role's `pg_hba.conf` entry to the engine's
  address and give it a role that cannot do so where that matters.
- The administrator who seals a credential chooses what the engine connects to. The engine dials
  the credential's database host and API address as written, with no filter on internal
  addresses, and a refused start says whether the server was reachable. Sealing credentials is
  tenant administration, like the secrets it already covers.
- The ssh and API helpers need a session whose network is not confined to its provider
  (confined launches that name them are refused with 422). The database helper works for both.
- A session runs as the engine's operating-system user. The secrets live in the engine's
  memory and the helpers' sockets are in a private directory under its data directory. A
  session confined to its folder (Landlock) is granted only the helper files it needs. Measured
  on Linux 6.12 (Landlock ABI 6) with `kernel.yama.ptrace_scope` at `0`, it could not open
  `/proc/<engine>/mem` or `maps`, which an unconfined shell of the same user could: Landlock
  refuses that access to a process outside its domain.
- A helper is not hidden, so it checks who connects. Landlock does not mediate connecting to a
  unix socket, the kernel lists every bound unix socket and every loopback port to every process
  of the engine's network namespace (`/proc/net/unix`, `/proc/net/tcp`), a confined session may
  read `/proc`, and a process of the same user reads another's environment, where the helpers'
  locations are. A session that needs the ssh or API helper shares that namespace. So every
  helper serves a connection only when the process behind it is one of the run's own: the
  process the engine started for the run, or a descendant of it. The ssh agent and the database
  proxy read the peer's credentials (`SO_PEERCRED`) and follow its parents; the API helper finds
  the client socket in the kernel's table and looks for it among the run's processes. A session
  of another run, of any tenant, is refused (`ssh` fails to reach the agent, `psql` is closed,
  the API helper answers 404). The engine tells the broker the child's process id as soon as it
  has started it; a connection that comes in between waits a few seconds for that, and a helper
  whose launch never started a child serves nobody. A process that detaches from the run (a
  daemon, a double fork) is reparented to the engine's session guard, which belongs to the run,
  and stays served while the session's program runs (the guard ends with it). The check does not defend against a process that can take over one of the
  run's (ptrace, `pidfd_getfd`, `/proc/<pid>/mem`): confinement and the kernel's
  `kernel.yama.ptrace_scope` do, and two confined sessions were not measured against each other.
  The run's own processes can list and reach their helpers. A session that is not confined can
  open every socket and file that user can and read the engine's memory, so a session that names a credential is started only where
  the host can confine it: otherwise the launch is refused (502, "this server cannot confine
  sessions") and the helpers are released. `olivares doctor` says whether this host can.
- A session given a managed credential cannot be moved or handed off, and its Activity and
  Messages answer 409 and its stored conversation is not read (the live output is still
  streamed): the tool's own transcript is unredacted, and a value the broker held for an earlier
  launch cannot be withheld from it by a later one. The same holds for a session given vault
  secrets or a repository credential.
- A session in a container is refused (422): the engine's sockets are not in the container.
  Start it with native isolation.
- A passphrase-protected key is refused: the engine has nobody to ask.
- A database credential is tried once when the session starts: a wrong password or an
  unreachable server refuses the start (422) with the reason, instead of starting a session
  whose every connection fails. The client's own user, database and connection parameters are
  ignored; the credential's are used.
- A value of fewer than 8 characters is not withheld from stored output, and a token an API
  echoes in a response is replaced by a mark by the API helper, not by the output filter.
- The API helper's address (`OLIVARES_CREDENTIAL_*_URL`) carries a random part that is the
  locator of this run's proxy. It stops with the session and is reachable from the engine's host
  only, and the helper answers only the run's own processes, so learning the address (it is in the
  session's environment and in commands the session writes) is not enough to use the account. A
  process of the run that relays for another (a port forward it starts) hands its access over.
- A run records the credentials it was given in a `credential_names` column that older
  engines do not know. Upgrade every node that serves a tenant before sealing credentials: an
  older node would resume such a run without them and would not refuse to move or hand it off.
  The column is added on the next boot, empty for the runs that already exist.
- The engine does not wait for the helpers when it shuts down: they end with the process, and
  the files a stopped run leaves hold no secret (public keys, certificates and configuration).
