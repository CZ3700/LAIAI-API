package forward

import (
	"cmp"
	"encoding/json"
	"maps"
	"strconv"
	"strings"
)

var volcengineContentPassthroughKeys = []string{
	"ratio",
	"resolution",
	"duration",
	"camera_fixed",
	"seed",
	"generate_audio",
	"return_last_frame",
	"watermark",
	"stream",
	"callback_url",
	"service_tier",
	"execution_expires_after",
	"draft",
	"tools",
	"safety_identifier",
	"priority",
	"frames",
	"omni_reference_task_type",
	"output_format",
	"bitrate_mode",
}

// BuildVolcengineContentArray 按 3 级兼容构建火山方舟 /api/v3/contents/generations/tasks 的 content 数组。
func BuildVolcengineContentArray(body map[string]any) []any {
	if arr, ok := body["content"].([]any); ok && len(arr) > 0 {
		return EnsureImageRolesForMultimodal(slicesCloneAny(arr))
	}
	if draftID, ok := body["draft_task_id"].(string); ok && strings.TrimSpace(draftID) != "" {
		return []any{
			map[string]any{
				"type":       "draft_task",
				"draft_task": map[string]any{"id": strings.TrimSpace(draftID)},
			},
		}
	}

	parts := []any{}
	if prompt := firstNonEmptyString(body["prompt"]); prompt != "" {
		parts = append(parts, map[string]any{"type": "text", "text": prompt})
	} else if msgs, ok := body["messages"].([]any); ok && len(msgs) > 0 {
		if msgPrompt := promptFromMessages(msgs); msgPrompt != "" {
			parts = append(parts, map[string]any{"type": "text", "text": msgPrompt})
		}
	}

	imgItems := CollectMediaValues(body, []string{"images", "image_urls", "image_url", "image"})
	if len(imgItems) > 0 {
		defaults := InferImageDefaultRoles(len(imgItems))
		for i, item := range imgItems {
			defRole := "reference_image"
			if i < len(defaults) {
				defRole = defaults[i]
			}
			u, role := ParseMediaItem(item, defRole)
			if u != "" {
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": u},
					"role":      role,
				})
			}
		}
	}

	// 支持显式首尾帧字段 first_frame_image / last_frame_image
	if u := AsMediaURL(body["first_frame_image"]); u != "" && len(imgItems) == 0 {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": u},
			"role":      "first_frame",
		})
	}
	if u := AsMediaURL(body["last_frame_image"]); u != "" && len(imgItems) < 2 {
		parts = append(parts, map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": u},
			"role":      "last_frame",
		})
	}

	for _, item := range CollectMediaValues(body, []string{"videos", "video_urls", "video_url"}) {
		if u, role := ParseMediaItem(item, "reference_video"); u != "" {
			parts = append(parts, map[string]any{
				"type":      "video_url",
				"video_url": map[string]any{"url": u},
				"role":      role,
			})
		}
	}

	for _, item := range CollectMediaValues(body, []string{"audios", "audio_urls", "audio_url"}) {
		if u, role := ParseMediaItem(item, "reference_audio"); u != "" {
			parts = append(parts, map[string]any{
				"type":      "audio_url",
				"audio_url": map[string]any{"url": u},
				"role":      role,
			})
		}
	}

	return EnsureImageRolesForMultimodal(parts)
}

// BuildVolcengineVideoBody 构造火山方舟 / Seedance 请求体（支持 draft_task_id、3 级多模态、web_search 与参数白名单）。
func BuildVolcengineVideoBody(upstreamModel string, body map[string]any) map[string]any {
	content := BuildVolcengineContentArray(body)
	out := map[string]any{
		"model":   upstreamModel,
		"content": content,
	}

	hasDraftTask := firstNonEmptyString(body["draft_task_id"]) != ""
	if !hasDraftTask {
		for _, item := range content {
			if m, ok := item.(map[string]any); ok && m["type"] == "draft_task" {
				hasDraftTask = true
				break
			}
		}
	}

	for _, key := range volcengineContentPassthroughKeys {
		if hasDraftTask {
			switch key {
			case "duration", "ratio", "seed", "camera_fixed", "frames", "draft", "tools", "omni_reference_task_type":
				continue
			}
		}
		if val, exists := body[key]; exists && val != nil {
			out[key] = val
		}
	}

	// OpenAI web_search: true -> tools: [{"type":"web_search"}]
	if !hasDraftTask && out["tools"] == nil && JSONTruthy(body["web_search"]) {
		out["tools"] = []any{map[string]any{"type": "web_search"}}
	}

	// OpenAI size 兼容映射（ratio/resolution 缺失时按语义提取）
	if sizeStr := firstNonEmptyString(body["size"]); sizeStr != "" && !hasDraftTask {
		lower := strings.ToLower(sizeStr)
		if (lower == "480p" || lower == "720p" || lower == "1080p" || lower == "2k" || lower == "4k") && out["resolution"] == nil {
			out["resolution"] = lower
		} else if out["ratio"] == nil {
			if r := SizeToRatio(sizeStr); r != "" {
				out["ratio"] = r
			}
		}
	}

	if _, exists := out["execution_expires_after"]; !exists {
		out["execution_expires_after"] = 3600
	}
	if firstNonEmptyString(out["resolution"]) == "" {
		if JSONTruthy(out["draft"]) {
			out["resolution"] = "480p"
		} else if r := extractResolutionFromBody(body); r != "" {
			out["resolution"] = r
		} else {
			out["resolution"] = "720p"
		}
	}
	return out
}

// BuildKlingV3VideoBody 构造可灵 3.0 / Omni 视频请求体（{prompt/contents, settings, options}）。
func BuildKlingV3VideoBody(body map[string]any, upstreamPath string) map[string]any {
	isOmni := strings.Contains(upstreamPath, "omni-video")

	// 原生透传路径（已含 contents 或文生 prompt+settings）
	_, hasContents := body["contents"]
	_, hasPrompt := body["prompt"]
	_, hasSettings := body["settings"]
	if hasContents || (!isOmni && hasPrompt && hasSettings) {
		out := maps.Clone(body)
		if out == nil {
			out = map[string]any{}
		}
		delete(out, "model")
		delete(out, "model_name")
		if isOmni {
			if rawPrompt, ok := out["prompt"]; ok {
				delete(out, "prompt")
				if text := firstNonEmptyString(rawPrompt); text != "" {
					contents := slicesCloneAny(toAnySliceGeneric(out["contents"]))
					hasPromptEntry := false
					for _, item := range contents {
						if m, ok := item.(map[string]any); ok && m["type"] == "prompt" {
							hasPromptEntry = true
							break
						}
					}
					if !hasPromptEntry {
						contents = append([]any{map[string]any{"type": "prompt", "text": text}}, contents...)
					}
					out["contents"] = contents
				}
			}
		}
		settings := mapCloneAny(out["settings"])
		for k, v := range buildKlingV3Settings(body) {
			if _, exists := settings[k]; !exists {
				settings[k] = v
			}
		}
		out["settings"] = settings
		if opts := buildKlingV3Options(body); len(opts) > 0 {
			out["options"] = opts
		}
		return out
	}

	mediaItems := []any{}
	imgItems := CollectMediaValues(body, []string{"images", "image_urls", "image_url", "image"})
	parsedImgs := make([]struct {
		url  string
		role string
	}, 0, len(imgItems))
	allEmptyRole := true
	for _, item := range imgItems {
		u, role := ParseMediaItem(item, "")
		if u == "" {
			continue
		}
		if role != "" {
			allEmptyRole = false
		}
		parsedImgs = append(parsedImgs, struct {
			url  string
			role string
		}{url: u, role: role})
	}
	pairPlain := len(parsedImgs) == 2 && allEmptyRole
	for idx, img := range parsedImgs {
		mediaItems = append(mediaItems, map[string]any{
			"type": klingV3ImageType(img.role, idx, pairPlain),
			"url":  img.url,
		})
	}
	for _, item := range CollectMediaValues(body, []string{"videos", "video_urls", "video_url"}) {
		u, role := ParseMediaItem(item, "")
		if u == "" {
			continue
		}
		ty := "base_video"
		switch strings.ToLower(role) {
		case "feature", "feature_video":
			ty = "feature_video"
		}
		mediaItems = append(mediaItems, map[string]any{"type": ty, "url": u})
	}

	out := map[string]any{
		"settings": buildKlingV3Settings(body),
	}
	prompt := firstNonEmptyString(body["prompt"])
	if !isOmni && len(mediaItems) == 0 {
		if prompt != "" {
			out["prompt"] = prompt
		}
	} else {
		contents := make([]any, 0, len(mediaItems)+1)
		if prompt != "" {
			contents = append(contents, map[string]any{"type": "prompt", "text": prompt})
		}
		contents = append(contents, mediaItems...)
		out["contents"] = contents
	}
	if opts := buildKlingV3Options(body); len(opts) > 0 {
		out["options"] = opts
	}
	return out
}

// ResolveKlingDynamicPath 根据请求中的媒体/模型自适应选择 Kling 3.0 端点（text-to-video / image-to-video / omni-video）。
func ResolveKlingDynamicPath(upstreamPath, upstreamModel string, builtBody map[string]any) string {
	trimmed := strings.TrimRight(strings.TrimSpace(upstreamPath), "/")
	if !strings.Contains(trimmed, "text-to-video") {
		return upstreamPath
	}
	model := strings.TrimSpace(upstreamModel)
	if model == "" {
		return upstreamPath
	}
	contents := toAnySliceGeneric(builtBody["contents"])
	if len(contents) == 0 {
		if strings.Contains(strings.ToLower(model), "omni") {
			return "/omni-video/" + model
		}
		return "/text-to-video/" + model
	}
	hasVideo := false
	hasRefer := false
	imageCount := 0
	for _, item := range contents {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ty := strings.ToLower(firstNonEmptyString(m["type"]))
		switch ty {
		case "base_video", "feature_video", "video_url":
			hasVideo = true
		case "refer_image", "reference_image":
			hasRefer = true
			imageCount++
		case "first_frame", "last_frame", "middle_frame", "image_url":
			imageCount++
		}
	}
	if hasVideo || hasRefer || imageCount > 2 || strings.Contains(strings.ToLower(model), "omni") {
		return "/omni-video/" + model
	}
	return "/image-to-video/" + model
}

func klingV3ImageType(role string, idx int, pairPlain bool) string {
	if pairPlain {
		if idx == 0 {
			return "first_frame"
		}
		return "last_frame"
	}
	switch strings.ToLower(role) {
	case "first_frame":
		return "first_frame"
	case "last_frame":
		return "last_frame"
	case "middle_frame":
		return "middle_frame"
	case "refer_image", "reference_image", "reference":
		return "refer_image"
	default:
		if idx == 0 {
			return "first_frame"
		}
		return "refer_image"
	}
}

func buildKlingV3Settings(body map[string]any) map[string]any {
	rawSettings := mapCloneAny(body["settings"])
	settings := map[string]any{}
	res := cmp.Or(firstNonEmptyString(rawSettings["resolution"]), firstNonEmptyString(body["resolution"]), "720p")
	settings["resolution"] = res
	if d := firstNonNil(rawSettings["duration"], body["duration"]); d != nil {
		settings["duration"] = d
	}
	audio := "off"
	if a := firstNonEmptyString(rawSettings["audio"], body["audio"]); a != "" {
		switch strings.ToLower(a) {
		case "off", "false":
			audio = "off"
		default:
			audio = "native"
		}
	} else if ga, exists := body["generate_audio"]; exists && JSONTruthy(ga) {
		audio = "native"
	}
	settings["audio"] = audio
	if ar := firstNonNil(rawSettings["aspect_ratio"], body["aspect_ratio"], body["ratio"]); ar != nil {
		settings["aspect_ratio"] = ar
	}
	if ms := firstNonNil(rawSettings["multi_shot"], body["multi_shot"]); ms != nil {
		settings["multi_shot"] = ms
	}
	if cc := firstNonNil(rawSettings["camera_control"], body["camera_control"]); cc != nil {
		settings["camera_control"] = cc
	}
	return settings
}

func buildKlingV3Options(body map[string]any) map[string]any {
	opts := mapCloneAny(body["options"])
	for _, key := range []string{"callback_url", "external_task_id", "watermark_info"} {
		if _, exists := opts[key]; !exists {
			if val, ok := body[key]; ok && val != nil {
				opts[key] = val
			}
		}
	}
	if _, exists := opts["watermark_info"]; !exists {
		if wm, ok := body["watermark"]; ok && wm != nil {
			opts["watermark_info"] = map[string]any{"enabled": JSONTruthy(wm)}
		}
	}
	return opts
}

var viduVideoPassthroughKeys = []string{
	"prompt",
	"duration",
	"resolution",
	"aspect_ratio",
	"audio",
	"is_rec",
	"seed",
	"off_peak",
	"watermark",
	"wm_position",
	"wm_url",
	"payload",
	"meta_data",
	"callback_url",
	"movement_amplitude",
	"style",
}

// BuildViduVideoBody 构造生数 Vidu 官方视频请求体（保留白名单参数、sounds 与角色排序的 images）。
func BuildViduVideoBody(model string, body map[string]any) map[string]any {
	modelName := cmp.Or(firstNonEmptyString(body["model"]), model)
	out := map[string]any{
		"model": modelName,
	}
	for _, key := range viduVideoPassthroughKeys {
		if val, exists := body[key]; exists && val != nil {
			out[key] = val
		}
	}
	if sounds := CollectURLs(body, []string{"sounds", "audios", "audio_urls", "audio_url"}); len(sounds) > 0 {
		out["sounds"] = sounds
	}
	mediaItems := CollectMediaValues(body, []string{"images", "image_urls", "image_url", "image"})
	if len(mediaItems) > 0 {
		var firstFrame, endFrame string
		others := make([]string, 0, len(mediaItems))
		for _, item := range mediaItems {
			u, role := ParseMediaItem(item, "")
			if u == "" {
				continue
			}
			switch strings.ToLower(role) {
			case "first_frame":
				if firstFrame == "" {
					firstFrame = u
					continue
				}
			case "last_frame":
				if endFrame == "" {
					endFrame = u
					continue
				}
			}
			others = append(others, u)
		}
		finalImages := make([]string, 0, len(mediaItems))
		if firstFrame != "" {
			finalImages = append(finalImages, firstFrame)
		} else if endFrame != "" && len(others) > 0 {
			finalImages = append(finalImages, others[0])
			others = others[1:]
		}
		if endFrame != "" {
			finalImages = append(finalImages, endFrame)
		}
		finalImages = append(finalImages, others...)
		if len(finalImages) > 0 {
			out["images"] = finalImages
		}
	}
	if _, exists := out["aspect_ratio"]; !exists {
		if r := firstNonNil(body["ratio"], body["size"]); r != nil {
			if s, ok := r.(string); ok {
				if ar := SizeToRatio(s); ar != "" {
					out["aspect_ratio"] = ar
				}
			} else {
				out["aspect_ratio"] = r
			}
		}
	}
	if _, exists := out["audio"]; !exists {
		if ga, ok := body["generate_audio"]; ok && ga != nil {
			out["audio"] = JSONTruthy(ga)
		}
	}
	if _, exists := out["resolution"]; !exists {
		out["resolution"] = "540p"
	}
	if _, exists := out["off_peak"]; !exists && firstNonEmptyString(body["service_tier"]) == "flex" {
		out["off_peak"] = true
	}
	return out
}

// ResolveViduDynamicPath 根据 Vidu 请求体媒体与角色自适应路由到 /ent/v2/{text2video,img2video,start-end2video,reference2video}。
func ResolveViduDynamicPath(upstreamPath, requestPath string, reqBody map[string]any) string {
	for _, seg := range []string{"text2video", "img2video", "start-end2video", "reference2video"} {
		if strings.Contains(requestPath, seg) {
			return "/ent/v2/" + seg
		}
	}
	_ = upstreamPath
	rawImgs := CollectMediaValues(reqBody, []string{"images", "image_urls", "image_url", "image"})
	hasSounds := len(CollectURLs(reqBody, []string{"sounds", "audios", "audio_urls", "audio_url"})) > 0
	hasEndRole, hasRefRole := false, false
	for _, item := range rawImgs {
		_, role := ParseMediaItem(item, "")
		switch strings.ToLower(role) {
		case "last_frame":
			hasEndRole = true
		case "reference_image", "reference":
			hasRefRole = true
		}
	}
	imgCount := len(rawImgs)
	switch {
	case (imgCount == 2 && !hasRefRole && !hasSounds) || hasEndRole:
		return "/ent/v2/start-end2video"
	case hasSounds || hasRefRole || imgCount > 2:
		return "/ent/v2/reference2video"
	case imgCount == 1:
		return "/ent/v2/img2video"
	default:
		return "/ent/v2/text2video"
	}
}

var dashscopeVideoParamKeys = []string{
	"resolution",
	"ratio",
	"duration",
	"prompt_extend",
	"watermark",
	"seed",
	"audio",
	"shot_type",
}

// BuildDashScopeVideoBody 构造阿里百炼 DashScope 通义万相视频请求体。
func BuildDashScopeVideoBody(upstreamModel string, body map[string]any) map[string]any {
	if _, hasInput := body["input"]; hasInput {
		out := maps.Clone(body)
		if out == nil {
			out = map[string]any{}
		}
		out["model"] = upstreamModel
		return out
	}

	prompt := firstNonEmptyString(body["prompt"])
	input := map[string]any{
		"prompt": prompt,
	}
	for _, field := range []string{"negative_prompt", "audio_url", "template", "img_url", "first_frame_url", "last_frame_url"} {
		if val := firstNonEmptyString(body[field]); val != "" {
			input[field] = val
		}
	}

	if existingMedia, ok := body["media"].([]any); ok && len(existingMedia) > 0 {
		input["media"] = slicesCloneAny(existingMedia)
	} else {
		videoType := "reference_video"
		if strings.Contains(strings.ToLower(upstreamModel), "happyhorse") {
			videoType = "video"
		}
		mediaList := []any{}
		imgItems := CollectMediaValues(body, []string{"images", "image_urls", "image_url", "image"})
		if len(imgItems) > 0 {
			defaults := InferImageDefaultRoles(len(imgItems))
			for i, item := range imgItems {
				defRole := "reference_image"
				if i < len(defaults) {
					defRole = defaults[i]
				}
				u, role := ParseMediaItem(item, defRole)
				if u != "" {
					mediaList = append(mediaList, map[string]any{"type": role, "url": u})
				}
			}
		}
		for _, item := range CollectMediaValues(body, []string{"videos", "video_urls", "video_url"}) {
			if u, role := ParseMediaItem(item, videoType); u != "" {
				mediaList = append(mediaList, map[string]any{"type": role, "url": u})
			}
		}
		for _, item := range CollectMediaValues(body, []string{"audios", "audio_urls", "audio_url"}) {
			if u, role := ParseMediaItem(item, "reference_audio"); u != "" {
				mediaList = append(mediaList, map[string]any{"type": role, "url": u})
			}
		}
		for _, item := range CollectMediaValues(body, []string{"files"}) {
			if u, role := ParseMediaItem(item, "file"); u != "" {
				mediaList = append(mediaList, map[string]any{"type": role, "url": u})
			}
		}
		for _, item := range CollectMediaValues(body, []string{"links"}) {
			if u, role := ParseMediaItem(item, "link"); u != "" {
				mediaList = append(mediaList, map[string]any{"type": role, "url": u})
			}
		}
		if len(mediaList) > 0 {
			input["media"] = mediaList
		}
	}

	params := map[string]any{}
	for _, key := range dashscopeVideoParamKeys {
		if val, exists := body[key]; exists && val != nil {
			params[key] = val
		}
	}
	if _, exists := params["resolution"]; !exists {
		if size := firstNonEmptyString(body["size"]); size != "" {
			params["resolution"] = strings.ToUpper(size)
		}
	} else if resStr, ok := params["resolution"].(string); ok {
		params["resolution"] = strings.ToUpper(strings.TrimSpace(resStr))
	}
	if _, exists := params["resolution"]; !exists {
		params["resolution"] = "720P"
	}
	if _, exists := params["duration"]; !exists {
		params["duration"] = 5
	}
	if _, exists := params["audio"]; !exists {
		if ga, exists := body["generate_audio"]; exists && ga != nil {
			params["audio"] = JSONTruthy(ga)
		}
	}

	return map[string]any{
		"model":      upstreamModel,
		"input":      input,
		"parameters": params,
	}
}

// BuildMiniMaxVideoBody 构造 MiniMax V2 (/v2/video_generation) 请求体。
func BuildMiniMaxVideoBody(upstreamModel string, body map[string]any) map[string]any {
	content := BuildVolcengineContentArray(body)
	out := map[string]any{
		"model":   upstreamModel,
		"content": content,
	}
	for _, key := range []string{"resolution", "ratio", "duration", "callback_url"} {
		if val, exists := body[key]; exists && val != nil {
			out[key] = val
		}
	}
	if wm := firstNonNil(body["aigc_watermark"], body["watermark"]); wm != nil {
		out["aigc_watermark"] = JSONTruthy(wm)
	}
	return out
}

// BuildJimengVideoBody 构造即梦 AI（火山 CV 视觉服务）视频提交请求体（req_key / frames / image_urls | binary_data_base64）。
func BuildJimengVideoBody(upstreamModel string, body map[string]any) map[string]any {
	out := map[string]any{
		"req_key": upstreamModel,
	}
	for _, key := range []string{"prompt", "seed", "frames", "aspect_ratio", "binary_data_base64", "image_urls", "req_json"} {
		if val, exists := body[key]; exists && val != nil {
			out[key] = val
		}
	}
	if _, exists := out["prompt"]; !exists {
		if p := extractPromptFromContent(body); p != "" {
			out["prompt"] = p
		}
	}
	if _, exists := out["frames"]; !exists {
		if dur, ok := anyToFloat64(body["duration"]); ok && dur > 0 {
			if dur <= 5 {
				out["frames"] = 121
			} else {
				out["frames"] = 241
			}
		} else {
			out["frames"] = 121
		}
	}
	if _, exists := out["aspect_ratio"]; !exists {
		if ratio := firstNonEmptyString(body["ratio"], body["size"]); ratio != "" {
			out["aspect_ratio"] = cmp.Or(SizeToRatio(ratio), ratio)
		}
	}
	if _, hasB64 := out["binary_data_base64"]; !hasB64 {
		if _, hasURLs := out["image_urls"]; !hasURLs {
			imgs := CollectURLs(body, []string{"images", "image_urls", "image_url", "image"})
			if len(imgs) > 0 {
				if IsBase64Media(imgs[0], 1) {
					cleaned := make([]string, 0, len(imgs))
					for _, img := range imgs {
						cleaned = append(cleaned, StripDataURIPrefix(img))
					}
					out["binary_data_base64"] = cleaned
				} else {
					out["image_urls"] = imgs
				}
			}
		}
	}
	return out
}

// BuildTencentVODVideoBody 构造腾讯云点播 AIGC CreateAigcVideoTask 请求体。
func BuildTencentVODVideoBody(upstreamModel string, subAppID uint64, body map[string]any) map[string]any {
	modelName, modelVersion, hasVer := strings.Cut(strings.TrimSpace(upstreamModel), "@")
	out := map[string]any{
		"ModelName": modelName,
	}
	if hasVer && strings.TrimSpace(modelVersion) != "" {
		out["ModelVersion"] = strings.TrimSpace(modelVersion)
	}
	if subAppID > 0 {
		out["SubAppId"] = subAppID
	} else if sid, ok := anyToFloat64(body["SubAppId"]); ok && sid > 0 {
		out["SubAppId"] = uint64(sid)
	}

	if p := firstNonEmptyString(body["prompt"], body["Prompt"]); p != "" {
		out["Prompt"] = p
	} else if p := extractPromptFromContent(body); p != "" {
		out["Prompt"] = p
	}
	if np := firstNonEmptyString(body["negative_prompt"], body["NegativePrompt"]); np != "" {
		out["NegativePrompt"] = np
	}
	if fi, exists := body["FileInfos"]; exists && fi != nil {
		out["FileInfos"] = fi
	} else if fileInfos := buildTencentVODFileInfos(body); len(fileInfos) > 0 {
		out["FileInfos"] = fileInfos
	}

	if oc, exists := body["OutputConfig"]; exists && oc != nil {
		out["OutputConfig"] = oc
	} else {
		out["OutputConfig"] = buildTencentVODOutputConfig(body)
	}

	if seed := firstNonNil(body["seed"], body["Seed"]); seed != nil {
		out["Seed"] = seed
	}
	if si := firstNonNil(body["SubjectInfos"], body["subject_infos"]); si != nil {
		out["SubjectInfos"] = si
	}
	if ep := firstNonEmptyString(body["EnhancePrompt"]); ep != "" {
		out["EnhancePrompt"] = ep
	} else if pe, ok := body["prompt_extend"]; ok {
		if JSONTruthy(pe) {
			out["EnhancePrompt"] = "Enabled"
		} else {
			out["EnhancePrompt"] = "Disabled"
		}
	}
	if ext := buildTencentExtInfo(body); ext != "" {
		out["ExtInfo"] = ext
	}
	return out
}

func buildTencentVODFileInfos(body map[string]any) []map[string]any {
	type mediaRef struct {
		url      string
		category string
		role     string
	}
	var refs []mediaRef
	videos := CollectMediaValues(body, []string{"videos", "video_urls", "video_url"})
	hasVideo := len(videos) > 0

	imgItems := CollectMediaValues(body, []string{"images", "image_urls", "image_url", "image"})
	defaults := InferImageDefaultRoles(len(imgItems))
	for i, item := range imgItems {
		defRole := "reference_image"
		if i < len(defaults) {
			defRole = defaults[i]
		}
		u, role := ParseMediaItem(item, defRole)
		if u != "" {
			refs = append(refs, mediaRef{url: u, category: "Image", role: role})
		}
	}
	for _, item := range videos {
		u, role := ParseMediaItem(item, "")
		if u != "" {
			refs = append(refs, mediaRef{url: u, category: "Video", role: role})
		}
	}
	for _, u := range CollectURLs(body, []string{"audios", "audio_urls", "audio_url", "sounds"}) {
		if u != "" {
			refs = append(refs, mediaRef{url: u, category: "Audio", role: "reference"})
		}
	}

	// 若尚未提取出媒体，则尝试从 content/contents 多模态数组提取
	if len(refs) == 0 {
		for _, arrKey := range []string{"contents", "content"} {
			arr, _ := body[arrKey].([]any)
			for _, raw := range arr {
				m, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				ty := strings.ToLower(firstNonEmptyString(m["type"]))
				u, role := ParseMediaItem(m, ty)
				if u == "" {
					continue
				}
				switch ty {
				case "video_url", "base_video", "feature_video":
					hasVideo = true
					refs = append(refs, mediaRef{url: u, category: "Video", role: role})
				case "audio_url":
					refs = append(refs, mediaRef{url: u, category: "Audio", role: "reference"})
				case "image_url", "first_frame", "last_frame", "middle_frame", "refer_image":
					refs = append(refs, mediaRef{url: u, category: "Image", role: role})
				}
			}
		}
	}

	if len(refs) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		item := map[string]any{"Category": ref.category}
		if IsBase64Media(ref.url, 1) {
			item["Type"] = "Base64"
			item["Base64"] = StripDataURIPrefix(ref.url)
		} else {
			item["Type"] = "Url"
			item["Url"] = ref.url
		}
		switch ref.category {
		case "Image":
			usage := "FirstFrame"
			switch strings.ToLower(ref.role) {
			case "last_frame":
				usage = "LastFrame"
			case "middle_frame", "middle_image", "refer_image", "reference_image", "reference":
				usage = "Reference"
			default:
				if hasVideo {
					usage = "Reference"
				}
			}
			item["Usage"] = usage
			if strings.EqualFold(ref.role, "middle_image") || strings.EqualFold(ref.role, "refer_image") {
				item["ReferenceType"] = strings.ToLower(ref.role)
			}
		case "Video":
			item["Usage"] = "Reference"
			if ref.role != "" && !strings.EqualFold(ref.role, "reference_video") && !strings.EqualFold(ref.role, "video_url") {
				item["ReferenceType"] = strings.ToLower(ref.role)
			}
		case "Audio":
			item["Usage"] = "Reference"
		}
		out = append(out, item)
	}
	return out
}

func buildTencentVODOutputConfig(body map[string]any) map[string]any {
	settings := mapCloneAny(body["settings"])
	oc := map[string]any{
		"StorageMode":           "Temporary",
		"InputComplianceCheck":  "Disabled",
		"OutputComplianceCheck": "Disabled",
	}
	if res := firstNonEmptyString(settings["resolution"], body["resolution"]); res != "" {
		oc["Resolution"] = strings.ToUpper(res)
	} else {
		oc["Resolution"] = "720P"
	}
	if ar := firstNonEmptyString(settings["aspect_ratio"], body["aspect_ratio"], body["ratio"]); ar != "" {
		oc["AspectRatio"] = ar
	}
	if d, ok := anyToFloat64(firstNonNil(settings["duration"], body["duration"])); ok && d > 0 {
		oc["Duration"] = d
	}
	if op := firstNonEmptyString(body["OffPeak"]); op != "" {
		oc["OffPeak"] = op
	} else if firstNonEmptyString(body["service_tier"]) == "flex" || JSONTruthy(body["off_peak"]) {
		oc["OffPeak"] = "Enabled"
	}
	if wm := firstNonNil(body["watermark"]); wm != nil {
		if JSONTruthy(wm) {
			oc["LogoAdd"] = "Enabled"
		} else {
			oc["LogoAdd"] = "Disabled"
		}
	}
	if ga, ok := body["generate_audio"]; ok && ga != nil {
		if JSONTruthy(ga) {
			oc["AudioGeneration"] = "Enabled"
		} else {
			oc["AudioGeneration"] = "Disabled"
		}
	} else if a := firstNonEmptyString(settings["audio"], body["audio"]); a != "" {
		switch strings.ToLower(a) {
		case "off", "false", "0", "disabled":
			oc["AudioGeneration"] = "Disabled"
		default:
			oc["AudioGeneration"] = "Enabled"
		}
	}
	return oc
}

func buildTencentExtInfo(body map[string]any) string {
	extMap := map[string]any{}
	if s := firstNonEmptyString(body["ExtInfo"]); s != "" {
		_ = json.Unmarshal([]byte(s), &extMap)
	} else if m, ok := body["ExtInfo"].(map[string]any); ok {
		extMap = maps.Clone(m)
	}
	addParams := map[string]any{}
	if s := firstNonEmptyString(extMap["AdditionalParameters"], body["AdditionalParameters"]); s != "" {
		_ = json.Unmarshal([]byte(s), &addParams)
	} else if m, ok := body["AdditionalParameters"].(map[string]any); ok {
		addParams = maps.Clone(m)
	}
	for _, key := range []string{"return_last_frame", "omni_reference_task_type"} {
		if val, exists := body[key]; exists && val != nil {
			addParams[key] = val
		}
	}
	if len(addParams) > 0 {
		if b, err := json.Marshal(addParams); err == nil {
			extMap["AdditionalParameters"] = string(b)
		}
	}
	if len(extMap) == 0 {
		return ""
	}
	b, err := json.Marshal(extMap)
	if err != nil {
		return ""
	}
	return string(b)
}

// BuildVolcMediaEnhanceBody 构造火山 MediaKit 视频画质增强请求体（/api/v1/tools/enhance-video*）。
func BuildVolcMediaEnhanceBody(upstreamModel string, body map[string]any) map[string]any {
	videoURL := firstNonEmptyString(body["video_url"])
	if videoURL == "" {
		if urls := CollectURLs(body, []string{"videos", "video_urls", "video_url"}); len(urls) > 0 {
			videoURL = urls[0]
		}
	}
	if videoURL == "" {
		if content, ok := body["content"].([]any); ok {
			for _, raw := range content {
				if m, ok := raw.(map[string]any); ok {
					if u := AsMediaURL(m["video_url"]); u != "" {
						videoURL = u
						break
					}
				}
			}
		}
	}
	out := map[string]any{
		"model":      cmp.Or(firstNonEmptyString(body["model"]), upstreamModel, "vve-sd"),
		"video_url":  videoURL,
		"mode":       cmp.Or(firstNonEmptyString(body["mode"]), "standard"),
		"scene":      cmp.Or(firstNonEmptyString(body["scene"]), "aigc"),
		"resolution": cmp.Or(firstNonEmptyString(body["resolution"]), "1080p"),
	}
	if fps, ok := anyToFloat64(firstNonNil(body["fps"], body["framespersecond"])); ok && fps > 0 {
		out["fps"] = int64(fps)
	}
	return out
}

func firstNonEmptyString(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok {
			if t := strings.TrimSpace(s); t != "" {
				return t
			}
		}
	}
	return ""
}

func firstNonNil(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func mapCloneAny(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return map[string]any{}
	}
	return maps.Clone(m)
}

func toAnySliceGeneric(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = item
		}
		return out
	default:
		return nil
	}
}

func slicesCloneAny(in []any) []any {
	if len(in) == 0 {
		return nil
	}
	out := make([]any, len(in))
	copy(out, in)
	return out
}

func promptFromMessages(msgs []any) string {
	var parts []string
	for _, raw := range msgs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch c := m["content"].(type) {
		case string:
			if strings.TrimSpace(c) != "" {
				parts = append(parts, strings.TrimSpace(c))
			}
		case []any:
			for _, item := range c {
				if im, ok := item.(map[string]any); ok {
					if t := firstNonEmptyString(im["text"]); t != "" {
						parts = append(parts, t)
					}
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

func extractPromptFromContent(body map[string]any) string {
	for _, arrKey := range []string{"content", "contents"} {
		arr, _ := body[arrKey].([]any)
		for _, raw := range arr {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if t := firstNonEmptyString(m["text"]); t != "" {
				return t
			}
		}
	}
	if msgs, ok := body["messages"].([]any); ok {
		return promptFromMessages(msgs)
	}
	return ""
}

func extractResolutionFromBody(body map[string]any) string {
	if r := firstNonEmptyString(body["resolution"]); r != "" {
		return r
	}
	return ""
}

func anyToFloat64(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	default:
		return 0, false
	}
}
