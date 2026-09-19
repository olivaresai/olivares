---
title: 添加提供方并启动 Claude Code、Codex 或 Grok
description: >-
  在控制平面注册 API 密钥，在不消耗任何额度的情况下测试连接，把它绑定到提供方
  配置档案，并启动第一次会话 —— 无论从控制台还是从 CLI。
---

本页是**提供方**平面的第一个小时：你的 API 密钥放在哪里，你怎么知道它可用，以及会话
如何使用它启动。

在 v26.10 之前，第一个问题的答案是服务器上的一个环境变量。那些变量仍然有效。它们不再
是唯一的路径，也不再是新操作者的起点。

## 这三个词的含义

各用一句话说明，因为产品曾经把它们混为一谈，而产品给出的拒绝信息是分开称呼它们的。

| 词 | 它是什么 |
|---|---|
| **提供方** | 一份凭据：一个 API 密钥、一个可选端点，以及它所属的类型（`anthropic`、`openai`、`xai`、`openai_compatible`）。 |
| **提供方配置档案** | 本机上的一个身份：哪个官方 CLI 运行，以及在哪个配置主目录和用户主目录之下。 |
| **会话** | 一个已启动的子进程，运行在一个配置档案之下，使用一个提供方的凭据。 |

仅有提供方不会启动任何东西。没有提供方的配置档案，只有在宿主机变量恰好已设置时才能
启动。让会话真正启动的，是两者之间的绑定。

## 1. 添加提供方

### 从控制台

1. 打开**提供方**（AI → 环境 → 提供方）。
2. 选择**添加提供方**。
3. 选择提供方，取一个你在选择器中能认出来的名字，并粘贴密钥。除非你指向自己的网关，
   否则请将端点留空；`openai_compatible` 的提供方必须填写端点，因为没有可假定的官方
   端点。
4. 确认。这次写入需要 AAL3 会话，与本产品中其他所有凭据一样；控制台展示的是认证流程，
   而不是一个拒绝。

引擎会在静态存储时封存该密钥，并返回四个字符的提示。**密钥永不再被返回**，即使在你刚
写入之后也是如此。如果丢失，请更换：没有任何读取可以把它取回。

### 从 CLI

```sh
# 密钥从标准输入读取。它绝不会成为某个 flag 的值：flag 会把凭据留在 shell 历史里，
# 也会留在进程表里。
olivares provider add --kind anthropic --name "Anthropic (prod)" < key.txt

# 或者从你自己 shell 的环境变量读取：
ANTHROPIC_KEY=sk-ant-... olivares provider add \
  --kind openai --name "Codex" --key-env ANTHROPIC_KEY
```

## 2. 测试连接

```sh
olivares provider test prv_01J8ABCDEF
```

该测试向提供方询问它提供哪些模型。**它不发送任何补全，也不消耗任何额度。**

它会给出三者之一，而这是三个不同的问题：

| 结果 | 含义 | 该怎么做 |
|---|---|---|
| `ok` | 提供方作出了响应并接受了该凭据。 | 无需处理。去绑定它。 |
| `refused` | 提供方作出了响应并拒绝了该凭据。 | 更换密钥。 |
| `unreachable` | 没有得到响应。 | 检查端点、网络和任何代理。**这并不能说明密钥有问题** —— 不要重新生成它。 |

你尚未测试过的提供方会显示为**未测试**，绝不会显示为可用。注册凭据是一个意图；测试才
是一个事实。

## 3. 注册配置档案并绑定提供方

配置档案的主目录必须已经存在于运行控制平面的那台机器上。服务器在那里校验它们，且绝不
创建缺失的目录：一个空的后备主目录会让会话获得无人配置过的提供方身份。

```sh
olivares agent profile create \
  --driver claude \
  --config-home /home/ops/.claude \
  --user-home /home/ops \
  --name "Claude（工作）" \
  --auth-source managed_injection \
  --provider prv_01J8ABCDEF
```

`--auth-source` 决定子进程的提供方身份来自哪里，而这两个取值并不构成回退链：

- `provider_account_home` —— 已保存在该配置档案自身主目录中的登录。Olivares 不注入任何
  东西，也从不读取那个文件。
- `managed_injection` —— 由引擎提供的凭据。若绑定了提供方，就是该提供方的凭据。

还有一个单动词的快捷方式，它把检测、注册和绑定一次做完，并把主目录默认为该驱动在你
`$HOME` 下的那一个：

```sh
olivares agent deploy claude --provider prv_01J8ABCDEF
```

它报告四种状态，而这不是同一个问题：**未安装**（它会给出安装命令而不是执行它）、
**已安装**、**配置档案就绪**、**可启动**。它绝不执行提供方登录，也绝不创建缺失的主
目录。

之后再绑定（或重新绑定）：

```sh
olivares provider bind prv_01J8ABCDEF --profile ppf_01J8ZZZZZZ
```

引擎会拒绝该配置档案的驱动无法读取的凭据。把 OpenAI 密钥绑到 Claude 配置档案上，会得到
一个同时指名两者的拒绝：绑定时一次，启动时再一次 —— 而不是一个在握手中途失败的会话。

| 提供方类型 | 能读取它的驱动 |
|---|---|
| `anthropic` | `claude`、`opencode` |
| `openai` | `codex`、`opencode` |
| `xai` | `grok`、`opencode` |
| `openai_compatible` | 全部，配合它自己的端点 |

## 4. 启动第一次会话

```sh
olivares agent workspace add /srv/projects/acme --name acme --mode ro --dlp deny
olivares agent session create \
  --name acme-1 \
  --workspace ws-123 \
  --provider-profile ppf_01J8ZZZZZZ
olivares agent session attach run-123
```

`--provider-profile` 是**必填项**：服务器不会隐式选择配置档案、主目录或环境。

在控制台中，同一条路径是**新手引导 → 代理与第一次会话**，或**会话 → 新建会话**。

## 更换与吊销

```sh
olivares provider rotate prv_01J8ABCDEF < new-key.txt   # 就地重新封存
olivares provider rm prv_01J8ABCDEF --yes               # 不可撤销
```

更换会在同一引用下替换取值，因此所有已绑定的配置档案继续可用，而**下一次**启动使用新
密钥。已在运行的会话仍保留它启动时的凭据。先前的连接测试结果会被清除：针对一份已不存
在的凭据所得出的结论，并不能证明替换它的那一份可用。

吊销会销毁封存的取值，并**指名**拒绝此后的每一次启动。记录与绑定被有意保留：一个悄悄
不再指向任何东西的配置档案，读起来就像没人配置过一样。这里的吊销不会在提供方那一侧吊销
密钥；那要在他们自己的控制台中操作。

## 引擎拒绝什么，以及为什么

| 你看到 | 含义 |
|---|---|
| `provider credentials cannot be stored on this deployment` | 没有接入封存式保管库。引擎拒绝保存密钥，而不是保存一份它无法保护的密钥。 |
| `the provider connection test is not available` | 没有接入探测器。**启动不受影响。** |
| `a … credential is not readable by driver …` | 类型与驱动不匹配。参见上面的表格。 |
| `the provider this profile is bound to is revoked` | 请绑定一个有效的提供方。 |
| `this launch has two endpoints` | 部署的推理网关与提供方自身的 `base_url` 同时生效。请去掉其中一个；引擎不会替你选择。 |
| `the registered provider credential could not be opened` | 启动被拒绝。**它不会回退到宿主机凭据** —— 那会让你的会话跑在一个你没有选择的账户上。 |

## 环境变量，以及它们仍然适用的地方

`OLIVARES_SESSION_RUNTIME_WIF` 和 `OLIVARES_SESSION_RUNTIME_TOKEN_FILE` 保持不变，仍受
支持。它们是整台宿主机的凭据，适用于**没有**指明提供方的任何配置档案。

指明了提供方的配置档案使用那一个。更具体的选择优先，并且不存在从它回退的路径：一份无法
取出的已绑定凭据会拒绝启动，而不是悄悄改用部署级的凭据。

## 相关

- [运行提供商会话](/zh/how-to/operate-provider-sessions/)
- [你的第一个小时](/zh/how-to/first-hour/)
- [CLI 参考](/zh/reference/cli/)
