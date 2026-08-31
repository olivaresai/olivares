---
title: 安装许可证并迁移到 Business
description: >-
  已购许可证应放在哪里、如何在不重启引擎的情况下安装、如何检查实际安装内容，
  以及如何原地从 Community 切换到企业版。Ed25519 验证离线进行——没有任何
  网络调用会确立权益。
---

你购买了一个套餐并收到了许可证。本页说明如何处理它：文件应放在哪里、如何将其应用到
正在运行的引擎、如何查看实际安装内容，以及购买企业版套餐后，如何在不重新安装任何内容
的情况下将 Community 二进制文件切换为企业版二进制文件。

:::note[许可证是存证，不是运行时开关]
**它不会门控你正在运行的软件中的任何功能。** 许可证过期或缺失不会关闭功能，任何许可证也都不会限制
用户账户——所有层级的自托管用户数量均不受限。它是一份说明你享有哪些权益的签名声明，
而不是用来解锁已在你磁盘上的代码的密钥。

**它真正门控的是制品访问权限**，而这一区别正是整个模型的关键：下载企业版构建以及从本地发布包
安装（`olivares upgrade --bundle`）都需要有效许可证；许可证会依据你的二进制文件中内嵌的密钥离线检查。
正因如此，企业版是你通过令牌获取的另一份二进制文件，而不是在已有二进制文件中翻转一个 feature flag——
因此，说“它什么都不门控”是错误的。
:::

## 你收到了什么

| 你购买的套餐 | 收到的内容 | 如何处理 |
|---|---|---|
| Community | 无需安装任何内容 | 已在运行——本页内容均不适用 |
| Business / Business Max，自托管 | 一个**许可证文件**和一个**下载令牌** | 安装许可证，然后切换到企业版二进制文件 |
| Cloud | 托管 tenant 的凭据 | 无需在自己的主机上安装任何内容 |

许可证是一个签名 blob。请将其保存为文件——`customer.license` 或任意其他名称——并保留
同一封邮件中的下载令牌：二者用于不同步骤，只有许可证需要安装。

## 1 · 安装许可证

```sh
olivares license install ./customer.license --data-dir /var/lib/olivares
```

该命令会使用构建中嵌入的 Ed25519 公钥，**在写入任何内容之前验证 blob**，因此被截断的
复制粘贴内容会在这里失败，而不是等到下次启动时才失败。成功后，它会写入
`<data-dir>/license.key`，模式为 `0600`——这是引擎默认读取的规范静态许可证。

传入 `-` 而不是路径，即可从标准输入读取 blob：

```sh
pbpaste | olivares license install - --data-dir /var/lib/olivares
```

在现有许可证上执行安装会以原子方式**替换**它，并输出被替换的是哪一个。

### 应用到正在运行的引擎——无需重启

正在运行的引擎会原地获取新许可证。以下任一方式均可完成：

```sh
kill -HUP "$(pidof olivares)"                 # signal the running process
curl -X POST .../v1/console/runtime/reload    # the API half
```

……也可以使用控制台自身的重新加载控件。重启同样有效，只是没有必要。

### 引擎按什么顺序查找

如果你已通过其他方式注入许可证，请注意，数据目录文件在四种来源中优先级**最低**。
引擎按以下顺序解析，优先级从高到低：

1. `--license <path>`（或配置文件中的 `LicenseFile`）
2. `OLIVARES_LICENSE_PATH=<path>`
3. `OLIVARES_LICENSE=<blob>`——直接放在环境中的许可证
4. `<data-dir>/license.key`——`license install` 写入的内容

当 `license install` 能够发现某个 override 的优先级高于其即将写入的文件时，它就会**拒绝执行**：
在这种 override 之下安装会留下一个引擎永远不会读取的文件，你会看到 exit 0，却没有任何变化。
命令会指出它发现的 override。`--force` 仍会暂存该文件——合理场景是你即将移除 override。

:::caution[此项拒绝能够看到和看不到的内容]
该命令从**自身的进程环境**中读取 `OLIVARES_LICENSE_PATH` 和 `OLIVARES_LICENSE`。如果引擎已作为独立进程运行，
传给该引擎的 `--license` flag（或配置中的 `LicenseFile` 条目）对它不可见——`install` 和 `uninstall`
本身根本不接受 `--license` flag。因此，在使用显式路径启动服务的主机上，这两个命令都可能
成功退出，却没有改变引擎读取的任何内容。

执行其中任意一个命令后，请运行 `olivares license status`。它会按照与引擎相同的优先级解析许可证，并告诉你
实际生效的是哪个来源——这才是关键问题。
:::

## 2 · 检查实际安装内容

```sh
olivares license status --data-dir /var/lib/olivares
```

`status` 离线运行，并按引擎使用的相同优先级解析许可证，因此它回答的是关键问题——
*实际生效的是哪一个许可证*——而不是“是否存在一个文件”。它会报告解析到的来源、持有者、
套餐和到期时间。

每次安装后以及移除 override 后都应运行它。

## 3 · Community → Business，原地切换

安装许可证后，下载企业版二进制文件即可。无需重新安装任何内容，也不会移动任何数据：

```sh
olivares upgrade --enterprise --token <TOKEN>
```

该命令会获取适用于你所在平台的已签名企业版构建，并**离线验证签名**——遭篡改的制品会
中止升级，正在运行的二进制文件保持原样——然后以原子方式完成替换，并保留上一个文件的
备份。如果希望先查看计划而不执行，请先使用 `--check`：

```sh
olivares upgrade --enterprise --token <TOKEN> --check
```

重启服务，然后启用附加组件：

```sh
olivares enterprise enable <preset>     # starter | regulated | full
```

启用过程受到治理并会被审计：它会先显示 diff；任何需要机密或审查的附加组件都会进入
暂存状态，而不是只启用一部分。`olivares enterprise status` 会报告哪些内容处于活动状态。
这些命令**仅存在于企业版二进制文件中**——如果 `olivares enterprise` 不是一个命令，
说明你仍在运行 Community 构建，上述切换尚未发生。

:::caution[切换前先备份]
切换替换的是二进制文件，而不是数据——但仍应执行[升级与回滚](/zh/how-to/upgrade-and-rollback/)
所要求的同一份备份。该页面还介绍了如何返回上一个二进制文件。
:::

## 移除许可证

```sh
olivares license uninstall --data-dir /var/lib/olivares --yes
```

该命令会删除 `<data-dir>/license.key` 并报告移除的内容。与 `install` 一样，只要它能看到
`OLIVARES_LICENSE*` override，就会**拒绝执行**——实际生效的并不是该文件，因此删除它不会改变任何内容——
并且它也有相同的盲区：传给另一个进程中运行的引擎的 `--license` flag 对它不可见。这是控制台自身
`DELETE /v1/console/license` 的离线一侧。

移除许可证**不会禁用**你正在运行的任何内容。它只是撤回存证；在切换回原版本之前，
企业版二进制文件仍会按企业版二进制文件的方式运行。

## 本页*不*包含的内容

- **签发许可证**（`license keygen` / `sign`）是同一命令的供应商侧操作。客户无需使用它。
- **每个套餐包含的内容**位于定价页面，而不在这里。
- **该模式如何运作**——为什么订阅提供的是制品访问权限，而不是一个开关——请参阅
  [开放内核与授权许可](/zh/explanation/open-core-and-licensing/)。
