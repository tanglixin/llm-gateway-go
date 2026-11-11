// Package kb 负责知识库的初始化：示例文档 -> 分块 -> 向量入库。
package kb

import (
	"context"
	"log"
	"strings"

	"github.com/llm-gateway/llm-gateway/internal/llm"
	"github.com/llm-gateway/llm-gateway/internal/store"
	"github.com/llm-gateway/llm-gateway/pkg/token"
)

type doc struct {
	name string
	acl  string
	body string
}

var samples = []doc{
	{
		name: "差旅与费用报销管理办法_v3.2.pdf",
		acl:  "全员可见",
		body: `一线城市指北京、上海、广州、深圳，住宿费上限为每人每晚 600 元；其余城市 400 元。超出部分原则上不予报销。

跨部门出差须由发起部门主管及费用归属部门主管双签确认，单笔超过 5000 元的报销追加财务复核节点。

因展会、旺季等客观原因导致住宿价格显著上浮的，可提交特批单，经费用归属部门总监批准后上浮幅度不超过 30%。

差旅申请应提前 3 个工作日在系统内提交，紧急出差可事后补单，但需注明原因并附主管确认。

市内交通费按实际发生额报销，单日上限 100 元；机场/车站往返可选择网约车或出租车。`,
	},
	{
		name: "员工入职指引_v2026.pdf",
		acl:  "全员可见",
		body: `新员工入职首日需完成：签署劳动合同、提交身份证与学历证明、领取工牌与电脑、加入企业微信与邮箱组。

研发岗位需在入职 3 个工作日内申请代码仓库权限与 VPN 账号，权限由直属主管审批。

试用期为 3 个月，转正评估包括主管评价、协作方反馈与项目产出三部分。

年假按司龄计算：满 1 年 5 天，满 3 年 10 天，满 5 年 15 天；年假需提前 3 个工作日申请。`,
	},
	{
		name: "研发环境接入规范.md",
		acl:  "研发组",
		body: `所有研发环境统一通过 VPN 接入，禁止将内网服务直接暴露到公网。

本地开发使用 Docker Compose 启动依赖：PostgreSQL、Redis、MinIO、Ollama 四个服务。

提交代码前必须通过 go test ./... 与 golangci-lint；CI 会额外跑评测闸门。

数据库变更必须通过迁移文件，禁止手工改线上表结构；迁移随二进制内嵌发布。`,
	},
}

// Seed 在知识库为空时写入示例文档（含向量）。
func Seed(ctx context.Context, st *store.Store, emb *llm.Embedder, tenant string) error {
	n, err := st.CountDocuments(ctx, tenant)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	total := 0
	for _, d := range samples {
		docID, err := st.InsertDocument(ctx, tenant, d.name, d.acl)
		if err != nil {
			return err
		}

		items := make([]store.ChunkInput, 0, 8)
		for i, para := range chunks(d.body) {
			vec, err := emb.Embed(ctx, para)
			if err != nil {
				return err
			}
			items = append(items, store.ChunkInput{
				Idx:       i,
				Content:   para,
				Tokens:    token.Estimate(para),
				Embedding: vec,
			})
		}
		if err := st.InsertChunks(ctx, docID, tenant, items); err != nil {
			return err
		}
		total += len(items)
	}

	log.Printf("knowledge base seeded: %d documents, %d chunks", len(samples), total)
	return nil
}

// chunks 按段落切分，过短的段落合并到上一块。
func chunks(body string) []string {
	var out []string
	var buf strings.Builder

	flush := func() {
		if s := strings.TrimSpace(buf.String()); s != "" {
			out = append(out, s)
		}
		buf.Reset()
	}

	for _, p := range strings.Split(body, "\n\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if buf.Len() > 0 && buf.Len()+len(p) > 300 {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString(p)
	}
	flush()
	return out
}
