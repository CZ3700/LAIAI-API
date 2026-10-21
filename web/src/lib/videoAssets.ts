import { api, ApiError, uuid } from '@/lib/api'
import { useAuth } from '@/stores/auth'

export interface AssetCapabilities { ready:boolean; project?:string; scope_id?:string; virtual:boolean; portrait:boolean; media_types:string[]; sync:boolean; update:boolean; delete:boolean; delete_empty_group:boolean; upload:boolean; reason?:string }
export interface AssetOperation { operation_id:string;kind:string;resource_id:string;status:string;poll_url?:string;request_id?:string;error?:{code:string;message:string;retryable:boolean};completed_at?:string|null }
export interface AssetGroup { id:string;kind:string;name:string;description:string;status:string;asset_count:number;authorization_status:string;version:number;source:string;last_synced_at?:string;portrait_session_id?:string;deletion_mode?:'official'|'local_failed'|'blocked';deletion_block_reason?:string;create_operation?:AssetOperation;delete_operation?:AssetOperation }
export interface VideoAsset { id:string;group_id:string;media_type:string;name:string;status:string;stage:string;version:number;bytes:number;duration_ms:number;error_code?:string;reference?:string;preview_url?:string;created_at:string;source:string;create_operation?:AssetOperation;delete_operation?:AssetOperation;deletion_mode?:'official'|'local_failed'|'blocked';deletion_block_reason?:string;provider_status?:'active'|'processing'|'failed'|'unknown';media_verification_status?:'verified'|'unverified';generation_status?:'ready'|'blocked';generation_block_reason?:string }
export interface Portrait { session_id:string;status:string;authorization_url?:string;expires_at?:string;group_id?:string }
export interface Page<T> {data:T[];total:number;next_cursor?:string}
export interface SyncJob {id:string;status:string;phase:string;pages:number;items:number;error_code?:string;scope_id?:string;project?:string;kind?:string;mode?:string;request_id?:string;http_status?:number;upstream_request_id?:string;attempts?:number;retry_count?:number;next_attempt_at?:string}
export interface SyncReadback {job_id:string;state:'idle'|'pending'|'complete';error:string}
export function syncIsRunning(job:SyncJob|undefined){return !!job&&['pending','running'].includes(job.status)}
export function syncIsTerminal(job:SyncJob|undefined){return !!job&&['succeeded','failed'].includes(job.status)}
export function validSyncJob(value:unknown):value is SyncJob {
 if(!value||typeof value!=='object')return false
 const job=value as Partial<SyncJob>
 return typeof job.id==='string'&&job.id.trim()===job.id&&/^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$/.test(job.id)&&['pending','running','succeeded','failed'].includes(job.status??'')&&typeof job.phase==='string'&&Number.isSafeInteger(job.pages)&&Number(job.pages)>=0&&Number.isSafeInteger(job.items)&&Number(job.items)>=0
}
export function syncPhaseText(job:SyncJob){return ({groups:'读取素材组',assets:'读取组内素材'} as Record<string,string>)[job.phase]??'核对官方资源'}
const syncErrors:Record<string,string>={
 management_scope_unavailable:'所属账号项目或同步权限已不可用，请核对素材管理配置。',
 resource_query_failed:'暂时无法读取本平台资源记录，请稍后刷新状态。',
 resource_mutation_in_progress:'素材组正在变更，本次同步已暂停；确认操作结果后可重新同步。',
 provider_sync_failed:'官方读取请求未成功，请核对管理权限、项目及接口连接后再试。',
 provider_timeout:'官方读取响应超时。',provider_rate_limited:'官方读取请求触发限流。',provider_unavailable:'官方读取服务暂时不可用。',
 provider_authentication_failed:'官方身份验证未通过，请管理员核对素材管理凭据。',provider_permission_denied:'官方拒绝读取，请管理员核对项目与素材管理权限。',
 provider_response_invalid:'官方返回的数据未通过完整性核验，本次未继续导入。',
 sync_storage_failed:'同步记录暂未保存，请管理员核对存储状态后再试。',sync_evidence_unavailable:'同步请求记录暂不可用，请管理员核对日志存储。',resource_identity_mismatch:'资源所属账号或项目与当前同步范围不一致，已停止导入。',
 checkpoint_expired:'同步断点已过期，需要重新发起同步。',checkpoint_invalid:'同步断点无效，需要重新发起同步。',checkpoint_unavailable:'暂时无法保存同步断点，请稍后重试。',
 pagination_cycle:'官方返回重复的分页位置，已停止同步。',sync_page_limit:'已达到本次同步的分页上限，请由管理员核对资源数量。',
}
export function syncErrorText(job:SyncJob){return job.error_code?(syncIsRunning(job)?'上次读取：':'')+(syncErrors[job.error_code]??(syncIsRunning(job)?'暂未取得有效结果，请查看任务进度。':'同步未完成，请凭任务 ID 核对日志。')):''}
export function syncSummary(job:SyncJob,readback:SyncReadback){
 if(syncIsRunning(job))return `${job.status==='pending'?(job.next_attempt_at?'等待自动重试':'等待同步'):'同步进行中'} · ${syncPhaseText(job)}`
 if(readback.job_id!==job.id||readback.state!=='complete')return job.status==='failed'?'同步未完成，正在刷新已获取的记录。':'官方读取已结束，正在刷新列表。'
 return job.status==='failed'?'同步未完成；列表已刷新，已获取的记录已保留。':'同步已完成，列表已刷新。'
}
export function groupStateText(row:AssetGroup){
 if(row.delete_operation?.status==='unknown')return '删除结果待核验'
 if(row.create_operation?.status==='unknown')return '创建结果待核验'
 return ''
}
export function assetStateText(row:VideoAsset){
 if(row.delete_operation?.status==='unknown')return '删除结果待核验'
 if(row.create_operation?.status==='unknown')return '注册结果待核验'
 return ({active:'已入库',processing:'处理中',failed:'失败',unknown:'官方状态待确认'})[assetProviderState(row)]
}
export function assetProviderState(row:VideoAsset):'active'|'processing'|'failed'|'unknown'{
 if(row.provider_status!==undefined)return ['active','processing','failed'].includes(row.provider_status)?row.provider_status:'unknown'
 // Older responses lack the independent provider projection. Never infer Active from import source or local media stage.
 if(['ready','active'].includes(row.status))return 'active'
 if(row.status==='processing')return 'processing'
 if(row.status==='failed')return 'failed'
 return 'unknown'
}
export function assetStateTone(row:VideoAsset){return row.create_operation?.status==='unknown'||row.delete_operation?.status==='unknown'?'unknown':assetProviderState(row)}
export function assetMediaStateText(row:VideoAsset){return row.media_verification_status==='verified'?'媒体已核验':row.media_verification_status==='unverified'?'媒体待核验':'媒体核验未确认'}
function hasUnresolvedAssetOperation(row:VideoAsset){return [row.create_operation,row.delete_operation].some(op=>op&&['pending','sending','in_flight','unknown'].includes(op.status))}
function assetReferenceReady(row:VideoAsset){return row.generation_status==='ready'&&row.media_verification_status==='verified'&&assetProviderState(row)==='active'&&!hasUnresolvedAssetOperation(row)}
export function assetGenerationText(row:VideoAsset){return assetReferenceReady(row)?'素材引用就绪':'暂不可用于生成'}
const generationReasons:Record<string,string>={
 submission_uncertain:'官方操作结果尚未确认，暂时不能用于生成。',operation_pending:'资源操作正在处理，暂时不能用于生成。',
 provider_not_ready:'官方素材尚未入库，暂时不能用于生成。',media_unverified:'平台尚未完成媒体安全核验，暂时不能用于生成。',
 resource_not_ready:'所属素材组或使用许可尚未满足引用条件，暂时不能用于生成。',
}
export function assetReviewExplanation(row:VideoAsset){
 if(row.delete_operation?.status==='unknown')return '删除是否完成尚未确认，不能重复提交删除。'
 if(row.create_operation?.status==='unknown')return '官方注册结果尚未确认，暂时不能用于生成或按失败清理。'
 if(hasUnresolvedAssetOperation(row))return generationReasons.operation_pending!
 if(assetReferenceReady(row))return '素材引用条件已满足；实际生成仍由任务接口核对模型、配额及授权。'
 if(row.generation_block_reason&&generationReasons[row.generation_block_reason])return generationReasons[row.generation_block_reason]!
 if(row.media_verification_status==='unverified')return generationReasons.media_unverified!
 if(row.media_verification_status!=='verified')return '平台媒体核验状态尚未确认，暂时不能用于生成。'
 return '素材引用状态尚未就绪，请刷新状态或查看请求日志。'
}
export function assetSizeText(row:VideoAsset){
 if(row.media_verification_status!=='verified'||!Number.isSafeInteger(row.bytes)||row.bytes<=0)return '大小未知'
 const units=['B','KB','MB','GB'];let value=row.bytes,unit=0
 while(value>=1000&&unit<units.length-1){value/=1000;unit++}
 return new Intl.NumberFormat('zh-CN',{maximumFractionDigits:2}).format(value)+' '+units[unit]
}
export function assetDurationText(row:VideoAsset){return row.media_verification_status==='verified'&&Number.isSafeInteger(row.duration_ms)&&row.duration_ms>0?new Intl.NumberFormat('zh-CN',{maximumFractionDigits:2}).format(row.duration_ms/1000)+' 秒':'时长未知'}
export function assetQuery(values:Record<string,string|number|undefined>) {const q=new URLSearchParams();for(const [k,v] of Object.entries(values)){if(v!==undefined&&v!=='')q.set(k,String(v))};return q.toString()}
export function assetWrite<T>(method:string,path:string,body?:unknown,key=uuid()){return api<T>(method,path,body,{headers:{'Idempotency-Key':key}})}
// A 202 response must carry an operation. Only HTTP 204 proves synchronous completion.
export async function assetDelete(path:string):Promise<{completed:boolean;operation?:AssetOperation}>{
 const auth=useAuth(),token=auth.token
 const response=await fetch(path,{method:'DELETE',headers:{'Content-Type':'application/json',Authorization:`Bearer ${token}`,'Idempotency-Key':uuid()}})
 if(response.status===204)return {completed:true}
 const data:unknown=await response.json().catch(()=>null)
 if(!response.ok){
  if(response.status===401&&auth.token===token)auth.logout()
  const payload=data&&typeof data==='object'?data as Record<string,unknown>:{}
  const code=typeof payload.code==='string'?payload.code:typeof payload.error==='string'?payload.error:'operation_failed'
  throw new ApiError(response.status,code,response.headers.get('X-Request-Id'),code)
 }
 const op=data&&typeof data==='object'?data as Partial<AssetOperation>:{}
 if(response.status!==202||typeof op.operation_id!=='string'||!op.operation_id||typeof op.resource_id!=='string'||typeof op.kind!=='string'||!['pending','sending','in_flight','unknown','confirmed','rejected'].includes(op.status??'')){
  throw new ApiError(502,'provider_response_invalid',response.headers.get('X-Request-Id'),'provider_response_invalid')
 }
 return {completed:false,operation:{...op,request_id:op.request_id||response.headers.get('X-Request-Id')||undefined} as AssetOperation}
}
export async function uploadAssetFile(id:string,file:File){
 const auth=useAuth(),token=auth.token
 const response=await fetch(`/api/video/media-uploads/${encodeURIComponent(id)}/complete`,{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/octet-stream','Idempotency-Key':uuid()},body:file})
 const data:unknown=await response.json().catch(()=>null)
 if(!response.ok){
  if(response.status===401&&auth.token===token)auth.logout()
  const payload=data&&typeof data==='object'?data as Record<string,unknown>:{}
  const code=typeof payload.code==='string'?payload.code:typeof payload.error==='string'?payload.error:'upload_failed'
  throw new ApiError(response.status,code,response.headers.get('X-Request-Id'),code)
 }
 return data
}
export async function assetPreview(path:string){const response=await fetch(path,{headers:{Authorization:`Bearer ${useAuth().token}`},cache:'no-store'});if(!response.ok)throw new Error('preview_unavailable');return URL.createObjectURL(await response.blob())}

export interface AssetDiscovery {id:string;scope_id:string;provider_id:string;kind:string;name:string;description:string;status:string;version:number}

export interface MediaUpload {id:string;media_type:string;status:string;mime?:string;bytes:number;duration_ms:number;error_code?:string;expires_at:string;created_at:string;updated_at:string}
