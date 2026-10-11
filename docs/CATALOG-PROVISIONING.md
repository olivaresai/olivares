# Provision an approved catalog entry

Catalog keeps the approved artifact and the instance request. Deploy performs
infrastructure changes through its configured executor and governance approvals.
An instance becomes `active` only after deploy reports `applied` or `noop`.

1. Create and approve an agent or MCP catalog entry whose `spec` is a deploy
   desired spec (`image`, `command`, resources and secret references).
2. Declare a deployment using the same spec, a target and runtime, and
   `source_ref: "catalog entry <entry-id>"`. The existing
   `olivares deploy definitions create` command accepts `--source-ref` and
   `--spec-file`; declaration itself does not change infrastructure.
3. Instantiate the approved entry with `target_ref: "deployment:<definition-id>"`
   and approve the instance request.
4. Run `olivares catalog instances transition <instance-id> --status active --yes`.
   The first apply normally returns HTTP 202, the instance's unchanged `approved`
   status, `requires_approval: true` and `approval_ref`. The CLI returns exit 7.
5. Have the required independent approvers decide that reference through
   governance. Repeat the catalog transition with `--approval-ref <reference>`.
   Success returns the instance with `status: "active"`.

The REST transition accepts the same optional `approval_ref`. Deploy checks that
the definition's source and current spec match the approved entry before planning
or requesting approval. The dispatch uses the caller's credentials and tenant:
catalog approval does not grant deploy permissions, satisfy step-up or bypass an
emergency stop. Executor, approval or persistence failures leave the instance
unactivated; retry after correcting the reported error.

Existing opaque target labels and non-deploy catalog specs remain stored and
readable. They cannot provision until a compatible deployment definition is
named. Previously stored active instances are retained. Catalog console request
and approval actions keep working; the console does not yet collect a deployment
approval reference, so complete that leg through REST or the CLI. Other catalog
kinds and runtimes require their own provisioning qualification.

Instance decisions are serialized by one engine while it applies a deployment.
Concurrent engines writing the same catalog require separate qualification.
