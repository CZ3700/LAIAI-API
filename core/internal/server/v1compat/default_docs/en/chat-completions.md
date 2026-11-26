# Chat & Response Endpoints

OpenAI-compatible chat. Follow the examples below with your platform API Key.

### 1. Chat Completions
* **Path**: `/v1/chat/completions`
* **Method**: `POST`

#### Core Request Parameters
| Parameter | Type | Required | Description |
| :--- | :--- | :--- | :--- |
| `model` | `string` | Yes | Target model name, e.g., `gpt-4o`, `claude-3-5-sonnet-20241022`, `gemini-1.5-pro` |
| `messages` | `array` | Yes | Array of historical conversation messages, e.g., `[{"role": "user", "content": "Hello"}]` |
| `stream` | `boolean` | No | Whether to return the response as an SSE event stream (streaming character-by-character, default is `false`) |
| `temperature` | `number` | No | Sampling temperature (0~2). Higher values increase randomness. Recommended: `0.7` to `1.0` |
| `max_tokens` | `integer` | No | Maximum token limit for model generation |
| `tools` | `array` | No | List of tools (Function Calling) available for the model |

#### Command Line Example (Curl)
```bash
curl -X POST https://{{domain}}/v1/chat/completions \
  -H "Authorization: Bearer sk-your_token" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-4o",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "请解释什么是量子纠缠。"}
    ],
    "stream": false
  }'
```

### 2. Responses API
* **Path**: `/v1/responses`
* **Method**: `POST`

> [!NOTE]
> Use `/v1/responses` when your client already speaks the Responses payload format. Billing, quotas, and usage logs still apply.

#### Request Example
```json
{
  "model": "gpt-4o",
  "input": [
    {"role": "user", "content": "Hello"}
  ],
  "stream": false
}
```
