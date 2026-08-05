# V2bX hardened fork

[![](https://img.shields.io/badge/TgChat-UnOfficialV2Board%E4%BA%A4%E6%B5%81%E7%BE%A4-green)](https://t.me/unofficialV2board)
[![](https://img.shields.io/badge/TgChat-YuzukiProjects%E4%BA%A4%E6%B5%81%E7%BE%A4-blue)](https://t.me/YuzukiProjects)

A V2Board/XBoard node server based on multiple cores, modified from XrayR.
一个基于多种内核的 V2Board/XBoard 节点服务端，修改自 XrayR，支持 V2Ray、
Trojan、Shadowsocks 和 Hysteria 等协议。

当前生产组合为核心 [`v0.1.1`](https://github.com/rmomo5285-droid/V2bX/releases/tag/v0.1.1)
与部署脚本 [`v0.1.3`](https://github.com/rmomo5285-droid/V2bX-script/releases/tag/v0.1.3)。
部署时应同时固定这两个版本，不要直接使用漂移的分支名。

## 特点

* 永久开源且免费。
* 支持Vmess/Vless, Trojan， Shadowsocks, Hysteria1/2多种协议。
* 支持Vless和XTLS等新特性。
* 支持单实例对接多节点，无需重复启动。
* 支持限制在线IP。
* 支持限制Tcp连接数。
* 支持节点端口级别、用户级别限速。
* 配置简单明了。
* 修改配置自动重启实例。
* 支持多种内核，易扩展。
* 支持条件编译，可仅编译需要的内核。

## 功能介绍

| 功能        | v2ray | trojan | shadowsocks | hysteria1/2 |
|-----------|-------|--------|-------------|----------|
| 自动申请tls证书 | √     | √      | √           | √        |
| 自动续签tls证书 | √     | √      | √           | √        |
| 在线人数统计    | √     | √      | √           | √        |
| 审计规则      | √     | √      | √           | √         |
| 自定义DNS    | √     | √      | √           | √        |
| 在线IP数限制   | √     | √      | √           | √        |
| 连接数限制     | √     | √      | √           | √         |
| 跨节点IP数限制  |√      |√       |√            |√          |
| 按照用户限速    | √     | √      | √           | √         |
| 动态限速(未测试) | √     | √      | √           | √         |

## TODO

- [ ] 重新实现动态限速
- [ ] 完善使用文档

## 软件安装

### 新机器安装

下面的命令固定部署脚本与核心版本，并在执行前校验安装脚本：

```bash
curl --proto '=https' --tlsv1.2 -fLo /tmp/v2bx-install-v0.1.3.sh https://github.com/rmomo5285-droid/V2bX-script/releases/download/v0.1.3/install.sh && printf '%s  %s\n' '46d6a44b2134fdebb349b42cbd7696e3d4e76d80e4b978e265ab9196ffbea2cb' /tmp/v2bx-install-v0.1.3.sh | sha256sum -c - && env V2BX_SCRIPT_REF=v0.1.3 bash /tmp/v2bx-install-v0.1.3.sh v0.1.1
```

### 从上游或旧版本迁移

迁移器会先下载、校验并准备替换内容，旧服务在停服切换前保持运行；健康检查失败
会自动回滚：

```bash
curl --proto '=https' --tlsv1.2 -fLo /tmp/v2bx-migrate-v0.1.3.sh https://github.com/rmomo5285-droid/V2bX-script/releases/download/v0.1.3/migrate-to-rmomo-v2bx.sh && printf '%s  %s\n' 'c5c2a3cb4cfbdbdd342f7740e37b8e2c760a929986b2ecafb78fcc2ad5cdda89' /tmp/v2bx-migrate-v0.1.3.sh | sha256sum -c - && env V2BX_SCRIPT_REF=v0.1.3 bash /tmp/v2bx-migrate-v0.1.3.sh check && env V2BX_SCRIPT_REF=v0.1.3 bash /tmp/v2bx-migrate-v0.1.3.sh apply --version v0.1.1
```

迁移输出中的 `Service: active`、`Abuse guard: 1/1-enabled` 和
`Legacy Hy2 ACL: absent` 分别表示服务正常、当前节点已启用用户滥用保护、旧 Hy2
中国 ACL 已清理。`Guard: not installed` 只表示可选内存硬限制未启用，不是故障。
每个 `BACKUP` ID 都应保存，用于对应事务的回滚。

### 手动安装

[手动安装教程](https://v2bx.v-50.me/v2bx/v2bx-xia-zai-he-an-zhuang/install/manual)

## 构建
``` bash
# 通过-tags选项指定要编译的内核， 可选 xray， sing, hysteria2
GOEXPERIMENT=jsonv2 go build -v -o build_assets/V2bX -tags "sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor" -trimpath -ldflags "-X 'github.com/InazumaV/V2bX/cmd.version=$version' -s -w -buildid="
```

## 统一路由与安全边界

核心 `v0.1.1` 让 Xray、sing-box 和原生 Hysteria2 在出站前执行同一个公共判定器。
XBoard 中类型为 `block` 的路由只解析一次，并按以下顺序执行：

| 顺序 | 规则 | 行为 |
| --- | --- | --- |
| 1 | 公共安全规则 | 阻止私网/特殊用途目标、TCP 25 和常见 UDP 反射放大端口 |
| 2 | XBoard 域名规则 | 普通域名匹配自身及全部子域名，`regexp:` 按显式正则匹配 |
| 3 | XBoard 协议规则 | 三种核心一致支持 `protocol:bittorrent` |

原生 Hysteria2 遇到无法可靠等价执行的其他 `protocol:` 规则时会拒绝加载对应节点，
而不是在面板显示规则存在、实际上却没有执行。

### 大陆目标阻断说明

当前版本执行的是 **XBoard 精确黑名单**，不是全量中国大陆封锁：

- XBoard `block` 中列出的域名及其子域名会被三种核心一致阻止。
- 未列入面板黑名单的大陆域名和公网 IP 仍允许访问。
- Bilibili、Apple 等站点不会因为中国大陆分类被默认阻断；将其加入面板黑名单后
  仍会正常阻止。
- 迁移器只在确认内容完全属于旧脚本管理时，删除原生 Hy2 的
  `direct(geosite:google)`、`reject(geosite:cn)`、`reject(geoip:cn)` 及旧 Bilibili
  例外块；自定义或损坏 ACL 会在停服前被拒绝。

不要重新把 `reject(geosite:cn)` / `reject(geoip:cn)` 单独写回原生 Hy2。这会让
Xray、sing-box 与 Hy2 的行为再次分裂，并可能阻断 Bilibili 登录、接口或 CDN。
若业务要求“全部大陆目标默认拒绝，仅放行少量站点”，需要在三个核心共同判定器中
另行实现“允许列表优先、CN geodata 其次”的策略，不能只修改 `hy2config.yaml`。

## 用户滥用保护与日志

启用 `AbuseGuardConfig` 后，默认按用户 UUID 使用 10 秒窗口，最多允许 20 个客户端
连接、300 个出站连接、128 个不同目标、32 个目标 IP 和 16 个端口；超限封禁
600 秒。所有阈值都能在节点配置中显式调整。

审计日志记录 UUID、目标主机/端口和阻断原因；协议阻断会体现在原因字段中，不承诺
记录 HTTPS 加密后的完整 URL。部署脚本会安装日志轮转，单个日志最多 10 MiB，保留 5 个轮转文件且
最长保留 7 天。查看最近 30 分钟指定用户日志：

```bash
V2bX userlog USER_UUID 30
```

这些控制用于降低扫描和异常扇出风险，不替代防火墙、供应商出站策略或 IDS。
完整迁移、检查、调优和回滚操作见配套
[`V2bX-script` 运维手册](https://github.com/rmomo5285-droid/V2bX-script/blob/feat/hardened-v2bx-deployment/docs/OPERATIONS.md)。

## 配置文件及详细使用教程

[详细使用教程](https://v2bx.v-50.me/)

## 免责声明

* 此项目用于本人自用，因此本人不能保证向后兼容性。
* 由于本人能力有限，不能保证所有功能的可用性，如果出现问题请在Issues反馈。
* 本人不对任何人使用本项目造成的任何后果承担责任。
* 本人比较多变，因此本项目可能会随想法或思路的变动随性更改项目结构或大规模重构代码，若不能接受请勿使用。

## 赞助

[赞助链接](https://v-50.me/)

## Thanks

* [Project X](https://github.com/XTLS/)
* [V2Fly](https://github.com/v2fly)
* [VNet-V2ray](https://github.com/ProxyPanel/VNet-V2ray)
* [Air-Universe](https://github.com/crossfw/Air-Universe)
* [XrayR](https://github.com/XrayR/XrayR)
* [sing-box](https://github.com/SagerNet/sing-box)

## Stars 增长记录

[![Stargazers over time](https://starchart.cc/wyx2685/V2bX.svg)](https://starchart.cc/wyx2685/V2bX)
