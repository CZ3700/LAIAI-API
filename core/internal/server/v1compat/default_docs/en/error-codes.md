# Gateway Error Codes & Troubleshooting

Failed API calls return an **HTTP status code** plus a **JSON error body**. The HTTP code **matches `status_code` in call logs** for the same request (`log_id` traceability).

---

## 1. Unified error response

### Platform-local rejections (auth, balance, rate limits)

```json
{
  "error": {
    "message": "账户余额不足0.5元",
    "type": "api_error",
    "code": "402"
  }
}
```

| Field | Meaning |
| :--- | :--- |
| `error.message` | Failure reason (URLs and `sk-` keys redacted for clients) |
| `error.type` | `api_error` for platform-local errors |
| `error.code` | **HTTP status as string** (e.g. `"402"`), not semantic OpenAI codes |

### Upstream pass-through

Valid upstream JSON may be returned **as-is** (vendor `type` / `code`); HTTP status still aligns with logs.

---

## 2. Status code quick reference

| HTTP | Meaning | Example `message` |
| :---: | :--- | :--- |
| **400** | Invalid params, type mismatch, **content / copyright filter** | param errors; `PolicyViolation` / copyright restrictions |
| **401** | Auth failure | `Missing Authorization Header` / `Invalid API Key` |
| **402** | **Insufficient balance** | `账户余额不足…` / `余额不足` |
| **403** | Token / IP / model allow-list / token quota | `API key disabled` / `Model xxx not allowed` / quota exhausted |
| **404** | Model missing, unbound, or no usable channel; or upstream resource missing | `模型不存在: xxx` / `模型未配置渠道: xxx` / `模型无可用渠道: xxx`; message contains `status 404` |
| **429** | **Rate limit or in-flight cap** | `RPS limit exceeded` / `RPM limit exceeded` / low-balance in-flight message |
| **500** | Internal platform error | `Internal server error` |
| **502** | Upstream unavailable / connection failure | upstream text |
| **504** | Timeout / gateway | contains `timeout`, `gateway`, etc. |

> **Note**: insufficient balance is **402**, not 429.

---

## 3. Troubleshooting by scenario

See sections 400–504 in the Chinese doc for the same mapping. Key fixes:

- **402**: recharge; POST blocked when overdrawn, GET poll / DELETE may still work.
- **400**: bad request **or** upstream content/copyright filter — not a token permission issue (that's 403).
- **403**: token settings, IP whitelist, model allow-list — not account balance (402), not content policy (400).
- **429**: backoff retry; reduce concurrent async jobs or raise RPS/RPM limits.
- **404**: `模型不存在` / `模型未配置渠道` / `模型无可用渠道`; or input URL returning 404.
- **502/504**: retry; HA failover may retry on another channel transparently.

---

## 4. Async tasks

Submit/poll for video/long jobs may return HTTP **200** with `"status": "failed"` in the body. Check body `status` and log `status_code`, not HTTP 200 alone.

---

## 5. Logs & HA

1. Every request logs `log_id`, `status_code`, `error_message`, latency, and billing.
2. HA groups may retry on alternate channels after 502/504.
3. Pending async jobs may show `status_code=0` until completion.

---

## 6. By capability (shared with example docs)

| Area | Typical case | Code / note |
| :--- | :--- | :--- |
| **Chat** | Context / param errors | Upstream OpenAI-style `error` may pass through |
| **Image** | Wrong endpoint for model type | **400** |
| **Qwen image** | Invalid `size` or reference URL | **400**; `1024x1024` auto-converts to `1024*1024` |
| **Video (async)** | Poll returns `status: failed` | Poll HTTP **200**; see §4 |
| **Wan video** | Mixing first/last frame with reference media; audio-only refs | **400** |
| **Wan video** | `files` / `links` mixed with first/last-frame roles | **400** |
| **Native routes** | Wrong DashScope/Volc path | **404** or upstream **400** |

Add new error scenarios **only in this article** (`error-codes`); example appendices link here.
