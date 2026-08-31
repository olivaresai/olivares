> 机器翻译。英文版本为权威来源。

# ADR-0005: 默认内嵌 SQLite，规模化时采用 Postgres + RLS

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions register (T4); data-model design

## 背景与待解决的问题

control plane 存储的是一个多租户数据模型（访问图是其之上的一个*视图*）。它必须能
作为零依赖的单个二进制运行，以支持小型/air-gapped 安装，同时又能扩展到多主机、
多租户的部署。

## 决策驱动因素

- 面向单二进制/air-gap 路径的零外部依赖。
- 规模化时强有力的多租户隔离。
- 不使用 CGO，以保持纯 Go 的静态二进制。

## 考量过的选项

- **SQLite（纯 Go）→ Postgres + 行级安全（row-level security）。**
- 为访问图使用**图数据库**（Neo4j、Dgraph）。

## 决策结果

所选方案：**内嵌 SQLite**（`modernc.org/sqlite`，纯 Go，无 CGO）用于单节点与
air-gap；**Postgres**（通过 `pgx`）配合按 `tenant_id` 键控的**行级安全**用于多主机、
规模化与多租户。访问图被建模为**通用数据模型之上的一个视图**，而非一套独立的存储。

### 影响

- **好处：** 单个二进制无需安装任何数据库；同一模型可扩展到带有按租户 RLS 隔离的
  Postgres。
- **坏处/权衡：** 需要支持两套存储后端；RLS 的正确性必须经过测试（确实如此——在 CI
  中以强制 RLS 模式进行测试）。
- **中性：** 访问图无需任何专门的图引擎，因为它是一个视图。

## 为何否决其他方案

- **图数据库** —— 自托管负担重且用力过猛：访问图是关系模型之上的一个视图，而非一种
  需要专用图引擎的工作负载。
