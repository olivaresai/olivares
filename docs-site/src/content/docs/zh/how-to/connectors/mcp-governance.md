---
title: "MCP 自省与注册表治理"
description: >-
  清点你的 agent 能触及的每一个 MCP 服务器，按规范将其自声明的提示视为不可信，
  扫描目录以发现工具投毒与态势问题，并与公共注册表和联邦注册表进行对账。
sidebar:
  order: 7
---

`mcp` source 治理你的 agent 所看到的**能力面**：它对 MCP 服务器进行自省
（工具、资源、提示），从其注解中推导出读/写*提示*，并 —— 可选启用 ——
将正在运行的内容与公共 MCP Registry、你的联邦注册表以及 Docker MCP Catalog
进行对账，并在此过程中为态势评级。

有一条规则锚定本 source 所发出的一切：

:::caution[按规范，MCP 注解不可信]
服务器的 `readOnlyHint` / `destructiveHint` 是自声明，而 MCP 规范规定客户端
MUST 将其视为不可信。本 source 产生的每一条边都是一个**已声明的能力提示** ——
`approximate`，既非被观测、亦非被许可 —— 它提供了可供比对的能力面。
它由被观测的 source 加以佐证，从不单独被信任。
:::

## 声明 source

```json
{
  "sources": [{
    "name": "mcp-estate",
    "kind": "mcp",
    "tenant": "<tenant-id>",
    "config": {
      "config_path": "/etc/claude-code/.mcp.json",
      "posture_scan": "true",
      "registry_enabled": "true"
    }
  }]
}
```

两种方式都可以指向服务器：

| 键 | 含义 |
|---|---|
| `servers` | 待自省的 MCP 服务器规格的内联 JSON 数组 |
| `config_path` | 一个 Claude Code `.mcp.json` 的路径，其 `mcpServers` 会被自省 |
| `timeout` | 每服务器的自省超时 |

## 治理层（每一层皆可选启用，每一层皆诚实）

- **态势扫描**（`posture_scan`，默认 `true`）—— 扫描自省得到的目录元数据，
  以发现工具投毒、注入、同形字以及过宽的作用域，并对照 OWASP MCP Top-10 为态势评级。
  仅扫描目录*元数据* —— 它不探测或利用服务器。
- **公共注册表**（`registry_enabled`，默认 `false`；`registry_url`）——
  从 MCP Registry 进行只读的来源（provenance）增强（上游为预览版；
  连接器会自行核验它所读取的内容）。
- **注册表同步**（`registry_sync` + `owned_namespaces`）—— 枚举你的组织在公共注册表中
  所拥有的反向 DNS 命名空间，以检测被撤回或无人管理的发布（供应链视角），
  并将你的内部服务器从影子标记中清除。
- **内部对账**（`internal_servers`）—— 一个已批准内部服务器的 JSON 数组
  （`{name, registry_name, version}`）；正在运行的服务器会与之对账，并进行版本漂移检测。
  运行中但不在列表上的，是一条**影子（shadow）**发现项。
- **联邦注册表**（`federated_registries`）—— GitHub BYO 组织注册表、
  Azure API Center 以及实现了固定 **`/v0.1` registry OpenAPI** 的私有子注册表。
- **弃用动态源**（`deprecation_feed`）—— 每一轮抓取官方 MCP 弃用特性注册表以检测规则漂移；
  已编译的弃用规则从不依赖该次抓取。
- **Docker MCP Catalog**（`docker_catalog`）—— 镜像摘要固定（digest-pin）漂移，
  外加每服务器的 Docker 构建（已签名）vs 社区（未证实）来源。
- **下一版本预览**（`next_revision_preview`）—— 以 MCP 2026-07-28 RC 的无状态模式
  自省服务器，同时仍对外通告 2025-11-25；明确是一个预览开关。

发现项按层落地：态势评级、来源缺口、影子服务器、弃用特性的使用、注册表漂移。

## 你将在控制台中看到什么

**MCP & skills** 是活动的能力目录 —— 服务器、它们的工具与已声明的提示、技能，
以及每一项如何接入 agent：

<img class="light:sl-hidden" src="/console/capabilities-dark.png" alt="MCP & skills 视图：带有服务器、工具、连接关系与托管配置的活动能力目录。" />
<img class="dark:sl-hidden" src="/console/capabilities-light.png" alt="MCP & skills 视图：带有服务器、工具、连接关系与托管配置的活动能力目录。" />

这些提示为**访问图（Access map）**贡献了*已声明*的能力面；漂移面板正是这样一处：
一个被声明为只读、却被观测到在写入的工具，从此不再是提示问题，而成为一条发现项。

## 诚实的局限

- **自省是服务器所声称内容的一张快照。** 服务器可能撒谎；
  这正是规范本身的立场，也是每条边都被如此标记的原因。佐证来自被观测的 source。
- **部分的注册表快照是一个错误，而非一个结果** ——
  连接器拒绝对照一次它未能完成的注册表读取来评级。
- **态势扫描读取的是元数据。** 它不执行工具、不对服务器做模糊测试，
  也无法检测出干净目录背后被植入后门的实现。

## 相关

- [连接 Claude Code](/zh/how-to/connect-claude-code/) —— MCP 提示与会话遥测相遇之处。
- [模块 V —— MCP、技能与能力](/zh/reference/modules/v-capabilities/)。
- [构建并交付一个连接器](/zh/how-to/build-a-connector/) ——
  连接器二进制文件自身的 deny-closed 已签名准入机制。
