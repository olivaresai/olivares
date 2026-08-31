> 机器翻译。英文版本为权威来源。

# ADR-0020: 从单独的私有仓库分发企业版

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** ADR-0010 (license is attestation-only), ADR-0011 (license boundary),
  `LICENSING.md`

## 背景与问题陈述

许可模式采用开放核心：AGPL 的 core/modules/web 是完整的社区版，SDK/connectors 采用
Apache-2.0，而 `enterprise/` 系列是仅通过 `-tags enterprise` 构建的附加商业代码
（ADR-0011）。但在此之前，`enterprise/` 源码**随公开仓库一同发布**。由于激活门槛是
build tag（而不是依据 ADR-0010 仅用于证明的许可证），并且许可证从不限制 runtime，任何人
都可执行 `git clone && go build -tags enterprise`，免费获得完整商业二进制文件。商业壁垒
完全依赖可见且可自由编译源码之上的法律许可证（荣誉制度）。

## 决策驱动因素

- 让 build-tag 门控成为**真正的**边界，而非表面机制：任何人都不应仅凭公开源码编译商业二进制文件。
- 保持 AGPL 社区二进制文件**逐位不变**——不进行 rug-pull，不移除任何已经开放发布的功能。
- 保持按目录划分的许可边界（ADR-0011）和仅用于证明的许可证（ADR-0010）均不变。

## 考虑过的选项

- **将 `enterprise/` 留在公开仓库**（GitLab 在单一仓库中设置 `ee/` 的 source-available
  模式）。这种方式诚实，但壁垒只是对可见、可自由编译源码实行的荣誉制度。
- **将 `enterprise/` 移至单独的私有仓库**（Grafana 模式：公开 OSS 源码 + 从私有源码
  构建的可下载企业二进制文件）。

## 决策结果

选定方案：**将 `enterprise/` 移至单独的私有仓库**。公开仓库不再包含 `enterprise/` 树、
使用 `//go:build enterprise` 的 `cmd/olivares` wiring，也不包含任何通过 `-tags enterprise`
构建的工具。商业二进制文件在私有仓库中构建：将商业代码树和 wiring 叠加到公开树的固定
checkout 上（公开树作为 submodule；wiring 叠加进 `cmd/olivares` 的 `package main`，仅靠
`go.work` 的模块选择无法实现这一点）。

此项决策只改变**分发**，不改变许可：

- **ADR-0011（许可边界）保持不变：** `enterprise/` 仍使用
  `LicenseRef-Olivares-Commercial`；AGPL/Apache 边界保持完整。
- **ADR-0010（仅用于证明的许可证）保持不变：** 开放二进制文件仍不会读取许可证以启用或禁用
  任何功能。许可证之所以变得具有*实质*意义，只是因为读取已证明 claim（add-on entitlements）的
  源码不再公开，而不是因为许可证开始限制 runtime。

### 后果

- 对公开仓库执行 `git clone` + `go build -tags enterprise` 不再生成商业二进制文件：其所需
  源码为私有。门控现在是真实的。
- 默认 AGPL 二进制文件保持不变——它本来就未链接 `enterprise/`。
- open≡enterprise schema-parity gate 需要两个版本，因此移至唯一能同时构建两者的私有仓库。
- 代价是维护两个仓库和一个小型 overlay assembly step；公开 release artifact 不受影响
  （它原本就使用 `-tags release`，从不使用 `-tags enterprise`）。

## 修订 (2026-07-28) ——上文提及的席位授权已取消

分发决策仍然有效：`enterprise/` 位于私有仓库中，且 build-tag 门控是实质有效的。已不再成立的是
「后果」中使用的*例子*——「读取已证明 claim（席位授权）的源码」。决策 B10（2026-07-27）移除了
用户上限，因此已不存在席位授权，且没有任何构建会读取许可证来限制用户；其余已证明的 claim
只会被读取以授予附加 add-on 的权益。原句按原样保留，因为它记录了本 ADR 作出时为真的情况。
当前决策：商业定价规范（私下维护）（`self_hosted.users: unlimited`）和
`LICENSING.md`。
