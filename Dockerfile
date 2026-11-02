# Build go
FROM golang:1.27.3-alpine AS builder
WORKDIR /app
COPY . .
ENV CGO_ENABLED=0
RUN GOEXPERIMENT=jsonv2 go mod download
RUN GOEXPERIMENT=jsonv2 go build -v -o V2bX -tags "http2legacy sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor"

# Release
FROM  alpine
# 安装必要的工具包
RUN  apk --update --no-cache add tzdata ca-certificates \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime
RUN mkdir /etc/V2bX/
COPY --from=builder /app/V2bX /usr/local/bin

COPY LICENSE /usr/share/doc/V2bX/LICENSE
COPY third_party/README.md /usr/share/doc/V2bX/third_party/README.md
COPY third_party/sing-box/LICENSE /usr/share/doc/V2bX/third_party/sing-box-LICENSE
COPY third_party/quic-go/LICENSE /usr/share/doc/V2bX/third_party/quic-go-LICENSE
COPY docs/security-mptcp.md docs/upstream-integration.md /usr/share/doc/V2bX/

ENTRYPOINT [ "V2bX", "server", "--config", "/etc/V2bX/config.json"]
