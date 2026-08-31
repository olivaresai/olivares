# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#

# Cloud managed estate — Terraform

**Status: NEVER APPLIED.** No `terraform apply` has run for this estate.

This directory defines six modules, one domain each. Committing or validating the
configuration does not create cloud resources; an authenticated, explicitly confirmed
workflow run is required to apply it.

| Module | Domain |
|---|---|
| `modules/network` | VPC, 2 AZ public/private, NAT (required), security groups |
| `modules/data` | RDS PostgreSQL 16 Multi-AZ, subnet/parameter groups, KMS |
| `modules/compute` | ECS Fargate cluster/service, ECR, task/execution roles |
| `modules/ingress` | NLB TCP passthrough + ALB HTTPS + ACM + WAF |
| `modules/secrets` | Secrets Manager: **six** slots — dsn, license-signing, audit, tls,
  cloud-cp-admin, cloud-cp-forward — under one CMK |
| `modules/observability` | Log groups, retention, alarms |

Current configuration:

- Region `us-east-1`.
- NAT Gateway is **required** for configured outbound services without VPC endpoints.
- Collector source IP: `preserve_client_ip = true` on the NLB IP target group.
- The collector NLB also sets `proxy_protocol_v2 = true`. The binary terminates
  mTLS and must see the real peer.

CI — two workflows, and neither of them has ever run against AWS:

- **`aws-terraform.yml` `validate`** (push/PR): `tofu validate` + estate check.
  Never applies, and **takes no AWS credentials at all** — the guard treats a
  credential step outside the `apply` job as a finding, because that job runs on
  every branch.
- **`aws-terraform.yml` `apply`** (`workflow_dispatch`): runs `tofu apply` only
  when `confirm == apply-sandbox-estate` **and** `AWS_ROLE_ARN` +
  `TF_BACKEND_BUCKET` are both set. Empty secrets with that confirm is a refusal
  (exit 1), not a green skip. It reaches AWS by exchanging its OIDC token through
  `aws-actions/configure-aws-credentials`, pinned to a commit OID and placed
  **before** the first `tofu` invocation; the S3 backend is initialised with
  `use_lockfile=true`.
- **`aws-images.yml` `push`** (`workflow_dispatch` **only**): builds the control-plane
  and engine images, signs both by digest with cosign (keyless) and pushes them to the
  ECR repository this estate creates. `confirm == push-images-to-ecr` is its gate. It
  has no `push:` or `schedule:` trigger, and the guard checks that **by absence**.

The workflows do not create an AWS account and store no credentials. The only path
to the account is the repository's role, assumed through OIDC.

Root variables that stay empty until someone fills them, and what each unlocks:

| variable | empty means | filled by |
|---|---|---|
| `hostname` | no ACM certificate, no `:443` listener, no `:80` redirect | a DNS record that exists first — ACM validates by DNS |
| `certificate_arn` | the module issues its own certificate from `hostname` | an existing ACM certificate, if there is one |
| `image` | no control-plane task definition and no service | a digest from `aws-images.yml` |
| `engine_image` | no engine task definition and no service | a digest from `aws-images.yml` |

⛔ **The ECR repository is created by this apply**, so the first apply cannot carry an
image digest. Three dispatches, in this order:

1. `aws-terraform.yml` with all four inputs empty — VPC, RDS, ECR, secrets, ingress.
   No ECS task and no service, because both are gated on a digest.
2. `aws-images.yml` with `confirm=push-images-to-ecr` — it prints the two
   `repository@sha256:…` references in the job summary.
3. `aws-terraform.yml` again, pasting those two into `image` and `engine_image`.

`hostname` joins step 1 if its DNS record already exists, and step 3 otherwise: ACM
validates by DNS, so a name without its record leaves the certificate pending and the
apply waiting on it.

The role that applies this estate should not keep AdministratorAccess. A least-privilege
replacement derived from the resources declared here is maintained by the operator, in
several attachable parts rather than one — a single document covering this estate runs to
about 11 000 characters, and an IAM customer managed policy is capped at 6 144 excluding
whitespace (an inline role policy, at 10 240). **No such policy has been exercised by an
apply**, so adopt it alongside the existing permissions first and read what it denies,
rather than swapping it in and declaring it sufficient.

Engine start is `serve --engine postgres --dsn file:/mnt/secrets/dsn`
(the engine has no DSN env fallback). Default `desired_count` is 2:
`/readyz` 200 is the writer, 503 drains the standby. ALB health
matcher is 200 so standbys take no HTTP. NLB `preserve_client_ip`
is on.
