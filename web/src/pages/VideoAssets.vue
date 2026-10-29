<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { api, ApiError, copyText } from '@/lib/api'
import { useAuth } from '@/stores/auth'
import QrCode from '@/components/QrCode.vue'
import DetailDrawer from '@/components/DetailDrawer.vue'
import AssetManagementSetup from '@/components/seedance/AssetManagementSetup.vue'
import { assetQuery, assetWrite, assetDelete, assetPreview, assetRegistrationURL, portraitFailureText, validPortraitDiagnosticID, validPortraitHTTPStatus, syncIsRunning, syncIsTerminal, validSyncJob, syncPhaseText, syncErrorText, syncSummary, groupStateText, assetStateText, assetStateTone, assetProviderState, assetMediaStateText, assetGenerationText, assetSizeText, assetDurationText, assetReviewExplanation, type AssetCapabilities, type AssetGroup, type AssetOperation, type VideoAsset, type Page, type Portrait, type SyncJob, type AssetDiscovery, type MediaUpload, type SyncReadback } from '@/lib/videoAssets'

const auth=useAuth()
const setupOpen=ref(false),setupBusy=ref(false)
function closeSetup(){setupOpen.value=false;setupBusy.value=false}
async function setupBound(){await loadScope()}
const kind=ref<'real_person'|'virtual'>('virtual'), caps=ref<AssetCapabilities>()
const groups=ref<AssetGroup[]>([]), assets=ref<VideoAsset[]>([]), selected=ref(''), groupTotal=ref(0), assetTotal=ref(0)
const groupStatus=ref(''), groupName=ref(''), assetName=ref(''), mediaType=ref(''), providerStatus=ref(''), nextGroup=ref(''), nextAsset=ref('')
const busy=ref(false), error=ref(''), notice=ref(''), modal=ref<'group'|'upload'|'rename'|'delete'|'portrait'|'discovery'|'assign'|'license'|'register'|''>('')
const name=ref(''), description=ref(''), sourceURL=ref(''), uploadType=ref('image'), target=ref<AssetGroup|VideoAsset>(), targetGroup=ref(false)
const validSourceURL=computed(()=>assetRegistrationURL(sourceURL.value))
const unresolvedRegistrations=ref<Record<string,boolean>>({})
const registrationUncertain=computed(()=>!!unresolvedRegistrations.value[selected.value])
const uploadID=ref(''), uploadPhase=ref(''), pendingOperations=ref<AssetOperation[]>([])
const deleteLocks=ref<Record<string,'pending'|'unknown'|'refreshing'>>({})
const completedDeletions=ref<{id:string;local:boolean;isGroup:boolean}[]>([])
const settledOperations=new Set<string>()
const operationReadbacks=ref<Record<string,boolean>>({}),operationReadErrors=ref<Record<string,string>>({})
let pollRunning=false,pollSequence=0,runSequence=0
const portrait=ref<Portrait>(), sync=ref<SyncJob>(), previewURL=ref(''), previewType=ref('image')
const previewAsset=ref<VideoAsset>(), previewLoading=ref(false), previewError=ref('')
let previewEpoch=0,scopeEpoch=0,groupsEpoch=0,assetsEpoch=0,uploadsEpoch=0
function closePreview(){previewEpoch++;previewAsset.value=undefined;previewLoading.value=false;previewError.value='';if(previewURL.value)URL.revokeObjectURL(previewURL.value);previewURL.value=''}
const uploads=ref<MediaUpload[]>([]), uploadStatus=ref(''), uploadMediaType=ref(''), uploadCursor=ref(''), uploadTotal=ref(0), selectedUpload=ref<MediaUpload>(), uploadsOpen=ref(false)
const discoveryCursor=ref(''), discoveryTotal=ref(0)
type SyncMode='owned'|'discover'
const emptySyncReadback=():SyncReadback=>({job_id:'',state:'idle',error:''})
const syncReadback=ref<SyncReadback>(emptySyncReadback()),discoveryReadback=ref<SyncReadback>(emptySyncReadback())
const syncQueryError=ref(''),discoveryQueryError=ref(''),syncRestoring=ref(false),syncRecoveryRequired=ref(false),syncDetailMode=ref<SyncMode|''>('')
let syncRestoreEpoch=0
const discoveries=ref<AssetDiscovery[]>([]), discoveryJob=ref<SyncJob>(), assignment=ref<AssetDiscovery>(), organizations=ref<{id:number;name:string}[]>([]), targetOrg=ref(0), evidence=ref(''), assignmentConfirmed=ref(false), licenseUntil=ref(''), reverifyAt=ref('')
const syncActionBlocked=computed(()=>syncRestoring.value||syncRecoveryRequired.value||syncIsRunning(sync.value)||syncIsRunning(discoveryJob.value)||syncReadback.value.state==='pending')
const discoveryActionBlocked=computed(()=>syncIsRunning(sync.value)||syncIsRunning(discoveryJob.value)||discoveryReadback.value.state==='pending')
const syncDetailJob=computed(()=>syncDetailMode.value==='owned'?sync.value:syncDetailMode.value==='discover'?discoveryJob.value:undefined)
const syncDetailReadback=computed(()=>syncDetailMode.value==='owned'?syncReadback.value:discoveryReadback.value)
const syncDetailError=computed(()=>syncDetailMode.value==='owned'?syncQueryError.value:discoveryQueryError.value)
const activeGroup=computed(()=>groups.value.find(g=>g.id===selected.value))
const detailGroup=ref<AssetGroup>()
const shownGroup=computed(()=>groups.value.find(g=>g.id===detailGroup.value?.id)??detailGroup.value)
const shownAsset=computed(()=>assets.value.find(a=>a.id===previewAsset.value?.id)??previewAsset.value)
function showGroupDetails(row:AssetGroup){detailGroup.value=row}
function resourceOperations(row:AssetGroup|VideoAsset|undefined){return row?[row.create_operation,row.delete_operation].filter((op):op is AssetOperation=>!!op&&op.resource_id===row.id):[]}
const operationNames:Record<string,string>={CreateAssetGroup:'创建素材组',CreateAsset:'注册素材',DeleteAssetGroup:'删除素材组',DeleteAsset:'删除素材',UpdateAssetGroup:'编辑素材组',UpdateAsset:'编辑素材'}
const uploadBlockedReason=computed(()=>{
 if(!caps.value?.ready)return errors[caps.value?.reason??'']??'素材管理尚未就绪，请核对配置。'
 if(!activeGroup.value)return '请先选择素材组。'
 if(activeGroup.value.status!=='ready')return activeGroup.value.status==='needs_review'?'素材组创建结果待核验，确认可用后才能添加素材。':'素材组尚未就绪，确认可用后才能添加素材。'
 if(activeGroup.value.kind!=='virtual'&&activeGroup.value.authorization_status!=='authorized')return '真人素材组尚未取得有效使用许可，请先完成认证与许可核验。'
 if(caps.value.url_registration!==true)return '官方网址注册尚未开通，请由管理员核对项目与素材能力。'
 if(!caps.value.media_types.length)return '该项目未启用图片、视频或音频素材能力，请核对管理版本。'
 return ''
})
function openUpload(){if(uploadBlockedReason.value)return;name.value='';sourceURL.value='';uploadType.value=caps.value?.media_types[0]??'image';modal.value='upload'}
const labels:Record<string,string>={ready:'可用',active:'可用',creating:'创建中',pending:'等待处理',processing:'注册中',registering:'注册中',uploading:'上传中',verifying:'核验中',failed:'失败',needs_review:'待核验',deleting:'删除中',deleted:'已删除',expired:'已过期',authorized:'已授权',not_required:'无需真人授权',revoked:'已撤销',unknown:'待核验',succeeded:'已完成',confirmed:'已完成',rejected:'未成功',sending:'提交中',in_flight:'处理中',running:'同步中',imported_pending:'待核验授权',imported_verified:'已核验导入',submission_unknown:'待确认'}
const text=(value:string)=>labels[value]??value
const errors:Record<string,string>={management_scope_not_configured:'素材管理配置尚未就绪。请在配置素材管理中核对账号项目、管理版本、已授权能力及组织默认路由。',execution_policy_required:'管理版本和组织绑定不会自动开启执行许可。请在配置素材管理的第 3 步开启本组织的异步执行许可并保存。',unsupported_capability:'该能力尚未配置或开通，请查看项目能力设置。',invalid_state:'资源状态已变化，请刷新后重试。',resource_in_use:'素材仍被进行中的视频任务引用，暂时不能删除。',group_not_empty:'素材组非空，请先处理组内素材。',authorization_required:'尚未取得有效的真人使用许可。',invalid_request:'参数或媒体校验不通过，请核对类型、大小和素材要求。',not_found:'资源不存在或无权查看。',storage_unavailable:'暂时无法保存数据，请稍后重试。',SourceDownloadFailed:'源文件无法下载，请检查地址是否可访问或已过期。',provider_sync_failed:'官方同步失败，请检查管理权限后重试。',pagination_cycle:'官方分页游标重复，已停止同步。',provider_response_invalid:'官方返回信息不完整，已保留待核验状态。',provider_unavailable:'官方处理暂未确认，请刷新状态。',media_verification_failed:'媒体安全检测或格式校验未通过。',checkpoint_expired:'同步断点已过期，请重新发起同步。',source_unavailable:'源文件无法下载，请检查地址是否可访问或已过期。',source_not_allowed:'源地址不符合安全要求，请使用公开可访问的 HTTPS 地址。',upload_expired:'上传会话已过期，请重新上传。',empty_upload:'上传内容为空，请重新选择文件。'}
const deletionReasons:Record<string,string>={
 real_group_not_supported:'真人素材组暂不支持删除。',group_not_empty:'素材组仍有素材，请先处理组内素材。',
 resource_in_use:'素材组仍被进行中的视频任务引用。',operation_pending:'删除处理中，请等待状态确认。',
 submission_uncertain:'官方提交结果待核验，请刷新或查看请求日志，不能再次删除。',
 asset_not_ready:'素材尚未就绪，请先确认注册结果。',creation_not_rejected:'创建结果尚未确认失败，不能清理该记录。',group_not_ready:'素材组尚未就绪，请先确认创建结果。',
 resource_identity_mismatch:'素材与所属素材组的账号项目不一致，请由管理员核对。',
 scope_unavailable:'资源所属账号项目配置不可用，请先核对管理配置。',
 delete_capability_disabled:'所属项目未启用此资源的官方删除能力。',
 exclusive_project_required:'需先确认该项目仅由本平台写入，才能删除官方空组。',
}
const safeMessages=new Set(['请填写公开可访问的 HTTPS 地址，不要包含账号密码或片段标记。'])
function requestSuffix(id:unknown){return typeof id==='string'&&/^[A-Za-z0-9][A-Za-z0-9._:-]{0,159}$/.test(id)?`（请求 ID：${id}）`:''}
function safeErrorText(e:unknown){
 const message=e instanceof Error?e.message:'',code=e instanceof ApiError?(e.code||message):message
 return (errors[code]??(safeMessages.has(message)?message:'操作未完成，请刷新状态或查看请求日志。'))+requestSuffix(e instanceof ApiError?e.requestId:undefined)
}
function report(e:unknown){error.value=safeErrorText(e)}
function resourceOperationNotice(row:AssetGroup|VideoAsset){return resourceOperations(row).map(op=>operationReadErrors.value[op.operation_id]||(op.status==='unknown'?operationError(op)||'官方操作结果尚未确认，请查看详情。':'')).filter(Boolean).join(' ')}
function context(){const epoch=scopeEpoch,org=auth.orgId,token=auth.token,admin=auth.isSuperadmin;return()=>epoch===scopeEpoch&&org===auth.orgId&&token===auth.token&&admin===auth.isSuperadmin}
async function run(fn:()=>Promise<unknown>){if(busy.value)return;const sequence=++runSequence;busy.value=true;error.value='';try{await fn()}catch(e){if(sequence===runSequence)report(e)}finally{if(sequence===runSequence)busy.value=false}}
function trackOperation(op:AssetOperation){
 if(!op.operation_id||settledOperations.has(op.operation_id))return
 const index=pendingOperations.value.findIndex(item=>item.operation_id===op.operation_id),previous=pendingOperations.value[index]
 if(['confirmed','rejected','unknown'].includes(op.status)&&(!previous||previous.status!==op.status||previous.error?.code!==op.error?.code))operationReadbacks.value[op.operation_id]=false
 if(index<0)pendingOperations.value.push(op);else pendingOperations.value[index]={...op,request_id:op.request_id||pendingOperations.value[index]?.request_id}
 if(op.kind==='DeleteAssetGroup'||op.kind==='DeleteAsset')deleteLocks.value[op.resource_id]=op.status==='unknown'?'unknown':'pending'
}
function trackResourceOperations(row:AssetGroup|VideoAsset,isGroup:boolean,includeCompletedCreation=false){
 const create=row.create_operation,remove=row.delete_operation
 if(create?.resource_id===row.id&&create.kind===(isGroup?'CreateAssetGroup':'CreateAsset')&&(includeCompletedCreation||['pending','sending','in_flight','unknown'].includes(create.status)))trackOperation(create)
 if(remove?.resource_id===row.id&&remove.kind===(isGroup?'DeleteAssetGroup':'DeleteAsset'))trackOperation(remove)
}
function groupDeleteReason(row:AssetGroup){
 if(deleteLocks.value[row.id])return deleteLocks.value[row.id]==='unknown'?'删除结果待核验，不能重复提交。':'删除已提交，正在确认结果与刷新列表。'
 if(row.delete_operation&&['pending','sending','in_flight','unknown','confirmed'].includes(row.delete_operation.status))return row.delete_operation.status==='unknown'?'删除结果待核验，不能重复提交。':'删除正在确认，请等待列表刷新。'
 if(row.kind!=='virtual')return '真人素材组暂不支持删除。'
 if(row.asset_count>0)return '素材组仍有素材，请先处理组内素材。'
 if(row.deletion_mode==='local_failed'&&row.status==='failed')return ''
 // Eligibility belongs to the group's fixed provider scope, not the current default route.
 if(row.deletion_mode==='official'&&row.status==='ready')return ''
 return deletionReasons[row.deletion_block_reason??'']??'当前记录暂不可删除，请刷新配置或查看请求日志。'
}
function assetDeleteReason(row:VideoAsset){
 if(deleteLocks.value[row.id])return deleteLocks.value[row.id]==='unknown'?'删除结果待核验，不能重复提交。':'删除已提交，请等待确认，不能重复提交。'
 if(row.delete_operation&&['pending','sending','in_flight','unknown','confirmed'].includes(row.delete_operation.status))return row.delete_operation.status==='unknown'?'删除结果待核验，不能重复提交。':'删除正在确认，请等待列表刷新。'
 if(row.deletion_mode==='local_failed'&&row.status==='failed')return ''
 if(row.deletion_mode==='official')return ''
 return deletionReasons[row.deletion_block_reason??'']??'当前素材暂不可删除，请刷新状态或查看操作详情。'
}
const deleteTarget=computed(()=>targetGroup.value?groups.value.find(row=>row.id===target.value?.id):assets.value.find(row=>row.id===target.value?.id))
const deleteBlocked=computed(()=>!deleteTarget.value?'记录已变化，请关闭后刷新列表。':targetGroup.value?groupDeleteReason(deleteTarget.value as AssetGroup):assetDeleteReason(deleteTarget.value as VideoAsset))
const localCleanup=computed(()=>deleteTarget.value?.deletion_mode==='local_failed')
async function loadGroups(append=false){const epoch=++groupsEpoch,org=auth.orgId;const p=await api<Page<AssetGroup>>('GET','/api/video/asset-groups?'+assetQuery({kind:kind.value,name:groupName.value,status:groupStatus.value,limit:30,cursor:append?nextGroup.value:undefined}));if(epoch!==groupsEpoch||org!==auth.orgId)return false;groups.value=append?[...groups.value,...p.data]:p.data;for(const group of p.data)trackResourceOperations(group,true);groupTotal.value=p.total;nextGroup.value=p.next_cursor??'';if(!groups.value.some(g=>g.id===selected.value)){selected.value=groups.value[0]?.id??'';await loadAssets()};return epoch===groupsEpoch&&org===auth.orgId}
async function loadAssets(append=false){const epoch=++assetsEpoch,org=auth.orgId,group=selected.value;if(!selected.value){assets.value=[];assetTotal.value=0;return true};const p=await api<Page<VideoAsset>>('GET','/api/video/assets?'+assetQuery({group_id:selected.value,name:assetName.value,media_type:mediaType.value,provider_status:providerStatus.value,limit:30,cursor:append?nextAsset.value:undefined}));if(epoch!==assetsEpoch||org!==auth.orgId||group!==selected.value)return false;assets.value=append?[...assets.value,...p.data]:p.data;for(const asset of p.data)trackResourceOperations(asset,false);assetTotal.value=p.total;nextAsset.value=p.next_cursor??'';return true}
async function refreshResources(){const current=context();if(!await loadGroups()||!current())return false;if(!await loadAssets()||!current())return false;return current()}
async function refresh(){const current=context();if(!await refreshResources()||!current())return false;await loadUploads();return current()}
function close(){uploadID.value='';uploadPhase.value='';modal.value='';portrait.value=undefined;if(previewURL.value)URL.revokeObjectURL(previewURL.value);previewURL.value='';sourceURL.value=''}
function openGroup(){name.value='';description.value='';modal.value='group'}
function openRename(row:AssetGroup|VideoAsset,isGroup:boolean){target.value=row;targetGroup.value=isGroup;name.value=row.name;description.value=isGroup?(row as AssetGroup).description:'';modal.value='rename'}
function openDelete(row:AssetGroup|VideoAsset,isGroup:boolean){if(isGroup?groupDeleteReason(row as AssetGroup):assetDeleteReason(row as VideoAsset))return;target.value=row;targetGroup.value=isGroup;error.value='';modal.value='delete'}
async function createGroup(){await run(async()=>{
 const current=context(),type=kind.value,input={name:name.value,description:description.value}
 if(type==='virtual'){
  const group=await assetWrite<AssetGroup>('POST','/api/video/asset-groups',{kind:'virtual',...input})
  if(!current())return
  trackResourceOperations(group,true,true);close();notice.value='已受理创建素材组，等待官方确认。'
  if(await refresh()&&current()&&groups.value.some(row=>row.id===group.id)){selected.value=group.id;await loadAssets()}
 }else{
  const result=await assetWrite<Portrait>('POST','/api/video/portrait-authorizations',input)
  if(!current())return
  portrait.value=result;modal.value='portrait';await checkPortrait()
 }
})}
async function checkPortrait(){if(!portrait.value)return;const current=context(),id=portrait.value.session_id;const out=await api<Portrait>('GET',`/api/video/portrait-authorizations/${id}`);if(!current()||portrait.value?.session_id!==id)return;if(out.status==='pending'){const withLink=await api<Portrait>('POST',`/api/video/portrait-authorizations/${out.session_id}/link-reads`,{});if(current()&&portrait.value?.session_id===id)portrait.value=withLink}else{portrait.value=out;if(out.status==='succeeded'){await refresh();if(current()&&out.group_id)selected.value=out.group_id}}}
async function rename(){await run(async()=>{if(!target.value)return;const current=context();const body:Record<string,unknown>={name:name.value,expected_version:target.value.version};if(targetGroup.value)body.description=description.value;const operation=await assetWrite<AssetOperation>('PATCH',`/api/video/${targetGroup.value?'asset-groups':'assets'}/${target.value.id}`,body);if(!current())return;trackOperation(operation);close();notice.value='名称变更已提交，官方确认后更新。';await refresh()})}
async function remove(){await run(async()=>{
 if(!deleteTarget.value||deleteBlocked.value)return
 const row=deleteTarget.value,id=row.id,isGroup=targetGroup.value,local=localCleanup.value,current=context()
 deleteLocks.value[id]='pending'
 let accepted=false
 try{
  const result=await assetDelete(`/api/video/${isGroup?'asset-groups':'assets'}/${id}`),operation=result.operation
  if(!current())return
  accepted=true
  if(operation?.operation_id){
   if(operation.resource_id!==id||operation.kind!==(isGroup?'DeleteAssetGroup':'DeleteAsset'))throw new Error('invalid_delete_receipt')
   trackOperation(operation)
  }else if(result.completed){completedDeletions.value.push({id,local,isGroup});deleteLocks.value[id]='refreshing'}
  else throw new Error('invalid_delete_receipt')
  close();notice.value=local?'清理已受理，正在刷新列表。':'删除请求已受理，正在查询官方处理结果。'
  if(await refresh()&&current())finishCompletedDeletions()
 }catch(e){
  if(!current())return
  if(!accepted&&e instanceof ApiError&&[400,401,403,404,409,422,429].includes(e.status)){delete deleteLocks.value[id];report(e)}
  else{if(!accepted||!pendingOperations.value.some(op=>op.resource_id===id)&&!completedDeletions.value.some(item=>item.id===id))deleteLocks.value[id]='unknown';report(e);notice.value=accepted?'请求已受理，但列表刷新尚未完成；系统将继续查询，不会重复发送删除。':'删除是否受理尚未确认，请刷新状态或查看请求日志；不会自动重发删除。'}
 }
})}
function finishCompletedDeletions(){
 const remaining:typeof completedDeletions.value=[]
 for(const item of completedDeletions.value){
  if((item.isGroup?groups.value:assets.value).some(row=>row.id===item.id)){remaining.push(item);continue}
  delete deleteLocks.value[item.id];error.value='';notice.value=item.local?'失败记录已清理，未调用官方删除；历史日志与账务记录仍保留。':'删除已完成，列表已刷新。'
 }
 completedDeletions.value=remaining
 if(remaining.length)notice.value='删除已受理，但列表仍包含该记录；正在继续刷新确认，不会重复发送删除。'
}
async function upload(){await run(async()=>{
 if(uploadBlockedReason.value||registrationUncertain.value)return
 const current=context(),group=selected.value,url=validSourceURL.value,type=uploadType.value,assetName=name.value.trim()||'素材'
 if(!url)throw new Error('请填写公开可访问的 HTTPS 地址，不要包含账号密码或片段标记。')
 uploadPhase.value='registering'
 let accepted=false
 try{
  const asset=await assetWrite<VideoAsset>('POST','/api/video/assets',{group_id:group,url,media_type:type,name:assetName})
  if(!current())return
  if(!asset||typeof asset.id!=='string'||!/^asset_[A-Za-z0-9_-]+$/.test(asset.id)||asset.group_id!==group||asset.media_type!==type)throw new ApiError(502,'provider_response_invalid',null,'provider_response_invalid')
  accepted=true;trackResourceOperations(asset,false,true);close();notice.value='网址已提交官方注册，由火山读取并处理素材；平台不存储或扫描媒体文件。';await refresh()
 }catch(e){
  if(current()&&!accepted&&!(e instanceof ApiError&&[400,401,403,404,409,422,429].includes(e.status)))unresolvedRegistrations.value[group]=true
  throw e
 }finally{if(current())uploadPhase.value=''}
})}
function syncRefs(mode:SyncMode){return mode==='owned'?{job:sync,readback:syncReadback,queryError:syncQueryError}:{job:discoveryJob,readback:discoveryReadback,queryError:discoveryQueryError}}
function acceptSyncJob(mode:SyncMode,result:unknown){
 if(!validSyncJob(result))throw new ApiError(502,'provider_response_invalid',null,'provider_response_invalid')
 const state=syncRefs(mode),old=state.job.value
 state.job.value=result;state.queryError.value=''
 if(old?.id!==result.id||old.status!==result.status)state.readback.value={job_id:result.id,state:syncIsTerminal(result)?'pending':'idle',error:''}
}
async function restoreLatestSync(){
 const current=context(),type=kind.value,epoch=++syncRestoreEpoch
 if(!caps.value?.ready){sync.value=undefined;syncReadback.value=emptySyncReadback();return}
 syncRestoring.value=true;syncQueryError.value=''
 try{
  const result=await api<{job:SyncJob|null}>('GET','/api/video/asset-sync-jobs/latest?'+assetQuery({kind:type}))
  if(!current()||epoch!==syncRestoreEpoch||kind.value!==type)return
  if(result.job===null){sync.value=undefined;syncReadback.value=emptySyncReadback()}
  else{
   if(!validSyncJob(result.job)||(result.job.kind&&result.job.kind!==type)||(result.job.mode&&result.job.mode!=='owned')||(result.job.scope_id&&result.job.scope_id!==caps.value?.scope_id))throw new ApiError(502,'provider_response_invalid',null,'provider_response_invalid')
   acceptSyncJob('owned',result.job)
  }
  syncRecoveryRequired.value=false
 }catch(e){
  if(!current()||epoch!==syncRestoreEpoch||kind.value!==type)return
  syncRecoveryRequired.value=!(e instanceof ApiError&&e.status===404)
  syncQueryError.value=e instanceof ApiError&&e.status===404?'当前服务暂不支持读取最近的同步任务；可发起新同步。':'暂时无法恢复同步任务。'+safeErrorText(e)
 }finally{if(current()&&epoch===syncRestoreEpoch&&kind.value===type)syncRestoring.value=false}
}
async function refreshSyncTask(mode:SyncMode,force=false){
 const current=context(),state=syncRefs(mode),id=state.job.value?.id
 if(!id)return
 try{
  if(syncIsRunning(state.job.value)||force){
   const out=await api<SyncJob>('GET',mode==='owned'?`/api/video/asset-sync-jobs/${id}`:`/api/admin/video/asset-discovery-jobs/${id}?org_id=${auth.orgId}`)
   if(!current()||state.job.value?.id!==id)return
   if(!validSyncJob(out)||out.id!==id)throw new ApiError(502,'provider_response_invalid',null,'provider_response_invalid')
   acceptSyncJob(mode,out)
  }
 }catch(e){if(current()&&state.job.value?.id===id)state.queryError.value='暂时无法查询同步状态。'+safeErrorText(e);return}
 if(!current()||state.job.value?.id!==id||!syncIsTerminal(state.job.value)||state.readback.value.state!=='pending')return
 try{
  const refreshed=mode==='owned'?await refreshResources():await loadDiscoveries()
  if(!current()||state.job.value?.id!==id||refreshed===false)return
  state.readback.value={job_id:id,state:'complete',error:''}
 }catch(e){if(current()&&state.job.value?.id===id)state.readback.value={job_id:id,state:'pending',error:'列表暂未刷新，系统将继续读取，不会重新发起同步。'+safeErrorText(e)}}
}
async function refreshSyncStatus(){await run(async()=>{if(syncRecoveryRequired.value||!sync.value)await restoreLatestSync();else await refreshSyncTask('owned',true)})}
async function startSync(){await run(async()=>{
 if(syncActionBlocked.value)return
 const current=context(),type=kind.value
 try{
  const result=await assetWrite<SyncJob>('POST','/api/video/asset-sync-jobs',{kind:type})
  if(!current()||kind.value!==type)return
  acceptSyncJob('owned',result);notice.value='';await refreshSyncTask('owned')
 }catch(e){if(current()&&kind.value===type){syncRecoveryRequired.value=true;syncQueryError.value='同步是否受理尚未确认，请先刷新同步状态。'+safeErrorText(e)}}
})}
async function showPreview(row:VideoAsset){
 closePreview();previewAsset.value=row;previewType.value=row.media_type
 if(!row.source_available&&!row.preview_url)return
 const epoch=previewEpoch,current=context();previewLoading.value=true
 try{
  let url=''
  if(row.source_available){
   const result=await api<{url:string;source:string}>('GET',`/api/video/assets/${row.id}/source`)
   if(result.source!=='upstream'||!assetRegistrationURL(result.url))throw new Error('preview_unavailable')
   url=result.url
  }else url=await assetPreview(row.preview_url!)
  if(epoch!==previewEpoch||!current()){if(url.startsWith('blob:'))URL.revokeObjectURL(url);return}
  previewURL.value=url
 }catch{if(epoch===previewEpoch&&current())previewError.value='预览暂不可用或链接已过期，请刷新官方预览后重试。'}
 finally{if(epoch===previewEpoch)previewLoading.value=false}
}
async function loadUploads(append=false){const epoch=++uploadsEpoch,org=auth.orgId;const page=await api<Page<MediaUpload>>('GET','/api/video/media-uploads?'+assetQuery({status:uploadStatus.value,media_type:uploadMediaType.value,limit:20,cursor:append?uploadCursor.value:undefined}));if(epoch!==uploadsEpoch||org!==auth.orgId)return;uploads.value=append?[...uploads.value,...page.data]:page.data;uploadTotal.value=page.total;uploadCursor.value=page.next_cursor??''}
function prepareRegistration(row:MediaUpload){selectedUpload.value=row;name.value='';modal.value='register'}
async function registerUpload(){await run(async()=>{
 if(!selectedUpload.value||!selected.value||uploadBlockedReason.value||!caps.value?.local_upload)return
 const current=context(),group=selected.value,id=selectedUpload.value.id,assetName=name.value
 const asset=await assetWrite<VideoAsset>('POST','/api/video/assets',{group_id:group,media_ref:id,name:assetName})
 if(!current())return
 trackResourceOperations(asset,false,true);close();notice.value='已恢复注册到选中的素材组，官方确认前状态为注册中。';await refresh()
})}
async function openDiscover(){await run(async()=>{const current=context(),result=await api<{orgs:{id:number;name:string}[]}>('GET','/api/admin/orgs');if(!current())return;organizations.value=result.orgs;await loadDiscoveries();if(current())modal.value='discovery'})}
async function loadDiscoveries(append=false){if(!caps.value?.scope_id)return false;const current=context(),scope=caps.value.scope_id;const page=await api<Page<AssetDiscovery>>('GET','/api/admin/video/asset-discoveries?'+assetQuery({scope_id:scope,limit:30,cursor:append?discoveryCursor.value:undefined}));if(!current()||caps.value?.scope_id!==scope)return false;discoveries.value=append?[...discoveries.value,...page.data]:page.data;discoveryCursor.value=page.next_cursor??'';discoveryTotal.value=page.total;return true}
async function discover(){await run(async()=>{
 if(discoveryActionBlocked.value)return
 const current=context()
 try{const result=await assetWrite<SyncJob>('POST','/api/admin/video/asset-discovery-jobs',{org_id:auth.orgId,scope_id:caps.value?.scope_id,kind:kind.value});if(!current())return;acceptSyncJob('discover',result);await refreshSyncTask('discover')}
 catch(e){if(current())discoveryQueryError.value='官方资源发现未完成。'+safeErrorText(e)}
})}
function openAssign(row:AssetDiscovery){assignment.value=row;targetOrg.value=0;evidence.value='';assignmentConfirmed.value=false;modal.value='assign'}
async function assign(){await run(async()=>{if(!assignment.value||!targetOrg.value||!assignmentConfirmed.value||!evidence.value.trim())return;const current=context();await assetWrite('POST',`/api/admin/video/asset-discoveries/${assignment.value.id}/assign`,{org_id:targetOrg.value,expected_version:assignment.value.version,evidence:{review_summary:evidence.value}});if(!current())return;await loadDiscoveries();if(!current())return;await refresh();if(!current())return;modal.value='discovery';notice.value='素材组已明确归属。请在目标组织同步组内素材，真人组需另行核验使用许可。'})}
function openLicense(){evidence.value='';licenseUntil.value=new Date(Date.now()+24*3600000).toISOString().slice(0,16);reverifyAt.value=new Date(Date.now()+3600000).toISOString().slice(0,16);modal.value='license'}
async function reviewLicense(){await run(async()=>{if(!activeGroup.value?.portrait_session_id)return;const current=context();await assetWrite('POST',`/api/admin/video/orgs/${auth.orgId}/portrait-authorizations/${activeGroup.value.portrait_session_id}/license`,{source:'reviewed_console',valid_from:new Date().toISOString(),valid_until:new Date(licenseUntil.value+'Z').toISOString(),reverify_due_at:new Date(reverifyAt.value+'Z').toISOString(),evidence:{review_summary:evidence.value}});if(!current())return;close();await refresh();if(!current())return;notice.value='使用许可核验已记录。到期或撤销后自动阻止新的生成任务。'})}
let timer:ReturnType<typeof setInterval>|undefined
let previousOverflow='', previousFocus:HTMLElement|null=null
watch(modal,async(value,old)=>{if(value&&!old){previousOverflow=document.body.style.overflow;previousFocus=document.activeElement as HTMLElement;document.body.style.overflow='hidden';await nextTick();document.querySelector<HTMLElement>('.va-dialog input,.va-dialog button')?.focus()}else if(!value&&old){document.body.style.overflow=previousOverflow;previousFocus?.focus()}})
function modalKeyboard(event:KeyboardEvent){if(!modal.value)return;if(event.key==='Escape'&&!busy.value){close();return};if(event.key!=='Tab')return;const items=Array.from(document.querySelectorAll<HTMLElement>('.va-dialog button:not(:disabled),.va-dialog input:not(:disabled),.va-dialog textarea,.va-dialog select,.va-dialog a[href]'));if(!items.length)return;const first=items[0],last=items[items.length-1];if(event.shiftKey&&document.activeElement===first){event.preventDefault();last?.focus()}else if(!event.shiftKey&&document.activeElement===last){event.preventDefault();first?.focus()}}

async function loadScope(){
 unresolvedRegistrations.value={}
 const epoch=++scopeEpoch,org=auth.orgId;++syncRestoreEpoch;++runSequence;++pollSequence;pollRunning=false;busy.value=true;closePreview();close();groupsEpoch++;assetsEpoch++;uploadsEpoch++
 pendingOperations.value=[];deleteLocks.value={};operationReadbacks.value={};operationReadErrors.value={};syncReadback.value=emptySyncReadback();discoveryReadback.value=emptySyncReadback();syncQueryError.value=discoveryQueryError.value='';syncRestoring.value=syncRecoveryRequired.value=false;syncDetailMode.value='';completedDeletions.value=[];settledOperations.clear();target.value=undefined;detailGroup.value=undefined;selectedUpload.value=undefined;organizations.value=[];assignment.value=undefined;groups.value=[];assets.value=[];uploads.value=[];groupTotal.value=assetTotal.value=uploadTotal.value=0;nextGroup.value=nextAsset.value=uploadCursor.value='';discoveries.value=[];discoveryCursor.value='';discoveryJob.value=undefined;sync.value=undefined;selected.value='';caps.value=undefined;error.value=notice.value=''
 try{const value=await api<AssetCapabilities>('GET','/api/video/asset-capabilities');if(epoch!==scopeEpoch||org!==auth.orgId)return;caps.value=value;try{await refresh()}catch(e){if(epoch===scopeEpoch&&org===auth.orgId)report(e)};if(epoch===scopeEpoch&&org===auth.orgId)await restoreLatestSync()}
 catch(e){if(epoch===scopeEpoch&&org===auth.orgId)report(e)}
 finally{if(epoch===scopeEpoch&&org===auth.orgId)busy.value=false}
}
const operationMessages:Record<string,string>={
 provider_unavailable:'官方处理结果待核验，请勿重复提交。',
 provider_rejected:'官方已拒绝此操作，请核对权限与资源状态。',
 provider_group_not_found:'官方未找到该素材组。',
 group_not_empty:'官方素材组内仍有素材，不能删除。',
 group_empty_check_failed:'无法确认官方素材组为空，本次未删除。',
 group_absence_unverified:'官方返回素材组不存在，但后续核验尚未确认。',
 group_identity_mismatch:"删除请求与素材组所属账号项目不一致。",
 provider_response_invalid:'官方返回信息不完整，操作结果待核验。',
 submission_uncertain:'操作结果待核验，请勿重复提交。',
 unsupported_capability:'所属账号项目已不允许此操作。',
 execution_payload_unavailable:'执行请求数据已不可用，请查看请求日志。',
 group_not_empty_or_unverified:'无法确认官方素材组为空，本次未删除。',
}
function operationError(op:AssetOperation){return op.error?.code?(errors[op.error.code]??operationMessages[op.error.code]??'操作结果尚未确认，请查看请求日志。'):''}
function operationFeedback(op:AssetOperation){
 const deletion=op.kind==='DeleteAssetGroup'||op.kind==='DeleteAsset'
 const detail=operationError(op)?` ${operationError(op)}`:''
 const request=requestSuffix(op.request_id)
 if(op.status==='confirmed'){error.value='';notice.value=(deletion?'删除已完成，列表已刷新。':'变更已完成，列表已刷新。')+request}
 else if(op.status==='rejected'){notice.value='';error.value=(deletion?'删除未成功。':'变更未成功。')+detail+request+' 请核对请求日志和当前状态。'}
 // Unknown outcomes stay attached to their own resource and never replace unrelated task feedback.
}
async function poll(){
 if(document.hidden||pollRunning)return
 const current=context(),sequence=++pollSequence
 let attemptedResourceReadback=false
 pollRunning=true
 try{
  if(busy.value){if(uploadID.value){const id=uploadID.value;try{const out=await api<{status:string}>('GET',`/api/video/media-uploads/${id}`);if(current()&&uploadID.value===id)uploadPhase.value=out.status}catch{}}return}
  for(const pending of [...pendingOperations.value]){
   try{
    const op=['confirmed','rejected'].includes(pending.status)?pending:await api<AssetOperation>('GET',`/api/video/asset-operations/${pending.operation_id}`)
    if(!current())return
    if(op.operation_id!==pending.operation_id||op.resource_id!==pending.resource_id||op.kind!==pending.kind)throw new Error('invalid_operation_receipt')
    if(!op.request_id&&pending.request_id)op.request_id=pending.request_id
    trackOperation(op);delete operationReadErrors.value[op.operation_id]
    if(['confirmed','rejected','unknown'].includes(op.status)&&!operationReadbacks.value[op.operation_id]){
     attemptedResourceReadback=true
     if(!await refresh()||!current())continue
     if(op.status==='confirmed'&&(op.kind==='DeleteAssetGroup'?groups.value:op.kind==='DeleteAsset'?assets.value:[]).some(row=>row.id===op.resource_id)){notice.value='删除已确认，列表仍在更新；正在继续刷新，不会重复发送删除。';continue}
     operationReadbacks.value[op.operation_id]=true
     if(op.status!=='unknown'){settledOperations.add(op.operation_id);pendingOperations.value=pendingOperations.value.filter(item=>item.operation_id!==op.operation_id);delete deleteLocks.value[op.resource_id]}
     operationFeedback(op)
    }
   }catch(e){if(!current())return;operationReadErrors.value[pending.operation_id]='此操作状态暂未更新。'+safeErrorText(e)}
  }
  // Sync progress and readback are independent from unresolved resource operations.
  await refreshSyncTask('discover');if(!current())return
  await refreshSyncTask('owned');if(!current())return
  if(completedDeletions.value.length){try{if(await refresh()&&current())finishCompletedDeletions()}catch(e){if(current())report(e)};if(!current())return}
  if(modal.value==='portrait'&&portrait.value&&!['succeeded','failed','expired'].includes(portrait.value.status)){try{await checkPortrait()}catch(e){if(current())report(e)};if(!current())return}
  if(uploadsOpen.value&&uploads.value.some(u=>['uploading','verifying'].includes(u.status))){await loadUploads();if(!current())return}
  if(!attemptedResourceReadback&&(groups.value.some(g=>['creating','deleting'].includes(g.status))||assets.value.some(a=>['registering','deleting'].includes(a.stage))))await refresh()
 }catch(e){if(current())report(e)}
 finally{if(sequence===pollSequence)pollRunning=false}
}
watch(kind,()=>{closePreview();sync.value=undefined;syncReadback.value=emptySyncReadback();syncDetailMode.value='';void run(async()=>{const current=context(),type=kind.value;try{await refresh()}finally{if(current()&&kind.value===type)await restoreLatestSync()}})});watch(selected,()=>{closePreview();void run(()=>loadAssets())});watch(()=>[auth.orgId,auth.token,auth.isSuperadmin],()=>void loadScope(),{flush:'sync'})
watch(()=>[auth.orgId,auth.token,auth.isSuperadmin],closeSetup,{flush:'sync'})
onMounted(()=>{window.addEventListener('keydown',modalKeyboard);void loadScope();timer=setInterval(poll,5000)})
onUnmounted(()=>{scopeEpoch++;syncRestoreEpoch++;groupsEpoch++;assetsEpoch++;uploadsEpoch++;++runSequence;++pollSequence;closePreview();window.removeEventListener('keydown',modalKeyboard);if(modal.value)document.body.style.overflow=previousOverflow;if(timer)clearInterval(timer);if(previewURL.value)URL.revokeObjectURL(previewURL.value)})
</script>

<template>
 <main class="va-page">
  <header class="va-heading"><div><p class="va-eyebrow">SEEDANCE · PRIVATE ASSETS</p><h1>私域素材库</h1><p>管理真人与虚拟素材，保持官方资源和本平台使用权限一致。</p></div><div class="space-y-2"><div class="va-project">{{ caps?.route_source==='shared_default'?'继承项目':'项目' }} <strong>{{ caps?.project ?? '未配置' }}</strong></div><button v-if="auth.isSuperadmin" type="button" :disabled="busy || setupBusy" @click="setupOpen=true">配置素材管理</button></div></header>
  <p class="va-help va-visibility">{{ auth.isSuperadmin && caps?.resource_visibility==='organization_admin'?'超级管理员：查看和管理当前组织资源。':'仅本人可见：共用项目配置不共享素材组或素材；同一账号的 Key 可访问本人资源。' }}</p>
  <div v-if="error" class="va-alert" role="alert">{{ error }} <button @click="error=''" aria-label="关闭错误">×</button></div>
  <div v-if="notice" class="va-notice" role="status">{{ notice }}</div>
  <div v-if="caps && !caps.ready" class="va-empty va-setup">{{ errors[caps.reason??'']??caps.reason }} <button v-if="auth.isSuperadmin" type="button" :disabled="busy || setupBusy" @click="setupOpen=true">配置素材管理</button></div>
  <div class="va-tabs" role="tablist" aria-label="素材类别"><button :disabled="busy" :aria-selected="kind==='real_person'" role="tab" :class="{active:kind==='real_person'}" @click="kind='real_person'">真人素材</button><button :disabled="busy" :aria-selected="kind==='virtual'" role="tab" :class="{active:kind==='virtual'}" @click="kind='virtual'">虚拟素材</button></div>
  <details class="va-upload-records" :open="uploadsOpen" @toggle="uploadsOpen=($event.target as HTMLDetailsElement).open"><summary>上传记录 · {{ uploadTotal }} 条 <span>关闭弹窗后可在此查看进度或继续注册</span></summary><p class="va-help">以下为可见范围内的历史上传会话。新素材使用 HTTPS 网址直接注册火山，平台不再接收或扫描本地媒体文件。</p><form class="va-filters" @submit.prevent="run(()=>loadUploads())"><select v-model="uploadStatus" aria-label="上传阶段"><option value="">全部上传阶段</option><option value="uploading">上传中</option><option value="verifying">核验中</option><option value="ready">核验完成</option><option value="failed">失败</option><option value="expired">已过期</option></select><select v-model="uploadMediaType" aria-label="上传类型"><option value="">全部类型</option><option value="image">图片</option><option value="video">视频</option><option value="audio">音频</option></select><button :disabled="busy">筛选 / 刷新</button></form><div class="va-table-wrap"><table><thead><tr><th>上传会话</th><th>类型 / 大小</th><th>上传阶段</th><th>更新时间</th><th>操作</th></tr></thead><tbody><tr v-for="u in uploads" :key="u.id"><td><button class="va-id" @click="copyText(u.id)">{{ u.id }} ⧉</button></td><td>{{ ({image:'图片',video:'视频',audio:'音频'} as Record<string,string>)[u.media_type] }} · {{ (u.bytes/1000000).toFixed(2) }} MB</td><td><span class="va-badge" :data-state="u.status">{{ u.status==='ready'?'核验完成':text(u.status) }}</span><small v-if="u.error_code">{{ errors[u.error_code]??'上传未完成或校验失败，请重新上传。' }}</small></td><td>{{ new Date(u.updated_at).toLocaleString() }}</td><td><button v-if="u.status==='ready'" :disabled="busy || !caps?.local_upload || !!uploadBlockedReason || new Date(u.expires_at).getTime()<=Date.now()" @click="prepareRegistration(u)">注册到选中组</button></td></tr></tbody></table><p v-if="!uploads.length" class="va-help">暂无符合条件的上传会话。</p></div><button v-if="uploadCursor" :disabled="busy" @click="run(()=>loadUploads(true))">加载更多上传记录</button></details>
  <div class="va-workspace">
   <aside class="va-groups"><div class="va-row"><h2>素材组 <span>{{ groupTotal }}</span></h2><button class="va-primary" :disabled="busy || !(kind==='virtual'?caps?.virtual:caps?.portrait)" @click="openGroup">＋ 创建</button></div><form class="va-search" @submit.prevent="run(()=>loadGroups())"><input v-model="groupName" placeholder="搜索素材组名称" aria-label="搜索素材组名称"><button>搜索</button></form><select v-model="groupStatus" class="va-sync" aria-label="素材组状态" @change="run(()=>loadGroups())"><option value="">全部组状态</option><option value="ready">可用</option><option value="creating">创建中</option><option value="needs_review">待核验</option><option value="failed">失败</option><option value="deleting">删除中</option></select><button v-if="auth.isSuperadmin" class="va-sync" :disabled="busy || !caps?.sync" @click="openDiscover">官方发现与归属管理</button><button class="va-sync" :disabled="busy || !caps?.sync || syncActionBlocked" @click="startSync">{{ syncIsRunning(sync)||syncIsRunning(discoveryJob)?'同步任务进行中':'↻ 同步官方资源' }}</button><p class="va-help">从火山读取本人可见的素材组和素材记录。共用项目中的其他用户资源不会因此分配给本人。</p>
    <section class="va-sync-status" aria-label="官方同步状态"><p v-if="syncRestoring" role="status">正在恢复同步状态…</p><template v-if="sync"><p role="status">{{ syncSummary(sync,syncReadback) }}</p><p class="va-help">{{ sync.project || caps?.project }} · {{ sync.kind==='real_person'?'真人素材':'虚拟素材' }} · 已处理 {{ sync.items }} 条记录 / {{ sync.pages }} 页</p><p v-if="syncErrorText(sync)" class="va-help" :class="{'text-danger':sync.status==='failed'}">{{ syncErrorText(sync) }}</p><p v-if="sync.status==='pending' && sync.next_attempt_at" class="va-help">预计下次读取：{{ new Date(sync.next_attempt_at).toLocaleString() }}。系统会自动重试，无需再次发起。</p><p v-if="syncReadback.error" class="va-help" role="status">{{ syncReadback.error }}</p><button @click="syncDetailMode='owned'">同步详情</button></template><p v-else-if="!syncRestoring && !syncQueryError" class="va-help">当前类别暂无同步记录。</p><p v-if="syncQueryError" class="va-help text-danger" role="alert">{{ syncQueryError }}</p><button :disabled="busy || syncRestoring" @click="refreshSyncStatus">刷新同步状态</button></section>
    <div v-if="!groups.length" class="va-empty">暂无素材组</div><button v-for="g in groups" :key="g.id" class="va-group" :disabled="busy" :class="{selected:selected===g.id}" @click="selected=g.id"><div class="va-row"><strong>{{ g.name||'未命名素材组' }}</strong><span class="va-badge" :data-state="g.status">{{ groupStateText(g)||text(g.status) }}</span></div><p>{{ g.description||'暂无描述' }}</p><small>{{ g.asset_count }} 个素材 · {{ text(g.authorization_status) }}</small></button><button v-if="nextGroup" :disabled="busy" @click="run(()=>loadGroups(true))">加载更多素材组</button>
   </aside>
   <section class="va-assets"><template v-if="activeGroup">
    <div class="va-title">
     <div class="va-group-heading"><h2>{{ activeGroup.name||'未命名素材组' }} <span class="va-badge" :data-state="activeGroup.status">{{ groupStateText(activeGroup)||text(activeGroup.status) }}</span></h2><p v-if="activeGroup.description">{{ activeGroup.description }}</p><p v-if="resourceOperationNotice(activeGroup)" class="va-help" role="status">{{ resourceOperationNotice(activeGroup) }} 请查看组详情。</p><p v-if="kind==='real_person'" class="va-help">使用授权：{{ text(activeGroup.authorization_status) }}。真人认证与使用许可分别核验。</p></div>
     <div class="va-actions va-group-actions" aria-label="素材组操作">
      <button @click="showGroupDetails(activeGroup)">组详情</button><button v-if="auth.isSuperadmin && kind==='real_person'" :disabled="busy || !activeGroup.portrait_session_id" @click="openLicense">核验使用许可</button><button :disabled="busy || !caps?.update" @click="openRename(activeGroup,true)">编辑组</button>
      <button :disabled="busy || !!groupDeleteReason(activeGroup)" :title="groupDeleteReason(activeGroup)" :aria-describedby="groupDeleteReason(activeGroup)?'va-group-delete-hint':undefined" @click="openDelete(activeGroup,true)">{{ activeGroup.deletion_mode==='local_failed'?'清理失败记录':'删除空组' }}</button>
      <button class="va-primary" :disabled="busy || !!uploadBlockedReason" :title="uploadBlockedReason" :aria-describedby="uploadBlockedReason?'va-group-upload-hint':undefined" @click="openUpload">＋ 添加素材</button>
     </div>
     <div v-if="groupDeleteReason(activeGroup) || uploadBlockedReason || activeGroup.delete_operation" class="va-action-hints" aria-label="操作说明">
      <p v-if="groupDeleteReason(activeGroup)" id="va-group-delete-hint" role="status"><strong>删除空组</strong><span>{{ groupDeleteReason(activeGroup) }}</span></p>
      <p v-if="activeGroup.delete_operation"><strong>删除进度</strong><span>{{ text(activeGroup.delete_operation.status) }} · 操作 {{ activeGroup.delete_operation.operation_id }}</span></p>
      <div v-if="uploadBlockedReason" class="va-action-hint"><p id="va-group-upload-hint" role="status"><strong>添加素材</strong><span>{{ uploadBlockedReason }}</span></p><button v-if="auth.isSuperadmin" :disabled="busy || setupBusy" @click="setupOpen=true">查看配置缺项</button></div>
     </div>
    </div>
    <form class="va-filters" @submit.prevent="run(()=>loadAssets())"><input v-model="assetName" placeholder="搜索素材名称" aria-label="素材名称"><select v-model="mediaType" aria-label="素材类型"><option value="">全部类型</option><option value="image">图片</option><option value="video">视频</option><option value="audio">音频</option></select><select v-model="providerStatus" aria-label="官方素材状态"><option value="">全部官方状态</option><option value="active">已入库</option><option value="processing">处理中</option><option value="failed">失败</option><option value="unknown">官方状态待确认</option></select><button :disabled="busy">搜索</button><button type="button" :disabled="busy" @click="run(refresh)">刷新</button></form>
    <div class="va-count">筛选结果 {{ assetTotal }} 个素材 <span v-if="caps?.url_registration">· 支持 HTTPS 网址注册</span></div>
    <div class="va-table-wrap"><table><thead><tr><th>素材</th><th>素材 ID</th><th>官方状态 / 媒体事实</th><th>创建时间</th><th>操作</th></tr></thead><tbody><tr v-for="a in assets" :key="a.id"><td><button class="va-link" @click="showPreview(a)">{{ a.name||'未命名素材' }}</button><small>来源：{{ a.source==='imported'?'官方同步':'本平台登记' }}</small><small>{{ ({image:'图片',video:'视频',audio:'音频'} as Record<string,string>)[a.media_type] }} · {{ assetSizeText(a) }}<span v-if="a.media_type!=='image' && a.media_verification_status==='verified' && a.duration_ms>0"> · {{ assetDurationText(a) }}</span></small></td><td><button class="va-id" @click="copyText(a.reference??a.id)">{{ a.id.slice(0,19) }}… ⧉</button></td><td><span class="va-badge va-provider-badge" :data-state="assetStateTone(a)">{{ assetStateText(a) }}</span><small class="va-media-status">{{ assetMediaStateText(a) }}</small><small class="va-generation-status" :title="assetReviewExplanation(a)">{{ assetGenerationText(a) }}</small><small v-if="resourceOperationNotice(a)">{{ resourceOperationNotice(a) }}</small><small v-if="a.error_code" :title="a.error_code">{{ errors[a.error_code]??'官方处理失败，请查看日志详情。' }}</small></td><td>{{ new Date(a.created_at).toLocaleString() }}</td><td><div class="va-actions"><button @click="showPreview(a)">详情</button><button :disabled="busy || !caps?.update" @click="openRename(a,false)">编辑</button><button :disabled="busy || !!assetDeleteReason(a)" :title="assetDeleteReason(a)" @click="openDelete(a,false)">{{ a.deletion_mode==='local_failed'?'清理失败记录':'删除' }}</button></div></td></tr></tbody></table><div v-if="!assets.length" class="va-empty">暂无符合条件的素材</div></div><button v-if="nextAsset" :disabled="busy" @click="run(()=>loadAssets(true))">加载更多素材</button>
   </template><div v-else class="va-empty va-welcome">从左侧选择一个素材组查看素材</div></section>
  </div>
  <Teleport to="body"><div v-if="modal" class="va-overlay" @click.self="!busy && close()"><section class="va-dialog" role="dialog" aria-modal="true" aria-labelledby="va-dialog-title"><header><h2 id="va-dialog-title">{{ ({group:kind==='virtual'?'创建虚拟素材组':'创建真人素材组',upload:'网址注册素材',rename:'编辑名称',delete:localCleanup?'清理失败记录':'确认删除',portrait:'真人认证',discovery:'官方资源发现与归属',assign:'确认资源归属',license:'核验真人使用许可',register:'将已核验上传注册到素材组'} as Record<string,string>)[modal] }}</h2><button :disabled="busy" @click="close" aria-label="关闭弹框">×</button></header><div class="va-dialog-body">
   <div v-if="modal==='group' || modal==='rename'"><div v-if="modal==='group'&&kind==='real_person'" class="va-steps"><strong>1 创建认证任务</strong><span>2 完成真人认证</span><span>3 生成素材组</span></div><label>素材{{ modal==='group'||targetGroup?'组':'' }}名称<input v-model="name" maxlength="64" autofocus></label><label v-if="modal==='group'||targetGroup">描述（可选）<textarea v-model="description" maxlength="300" rows="3" /></label></div>
   <div v-if="modal==='upload'"><p v-if="uploadPhase" role="status" class="va-notice">{{ text(uploadPhase) }}<span v-if="uploadID"> · {{ uploadID }}</span></p><label>素材名称<input v-model="name" maxlength="64"></label><label>素材类型<select v-model="uploadType"><option v-for="m in caps?.media_types" :value="m">{{ ({image:'图片',video:'视频',audio:'音频'} as Record<string,string>)[m] }}</option></select></label><label>公开可访问的 HTTPS 网址<input v-model="sourceURL" type="url" autocomplete="off" placeholder="https://" :disabled="registrationUncertain"></label><p class="va-help">火山直接读取该网址并处理素材。平台不下载、不存储、不扫描文件；网址应在官方处理完成前持续有效。</p><label>本地文件<input type="file" disabled aria-describedby="va-local-file-unavailable"></label><p id="va-local-file-unavailable" class="va-help">暂不支持浏览器直接上传至火山。请先将文件放在可公开访问的 HTTPS 地址，再填写上方网址。</p><p v-if="registrationUncertain" class="va-alert" role="alert">本次注册是否受理尚未确认，请先刷新列表并核对请求日志；不会自动重复注册。</p><p class="va-help">图片小于 30 MB；视频不超过 200 MB；音频不超过 15 MB。视频、音频时长为 2–30 秒，生成时还需符合所选模型限制。</p></div>
   <div v-if="modal==='delete'"><template v-if="localCleanup"><p>确认清理“{{ target?.name }}”的失败记录？</p><p class="va-help">仅清理本平台中已确认创建失败、未生成官方{{ targetGroup?'素材组':'素材' }}的记录。不会调用官方删除，历史任务、请求日志和账务记录仍保留。</p></template><template v-else><p>将删除“{{ target?.name }}”。官方删除不可恢复。</p><p class="va-help">存在进行中的任务引用、同步不完整或上游内容变化时，服务端会拒绝删除。不会清除历史任务、日志或账务记录。</p></template><p v-if="deleteBlocked" class="va-alert" role="alert">{{ deleteBlocked }}</p></div>
   <div v-if="modal==='portrait'" class="va-portrait"><div class="va-steps"><span>1 创建认证任务</span><strong>2 完成真人认证</strong><span>3 生成素材组</span></div><p>{{ text(portrait?.status??'creating') }}</p><p v-if="portrait && portraitFailureText(portrait)" class="va-alert" role="alert">{{ portraitFailureText(portrait) }}</p><p v-if="validPortraitHTTPStatus(portrait?.provider_http_status)" class="va-help">火山 HTTP 状态：{{ portrait?.provider_http_status }}</p><p v-if="validPortraitDiagnosticID(portrait?.upstream_request_id)" class="va-help">火山请求 ID：<span class="va-id">{{ portrait?.upstream_request_id }}</span></p><p v-if="validPortraitDiagnosticID(portrait?.request_id)" class="va-help">平台请求 ID：<span class="va-id">{{ portrait?.request_id }}</span></p><template v-if="portrait?.authorization_url"><QrCode :value="portrait.authorization_url" :size="220" /><a :href="portrait.authorization_url" target="_blank" rel="noopener noreferrer" class="va-primary">打开官方认证页</a></template><p v-if="portrait?.expires_at" class="va-help">有效期至 {{ new Date(portrait.expires_at).toLocaleString() }}</p><p class="va-help">由真人本人完成官方认证。认证成功后会同步素材组，并单独核验使用许可。</p><button :disabled="busy" @click="run(checkPortrait)">刷新认证状态</button></div>
   <div v-if="modal==='discovery'"><p class="va-help">项目 {{ caps?.project }} · {{ caps?.scope_id }}。仅实名超级管理员可查看未归属资源。官方 default 项目不会自动分配给当前组织。</p><div class="va-actions"><button class="va-primary" :disabled="busy || !caps?.sync || discoveryActionBlocked" @click="discover">发现{{ kind==='virtual'?'虚拟':'真人' }}素材组</button><button :disabled="busy" @click="run(()=>loadDiscoveries())">刷新待归属列表</button></div><section v-if="discoveryJob" class="va-sync-status"><p role="status">{{ syncSummary(discoveryJob,discoveryReadback) }}</p><p class="va-help">已处理 {{ discoveryJob.items }} 个组 / {{ discoveryJob.pages }} 页</p><p v-if="syncErrorText(discoveryJob)" class="va-help" :class="{'text-danger':discoveryJob.status==='failed'}">{{ syncErrorText(discoveryJob) }}</p><p v-if="discoveryJob.status==='pending' && discoveryJob.next_attempt_at" class="va-help">预计下次读取：{{ new Date(discoveryJob.next_attempt_at).toLocaleString() }}。系统会自动重试，无需再次发起。</p><p v-if="discoveryReadback.error" class="va-help">{{ discoveryReadback.error }}</p><button @click="syncDetailMode='discover'">发现任务详情</button><button :disabled="busy" @click="run(()=>refreshSyncTask('discover',true))">刷新任务状态</button></section><p v-if="discoveryQueryError" class="va-help text-danger" role="alert">{{ discoveryQueryError }}</p><div class="va-table-wrap"><table><thead><tr><th>官方素材组</th><th>类别</th><th>操作</th></tr></thead><tbody><tr v-for="d in discoveries" :key="d.id"><td>{{ d.name }}<small>{{ d.description }}</small><small>{{ d.provider_id }}</small></td><td>{{ d.kind==='real_person' || d.kind==='real'?'真人':'虚拟' }}</td><td><button :disabled="busy" @click="openAssign(d)">分配归属</button></td></tr></tbody></table><p class="va-help">已显示 {{ discoveries.length }} / {{ discoveryTotal }} 个待归属组</p><button v-if="discoveryCursor" :disabled="busy" @click="run(()=>loadDiscoveries(true))">加载更多待归属组</button><p v-if="!discoveries.length" class="va-empty">暂无待归属素材组，可先发起官方发现。</p></div></div>
   <div v-if="modal==='assign'"><p>素材组：{{ assignment?.name }}</p><label>目标组织<select v-model="targetOrg"><option :value="0">请选择目标组织</option><option v-for="o in organizations" :key="o.id" :value="o.id">{{ o.name }}（{{ o.id }}）</option></select></label><label>归属核验依据<textarea v-model="evidence" rows="4" maxlength="3000" placeholder="记录归属依据、凭据编号或已审核合同；不要填写 API Key、身份证号或密码。" /></label><label class="va-check"><input v-model="assignmentConfirmed" type="checkbox">已核验归属，确认只将此组分配给选定组织。目标组织须已配置相同官方项目路由。</label><p class="va-help">真人素材组分配不代表真人认证完成或使用许可通过。</p></div>
   <div v-if="modal==='register'"><p>目标素材组：{{ activeGroup?.name }}</p><p class="va-help">上传会话：{{ selectedUpload?.id }}。只有已核验且未过期的媒体可注册；真人组仍需有效许可。</p><label>素材名称<input v-model="name" maxlength="64"></label></div><div v-if="modal==='license'"><p class="va-help">由实名超级管理员核对真人使用许可。官方认证成功或导入成功均不自动代表许可有效。</p><label>有效期截止（UTC）<input v-model="licenseUntil" type="datetime-local"></label><label>下次复核时间（UTC，最长 24 小时）<input v-model="reverifyAt" type="datetime-local"></label><label>许可核验依据<textarea v-model="evidence" rows="5" maxlength="3000" placeholder="记录经审核的许可范围和凭据编号，不要填写身份证号或密码。" /></label></div>
   <p class="va-help va-visibility">{{ auth.isSuperadmin && caps?.resource_visibility==='organization_admin'?'超级管理员：查看和管理当前组织资源。':'仅本人可见：共用项目配置不共享素材组或素材；同一账号的 Key 可访问本人资源。' }}</p>
  <div v-if="error" class="va-alert" role="alert">{{ error }}</div>
  </div><footer><button :disabled="busy" @click="close">{{ modal==='portrait'?'关闭':'取消' }}</button><button v-if="modal==='group'" class="va-primary" :disabled="busy || !name.trim()" @click="createGroup">{{ busy?'提交中…':kind==='real_person'?'生成认证二维码':'创建素材组' }}</button><button v-if="modal==='rename'" class="va-primary" :disabled="busy || !name.trim()" @click="rename">保存</button><button v-if="modal==='upload'" class="va-primary" :disabled="busy || !!uploadBlockedReason || !validSourceURL || registrationUncertain" @click="upload">{{ busy?'提交官方注册中…':'提交官方注册' }}</button><button v-if="modal==='assign'" class="va-primary" :disabled="busy || !targetOrg || !assignmentConfirmed || !evidence.trim()" @click="assign">确认分配</button><button v-if="modal==='register'" class="va-primary" :disabled="busy || !!uploadBlockedReason || !name.trim() || !selected" @click="registerUpload">确认注册</button><button v-if="modal==='license'" class="va-primary" :disabled="busy || !evidence.trim() || !licenseUntil || !reverifyAt" @click="reviewLicense">保存核验结果</button><button v-if="modal==='delete'" class="va-danger" :disabled="busy || !!deleteBlocked" @click="remove">{{ busy?'处理中…':localCleanup?'确认清理失败记录':'确认删除' }}</button></footer></section></div></Teleport>
  <DetailDrawer v-model:open="setupOpen" title="素材管理配置" size="lg" :close-disabled="setupBusy" description="登记管理凭据与明确授权能力，再单独绑定当前组织。">
   <AssetManagementSetup v-if="setupOpen && auth.isSuperadmin" :org-id="auth.orgId" @busy-change="setupBusy=$event" @bound="setupBound" />
  </DetailDrawer>
  <DetailDrawer :open="!!syncDetailMode" :title="syncDetailMode==='owned'?'官方同步详情':'官方发现详情'" size="lg" @update:open="open => { if (!open) syncDetailMode='' }">
   <template v-if="syncDetailJob"><p class="mb-4" role="status">{{ syncSummary(syncDetailJob,syncDetailReadback) }}</p><dl class="detail-grid mb-6"><div class="col-span-2"><dt>任务 ID</dt><dd><button class="va-id" @click="copyText(syncDetailJob.id)">{{ syncDetailJob.id }} ⧉</button></dd></div><div><dt>项目</dt><dd>{{ syncDetailJob.project || '未返回' }}</dd></div><div><dt>素材类别</dt><dd>{{ syncDetailJob.kind==='real_person'?'真人素材':syncDetailJob.kind==='virtual'?'虚拟素材':'未返回' }}</dd></div><div><dt>当前阶段</dt><dd>{{ syncPhaseText(syncDetailJob) }}</dd></div><div><dt>已处理</dt><dd>{{ syncDetailJob.items }} 条记录 / {{ syncDetailJob.pages }} 页</dd></div><div v-if="syncDetailJob.request_id" class="col-span-2"><dt>请求 ID</dt><dd>{{ syncDetailJob.request_id }}</dd></div><div v-if="syncDetailJob.upstream_request_id" class="col-span-2"><dt>官方请求 ID</dt><dd>{{ syncDetailJob.upstream_request_id }}</dd></div><div v-if="syncDetailJob.http_status"><dt>官方响应状态</dt><dd>{{ syncDetailJob.http_status }}</dd></div><div v-if="syncDetailJob.attempts!==undefined"><dt>读取尝试次数</dt><dd>{{ syncDetailJob.attempts }}</dd></div><div v-if="syncDetailJob.retry_count!==undefined"><dt>当前重试次数</dt><dd>{{ syncDetailJob.retry_count }}</dd></div><div v-if="syncDetailJob.next_attempt_at" class="col-span-2"><dt>下一次读取</dt><dd>{{ new Date(syncDetailJob.next_attempt_at).toLocaleString() }}</dd></div></dl><p v-if="syncErrorText(syncDetailJob)" :class="{'text-danger':syncDetailJob.status==='failed'}">{{ syncErrorText(syncDetailJob) }}</p><p v-if="syncDetailError" class="text-danger">{{ syncDetailError }}</p><p v-if="syncDetailReadback.error" class="va-help">{{ syncDetailReadback.error }}</p><p class="va-help">同步只导入资源记录；部分记录已获取时会保留。历史未确认的创建、删除以及缺少的媒体事实各自保留状态，不会因为同步而自动确认。</p><button :disabled="busy" @click="run(()=>refreshSyncTask(syncDetailMode as SyncMode,true))">刷新任务状态</button></template>
  </DetailDrawer>
  <DetailDrawer :open="!!detailGroup" :title="shownGroup?.name || '素材组详情'" size="lg" @update:open="open => { if (!open) detailGroup=undefined }">
   <template v-if="shownGroup">
    <dl class="detail-grid mb-6"><div><dt>素材组 ID</dt><dd>{{ shownGroup.id }}</dd></div><div><dt>状态</dt><dd>{{ groupStateText(shownGroup)||text(shownGroup.status) }}</dd></div><div><dt>类型</dt><dd>{{ shownGroup.kind==='virtual'?'虚拟素材':'真人素材' }}</dd></div><div><dt>素材数量</dt><dd>{{ shownGroup.asset_count }}</dd></div><div class="col-span-2"><dt>描述</dt><dd>{{ shownGroup.description || '暂无描述' }}</dd></div></dl>
    <p v-if="groupDeleteReason(shownGroup)" class="va-help">{{ groupDeleteReason(shownGroup) }}</p>
    <div class="va-operation-list"><section v-for="op in resourceOperations(shownGroup)" :key="op.operation_id" class="va-operation"><h3>{{ operationNames[op.kind] || '资源操作' }} · {{ text(op.status) }}</h3><dl class="detail-grid"><div><dt>操作 ID</dt><dd>{{ op.operation_id }}</dd></div><div v-if="op.request_id"><dt>请求 ID</dt><dd><button class="va-id" @click="copyText(op.request_id)">{{ op.request_id }} ⧉</button></dd></div><div v-if="operationError(op)" class="col-span-2"><dt>处理结果</dt><dd>{{ operationError(op) }}</dd></div><div v-if="operationReadErrors[op.operation_id]" class="col-span-2"><dt>状态查询</dt><dd>{{ operationReadErrors[op.operation_id] }}</dd></div></dl><p v-if="op.status==='unknown'" class="va-help">官方是否完成尚未确认，暂时不能重复提交或清理此记录。查询状态不会重新发送操作。</p></section></div>
    <p v-if="!resourceOperations(shownGroup).length" class="va-help">此记录暂无创建或删除操作回执。</p><button :disabled="busy" @click="run(refresh)">刷新状态</button>
   </template>
  </DetailDrawer>
  <DetailDrawer :open="!!previewAsset" :title="shownAsset?.name || '素材详情'" size="lg" @update:open="open => { if (!open) closePreview() }">
   <template v-if="shownAsset">
    <dl class="detail-grid mb-6">
     <div><dt>素材 ID</dt><dd>{{ shownAsset.id }}</dd></div><div><dt>素材组 ID</dt><dd>{{ shownAsset.group_id }}</dd></div>
     <div><dt>类型</dt><dd>{{ ({image:'图片',video:'视频',audio:'音频'} as Record<string,string>)[shownAsset.media_type] || shownAsset.media_type }}</dd></div>
     <div><dt>当前状态</dt><dd>{{ assetStateText(shownAsset) }}</dd></div><div><dt>官方素材状态</dt><dd>{{ ({active:'已入库',processing:'处理中',failed:'失败',unknown:'待确认'})[assetProviderState(shownAsset)] }}</dd></div><div><dt>媒体事实</dt><dd>{{ assetMediaStateText(shownAsset) }}</dd></div><div><dt>生成引用条件</dt><dd>{{ assetGenerationText(shownAsset) }}</dd></div><div><dt>来源</dt><dd>{{ shownAsset.source==='imported'?'官方同步':'本平台登记' }}</dd></div>
     <div><dt>大小</dt><dd>{{ assetSizeText(shownAsset) }}</dd></div><div v-if="shownAsset.media_type!=='image'"><dt>时长</dt><dd>{{ assetDurationText(shownAsset) }}</dd></div><div><dt>创建时间</dt><dd>{{ new Date(shownAsset.created_at).toLocaleString() }}</dd></div>
     <div v-if="shownAsset.reference" class="col-span-2"><dt>引用标识</dt><dd>{{ shownAsset.reference }}</dd></div>
     <div v-if="shownAsset.error_code" class="col-span-2"><dt>失败原因</dt><dd>{{ errors[shownAsset.error_code] || '官方处理失败，请查看请求日志。' }} · {{ shownAsset.error_code }}</dd></div>
    </dl>
    <div class="va-operation-list"><section v-for="op in resourceOperations(shownAsset)" :key="op.operation_id" class="va-operation"><h3>{{ operationNames[op.kind] || '资源操作' }} · {{ text(op.status) }}</h3><dl class="detail-grid"><div><dt>操作 ID</dt><dd>{{ op.operation_id }}</dd></div><div v-if="op.request_id"><dt>请求 ID</dt><dd><button class="va-id" @click="copyText(op.request_id)">{{ op.request_id }} ⧉</button></dd></div><div v-if="operationError(op)" class="col-span-2"><dt>处理结果</dt><dd>{{ operationError(op) }}</dd></div><div v-if="operationReadErrors[op.operation_id]" class="col-span-2"><dt>状态查询</dt><dd>{{ operationReadErrors[op.operation_id] }}</dd></div></dl><p v-if="op.status==='unknown'" class="va-help">官方是否完成尚未确认，暂时不能重复提交或清理此记录。</p></section></div>
    <p v-if="assetReviewExplanation(shownAsset)" class="va-help">{{ assetReviewExplanation(shownAsset) }}</p>
    <p v-if="assetDeleteReason(shownAsset)" class="va-help">{{ assetDeleteReason(shownAsset) }}</p>
    <p v-if="previewLoading" role="status">正在读取预览…</p><p v-if="previewError" role="alert" class="text-danger">{{ previewError }}</p>
    <button v-if="shownAsset.source_available" :disabled="previewLoading" @click="showPreview(shownAsset)">重新加载官方预览</button><p v-if="shownAsset.source_available" class="va-help">浏览器直接读取官方媒体地址，不向媒体地址发送平台 Key。链接有效期由火山决定。</p>
    <div v-if="previewURL" class="va-preview"><img v-if="previewType==='image'" :src="previewURL" :alt="shownAsset.name" referrerpolicy="no-referrer" @error="previewError='媒体地址不可访问或已过期，请重新加载官方预览。'"><video v-else-if="previewType==='video'" :src="previewURL" controls playsinline @error="previewError='媒体地址不可访问或已过期，请重新加载官方预览。'" /><audio v-else :src="previewURL" controls @error="previewError='媒体地址不可访问或已过期，请重新加载官方预览。'" /></div>
    <p v-else-if="!previewLoading && !previewError" class="va-help">当前素材暂无可用预览。</p>
   </template>
  </DetailDrawer>
 </main>
</template>

<style scoped>
.va-sync-status{border:1px solid rgb(var(--c-line));border-radius:8px;padding:12px;margin:12px 0;font-size:12px;overflow-wrap:anywhere}.va-sync-status button{margin:4px 6px 0 0}.va-operation-list{display:grid;gap:12px;margin-bottom:20px}.va-operation{border:1px solid rgb(var(--c-line));border-radius:8px;padding:14px;overflow-wrap:anywhere}.va-operation h3{font-size:14px;font-weight:600;margin:0 0 14px}

.va-page,.va-overlay{--text:rgb(var(--c-ink));--surface:rgb(var(--c-panel));--border:rgb(var(--c-line));--muted:rgb(var(--c-muted))}.va-page{color:var(--text,#17212f);padding:8px 0}.va-heading{display:flex;justify-content:space-between;align-items:center;margin-bottom:24px;gap:20px}.va-heading h1{font-size:28px;font-weight:700;margin:4px 0 8px}.va-heading p,.va-title p{color:var(--muted,#64748b);font-size:13px}.va-eyebrow{font-size:10px!important;letter-spacing:.14em}.va-project{background:var(--surface,#f6f8fa);border:1px solid var(--border,#e4e7ec);border-radius:8px;padding:12px 18px;min-width:170px;font-size:13px}.va-project strong{margin-left:24px}.va-tabs{display:flex;border-bottom:1px solid var(--border,#e4e7ec);gap:24px}.va-tabs button{border:0;border-radius:0;padding:12px 2px;background:none;border-bottom:2px solid transparent}.va-tabs button.active{color:#2563eb;border-color:#2563eb;font-weight:600}.va-upload-records{border:1px solid var(--border,#e4e7ec);border-radius:8px;padding:14px;margin:16px 0}.va-upload-records summary{cursor:pointer;font-size:13px;font-weight:600}.va-upload-records summary span{font-weight:400;font-size:12px;color:var(--muted);margin-left:12px}.va-workspace{display:grid;grid-template-columns:286px minmax(0,1fr);min-height:520px}.va-groups{border-right:1px solid var(--border,#e4e7ec);padding:24px 20px 24px 0}.va-assets{padding:24px 0 24px 24px;min-width:0}.va-row{display:flex;justify-content:space-between;align-items:center;gap:12px}.va-row h2{font-size:16px;font-weight:600;margin:0}.va-row h2>span:not(.va-badge){font-size:12px;color:#64748b}.va-search{display:flex;gap:6px;margin:16px 0 10px}.va-search input{min-width:0;flex:1}.va-group{display:block;text-align:left;width:100%;padding:14px;margin:8px 0;border-radius:8px}.va-group.selected{border-color:#3b82f6;background:color-mix(in srgb,#3b82f6 8%,transparent)}.va-group strong{font-size:13px;overflow-wrap:anywhere}.va-group p{font-size:12px;color:#64748b;margin:8px 0}.va-group small{font-size:11px;color:#64748b}.va-sync{width:100%;margin-bottom:8px}.va-title{align-items:flex-start;margin-bottom:24px}.va-actions{display:flex;align-items:center;gap:6px;flex-wrap:wrap}.va-filters{display:flex;gap:8px;flex-wrap:wrap}.va-filters input{min-width:180px;flex:1}.va-count{font-size:12px;color:#64748b;margin:18px 0}.va-table-wrap{overflow:auto}table{width:100%;border-collapse:collapse;font-size:12px}th{text-align:left;font-weight:500;color:#64748b;padding:12px;border-bottom:1px solid var(--border,#e4e7ec);white-space:nowrap}td{padding:16px 12px;border-bottom:1px solid var(--border,#e4e7ec);vertical-align:middle}td small{display:block;color:#64748b;margin-top:5px}td:first-child{min-width:170px}.va-badge{display:inline-block;font-size:11px!important;border-radius:4px;padding:3px 6px;background:#edf2f7;color:#475569;white-space:nowrap}.va-badge[data-state=ready]{background:#dcfce7;color:#166534}.va-badge[data-state=failed]{background:#fee2e2;color:#991b1b}.va-badge[data-state=needs_review],.va-badge[data-state=registering]{background:#fef3c7;color:#92400e}.va-link,.va-id{border:0!important;background:transparent!important;color:#2563eb;text-align:left;padding:0!important;word-break:break-all}.va-id{font-family:monospace;font-size:11px}.va-empty{text-align:center;padding:64px 12px;color:#94a3b8;font-size:13px}.va-welcome{padding-top:180px}.va-setup{padding:20px;text-align:left;background:#fffbeb;color:#92400e;border-radius:8px}.va-alert,.va-notice{padding:12px 14px;border-radius:6px;margin:10px 0;font-size:13px}.va-alert{background:#fef2f2;color:#991b1b;display:flex;justify-content:space-between}.va-notice{background:#eff6ff;color:#1d4ed8}.va-help{font-size:12px;color:#64748b;line-height:1.6}.va-page button,.va-dialog button{border:1px solid var(--border,#dbe1e8);background:var(--surface,#fff);color:inherit;border-radius:6px;padding:7px 11px;font-size:12px;cursor:pointer}.va-page button:disabled,.va-dialog button:disabled{opacity:.45;cursor:not-allowed}.va-primary{background:#2563eb!important;color:#fff!important;border-color:#2563eb!important;text-decoration:none;padding:9px 14px;border-radius:6px;font-size:13px}.va-danger{background:#dc2626!important;color:white!important;border-color:#dc2626!important}.va-page input,.va-page select,.va-dialog input,.va-dialog textarea,.va-dialog select{background:var(--surface,#fff);border:1px solid var(--border,#dbe1e8);border-radius:6px;padding:9px 10px;font-size:12px;color:inherit}.va-overlay{position:fixed;inset:0;z-index:1000;display:flex;align-items:center;justify-content:center;padding:24px;background:#0f172a80}.va-dialog{width:min(760px,100%);max-height:calc(100dvh - 48px);display:flex;flex-direction:column;background:var(--surface,#fff);color:var(--text,#17212f);border-radius:12px;box-shadow:0 16px 64px #0003}.va-dialog>header,.va-dialog>footer{padding:18px 24px;display:flex;align-items:center;justify-content:space-between;flex-shrink:0}.va-dialog>header{border-bottom:1px solid var(--border,#e4e7ec)}.va-dialog>header h2{font-size:17px;font-weight:600;margin:0}.va-dialog>footer{justify-content:flex-end;gap:10px;border-top:1px solid var(--border,#e4e7ec)}.va-dialog-body{padding:24px;overflow:auto}.va-dialog label{display:flex;flex-direction:column;gap:8px;margin-bottom:18px;font-size:13px}.va-check{flex-direction:row!important;align-items:flex-start;line-height:1.6}.va-check input{margin-top:4px}.va-steps{display:flex;justify-content:space-between;gap:10px;margin:0 0 24px;font-size:12px;color:#64748b}.va-steps strong{color:#2563eb}.va-portrait{text-align:center}.va-portrait a{display:block;width:fit-content;margin:20px auto}.va-preview img,.va-preview video{max-width:100%;max-height:65dvh;display:block;margin:auto}.va-preview audio{width:100%}@media(max-width:900px){.va-workspace{grid-template-columns:230px minmax(0,1fr)}.va-title{flex-direction:column}.va-heading{align-items:flex-start}}@media(max-width:640px){.va-heading{flex-direction:column}.va-workspace{display:block}.va-groups{border-right:0;border-bottom:1px solid var(--border,#e4e7ec);padding-right:0;max-height:320px;overflow:auto}.va-assets{padding:20px 0}.va-overlay{padding:12px}.va-dialog{max-height:calc(100dvh - 24px)}.va-dialog-body{padding:18px}.va-dialog>header,.va-dialog>footer{padding:16px 18px}.va-steps{font-size:11px}.va-welcome{padding-top:60px}}

.va-title{display:grid;gap:12px;min-width:0;margin-bottom:18px}.va-group-heading{min-width:0;overflow-wrap:anywhere}.va-group-heading h2{font-size:16px;font-weight:600;line-height:1.6;margin:0}.va-group-heading p{margin:5px 0 0;line-height:1.6}.va-group-actions{align-items:center;justify-content:flex-start;gap:8px}.va-group-actions>button{display:inline-flex;align-items:center;justify-content:center;flex:0 0 auto;align-self:center;height:36px;box-sizing:border-box;white-space:nowrap;line-height:1.2;padding:0 12px}.va-action-hints{display:grid;gap:6px;border:1px solid var(--border);background:rgb(var(--c-elev)/.5);border-radius:8px;padding:10px 12px;font-size:12px;color:var(--muted);line-height:1.6;min-width:0}.va-action-hints p{display:flex;align-items:baseline;gap:10px;min-width:0;margin:0;font-size:12px}.va-action-hints strong{flex:0 0 auto;font-weight:500;color:var(--text)}.va-action-hints span{min-width:0;overflow-wrap:anywhere}.va-action-hint{display:flex;align-items:flex-start;flex-wrap:wrap;gap:8px 12px;min-width:0}.va-action-hint p{flex:1 1 300px}.va-action-hint button{flex:0 0 auto;align-self:flex-start;white-space:nowrap;min-height:32px}.va-media-status,.va-generation-status{color:var(--muted)}.va-provider-badge{background:rgb(var(--c-elev));color:var(--muted)}.va-provider-badge[data-state=active]{background:rgb(var(--c-ok)/.12);color:rgb(var(--c-ok))}.va-provider-badge[data-state=processing]{background:rgb(var(--c-accent)/.12);color:rgb(var(--c-accent))}.va-provider-badge[data-state=failed]{background:rgb(var(--c-danger)/.12);color:rgb(var(--c-danger))}.va-provider-badge[data-state=unknown]{background:rgb(var(--c-elev));color:var(--muted)}
@media(max-width:640px){.va-group-actions{gap:6px}.va-group-actions>button{height:36px;padding:0 10px}.va-action-hints{padding:9px 10px}.va-action-hints p{gap:8px}.va-action-hint p{flex-basis:100%}}
</style>
