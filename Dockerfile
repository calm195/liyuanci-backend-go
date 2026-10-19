# T004/REQ-020：锁文件构建API、worker与探针；不在构建中重写依赖。
FROM golang:1.25.0-alpine@sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440 AS builder
WORKDIR /src
COPY go.mod go.sum ./
# T004/REQ-020：本地官方代理超时，默认使用已验证可达的下载代理；可由构建参数覆盖。
# 仅改变下载路径，go.sum与公共校验数据库保持启用。
ARG GOPROXY=https://goproxy.cn,direct
RUN GOPROXY="$GOPROXY" go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /artifacts/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -o /artifacts/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -o /artifacts/probe ./cmd/probe
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
WORKDIR /app
COPY --from=builder /artifacts/ ./
COPY configs/config.yaml configs/integration.yaml ./configs/
USER app
EXPOSE 8080
ENTRYPOINT ["./server"]
