import { api, uuid } from '@/lib/api'
import { useAuth } from '@/stores/auth'

export interface AssetCapabilities { ready:boolean; project?:string; scope_id?:string; virtual:boolean; portrait:boolean; media_types:string[]; sync:boolean; update:boolean; delete:boolean; delete_empty_group:boolean; upload:boolean; reason?:string }
export interface AssetGroup { id:string;kind:string;name:string;description:string;status:string;asset_count:number;authorization_status:string;version:number;source:string;last_synced_at?:string;portrait_session_id?:string }
export interface VideoAsset { id:string;group_id:string;media_type:string;name:string;status:string;stage:string;version:number;bytes:number;duration_ms:number;error_code?:string;reference?:string;preview_url?:string;created_at:string;source:string }
export interface Portrait { session_id:string;status:string;authorization_url?:string;expires_at?:string;group_id?:string }
export interface Page<T> {data:T[];total:number;next_cursor?:string}
export interface SyncJob {id:string;status:string;phase:string;pages:number;items:number;error_code?:string}
export function assetQuery(values:Record<string,string|number|undefined>) {const q=new URLSearchParams();for(const [k,v] of Object.entries(values)){if(v!==undefined&&v!=='')q.set(k,String(v))};return q.toString()}
export function assetWrite<T>(method:string,path:string,body?:unknown,key=uuid()){return api<T>(method,path,body,{headers:{'Idempotency-Key':key}})}
export async function uploadAssetFile(id:string,file:File){const token=useAuth().token;const response=await fetch(`/api/video/media-uploads/${encodeURIComponent(id)}/complete`,{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/octet-stream','Idempotency-Key':uuid()},body:file});if(!response.ok){const e=await response.json().catch(()=>({error:'upload_failed'}));throw new Error(e.error??'upload_failed')};return response.json()}
export async function assetPreview(path:string){const response=await fetch(path,{headers:{Authorization:`Bearer ${useAuth().token}`},cache:'no-store'});if(!response.ok)throw new Error('preview_unavailable');return URL.createObjectURL(await response.blob())}

export interface AssetDiscovery {id:string;scope_id:string;provider_id:string;kind:string;name:string;description:string;status:string;version:number}

export interface MediaUpload {id:string;media_type:string;status:string;mime?:string;bytes:number;duration_ms:number;error_code?:string;expires_at:string;created_at:string;updated_at:string}
