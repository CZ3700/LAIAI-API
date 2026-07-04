package relay

import "sort"

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
	Adapter        string // chat 家族：openai|anthropic|gemini|azure|volcengine|openai-responses；或 task 家族名（TaskOnly=true）
	Notes          string
	// TaskOnly：纯异步任务厂商（kling/vidu 等，只服务 /v1/video/generations，无 chat 端点）——
	// Adapter 是 task.Adaptor 家族名而非 chat adapter；不参与 chat 选路（picker 只按渠道 models 命中）。
	TaskOnly bool
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
	"doubao": {DisplayName: "字节豆包（国内方舟）", DefaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3", Adapter: "volcengine",
		Notes: "火山方舟国内端点（Seedance 2.0 视频任务 + chat）；真机验证 2026-07-05"},
	// —— 视频任务厂商（§3.8 批次 2，知识移植自 New API；仅 task 路由，非 chat）——
	"kling": {DisplayName: "快手可灵 Kling（视频任务）", DefaultBaseURL: "https://api-beijing.klingai.com", Adapter: "kling", TaskOnly: true,
		Notes: "视频生成任务；凭证格式 access_key|secret_key（JWT-HS256 现签）。真机验证待补"},
	"vidu": {DisplayName: "生数 Vidu（视频任务）", DefaultBaseURL: "https://api.vidu.cn", Adapter: "vidu", TaskOnly: true,
		Notes: "视频生成任务；鉴权 Authorization: Token <key>（非 Bearer）。真机验证待补"},
	// —— 视频/音乐任务厂商（§3.8 批次 3，知识移植自 New API）——
	"hailuo": {DisplayName: "MiniMax 海螺（视频任务）", DefaultBaseURL: "https://api.minimax.chat", Adapter: "hailuo", TaskOnly: true,
		Notes: "MiniMax 视频；bearer；产物 file_id 二跳取 download_url。真机验证待补"},
	"wan": {DisplayName: "阿里通义万相（视频任务）", DefaultBaseURL: "https://dashscope.aliyuncs.com", Adapter: "wan", TaskOnly: true,
		Notes: "DashScope wan/wanx 视频；bearer + X-DashScope-Async；video_url 直链。真机验证待补"},
	"suno": {DisplayName: "Suno（音乐任务）", DefaultBaseURL: "", Adapter: "suno", TaskOnly: true,
		Notes: "音乐/歌词；bearer；网关方言 /suno/submit/{MUSIC|LYRICS}；base_url 必填。真机验证待补"},
	"openai-responses": {DisplayName: "OpenAI Responses 上游", DefaultBaseURL: "", Adapter: "openai-responses",
		Notes: "上游只支持 /v1/responses（Codex 系/只暴露 Responses 的网关）；对内 chat 契约,adapter 双向转换。base_url 必填"},
	"advanced_custom": {DisplayName: "Advanced Custom（自定义路由）", DefaultBaseURL: "", Adapter: "openai",
		Notes: "不写代码接自定义上游：converter 选格式转换器 + upstream_path 自定义端点 + header_override 自定义认证；SSRF 拦内网/metadata。base_url 必填"},

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
	"qwen":        {DisplayName: "阿里通义千问", DefaultBaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Adapter: "openai", Notes: "国际版新加坡端点；国内换 dashscope.aliyuncs.com"},
	"dashscope":   {DisplayName: "阿里 DashScope", DefaultBaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Adapter: "openai"},
	"zhipu":       {DisplayName: "智谱 GLM", DefaultBaseURL: "https://open.bigmodel.cn/api/paas/v4", Adapter: "openai", Notes: "v4 起 OpenAI 兼容 + Bearer（旧版 JWT 协议不支持）"},
	"moonshot":    {DisplayName: "月之暗面 Kimi", DefaultBaseURL: "https://api.moonshot.cn/v1", Adapter: "openai"},
	"baidu":       {DisplayName: "百度千帆 ERNIE", DefaultBaseURL: "https://qianfan.baidubce.com/v2", Adapter: "openai", Notes: "千帆 v2 OpenAI 兼容（旧版 OAuth 协议不支持）"},
	"hunyuan":     {DisplayName: "腾讯混元", DefaultBaseURL: "https://api.hunyuan.cloud.tencent.com/v1", Adapter: "openai", Notes: "OpenAI 兼容端点（旧版 HMAC 签名协议不支持）"},
	"minimax":     {DisplayName: "MiniMax", DefaultBaseURL: "https://api.minimaxi.com/v1", Adapter: "openai"},
	"siliconflow": {DisplayName: "硅基流动", DefaultBaseURL: "https://api.siliconflow.cn/v1", Adapter: "openai"},
	"stepfun":     {DisplayName: "阶跃星辰", DefaultBaseURL: "https://api.stepfun.com/v1", Adapter: "openai"},
	"spark":       {DisplayName: "讯飞星火", DefaultBaseURL: "https://spark-api-open.xf-yun.com/v1", Adapter: "openai", Notes: "HTTP 兼容端点（旧版 WebSocket 协议不支持）"},
	"lingyiwanwu": {DisplayName: "零一万物 Yi", DefaultBaseURL: "https://api.lingyiwanwu.com/v1", Adapter: "openai"},
	"kimi":        {DisplayName: "月之暗面 Kimi（别名）", DefaultBaseURL: "https://api.moonshot.cn/v1", Adapter: "openai"},
	"ai360":       {DisplayName: "360 智脑", DefaultBaseURL: "https://api.360.cn/v1", Adapter: "openai"},
	"jina":        {DisplayName: "Jina AI", DefaultBaseURL: "https://api.jina.ai/v1", Adapter: "openai", Notes: "embeddings + rerank（/v1/rerank）"},
	"submodel":    {DisplayName: "SubModel", DefaultBaseURL: "https://llm.submodel.ai/v1", Adapter: "openai"},
	"xinference":  {DisplayName: "Xinference (self-host)", DefaultBaseURL: "", Adapter: "openai", Notes: "自建，base_url 必填（http://host:9997/v1）；含 rerank"},

	// —— OpenAI 兼容自建/级联（base_url 必填，不预置可能臆造的 URL）——
	"vllm":   {DisplayName: "vLLM (self-host)", DefaultBaseURL: "", Adapter: "openai", Notes: "自建，base_url 必填（http://host:8000/v1）"},
	"oneapi": {DisplayName: "One/New API 上游", DefaultBaseURL: "", Adapter: "openai", Notes: "级联另一套 OpenAI 兼容网关，base_url 必填"},
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

// ProviderInfo：注册表对外形态（admin API / Console 厂商下拉的数据源——UI 不重复维护清单）。
type ProviderInfo struct {
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	DefaultBaseURL string `json:"default_base_url"`
	Adapter        string `json:"adapter"`
	Notes          string `json:"notes,omitempty"`
}

// ListProviders 按名称排序返回全部档案。
func ListProviders() []ProviderInfo {
	out := make([]ProviderInfo, 0, len(providerProfiles))
	for name, p := range providerProfiles {
		out = append(out, ProviderInfo{
			Name: name, DisplayName: p.DisplayName,
			DefaultBaseURL: p.DefaultBaseURL, Adapter: p.Adapter, Notes: p.Notes,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
