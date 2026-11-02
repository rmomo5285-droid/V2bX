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

请求错误、内容错误、计费字节不一致、自然空闲后仍有在线设备、最后关闭后 6 秒内 goroutine 未回到基线，均使测试失败。
延迟采用 0.1 ms 直方图，输出分位数是桶上界。客户端和服务端共享 CPU；
Windows 回环吞吐不能代表 VPS 带宽、Linux 网络栈或不同协议的公平性能排名。

当前实测 32 并发六场景通过；128 并发同步突发时，Hysteria2 持久会话有 3/128 次握手返回 CONNECTION_REFUSED。
该结果保留为失败，不增加客户端隐式重试掩盖错误。
后续在工作区复制的 QUIC 依赖上增加队列满诊断，先重复 3 轮，再重复 20 轮；
两批 128 并发持久连接均通过，未捕获到队列满日志。该诊断构建不进入发布依赖；
没有复现不等于证明原始失败原因。Linux CI 同时运行 32 并发、128 并发及三个核心各 180 秒自然回收观察。
依赖中的 QUIC accept 队列容量为 32，队列满会使用相同错误拒绝；这是匹配的可能原因，尚未用运行时插桩证明。
其他 128 并发场景通过。不能据此宣称全部高并发验收完成。

客户端断开而节点保持运行的实测中，三个核心在 5 秒采样时在线设备清零，
180 秒采样时均观察到自然 GC 和驻留内存下降；120 秒采样时尚未新增 GC。
同一节点三轮诊断回收后的 HeapAlloc 未明显累积。
Xray 在约 1,000 次/秒短连接后的暂存内存较高，不能宣称断开后会立即回到最初工作集。

不覆盖真实生产面板、大量不同用户、UDP 数据报转发、TLS/REALITY 全部配置、异常断网/黑洞连接、
数小时泄漏趋势、Linux OOM/cgroup 行为或内核漏洞是否修复。Linux CI 增加有界压力步骤和 JSON 工件，
仍须仓库 Actions 开启后实际运行。
