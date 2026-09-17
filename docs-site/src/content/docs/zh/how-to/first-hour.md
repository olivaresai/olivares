---
title: "使用 Olivares AI 的第一个小时（v26.9.0，按实际交付状态）"
description: >-
  全新安装公开版 v26.9.0 二进制文件后，第一个小时内实际可以完成的操作：
  安装令牌、AAL3 屏障、通行密钥注册、通过提供商配置文件启动会话、部署、
  知识库，以及 Codex 和 Grok 的 PEP hook。
---

本页描述的是 **实际交付状态下的 v26.9.0**。它不是计划中的首次运行向导，也不是
设计稿的截图。以下每个步骤都是公开版二进制文件如今确实能够执行的操作，并注明了
使其成立的文件或环境变量。在产品拒绝执行某项操作的地方，本页会如实说明。

这些编号事实于 2026-09-04 在全新安装的公开版二进制文件上测得。本页引用了这些
测量结果，以及执行路径所到达的代码。

如何将二进制文件安装到主机，请参阅
[自行托管 Olivares AI](/how-to/self-hosting/) 和
[验证发行版](/how-to/verify-a-release/)。本页不会让你把
`https://olivares.ai/install` 通过管道传给 shell。二进制文件安装到主机后，推荐的
第一个命令是 `olivares quickstart`。

:::note[这不是什么]
`--seed-demo` 不是产品导览。2026-09-04 在全新安装的公开版二进制文件上测得：使用
种子数据启动后，控制台 **54 条路由中仍有 36 条**为空。该数字是测量日的普查。
本树生成的 [控制台参考](/reference/console/) 列出 **75 条路由**。本页不对
v26.9.0 上 `--seed-demo` 之后的空选项卡重新计数。演示环境会填充
[从零开始构建读写访问图](/tutorials/zero-to-graph/) 中的访问图流程，但不会填充控制台
的其余部分。不要用它来「探索产品」。
:::

## 1. 推荐的启动方式：`olivares quickstart`

新的数据目录 **没有默认凭据**。推荐的第一个命令是 `olivares quickstart`
（`cmd/olivares/cmd_quickstart.go`）。它相当于采用安全默认值的 `serve`：启用 TLS、
仅监听 loopback、没有默认凭据。默认监听地址是 `127.0.0.1:8443`（`:61`）。
2026-09-04 在全新安装的公开版二进制文件上使用真实 TLS 测得（包括一次在
**:8460** 上的运行）；面板文本相同。

欢迎面板（`announceQuickstart`，`:154-163`）带有编号。引擎会打印
`127.0.0.1`。**不要使用这个主机地址完成通行密钥仪式。** 浏览器会拒绝将 IP 地址
用作 WebAuthn RP ID（`SecurityError`）。产品从请求的主机名派生 RP ID
（`core/api/handlers_webauthn.go:33-50`）。注册通行密钥前，请通过
`https://localhost:PORT`（或真实主机名）打开控制台，而不要使用 `127.0.0.1`。
除非传入了 `--listen`，否则 `PORT` 是 `8443`。

```text
=== WELCOME TO OLIVARES AI ===
  1. Open:   https://localhost:8443
     (HTTPS with a self-signed certificate on first boot — your browser will
      warn once; that is expected for a local install.)
  2. Complete setup with this one-time token (shown once, single-use):

         olst_…
```

打印出的横幅仍然显示 `https://127.0.0.1:8443`。请在地址栏中将主机地址改为
`localhost`。

令牌前缀是 `olst_`（`cmd/olivares/e2e_binary_test.go` 匹配
`olst_[A-Z0-9]+`）。控制台页面是 `/setup`。向导提交到的 API 是：

```http
POST /v1/setup
Content-Type: application/json

{"token":"olst_…","email":"you@example.com","password":"…"}
```

`olivares serve` 会打印类似的横幅（`cmd/olivares/cmd_serve.go` 中的
`announceSetup`）。第一个小时请使用 `quickstart`：URL、自签名证书警告和一次性令牌
都在同一个面板中。然后使用 `POST /v1/auth/login` 登录。此时你拥有一个 AAL1 密码
会话。

`README.md` 和该欢迎面板会指出令牌与 URL，但 **不会**提及通行密钥注册。该步骤在
接下来按下按钮后由控制台给出。

## 2. AAL3 屏障——控制台在你按下按钮后才引导，而不是事先引导

安装完成后，**在会话达到 AAL3 之前，创建数据源、连接器、工作空间和密钥的操作都会
被拒绝**。该屏障是 `core/api/middleware.go:310` 中的 `requireAAL3`。低于 AAL3
的 principal 会收到 `403
step_up_required`。共有 **21 个调用点**经过该屏障。本代码树
中的写入路径包括：

| 操作面 | Handler | 文件 |
|---|---|---|
| 添加/删除/重新加载数据源清单 | `handlePutSource` / `handleDeleteSource` / `handleReloadRuntime` | `core/api/handlers_sources.go` |
| 连接器写入和测试 | `handlers_connectors.go` | `core/api/handlers_connectors.go` |
| 添加/删除密钥 | `handlePutSecret` / `handleDeleteSecret` | `core/api/handlers_secrets.go` |
| 创建/更新工作空间 | `handleCreateWorkspace` / `handleUpdateWorkspace` | `core/api/handlers_scoping.go:152` / `:199` |
| 成员加入 | `handleOnboardMember` | `core/api/handlers_onboarding.go:59` |

**在标准安装中，PIV/CAC 无法让你达到该级别。** 未配置时，PIV 路由会返回 **501**
`piv_not_configured`（`core/api/handlers_piv.go`、`core/api/errors.go`）。

### 控制台实际会做什么

控制台 **确实会**引导你进行注册，但它是 **被动响应**的。

1. 你尝试执行特权操作。升级认证面板显示 **使用安全密钥认证**
   （`web/src/features/identity/i18n/en.json` 中的 `assurance.authenticate`；
   按钮位于 `web/src/features/identity/assurance.tsx:230-239`）。
2. 点击后会调用 `POST /v1/auth/webauthn/authenticate/options`（升级认证，而不是注册）。
   在没有通行密钥时，引擎返回 **400** `no_webauthn_credential`
   （`web/src/features/identity/api.ts:260-265` 中的 `isNoWebAuthnCredential`）。
3. 随后，面板会让你先在 **特权登录** 选项卡中注册
   （`assurance.tsx:160-168` → `assurance.unenrolled`）。这句话 **不是链接**。

你需要手动前往：

1. 通过 **`https://localhost:PORT`** 打开控制台，而不是 `127.0.0.1`
   （参见 §1）。
2. 打开 `/identity`（`web/src/features/registry.tsx` —
   `path: '/identity'`）。
3. 选择 **特权登录** 选项卡（`tabs.login`）。
4. 选择 **注册通行密钥**（`passkeys.register`）。使用 **平台认证器即可**
   （浏览器或操作系统的提示，无需实体密钥）。服务器 **要求用户验证**
   （`core/auth/webauthn.go:74-85`，`UserVerification: VerificationRequired`）。
   2026-09-04 在全新安装的公开版二进制文件上完成整个 WebAuthn 仪式后的测量结果：
   注册 **200**，认证 `{"aal":3}`，随后 `PUT /v1/console/connectors` 返回 **200**。

`POST /v1/auth/webauthn/register/options` 以 **会话 principal** 身份通过认证，且 **不会**
调用 `requireAAL3`。成功时，它会写入带 `{publicKey: …}` 的 **200** 响应
（`core/api/handlers_webauthn.go:76-88`）。使用
`POST /v1/auth/webauthn/register` 完成仪式，然后重试升级认证。

`README.md` 和 `olivares quickstart` 欢迎面板 **不会**提及特权登录选项卡。控制台只在
该 400 响应 **之后**才会指出它。

身份面板将 AAL3（NIST SP 800-63B-4）和 PIV/CAC（FIPS 201-3）称为
**目标标准**，并声明它 **不声称获得任何认证**（`targetStandardsNote`）。本页也不声称
获得任何认证。

### 添加连接器：只有屏障，没有字段

**添加连接器**（`web/src/features/console/i18n/en.json` 中的
`connectors.add`）会打开一个对 `ConnectorForm` 应用
`<RequireAssurance minAal={AAL.HARDWARE}>` 的对话框
（`web/src/features/console/connectors-tab.tsx:348-357`）。低于 AAL3 时，该表单不会
挂载。2026-09-04 在全新安装的公开版二进制文件上测得：该对话框 **没有输入字段**
（`inputs: []`），只有升级认证面板。查看类型目录只需 AAL1（`ConnectorCatalog` 位于
屏障外，同一文件的 `:341-346`）；但 AAL1 **不能添加**连接器。

### `/workspace` 与协议绑定：没有工作空间切换器

`/workspace`（`registry.tsx` 中的 `path: '/workspace'`）和
`/communications/protocol-bindings` 需要工作空间。在全新安装中，你没有任何工作空间，
而且在达到 AAL3 之前无法创建（`handleCreateWorkspace`）。当工作空间不超过一个时，
`WorkspaceSwitcher` **不会渲染**
（`web/src/components/layout/workspace-switcher.tsx:43-44`：
`if (workspaces.length <= 1) return null`）。顶部栏保留的是 **切换组织**
（`web/src/lib/i18n/locales/en/auth.json` 中的 `tenant.switch`）。没有可供点击的工作空间
选择器。

## 3. 从控制台启动 Claude Code 会话

只有当 **主机**具备推理凭据来源时，控制台才能启动 `claude` 进程。若没有凭据来源，
stream-json 启动会按 deny-closed（默认拒绝）原则被拒绝。2026-09-04 在全新安装的公开版
二进制文件上测得：**HTTP 503**。

请设置以下选项中的 **一个**：

- `OLIVARES_SESSION_RUNTIME_WIF` — 进程内 WIF 签发（`cmd/olivares/sessionruntime.go`、`cmd/olivares/wifbroker.go`）
- `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` — 指向轮换后的短期令牌文件的路径

composition root 会记录已接入的来源；若没有，则记录：

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

（`cmd/olivares/sessionruntime.go:74-76`）。可选的相关变量：
`OLIVARES_SESSION_RUNTIME_WIF_RULE`、`OLIVARES_SESSION_RUNTIME_TOKEN_TTL`、
`OLIVARES_SESSION_RUNTIME_BASE_URL`、`OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`
（列于 `cmd/olivares/config_registry.go` 和
[配置](/reference/configuration/)）。

**Operate** 的共同部署方式（与 `claude` 位于同一主机）参见
[通过 Olivares 运行 Claude Code](/how-to/run-claude-code-with-olivares/)。OTLP observe 路径
参见 [连接 Claude Code](/how-to/connect-claude-code/)。

### Provider key 并不是启动会话所需的凭据

**模型 → Provider key** 是一个管理 **引用** 的治理注册表。该表单 **从不接受密钥**：

> 此表单从不接受密钥。Olivares 仅存储一个引用和一个
> 掩码提示。

（`web/src/features/models/i18n/en.json` 中的 `keys.dialog.noSecretNote`）。填写该选项卡既不能
满足 `OLIVARES_SESSION_RUNTIME_WIF`，也不能满足
`OLIVARES_SESSION_RUNTIME_TOKEN_FILE`。它不会启用会话启动。

## 4. 在预置 executor 之前，部署计划会返回 503

部署模块的 plan/apply `POST` 会返回 **503**，直到主机将
`OLIVARES_DEPLOY_EXECUTOR_CONFIG` 设置为一个 JSON 文件。缺少该变量时，模块会保留
未接入的 deny-closed executor（`cmd/olivares/deployexec_load.go:16-20`）。无法读取的文件
会使 **启动失败**。

JSON 对象是 `cmd/olivares/deployexec_load.go:26-42` 中的
`deployExecutorConfig`。可选的 backend block：`tofu`、`terraform`、`gitops`、`k8s`、
`docker`、`nomad`、`crossplane`，以及 `credential`、`blast_radius`、
`identity_binding`、`drift`。

该环境变量记录在 [配置](/reference/configuration/) 中
（`docs-site/src/content/docs/reference/configuration.md:152`）。它没有被描述为第一个小时
内的控制台点击操作。UI 中没有可以替代该文件的选项。

模块目录将 Deployment actuate 标为 **on-demand (503)**
（[模块](/reference/modules/overview/)）。这一行表达的是同一个事实。

## 5. 查询公开知识库，或使用代理身份进行查询

默认 embedder 是零外传的 **LocalHashEmbedder**。启动时会警告检索是 **词法检索，而非
语义检索**，并显示 `embed_model=local-hash`（`cmd/olivares/claude_inference.go`、
`cmd/olivares/knowledgestatus.go`）。只要 guard 允许，词法检索仍会返回片段。

如果没有经过认证的代理身份，retrieval guard 只允许访问 **公开且不受限制**的内容
（`modules/knowledge/query.go:120-127`）。人类通过 REST `/query` 查询 **内部**知识库时
会被拒绝。2026-09-04 在全新安装的公开版二进制文件上测得：一个 **公开**知识库返回了
**1 个结果**（得分 0.738）。请查询公开知识库，或使用代理身份进行查询。

目前，即使所有内容都被排除，被拒绝的查询仍会报告 `excluded_chunks: 0`。该计数器只会
因运维人员设定的 `excluded_sources` 下限而递增（`query.go:256-284`）；clearance 或
ACL 拒绝永远不会使它递增。

## 6. Codex 与 Grok 会话：提供商配置文件，然后才是其余 CLI hook

v26.9.0 将官方 Codex CLI 和官方 Grok CLI 作为会话驱动程序运行，外加 Claude Code
（`CHANGELOG.md` `[26.9.0]` Added）。控制台在 **Provider profiles**
（`/provider-profiles`，`sessions:profile:read`）和 **Source bindings**
（`/provider-bindings`，`sessions:profile-binding:read`）上管理这些启动。两条路由
都在生成的 [控制台参考](/reference/console/) 中。

配置文件是一个执行环境上一个已配置提供商实例的持久身份。它不是已认证的提供商
账户（`web/src/features/agentops/types.ts`）。登记会验证本节点上已经存在的主目录。
服务器不安装、不创建、不登录。

### 启动前在主机上注册驱动程序

就绪按驱动程序分别计算。没有共享开关（`cmd/olivares/sessionruntime.go`）。
设置对应环境变量即在本节点注册该驱动程序：

| 驱动程序 | 环境变量 | 未设置时 |
|---|---|---|
| Claude Code | `OLIVARES_SESSION_RUNTIME_CLAUDE_BIN`（默认 `claude`） | Claude 路径使用默认名称 |
| Codex | `OLIVARES_SESSION_RUNTIME_CODEX_BIN` | Codex 配置文件仍可观察，但不能启动 |
| Grok | `OLIVARES_SESSION_RUNTIME_GROK_BIN` | Grok 配置文件仍可观察，但不能启动 |

值是固定的官方二进制文件。引擎不会从 `PATH` 解析 `codex` 或 `grok`。来源：
[配置](/reference/configuration/)。

Claude 启动仍需要 §3 中的推理凭证来源。Codex 和 Grok 使用配置文件的 AUTHORIZED
`auth_source`（`provider_account_home` 或 `managed_injection`，无回退）。
`CHANGELOG.md` `[26.9.0]` **不断言**与已认证官方 Grok 账户的兼容性。

启动对话框要求提供商配置文件。生产创建端点要求 `provider_profile_ref`。省略它会
保留旧请求体，本 API 会拒绝（[CLI](/reference/cli/)
`olivares agent session create --provider-profile`）。

如何登记配置文件、绑定源、启动、中断和停止：
[运行提供商会话](/how-to/operate-provider-sessions/)。

### 仍仅限 CLI 的部分（hook 与受管配置）

这些命令不是控制台会话启动。它们仍然存在：

| 命令 | 功能 | 源文件 |
|---|---|---|
| `olivares codex` | 根据 Policy JSON 生成 Codex `requirements.toml` / `managed_config.toml`。**它会写入文件；不会与控制平面通信。** | `cmd/olivares/cmd_codexmanagedconfig.go` |
| `olivares codex-hook` | Codex 调用的 deny-closed PEP hook（stdin → 控制平面 → 按事件结构输出到 stdout）。 | `cmd/olivares/cmd_codexhook.go` |
| `olivares grok-hook` | Grok Build 调用的 deny-closed PEP hook。deny 只有在 `pre_tool_use` 时才会 **阻止**操作。 | `cmd/olivares/cmd_grokhook.go` |

安装 Codex hook 时（已根据该文件注释中的 Codex `hooks.json` 结构验证），`command` 必须
是一个 **字符串**：`olivares codex-hook`。环境变量：
`OLIVARES_CODEX_HOOK_URL`、`OLIVARES_CODEX_HOOK_TOKEN`、
`OLIVARES_CODEX_HOOK_TENANT`（以及可选的 agent/org/account）。

Grok hook 环境变量：`OLIVARES_GROK_HOOK_URL`、`OLIVARES_GROK_HOOK_TOKEN`、
`OLIVARES_GROK_HOOK_TENANT`。Grok 可以通过 `~/.grok/disabled-hooks` 按名称禁用 hook；
这不是控制台控制项。

不要寻找「连接 Codex」或「连接 Grok」**按钮**。连接器登记是
**Control console → Connectors**（类型 `codex` 或 `grok`）。那是
[集成 Codex](/how-to/integrations/codex/) 与
[集成 Grok Build](/how-to/integrations/grok/) 中的观察/治理平面。它不注册会话
驱动程序。

## 7. `--seed-demo` 不会填充整个控制台

`olivares serve --seed-demo` 会载入演示环境，以便运行访问图教程。2026-09-04 在全新
安装的公开版二进制文件上测得：该环境中，控制台 **54 个页面中仍有 36 个**为空。该数字
是测量日的普查；生成的控制台参考今天列出 **75 条路由**。`--seed-demo` 只
应用于 [从零开始构建读写访问图](/tutorials/zero-to-graph/) 所述的流程。不要把
`--seed-demo` 后的空选项卡视为安装损坏，也不要把该标志当作产品导览。

## 相关页面

- [诚实声明与限制](/start/honesty-and-limits/) — 文档可以作出的声明。
- [自行托管 Olivares AI](/how-to/self-hosting/) — 二进制文件的运行方式。
- [运行提供商会话](/how-to/operate-provider-sessions/) — 配置文件、驱动程序固定、启动、中断。
- [配置](/reference/configuration/) — 上述环境变量。
- [模块](/reference/modules/overview/) — on-demand (503) 与已启用 actuation 的区别。
