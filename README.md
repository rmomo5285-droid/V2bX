# V2bX hardened fork

[![](https://img.shields.io/badge/TgChat-UnOfficialV2Board%E4%BA%A4%E6%B5%81%E7%BE%A4-green)](https://t.me/unofficialV2board)
[![](https://img.shields.io/badge/TgChat-YuzukiProjects%E4%BA%A4%E6%B5%81%E7%BE%A4-blue)](https://t.me/YuzukiProjects)

A V2Board/XBoard node server based on multiple cores, modified from XrayR.
一个基于多种内核的 V2Board/XBoard 节点服务端，修改自 XrayR，支持 V2Ray、
Trojan、Shadowsocks 和 Hysteria 等协议。

当前生产组合为核心 [`v0.1.2`](https://github.com/rmomo5285-droid/V2bX/releases/tag/v0.1.2)
与部署脚本 [`v0.1.3`](https://github.com/rmomo5285-droid/V2bX-script/releases/tag/v0.1.3)。
正式节点应使用固定版本的 Release；默认分支用于同步上游和后续开发，不应替代
生产版本锁定。

## 特点

新增可选的三核心访问记录上报和独立在线心跳，配置、升级和 ClickHouse 对接见
[访问分析部署说明](docs/accesslog.md)。默认关闭上报，现有节点配置可继续使用。

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
* Xray、sing-box、原生 Hysteria2 统一执行 XBoard 阻断规则。
* 提供按用户 UUID 的扫描/异常扇出保护和有限频率审计日志。
* 提供公共出站安全规则，减少端口扫描和 UDP 反射滥用风险。

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

### 已部署节点迁移

迁移器在旧服务仍运行时完成下载和校验，随后才停服切换；健康检查失败会自动回滚：

```bash
curl --proto '=https' --tlsv1.2 -fLo /tmp/v2bx-migrate-v0.1.3.sh https://github.com/rmomo5285-droid/V2bX-script/releases/download/v0.1.3/migrate-to-rmomo-v2bx.sh && printf '%s  %s\n' 'c5c2a3cb4cfbdbdd342f7740e37b8e2c760a929986b2ecafb78fcc2ad5cdda89' /tmp/v2bx-migrate-v0.1.3.sh | sha256sum -c - && env V2BX_SCRIPT_REF=v0.1.3 bash /tmp/v2bx-migrate-v0.1.3.sh check && env V2BX_SCRIPT_REF=v0.1.3 bash /tmp/v2bx-migrate-v0.1.3.sh apply --version v0.1.1
```

### 手动安装

[手动安装教程](https://v2bx.v-50.me/v2bx/v2bx-xia-zai-he-an-zhuang/install/manual)

## 构建
``` bash
# 通过-tags选项指定要编译的内核， 可选 xray， sing, hysteria2
GOEXPERIMENT=jsonv2 go build -v -o build_assets/V2bX -tags "sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor" -trimpath -ldflags "-X 'github.com/InazumaV/V2bX/cmd.version=$version' -s -w -buildid="
```

## 与上游原版的主要区别

| 项目 | 上游原版 | 本 Fork `v0.1.1` |
| --- | --- | --- |
| XBoard 阻断规则 | 不同核心存在各自实现，原生 Hy2 能力不完整 | Xray、sing-box、原生 Hy2 共用同一出站判定器 |
| 域名匹配 | 依赖各核心路由实现 | 普通域名匹配自身和全部子域名，保留显式 `regexp:` |
| 协议阻断 | 原生 Hy2 无法稳定等价执行 | 三核心统一支持 `protocol:bittorrent`；其他 Hy2 不支持的协议规则明确拒绝加载 |
| 用户滥用防护 | 主要依靠连接/IP 数限制 | 增加按 UUID 的连接速率、目标 IP、端口及目标数量限制和临时封禁 |
| 出站安全 | 无统一安全前置检查 | 统一阻止私网/特殊用途目标、TCP 25 和常见 UDP 反射放大端口 |
| UDP 扫描 | 单个 UDP 会话可持续更换目标 | 原生 Hy2 增加会话内目标扇出限制 |
| 审计日志 | 缺少统一的阻断原因和限频 | 记录 UUID、目标主机/端口、原因、计数和封禁时间，并抑制重复日志 |
| Hy2 geodata | 更新时可能产生较高瞬时内存压力 | 加固加载、校验和更新路径，减少重复解析及低内存节点抖动风险 |
| 配置/证书重载 | 节点域名变化等场景可能不能正确重建 | 加固节点差异检测、证书状态和失败回滚，避免静默沿用旧实例 |
| 发布门禁 | 以上游构建流程为主 | 增加全仓测试、race/vet、格式检查、多架构 Release 与容器构建验证 |

### XBoard 路由执行顺序

| 顺序 | 规则 | 行为 |
| --- | --- | --- |
| 1 | 公共安全规则 | 阻止私网/特殊用途目标、TCP 25 和常见 UDP 反射放大端口 |
| 2 | XBoard `block` 域名规则 | 普通域名匹配自身及全部子域名，`regexp:` 按显式正则匹配 |
| 3 | XBoard 协议规则 | 三种核心一致支持 `protocol:bittorrent` |

当前执行的是 **XBoard 精确黑名单**，不是全量中国大陆封锁。面板 `block` 中列出的
域名及其子域名会被三种核心一致阻止；未列入黑名单的大陆域名和公网 IP 仍允许
访问。Bilibili、Apple 等站点不会因为 `geosite:cn` / `geoip:cn` 被默认误伤。

不要在原生 Hy2 中单独恢复 `reject(geosite:cn)` / `reject(geoip:cn)`，否则三个
核心会再次出现规则差异。需要“大陆目标默认拒绝、少量站点放行”时，应在公共
判定器中实现允许列表优先的共享 geodata 策略，而不是只修改 `hy2config.yaml`。

### 默认用户滥用阈值

启用 `AbuseGuardConfig` 后，默认按 UUID 使用 10 秒窗口，最多允许 20 个客户端
连接、300 个出站连接、128 个不同目标、32 个目标 IP 和 16 个端口；超限封禁
600 秒。阈值可以在节点 `LimitConfig` 中覆盖。

审计日志不会解密 HTTPS，也不承诺记录完整 URL。配套部署脚本提供日志轮转和：

```bash
V2bX userlog USER_UUID 30
```

这些控制用于降低扫描和异常扇出风险，不替代防火墙、供应商出站策略或 IDS。
部署、迁移、网络调优和回滚操作见配套
[`V2bX-script` 运维手册](https://github.com/rmomo5285-droid/V2bX-script/blob/master/docs/OPERATIONS.md)。

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
