# Google Gemini Native API Guide

If you are using the official Google SDK, or wish to use Gemini Multi-modal, System Instructions, or JSON Mode, call the Google-compatible paths below. [Gemini API](https://ai.google.dev/gemini-api/docs/generate-content/image-generation?hl=zh-cn)

### 1. Text Generation (Non-stream)
* **Path**: `/v1beta/models/{model}:generateContent`
* **Request Method**: `POST`

### 2. Streaming Generation (Streaming)
* **Path**: `/v1beta/models/{model}:streamGenerateContent`
* **Request Method**: `POST`

#### Core Request Payload Example
```json
{
  "contents": [
    {
      "role": "user",
      "parts": [
        {
          "text": "请扮演我的私人旅行助手，规划一份 3 天的京都赏樱路线。"
        }
      ]
    }
  ],
  "systemInstruction": {
    "parts": [
      {
        "text": "你是一个专业的旅行规划师，语气亲切幽默。"
      }
    ]
  },
  "generationConfig": {
    "temperature": 0.4,
    "maxOutputTokens": 2000,
    "responseMimeType": "text/plain"
  }
}
```

### 3. Interactions API (v1 / v1beta)
* **Path**: `/v1/interactions` (Compatible with `/v1beta/interactions`)
* **Request Method**: `POST`

Google Gemini unified Interactions API, allowing direct configuration of `model` (or `agent`) and `input` (text, multimodal, or multiturn items), with support for `system_instruction`, `tools`, `response_format`, and more.

#### Core Request Payload Example
```json
{
  "model": "gemini-2.5-flash",
  "input": "Please help me plan a 3-day cherry blossom trip in Kyoto.",
  "system_instruction": "You are a professional travel planner with a friendly and humorous tone.",
  "stream": false
}
```

#### Authentication Methods (Choose One of Three)
* Standard Header: `Authorization: Bearer sk-your_token`
* Google Header: `X-Goog-Api-Key: sk-your_token`
* Query Parameter: `?key=sk-your_token`
