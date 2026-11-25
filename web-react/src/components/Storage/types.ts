import type React from 'react';

type StorageProviderKey = 'tos' | 'cos' | string;

interface RegionItem {
  label: string;
  /** 写入配置并参与签名的官方地域代码 */
  region: string;
  /** 下拉唯一值。国内与国际地域代码相同，国际版另给 id，不写入配置 */
  id?: string;
  endpointExternal: string;
  endpointInternal: string;
}

export interface RegionGroup {
  group: string;
  regions: RegionItem[];
}

export interface StorageProviderMeta {
  key: StorageProviderKey;
  name: string;
  shortName: string;
  brand: string;
  tag?: string;
  description: string;
  icon?: React.ReactNode;
  brandColor?: string;
  docUrl?: string;
  fieldKeys: {
    keyId: string;
    keyIdLabel: string;
    keyIdPlaceholder: string;
    keySecret: string;
    keySecretLabel: string;
    keySecretPlaceholder: string;
    region: string;
    endpoint: string;
    bucket: string;
    bucketExtra?: string;
    pathPrefix: string;
    customDomain: string;
  };
  regionGroups: RegionGroup[];
  allRegions: RegionItem[];
  detectNetworkType: (endpoint?: string) => 'external' | 'internal';
  resolveEndpoint: (region: string, networkType: 'external' | 'internal', endpointHint?: string) => string;
  isConfigured: (values: Record<string, any>) => boolean;
}

export interface TestConnectionResult {
  success: boolean;
  message: string;
}

export interface ListedStorageBucket {
  name: string;
  location: string;
  extranet_endpoint: string;
  intranet_endpoint: string;
}

