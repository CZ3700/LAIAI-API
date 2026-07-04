package relay

// Adapter contract v2 第一期（商用迭代指令 §3.1；设计 §4.4）：RelayFormat × RelayMode 双轴的
// "上游端点 + 鉴权"统一矩阵。此前 azure deployments 方言在 5 个 builder 各抄一份、
// provider→URL switch 共 8+ 份——新增 provider/方言要改 N 处。现在只改这一张表。
//
// 轴定义：
//   - RelayFormat = adapter 家族（providers.go profile.Adapter：openai|anthropic|gemini|azure|
//     volcengine|openai-responses）。anthropic/gemini/openai-responses 的 chat 是真协议转换，
//     URL/头由各自 adapter 原生构建，不经本表；它们的非 chat 模态无 OpenAI 兼容端点 → 哨兵错误。
//   - RelayMode = 模态（本文件枚举）。task/realtime 后续并入。
//
// 行为保真不变量（重构红线，设计 §4.4）：
//   ① UpstreamPath（0036）仍只对 openai 家族 chat 生效；
//   ② azure+rerank 无 deployments 端点 → 沿现状落 openai 默认形态；
//   ③ cohere rerank 按 Provider 特判剥 /compatibility/v1；
//   ④ volcengine chat 的 bot- 前缀路由进表；
//   ⑤ 哨兵错误逐字保留（handler 等值判断做 400 终止不换渠道）。

import (
	"fmt"
	"net/http"
	"strings"
)

// RelayMode：模态轴。
type RelayMode string

const (
	ModeChat                RelayMode = "chat"
	ModeEmbeddings          RelayMode = "embeddings"
	ModeRerank              RelayMode = "rerank"
	ModeImagesGenerations   RelayMode = "images_generations"
	ModeImagesEdits         RelayMode = "images_edits"
	ModeImagesVariations    RelayMode = "images_variations"
	ModeAudioSpeech         RelayMode = "audio_speech"
	ModeAudioTranscriptions RelayMode = "audio_transcriptions"
	ModeAudioTranslations   RelayMode = "audio_translations"
)

// modePaths：openai 兼容默认端点路径（base_url 已含版本段，如 /v1）。
var modePaths = map[RelayMode]string{
	ModeChat:                "/chat/completions",
	ModeEmbeddings:          "/embeddings",
	ModeRerank:              "/rerank",
	ModeImagesGenerations:   "/images/generations",
	ModeImagesEdits:         "/images/edits",
	ModeImagesVariations:    "/images/variations",
	ModeAudioSpeech:         "/audio/speech",
	ModeAudioTranscriptions: "/audio/transcriptions",
	ModeAudioTranslations:   "/audio/translations",
}

// azureModePaths：azure deployments 方言的端点段（/openai/deployments/{model}/<段>?api-version=…）。
// rerank 缺席是有意的：azure 无 rerank 端点，沿现状落 openai 默认形态（不变量②）。
var azureModePaths = map[RelayMode]string{
	ModeChat:                "chat/completions",
	ModeEmbeddings:          "embeddings",
	ModeImagesGenerations:   "images/generations",
	ModeImagesEdits:         "images/edits",
	ModeImagesVariations:    "images/variations",
	ModeAudioSpeech:         "audio/speech",
	ModeAudioTranscriptions: "audio/transcriptions",
	ModeAudioTranslations:   "audio/translations",
}

// multipartMode：请求体为 multipart 的模态（header_override 的 Content-Type 防御性跳过面）。
func (m RelayMode) multipartMode() bool {
	switch m {
	case ModeImagesEdits, ModeImagesVariations, ModeAudioTranscriptions, ModeAudioTranslations:
		return true
	}
	return false
}

// upstreamSpec：矩阵输出——端点 + 鉴权方式。
type upstreamSpec struct {
	URL        string
	AuthHeader string // "Authorization" | "api-key"
	AuthPrefix string // "Bearer " | ""
}

// applyAuth：按 spec 设置鉴权头。
func (s upstreamSpec) applyAuth(req *http.Request, credential string) {
	req.Header.Set(s.AuthHeader, s.AuthPrefix+credential)
}

// modeUnsupportedErr：家族不支持该模态时的哨兵错误（不变量⑤：handler 等值判断，逐字保留）。
func modeUnsupportedErr(mode RelayMode) error {
	switch mode {
	case ModeEmbeddings:
		return errEmbeddingsUnsupported
	case ModeRerank:
		return errRerankUnsupported
	case ModeImagesGenerations, ModeImagesEdits, ModeImagesVariations:
		return errImagesUnsupported
	case ModeAudioSpeech, ModeAudioTranscriptions, ModeAudioTranslations:
		return errAudioUnsupported
	}
	return fmt.Errorf("relay: mode %s not supported by this provider", mode)
}

// channelAdapterFamily：渠道 provider → adapter 家族（收编 5 份 ProfileFor 解析样板）。
// 未登记 provider 返回原名——在矩阵里落 openai 兼容默认分支（与旧各 builder 的 default 一致）。
func channelAdapterFamily(ch *Channel) string {
	if p, ok := ProfileFor(ch.Provider); ok {
		return p.Adapter
	}
	return ch.Provider
}

// modeUpstreamSpec：非 chat handler 入口——家族从渠道 provider 档案解析后查矩阵。
func modeUpstreamSpec(ch *Channel, mode RelayMode, upModel string) (upstreamSpec, error) {
	return formatModeSpec(channelAdapterFamily(ch), ch, mode, upModel)
}

// formatModeSpec：(RelayFormat × RelayMode) → 上游端点 + 鉴权。
// chat adapter 直接以自身家族调用（openaiAdapter 恒传 "openai"——它也是未知 provider 与
// ConverterNone 的兜底 adapter，URL 形态由 adapter 身份决定，不回查渠道 provider）。
func formatModeSpec(format string, ch *Channel, mode RelayMode, upModel string) (upstreamSpec, error) {
	base := strings.TrimRight(ch.BaseURL, "/")
	switch format {
	case "anthropic", "gemini":
		// chat 由各自 adapter 原生转换（不经本表）；非 chat 无 OpenAI 兼容端点。
		return upstreamSpec{}, modeUnsupportedErr(mode)
	case "azure":
		if seg, ok := azureModePaths[mode]; ok {
			return upstreamSpec{
				URL:        base + "/openai/deployments/" + upModel + "/" + seg + "?api-version=" + AzureAPIVersion,
				AuthHeader: "api-key",
			}, nil
		}
		// rerank 等无 azure 方言端点：落 openai 兼容默认（不变量②）。
	case "volcengine":
		if mode == ModeChat && strings.HasPrefix(upModel, "bot-") {
			return upstreamSpec{URL: base + "/bots/chat/completions", AuthHeader: "Authorization", AuthPrefix: "Bearer "}, nil
		}
	}
	// cohere rerank 原生端点（按 Provider 特判，不变量③）：默认 base 是 …/compatibility/v1
	// （chat 兼容用），rerank 必须归一回主机根再拼 /v1/rerank，否则 404（对标 NewAPI cohere adaptor）。
	if mode == ModeRerank && ch.Provider == "cohere" {
		return upstreamSpec{
			URL:        strings.TrimSuffix(base, "/compatibility/v1") + "/v1/rerank",
			AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		}, nil
	}
	path, ok := modePaths[mode]
	if !ok {
		return upstreamSpec{}, modeUnsupportedErr(mode)
	}
	// UpstreamPath（0036）只对 openai 家族 chat 生效（不变量①）：其余 adapter/模态照旧忽略——
	// 统一生效会让存量渠道请求 URL 突变，单独立项（设计 §4.4 不做清单）。
	if mode == ModeChat && format == "openai" {
		return upstreamSpec{URL: ch.upstreamURL(path), AuthHeader: "Authorization", AuthPrefix: "Bearer "}, nil
	}
	return upstreamSpec{URL: base + path, AuthHeader: "Authorization", AuthPrefix: "Bearer "}, nil
}
