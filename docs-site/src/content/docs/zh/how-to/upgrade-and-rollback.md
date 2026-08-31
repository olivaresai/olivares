---
title: 升级与回滚
description: >-
  如何将自托管 Olivares AI 部署迁移到较新的版本——预览计划、执行替换、验证结果，
  并在必要时退回。涵盖自助式 `olivares upgrade` 命令、隔离网络发布包和平台镜像替换。
---

升级会替换二进制文件；它不会把你迁移到另一个产品。数据目录、审计签名密钥和 TLS
材料会留在原处，引擎在启动时自行应用所有新架构迁移。本页是操作员完成整个过程的路径，
从“我应该采用这个版本吗？”直到“我需要恢复上一个版本”。

:::caution[先备份]
每次升级前都要备份，包括看起来很常规的升级。控制台的 **Backups** 屏幕
（`/backups`）和[备份与恢复](/zh/how-to/backup-and-restore/)都可以完成备份。本页的
任何操作都不以已有备份为前提——但总会有一次意外让你庆幸自己做过备份。
:::

## 选择升级路径

二进制文件有两种升级方式，最终结果相同。

| 你的安装方式 | 路径 |
|---|---|
| 主机上的二进制文件、systemd、Docker Compose | `olivares upgrade`——本页 |
| Kubernetes / Helm | 设置镜像，让 operator 执行滚动更新。不要在 pod 内运行 `olivares upgrade`：部署是声明式的，下一次协调会撤销它。 |

## 首先阅读计划

`--check` 会下载并验证渠道清单，将其与已安装版本比较，然后输出将要发生的操作。
它不会替换任何内容。

```sh
olivares upgrade --check
```

输出包括已安装版本、可用版本，以及以下状态之一：`up to date`、
`upgrade available`、`DOWNGRADE (blocked unless --force-rollback)` 或 `UNKNOWN`。
请阅读状态行，不要自行比较两个版本号。

**`UNKNOWN` 并不表示“应该没问题”。** 它表示无法测量已安装版本——例如跨架构
暂存目录、`noexec` 挂载点或源码构建。防回滚保护和最低版本门槛都是针对
*已安装版本*作出的判断，因此两者都无法求值。命令会拒绝继续，不会猜测。声明你确定
已经安装的版本，所有保护仍会启用：

```sh
olivares upgrade --check --current-version 26.8.0
```

## 发布渠道

<!-- BEGIN GENERATED olivares-upgrade-channels — regenerate with `bash scripts/check-guide-docs.sh --write`; do not edit by hand -->

`olivares upgrade` 跟随一个发布**渠道**。共有 **3** 个，按稳定性递增顺序声明在
`core/release/manifest.go` 中：

| `--channel` 值 | 声明为 |
|---|---|
| `stable` | `release.ChannelStable` |
| `security` | `release.ChannelSecurity` |
| `lts` | `release.ChannelLTS` |

不在此表中的值会在下载任何内容前被拒绝（`release.ValidChannel`）。

<!-- END GENERATED olivares-upgrade-channels -->

`stable` 是正式发布渠道，也是默认值。`security` 只携带带外安全修复，不含其他内容；
跟随它的部署会采用安全版本，而不会采用功能版本。

:::caution[`lts` 能通过验证，但没有发布方]
上表由代码声明的渠道常量生成，因此列出了 `--channel` 接受的每个值——其中包括
`lts`。**没有生成或发布任何 `lts` 清单**，所以跟随它的部署会向更新主机请求不存在的
对象。安全支持仅在合同期限内提供，不含常规回移修复，也没有冻结版本线：权利在已付费
期限内有效，没有累积的降级权益，也没有永久权利。请选择 `stable` 或 `security`。
:::

选择符合运维方式的渠道，并持续使用它：

```sh
olivares upgrade --channel security
```

安全版本会在清单中标记，`--check` 会输出它修复的安全公告。如果使用 security 渠道，
你会在正式发布线之外接收这些版本。

## 执行升级

```sh
olivares upgrade
```

命令依次执行以下操作，每一步都有明确目的：

1. **下载渠道清单并离线验证其签名**，验证依据是构建中嵌入的 Ed25519 发布密钥。
   信任锚是签名，不是传输。没有嵌入密钥的构建要求通过 `--pubkey` 提供密钥；不存在
   未验证路径。
2. **拒绝后退。** 安装比当前运行版本更旧的版本会被阻止，除非传入
   `--force-rollback`；该操作会写入审计记录。
3. 在执行任何字节前，**将制品绑定到清单签名的 SHA-256**。
4. **探测候选文件**，然后进行原子替换，并保留被替换二进制文件的带时间戳备份。
   如果新安装的二进制文件无法运行，命令会自行恢复该备份。
5. **不干扰正在运行的进程。** 替换只改变磁盘上的文件。服务重启后，新代码才会接管。

如果脚本正在驱动升级且无人响应确认提示，请添加 `--yes`。

:::note[没有热补丁]
Go 二进制文件不会原地打补丁。这里的“零停机”指优雅排空和切换，或滚动重启——绝不
是进程内补丁。无需重启即可实时应用的是数据和配置：来源、连接器、机密、策略和许可证。
:::

## 隔离网络安装

隔离网络部署绝不会连接更新主机。使用你已经信任的方式移入发布包，再从本地文件安装；
验证过程完全相同，因为受信任的从来都不是网络。

**从发布包安装要求机器上有有效许可证。** 它会依据二进制文件内嵌的许可证密钥离线
检查：不会发出网络请求，所以能够在隔离网络中工作。如果尚未在机器上安装许可证，请参阅
[安装许可证并迁移到企业版](/zh/how-to/install-a-license/)。
`--check` 不受许可证限制，因此
可以在暂存任何内容前验证发布包：

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --check   # verify only; no license read
olivares upgrade --bundle ./olivares-release.tar.gz --yes     # install; needs a live license
```

如果构建没有内嵌发布密钥，或者镜像发布使用你自己的签名密钥，请将命令指向验证密钥：

```sh
olivares upgrade --bundle ./olivares-release.tar.gz --pubkey @/etc/olivares/release.pub
```

发布包的生成和跨越隔离边界方式请参阅[隔离网络安装](/zh/how-to/air-gap-install/)。

## 分阶段发布和无人值守检查

清单可以指定分阶段发布群组，让版本先到达一部分基础设施。`--if-eligible` 使节点只在
属于该群组时执行操作，否则什么也不做：

```sh
olivares upgrade --if-eligible --yes
```

内置计时器运行的就是这种形式。要生成在维护窗口内调用它的 systemd 计时器和服务：

```sh
olivares upgrade --install-timer --timer-schedule 'Sun *-*-* 03:00:00'
```

它默认输出 unit；`--timer-dir` 会将它们写入指定位置。这是选择加入功能——不会自行安排。

控制台提供相同信息的只读部分：**Settings → update status** 调用
`POST /v1/console/update-check`，按需检查配置的渠道。隔离网络部署或未配置渠道的部署会
返回 `501` 并说明原因，而不会报告没有更新。

## 验证升级

```sh
olivares version
olivares upgrade --check
```

此时 `--check` 应报告 `up to date`。然后确认服务本身健康：查看控制台的 **Health**
屏幕（`/health`），或按照[使用 Prometheus 监控](/zh/how-to/monitor-with-prometheus/)
检查引擎的就绪端点。

## 回滚

上一个二进制文件会保存在替换它的文件旁边，命令在替换时会输出其路径。回滚就是恢复
该文件并重启服务。

回滚的安全来自设计，而非运气：每次架构变更都先作为增量 expand 发布，破坏性的
contract 只在后续版本发布，因此上一版本的二进制文件能够继续使用升级后的架构。这使
回滚成为“放回旧二进制文件”，而不是“逆转数据库”。

如果需要安装旧版本而不是恢复保留的备份，防回滚保护会阻止操作，直到你明确授权：

```sh
olivares upgrade --force-rollback --yes
```

覆盖操作会写入审计日志。最低版本门槛**不能**由它覆盖：如果清单声明的最低版本高于
已安装版本，请先经过一个中间版本，而不是尝试直接跳跃。

## 出现问题时

| 症状 | 含义 | 处理方式 |
|---|---|---|
| `--check` 输出 `UNKNOWN` | 无法测量已安装版本，因此无法判断版本顺序 | 通过 `--current-version` 传入你确定已安装的版本 |
| `min_ver` 表示版本过旧 | 该版本拒绝直接覆盖你的版本安装 | 先升级到指定的中间版本 |
| 新二进制文件未启动 | 替换后的探测失败 | 它已经恢复备份；检查日志并报告该版本 |
| `--install-timer` 触发但没有操作 | 节点不在分阶段发布群组中 | 使用 `--if-eligible` 时属于预期行为；群组会随发布进展扩大 |
| “another olivares upgrade is already installing”，退出码 **5** | 每个二进制文件一次只能进行一项升级。整个下载和替换过程都会持有锁 | 等待正在运行的升级，再重新执行。如果没有进程在运行，内核已经释放锁，立即重试即可 |
| “it CHANGED while this upgrade was downloading” | 制定计划后，有其他机制替换了二进制文件——包管理器、镜像发布或配置管理作业 | 重新运行：保护会根据实际安装内容重新求值。如果持续发生，则有两个机制在管理同一个二进制文件 |

**每个二进制文件只能有一个升级代理。** `olivares upgrade` 在整个准备—下载—替换
过程中对目标加独占锁，因此第二次运行会以退出码 `5` 退出，而不会执行安装。请只安装
**一个**计时器并更改其中的 `--channel`，不要为每个渠道运行一个计时器：过去，两个在
同一秒完成的安装会覆盖彼此的回滚备份，失败一方的自动回滚随后会恢复*另一个*二进制
文件并报告成功。就在替换前，命令还会重新读取目标字节；如果它们不是计划所依据的
字节，命令就会拒绝继续，因为防回滚和最低版本判断是针对某个特定已安装文件的判断。

其他问题请使用通用[故障排除](/zh/how-to/troubleshooting/)路径；控制台的 **Logs**
屏幕（`/logs`）会流式显示引擎自己的日志。
