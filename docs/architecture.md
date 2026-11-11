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
浏览器 POST /api/chat {conversationId?, prompt, stream?}
   │
   ├─ conversationId 为空 → 新建会话（title 由首条用户消息生成）
   ├─ 落库：messages(role=user)
   │
   ├─ llm.Client.Generate        （stream=false：等完整回答）
   │  或 llm.Client.GenerateStream（stream=true：边收边推 SSE）
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

### 流式的边界

- **写帧失败 = 客户端已断开**：`onDelta` 返回 error 会让 langchaingo 中断生成，
  避免继续白烧算力；已生成的部分仍会落库。
- **`ttftMs` 由服务端测量**（首个 delta 的时间），客户端还会再测一次并取更小值。
- 帧格式保持 OpenAI 兼容，前端只认 `choices[0].delta.content`。

两个实现细节：

- **写库用 `context.WithoutCancel`**：用户关页面时 `ctx` 会被取消，但请求已经发生、
  模型已经算完，用原 ctx 写库就是「钱花了，记录没了」。
- **Ollama 返回 NDJSON**：每行一个 JSON，必须逐行解析。多行 pretty-print 的 JSON
  会被 `bufio.Scanner` 拆碎，第一行 `{` 直接解析失败——写 mock 时踩过。

## 用量聚合

`GET /api/usage/summary?range=today|all` 直接从 `messages` 表算：

```
count / sum(tokens_in) / sum(tokens_out) / avg(latency_ms)
percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms)
GROUP BY model  → 按模型拆成本
ORDER BY created_at DESC LIMIT 5 → 最近调用
GROUP BY to_char(created_at,'MM-DD') → 近 7 天
```

- **不建汇总表**：数据量还小，实时聚合最简单且不会算错
- **成本公式只有一处**：`store.cost()`，前端只是展示
- 注意 `avg()` / `percentile_cont()` 返回 numeric，扫进 `int64` 会报错，必须 `::bigint`

## 文档 ETL

```
POST /api/documents → MinIO(原件) → documents 行(status=uploaded) → RPUSH etl:jobs
                                                                        │
   worker: BLPOP ──▶ 解析 ──▶ 切分 ──▶ 向量化 ──▶ 写 chunks ──▶ status=ready
                       │        │         │           │
                     stage=1  stage=2   stage=3     stage=4
```

- **状态落在数据库里**，不是内存：worker 崩了重启也不会丢进度
- **失败可重试**：`stage`/`status`/`error` 三列记录，`POST /retry` 重置后重新入队
- **替旧重来**：重试时先 `DELETE FROM chunks WHERE document_id = ...` 再插入，不会留下旧分块
- **队列用 Redis list** 而不是 Asynq：够用、零额外依赖，`BLPOP` 天然阻塞

## 检索

| 模式 | 实现 | 位置 |
| --- | --- | --- |
| bm25 | 内存倒排索引 + CJK bigram 分词 | `internal/search/bm25.go` |
| vector | pgvector 余弦 | `internal/store/vector.go` |
| hybrid | 两路 Top-20 → 加权分数融合（0.6 词法 + 0.4 向量） | `internal/search/fusion.go` |

- **中文没有空格**：用字符 bigram（`一线城市` → `一线/线城/城市`），
  英文数字按连续字符切。这是最省事的做法，代价是索引膨胀约 2 倍。
- **BM25 索引在内存里**，分块数变化时自动重建（`CountDocuments` 比对）。
  语料上千块后应该换成独立搜索引擎或 Postgres `tsvector`。
- **评测是真实的**：`internal/search/golden.go` 里 7 条「查询 → 期望文档」，
  `POST /api/retrieval/evaluate` 现场跑三种模式算 Recall@5 / MRR / NDCG@5。

> 实测结论：RRF 的 MRR 只有 0.65（低于纯 BM25 0.81），换成加权分数融合后回到 0.79。
> 教训是「名次融合」会抹平某一路的置信度优势，分数融合不会。

## 向量检索（RAG）

```
文档 → 分块（按段落，>300 字切） → nomic-embed-text(768) → chunks.embedding
查询 → 同一模型向量化 → ORDER BY embedding <=> $1 LIMIT k（余弦） → 拼进提示词
```

- **同库同事务**：`chunks` 表就在业务库里，向量检索和 ACL 过滤一条 SQL 完成
- **HNSW 索引**：`CREATE INDEX ... USING hnsw (embedding vector_cosine_ops)`
- **ACL 前置**：`WHERE c.tenant_id = $2` 在排序前生效，不是召回后再过滤
- **提示词刻意做简单**：小模型（qwen2:0.5b）对长指令遵循差，
  用「资料 + 问题 + 回答：」的补全格式反而答得准

> 踩过的坑：写库的 `context.WithTimeout` 不能在建流前创建——生成本身要 30s+，
> 等生成完再落库时上下文早就过期了。现在用户消息和助手消息各自创建超时。

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
| `OLLAMA_MODEL` | `qwen2:0.5b` | 对话模型（7B 需约 5G 内存） |
| `OLLAMA_EMBED_MODEL` | `nomic-embed-text` | 向量模型（768 维） |
| `POSTGRES_DSN` | `postgres://llm:llm@127.0.0.1:5432/llm?sslmode=disable` | 数据库 |
| `NOVA_TENANT` | `default` | 租户标识（当前单租户） |
| `REDIS_ADDR` | `127.0.0.1:6379` | 预留 |
| `MINIO_ENDPOINT` | `127.0.0.1:9000` | 预留 |

## 为什么是这些组件

- **pgvector**：文档向量和业务数据同库，省一套向量数据库，事务内可查。
- **Redis**：ETL 任务队列（list + BLPOP），后续可加语义缓存。
- **MinIO**：S3 兼容，存文档原件；线上换成 OSS/S3 只改 endpoint。
- **Ollama**：本地推理，断网可跑；线上可换任意 OpenAI 兼容端点。

## 已知取舍

| 项 | 现状 | 影响 |
| --- | --- | --- |
| 看板数据 | 总览/成本/检索页已接真实聚合；可观测、评测、安全仍是种子 | 剩余页面结构已定型，换 SQL 即可 |
| 检索策略 | BM25 + 向量 + 加权分数融合 | 混合与纯 BM25 持平，语料小时难以拉开差距 |
| 聊天 | 已支持 SSE 流式 | 首字约 0.2s；后续速度受模型与 CPU 限制 |
| 消息列表 | 一次拉全量 | 长会话会有压力，需要分页 |
| 多租户 | `tenant_id` 恒为 `default` | 没有鉴权，任何人都是同一租户 |
| 文档格式 | `md/txt/pdf/docx` | 扫描件 PDF 无文本层会明确报错 |
| 动作接口 | 评测/备份仍返回模拟结果 | 上传与 ETL 已是真实实现 |

## 下一步

- [x] `/api/chat` 改 SSE：首个 token 就渲染
- [x] 文档接入 MinIO + Redis 队列：ETL 五阶段
- [x] pgvector 检索：余弦相似度 + HNSW
- [x] 混合检索（BM25 + 向量 + RRF）
- [x] 加权分数融合（替代 RRF），MRR 从 0.65 回到 0.79
- [ ] rerank：需要交叉编码器，且要有更大的 Golden Set 才能证明有效
- [ ] 看板数据从内存种子换成真实查询（usage / trace / eval）
