---
title: "源与凭据作用域"
description: >-
  将一个已连接的源——MCP 服务器、模型、提供方、知识库或数据源——绑定到某个工作区或代理组，
  并在代理或会话取用它的那一刻，解析出该参与方是否在作用域内、以及应使用哪个凭据引用。
  其构造上为默认拒绝（deny-closed）。
---

源与凭据作用域（`modules/sourcescope`）在运行时回答唯一一个问题：当一个代理或会话取用
某个已连接的源——MCP 服务器、模型、提供方、知识库或数据源时，**这个参与方是否在作用域内，
以及应使用哪个凭据引用？** 它处于 **LIVE** 状态：绑定表、它的写入 API，以及运行时
PEP 所调用的解析器（resolver），全部随二进制发布。

它之所以是一个模块而非一个列，是因为它所执行的作用域并非任何单一源实体的属性——MCP 配置、
模型、提供方与知识库分处不同模块，而只有代理/会话/资源这条轴线本身携带工作区。该作用域是一个
**绑定**：`(source) → (workspace or agent-group)`，并带有一个可选的、有作用域的凭据引用。
本模块拥有该绑定表与解析器。

## 绑定及其 API

`/v1/m/sourcescope/bindings` 是一个标准的 CRUD 面，由 `sourcescope:binding:read` 与
`:binding:write` 门控。一个绑定面向一种源类型（`mcp`、`model`、`provider`、`knowledge`、`data`）
与一棵作用域树（`workspace`、`agent_group`），并携带一个 **无明文值的 `CredRef`** ——一个逻辑名称、
一个 `ref_kind` 定位符（`env`、`vault`、`secret_manager`、`file`、`other`）以及一个可选的掩码提示。
任何字段都不能持有可用的密钥；处理器会拒绝内联凭据，这与
`capabilities.mcp_config.secret_refs` 的最小化数据不变式相同。

## 解析器如何决策

该决策是默认拒绝且由多项组合而成，并不是第二个授权引擎：

- **包含（Containment）** —— 绑定到工作区 W 的源，可被 W 中的代理或会话解析，无需任何进一步配置。
- **授予（Grant）** —— 来自 [`vi-governance`](/zh/reference/modules/vi-governance/) 的一个
  跨 [`x-models`](/zh/reference/modules/x-models/)、有作用域的 Cedar 授予，可打开一个外部工作区。
- **RBAC** —— 全租户范围的权限仍可见一切；工作区是软隔离（soft-isolation），租户才是硬边界。
- **禁止（Forbid）** —— 一个有作用域的 Cedar 禁止会覆盖上述全部。

该门是 **增量式（additive）** 的：未绑定的源为向后兼容保持全局可见；一个已绑定但既无包含作用域、
又无授予、也无 RBAC 的源会被 **拒绝**。解析器作为 `ScopeGate` 接入到模型执行链上，以及
[`viii-knowledge`](/zh/reference/modules/viii-knowledge/) 检索上。

## 有界上下文，直说

- 这 **仅是引用绑定**。在实际提供方调用中对有作用域凭据的 **消费**，以及一个代表代理拨号连接服务器的
  运行时 **MCP 代理（broker）**，**目前尚不存在于代码树中**——解析器返回作用域内的引用，
  但这里没有任何东西用它去认证一次出站调用。
- 参与方的作用域来自 **由调用方的参与方引用所指定** 的代理/会话。作用域值是从已存储的行中读取的
  （调用方无法注入一个工作区），但选择哪个代理是由调用方决定的；将该引用与主体（principal）绑定
  是一项加固类的后续工作。参见 [诚实与限制](/zh/start/honesty-and-limits/)。

## 相关内容

- [治理（vi）](/zh/reference/modules/vi-governance/) —— 解析器所组合的 Cedar 授予/禁止代数与 RBAC。
- [模型（x）](/zh/reference/modules/x-models/) —— `ScopeGate` 运行所在的执行链。
- [知识（viii）](/zh/reference/modules/viii-knowledge/) —— 受治理的检索，是解析器进行门控的第二处。
