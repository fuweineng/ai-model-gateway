# 多阶段构建：Go 编译 → 最小镜像
FROM golang:1.26-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o ai-model-gateway .

# 运行镜像
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/ai-model-gateway .
COPY ai-model-gateway.yaml .
RUN mkdir -p /app/logs

EXPOSE 8650
CMD ["./ai-model-gateway", "--config", "ai-model-gateway.yaml"]
