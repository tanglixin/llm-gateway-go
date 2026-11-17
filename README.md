# LLM Gateway · 企业级 RAG 网关

> 一个能跑起来的完整闭环：**文档进 → 向量检索 → RAG 问答（流式）→ Agent 工具调用 → 可观测与评测**。
> Go 1.23 + Gin + langchaingo + PostgreSQL(pgvector)，前端 Vue 3 由同一个二进制托管。

不是 demo 页面拼出来的壳子——**每个数字都来自真实请求**：

| 能力 | 实现 | 数据来源 |
| --- | --- | --- |
| 混合检索 | BM25 + pgvector 余弦 + 加权融合 | 真实语料 13 个分块 |
| 文档 ETL | MinIO 存原件 → Redis 队列 → 解析/切分/向量化/入库 | PDF / DOCX / MD / TXT |
| RAG 问答 | 检索 top-3 拼进提示词，SSE 逐字返回 | 首字约 0.2s |
| Agent | ReAct 循环 + 5 个真实工具 + 数字溯源校验 | 每次工具调用都落 trace |
| 多 provider 网关 | 加权轮询 + 熔断 + 故障转移 | 失败自动切换 |
| 安全 | JWT + 租户隔离（行级过滤）+ RBAC | 越权返回 404 |
| 可观测 | OpenTelemetry span 写 PostgreSQL | 真实 waterfall |
| 评测 | 4 个 prompt 版本 × 7 条 Golden Set | Recall 低于 0.80 闸门阻断 |

## 架构

```
浏览器（Vue 3，由 Go 同源托管，无 CORS）
    │  JWT
    ▼
┌──────────────────────────────────────────────────┐
│ cmd/server  Gin                                   │
│  ├─ middleware    auth / RBAC / OTel / 租户        │
│  ├─ httpapi       chat · conversations · docs      │
│  │                retrieval · agent · eval · ops   │
│  ├─ gateway       多 provider 加权路由 + 熔断        │
│  ├─ agent         ReAct + 5 工具 + 溯源             │
│  ├─ search        BM25（CJK bigram）+ 分数融合      │
│  ├─ store         PostgreSQL：会话/文档/span/评测   │
│  └─ tracing       OTel SDK + 自建 PG exporter      │
└──────────────────────────────────────────────────┘
    │              │            │           │
  PG(pgvector)   Redis       MinIO      Ollama
```

## 快速开始

```bash
make up      # 起 PG(pgvector) / Redis / MinIO / Ollama，四个都带健康检查
make pull    # 拉模型（qwen2:0.5b + nomic-embed-text）
make run     # 起服务
open http://127.0.0.1:8080/
```

首次启动会自动迁移表结构、灌入 3 份示例文档（含向量）、写入演示账号和 4 个 prompt 版本。

**演示账号**（密码都是 `gateway`）：

| 账号 | 租户 | 角色 | 看到 |
| --- | --- | --- | --- |
| `tang@acme.dev` | `acme-rd` | admin | 10 份文档、全部功能 |
| `demo@acme.dev` | `demo-guest` | member | 空租户，管理按钮禁用 |

## 3 分钟能看完什么

完整脚本见 [docs/demo.md](docs/demo.md)。最小路径：

1. **登录** → `tang@acme.dev`，看顶栏的租户与角色
2. **知识库** → 上传一份 `.md`，看 ETL 五阶段实时跑完（约 2 秒）
3. **检索调试台** → 同一句话在 BM25 / 向量 / 混合三种模式下对比
4. **智能问答** → 逐字流式回答 + 可点击的引用卡
5. **Agent 工作台** → 真实 ReAct：检索 → 调工具 → 终答 + 数字溯源徽标
6. **可观测** → Trace 瀑布图（真实 span）与审计流水

## 实测数据（本机 Intel Mac，CPU 推理）

```
问答：首字 212ms，全文 30s（qwen2:0.5b 约 1 token/s）
检索：BM25 1ms · 向量 2ms · 混合 10ms（不含 embedding 439ms）
评测：4 个版本跑完 20s，v4 召回 0.714 被闸门阻断
trace：一次问答 3 个 span，根 span 18574ms 中生成占 16572ms
```

## 已知取舍

| 项 | 现状 | 原因 |
| --- | --- | --- |
| 模型 | qwen2:0.5b | CPU 推理，7B 要等几分钟；Agent 规划因此加了规则兜底 |
| 评测 | 只算检索指标 | Faithfulness 需要 LLM 打分，慢且不稳，宁可不做 |
| 部署运维 | backup/restore 是模拟 | 真做要动 `pg_dump`，演示价值低风险高 |
| 行级隔离 | 应用层 WHERE | 没开 Postgres RLS，直连库可绕过 |
| 多租户 | tenant_id 从 token 取 | 无刷新 token、无吊销 |

---

# 工程细节

以下是最初的工程笔记，按主题记录，包含踩坑与实现细节。

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

## 详细启动步骤

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

### 用量与成本（真实数据）

```bash
curl -s 'localhost:8080/api/usage/summary?range=all'   # 或 range=today
```

从 `messages` 表实时聚合：调用次数、prompt/completion token、按模型拆分、
平均与 P95 延迟、最近 5 次调用、近 7 天分布。成本按 `prompt ¥0.14/M + completion ¥0.28/M` 估算。

总览页与成本页的 KPI、成本页的「最近调用」表都来自这个接口，不是样例数字。

### 鉴权与租户隔离

所有 `/api/*`（除登录）都需要 `Authorization: Bearer <token>`。

```bash
# 登录（两个演示账号，密码都是 gateway）
curl -s -X POST localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"tang@acme.dev","password":"gateway"}'

TOKEN=...   # 从响应里取
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/api/conversations
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/api/auth/me
```

| 账号 | 租户 | 角色 | 能看到 |
| --- | --- | --- | --- |
| `tang@acme.dev` | `acme-rd` | admin | 6 份文档、21 个会话 |
| `demo@acme.dev` | `demo-guest` | member | 空（只有自己新建的） |

隔离是**应用层**做的：所有按 ID 的操作都带 `AND tenant_id = $n`，
跨租户一律返回 404（不是 403，避免泄露 id 是否存在）。有回归测试兜底：

```bash
TEST_POSTGRES_DSN='postgres://llm:llm@127.0.0.1:5432/llm?sslmode=disable' \
  go test ./internal/store/ -run Isolation -v
```

> 这个测试是有来历的：第一版只做了列表过滤，`GET /api/conversations/{id}`
> 竟然能让 demo 租户读到 acme-rd 的会话，`DELETE` 还能删掉。写测试时才发现。

### 角色（RBAC）

| 接口 | 角色要求 |
| --- | --- |
| `POST /api/gateway/drill` | admin |
| `POST /api/eval/run` | admin |
| `POST /api/ops/backup` / `restore` | admin |
| `POST /api/documents`（上传） / `{id}/retry` | admin |
| 其余读写接口 | 任意登录用户 |

```bash
# member 访问管理接口 → 403
curl -s -X POST -H "Authorization: Bearer $MEMBER_TOKEN" localhost:8080/api/gateway/drill -d '{}'
{"error":{"code":"forbidden","message":"需要 admin 角色，当前是 member"}}
```

角色判断放在**路由表**上（`s.jwt.RequireRole("admin")`），不是散在 handler 里——
加接口时一眼能看出哪些是管理员的，漏加也容易发现。
前端同步把按钮禁掉，但**真正拦住的是后端**。

### AI 网关：多 provider 路由

```bash
curl -s localhost:8080/api/gateway/status
curl -s -X POST localhost:8080/api/gateway/drill -d '{"enable":true,"provider":"本地 Ollama"}'
```

用 `GATEWAY_PROVIDERS`（JSON）配置，不配就是单个 Ollama：

```json
[
  {"name":"本地 Ollama","kind":"ollama","baseUrl":"http://127.0.0.1:11434","model":"qwen2:0.5b","weight":80},
  {"name":"云端 API","kind":"openai","baseUrl":"https://api.openai.com/v1","model":"gpt-4o-mini","token":"sk-...","weight":20}
]
```

- **平滑加权轮询**（nginx 算法）：80/20 会变成 `A A A A B` 的均匀分布，不是前 8 次全是 A
- **熔断**：连续失败 3 次断开 30 秒，之后半开试探一次
- **故障转移**：选中的 provider 失败会自动换下一个，响应里带 `provider` 字段说明谁服务的
- **演练**：`/api/gateway/drill` 强制某个 provider 熔断，用来看流量是否真的切走

实测（80/20 + 一个故意不可达的 provider）：5 次请求中 1 次命中坏 provider，
失败 1 次后自动回退，5 次全部成功，状态里能看到 `请求 1 · 失败 1` 和具体错误。

### 评测：真实版本回归与 CI 闸门

Prompt 版本存在 `prompt_versions` 表里，每个版本有自己的**模板**和**检索配置**：

| 版本 | 检索配置 | Recall@5 | 闸门 |
| --- | --- | --- | --- |
| prompt_v1 基线 CoT | vector, top3 | 0.857 | 通过 |
| prompt_v2 先结论后依据 | hybrid, top3 | 1.000 | 通过 |
| prompt_v3 强制引用（生效中） | hybrid, top3 | 1.000 | 通过 |
| prompt_v4 实验版 | bm25, top1 | 0.714 | **阻断** |

```bash
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/api/eval/versions
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:8080/api/eval/run      # 跑全部版本
curl -s -X POST -H "Authorization: Bearer $TOKEN" localhost:8080/api/eval/activate   -d '{"version":"prompt_v2"}'                                                    # 切换生效版本
```

- 回归跑的是**真实检索指标**（7 条 Golden Set × 该版本的检索配置），一次约 20 秒
- `Recall@5 < 0.80` 直接标 `blocked`，这就是 CI 闸门
- **切换版本会真的改变对话行为**：模板和 topK/检索模式都从数据库读
- 版本表、用例表、KPI 全部来自接口，不是样例

### Trace：OpenTelemetry 接进 PostgreSQL

```bash
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/api/observability/traces
curl -s -H "Authorization: Bearer $TOKEN" localhost:8080/api/observability/traces/<traceId>
```

用官方 OTel SDK 埋点，**自建 exporter 把 span 写进 Postgres**——没有引入 Jaeger/Tempo 容器，
换 OTLP 只改 `internal/tracing.Init` 里的一行。

实测一次问答的瀑布图：

```
/api/chat        18574 ms   ← server span
  rag.retrieve    1959 ms   ← 检索（含 embedding 冷启动）
  llm.generate   16572 ms   ← 模型生成
```

span 属性也落库了（`http.method`、`llm.provider`、`llm.prompt_tokens`、`cites` 等）。
静态资源和探针不产生 trace，否则 trace 列表里全是 favicon。

### Agent：真实的 ReAct 循环

```bash
curl -s -X POST localhost:8080/api/agent/run \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"task":"一线城市住宿上限是多少？","maxSteps":2}'
```

五个真实工具：`kb_search`（pgvector 检索）、`usage_query`（用量统计）、
`documents_list`、`calculator`（自写安全求值器，不执行任意代码）、`time_now`。

轨迹是真的，实测一次（33 秒）：

```
Action       kb_search {"query":"一线城市住宿上限是多少？","top_k":3}  168ms
Observation  [1] 《差旅与费用报销管理办法_v3.2.pdf》相关度 0.77 一线城市…600 元
Thought      规则兜底选择 → kb_search（模型原始输出：usage_query）
Thought      kb_search 已经调用过，不再重复
Final        一线城市指北京、上海、广州、深圳，住宿费上限为每人每晚600元。
```

三个设计决定，都是为了「小模型也能跑」：

1. **先检索再规划**：不等模型决定要不要查，先把知识库结果拿到手。
2. **规则兜底**：qwen2:0.5b 实测几乎永远回 `usage_query`，
   所以对模型的选择做合理性校验，明显错配就走关键词规则，并在轨迹里注明「规则兜底」。
3. **幻觉检测**：终答里的数字必须在观察结果里出现过，否则标记「无出处」。
   实测它把 600 元说成 200 元时，这个检查会亮红。

### 可观测（真实时间序列）

```bash
curl -s 'localhost:8080/api/observability/series?minutes=180'
```

按分钟分桶返回 `requests / avgMs / p95Ms / tokens`，外加最近 10 次调用。
可观测页的四张图（请求数、平均延迟、P95、Token）都由它渲染。

> Trace 回放页仍是示例数据——真实 span 需要接 OpenTelemetry，那是另一件事。

### 文档 ETL

上传 → 存原件 → 入队 → 五阶段处理，全程可查询进度：

```bash
# 上传（multipart，也支持 JSON {name, content}）
curl -s -X POST localhost:8080/api/documents -F "file=@食堂管理规定.md"
curl -s localhost:8080/api/documents          # 列表 + queueDepth
curl -s localhost:8080/api/documents/<id>     # 单条，前端轮询用
curl -s -X POST localhost:8080/api/documents/<id>/retry
```

| 阶段 | 名称 | 做什么 |
| --- | --- | --- |
| 1 | 解析 | PDF 抽文本（`ledongthuc/pdf`）、docx 解 zip 取 `word/document.xml`、md/txt 直读 |
| 2 | 切分 | 按段落，超过 300 字切开，过短合并 |
| 3 | 向量化 | `nomic-embed-text` 768 维 |
| 4 | 入库 | 写 `chunks`，重建该文档的分块 |
| 5 | 就绪 | 可被检索与引用 |

- 原件存 MinIO：`<tenant>/<时间戳>-<文件名>`
- 任务走 Redis list `etl:jobs`（RPUSH + BLPOP），worker 在进程内跑
- 失败会写 `documents.error`，前端显示「解析失败」并可重试
- 扫描件 PDF（无文本层）会明确报错，不会静默入库空文档

### 检索的三种模式

```bash
curl -s -X POST localhost:8080/api/retrieval/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"一线城市住宿上限","mode":"hybrid","topK":3}'

# 评测：7 条 Golden Set × 3 模式，返回 Recall@5 / MRR / NDCG@5
curl -s -X POST localhost:8080/api/retrieval/evaluate
```

| 模式 | 实现 |
| --- | --- |
| `bm25` | 内存倒排索引，中文按字符 bigram 切词（`一线城市` → `一线/线城/城市`） |
| `vector` | pgvector 余弦相似度 |
| `hybrid` | 两路各取 Top-20，**加权分数融合**（min-max 归一化后 `0.6·词法 + 0.4·向量`） |

实测（7 条 Golden Set，语料 6 篇 13 块）：

| 模式 | Recall@5 | MRR | NDCG@5 |
| --- | --- | --- | --- |
| BM25 | 0.86 | **0.79** | **0.80** |
| 向量 | 0.86 | 0.69 | 0.73 |
| 混合（加权融合） | 0.86 | **0.79** | **0.80** |

演进过程值得记一笔：

- 最早用 **RRF**（只看名次），MRR 只有 **0.65**——比纯 BM25 还低。
  原因是这批查询几乎都是原文词，BM25 接近满分，无差别 RRF 把较弱的向量结果按名次混进来，
  反而把正确结果从第 1 位挤下去。
- 改成**加权分数融合**后回到 **0.79**，与 BM25 持平。
  它对「某一路明显更可靠」的场景更稳，因为它保留了分数的相对差距，而不只看名次。

**已知短板**：`怎么申请 VPN 权限` 这条 BM25 完全没进 Top5（知识库只有 7 条样本，
「申请」「权限」把入职指引的片段顶了上来）。这类语义鸿沟正是混合检索该解决、
但这批小语料解决不了的问题——需要更大的 Golden Set 才能调好。

### 向量检索

文档 → 分块 → embedding（`nomic-embed-text`，768 维）→ pgvector，检索走余弦距离：

```bash
curl -s -X POST localhost:8080/api/retrieval/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"一线城市住宿上限","topK":3}'
```

首次启动时若 `documents` 表为空，会自动灌入 3 份示例文档（7 个分块，含向量）。
想重新灌数据：`docker compose exec postgres psql -U llm -d llm -c "TRUNCATE documents CASCADE;"`，重启服务即可。

问答默认走 RAG：先检索 top-3 片段拼进提示词，回答里带 `cites`（来源、片段、相关度），
前端渲染成可点的引用卡。

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

- [docs/demo.md](docs/demo.md) — 3 分钟演示脚本（点什么、说什么、看什么）
- [docs/architecture.md](docs/architecture.md) — 分层、数据流、会话持久化、前端接入、已知取舍
- [docs/api-contract.md](docs/api-contract.md) — 12 个页面的接口契约与实现状态
- [docs/ai-code-metrics.md](docs/ai-code-metrics.md) — AI 代码率统计口径、提交规范与复现命令

## AI 代码率统计

Git 无法识别代码由谁编写，因此本项目采用**声明式区间口径**：提交时在 commit message 里用 trailer 声明 AI 参与程度，脚本聚合成可审计的上下界，并同时输出标注覆盖率。

```bash
make ai-ratio            # 终端表格（含分层）
make ai-ratio-md         # 生成 docs/ai-code-report.md
make ai-ratio-ci         # CI 闸门：标注覆盖率 < 80% 则失败
make ai-ratio-baseline   # 从当前 HEAD 开始统计，不改写历史

git commit -m "feat(rag): 混合检索" \
           -m "AI-Generated-By: claude-code" \
           -m "AI-Scope: full"
git commit -m "fix: 熔断半开判定" -m "Human-Only: yes"
```

报告给出 `[下界, 上界]` 而不是单一百分比：**下界**只算 `AI-Scope: full` 提交的新增行，**上界**把 `partial` 也算进去。**覆盖率不足时所有数字都是下限**，务必一起阅读。完整口径、排除规则、双仓库配置与面试讲解要点见 [docs/ai-code-metrics.md](docs/ai-code-metrics.md)。
# llm-gateway-go
