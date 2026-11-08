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

在原有版本的 Xray fork 上增加窄范围补丁，源码固定在 `third_party/xray-core`。
计数器通过可选接口穿过透明的在线追踪包装器；Linux Vision 改用已有 sing
复制实现，在每批成功 splice 写入后更新用户计数器和空闲计时器。长连接未关闭
即可上报，不再把活动连接空闲超时强行延长到 24 小时。

不限速用户保留内核 TCP splice。限速和未知包装器继续经过 reader/writer，
避免旁路限速及其他语义。没有修改倍率，也没有恢复只能在 EOF 后计数的路径。
本地固定的 sing 版本另补部分写入后报错的计数，覆盖实际已转发字节。
性能差异仍需以同一资源、同一负载的 Linux auto/disable 对照测量为准。

连接管理同时改用原子 LoadOrStore，避免同用户并发首连创建多个互相覆盖的
管理器；连接上下文结束时移除注册。用户撤销时关闭原始连接，中断路径能穿过
TimeoutWrapperReader。CounterReader 保留与 EOF 同时返回的有效数据及调用方
超时时间。相关测试验证 150 条并发注册可全部撤销、协议 writer 不可关闭时
也不遗留注册。

## 防回归

`core/xray/app/dispatcher/accounting_test.go` 检查两个 dispatcher 入口，覆盖
不限速和限速用户、上下行两轮计数清空、避免重复计费及在线设备半关闭行为。

`limiter/vision_accounting_test.go` 继续使用真实 TLS VLESS 链路和 Controller
HTTP 上报，要求两个下载阶段在连接未关闭时就完整计数，关闭后不重复计数，
并要求 Linux Vision auto 使用逐批计数的 splice，strace 另验证实际成功的内核
splice 系统调用。测试允许结束时最多 256 字节的
TLS 关闭记录差异；这是测试容差，不是生产计费扣减。

`vision-accounting.yml` 通过 `ci.yml` 被每次 push、PR 以及发布验证调用。
普通 VLESS、Vision auto、Vision disable 三组必须全部通过，发布流水线才能
继续构建。Windows 本地通过不能替代这项 Linux 验证。

## 节点黄色与在线人数

本次检视的 Xboard 源码中，节点黄色表示流量 push 超过 5 分钟未更新，不能
直接解释成代理转发进程已停止。节点列表的在线人数来自最近一次流量批次的
用户行数；它与设备/IP 心跳的存活连接口径不同。多个入口共用落地也不能用
网卡下行数直接推导每个节点的活跃用户数。

`vision-capacity.yml` 在 Linux 1 CPU/1 GiB 容器中跑 150/300 用户、每用户
4 条 Vision 下载连接，auto/disable 两组，并分别关闭日志或叠加 accesslog
慢响应、超时、队列满和恢复。每组复用同一节点跑三轮，记录 push 年龄、在线
设备心跳、管理器注册、goroutine、内存、cgroup OOM 和日志投递状态。
测试客户端、HTTPS 目标、模拟面板都在同一容器；结果不能当成真实 VPS 的
容量承诺。Linux 修复版结果需以实际 Actions 运行及对应提交为准。

已经漏记的历史字节没有保存在用户计数器中，升级无法精确追补；不要使用统一
翻倍或修改倍率来追补。上线后用相同时间窗口比较节点上报与业务流量，并保留
网卡重传、协议开销等统计口径差异。
