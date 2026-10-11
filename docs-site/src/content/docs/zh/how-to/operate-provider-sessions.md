---
title: 运行提供商会话
description: >-
  为该节点上已有的 Claude、Codex 或 Grok 主目录注册提供商配置文件，固定官方
  驱动程序二进制文件，从控制台或 CLI 启动受治理会话，然后中断或停止回合，
  而不虚构运行器。
---

本页是官方提供商 CLI 的 **运行** 路径。控制平面在 **提供商配置文件** 下启动
它所拥有的子进程。它不安装提供商、不创建其主目录，也不启动交互式浏览器登录。

这不是连接器 / hook 路径。要清点或治理 Grok Build 或 Codex 的配置文件，请使用
[集成 Grok Build](/how-to/integrations/grok/) 或
[集成 Codex](/how-to/integrations/codex/)。要在同一主机上与 Claude Code
共存，请使用
[在 Olivares 中运行 Claude Code](/how-to/run-claude-code-with-olivares/)。

该行为的来源：`CHANGELOG.md` 的 `[26.9.0]`（提供商配置文件、Codex
驱动程序、Grok 驱动程序）、生成的
[控制台](/reference/console/) 与
[配置](/reference/configuration/) 参考、`cmd/olivares/sessionruntime.go` 以及
`web/src/features/agentops/types.ts`。

## 前置条件

启动前完成这些项。缺一项就是拒绝，而不是回退。

1. 已安装 Olivares AI，并且已有首位管理员。
   安装令牌见 [第一个小时](/how-to/first-hour/)。
   管理操作的追加认证（`admin_step_up`）默认为 `none`。
   如果管理员启用 `totp` 或 `passkey`，请在特权操作前满足该策略的要求
   （`core/api/middleware.go` `requireStepUp`）。
2. 官方提供商 CLI 已安装在 **本节点**。配置文件登记的是已经存在的主目录。
   服务器解析路径（绝对路径、解析符号链接、已存在的目录），不创建、不安装、
   不登录（`web/src/features/agentops/types.ts` `CreateProfileRequest`）。
3. 打开 **Provider profiles**（`/provider-profiles`）需要
   `sessions:profile:read`，登记需要 `sessions:profile:write`。
   绑定源需要 `sessions:profile-binding:write` 以及源管理。启动运行需要
   `sessions:run:write`。权限见 [控制台参考](/reference/console/)。
4. 对应驱动程序的可执行文件必须位于 **此节点**。显式固定的二进制文件
   优先；未固定时，引擎在启动时查找。各驱动程序仍须通过就绪状态和
   策略检查。

| 驱动程序 | 固定此环境变量 | 未设置时 |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN` | 最新的已验证托管安装，然后是引擎 `PATH` 中的 `claude`。 |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | 最新的已验证托管安装，然后是引擎 `PATH` 中的 `codex`。 |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | 最新的已验证托管安装，然后是引擎 `PATH` 中的 `grok`。 |

该值固定此节点可运行的官方可执行文件。未固定时，安装工具后无需重启
引擎即可使用。如果既没有托管安装，也没有 `PATH` 中的可执行文件，
则拒绝启动。`OLIVARES_SESSION_RUNTIME_OPENCODE_BIN` 使用相同的查找顺序。

对于未指定提供方的 `managed_injection` Claude 配置档案，
`OLIVARES_SESSION_RUNTIME_WIF` 或 `OLIVARES_SESSION_RUNTIME_TOKEN_FILE`
提供主机的推理凭据。绑定提供方的配置档案使用该提供方的凭据；若无法读取，
则拒绝启动，不回退到主机凭据。`provider_account_home` 配置档案使用获授权的
工具登录，不需要这两个变量。参见
[添加提供方](/zh/how-to/add-a-provider/)。
Codex 和 Grok 只使用配置文件的 AUTHORIZED `auth_source`：
`provider_account_home` 或 `managed_injection`，二者之间没有回退，也没有默认值
（`CHANGELOG.md` `[26.9.0]`；`ProviderProfileDTO.auth_source`）。

:::caution[本页不断言的内容]
`CHANGELOG.md` `[26.9.0]` 写明，Grok 驱动程序行为是在自有的假 ACP 子进程上，
通过真实的 HTTP、运行时、存储和进程组证明的。
**与已认证官方 Grok 账户的兼容性是后续工作，此处不断言。**
:::

## 1. 登记提供商配置文件

提供商配置文件是 **一个** 执行环境上 **一个** 已配置提供商实例的持久身份：
驱动程序、所属环境，以及子进程使用的规范 `config_home` / `user_home`。
这是配置与存储身份，不是已认证的提供商账户（`CHANGELOG.md` `[26.9.0]` B1；
控制台文案 `agentops.profiles.subtitle`）。

### 控制台

1. 打开 **Provider profiles**（`/provider-profiles`）。
2. 选择 **Register profile**。
3. 设置驱动程序（`claude`、`codex` 或 `grok`）、已有的 `config_home` 和已有的
   `user_home`。`environment_ref` 可以省略（本节点）。
4. 保存。列表显示 `profile_ref`、驱动程序、状态，以及该配置文件是否在本环境
   启用。路径 **不** 出现在普通列表中。
5. 要查看已存储的主目录，使用 **Reveal configuration**
   （`sessions:profile:admin`）。该读取按需进行，隐藏时丢弃。仍然不携带凭证值。

重命名、禁用和启用保持同一 id 和同一主目录。**Retire** 不可逆，需键入确认，
并释放主目录给 **新的** id。

### 引擎拒绝的情况

- 本节点未注册其驱动程序的配置文件仍可见，但不能启动（`operable` 不是启动
  保证；`GET …/launch-readiness` 才是要求面板）。
- 属于另一执行环境的配置文件显示为外部，绝不会从本节点启动。
- 转发 `HOME`、`CLAUDE_CONFIG_DIR`、`CODEX_HOME` 或 `GROK_HOME` 的启动被拒绝。
  这些名称属于配置文件（`agentops.create.profileEnvConflict`）。

已发布的截图集中没有此屏幕。不要把 Connectors 选项卡的图片当作此表单。

## 2. 绑定源（可选，用于观察到的归属）

可以将源在本节点已应用的精确名册修订上专用于某个配置文件。键是该行的持久
id，绝不是可编辑名称（`CHANGELOG.md` `[26.9.0]` B1；**Source bindings**
`/provider-bindings`）。

1. 打开 **Source bindings**（`/provider-bindings`）。
2. 绑定源的持久 `id` 以及本节点协调器接好的 `applied_revision`。
   `GET /v1/console/sources` 报告二者。
3. 撤销绑定以停止 **新的** 配置文件归属。较早的信封在回放时保留其历史绑定。

没有绑定的已知登记仍显示为 `source` 观察行。它不会合并进受管运行。见
[实时运行与会话](/reference/modules/ii-sessions/)。

## 3. 启动

生产创建端点要求 `provider_profile_ref`。省略它会保留旧请求体，本 API 会拒绝
（`CHANGELOG.md` `[26.9.0]` B2；CLI 标志 `--provider-profile`）。

启动对话框提供 **active** 配置文件。只有一个 active 配置文件时会预选它；有多个时
不预选，**Start** 会提示选择一个。workspace 与 template 选择可以清除；配置文件
不可以（`CHANGELOG.md` `[26.9.0]` Fixed）。

### 控制台

1. 打开 **会话**（`/sessions`）。`/agentops` 打开同一屏幕
   （[控制台参考](/reference/console/)）。
2. 打开 **New session**，再打开 **Advanced launch options**（有工具就绪时位于
   **More options** 内）。
3. 确认 **Provider profile**（`agentops.create.profile`），需要时填写
   **First message**。
4. 可在 **Advanced options** 中设置 workspace、template、model 和 effort。
   对 Grok 而言，model 和 effort 在官方代理标志上仍是提供商拥有的开放字符串
   （`CHANGELOG.md` `[26.9.0]`）。
5. 点击 **Start**。无法启动时，按钮下方的一行会说明缺少什么。只发布配置文件的
   **引用**。服务器解析主目录。

### 打开交接所指明的工作

工作树隔离文件，但仓库的 git 元数据仍共享。交接内容可选地携带 `branch` 和 `sha`（完整提交 ID），指明工作在 git 中的位置。通过 API，或使用 JSON 可包含这两个字段的 `olivares message handoff offer --context-file` 提供交接。两者都未指定的交接保持原有行为。

阅读指定了这些字段的交接时，面板以文本显示分支和提交。**在新会话工作树中打开**会打开启动界面，选中工作树并显示起点。选择包含该提交的仓库所在的工作区之前，启动会等待。清除文件夹选择仍会保留该请求；如需普通会话，请明确取消工作树选项。命令行用法：

```sh
olivares session start . --worktree-from <commit or branch> --name review
```

`--worktree-from` 隐含 `--worktree`。会话在该提交处建立自己的新分支，因此发送方的分支和你的检出不会移动。如果工作区仓库中没有该提交，启动会在创建任何内容之前拒绝（422）；请先在该仓库 fetch。未被任何分支、标签或远程分支持有的提交也会被拒绝。会话的**分支变更**窗格列出其分支包含、而工作区当前提交不包含的工作，并将路径在合并基点与分支顶端的文本并排打开。只显示已提交工作，且遵守工作区允许的子路径和 DLP 策略；未提交编辑位于**变更**中。

### CLI

```sh
olivares agent session create --provider-profile <profile_ref>
```

按 [CLI 参考](/reference/cli/) 添加 `--server`、`--tenant` 和 `--token-file`
（或活动客户端上下文）。本发行版的 isolation 为 `native`；
`container` 和 `sandbox` 在创建运行记录之前返回 HTTP 422。
使用内置运行器时请选择 `native`。

结果：运行资源。受管实时行按观察范围和外部 id 唯一。指明某一行的读取使用
`live_ref`，而不是两个主目录可能共享的裸提供商会话 id。

## 4. 中断或停止

| 意图 | 控制台 | CLI | 结果 |
|---|---|---|---|
| 结束活动回合，保留进程和对话 | 实时会话上的 interrupt 控件 | `olivares agent session interrupt <run-ref>` | 回合结束；进程仍可用于下一回合（`CHANGELOG.md` `[26.9.0]`） |
| 结束运行 | stop 控件 | `olivares agent session stop <run-ref>` | 运行资源；运行时仍回收子进程 |

受工作约束的运行发送其确切租约围栏。过时或不确定的结果保持显式。恢复只在
同一已证明的主目录上继续。

Grok 中断使用 ACP `session/cancel`，这是没有确认的通知。中断会解决待处理
批准、取消，并让回合保持打开，直到该 prompt 自己的相关结果返回
（`CHANGELOG.md` `[26.9.0]`）。不要把静默取消当作已确认的提供商回执。

## 相关

- [第一个小时](/how-to/first-hour/) — 安装令牌、管理操作的追加认证、Claude 凭证来源。
- [在 Olivares 中运行 Claude Code](/how-to/run-claude-code-with-olivares/) — 共存拓扑。
- [集成 Codex](/how-to/integrations/codex/) / [集成 Grok Build](/how-to/integrations/grok/) — 连接器与 PEP hook。
- [会话运行时 API](/reference/session-runtime-api/) — 列表、attach、input、stop；Community PTY 与版本边界。
- [实时运行与会话](/reference/modules/ii-sessions/) — `live_ref` 与归属。
- [配置](/reference/configuration/) — 驱动程序固定变量。
- [CLI 参考](/reference/cli/) — `olivares agent session *`（从二进制生成）。
