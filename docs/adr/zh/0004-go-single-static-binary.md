> 机器翻译。英文版本为权威来源。

# ADR-0004: 引擎采用 Go，单个静态二进制并内嵌 Web

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Fran Olivares
- **References:** stack decisions register (T1, T5); stack architecture

## 背景与待解决的问题

一个可自托管、对 air-gapped（物理隔离）友好的安全 control plane，需要做到部署极其
简单、原生融入 eBPF/OpenTelemetry/云原生生态、并能作为单一产物交付。引擎语言以及
UI 的交付方式都由此推导而来。

## 决策驱动因素

- 面向自托管与 air-gap 的单一自包含产物。
- 原生 eBPF，以及成熟的模块/插件运行时。
- 面向数据摄取与事件总线的强并发能力。

## 考量过的选项

- **Go**，单个静态二进制，通过 `go:embed` 内嵌 Web。
- **Rust** 引擎。
- **Node/TypeScript** 引擎。
- **独立的 SPA**（两个产物），而非内嵌式 UI。

## 决策结果

所选方案：**Go**，编译为单个静态二进制，将 React Web UI **通过 `go:embed` 内嵌**，
并从与 API 相同的 origin 提供服务——因此整个产品就是**一个文件**。

### 影响

- **好处：** 只有一个产物需要交付、验证和运行；原生 eBPF；与云原生高度契合；并发
  能力适配数据摄取。
- **坏处/权衡：** UI 作为二进制构建的一部分被构建并内嵌进去。
- **中性：** Node/TypeScript 仅用于 Web UI，不用于引擎。

## 为何否决其他方案

- **Rust** —— 构建/迭代更慢，且对 v1 的需求而言用力过猛。
- **Node/TS 引擎** —— eBPF 支持薄弱，且无法做到单个静态二进制，尽管它是熟悉的舒适区。
- **独立的 SPA** —— 需要部署和管理版本的两个产物；内嵌式 UI 让它保持为一个。
