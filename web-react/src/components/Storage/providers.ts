import type { RegionGroup, StorageProviderMeta } from './types';

const TOS_REGION_GROUPS: RegionGroup[] = [
  {
    group: '🇨🇳 国内版 - 火山引擎',
    regions: [
      { label: '华北2（北京）', region: 'cn-beijing', endpointExternal: 'https://tos-cn-beijing.volces.com', endpointInternal: 'https://tos-cn-beijing.ivolces.com' },
      { label: '华南1（广州）', region: 'cn-guangzhou', endpointExternal: 'https://tos-cn-guangzhou.volces.com', endpointInternal: 'https://tos-cn-guangzhou.ivolces.com' },
      { label: '华东2（上海）', region: 'cn-shanghai', endpointExternal: 'https://tos-cn-shanghai.volces.com', endpointInternal: 'https://tos-cn-shanghai.ivolces.com' },
      { label: '中国香港', region: 'cn-hongkong', endpointExternal: 'https://tos-cn-hongkong.volces.com', endpointInternal: 'https://tos-cn-hongkong.ivolces.com' },
      { label: '亚太东南（柔佛）', region: 'ap-southeast-1', endpointExternal: 'https://tos-ap-southeast-1.volces.com', endpointInternal: 'https://tos-ap-southeast-1.ivolces.com' },
      { label: '亚太东南（雅加达）', region: 'ap-southeast-3', endpointExternal: 'https://tos-ap-southeast-3.volces.com', endpointInternal: 'https://tos-ap-southeast-3.ivolces.com' },
    ]
  },
  {
    group: '🌏 海外版 - BytePlus',
    regions: [
      { id: 'byteplus:ap-southeast-1', label: 'Asia Pacific (Johor)', region: 'ap-southeast-1', endpointExternal: 'https://tos-ap-southeast-1.bytepluses.com', endpointInternal: 'https://tos-ap-southeast-1.ibytepluses.com' },
      { id: 'byteplus:cn-hongkong', label: 'China (Hong Kong)', region: 'cn-hongkong', endpointExternal: 'https://tos-cn-hongkong.bytepluses.com', endpointInternal: 'https://tos-cn-hongkong.ibytepluses.com' },
      { id: 'byteplus:ap-southeast-3', label: 'Asia Pacific (Jakarta)', region: 'ap-southeast-3', endpointExternal: 'https://tos-ap-southeast-3.bytepluses.com', endpointInternal: 'https://tos-ap-southeast-3.ibytepluses.com' },
      { id: 'byteplus:cn-beijing', label: 'China (Beijing)', region: 'cn-beijing', endpointExternal: 'https://tos-cn-beijing.bytepluses.com.cn', endpointInternal: 'https://tos-cn-beijing.ibytepluses.com.cn' },
      { id: 'byteplus:cn-guangzhou', label: 'China (Guangzhou)', region: 'cn-guangzhou', endpointExternal: 'https://tos-cn-guangzhou.bytepluses.com.cn', endpointInternal: 'https://tos-cn-guangzhou.ibytepluses.com.cn' },
      { id: 'byteplus:cn-shanghai', label: 'China (Shanghai)', region: 'cn-shanghai', endpointExternal: 'https://tos-cn-shanghai.bytepluses.com.cn', endpointInternal: 'https://tos-cn-shanghai.ibytepluses.com.cn' },
    ]
  }
];

const ALL_TOS_REGIONS = TOS_REGION_GROUPS.flatMap(g => g.regions);

/** 已保存的地域 + 请求地址 → 下拉唯一值。旧配置的 bp- 前缀视为国际版 */
export function tosOptionIdFromSaved(region?: string, endpoint?: string): string | undefined {
  if (!region) return undefined;
  const official = region.replace(/^bp-/, '');
  if ((endpoint || '').includes('bytepluses.com') || region.startsWith('bp-')) {
    return `byteplus:${official}`;
  }
  return official;
}

function matchTosRegion(regionOrId: string, endpointHint?: string) {
  const byId = ALL_TOS_REGIONS.find(r => r.id === regionOrId);
  if (byId) return byId;
  const official = regionOrId.replace(/^bp-/, '');
  const matches = ALL_TOS_REGIONS.filter(r => r.region === official);
  if (matches.length <= 1) return matches[0];
  const international = (endpointHint || '').includes('bytepluses.com') || regionOrId.startsWith('bp-');
  return matches.find(r => (international
    ? r.endpointExternal.includes('bytepluses.com')
    : r.endpointExternal.includes('volces.com'))) || matches[0];
}

const COS_REGION_GROUPS: RegionGroup[] = [
  {
    group: '🇨🇳 中国大陆',
    regions: [
      { label: '北京', region: 'ap-beijing', endpointExternal: 'https://cos.ap-beijing.myqcloud.com', endpointInternal: 'https://cos-internal.ap-beijing.tencentcos.cn' },
      { label: '南京', region: 'ap-nanjing', endpointExternal: 'https://cos.ap-nanjing.myqcloud.com', endpointInternal: 'https://cos-internal.ap-nanjing.tencentcos.cn' },
      { label: '上海', region: 'ap-shanghai', endpointExternal: 'https://cos.ap-shanghai.myqcloud.com', endpointInternal: 'https://cos-internal.ap-shanghai.tencentcos.cn' },
      { label: '广州', region: 'ap-guangzhou', endpointExternal: 'https://cos.ap-guangzhou.myqcloud.com', endpointInternal: 'https://cos-internal.ap-guangzhou.tencentcos.cn' },
      { label: '成都', region: 'ap-chengdu', endpointExternal: 'https://cos.ap-chengdu.myqcloud.com', endpointInternal: 'https://cos-internal.ap-chengdu.tencentcos.cn' },
      { label: '重庆', region: 'ap-chongqing', endpointExternal: 'https://cos.ap-chongqing.myqcloud.com', endpointInternal: 'https://cos-internal.ap-chongqing.tencentcos.cn' },
      { label: '香港', region: 'ap-hongkong', endpointExternal: 'https://cos.ap-hongkong.myqcloud.com', endpointInternal: 'https://cos-internal.ap-hongkong.tencentcos.cn' },
    ],
  },
  {
    group: '🌏 境外',
    regions: [
      { label: '新加坡', region: 'ap-singapore', endpointExternal: 'https://cos.ap-singapore.myqcloud.com', endpointInternal: 'https://cos-internal.ap-singapore.tencentcos.cn' },
      { label: '东京', region: 'ap-tokyo', endpointExternal: 'https://cos.ap-tokyo.myqcloud.com', endpointInternal: 'https://cos-internal.ap-tokyo.tencentcos.cn' },
      { label: '首尔', region: 'ap-seoul', endpointExternal: 'https://cos.ap-seoul.myqcloud.com', endpointInternal: 'https://cos-internal.ap-seoul.tencentcos.cn' },
      { label: '曼谷', region: 'ap-bangkok', endpointExternal: 'https://cos.ap-bangkok.myqcloud.com', endpointInternal: 'https://cos-internal.ap-bangkok.tencentcos.cn' },
      { label: '雅加达', region: 'ap-jakarta', endpointExternal: 'https://cos.ap-jakarta.myqcloud.com', endpointInternal: 'https://cos-internal.ap-jakarta.tencentcos.cn' },
      { label: '硅谷', region: 'na-siliconvalley', endpointExternal: 'https://cos.na-siliconvalley.myqcloud.com', endpointInternal: 'https://cos-internal.na-siliconvalley.tencentcos.cn' },
      { label: '弗吉尼亚', region: 'na-ashburn', endpointExternal: 'https://cos.na-ashburn.myqcloud.com', endpointInternal: 'https://cos-internal.na-ashburn.tencentcos.cn' },
      { label: '法兰克福', region: 'eu-frankfurt', endpointExternal: 'https://cos.eu-frankfurt.myqcloud.com', endpointInternal: 'https://cos-internal.eu-frankfurt.tencentcos.cn' },
    ],
  },
];

const ALL_COS_REGIONS = COS_REGION_GROUPS.flatMap(g => g.regions);

export const STORAGE_PROVIDERS: StorageProviderMeta[] = [
  {
    key: 'tos',
    name: '火山引擎 TOS',
    shortName: 'TOS',
    brand: '火山引擎 / BytePlus',
    tag: '推荐',
    brandColor: '#1664ff',
    description: '字节跳动旗下高可用对象存储服务，兼容 S3 协议，支持大文件分片直传与全球加速',
    docUrl: 'https://console.volcengine.com/tos',
    fieldKeys: {
      keyId: 'tos_access_key',
      keyIdLabel: 'Access Key',
      keyIdPlaceholder: '火山引擎 Access Key (AK)',
      keySecret: 'tos_secret_key',
      keySecretLabel: 'Secret Key',
      keySecretPlaceholder: '火山引擎 Secret Key (SK)',
      region: 'tos_region',
      endpoint: 'tos_endpoint',
      bucket: 'tos_bucket',
      bucketExtra: '对象存储桶名称',
      pathPrefix: 'tos_path_prefix',
      customDomain: 'tos_custom_domain',
    },
    regionGroups: TOS_REGION_GROUPS,
    allRegions: ALL_TOS_REGIONS,
    detectNetworkType: (ep?: string) => {
      const endpoint = ep || '';
      return endpoint.includes('ivolces.com') || endpoint.includes('ibytepluses.com') ? 'internal' : 'external';
    },
    resolveEndpoint: (region: string, networkType: 'external' | 'internal', endpointHint?: string) => {
      const found = matchTosRegion(region, endpointHint);
      if (!found) return '';
      return networkType === 'internal' ? found.endpointInternal : found.endpointExternal;
    },
    isConfigured: (values: Record<string, any>) => {
      return !!(values?.tos_access_key && (values?.tos_bucket || values?.tos_endpoint));
    },
  },
  {
    key: 'cos',
    name: '腾讯云 COS',
    shortName: 'COS',
    brand: '腾讯云 Tencent Cloud',
    brandColor: '#0052d9',
    description: '腾讯云海量、安全、低成本、高可靠的云端存储服务，支持内网专线互通',
    docUrl: 'https://console.cloud.tencent.com/cos',
    fieldKeys: {
      keyId: 'cos_secret_id',
      keyIdLabel: 'SecretId',
      keyIdPlaceholder: '腾讯云 API 密钥 SecretId',
      keySecret: 'cos_secret_key',
      keySecretLabel: 'SecretKey',
      keySecretPlaceholder: '腾讯云 API 密钥 SecretKey',
      region: 'cos_region',
      endpoint: 'cos_endpoint',
      bucket: 'cos_bucket',
      bucketExtra: '需包含 APPID，例如：example-1250000000',
      pathPrefix: 'cos_path_prefix',
      customDomain: 'cos_custom_domain',
    },
    regionGroups: COS_REGION_GROUPS,
    allRegions: ALL_COS_REGIONS,
    detectNetworkType: (ep?: string) => {
      const endpoint = ep || '';
      return endpoint.includes('tencentcos.cn') ? 'internal' : 'external';
    },
    resolveEndpoint: (region: string, networkType: 'external' | 'internal') => {
      const found = ALL_COS_REGIONS.find(r => r.region === region);
      if (found) {
        return networkType === 'internal' ? found.endpointInternal : found.endpointExternal;
      }
      if (!region) return '';
      return networkType === 'internal'
        ? `https://cos-internal.${region}.tencentcos.cn`
        : `https://cos.${region}.myqcloud.com`;
    },
    isConfigured: (values: Record<string, any>) => {
      return !!(values?.cos_secret_id && (values?.cos_bucket || values?.cos_endpoint));
    },
  },
];

export function getStorageProvider(key: string): StorageProviderMeta {
  return STORAGE_PROVIDERS.find(p => p.key === key) || STORAGE_PROVIDERS[0];
}
