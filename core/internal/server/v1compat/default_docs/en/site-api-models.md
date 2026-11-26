# Available Models List API

Retrieve the list of active models currently available for your API key (automatically filtered by active channels, token model whitelist, and user level visibility). Both **OpenAI Protocol** and **Google Gemini Protocol** are supported.

### 1. OpenAI Protocol (`GET /v1/models`)
* **Method & URL**: `GET {baseUrl}/v1/models` (if `baseUrl` already ends with `/v1`, `/models` is appended directly, e.g. `https://{{domain}}/v1/models`; also compatible with Volcengine Ark `GET /api/v3/models`)
* **Request Headers**:
```http
Authorization: Bearer <API Key>
```
* **Curl Example**:
```bash
curl -X GET https://{{domain}}/v1/models \
  -H "Authorization: Bearer sk-your_token"
```
* **Response Format & Parsing**:
  Read the `data` array in the returned JSON and extract the `id` field of each item (standard OpenAI `/v1/models` format `{ "data": [{ "id": "model-name" }] }`):
```json
{
  "object": "list",
  "data": [
    {
      "id": "gpt-4o",
      "object": "model",
      "created": 1719441600,
      "owned_by": "OpenAI"
    },
    {
      "id": "gemini-2.5-flash",
      "object": "model",
      "created": 1719441600,
      "owned_by": "Google"
    }
  ]
}
```

### 2. Google Gemini Protocol (`GET /v1beta/models`)
* **Method & URL**: `GET {baseUrl}/v1beta/models` (if `baseUrl` already ends with `/v1` or `/v1beta`, `/models` is appended directly, e.g. `https://{{domain}}/v1beta/models` or `https://{{domain}}/v1/models` with `x-goog-api-key`)
* **Request Headers**:
```http
x-goog-api-key: <API Key>
Content-Type: application/json
```
*(Also supports URL query parameter `?key=<API Key>` or `Authorization: Bearer <API Key>` on `/v1beta/models`)*
* **Curl Example**:
```bash
curl -X GET https://{{domain}}/v1beta/models \
  -H "x-goog-api-key: sk-your_token" \
  -H "Content-Type: application/json"
```
* **Response Format & Parsing**:
  Read the `models` array in the returned JSON, extract the `name` field of each item and strip the leading `models/` prefix (or read `baseModelId` directly), matching the native Google Gemini format `{ "models": [{ "name": "models/gemini-..." }] }`:
```json
{
  "models": [
    {
      "name": "models/gemini-2.5-flash",
      "baseModelId": "gemini-2.5-flash",
      "displayName": "Gemini 2.5 Flash",
      "description": "Google Gemini 2.5 Flash multimodal model",
      "supportedGenerationMethods": [
        "generateContent",
        "streamGenerateContent"
      ]
    }
  ]
}
```
