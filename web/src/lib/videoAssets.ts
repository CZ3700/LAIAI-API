import { api, ApiError, uuid } from '@/lib/api'
import { useAuth } from '@/stores/auth'

export interface AssetCapabilities { ready:boolean; project?:string; scope_id?:string; virtual:boolean; portrait:boolean; media_types:string[]; sync:boolean; update:boolean; delete:boolean; delete_empty_group:boolean; upload:boolean; reason?:string }
export interface AssetOperation { operation_id:string;kind:string;resource_id:string;status:string;poll_url?:string;request_id?:string;error?:{code:string;message:string;retryable:boolean};completed_at?:string|null }
export interface AssetGroup { id:string;kind:string;name:string;description:string;status:string;asset_count:number;authorization_status:string;version:number;source:string;last_synced_at?:string;portrait_session_id?:string;deletion_mode?:'official'|'local_failed'|'blocked';deletion_block_reason?:string;delete_operation?:AssetOperation }
export interface VideoAsset { id:string;group_id:string;media_type:string;name:string;status:string;stage:string;version:number;bytes:number;duration_ms:number;error_code?:string;reference?:string;preview_url?:string;created_at:string;source:string }
export interface Portrait { session_id:string;status:string;authorization_url?:string;expires_at?:string;group_id?:string }
export interface Page<T> {data:T[];total:number;next_cursor?:string}
export interface SyncJob {id:string;status:string;phase:string;pages:number;items:number;error_code?:string}
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
export async function uploadAssetFile(id:string,file:File){const token=useAuth().token;const response=await fetch(`/api/video/media-uploads/${encodeURIComponent(id)}/complete`,{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/octet-stream','Idempotency-Key':uuid()},body:file});if(!response.ok){const e=await response.json().catch(()=>({error:'upload_failed'}));throw new Error(e.error??'upload_failed')};return response.json()}
export async function assetPreview(path:string){const response=await fetch(path,{headers:{Authorization:`Bearer ${useAuth().token}`},cache:'no-store'});if(!response.ok)throw new Error('preview_unavailable');return URL.createObjectURL(await response.blob())}

export interface AssetDiscovery {id:string;scope_id:string;provider_id:string;kind:string;name:string;description:string;status:string;version:number}

export interface MediaUpload {id:string;media_type:string;status:string;mime?:string;bytes:number;duration_ms:number;error_code?:string;expires_at:string;created_at:string;updated_at:string}
