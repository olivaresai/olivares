---
title: 在隔离网络环境中安装
description: >-
  把一个已签名的发布捆绑包带过隔离边界，完全离线地验证每个镜像和 Helm chart，
  按摘要将它们镜像到私有 registry 并安装——断网一侧不发起任何外呼。
---

This deployment capability is distributed with Enterprise. See [editions](https://olivares.ai/pricing).

## 1. 构建捆绑包（在线，一次性）

### 捆绑包包含的内容

## 2. 在隔离边界内验证并镜像

### 离线验证每个镜像（无透明日志）

### 离线验证 Helm chart

# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz

# (needs the signer's GPG public key in your keyring)

### 按摘要镜像到你的私有 registry

### 按摘要安装，绝不按标签

## 隔离边界内不会向外发起任何调用

## FIPS / STIG 变体

## 另见
