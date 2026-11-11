# 架构说明

## 分层

```
  浏览器（Vue 构建产物，由 Go 托管在 web/）
        │  /api/*            ▲ 静态资源
        ▼                    │
┌───────────────────────────────────────────────┐
│ cmd/server            HTTP 入口（Gin）         │
├───────────────────────────────────────────────┤
│ internal/httpapi      路由 / 探针 / 会话 / 动作 │
│ internal/store        PostgreSQL：会话与消息    │
│ internal/data         看板数据（当前为内存种子）│
│ internal/llm          Ollama 客户端封装        │
│ internal/config       环境变量配置             │
├───────────────────────────────────────────────┤
│ pkg/token             token 用量统计           │
└───────────────────────────────────────────────┘
        │                    │
        ▼                    ▼
┌──────────────┐   ┌──────────────────────────────────┐
│ PostgreSQL   │   │  deploy/: Redis MinIO Ollama      │
│ (pgvector)   │   │  （Redis/MinIO 已起，尚未接入代码）│
└──────────────┘   └──────────────────────────────────┘
```

- `internal/` 不对外暴露；`pkg/token` 是可被外部引用的通用能力
- `web/` 是前端构建产物，`docs/` 放设计文档
- **没有 CORS 中间件**：UI 由 Go 同源托管，天然不存在跨域

## 端口

| 服务 | 端口 | 用途 |
| --- | --- | --- |
| Go 服务 | 8080 | HTTP API + 静态资源 |
| PostgreSQL | 5432 | 会话/消息 + 向量（pgvector） |
| Redis | 6379 | 预留：缓存 / 异步队列 |
| MinIO | 9000 | 预留：对象存储 |
| Ollama | 11434 | 本地推理 |

全部只绑 `127.0.0.1`。

## 一次问答的数据流

```
浏览器 POST /api/chat {conversationId?, prompt}
   │
   ├─ conversationId 为空 → 新建会话（title 由首条用户消息生成）
   ├─ 落库：messages(role=user)
   │
   ├─ llm.Client.Generate
   │     └─ langchaingo → Ollama POST /api/chat  (NDJSON)
   │     ← 回答 + GenerationInfo{PromptTokens, CompletionTokens}
   │
   ├─ pkg/token 归一化成 Stats{Prompt, Completion}
   ├─ 落库：messages(role=assistant, tokens_in/out, latency_ms)
   └─ 更新会话 preview / updated_at
   │
   ▼
200 {conversationId, answer, usage, model, latencyMs}
```

两个实现细节：

- **写库用 `context.WithoutCancel`**：用户关页面时 `ctx` 会被取消，但请求已经发生、
  模型已经算完，用原 ctx 写库就是「钱花了，记录没了」。
- **Ollama 返回 NDJSON**：每行一个 JSON，必须逐行解析。多行 pretty-print 的 JSON
  会被 `bufio.Scanner` 拆碎，第一行 `{` 直接解析失败——写 mock 时踩过。

## 会话持久化

| 表 | 关键字段 |
| --- | --- |
| `conversations` | `id, tenant_id, title, model, preview, created_at, updated_at` |
| `messages` | `id, conversation_id, role, content, model, tokens_in, tokens_out, latency_ms` |
| `schema_migrations` | 已执行的迁移版本 |

设计取舍：

- **消息只追加**：`ON DELETE CASCADE` 跟随会话删除，不做软删。
- **`preview` 冗余在会话上**：否则侧栏每行都要子查询最后一条消息。
- **`tokens_in/out/latency_ms` 落库**：后面做成本归因和 P95 直接查，不用回溯日志。
- **`tenant_id` 现在就留着**：恒为 `default`，加团队时不用改表。

### 迁移

SQL 用 `//go:embed` 编进二进制，启动时按文件名顺序执行，已执行的记在
`schema_migrations`。部署只有一个二进制，不需要在服务器上跑 migrate 命令。

### 连接

`store.Open` 内置 30 次重试（每次 1 秒）。`docker compose up` 时 API 可能比 PG 先就绪，
没有这个重试就是启动即崩。

## 前端接入

`web/` 放 `llm-gateway-ui` 的构建产物，启动流程：

```
main.ts
  └─ hydrate()  → GET /api/bootstrap（12 个页面的数据）
                 失败则回落到 src/data/mock.ts，控制台打印「离线模式」
  └─ createApp().mount()
```

- 看板类数据（文档、provider、指标、审计…）走 `/api/bootstrap` 一次性拉取
- 会话类数据走 `/api/conversations*`
- 聊天走 `POST /api/chat`

**当前实现是「模块级注入」**：`mock.ts` 里把常量改成 `export let`，`hydrate()` 重新赋值，
页面在 setup 时读到新值。这是最小改动方案，下一步会换成每页一个 Pinia store。

## 配置项

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `SERVER_ADDR` | `:8080` | 监听地址 |
| `OLLAMA_BASE_URL` | `http://127.0.0.1:11434` | Ollama 地址 |
| `OLLAMA_MODEL` | `qwen2:0.5b` | 默认模型（7B 需约 5G 内存） |
| `POSTGRES_DSN` | `postgres://llm:llm@127.0.0.1:5432/llm?sslmode=disable` | 数据库 |
| `NOVA_TENANT` | `default` | 租户标识（当前单租户） |
| `REDIS_ADDR` | `127.0.0.1:6379` | 预留 |
| `MINIO_ENDPOINT` | `127.0.0.1:9000` | 预留 |

## 为什么是这些组件

- **pgvector**：文档向量和业务数据同库，省一套向量数据库，事务内可查。
- **Redis**：预留给 ETL 任务队列（Asynq）与语义缓存，都是短生命周期数据。
- **MinIO**：S3 兼容，本地开发等同云上对象存储，存原始文档与解析中间产物。
- **Ollama**：本地推理，断网可跑；线上可换任意 OpenAI 兼容端点。

## 已知取舍

| 项 | 现状 | 影响 |
| --- | --- | --- |
| 看板数据 | 内存种子（`internal/data`） | 重启回到初始值；结构已定型，换 SQL 即可 |
| 聊天 | 非流式，等完整回答 | 0.5B 约 0.7s；7B 要十几秒才出字 |
| 消息列表 | 一次拉全量 | 长会话会有压力，需要分页 |
| 多租户 | `tenant_id` 恒为 `default` | 没有鉴权，任何人都是同一租户 |
| 动作接口 | 返回模拟结果 | 上传/评测/备份尚未真正执行 |

## 下一步

- [ ] `/api/chat` 改 SSE：首个 token 就渲染
- [ ] 文档接入 MinIO + Redis 队列：ETL 五阶段
- [ ] pgvector 检索：混合检索 + rerank
- [ ] 看板数据从内存种子换成真实查询（usage / trace / eval）
