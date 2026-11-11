# 接口契约 v0.1

前端 12 个页面 ↔ 后端接口的对应关系。**先按这个实现，改接口先改这份文档。**

## 约定

- 全部 `Content-Type: application/json; charset=utf-8`
- 字段一律 camelCase（与前端 TS 类型一致）
- 时间统一 RFC3339 字符串
- 错误体：`{"error":{"code":"...","message":"..."}}`
- 未实现的接口返回 `501 {"error":{"code":"not_implemented",...}}`

## 一、启动聚合

### GET /api/bootstrap

一次返回 12 个页面的初始数据，避免首屏发 12 个请求。

```json
{
  "overview":  { "kpis": [...], "services": [...], "alerts": [...], "roadmap": [...] },
  "docs":      { "items": [...], "stages": [...] },
  "retrieval": { "modes": {...}, "metrics": {...} },
  "agent":     { "steps": [...], "tools": [...], "mcp": [...] },
  "gateway":   { "providers": [...], "cache": {...} },
  "finops":    { "summary": {...}, "trend": [...], "top": [...] },
  "observability": { "metrics": [...], "traces": [...], "audit": [...] },
  "eval":      { "kpis": {...}, "versions": [...], "cases": [...] },
  "security":  { "injections": [...], "members": [...], "isolation": [...] },
  "ops":       { "services": [...], "runbooks": [...], "checks": [...] },
  "design":    { "colors": [...], "matrix": [...] }
}
```

## 二、动作类

| 方法 | 路径 | 请求 | 说明 |
|---|---|---|---|
| POST | `/api/chat` | `{conversationId?, prompt}` | 调 Ollama + 落库，返回 `{conversationId, answer, usage, model, latencyMs}` |
| GET | `/api/conversations` | — | 会话列表（不含消息，带 count） |
| POST | `/api/conversations` | `{title?}` | 新建会话 |
| GET | `/api/conversations/{id}` | — | 会话 + 全部消息 |
| PATCH | `/api/conversations/{id}` | `{title}` | 改名 |
| DELETE | `/api/conversations/{id}` | — | 删除（消息级联） |
| GET | `/api/documents` | — | 文档列表 + `queueDepth` |
| POST | `/api/documents` | multipart `file` 或 `{name, content}` | ✅ 存 MinIO + 入队，返回文档 |
| GET | `/api/documents/{id}` | — | 单条（轮询 ETL 进度） |
| POST | `/api/documents/{id}/retry` | — | ✅ 重置阶段并重新入队 |
| POST | `/api/retrieval/search` | `{query, topK}` | ✅ pgvector 余弦检索，返回 `{hits, tookMs, embedModel}` |
| POST | `/api/agent/run` | `{task}` | 返回 ReAct 步骤数组 |
| POST | `/api/gateway/drill` | `{enable}` | 故障演练开关 |
| POST | `/api/eval/run` | `{version}` | 触发回归，返回结果 |
| POST | `/api/ops/backup` | — | 触发备份 |
| POST | `/api/ops/restore` | `{backupId}` | 恢复演练 |
| PATCH | `/api/ops/checklist/{id}` | `{done}` | 勾选检查项 |

## 三、探针与元信息

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 进程存活 |
| GET | `/readyz` | 真探 Ollama |
| GET | `/api/version` | 版本 + 模型 |
| GET | `/api/meta/pages` | 12 页面的标题/FR/周次（供 design 页矩阵用） |

## 四、前端如何消费

```
src/api/client.ts     fetch 封装 + 错误处理
src/stores/data.ts    应用启动时 GET /api/bootstrap 一次，写入 Pinia
src/views/*.vue       从 store 读，不再 import mock 常量
```

**降级**：bootstrap 失败时自动回落到 `src/data/mock.ts`，页面仍可演示（左下角标注「离线样例数据」）。

## 五、实现状态

| 接口 | 状态 |
|---|---|
| `/healthz` `/readyz` `/api/version` `/api/chat` | ✅ 已实现（真实 Ollama） |
| `/api/bootstrap` | ✅ 已实现（内存种子，结构已定型） |
| 其它动作接口 | 🚧 内存种子 + 状态变更 |
| PostgreSQL 落库（会话/消息） | ✅ 已实现（迁移内嵌，启动自动执行） |
