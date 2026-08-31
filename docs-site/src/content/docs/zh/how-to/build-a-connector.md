---
title: 构建并发布一个连接器
description: >-
  使用公开的 Apache-2.0 连接器 SDK 来脚手架、实现、测试、签名并分发一个第三方
  连接器——并以拒绝关闭的签名准入将其接入控制平面。
---

本指南带你从零开始，做出一个运维方可以接入控制平面的**已签名第三方连接器**。
连接器 SDK 采用 Apache-2.0 许可，且不从 AGPL 引擎导入任何东西，因此你的连接器
是**你的**代码、采用**你的**许可、构建在**你的**仓库中。

你构建的是一个普通的 Go 程序：一个实现 `sdk.SourceConnector`（收集事实、发出
观测）、`sdk.OutputConnector`（投递通知）或 `sdk.ContentSource`（向受治理知识库
提供文档与 ACL 引用）的类型，打包为一个
[go-plugin](https://github.com/hashicorp/go-plugin) 二进制，引擎以进程外方式
启动它并通过 gRPC 与之通信（双向认证的环回地址，AutoMTLS）。请先阅读
[连接一个源](/zh/how-to/connect-a-source/)，了解连接器的*模型*——仅观测、
最小数据、三种观测类别。

:::note[稳定性]
SDK 契约（`Descriptor/Open/Gather/Close`、传输线协议、插件握手）是**稳定的 v1**
——见 [API 稳定性](/zh/reference/api-stability/) 以及仓库中的 `sdk/VERSIONING.md`。
在首批公开 semver 标签发布之前，请针对仓库的一个检出进行构建（下文的
`-sdk-path`）。
:::

## 1. 脚手架

首选命令行入口：

```sh
# from the repository checkout root
go run ./cmd/olivares connector init acme.widget-audit \
  --dir ~/olivares-connector-widget \
  --module github.com/acme/olivares-connector-widget \
  --template access-edge-source \
  --plugin \
  --sdk-path "$PWD/sdk"
```

从五种原型中选择一种。它们只是稳定 SDK 表面的预设，并非新的作者契约：

| 模板 | 声明的表面 | 适用场景 |
|---|---|---|
| `content-source` | `knowledge.document` | 用于受治理知识 ingest 的文档，包括进程外内容源。 |
| `access-edge-source` | `observation.edge` | 访问图、身份、SaaS 与基础设施关系事实。 |
| `output-sink` | `notify.sink` | 通知或工单接收端。 |
| `agent-surface` | `observation.edge`, `observation.finding` | 上报访问边和发现的智能体 runtime 适配器。 |
| `model-provider` | `observation.cost`, `observation.edge` | 提供方清单、使用量与成本观测；模型治理留在引擎侧。 |

较早的独立脚手架仍然有效，并生成相同的稳定作者契约：

在仓库的一个检出中运行以下命令（在首批公开 SDK 标签发布之前，该包通过 workspace
解析，且 `-sdk-path` 指向该检出的 `sdk/`）：

```sh
# from the repository checkout root
go run ./sdk/scaffold/cmd/olivares-connector-new \
  -dir ~/olivares-connector-widget \
  -name acme.widget-audit \
  -module github.com/acme/olivares-connector-widget \
  -kind source -plugin \
  -sdk-path "$PWD/sdk"
```

你会得到一个完整的仓库：连接器骨架、一个生命周期测试、插件 `main`、一个包含
整个生命周期的 README，以及 `scripts/check-boundary.sh`——**与我们 CI 运行的
完全相同的许可边界检查**，供你使用。`-name` 是你的 `Descriptor.Name`：全局
唯一、带点号、`<vendor>.<connector>`。

## 2. 实现

简而言之的契约（`sdk.SourceConnector` 上的 godoc 是规范性的）：

- **`Open`** 读取配置（在你的 `Descriptor.ConfigFields` 中声明；机密是*引用*，
  标记为 `Secret: true`，绝不内联）。失败要在这里发生，而不是在 `Gather` 中。
- **`Gather`** 向引擎的 `Sink` 发出观测。**调度由引擎掌握**：批处理源做完工作
  后返回；流式源则阻塞直到 `ctx` 被取消。绝不要自己拥有计时器。
- 投递是**至少一次**的；消费者按观测的自然键去重。不要跟踪投递状态。
- **最小数据**：发出引用和元数据，绝不发出载荷、提示或机密值。
- 对于 `content-source`，**`List`** 返回足够廉价、可枚举的引用，**`Fetch`**
  返回一份文档正文，可选的 `DeltaContentSource` 则加入实时增量与 ACL 刷新。
  实现该可选接口的内容源插件会自动声明 `content.delta`；未声明此能力时，宿主
  不会调用增量方法。

运行你的测试，然后在你的 CI 中证明许可边界：

```sh
go test ./...
./scripts/check-boundary.sh   # fails if anything links github.com/olivaresai/olivares/core
```

## 3. 打包并签名

构建插件二进制，固定其摘要，并附加一个作为 **Sigstore 捆绑包**的供应链证明。
控制平面会验证 SLSA 溯源或 SBOM 证明（SPDX / CycloneDX 谓词）——用你自己的密钥
签名（此处所示）或用你的 CI 身份无密钥签名：

```sh
go build -trimpath -o widget-audit ./cmd/acme-widget-audit
sha256sum widget-audit

# keyed (the dev loop: trust your own public key)
cosign generate-key-pair
cosign attest-blob --key cosign.key \
  --type slsaprovenance1 --predicate provenance.json \
  --bundle widget-audit.sigstore.json widget-audit

# keyless alternative (CI): same command with --yes and an OIDC identity,
# or GitHub artifact attestations (gh attestation download produces the bundle).
```

## 4. 分发

发布一个带有二进制、其 `sha256` 和 `.sigstore.json` 捆绑包的 **GitHub release**
——或者用 `oras push` 把相同的工件推送到一个 OCI registry（证明作为 referrer）。
用 semver 版本化；在你的 README 中声明你所针对构建的 `ProtocolVersion`
（今天是 v1）。

## 5. 运维（你的用户要做什么）

运维方把二进制和捆绑包放到主机上，并在源配置（`OLIVARES_SOURCES_CONFIG`）中
同时固定**摘要和信任**：

```json
{
  "connector_trust": {
    "trusted_keys": ["-----BEGIN PUBLIC KEY-----\n…acme's cosign.pub…\n-----END PUBLIC KEY-----\n"],
    "allowed_predicates": ["https://slsa.dev/provenance/v1"]
  },
  "sources": [
    {
      "name": "widget-prod",
      "tenant": "<tenant-id>",
      "config": { "endpoint_ref": "…" },
      "plugin": {
        "path": "/opt/olivares/plugins/widget-audit",
        "sha256": "<the released digest>",
        "bundle": "/opt/olivares/plugins/widget-audit.sigstore.json"
      }
    }
  ]
}
```

准入是**拒绝关闭、没有任何逃生口**的：没有信任锚、没有捆绑包、摘要不匹配、
签名者不受信任或谓词类型错误，全都意味着该源**未被接入**（启动时会说明原因）。
成功时，引擎会在 exec 时对二进制重新计算哈希（go-plugin 的 `SecureConfig`），
从而使已验证的字节就是被执行的字节，且子进程通道以 AutoMTLS 固定。

内容源插件使用同一个根级 `connector_trust`，并在 `documents` 配置块下为每个
源使用相同的 `plugin { path, sha256, bundle }` 形状。它们是用于知识 ingest 的
一等进程外内容源。

信任锚是**强制性的**——既无 `trusted_roots` 也无 `trusted_keys` 的
`connector_trust` 会被直接拒绝。对于**无密钥**签名，锚是 Fulcio（或私有 CA）
根，因此运维方设置 `trusted_roots`（根 PEM，例如来自 `cosign initialize`）
**外加** `allowed_identities` 和 `allowed_issuers`（两者一起——签名必须携带的
SAN 身份和 OIDC 颁发者）；只有 `trusted_keys` 被替换。上面的裸密钥示例是最简单
的锚。

## 6. 获得认证（可选但推荐）

两条互补的记录：

- **产品内认证** —— 你的用户把你的连接器策划为一个目录条目（类别 `connector`，
  模块 XIV），并针对你发布的摘要记录一个已验证的溯源/SBOM 准入裁决
  （`POST /entries/{id}/admit`）；在 `require_signed` 开启时，审批对该裁决是
  拒绝关闭的。见 [模块 XIV](/zh/reference/modules/xiv-catalog/)。
- **已验证连接器索引** —— 提交你的连接器以列入
  [已验证连接器](/zh/reference/verified-connectors/)：维护者会重新验证你的
  发布物（边界、签名、溯源、最小数据审查）并将其列出。该索引记录的是验证；
  它**不是**一个信任根——运维方仍需自行固定*你的*身份/密钥。

## 构造上即受治理

执行机制从构造上位于引擎侧：连接器不链接治理代码，且无法选择退出。引擎按已配置
的源身份（`source_type`、`source_ref`）应用控制，包括源作用域、ACL 交集、
DLP/检索扫描、准入和审计；`Descriptor.Surfaces` 只被视为提示性元数据，**绝不**
作为执行输入。

私有连接器也是一等公民。你可以将连接器保留在企业内部，从不发布、也不公开列出；
只要运维方固定其二进制摘要与信任根，它仍会受到治理。已验证连接器索引记录认证，
并非信任根。

## 诚实的限制（v1）

- 外部接入覆盖**观测源**和**内容源**；一个输出连接器以完全相同的方式构建和发布，但
  通知组合尚不加载外部输出插件。
- 进程外**模块**尚不可用（proto 已冻结，宿主集成有意尚未装配）。
- 观测的和类型是**密封的**：你发出边、成本样本和发现——带有开放的字符串词汇表
  ——但不能定义新的观测类别。
