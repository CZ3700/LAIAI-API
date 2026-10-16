export interface LogRow {
  request_id: string; trace_id: string; org_id: number; user_id: number | null; api_key_id: number | null
  method: string; route: string; operation: string; model: string; task_id: string; operation_id: string
  origin_request_id: string; replay_state: string; started_at: string; finished_at: string | null; duration_ms: number | null
  http_status: number; outcome: string; safe_error_code: string; task_status: string; billing_state: string; artifact_status: string
  deleted_at: string | null; task_net_charged_amount: string | null
}
export interface BodyInfo { availability: string; representation: string; expires_at: string | null; bytes: number }
export interface Billing {
  currency: string; state: string; reserved_amount: string; settled_amount: string | null; released_amount: string; net_charged_amount: string | null
  pricing_version: string; billing_rule_version: string; actual_usage: { completion_tokens: number | null; total_tokens: number | null } | null
  entries: { entry_id: string; type: string; amount: string; created_at: string }[]
}
export interface LogDetail { request: LogRow; task: Record<string, unknown> | null; billing: Billing | null; bodies: Record<string, BodyInfo>; as_of: string }
export interface ExecutionRow { id: string; attempt_id?: string; operation_id?: string; type: string; request_id: string; created_at: string; from_status?: string; to_status?: string; http_status?: number; billing_state?: string; safe_error_code?: string; response_body?: BodyInfo }
export interface Page<T> { data: T[]; next_cursor: string | null }
export interface BodyRead { request_id: string; section: string; representation: string; body: unknown; expires_at: string }
