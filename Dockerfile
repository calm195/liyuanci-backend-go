# 锁文件构建API、worker与探针；不在构建中重写依赖。
FROM golang:1.25.0-alpine@sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440 AS builder
WORKDIR /src
COPY go.mod go.sum ./
# 本地官方代理超时，默认使用已验证可达的下载代理；可由构建参数覆盖。
# 仅改变下载路径，go.sum与公共校验数据库保持启用。
ARG GOPROXY=https://goproxy.cn,direct
RUN GOPROXY="$GOPROXY" go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /artifacts/content-worker ./cmd/content-worker && \
    CGO_ENABLED=0 go build -trimpath -o /artifacts/card-worker ./cmd/card-worker && \
    CGO_ENABLED=0 go build -trimpath -o /artifacts/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -o /artifacts/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -o /artifacts/probe ./cmd/probe && \
    CGO_ENABLED=0 go build -trimpath -o /artifacts/migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -o /artifacts/admin-init ./cmd/admin-init && \
    CGO_ENABLED=0 go build -trimpath -o /artifacts/dev-init ./cmd/dev-init
# 隔离业务往返使用真实领域测试二进制，测试代码不进入普通运行镜像。
FROM builder AS business-test-build
RUN CGO_ENABLED=0 go test -c -o /artifacts/business.test ./internal/studentcard && \
    CGO_ENABLED=0 go test -c -o /artifacts/durable.test ./internal/durable && \
    CGO_ENABLED=0 go test -c -o /artifacts/moderation.test ./internal/moderation && \
    CGO_ENABLED=0 go test -c -o /artifacts/wish.test ./internal/wish
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8 AS business-test
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
COPY --from=business-test-build /artifacts/business.test /artifacts/durable.test /artifacts/moderation.test /artifacts/wish.test /
COPY --from=builder /artifacts/card-worker /card-worker
COPY --from=builder /artifacts/content-worker /content-worker
USER app
ENTRYPOINT ["/business.test"]
FROM alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app
RUN mkdir -p /var/lib/liyuanci/objects && chown app:app /var/lib/liyuanci/objects && chmod 700 /var/lib/liyuanci/objects
WORKDIR /app
COPY --from=builder /artifacts/ ./
COPY configs/app ./configs/app
USER app
EXPOSE 8080
ENTRYPOINT ["./server"]
