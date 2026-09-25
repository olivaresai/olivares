---
title: 使用 Homebrew 安装
description: >-
  Olivares AI 的 macOS Homebrew cask 坐标、cask 对 Gatekeeper 的处理，以及
  v26.9.0 tap 提升的发布状态。
draft: false
---

这是 `INSTALL.md` 称为推荐的 macOS 路径。它通过 Homebrew cask 安装已签名的
`olivares` 二进制文件，并清除 Gatekeeper 隔离。它不是 Linux 软件包路径
（[从软件包安装](/how-to/install-from-packages/)），也不是 Docker
（[用 Docker 部署](/how-to/docker-deployment/)）。

:::note[测试版 — v26.9.0 cask 已发布]
安装面证人将 Homebrew 记为 **published**
（`docs/releases/v26.9.0-install-surfaces.json`，测量于
2026-09-23T20:28:11Z）：tap 的 `Casks/olivares.rb` 标明版本 26.9.0，以及四个平台归档，
其 SHA-256 与该发布的一致。生产者是 `.goreleaser.yaml` `homebrew_casks:`；tap 的 cask
由发布作业提升。下面的命令是 `INSTALL.md` 命名的坐标（`brew install olivaresai/tap/olivares`）。
:::

## 1. 安装 cask

```sh
brew install olivaresai/tap/olivares
```

Homebrew 用记录的 SHA-256 核验每次 cask 下载（`INSTALL.md`）。cask 安装已
签名的二进制文件并**清除 Gatekeeper 隔离**。

Darwin 二进制文件由 cosign 签名（供应链信任），**尚未经 Apple 公证**。手动
下载归档会被隔离；cask 会处理，或按 `INSTALL.md` 的手动路径用
`xattr -d com.apple.quarantine olivares` 清除。

## 2. 首次运行

```sh
olivares quickstart
```

安全默认：TLS 开启、loopback、无默认凭据。引擎打印控制台 URL 和一次性安装
令牌。继续见 [第一个小时](/how-to/first-hour/)。

临时合成 estate（loopback、明文）仅供查看：

```sh
olivares serve --seed-demo --insecure --data-dir "$(mktemp -d)"
```

`--seed-demo` 不是产品导览。见 [第一个小时](/how-to/first-hour/)。

## 相关

- [自行托管控制平面](/how-to/self-hosting/) — 其他安装形状。
- [验证一次发布](/how-to/verify-a-release/) — cosign、SBOM、来源。
- [从软件包安装](/how-to/install-from-packages/) — Linux `.deb` / `.rpm` / `.apk`。
