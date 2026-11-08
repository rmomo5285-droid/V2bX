# Xray Vision 实时计费修复

## 原因

V2bX 的用户流量通过 dispatcher 的 reader/writer 包装器累计，再定期发送到
Xboard `/push`。Linux 的 Xray Vision 可以切换到 TCP splice，直接在两个原始
socket 之间复制，绕过这些包装器。

当前依赖 `wyx2685/xray-core@dcfae7c4776b` 的
`proxy.CopyRawConnIfExist` 只用最外层的 `*dispatcher.SizeStatWriter` 类型断言
寻找用户计数器。`0fb6b23eb7e4bfa289075a219d7dbbbb0d980150` 引入的
`onlineWriter` 包在计数器外面，使该断言失败；已经 splice 的数据无法补记。
即使把计数器移到最外层，`TCPConn.ReadFrom` 也只在返回后一次性计数，无法
满足长连接的实时流量上报和额度控制。

此前只有设置了限速、获得非空限速 bucket 的用户会设置 `CanSpliceCopy = 3`。
不限速用户仍可进入上述旁路。这也意味着该旁路与在线记录上报开关无关：
关闭访问记录上传不能撤销 dispatcher 对连接的在线追踪。

## 已有 Linux 复现

基线 `63fedd3a4127f250c4b001334735b63779308451` 的生产代码，使用真实的
本地 TLS VLESS 连接和 V2bX Controller 向模拟面板发送 HTTP 流量批次。
响应分成两个 8 MiB 阶段，每阶段保持连接打开并等待至少三次上报机会。

[复现运行 42452679065](https://github.com/rmomo5285-droid/V2bX/actions/runs/42452679065)
的观测如下，比较的是目标 HTTPS 服务器实际写出的 TCP 负载和节点 `/push`
上报的下载字节，而不是网卡或应用净载荷：

| 模式 | 目标写出字节 | 上报下载字节 | 进入 splice |
| --- | ---: | ---: | --- |
| VLESS，非 Vision | 16,802,497 | 16,802,497 | 否 |
| Vision，splice=auto | 16,802,497 | 6,078 | 是 |
| Vision，splice=disable | 16,802,497 | 16,802,497 | 否 |

Vision auto 组在客户端关闭后以及目标端强制 EOF 后各等待 8 秒，仍只有
6,078 字节。此数据说明存在真实漏记，并不说明所有业务固定少算 50%。
生产环境中 TLS 流量构成、持续时间和并发情况可能影响总体漏记比例；上述
测试没有测量用户服务器的具体业务构成。

## 修复策略

`getLink` 与 `DispatchLink` 在安装用户计数器和受管理的连接包装器时，都
设置 Xray 的已有禁止原始 splice 标志，不再取决于用户是否限速。
Vision 协议仍保留，数据逐段通过同一套计数和限速包装器；连接打开期间即可
累计并按原有周期上报。无需额外的环境变量，也不需要修改节点倍率或 Xboard。

这会放弃受计费管理连接的内核 TCP splice 优化，CPU 开销可能增加；未测量
具体服务器的吞吐上限。恢复此优化前，必须先让核心支持逐段计数以及受管理的
连接生命周期，而不能仅恢复最外层类型断言。

## 防回归

`core/xray/app/dispatcher/accounting_test.go` 检查两个 dispatcher 入口，覆盖
不限速和限速用户、上下行两轮计数清空、避免重复计费及在线设备半关闭行为。

`limiter/vision_accounting_test.go` 继续使用真实 TLS VLESS 链路和 Controller
HTTP 上报，要求两个下载阶段在连接未关闭时就完整计数，关闭后不重复计数，
并要求 Linux Vision auto 使用可计费复制路径。测试允许结束时最多 256 字节的
TLS 关闭记录差异；这是测试容差，不是生产计费扣减。

`vision-accounting.yml` 通过 `ci.yml` 被每次 push、PR 以及发布验证调用。
普通 VLESS、Vision auto、Vision disable 三组必须全部通过，发布流水线才能
继续构建。Windows 本地通过不能替代这项 Linux 验证。

已经漏记的历史字节没有保存在用户计数器中，升级无法精确追补；不要使用统一
翻倍或修改倍率来追补。上线后用相同时间窗口比较节点上报与业务流量，并保留
网卡重传、协议开销等统计口径差异。
