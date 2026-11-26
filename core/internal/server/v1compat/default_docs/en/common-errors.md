
### 5. Common Error Codes & Troubleshooting

HTTP codes, error body format, and troubleshooting are **maintained in one dedicated page** (not duplicated under each example).

👉 **[Gateway Error Codes & Troubleshooting]({{error_codes_href}})**

**Quick reference**:

| Code | Meaning |
| :---: | :--- |
| **402** | Insufficient balance (**not** 429) |
| **400** | Bad request / content or copyright filter |
| **429** | RPS/RPM limits or in-flight task cap (low balance) |
| **403** | Token / IP / model permission (balance → 402; content filter → 400) |
| **200 + `status: failed`** | Async video/long-task business failure on poll |

HTTP status matches call log `status_code` for traceability by `log_id`.

> **Maintenance**: update the `error-codes` article only; this appendix links to it.
