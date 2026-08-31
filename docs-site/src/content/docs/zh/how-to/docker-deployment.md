---
title: 使用 Docker 部署
description: >-
  从 Docker Hub 拉取并验证镜像，然后用 Docker 在生产环境运行 control plane——
  加固的单节点 SQLite、多租户 Postgres、定时 DR 备份、反向代理 TLS 终止、
  升级与 digest 固定。
---

本指南面向用 Docker 将 Olivares AI control plane 投入生产的工程师与 SRE。
整个产品是一个 distroless 单镜像——引擎内嵌 web UI——因此单台主机即可运行
SQLite 拓扑而无需任何外部依赖，在需要时通过 Postgres override 即可获得多租户拓扑。
每条路径都保持相同的安全默认值：无默认凭据、一次性 setup token、默认开启 TLS，
以及将主机端口绑定到 loopback。

:::note[Beta——尚未发布任何版本]
Olivares AI 处于 **beta** 阶段。下文的镜像坐标只有在**第一个版本
（CalVer `26.8.0`）发布之后**才能解析；在此之前各 registry 上没有任何可拉取的内容。
请将其视为你将要使用的部署形态，而非可投入生产的保证。
:::

要从决策页的角度查看所有部署选项及其默认值，参见
[自托管 control plane](/how-to/self-hosting/)。对于断网站点，参见
[在 air-gapped 环境中安装](/how-to/air-gap-install/)；对于横向扩展，
参见下文的 Kubernetes/Helm 路径。

## 1. 拉取并验证镜像

主要的容器拉取来源是 **Docker Hub**：

```bash
docker pull docker.io/olivaresai/olivares:26.8.0
```

相同的内容也发布到 `ghcr.io/olivaresai/olivares`——按 digest 完全一致，
用作备份和构建 registry。Docker Hub 对**匿名**拉取施加速率限制；ghcr.io 不对公共镜像的匿名拉取
限速——因此当 CI 节点或大规模集群触及上限时，可以先 `docker login`，或改用 ghcr.io 坐标。
Tag **不带前导 `v`**：
`:26.8.0` 固定一个版本，`:latest` 浮动，而 `:26.8.0-fips` / `:26.8.0-stig`
是加固变体。基础 tag 和 `:latest` 是多架构的
（`linux/amd64`、`linux/arm64`）；`fips`/`stig` 仅有 `amd64`。

control plane 是一款安全产品，所以运行前先验证。签名是
**无密钥（keyless，Sigstore）**的，针对项目的 GitHub Actions 身份，
并且对任一 registry 工作方式相同——签名与证明（attestation）通过
`cosign copy` 复制到 Docker Hub，因此 digest 相同：

```bash
IMAGE=docker.io/olivaresai/olivares          # fallback: ghcr.io/olivaresai/olivares (same digest)
DIGEST="$(crane digest "$IMAGE:26.8.0")"
REF="$IMAGE@$DIGEST"

cosign verify "$REF" \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
cosign verify-attestation "$REF" --type spdxjson \
  --certificate-identity-regexp '^https://github\.com/olivaresai/olivares/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

完整的信任链——校验和签名、SBOM、OpenVEX、SLSA provenance——在
[验证你下载的内容](/how-to/verify-a-release/)中。验证完成后，
请按你所验证的 **digest** 进行部署，绝不要用可变的 tag
（参见 [§8](#8-生产环境按-digest-固定)）。

## 2. 单节点，SQLite

### 使用 `docker run`（加固）

镜像的默认命令在**容器内部**绑定 `0.0.0.0`，以便你用 ingress 在前面承载它；
下面的主机侧端口映射将暴露面限定在 loopback。以非 root、只读、丢弃所有
capabilities 的方式运行：

```bash
docker volume create olivares-data

docker run -d --name olivares \
  --user 65532:65532 \
  --read-only \
  --tmpfs /tmp \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -v olivares-data:/var/lib/olivares \
  -p 127.0.0.1:8443:8443 \
  -p 127.0.0.1:8444:8444 \
  docker.io/olivaresai/olivares:26.8.0 \
  serve \
    --listen=0.0.0.0:8443 \
    --grpc-listen=0.0.0.0:8444 \
    --data-dir=/var/lib/olivares \
    --checkpoint-interval=1h
```

| 标志 | 原因 |
|---|---|
| `--user 65532:65532` | 以烘焙进 distroless 镜像的非 root `nonroot` UID 运行 |
| `--read-only` | 根文件系统不可变；只有数据卷和 `/tmp` 可写 |
| `--tmpfs /tmp` | 一个可写的临时 tmpfs，因 rootfs 只读而必需 |
| `--cap-drop ALL` | 引擎不需要任何 Linux capabilities |
| `--security-opt no-new-privileges` | 阻止通过 setuid 二进制提权 |
| `-v olivares-data:/var/lib/olivares` | 持久化数据目录（参见 [§5](#5-运维须知)） |
| `-p 127.0.0.1:8443:8443` | 仅向 **loopback** 发布 HTTPS（REST + web UI） |
| `-p 127.0.0.1:8444:8444` | 仅向 loopback 发布 gRPC（摄取 / ControlPlane API） |

从日志读取一次性 setup token 并创建第一位管理员：

```bash
docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

curl -fsS -k -X POST https://127.0.0.1:8443/v1/setup \
  -H 'Content-Type: application/json' \
  -d '{"token":"<olst_ token>","email":"you@example.com","password":"<strong-password>"}'
```

`-k` 接受引擎在首次启动时签发的自签名证书；请通过反向代理
（[§6](#6-反向代理--tls-终止)）或你自己的 TLS 材料将其替换为
真实证书。该 token **只显示一次**，且为一次性使用。

### 使用 Docker Compose

仓库附带一套 Compose stack，它接好数据卷、loopback 端口映射，
以及与上文相同的加固标志：

```bash
docker compose -f deploy/compose/docker-compose.yml up -d

# Read the one-time first-boot setup token:
docker compose -f deploy/compose/docker-compose.yml logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'

# Then open https://localhost:8443 (self-signed TLS by default)
```

基础文件将镜像默认设为 `docker.io/olivaresai/olivares:latest`（Docker Hub）；
若要进行可验证的生产部署，请在 `deploy/compose/.env` 中将 `OLIVARES_IMAGE`
设为一个 digest 固定的引用（参见 [§8](#8-生产环境按-digest-固定)）。
数据持久化在 `olivares-data` 卷中。

## 3. 多租户 Postgres

对于多租户拓扑，在基础文件之上叠加 Postgres override。
先设置两个密码，再拉起 stack：

```bash
cp deploy/compose/.env.example deploy/compose/.env   # set POSTGRES_SUPERUSER_PASSWORD + OLIVARES_DB_PASSWORD
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.postgres.yml up -d
```

该 override 会拉起 `postgres:16-alpine`，在首次初始化时配置**最小权限**的
`olivares_app` 角色和 `olivares` 数据库（通过 `initdb/10-app-role.sh` 运行
规范的 `deploy/postgres/01-app-role.sql`），并用 `--engine=postgres` 将引擎
指向该非超级用户角色。这使得 FORCE-RLS 租户兜底真正生效：
引擎面对超级用户/`BYPASSRLS` 角色时**拒绝启动**。

:::caution[`sslmode=disable` 仅用于网内演示]
override 中的 DSN 使用 `sslmode=disable`，因为两个容器共享同一个 Docker
网络。**生产环境应使用 TLS 并设 `sslmode=verify-full`。** 对于加固部署，
更应选用 Helm chart，配合一个 DSN Secret 和一个托管的（或你自己的）Postgres——参见
[§8](#8-生产环境按-digest-固定)。
:::

## 4. 灾难恢复备份

backup profile 生成定时的、账本连续性安全的 DR 包：存储快照加上签名密钥，
在你的 KEK 下加密，并附带一份各租户链尖（chain tip）的清单。先将你的口令写入
一个**置于仓库与镜像之外**保存的文件，然后运行一次性的 `backup` profile：

```bash
printf 'a strong DR passphrase' > deploy/compose/dr-pass
# the host stamps the bundle name (the distroless image has no `date`):
BACKUP_TS="$(date -u +%Y%m%dT%H%M%SZ)" \
docker compose -f deploy/compose/docker-compose.yml \
               -f deploy/compose/docker-compose.backup.yml \
               --profile backup run --rm backup
```

该作业共享引擎的数据卷，将包写入 `olivares-backups` 卷，并且——由于镜像是
distroless 的——把保留策略交给主机：用主机 cron 清理旧包
（`find <backups> -name '*.drbundle' -mtime +14 -delete`）。把该运行包进主机
cron 以实现定时 RPO，并**将 `olivares-backups` 卷异地镜像**——同主机的备份
不构成灾难恢复。用以下命令恢复并验证：

```bash
olivares dr restore --in <bundle> --data-dir <dir> --passphrase-file dr-pass
```

完整的 RPO/RTO、密钥保管和 DR 演练流程随仓库的 DR runbook 一同提供；
更高层的演练在[备份与恢复](/how-to/backup-and-restore/)中。

## 5. 运维须知

**从主机而非容器探测健康状态。** 该镜像是 **distroless** 的——没有 shell
也没有 `curl`，因此有意不在容器内设 `HEALTHCHECK`。引擎在 HTTPS 端口上暴露
`/livez` 和 `/readyz`；请从主机（或你的编排器）探测它们：

```bash
# liveness — process is up; no dependency checks, so a store outage never restart-loops:
curl -fsS -k https://127.0.0.1:8443/livez

# readiness — store ping (and HA leadership): 200 when serving, 503 when the store is down:
curl -fsS -k https://127.0.0.1:8443/readyz
```

`/readyz` 的可达性就是可用性信号——把它接入你的外部监控
（参见 [用 Prometheus 监控](/how-to/monitor-with-prometheus/)）。

**setup token 只在日志中出现一次。** 首次启动会在容器输出中打印一个一次性的
`olst_…` token。请在缓冲区轮转之前用
`docker logs olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'`（或 Compose 等效命令）
将其捕获；它在你创建第一位管理员时被消耗。

**备份数据目录。** `/var/lib/olivares`（即 `olivares-data` 卷）保存着
**SQLite 存储、审计签名密钥以及 TLS 材料**。丢失它就会丢失账本的签名身份
并破坏审计连续性，所以请保护并备份该卷——使用 [§4](#4-灾难恢复备份)
中的 DR profile，而非对运行中存储的临时拷贝。

## 6. 反向代理 / TLS 终止

开箱即用时引擎提供自己的**自签名**证书，这对评估足够，但不适用于会校验
信任的客户端。在生产环境中，用一个以运营者提供的证书（来自你的 CA 或 ACME）
终止 TLS 的反向代理来承载绑定在 loopback 上的引擎，并让该代理成为网络上唯一
被暴露的东西。

由于引擎本身讲 TLS，代理通过 loopback 端口以 HTTPS 连接它。一个最小的 nginx
server 块：

```nginx
server {
  listen 443 ssl;
  server_name olivares.example.com;

  ssl_certificate     /etc/ssl/olivares/fullchain.pem;   # operator-provided cert
  ssl_certificate_key /etc/ssl/olivares/privkey.pem;

  location / {
    proxy_pass         https://127.0.0.1:8443;   # engine's own TLS on loopback
    proxy_ssl_verify   off;                       # engine cert is self-signed
    proxy_set_header   Host              $host;
    proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header   X-Forwarded-Proto $scheme;
  }
}
```

用 Caddy 的等效配置，它会自动配置一张公开证书：

```caddy
olivares.example.com {
  reverse_proxy https://127.0.0.1:8443 {
    transport http {
      tls_insecure_skip_verify   # engine cert is self-signed on loopback
    }
  }
}
```

将引擎的主机端口保持绑定在 `127.0.0.1`（上文的默认值），这样只有代理可达。
gRPC 摄取端口（`8444`）用于 collector；只有在你运行分布式拓扑时，才有意地、
用其自己的 TLS 路径暴露它。

## 7. 升级

数据卷在容器替换间保持持久，所以一次升级即为：备份、拉取新的固定 tag、
重新创建容器。

```bash
# 1. Back up first (see §4).
# 2. Pull the new release and re-verify it (see §1):
docker pull docker.io/olivaresai/olivares:26.8.1

# docker run:
docker stop olivares && docker rm olivares
# re-run the §2 command with the new tag — the olivares-data volume is reused.

# Compose: set OLIVARES_IMAGE to the new digest in .env, then:
docker compose -f deploy/compose/docker-compose.yml up -d
```

重新创建容器不会触及命名卷，因此存储、签名密钥和 TLS 材料会延续过来。
始终**在升级前备份**，并在重新创建之前重新验证新镜像。

## 8. 生产环境按 digest 固定

可变 tag（`:26.8.0`、`:latest`）用于评估。在生产环境中，请固定你所验证的
**digest**——digest 不可变，且正是你签字确认过的东西：

```bash
docker run ... docker.io/olivaresai/olivares@sha256:<digest> serve ...
```

对于 Compose，在 `deploy/compose/.env` 中设置 digest 引用：

```bash
OLIVARES_IMAGE=docker.io/olivaresai/olivares@sha256:<digest>
```

对于横向扩展和多节点，使用 Helm chart——它作为 OCI artifact 发布于
`oci://ghcr.io/olivaresai/charts/olivares`，经 cosign 签名，并按镜像 digest 固定。
chart 命令参见[自托管 control plane](/how-to/self-hosting/)，
完全断网站点参见[在 air-gapped 环境中安装](/how-to/air-gap-install/)。
