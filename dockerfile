# ---------- 第一阶段：编译器镜像 ----------
FROM golang:1.20-alpine AS builder

# 安装 git（如果使用私有仓库或需要拉第三方库时）
RUN apk update && apk add --no-cache git

WORKDIR /app

# 把 go.mod、go.sum 先拷进去，下载依赖，加速 rebuild
COPY go.mod go.sum ./
RUN go mod download

# 拷贝项目所有源码
COPY . .

# 静态编译：关闭 CGO，目标平台 linux/amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o /reminder-bot

# ---------- 第二阶段：运行时镜像 ----------
FROM alpine:latest

# 如果代码里用到了时区或 HTTPS，安装 ca-certificates、tzdata
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /root/

# 把编译后的二进制、配置文件拷到运行时镜像
COPY --from=builder /reminder-bot .
COPY config.json .
# 如果你有已有的 reminder.json，也可以 COPY 过去，否则启动时会自动创建
# COPY reminder.json .

# 暴露端口（可选，telegram 轮询模式不需要开放端口）
# EXPOSE 8080

# 默认启动命令
ENTRYPOINT ["./reminder-bot"]