---
title: 运营提供商会话
description: >-
  选择 AI 工具及其账户或提供商，在项目文件夹中工作，批准操作，中断一个回合，并从
  控制台或 CLI 停止与恢复。
---

请先完成[Olivares AI 的第一个小时](/how-to/first-hour/)，再开始本页。
工具运行在引擎所在的主机上；对于 Compose 安装，则运行在容器内部。你客户端终端中的
登录信息和文件夹不会自动在那里可用。

## 选择工具如何连接

**AI 工具**（`/agent-tools`）用于安装工具并运行其自身的登录流程。在原生引擎上，
默认工具实例使用引擎用户自己的登录；通过 Olivares 登录的账户保持为独立实例。选择你
想使用的账户。账户快照会显示工具报告的邮箱、套餐、用量窗口和模型。缺失的值保持
不可用，刷新失败的会标记为过期。

**工具**（`/agent-tools`）中的 **API 密钥** 保存 API 密钥和本地模型端点。请在那里添加
并测试提供商，然后从 **新建会话** 中使用它。提供商密钥与工具订阅是两种不同的连接方式。
Ollama 需要一个可访问的端点和一个模型，无需 API 密钥。参见
[添加提供商](/how-to/add-a-provider/)。

对于指定了 **未** 绑定提供商的 `managed_injection` Claude 配置文件，
`OLIVARES_SESSION_RUNTIME_WIF` 或 `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` 提供主机范围的
推理凭据。绑定到提供商记录的配置文件则使用该记录；若其凭据无法打开，则拒绝启动，
而不会回退到主机凭据。`provider_account_home` 配置文件使用其已授权的工具登录，两个
变量都不需要。参见
[环境变量及其适用范围](/how-to/add-a-provider/#the-environment-variables-and-where-they-still-apply)。

## 开始并对话

打开 **会话 → 新建会话**，并选择一个就绪的工具。引擎会填好配置文件和文件夹。对于
README 的 Compose 安装，请选择 **更改文件夹** 并输入 `/project`，以便在挂载的宿主机
项目中工作。保留默认文件夹则无需输入路径。将 **第一条消息** 留空即可开始，然后在
对话中输入消息，或在按下 **启动** 之前输入。

使用提供商密钥的工具会使用其已保存的模型，或让你选择连接测试返回的模型。如果必须有
模型但测试尚未返回任何模型，请先在 **工具** 的 **API 密钥** 下测试连接。Claude Code
可以使用其原生默认模型。

对于 README 的 Compose 安装，请在引擎容器内运行 CLI。它会在那里找到本地引擎及其证书。
请使用你在设置期间创建的 Olivares 管理员账户登录，而不是你的工具订阅。

当使用提供商密钥且工具需要模型（例如 Codex）时，请在运行下面的 `session start` 之前，
先在 **工具** 的 **API 密钥** 下保存默认模型。如果提供商和工具配置文件都没有配置模型，
请在 `session start` 命令后追加 `--model <tested-model-id>`，并将占位符替换为连接测试
返回的模型 ID。在 **新建会话** 中选择的模型仅适用于该次控制台启动；它不会为 CLI 启动
设置模型。

在宿主机终端中设置一次 Compose 文件：

```sh
export COMPOSE_FILE="$HOME/olivares/deploy/compose/docker-compose.yml"
docker compose exec olivares olivares login
docker compose exec olivares olivares tool ls
docker compose exec olivares olivares tool providers
docker compose exec olivares olivares session start /project --name first-hour
docker compose exec olivares olivares session send first-hour "Explain this project."
```

对于原生安装，请直接在引擎主机上运行相同的 Olivares 命令，并将 `/project` 替换为你的
项目路径。客户端会打印它所选择的工具和连接。若有多个工具，请使用 `--tool` 选择其一；
`--profile` 用于选择已有的配置文件。关闭客户端终端不会停止引擎上的会话。要重新连接
其输出，请运行：

```sh
docker compose exec olivares olivares session follow first-hour
```

## 批准、中断、停止与恢复

在 **更多选项** 下，**每次操作前询问** 会让工具在行动前先询问。在 **规则** 中，等待你
处理的请求会在队列旁边打开，并显示其命令、将访问的范围以及触发它的规则。选择
**仅批准一次**（`A`），或选择 **拒绝**（`D`）并添加备注。需要组织批准的启动也会在此
等待；请待启动获得批准后再发送消息。

使用 **中断** 结束当前回合，同时保持对话打开。使用 **停止** 结束进程，如有提示请确认。
**恢复** 会在同一配置文件、账户和文件夹上重新启动该会话；它会重新检查凭据和策略。
恢复后请发送新消息。

```sh
docker compose exec olivares olivares session interrupt first-hour
docker compose exec olivares olivares session stop first-hour
docker compose exec olivares olivares session resume first-hour
docker compose exec olivares olivares session send first-hour "Continue explaining this project."
```

账户变更或不可用会导致启动被拒绝，而不是许可切换到另一个登录。被中断的 Grok 回合使用
ACP 取消，且没有确认；在将其视为已结束之前，请等待相关的回合结果返回。

## 可选的配置文件与 git 设置

对于已配置的现有工具主目录，**会话**（`/sessions?tab=profiles`）的 **提供商配置文件**
标签页允许管理员登记配置文件，并检查启动就绪状态。已命名账户的配置文件和来源绑定也
位于 **工具** 中该账户的详情里。主目录必须存在于执行主机上。在新建会话中使用
**更多选项 → 高级启动选项** 来选择该配置文件。二进制覆盖项用于选择一个可执行文件；
否则，引擎会先尝试最新的已验证托管安装，然后尝试 `PATH` 中的工具。参见
[配置](/reference/configuration/)。

对于至少有一个提交的 git 仓库顶层文件夹，**在新的 git 工作树中工作** 会为会话创建一个
分支和工作目录。恢复会复用这些内容。带有分支和提交的交接会提供 **在新的会话工作树中
打开**；请选择一个仓库包含该提交的已登记文件夹。引擎会在创建工作树之前，拒绝不支持
或不安全的仓库设置。

文件夹可以为新工作树存储一条设置命令。在接受工作树启动之前，必须由人按 SHA-256 批准
其精确文本；更改一个字符就需要重新批准。目前尚未运行该命令。参见
[批准文件夹的设置命令](/reference/session-runtime-api/#approving-a-folders-setup-command)。

清理操作会拒绝丢弃未提交或未合并的工作。**同时删除工作树和分支** 会明确授权这种丢失；
被忽略的文件会随工作树一并删除。工作树共享该仓库的 git 元数据。工作树选项参见
[会话 CLI 参考](/reference/cli/#command-olivares-session-start)，配置文件权限、生命周期
调用和隔离限制参见[会话运行时 API](/reference/session-runtime-api/)。
