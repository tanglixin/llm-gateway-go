package search

// GoldenCase 是检索评测的一条样本：查询 + 期望命中的文档名。
type GoldenCase struct {
	Query string `json:"query"`
	Doc   string `json:"doc"`
}

// GoldenSet 是内置评测集，只覆盖种子文档（不依赖用户上传内容）。
var GoldenSet = []GoldenCase{
	{"一线城市住宿上限是多少", "差旅与费用报销管理办法_v3.2.pdf"},
	{"跨部门出差需要谁审批", "差旅与费用报销管理办法_v3.2.pdf"},
	{"旺季住宿价格可以上浮多少", "差旅与费用报销管理办法_v3.2.pdf"},
	{"试用期多长时间", "员工入职指引_v2026.pdf"},
	{"年假有几天", "员工入职指引_v2026.pdf"},
	{"怎么申请 VPN 权限", "研发环境接入规范.md"},
	{"数据库表结构怎么改", "研发环境接入规范.md"},
}
