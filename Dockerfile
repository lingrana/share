# 多阶段构建：编译 + 运行
FROM golang:1.22-alpine AS builder

WORKDIR /app

# 安装 git 和 gcc（SQLite 编译需要）
RUN apk add --no-cache git gcc musl-dev

# 先复制依赖文件，利用 Docker 缓存
COPY go.mod go.sum ./
RUN go mod download

# 复制源码并编译
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o /app/share-go -ldflags="-s -w" .

# 运行阶段
FROM alpine:3.19

# 安装 ca-certificates（HTTPS）和 tzdata（时区）
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# 从构建阶段复制二进制
COPY --from=builder /app/share-go /app/share-go

# 创建数据目录
RUN mkdir -p /app/data

# 暴露端口
EXPOSE 8080

# 环境变量
ENV SHARE_DATA=/app/data
ENV TZ=Asia/Shanghai

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:8080/health || exit 1

# 启动
CMD ["/app/share-go"]
