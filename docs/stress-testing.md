# 本地协议压力与断开后内存回收测试

测试入口为 `limiter/TestProxyStress`，默认跳过，必须显式设置 `V2BX_STRESS=1`。
只监听 127.0.0.1，面板为本进程内的模拟 Xboard HTTP 服务；不连接生产服务器。
Xray / sing 使用 VLESS TCP，原生 Hysteria2 使用 QUIC 承载 TCP。
并发表示同一授权用户的并发客户端连接，并非同等数量的不同用户。

## Linux 复现

使用 go.mod 对应的 Go 1.27.3，在仓库目录执行：

```bash
export GOEXPERIMENT=jsonv2
export GOFLAGS='-tags=http2legacy,sing,xray,hysteria2,with_quic,with_grpc,with_utls,with_wireguard,with_acme,with_gvisor'
export V2BX_STRESS=1
export V2BX_STRESS_SECONDS=8
export V2BX_STRESS_CONCURRENCY=32
export V2BX_STRESS_CHURN_RPS=1000
export V2BX_STRESS_REPORT="$PWD/proxy-stress.json"
go test -count=1 -timeout 3m -run '^TestProxyStress$' -v ./limiter
```

持久连接每次交换 64 KiB，短连接每次交换 4 KiB。短连接速率采用每个工作线程的间隔控制，
总目标不超过约 1,000 次/秒；实际值受计时精度和处理耗时影响。这是有界稳定性负载，不能宣称最大容量。
Hysteria2 短连接每次新建并关闭一个 QUIC 客户端会话，包含真实握手开销。
采样每 25 ms 读取并清零计费计数器，最终与成功交换的精确字节数核对。

## 保持节点运行，检查客户端断开后的回收

```bash
export V2BX_STRESS_SECONDS=3
export V2BX_STRESS_IDLE_SECONDS=15
export V2BX_STRESS_REPORT="$PWD/proxy-memory.json"
go test -count=1 -timeout 4m -run '^TestProxyStress$' -v ./limiter

# 同一节点连续三轮，节点只在最后一轮结束后关闭。
export V2BX_STRESS_IDLE_SECONDS=10
export V2BX_STRESS_ROUNDS=3
export V2BX_STRESS_REPORT="$PWD/proxy-memory-cycles.json"
go test -count=1 -timeout 3m -run '^TestProxyStress$/.*/churn$' -v ./limiter

# 一个核心自然空闲 120 秒，最后一个自然采样点前不手动 GC。
export V2BX_STRESS_IDLE_SECONDS=120
export V2BX_STRESS_ROUNDS=1
export V2BX_STRESS_REPORT="$PWD/proxy-memory-natural-xray.json"
go test -count=1 -timeout 3m -run '^TestProxyStress$/xray/churn$' -v ./limiter
```

可将最后一条的 xray 替换成 sing 或 hysteria2。
自然空闲时长允许 5–300 秒；检查自动 GC 的延迟时可改成 180 秒，并将测试 timeout 改为 4m。
每轮负载前做一次 GC 取得基线；随后关闭客户端，保留核心、入站、授权用户和后台任务，
自然空闲采样后才执行诊断性的 `runtime.GC()`、`debug.FreeOSMemory()`。
后两步仅在测试中执行，不能把它们当成生产环境自动回收速度。
多轮测试每轮都会执行这些诊断步骤，因此用于观察可达对象保留量，不能替代数小时的自然负载测试。

JSON 区分 HeapAlloc、HeapInuse、HeapIdle、HeapReleased、对象数、goroutine 和在线设备。
Windows 的 RSS 字段是进程 WorkingSetSize，Linux 是 /proc/self/statm 的驻留页；
二者均包含同一测试进程里的客户端、代理和 echo，不是服务端独占内存。
RSS 读取失败或平台不支持时为 0。HeapIdle 不等于已经归还给操作系统；
保留可复用空间和未触发新 GC 时的暂存对象不能直接判定为泄漏。
每轮只有 `node_closed=true` 才实际关闭节点；`goroutines_after_settle` 记录本轮回稳后的数量。

## 判定与边界

从 `v0.1.3-security.2` 起，短连接默认使用一个共享 ticker 按全局速率准入，
JSON 的 `churn_pacing` 为 `global-ticker`；时钟延迟时不会补发积压令牌。
早期测试的每线程固定间隔会让 128 个线程周期性齐发，属于重复握手突发，
不能把速率上限误读为平滑的 1000 次/秒。
设置 `V2BX_STRESS_BURST_CHURN=1` 可复现原模式（`synchronized-workers`）。
持久连接模式仍从同一闸门同时发起全部连接。任何连接失败继续使测试失败，未添加重试。
Linux CI 还会在同一节点连续执行五轮 128 并发的默认短连接稳定性测试。

`.2` 的五轮 Linux 测试仍出现 11/2/6/5/4 次失败，说明共享时钟本身不能
解决每秒重建大量完整 QUIC 会话的容量问题；其中一轮计费一致性检查也失败。
`.2` 未发布，原始结果保留于 run 41483389723 的工件。
从 `.3` 起，Hysteria2 的常规 churn 每个客户端复用一个 QUIC 会话，反复建立/关闭 TCP 流；
`session_mode=one-quic-session-per-worker`，`successful_quic_sessions` 单独记录会话数量。
该模式测试协议内复用，不能再把 successful_connections 解释为 QUIC 握手次数。
Xray/sing 的 TCP churn 不变；持久模式仍同步建立 128 个独立 QUIC 会话。
设置 `V2BX_STRESS_FRESH_QUIC=1` 可使用原来的每流新建 QUIC 会话；
再设置 `V2BX_STRESS_BURST_CHURN=1` 可恢复原周期握手突发。容量失败并未修复或掩盖，
生产队列和重试策略未改变。

请求错误、内容错误、计费字节不一致、自然空闲后仍有在线设备、最后关闭后 6 秒内 goroutine 未回到基线，均使测试失败。
延迟采用 0.1 ms 直方图，输出分位数是桶上界。客户端和服务端共享 CPU；
Windows 回环吞吐不能代表 VPS 带宽、Linux 网络栈或不同协议的公平性能排名。

当前实测 32 并发六场景通过；128 并发同步突发时，Hysteria2 持久会话有 3/128 次握手返回 CONNECTION_REFUSED。
该结果保留为失败，不增加客户端隐式重试掩盖错误。
后续在工作区复制的 QUIC 依赖上增加队列满诊断，先重复 3 轮，再重复 20 轮；
两批 128 并发持久连接均通过，未捕获到队列满日志。随后设置 `GOMAXPROCS=1`，
在同一进程里让客户端、服务端和 echo 争用一个 Go 执行线程，重复 10 轮，每轮负载 3 秒。
其中两轮分别捕获 2 条 `ACCEPT_QUEUE_FULL 32 32`，并各有 2 次 CONNECTION_REFUSED；
另有一轮捕获 6 条队列满日志并出现 6 次失败，保存的前 5 条错误均为超时。该受限场景证明突发负载可以填满
上游容量为 32 的 accept 队列并触发拒绝；不能把原始未插桩的三次失败逐一追溯到同一路径。
超时轮次服务端已处理部分未获客户端成功确认的字节，不能用仅成功交换的字节数判定漏计。
没有增大上游队列或移除保护；诊断依赖不进入发布源码。Linux CI 同时运行 32 并发、128 并发及三个核心各 180 秒自然回收观察。
其他 Windows 128 并发场景通过。不能据此宣称全部环境和负载均能通过。

客户端断开而节点保持运行的实测中，三个核心在 5 秒采样时在线设备清零，
180 秒采样时均观察到自然 GC 和驻留内存下降；120 秒采样时尚未新增 GC。
同一节点三轮诊断回收后的 HeapAlloc 未明显累积。
Xray 在约 1,000 次/秒短连接后的暂存内存较高，不能宣称断开后会立即回到最初工作集。

不覆盖真实生产面板、大量不同用户、UDP 数据报转发、TLS/REALITY 全部配置、异常断网/黑洞连接、
数小时泄漏趋势、Linux OOM/cgroup 行为或内核漏洞是否修复。

Linux [run 41473791934](https://github.com/rmomo5285-droid/V2bX/actions/runs/41473791934)
已通过全量测试、race、32/128 并发六场景、三个核心各 180 秒自然回收观察、升级回滚及 vet。
180 秒采样时在线设备均清零，自然 GC 次数均增加，RSS 均下降。完整数据见
`validation-linux-20261102.md`；JSON 及日志见该运行工件。Linux 本轮通过不会抹去 Windows 及单执行线程诊断的失败结果。

后续 Linux [run 41481256146](https://github.com/rmomo5285-droid/V2bX/actions/runs/41481256146)
在原模式的 Hysteria2 128 并发短连接中记录 10 次 CONNECTION_REFUSED，成功交换计费一致，
发布已被拦截。该结果说明此前一次通过不能保证这种周期突发在所有 runner 上稳定。
新模式的验证与原模式的容量失败分别保留，未修改生产 QUIC 队列大小或客户端重试。

## .5 发布验收的进程隔离与已知失败

`.4` 的发布运行曾通过，但同一提交两个独立 Linux 运行失败；该版本已移出
Latest 并改为预发布，详见 `release-cross-check-20261103.md`。不能用通过的
一次测试覆盖失败结果。

从 `.5` 起，CI 压力步骤设置 `V2BX_STRESS_ISOLATE_CLIENTS=1` 以及
`GOMAXPROCS=2`，客户端使用同一测试程序的子进程，父进程保留代理、模拟面板、
echo、计费采样及自然清理检查。128 并发、轮数、负载和零错误断言保持原值；
不存在失败后自动重试。`process_model` 明确记录进程口径；父进程内存、RSS 和
goroutine 不再包含客户端。因此此前含客户端的测量与新结果不可直接比较。

对照运行 41494156018 的独立客户端 GOMAXPROCS=2 共 15 轮全部通过；
独立客户端 GOMAXPROCS=1 有 14 次拒绝。同进程 GOMAXPROCS=1/2 组合也失败。
这类受限资源与集中起连容量问题未由生产代码修复；未扩大生产 QUIC 队列，
未改变生产运行时默认值，也不承诺 1 核 VPS 可稳定承接 128 个集中起连客户端。

原模式继续保留：不设置 `V2BX_STRESS_ISOLATE_CLIENTS` 即可运行同进程测试。
`stress-process-comparison.yml` 可手动运行四组对照。任何一组发生错误仍记为失败。
测试只覆盖本机回环和限定负载，不能替代真实生产面板、多用户及数小时观察。
