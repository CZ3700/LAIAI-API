package relay

// 厂商档案注册表（设计 §4.4 适配器池）。
//
// 工程判断（知识库 §5/§6 + 2026 现状）：绝大多数厂商已提供 OpenAI 兼容端点，
// 它们共用经过验证的 openai 适配器，差异只剩"默认端点 + 认证方式"——用档案显式声明，
// 而不是复制 N 份适配器（NewAPI 一半"适配器"本质就是 openai 的薄包装）。
// 真异协议（anthropic/gemini/azure/volcengine bot 路由）才写专用适配器。
//
// 渠道创建时 provider 必须命中本表（或专用适配器名），base_url 缺省取档案默认端点。
// ⚠️ 默认端点以各厂商官方文档为准登记；新厂商上线前用真实 API 抽样验证（fixture 纪律）。
type providerProfile struct {
	DisplayName    string
	DefaultBaseURL string // OpenAI 兼容 base（含版本路径，适配器再拼 /chat/completions）
	Adapter        string // openai | anthropic | gemini | azure | volcengine
	Notes          string
}

var providerProfiles = map[string]providerProfile{
	// —— 专用协议 ——
	"openai":    {DisplayName: "OpenAI", DefaultBaseURL: "https://api.openai.com/v1", Adapter: "openai"},
	"anthropic": {DisplayName: "Anthropic Claude", DefaultBaseURL: "https://api.anthropic.com/v1", Adapter: "anthropic"},
	"gemini":    {DisplayName: "Google Gemini", DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta", Adapter: "gemini"},
	"azure":     {DisplayName: "Azure OpenAI", DefaultBaseURL: "", Adapter: "azure", Notes: "base_url 必填（https://<res>.openai.azure.com），model = deployment 名"},
	"volcengine": {DisplayName: "字节豆包/火山方舟", DefaultBaseURL: "https://ark.ap-southeast.bytepluses.com/api/v3", Adapter: "volcengine",
		Notes: "默认 BytePlus ModelArk 国际端点；bot- 前缀模型走 /bots/ 路由"},
	"modelark": {DisplayName: "BytePlus ModelArk", DefaultBaseURL: "https://ark.ap-southeast.bytepluses.com/api/v3", Adapter: "volcengine"},

	// —— OpenAI 兼容（西方系）——
	"deepseek":   {DisplayName: "DeepSeek", DefaultBaseURL: "https://api.deepseek.com/v1", Adapter: "openai"},
	"xai":        {DisplayName: "xAI Grok", DefaultBaseURL: "https://api.x.ai/v1", Adapter: "openai"},
	"mistral":    {DisplayName: "Mistral", DefaultBaseURL: "https://api.mistral.ai/v1", Adapter: "openai"},
	"groq":       {DisplayName: "Groq", DefaultBaseURL: "https://api.groq.com/openai/v1", Adapter: "openai"},
	"openrouter": {DisplayName: "OpenRouter", DefaultBaseURL: "https://openrouter.ai/api/v1", Adapter: "openai"},
	"together":   {DisplayName: "Together AI", DefaultBaseURL: "https://api.together.xyz/v1", Adapter: "openai"},
	"fireworks":  {DisplayName: "Fireworks AI", DefaultBaseURL: "https://api.fireworks.ai/inference/v1", Adapter: "openai"},
	"perplexity": {DisplayName: "Perplexity", DefaultBaseURL: "https://api.perplexity.ai", Adapter: "openai"},
	"cohere":     {DisplayName: "Cohere", DefaultBaseURL: "https://api.cohere.ai/compatibility/v1", Adapter: "openai", Notes: "compatibility 端点"},
	"ollama":     {DisplayName: "Ollama (self-host)", DefaultBaseURL: "http://localhost:11434/v1", Adapter: "openai"},

	// —— OpenAI 兼容（中国系，A1 核心供给）——
	"qwen":      {DisplayName: "阿里通义千问", DefaultBaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Adapter: "openai", Notes: "国际版新加坡端点；国内换 dashscope.aliyuncs.com"},
	"dashscope": {DisplayName: "阿里 DashScope", DefaultBaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Adapter: "openai"},
	"zhipu":     {DisplayName: "智谱 GLM", DefaultBaseURL: "https://open.bigmodel.cn/api/paas/v4", Adapter: "openai", Notes: "v4 起 OpenAI 兼容 + Bearer（旧版 JWT 协议不支持）"},
	"moonshot":  {DisplayName: "月之暗面 Kimi", DefaultBaseURL: "https://api.moonshot.cn/v1", Adapter: "openai"},
	"baidu":     {DisplayName: "百度千帆 ERNIE", DefaultBaseURL: "https://qianfan.baidubce.com/v2", Adapter: "openai", Notes: "千帆 v2 OpenAI 兼容（旧版 OAuth 协议不支持）"},
	"hunyuan":   {DisplayName: "腾讯混元", DefaultBaseURL: "https://api.hunyuan.cloud.tencent.com/v1", Adapter: "openai", Notes: "OpenAI 兼容端点（旧版 HMAC 签名协议不支持）"},
	"minimax":   {DisplayName: "MiniMax", DefaultBaseURL: "https://api.minimaxi.com/v1", Adapter: "openai"},
	"siliconflow": {DisplayName: "硅基流动", DefaultBaseURL: "https://api.siliconflow.cn/v1", Adapter: "openai"},
	"stepfun":   {DisplayName: "阶跃星辰", DefaultBaseURL: "https://api.stepfun.com/v1", Adapter: "openai"},
	"spark":     {DisplayName: "讯飞星火", DefaultBaseURL: "https://spark-api-open.xf-yun.com/v1", Adapter: "openai", Notes: "HTTP 兼容端点（旧版 WebSocket 协议不支持）"},
	"lingyiwanwu": {DisplayName: "零一万物 Yi", DefaultBaseURL: "https://api.lingyiwanwu.com/v1", Adapter: "openai"},
	"kimi":      {DisplayName: "月之暗面 Kimi（别名）", DefaultBaseURL: "https://api.moonshot.cn/v1", Adapter: "openai"},
}

// ProfileFor 返回厂商档案；未登记的 provider 按 OpenAI 兼容裸档案处理
// （自建/聚合上游大多说 OpenAI 方言，base_url 必填即可工作）。
func ProfileFor(provider string) (providerProfile, bool) {
	p, ok := providerProfiles[provider]
	return p, ok
}

// DefaultBaseURLFor：渠道创建时 base_url 缺省的解析。
func DefaultBaseURLFor(provider string) string {
	if p, ok := providerProfiles[provider]; ok {
		return p.DefaultBaseURL
	}
	return ""
}

// SupportedProviders 返回已登记厂商名（admin 展示/文档用）。
func SupportedProviders() []string {
	out := make([]string, 0, len(providerProfiles))
	for name := range providerProfiles {
		out = append(out, name)
	}
	return out
}
