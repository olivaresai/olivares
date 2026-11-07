---
title: "模块 II——实时运行与会话"
description: >-
  每个 agent 会话的实时运行叠加层：当前动作、实时 token/成本、派生的 Claude Code 状态，
  以及可回放的时间线，通过服务器推送事件（SSE）流式传输。它派生什么、什么如实地保持为空，
  以及其限制。
---

模块 II 是 estate 的**实时运行**视图：每个 agent 会话此刻在做什么、它的实时 token 和成本
总量、一个派生的 Claude Code 状态，以及一条可重建的时间线。模块 I（清点）物化的是持久的
estate，而模块 II 则在同一条观测流之上为每个会话维护一个**实时运行叠加层**——并且只展示
该流如实承载的内容。

Olivares <!-- release -->0.8<!-- /release --> 还会在 [提供商配置文件](/how-to/operate-provider-sessions/) 下把官方提供商
CLI **作为自有子进程启动**。该受管路径是同一模块。它不替代叠加层，也不合并两个
宣布同一提供商会话 id 的主目录（`CHANGELOG.md` `[26.9.0]` B1/B2）。

## 它是什么

模块 II 是一个由总线驱动的 Core 层模块，与清点同级。它维护一份按每个会话的外部引用作键的
实时记录，由协作式的观测流构建——从不轮询、从不编造。每个会话它跟踪：

- **当前动作**（最后使用的工具）以及它所触及的资源/模式；
- **实时 token 与成本总量**，读自成本采样（规范的成本账本和 FinOps 是模块 XI，不在此处——
  这里只是实时数字）；
- 一个**派生的 Claude Code 状态**（`cc_state`）；以及
- 一条**时间线**，每个被观测到的事件按摄取顺序追加于其上。

## 它的契约与实体

本模块注册了两个按租户限定的实体。`sessions.live` 持有每个会话的实时记录——当前
动作/资源/模式、模型引用、实时输入/输出 token、实时成本、事件与工具调用计数，以及首次/最后
事件时间戳。`sessions.timeline` 为每个事件持有一行可回放记录，按摄取排序。**没有存储的生命周期
列**：协作式流不携带任何结束或失败信号，因此唯一如实的存活性信号是派生的 `cc_state`。

`cc_state` 在**读取时**从事件新近度派生——`active` / `idle` / `ended`——并在连接器触发该
发现项时翻转为静默规避（silent-evasion）状态（它从不由模块自身写入）。读取在模块路由下提供
（实时列表、单个会话、按会话的时间线）外加一条实时 SSE 流；每次读取都需要会话读取权限，且
**打开该流会被自动审计**。SSE 通道是严格**按租户隔离的**（一个客户端只接收其授权租户的快照），
且是**尽力而为的**（慢速客户端会丢弃中间帧并获取下一帧——摄取从不阻塞）。

## Provider profiles and `live_ref`

**提供商配置文件**是某一执行环境上一个已配置提供商实例的持久身份：驱动程序、
环境和规范的 `config_home` / `user_home`。它不是已认证的提供商账户。注册、
重命名、停用/启用和退役位于 `/v1/m/sessions/provider-profiles`。路径只出现在
管理员 `configuration` 读取上。启动会命名 `provider_profile_ref`；服务器解析
homes，并在 spawn 前把非秘密快照持久化到该 run（`CHANGELOG.md` `[26.9.0]` B1；
`web/src/features/agentops/types.ts`）。

观测折叠进其 **channel** 的实时行，由服务器从主机盖章的源登记计算
（`CHANGELOG.md` `[26.9.0]` B2）：

| Channel | Meaning |
|---|---|
| `legacy` | no registration |
| `observed` | a source dedicated to a profile by a binding approved at host admission for the exact applied revision |
| `source` | a known registration with no verifiable profile |
| `managed` | a run the plane launched; the only row carrying `canonical_sid` and `run_ref` |

每一实时行公开 `live_ref` 和 `attribution`。两个宣告同一提供商会话 id 的
home 是两行、两条时间线。用 `GET /v1/m/sessions/live/by-id/{live_ref}`
（及其 timeline / stream / runs 查询）读取一行。裸外部 id 路由仍在，且是
**legacy**：只回答 legacy 行。

驱动程序在每个节点启动时注册。启动会话时，引擎先使用最新的已验证托管安装，
再查找自身 `PATH` 中的 CLI。`OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`、`_CODEX_BIN`、
`_GROK_BIN` 和 `_OPENCODE_BIN` 显式覆盖此查找过程（见 [配置](/reference/configuration/)）。
找不到可执行文件时，引擎会拒绝启动。配置文件认证、启动策略等其他就绪检查仍然适用。
操作步骤：[运行提供商会话](/how-to/operate-provider-sessions/)。

## 受管运行的生命周期与重启边界

Sessions 是内核模块：即使它不在已保存的模块选择中，也会继续运行。关闭可选模块不会重置会话数据。

`/v1/m/sessions/runs` 下的受管运行 API 与 `olivares session` 使用同一个运行时。读取运行需要
`sessions:run:read`；输出需要 editor 及以上角色默认拥有的 `sessions:output:read`。启动、输入和 resume
需要 `sessions:run:write`，停止需要 `sessions:stop:write`。viewer 保留列表和元数据，但未经明确
授权将失去对会话内容的访问权限。租户和工作区权限仍适用于每个运行。

```sh
olivares session ls -o json
olivares session show <session> -o json
olivares session send <session> "continue the task"
olivares session stop <session>
olivares session resume <session>
```

一条消息可以携带图片。`olivares session send <session> --attach shot.png "what is wrong here?"`
以及控制台编辑器（粘贴、拖放或回形针）会把每个 PNG、JPEG、WebP 或 GIF 与会话一起存储，
每条消息最多四张，每张最大 3.75 MB，然后与文本一起发送；消息必须有文本，所以只有图片不会被发送
（CLI 和编辑器会要求补充文本）。Claude Code 接收图片内容块，Codex 接收它自己的图片输入；
没有图片输入的工具会在任何内容发出之前按名称拒绝该消息。API 是
`POST /v1/m/sessions/runs/{ref}/attachments`（图片，base64）、`GET .../attachments/{id}`，
以及 `.../input` 上的 `attachments`。图片位于数据目录下，按租户和会话存放，并随会话一起移除。

已停止的运行、其提供商配置文件和工作区引用以及生命周期事件在引擎重启后仍然存在。
Resume 会为现有运行启动一个新的自有子进程。实时 attach 输出是内存中的有界环；保留的运行元数据和
生命周期事件不承诺持久化的对话记录。录制策略与工具自身的 resume 支持仍是独立的要求。

## NFS 或 SMB 共享上的会话文件夹

会话文件夹是引擎主机上的一条路径，因此网络共享一旦被主机挂载，就成为一个文件夹。`olivares netfolder add` 使用 systemd 自身的挂载单元完成挂载，并将挂载点登记为一个文件夹；挂载仍由 systemd 负责，而这些单元是可以阅读和编辑的普通文件。请在引擎主机上以 root 身份运行：

```sh
printf '%s\n' "$SMB_PASSWORD" | sudo olivares netfolder add files //fileserver/projects --user svc --password-stdin
sudo olivares netfolder add data fileserver:/export --mode write_no_delete
olivares netfolder ls
```

来源对于 SMB 是 `//server/share`，对于 NFS 是 `server:/export`，与 `mount(8)` 的写法一致。名称由小写字母和数字组成，并将挂载点命名为 `/srv/olivares/mnt/<name>`。`--mode` 是该文件夹的访问模式（默认为 `rw`）。这些单元将 SMB 文件夹所显示的每个文件的所有者账户设为 `olivares`，因此引擎必须以该账户运行。`ls` 会将每个文件夹显示为 `mounted`、`unmounted`（首次使用时挂载，`ls` 不会触发挂载）或 `not responding`。已存在的文件夹永远不会被覆盖。

对于上面的 SMB 示例，该命令会写入以下单元。这些数据是在 Debian 13（Linux 6.12）上，针对 NFSv4.2 导出和 Samba 4.22 共享（SMB 3.1.1）测得的。文件名是 `systemd-escape -p /srv/olivares/mnt/files` 的输出加上 `.mount` 和 `.automount`：

```ini
# /etc/systemd/system/srv-olivares-mnt-files.mount
[Unit]
Description=Olivares network folder files
After=network-online.target
Wants=network-online.target

[Mount]
What=//fileserver/projects
Where=/srv/olivares/mnt/files
Type=cifs
Options=credentials=/etc/olivares/netfolders/files.cred,uid=olivares,gid=olivares,vers=3.1.1,nodev,nosuid,soft,echo_interval=5
TimeoutSec=30
```

```ini
# /etc/systemd/system/srv-olivares-mnt-files.automount
[Unit]
Description=Olivares network folder files (mounted on first use)

[Automount]
Where=/srv/olivares/mnt/files
TimeoutIdleSec=0

[Install]
WantedBy=remote-fs.target
```

对于 NFS，它会写入 `Type=nfs4`、`What=fileserver:/export` 以及 `Options=nodev,nosuid,vers=4.2,soft,timeo=50,retrans=2`，并且不写入凭据文件。在强制 SELinux 且使用该 appliance 策略的主机上，它会在选项中添加 `context=system_u:object_r:olivares_workspace_t:s0`，这是引擎及其会话工具为文件夹已使用的标签。要删除一个文件夹，请对其两个单元运行 `systemctl disable --now`，删除它们及其凭据文件，并注销该文件夹。

- **Everyone who has the folder acts as one account.** 文件由挂载所呈现的账户读写：SMB 为 `uid=` 和 `gid=`，NFS 为导出的用户映射。共享本身无法区分不同的人。由 Olivares 决定谁能获得该文件夹。
- **个人专属文件夹以该本人的身份读取。** `--owner <用户 ID>` 像个人文件夹一样，把一个 SMB 文件夹注册为某个人专属：其内容只属于本人，管理员仍可找到该文件夹来管理它。用 `--user` 和密码提供该本人自己的 SMB 登录，文件服务器就会应用他们的权限：挂载同一共享的两个人，各自只看到自己的账户能打开的内容。每个人添加一个文件夹，各用各的名称。NFS 无法区分不同的人，因此不接受所有者。管理员只输入一次该人的密码，它与共享账户的密码一样保存在仅 root 可读的凭据文件中；该人的会话从不持有它。按会话提供 Kerberos 票据、让本人无需把密码交给任何人，目前尚未实现。已用 Samba AD 域的两个账户在同一共享上验证：每个人的文件夹列出了自己的目录和公共目录，读取了自己的文件，而文件服务器拒绝了对方的文件。
- **The credential stays in a file.** 命令将 `username=` 和 `password=` 写入 `credentials=` 文件，该文件归 root 所有，权限为 0600，密码来自标准输入。密码不出现在任何单元、挂载选项、进程参数或日志行中：`mount.cifs` 只接收路径。使用 `sec=sys` 的 NFS 不需要凭据。
- **The automount unit keeps boot independent of the file server.** 启动时不会等待共享。首次使用时挂载；若那一刻文件服务器宕机，则是该次使用失败，而不是启动失败。
- **Use `soft`.** 默认的 `hard` NFS 挂载会使访问已失效服务器的进程卡住，无法停止。使用上述选项后，对已挂载共享的读写会在 20 到 30 秒后以 input/output error 或 "Host is down" 失败，服务器重新响应后共享即恢复工作。
- **A folder that does not respond is reported.** 登记、打开、列出文件夹以及在其中启动会话，都会等待共享 10 秒，然后以 "The folder ... is not responding. If it is on a network share, check that its file server is up and reachable." 拒绝并返回 424 状态。会话不会启动。
- **`write_no_delete` holds.** Landlock 在内核的文件系统层执行该限制，位于网络文件系统之上。在两种共享上的实际会话启动中，会话创建并修改了文件，且被拒绝重命名、删除（unlink）和删除目录（rmdir），与本地磁盘上完全一致，而 `rw` 则允许这些操作。无法实施限制的主机仍会拒绝启动。
- **The folder is the boundary.** 在同样的启动中，两种模式下，对文件夹父目录（位于同一共享上）的写入都被拒绝，且不留下任何文件。会话只会写入其文件夹内部。

## 它消费什么（以及它派生什么）

模块 II 消费与清点相同的最小数据观测流——[`edge.observed`](/zh/reference/events/)、
`cost.sampled` 和 `finding.reported`。只有发起方为**会话**的边才产生实时运行；与某会话关联的
成本采样会累加到实时 token/成本数字（此处不写入任何 `CostRecord`）；以会话为主体的发现项会被
标注，而一个反规避发现项会标记规避状态。有两个字段是从这些相同信号中**实时派生**的：`agent_ref`
派生自某会话的归属 agent，而 `summary` 派生自一个上下文压实（取证性）发现项，其标题按契约是
摘要安全的——从不是 LLM 编造的摘要。

:::caution[诚实的限制]

- **`goal` 保持为空——如实地。** 协作式流是最小数据的，**不**携带会话的目标或任务列表；
  它们在连接器处被脱敏，传输中也没有任何进程内提示词文本。实时记录对该字段建模，是为了让契约和
  UI 就绪，以便任何未来的元数据通道都能填充它，但模块**从不发明它**。
- **没有存储的生命周期。** 该流没有结束/失败信号，因此一个会话的存活性是按新近度**派生**的
  `cc_state`——而非一个被持久化的状态。`ended` 状态意味着*近期无事件*，而非一次已确认的干净关停。
- **实时数字不是账本。** 实时 token/成本是来自成本采样的运行读数；权威的、可对账的成本记录是
  模块 XI 的 FinOps 账本。不要把实时数字当作计费真相。
- **最小数据是传输协议（wire）的特性。** 只有引用、分类和存活性/成本计数器被承载并持久化——
  从不承载载荷、提示词、命令或 PII。
:::

## 相关

- [事件总线参考](/zh/reference/events/)——本模块消费的 `edge.observed`、`cost.sampled` 和
  `finding.reported` 事件。
- [模块目录](/zh/reference/modules/overview/)——模块 II 所处的位置以及诚实的执行（actuate）划分。
- [访问与资源映射](/zh/reference/modules/iii-access-map/)——拥有 R/RW 访问图的同级 Core 模块。
- [架构概览](/zh/explanation/architecture/overview/)——引擎与各层。
- [连接 Claude Code](/zh/how-to/connect-claude-code/)——开始产生实时流。
- [诚实与限制](/zh/start/honesty-and-limits/)——本产品当下做什么、不做什么。
