# llm-gateway

多模型 LLM 网关：**Go 1.23 + Gin + langchaingo**，本地基础设施用 Docker Compose 一键起。

```
deploy/     PG(pgvector) + Redis + MinIO + Ollama
cmd/server  网关 HTTP 入口（:8080）
cmd/ollama-demo  最小可运行示例：调一次 Ollama 并打印 token 统计
internal/   配置、路由、LLM 客户端
pkg/token   token 用量统计
web/        前端产物目录
docs/       架构与运维文档
```

## 前置条件

- Go 1.23+
- Docker Desktop（已在本机安装：29.9.3 / Compose v5.7.0）
- 内存 ≥ 8G（跑 7B 模型约需 5G；默认用 0.5B 只需 1G）

> Docker Hub 在本机被 DNS 污染，已在 `~/.docker/daemon.json` 里配置镜像源
> `https://docker.m.daocloud.io`。换机器时记得同步这个配置。

## 快速开始

```bash
# 1. 起基础设施
cd deploy && cp .env.example .env && cd ..
make up

# 2. 等健康检查全部变绿
make ps            # 四个服务都应是 (healthy)

# 3. 拉模型（Ollama 镜像启动后是空的，不拉模型 /api/chat 会 404）
make pull              # 默认 qwen2:0.5b（352MB，秒级完成）
# 想用更大的模型：MODEL=qwen2:7b make pull，并 export OLLAMA_MODEL=qwen2:7b

# 4. 验证连通性
make verify
```

预期 `make ps` 输出类似：

```
NAME              IMAGE                        STATUS
llm-gw-postgres   pgvector/pgvector:pg16       Up (healthy)
llm-gw-redis      redis:7-alpine               Up (healthy)
llm-gw-minio      minio/minio:latest           Up (healthy)
llm-gw-ollama     ollama/ollama:latest         Up (healthy)
```

## 跑 Go 程序

```bash
make demo    # 调一次 Ollama，打印回答与 token
make run     # 起服务，:8080
```

`make demo` 输出：

```
model: qwen2:7b
answer: RAG 是检索增强生成……
tokens: prompt=23 completion=48 total=71
```

服务接口：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 存活探针 |
| GET | `/readyz` | 就绪探针（真的打一次 Ollama） |
| GET | `/api/version` | 服务与模型信息 |
| POST | `/api/chat` | `{"prompt":"..."}` → 回答 + token 用量 |

```bash
curl -s localhost:8080/api/chat \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"用一句话解释什么是 RAG"}'
```

## 端口

| 服务 | 端口 | 说明 |
| --- | --- | --- |
| Go 服务 | 8080 | HTTP API |
| PostgreSQL | 5432 | pgvector 已内置 |
| Redis | 6379 | |
| MinIO | 9000 | S3 API（`minioadmin` / `minioadmin`） |
| Ollama | 11434 | |

全部只绑定 `127.0.0.1`。

## 数据库

会话与消息都落在 PostgreSQL 16（用的是 `pgvector/pgvector:pg16` 镜像，向量扩展已装好）。

```
postgres://llm:llm@127.0.0.1:5432/llm?sslmode=disable
```

### 配置分三层（容易搞混）

| 层 | 位置 | 作用对象 |
| --- | --- | --- |
| Docker 容器 | `deploy/.env` 的 `POSTGRES_USER/PASSWORD/DB/PORT` | 决定容器**初始化**成什么库 |
| Go 服务 | 环境变量 `POSTGRES_DSN` | 决定服务连到哪 |
| 代码默认值 | `internal/config/config.go` | 没设环境变量时兜底 |

**这两层不会自动同步。** 只改 `deploy/.env` 里的密码，容器密码变了，
但 Go 仍拿默认 DSN 去连，会直接认证失败。要么两处一起改，要么覆盖变量：

```bash
POSTGRES_DSN='postgres://user:pass@127.0.0.1:5432/db?sslmode=disable' make run
```

### 连接参数

| 字段 | 值 |
| --- | --- |
| Host | `127.0.0.1`（只绑本机，局域网不可达） |
| Port | `5432` |
| Database | `llm` |
| User | `llm` |
| Password | `llm` |
| SSL | `disable` |

### 三种连法

```bash
# ① 容器内 psql（最省事）
docker compose -f deploy/docker-compose.yml exec postgres psql -U llm -d llm

# ② 图形客户端：按上表填，注意选 PostgreSQL 类型、SSL 不勾
# ③ Go 代码：读 POSTGRES_DSN 环境变量
```

### 表结构

| 表 | 用途 | 关键字段 |
| --- | --- | --- |
| `conversations` | 会话 | `id, tenant_id, title, model, preview, updated_at` |
| `messages` | 消息 | `id, conversation_id, role, content, tokens_in/out, latency_ms` |
| `schema_migrations` | 迁移记录 | 记录哪些 SQL 已执行 |

```sql
-- 最近 10 个会话
SELECT id, title, preview, updated_at FROM conversations ORDER BY updated_at DESC LIMIT 10;

-- 某个会话的完整对话
SELECT role, content, tokens_in, tokens_out, latency_ms
FROM messages WHERE conversation_id = '<uuid>' ORDER BY created_at;
```

### 迁移是自动的

SQL 用 `//go:embed` 编进二进制（`internal/store/migrations/`），服务启动时按文件名顺序执行，
已执行的记在 `schema_migrations` 里。部署只需要一个二进制，不用在服务器上跑 migrate 命令。

### 一个坑：改了 `.env` 不生效

Postgres 只在**数据卷为空时**初始化一次。改了 `deploy/.env` 里的用户名/密码/库名，
容器里还是旧的，必须删卷重建：

```bash
docker compose -f deploy/docker-compose.yml down -v   # -v 会删数据，慎用
```

### 数据存在命名卷里

```
llm-gateway_pgdata      ← 数据库文件
llm-gateway_redisdata
llm-gateway_miniodata
llm-gateway_ollamadata
```

`make down` 只停容器、保留数据；`down -v` 才会连数据一起删。

### 会话接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/conversations` | 列表（带消息条数） |
| POST | `/api/conversations` | 新建 |
| GET | `/api/conversations/{id}` | 会话 + 全部消息 |
| PATCH | `/api/conversations/{id}` | 改名 |
| DELETE | `/api/conversations/{id}` | 删除（消息级联） |

`POST /api/chat` 接受 `{conversationId?, prompt, stream?}`：

- `stream: false`（默认）—— 等完整回答再返回 JSON
- `stream: true` —— SSE 逐字推送，帧格式是 OpenAI 兼容的 chunk，
  最后一帧带 `usage` 与 `ttftMs`，然后是 `[DONE]`

不传 `conversationId` 会自动建会话（id 通过 `X-Conversation-Id` 响应头带回来）；
用户消息先落库，回答完成后助手消息再落库；中途断开也会保存已生成的部分。

```bash
curl -N -X POST localhost:8080/api/chat \
  -H 'Content-Type: application/json' \
  -d '{"prompt":"用三句话介绍杭州","stream":true}'
```

## 前端

`web/` 放的是 [llm-gateway-ui](../llm-gateway-ui) 的构建产物，由 Go 同源托管（无 CORS）。

```bash
# 改完 UI 后重新部署
cd ../llm-gateway-ui && npm run build
rm -rf ../llm-gateway/web/* && cp -R dist/* ../llm-gateway/web/
```

打开 <http://127.0.0.1:8080/>。12 个页面启动时先 `GET /api/bootstrap` 拉数据，
失败会回落到本地样例（控制台打印「离线模式」），页面仍可演示。

已接通的链路：智能问答（真实回答 + token + 落库）、知识库、AI 网关、可观测、
部署运维、评测中心、安全与租户、设计规范；成本页表格与总览服务/告警也已接入，
少数指标卡和证据链卡片仍是静态样例。

## 测试

```bash
make test
```

单元测试用 `httptest` 起一个假 Ollama，**不需要真的装 Ollama**，覆盖了：
token 字段解析、回答透传、上游 4xx 错误、`/api/tags` 探活。

## 常见问题

| 现象 | 原因 | 解决 |
| --- | --- | --- |
| `/api/chat` 返回 404 `model not found` | Ollama 里没有该模型 | `make pull`，或 `MODEL=llama3 make pull` |
| `readyz` 一直 503 | Ollama 还没起来 | 等 healthcheck 变绿，或看 `make logs` |
| 端口被占用 | 本机已有 PG/Redis | 改 `deploy/.env` 里的 `*_PORT` |
| 找不到 MinIO 镜像 | 官方 `minio/minio` 已从 Docker Hub 下架 | 本仓库用 `bitnamilegacy/minio`，是同一份 MinIO 二进制 |
| MinIO 没有 Web 控制台 | 该镜像未内置 embedded console | 用 `mc`：`docker compose exec minio mc alias set local http://127.0.0.1:9000 minioadmin minioadmin && mc ls local` |

## 已验证（2026-11-11）

在本机实际跑通：

```
$ docker compose ps
llm-gw-minio      bitnamilegacy/minio:latest   Up (healthy)
llm-gw-ollama     ollama/ollama:latest         Up (healthy)
llm-gw-postgres   pgvector/pgvector:pg16       Up (healthy)
llm-gw-redis      redis:7-alpine               Up (healthy)

$ make verify
  ✓ minio      http://127.0.0.1:9000/minio/health/live
  ✓ ollama     http://127.0.0.1:11434/api/tags
  ✓ postgres   127.0.0.1:5432
  ✓ redis      127.0.0.1:6379
全部就绪。

$ make demo
model: qwen2:0.5b
answer: RAG 是一个用于处理和分析大数据的工具……
tokens: prompt=14 completion=23 total=37
```

会话持久化（浏览器里发消息 → 刷新 → 数据还在）：

```
1) 侧栏会话数: 1   标题: 用一句话说明什么是RAG   消息数: 2
2) 发送后消息数: 4
3) 刷新后消息数: 4        ← 来自 PostgreSQL

$ docker compose exec postgres psql -U llm -d llm -c \
    "select c.title, count(m.id) from conversations c left join messages m on m.conversation_id=c.id group by c.id, c.title;"
         title         | count
-----------------------+-------
 用一句话说明什么是RAG |     4
```

## 文档

- [docs/architecture.md](docs/architecture.md) — 分层、数据流、会话持久化、前端接入、已知取舍
- [docs/api-contract.md](docs/api-contract.md) — 12 个页面的接口契约与实现状态
