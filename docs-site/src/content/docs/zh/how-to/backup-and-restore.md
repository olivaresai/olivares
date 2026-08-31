---
title: "备份与恢复（能自证的灾备）"
description: >-
  使用 olivares dr 进行加密的、账本连续性安全的备份：面向 SQLite 与 Postgres
  的定时捆绑包、会验证账本链的恢复、无需触及生产环境即可运行的演练——以及决定
  你的证据能否存续的那两把密钥。
---

控制平面的备份比大多数备份的任务更艰巨：它必须带着其**可证明完好无损、篡改可检测的
账本**回来。`olivares dr` 正是围绕这一要求构建的——每个捆绑包都记录每个租户的
链尖，恢复在**所恢复的账本不具备连续性安全时以非零状态失败**，而演练子命令则
在不触及生产环境的情况下证明一个捆绑包可恢复。

捆绑包在**由你提供的 KEK** 下加密——一个 Argon2id 派生的口令
（`--passphrase-file`）或一个来自你的 KMS 的原始 32 字节密钥
（`--kek-key-file`）；二者必须且只能提供其一。审计与目录签名密钥以**密封**
形式随捆绑包一同传输。

## 备份

**SQLite**（单节点）—— 在 `serve` 运行时也可安全执行（快照使用
`VACUUM INTO`；WAL 允许并发读取）：

```bash
olivares dr backup \
  --data-dir /var/lib/olivares --engine sqlite \
  --out /backups/olivares-dr-$(date -u +%Y%m%dT%H%M%SZ).drbundle \
  --passphrase-file <your-dr-passphrase-file>
```

**Postgres** —— 由同一命令驱动的一致性 `pg_dump --format=custom`
（`--engine postgres --dsn … --admin-dsn …`），或用 `--snapshot-file` 把一个预制的
转储交给它。直接执行该转储**必须提供 `--admin-dsn`**：`pg_dump` 会保持
`row_security=off`，以应用角色去访问 `FORCE ROW LEVEL SECURITY` 的表时会**中止**，
因此该命令会在一开始就拒绝执行，而不是跑完却什么也没产出。
对于近乎零 RPO，`--pitr-ref` 会生成一个密钥+清单的配套捆绑包，与你的 WAL 归档
PITR 配置（`deploy/postgres/backup/pitr-setup.md`）配对使用；包装脚本
`deploy/postgres/backup/pg-dump.sh` / `pg-restore.sh` 封装了相同的流程。

两个值得了解的诚实开关：

- 备份在备份时**拒绝捕获一个无法通过验证的账本**——`--allow-unverified` 存在、
  会被记录日志，且不推荐使用。
- 在使用**预制**快照（`--snapshot-file`/`--pitr-ref`）且没有 `--admin-dsn`
  （一个专用的 `NOSUPERUSER BYPASSRLS` 角色）时，备份会警告：所捕获的租户集合
  可能受 RLS 限制而**不完整**——转储本身没有问题，需要该 admin 角色的是清单里的
  跨租户清点。（**直接**执行 `pg_dump` 是另一种情况：会被直接拒绝，见上文。）

**调度：** Compose 栈提供了一个
[备份 profile](/zh/tutorials/getting-started/docker-compose/#3-加密的-dr-备份backup-profile)，
Helm chart 提供了一个
[CronJob](/zh/tutorials/getting-started/kubernetes/#4-定时加密备份)；
在裸机上，把上面的命令放进 cron。你的调度间隔**就是**你的 RPO：

| 分级 | 机制 | RPO | RTO |
|---|---|---|---|
| SQLite | cron 上的 `dr backup` | cron 间隔 | < 15 分钟 |
| Postgres 逻辑备份 | cron 上的 `pg-dump.sh` | cron 间隔 | < 30 分钟 |
| Postgres PITR | 基础备份 + WAL 归档 | ≈ 秒级 | < 30 分钟 |

把捆绑包镜像到**异地**，并把 KEK 与捆绑包**分开存放**（3-2-1）：同主机备份
不是灾难恢复，而与口令一同传输的捆绑包在任何有意义的意义上都算不上加密。

## 演练——在你需要它之前

`dr verify` 在**不触及你的数据目录**的情况下证明一个捆绑包可恢复（SQLite：在
临时目录中进行完整链验证；若不安全则以非零状态退出）：

```bash
olivares dr verify --in /backups/olivares-dr-<ts>.drbundle \
  --passphrase-file <your-dr-passphrase-file>
```

`dr inspect --in <bundle>` 会打印清单（无需 KEK，不显示任何机密）——用的是哪个
引擎、哪些租户、哪些链尖。以与备份相同的节奏运行演练；一份未经验证的备份是一种
期望，而不是一项控制措施。

## 恢复

```bash
olivares dr restore --in /backups/olivares-dr-<ts>.drbundle \
  --data-dir /var/lib/olivares --engine sqlite \
  --passphrase-file <your-dr-passphrase-file>
```

恢复顺序是刻意安排的：先是签名密钥（覆盖时失败关闭——`--force` 是显式的
覆盖手段），然后是存储快照，接着它会**启动所恢复的存储并证明账本连续性**，
若链不安全则以非零状态退出。任何恢复之后，都要针对你的**离机**检查点固定值
重新验证——一个恢复出来的较旧快照可能通过朴素的遍历，却在离机比对中失败
（[故障排查 § 账本](/zh/how-to/troubleshooting/#ledger-验证失败)）。

## 决定一切的那两把密钥

| 密钥 | 规则 |
|---|---|
| **DR KEK**（口令或原始密钥） | 没有它，每个捆绑包都只是噪声。把它存放在与捆绑包不同的系统中；同时丢失两者就是失效模式 |
| **`audit-signing.key`**（在数据目录中） | 在置备时就把它离机备份——引擎仅在首次启动时**警告**，没有强制托管，丢失该密钥会使账本永久无法验证。也要把公钥离机固定（`GET /v1/audit/pubkey`） |

关于以 KMS 方式托管签名密钥本身（BYOK 信封、轮换仪式、`olivares keys`），见
[CLI 参考](/zh/reference/cli/)；关于失效模式的逐步讲解，见
[故障排查页](/zh/how-to/troubleshooting/)。
