# AI Model Gateway

> 统一 AI API 接入层，整合多个上游提供商，5 层免费 fallback，零成本优先。
> **纯 Go 实现**，单二进制部署，零依赖。

[![Go Report](https://goreportcard.com/badge/github.com/fuweineng/ai-model-gateway)](https://goreportcard.com/report/github.com/fuweineng/ai-model-gateway)
[![License](https://img.shields.io/badge/License-MIT-blue)](LICENSE)

## ✨ 特性

- **统一端点** — 所有 agent 走 `:8650`，OpenAI 兼容 API
- **零成本优先** — 5 个免费上游试完再碰付费
- **智能路由** — 按角色（编码/推理/视觉/压缩）自动匹配模型层级
- **自动 fallback** — 上游不可用时无缝切换
- **健康检查** — 定期检查上游可用性
- **用量仪表盘** — 实时 Web UI 查看调用情况
- **单二进制** — Go 编译，Linux/macOS/Windows 全平台

## 🚀 快速开始

### Docker（推荐）

```bash
git clone https://github.com/fuweineng/ai-model-gateway.git
cd ai-model-gateway

# 编辑 .env，填入 API Key
cp .env.example .env

# 启动
docker compose up -d

# 测试
curl http://localhost:8650/v1/models
curl http://localhost:8650/v1/health
```

### 直接运行

下载对应平台的二进制，或自己编译：

```bash
# 编译
go build -o ai-model-gateway .

# 运行
./ai-model-gateway --config ai-model-gateway.yaml
```

## 📋 API

| 端点 | 方法 | 说明 |
|------|------|------|
| `/v1/models` | GET | 列出所有可用模型 |
| `/v1/health` | GET | 网关健康状态 |
| `/v1/health/upstream` | GET | 上游健康检查 |
| `/v1/usage` | GET | 用量统计 |
| `/v1/requests` | GET | 最近请求（仪表盘） |
| `/v1/chat/completions` | POST | 聊天补全（OpenAI 兼容） |
| `/v1/responses` | POST | Responses API 透传 |
| `/dashboard` | GET | Web 仪表盘 |

## 🧠 角色路由

通过 `X-Agent-Role` header 或 body 中的 `role` 字段指定角色：

| 角色 | 主模型 | Free Fallback | 付费 |
|------|--------|---------------|------|
| **hermes/decision** | glm-5.2 | zen → nemotron(免费) → LongCat(免费) | deepseek → volcengine |
| **coder/tester** | deepseek-v4-flash | zen → minimax(免费) → nemotron(免费) | deepseek → volcengine |
| **default** | deepseek-v4-flash | zen → minimax → nemotron → LongCat | deepseek → volcengine |

## 🔧 配置

编辑 `ai-model-gateway.yaml`，支持自定义：

- 上游 API 端点与模型
- 角色路由与 fallback 顺序
- 自动升级规则（`x-reasoning: deep` → 强推理模型）
- 安全认证与限速

## 🔒 安全

- API Key 通过环境变量注入，不写死
- 支持 Bearer Token 认证
- per-IP 滑动窗口限速
- 并发请求上限控制

## 🏗 项目结构

```
ai-model-gateway/
├── main.go                    # 入口
├── ai-model-gateway.yaml      # 配置模板
├── internal/
│   ├── config/               # 配置加载
│   ├── router/               # 模型路由
│   ├── upstream/             # 上游管理 + 健康检查
│   ├── handler/              # HTTP 处理器
│   ├── usage/                # 用量追踪
│   ├── auth/                 # 认证 + 限速
│   └── dashboard/            # Web 仪表盘
├── Dockerfile
├── docker-compose.yml
└── .env.example
```

## 📦 上游支持

| 上游 | 类型 | 说明 |
|------|------|------|
| opencode-go | 免费 | 主力，额度有限 |
| opencode-zen | 免费 | 无限量兜底 |
| NVIDIA NIM | **免费** | 中国手机号注册 |
| LongCat 龙猫 | **免费** | 美团，每日免费额度 |
| DeepSeek | 预付费 | 官方 API |
| 火山引擎 | 订阅 | 豆包系列 |
| Ollama | 本地 | 本机模型 |
| LM Studio | 本地 | 局域网大机 |
