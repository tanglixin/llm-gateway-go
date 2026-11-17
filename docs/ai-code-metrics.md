# AI 代码率统计：口径、规范与复现

> 版本 v1.0 · 2026-11-17 · 适用仓库：`llm-gateway` / `llm-gateway-ui`
> 工具：`cmd/airatio`（纯 Go 标准库，零新增依赖）

## 1 为什么不能「自动识别」

Git 的提交作者永远是提交人，代码本身也不带作者标记。任何声称能从代码风格、注释格式反推 AI  authorship 的方法都不可审计、会被重构抹掉。

本项目采用**声明式区间口径**：让每次提交在 commit message 里显式声明 AI 参与程度，脚本只做聚合与校验。好处是数字可复现、可审计、能进 CI；代价是它测的是**声明覆盖率下的下限**，不是客观真相。报告会同时输出标注覆盖率，覆盖率不足时所有百分比都必须打折阅读。

这与业界主流做法一致：2026 年公开数字从 20% 到 75% 的巨大差异，几乎全部来自「单位（行 / commit / PR）」「范围（新增 / 全量）」「阶段（建议 / 采纳 / 合入 / 留存）」「阈值（完全生成 / AI 辅助）」四个口径选择不同，而非测量精度不同。

## 2 口径定义（本工具固定为下列选择）

| 维度 | 本项目选择 | 理由 |
| --- | --- | --- |
| 单位 | **行**（主）+ commit（辅） | 提交数反映工作单元，行数反映产出量，两者都报 |
| 范围 | **新增有效代码行**，不含删除行 | 删除/重构不代表新产出；同时单独报删除行数体现 churn |
| 阶段 | **已合入当前分支的提交**（`git log HEAD`） | 不统计 IDE 里被采纳但没提交的建议，避免虚高 |
| 阈值 | `full` / `partial` / `human` 三态 | 不伪造单一比例，改报**区间** |

### 三个核心公式

```
标注覆盖率  = 已标注提交 / 提交总数                       ← 决定数据可信度
AI 参与提交占比 = (full + partial 提交) / 已标注提交
下界 lower  = Σ(full 提交新增有效行) / Σ(全部新增有效行)
上界 upper  = Σ(full + partial 提交新增有效行) / Σ(全部新增有效行)
点估计      = lower + (upper - lower) × partial-weight   ← 默认 0.5，仅报表便利
```

**真实 AI 代码率落在 `[lower, upper]` 之间**：`full` 把整个提交都算作 AI 会略微高估局部，`partial` 完全不计入下界会低估。覆盖率低于 100% 时，区间整体仍是下限。报告不会输出一个假装精确的百分数。

## 3 提交规范（trailer）

在 commit message 的 body 里加任意一行或多行，大小写不敏感：

| Trailer | 取值 | 含义 |
| --- | --- | --- |
| `AI-Generated-By` | `claude-code` / `cursor` / `copilot` / `codex` / 其它工具名 | 声明 AI 参与；**缺此行时 `AI-Scope` 不生效** |
| `AI-Scope` | `full` / `partial` | `full` = 本提交新增代码整体由 AI 生成并人工审定；`partial` = 部分生成或大幅改写（缺省值） |
| `Human-Only` | `yes` | 明确纯人工编写，用于计算覆盖率 |
| `AI-Reviewed` | `yes` | 可选，记录人工审查（当前仅记录，不影响计算） |

规则：

- 同时写 `AI-Generated-By` 与 `Human-Only` 视为**声明冲突**，按未标注处理并在报告里告警。
- 什么都不写 = 未标注，计入未标注数、拉低覆盖率，**不会**被猜测成 AI 或人工。
- 提交里只有文档/配置变更时，`Add` 为 0，仍计入提交级指标但不拉低行级比例。

示例：

```bash
git commit -m "feat(rag): 混合检索 RRF 融合" \
           -m "AI-Generated-By: claude-code" \
           -m "AI-Scope: full" \
           -m "AI-Reviewed: yes"

git commit -m "fix(gateway): 修正熔断半开状态判定" -m "Human-Only: yes"
```

可以一次性启用提交模板（`scripts/commit-template.txt` 会自动提示这些字段）：

```bash
git config commit.template scripts/commit-template.txt
```

## 4 计入范围

**计入**：`.go` `.ts` `.tsx` `.js` `.jsx` `.vue` `.sql` `.proto` `.sh` `.yaml` `.yml` `.html` `.css`

**排除**：Markdown / JSON / lock / go.mod / go.sum；`web/` `dist/` `build/` `bin/` `vendor/` `node_modules/` `coverage/` 等构建产物与依赖目录；二进制文件（git numstat 显示 `-`）；`*.min.js`。

分层按路径前两级聚合（如 `llm-gateway/internal/rag`、`llm-gateway-ui/src/api`），用于回答「哪一层适合交给 AI」——这比一个总数有用得多。可用 `-ext` 覆盖后缀白名单。

> 注意：后端仓库的 `web/` 是前端构建产物，被 `.gitignore` 排除且不计入统计；前端源码在独立仓库，需单独纳入 `-repos`。

## 5 怎么用

```bash
# 最常用：终端表格（Makefile 已配置 AI_REPOS=.,../llm-gateway-ui）
make ai-ratio

# 只统计当前仓库，或临时换仓库列表
make ai-ratio AI_REPOS=.

# 生成 Markdown 报告，可直接贴进 README / 简历证据包
make ai-ratio-md

# CI 闸门：标注覆盖率低于 80% 则以非零码退出
make ai-ratio-ci

# 直接调 CLI，参数更灵活
go run ./cmd/airatio -repos=.,../llm-gateway-ui -format=json
go run ./cmd/airatio -since=2026-11-01 -format=table
go run ./cmd/airatio -baseline=c3c2fa7 -format=markdown -out=docs/ai-code-report.md
go run ./cmd/airatio -partial-weight=0.3 -min-coverage=0.9 -strict
```

| 参数 | 默认 | 说明 |
| --- | --- | --- |
| `-repos` | `.` | 逗号分隔多个仓库；无 `.git` 的仓库会告警并跳过 |
| `-since` / `-until` | 空 | 传给 `git log --since` / `--until` |
| `-baseline` | 空 | 只统计该 commit/tag 之后的提交 |
| `-format` | `table` | `table` / `json` / `markdown` |
| `-out` | 空 | 写文件而非 stdout |
| `-partial-weight` | `0.5` | 点估计里 partial 的折算权重 |
| `-min-coverage` | `0.8` | 覆盖率告警阈值 |
| `-strict` | `false` | CI 模式，覆盖率不达标退出码 1 |
| `-ext` | 见第 4 节 | 覆盖计入的代码后缀 |

### 从今天开始，不改写历史

历史提交没有 trailer 是正常的，**不要**用 `rebase` / `filter-branch` 回填（改写已推送历史的代价远大于这点数据）：

```bash
make ai-ratio-baseline   # 把当前 HEAD 写进 .ai-ratio-baseline
```

之后 CLI 自动只统计 `baseline..HEAD`，新提交按规范声明即可。基线文件是普通文本，可提交进仓库，也能用 `-baseline` 临时覆盖。若某个仓库不包含该 commit（例如另一个独立仓库），CLI 会退回全量历史并在报告里跳过告警。

## 6 双仓库注意事项

`llm-gateway-ui` 目前**没有 `.git`**，因此无法统计。先纳入版本管理：

```bash
cd ../llm-gateway-ui
git init && git add . && git commit -m "chore: 初始导入" -m "Human-Only: yes"
```

之后 `make ai-ratio` 会同时输出两个仓库、按仓库名前缀分层。前端的 `dist/`、`node_modules/` 已被排除，不会把构建产物算成代码。

## 7 CI 片段（可选）

项目当前未启用 GitHub Actions，接入时可直接用：

```yaml
- name: AI code ratio
  run: |
    go run ./cmd/airatio \
      -repos=.,../llm-gateway-ui \
      -format=markdown -out=docs/ai-code-report.md
    go run ./cmd/airatio -strict -min-coverage=0.8 -format=json > ai-ratio.json
```

建议只把覆盖率当门禁（防止统计失去意义），**不要**把「AI 代码率」本身设为 KPI 或考核目标。

## 8 怎么读这个数字（以及不要怎么读）

- **先看覆盖率再看比例**：覆盖率 0% 时，报告里的 0% 只说明「没有声明」，不说明「没有用 AI」。
- **区间比单点诚实**：`[lower, upper]` 明确表达了 partial 提交的不确定性。
- **必须配反向指标**：AI 代码占比上升若伴随返工率、评审负载、变更失败率上升，不是提效。建议同时跟踪 FPY（首次正确率）、变更失败率、评审时长。
- **分层比总数有用**：CRUD / DTO 层由 AI 生成 90% 很正常，基础设施与领域规则层应显著更低；后者才是需要人工兜底的地方。
- **不要跨口径比较**：本工具的行级区间不能与某厂商「75% 新代码由 AI 生成」（自报、含人工批准）或「25% commit 含 AI 痕迹」（外部检测）直接对比。

## 9 面试里怎么讲（L3 / L4 素材）

一句话立意：**「AI 代码率无法从代码本身测出，所以我把它做成一套可审计的声明契约，而不是一个拍脑袋的百分比。」**

可展开的权衡：

1. 为什么不从代码风格识别 —— 不可审计、重构即失效、无法复现；
2. 为什么报区间不报单点 —— `full` 高估局部、`partial` 无法定量，单点数字会制造虚假精度；
3. 为什么把覆盖率设计成一等指标 —— 声明式方法的失效模式是「大家不声明」，覆盖率就是这套度量自身的健康度；
4. 为什么不回填历史 —— 改写历史破坏协作与审计链，用基线切分更划算；
5. 为什么排除构建产物与文档 —— 否则 `web/assets` 会让分母虚高，数字失去意义；
6. 与项目已有的评测闸门、OTel 审计、CI 是同一套思路：**把概率性/不可回放的过程变成有证据、可回归的工程对象**。

## 10 已知限制

- 依赖提交者自觉声明，无法防止漏标或误标；覆盖率只能发现漏标，不能校验真伪。
- `AI-Scope: full` 是「整个提交」粒度，若一次提交里混了人工重写，会偏高；建议按功能拆小提交。
- 不统计未提交的工作区改动，也不统计 squash 后丢失 trailer 的历史（故建议 CI 用 `-strict` 兜底）。
- 未做跨分支对比与趋势图；JSON 输出已结构化，可直接接 Grafana 或项目控制台 P07 / P11 页面。
