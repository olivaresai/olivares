---
title: "Olivares AI 的第一个小时"
description: >-
  安装 Olivares AI，使用 AI 工具登录或连接提供商，在项目文件夹中开始对话，
  批准一项操作，并停止与恢复会话。
---

你需要 Docker Compose、一个希望工具处理的项目文件夹，以及以下三者之一：
工具订阅、提供商 API 密钥或本地模型。

## 1. 安装并打开控制台

请按照 [README 中的安装说明](https://github.com/olivaresai/olivares#install)操作。
它会显式选择发布镜像，将宿主机上的项目挂载到 `/project`，并打印一个控制台地址和
一个可替换的一次性设置令牌。如果你的 Docker 主机适用 AppArmor，请使用其中的说明。

打开打印出的地址。首次安装使用自签名证书；在接受浏览器警告之前，请确认你打开的是
自己的服务器。输入设置令牌、邮箱和密码，以创建第一个管理员。请妥善保管令牌和
密码。控制台会为你登录并打开设置向导。

容器在服务器上运行工具。你在笔记本电脑上登录的工具属于另一套安装；其登录信息不会
复制到容器中。

## 2. 选择工具及其登录

使用设置向导，或打开 **AI 工具**（`/agent-tools`）。每个工具会显示是否已安装以及
是否已登录。若工具未安装，请检查并批准其安装。使用该工具的登录控件，并完成其自身的
登录流程。对于 Claude Code 或 Codex，请使用你的 Claude 或 ChatGPT 账户批准登录。

在原生安装中，默认实例使用运行引擎的用户的登录。通过 Olivares 完成的登录保持为
独立实例。工具会报告其提供的账户、套餐、模型和用量窗口。不可用的值并不等于零余额；
刷新失败时，会显示上一次的快照并标记为过期，同时显示失败的命令。

若要改用 API 密钥，请打开 **工具**，在 **API 密钥** 下添加提供商，并测试连接。对于
本地模型，请添加一个引擎可以访问的 Ollama 端点；无需 API 密钥。参见
[添加提供商](/how-to/add-a-provider/)。对于需要模型的工具（例如 Codex），如果提供商
没有已保存的默认模型，请选择连接测试返回的某个模型。Claude Code 可以使用其原生默认
模型。

## 3. 在项目中开始对话

1. 打开 **会话**（`/sessions`），然后选择 **新建会话**。
2. 选择一个就绪的工具。表单会显示一个文件夹并为你填好。若会话使用其自身的文件夹，
   请保持不变；若要使用 README 安装所挂载的宿主机文件夹，请选择 **更改文件夹**，
   并输入 `/project`。
3. 将 **第一条消息** 留空，然后按 **启动**。会话打开后，你可以在对话中输入消息。
   配置文件和主目录路径由引擎填写；首个会话无需预先登记它们。

容器用户（UID 65532）需要对宿主机项目具有写权限。
[项目文件夹说明](https://github.com/olivaresai/olivares/blob/main/deploy/compose/README.md#work-on-a-host-project-folder)
解释了相关权限。会话可以更改所挂载文件夹中的内容；请使用项目目录，而不是整个主目录。

请让工具解释该项目，并在对话中阅读它的回复。如果 **启动** 无法继续，表单会指出缺少
什么。请按照该提示在 **工具** 中操作，然后返回会话。

## 4. 批准一项操作

要尝试一次批准，请在启动会话时选择 **更多选项 → 受监督**。让工具在项目中
运行一个无害的命令。当它请求权限时，打开 **规则**。等待你处理的请求会以橙色标记，
并在队列旁边打开，其中显示命令、它将访问的范围，以及触发该请求的规则。选择
**仅批准一次**（或按 `A`），或选择 **拒绝**（`D`）并添加备注。返回对话即可阅读结果。
组织策略也可能要求会话启动前先获得批准；此时审批人必须先接受该请求。

## 5. 停止与恢复

在会话上选择 **停止**，如有提示请确认。工具进程会结束，但对话会保留。在同一会话上
选择 **恢复**，然后发送另一条消息。恢复会保留所选的配置文件、账户和文件夹，并检查
它们是否仍然可用。

关于持续运行、CLI 和可选的配置文件设置，请参阅
[运营提供商会话](/how-to/operate-provider-sessions/)。

## 原生启动消息

其他安装方式有各自的限定条件；请参阅
[安装](https://github.com/olivaresai/olivares/blob/main/INSTALL.md#installation-qualification)。
原生快速启动会打印控制台地址，以及其设置状态对应的下一步。以下是全新、回访和待完成
设置时显示的消息：

```text
Next: Open the console; it guides setup, sign-in and your first session.
Next: Open the console and sign in to continue your work.
Next: Open the console to finish setup with the one-time token issued earlier.
```

如果没有配置主机级推理凭据，引擎启动日志会包含：

```text
session runtime: no inference credential source configured; stream-json launches are deny-closed
```

这会限制使用托管注入但未绑定提供商的 Claude 配置文件。绑定的提供商密钥或工具已授权
的登录是另一种凭据来源；参见
[运营提供商会话](/how-to/operate-provider-sessions/#choose-how-the-tool-connects)。
