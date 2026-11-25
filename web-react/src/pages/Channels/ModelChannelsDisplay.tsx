import React, { useEffect, useState, useMemo, useCallback } from 'react';
import { Typography, List, Spin, Button, Space, Tooltip, Input, Dropdown, Popconfirm, message, Segmented, Select, Empty } from 'antd';
import { useNavigate } from 'react-router-dom';
import request from '../../utils/request';
import type { Channel, ModelModel } from '../../types';
import { modelMatchesKeyword } from '../../utils/modelKeywordMatch';
import { channelEditPath } from './channelPaths';
import { copyToClipboard } from '../../utils/clipboard';
import SmartSvgIcon from '../../components/SmartSvgIcon';
import { listPagination } from '../../components/ListPagination';
import { 
  Image as ImageIcon, 
  Video, 
  AudioLines, 
  MessageSquare, 
  Cuboid, 
  ListOrdered, 
  Sparkles, 
  LayoutGrid,
  ShieldCheck,
  AlertTriangle,
  Layers,
  ArrowLeft,
  RefreshCw,
  Search,
  Copy,
  Check,
  CheckCircle2,
  Plus,
  X
} from 'lucide-react';

const { Title, Text } = Typography;

interface ModelChannelGroup {
  model: string;
  modelName: string;
  modelId: string;
  modelIdAlias?: string;
  remark?: string;
  channels: Channel[];
  typeId?: number;
  typeName?: string;
  typeLogo?: string;
}

type SearchMode = 'fuzzy' | 'exact';
type SortOption = 'name_asc' | 'channels_desc' | 'channels_asc';

const styles = `
/* ==========================================================================
   Model Channels Matrix - Modern Fixed-Width Card System
   ========================================================================== */

.matrix-container {
  min-height: 100%;
}

/* Card List with Responsive Grid: Max 6 per row (stepped 6 -> 4 -> 2) */
.model-channels-grid-list .ant-list-items {
  display: grid !important;
  grid-template-columns: repeat(6, minmax(0, 1fr)) !important;
  gap: 14px !important;
  align-items: stretch !important;
}

/* Screen width <= 1920px: At most 4 columns */
@media (max-width: 1920px) {
  .model-channels-grid-list .ant-list-items {
    grid-template-columns: repeat(4, minmax(0, 1fr)) !important;
    gap: 14px !important;
  }
}

/* Screen width <= 900px: 2 columns */
@media (max-width: 900px) {
  .model-channels-grid-list .ant-list-items {
    grid-template-columns: repeat(2, minmax(0, 1fr)) !important;
    gap: 12px !important;
  }
}

/* Screen width <= 480px: 1 column */
@media (max-width: 480px) {
  .model-channels-grid-list .ant-list-items {
    grid-template-columns: repeat(1, minmax(0, 1fr)) !important;
    gap: 10px !important;
  }
}

.model-channels-grid-list .ant-list-item {
  margin: 0 !important;
  padding: 0 !important;
  width: 100% !important;
  min-width: 0 !important;
  max-width: 100% !important;
  flex: none !important;
  border-block-end: none !important;
  display: flex !important;
  box-sizing: border-box !important;
}

/* Modern Model Card */
.matrix-card {
  position: relative;
  background: var(--ant-color-bg-container, #ffffff);
  border: 1px solid var(--ant-color-border-secondary, #e5e7eb);
  border-radius: 5px;
  padding: 14px;
  transition: all 0.22s cubic-bezier(0.4, 0, 0.2, 1);
  box-shadow: 0 1px 3px 0 rgba(0, 0, 0, 0.03);
  height: 100%;
  width: 100% !important;
  min-width: 0 !important;
  max-width: 100% !important;
  box-sizing: border-box !important;
  display: flex;
  flex-direction: column;
  justify-content: space-between;
}

.matrix-card:hover {
  transform: translateY(-2px);
  border-color: var(--ant-color-border, #d1d5db);
  box-shadow: 0 12px 28px -6px rgba(0, 0, 0, 0.08);
}

[data-theme='dark'] .matrix-card {
  background: #141416;
  border-color: rgba(255, 255, 255, 0.08);
  box-shadow: 0 1px 3px 0 rgba(0, 0, 0, 0.2);
}

[data-theme='dark'] .matrix-card:hover {
  border-color: rgba(255, 255, 255, 0.18);
  box-shadow: 0 14px 32px -8px rgba(0, 0, 0, 0.5);
}

/* Card Header */
.matrix-card-header {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  margin-bottom: 10px;
}

.matrix-type-squircle {
  width: 32px;
  height: 32px;
  border-radius: 6px;
  background: var(--ant-color-fill-alter, #f4f4f5);
  border: 1px solid var(--ant-color-border-secondary, #e4e4e7);
  color: var(--ant-color-text, #18181b);
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  transition: all 0.18s ease;
}

[data-theme='dark'] .matrix-type-squircle {
  background: #18181b;
  border-color: #27272a;
  color: #f4f4f5;
}

.matrix-card:hover .matrix-type-squircle {
  transform: scale(1.04);
  border-color: var(--ant-color-border, #d4d4d8);
}

[data-theme='dark'] .matrix-card:hover .matrix-type-squircle {
  border-color: #3f3f46;
}

.matrix-card-title-group {
  flex: 1;
  min-width: 0;
  overflow: hidden;
}

.matrix-card-title-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
}

.matrix-model-name {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--ant-color-text, #09090b);
  line-height: 1.3;
  word-break: break-word;
  white-space: normal;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
  flex: 1;
  min-width: 0;
}

/* Meta Pills under title */
.matrix-meta-strip {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  margin-top: 5px;
  min-width: 0;
}

.matrix-id-badge {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 10.5px;
  background: var(--ant-color-fill-alter, #f4f4f5);
  color: var(--ant-color-text-secondary, #71717a);
  border: 1px solid var(--ant-color-border-secondary, #e4e4e7);
  padding: 1px 6px;
  border-radius: 5px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
  transition: all 0.15s ease;
  user-select: none;
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.matrix-id-badge:hover {
  background: var(--ant-color-fill-secondary, #e4e4e7);
  color: var(--ant-color-text, #09090b);
  border-color: var(--ant-color-border, #d4d4d8);
}

[data-theme='dark'] .matrix-id-badge {
  background: rgba(255, 255, 255, 0.05);
  border-color: rgba(255, 255, 255, 0.08);
  color: #a1a1aa;
}

[data-theme='dark'] .matrix-id-badge:hover {
  background: rgba(255, 255, 255, 0.1);
  color: #fafafa;
  border-color: rgba(255, 255, 255, 0.16);
}

.matrix-remark-badge {
  font-size: 10px;
  background: var(--ant-color-fill-quaternary, rgba(0, 0, 0, 0.03));
  color: var(--ant-color-text-tertiary, #a1a1aa);
  border: 1px solid var(--ant-color-border-secondary, #e4e4e7);
  padding: 1px 6px;
  border-radius: 5px;
  display: inline-block;
  max-width: 110px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

[data-theme='dark'] .matrix-remark-badge {
  background: rgba(255, 255, 255, 0.04);
  border-color: rgba(255, 255, 255, 0.08);
  color: #71717a;
}

/* Redundancy Status Micro Badges - shadcn ui monochrome */
.matrix-status-pill {
  font-size: 10px;
  font-weight: 500;
  padding: 1px 6px;
  border-radius: 4px;
  display: inline-flex;
  align-items: center;
  gap: 3px;
  flex-shrink: 0;
  letter-spacing: 0.01em;
}

.matrix-status-pill.multi {
  background: var(--ant-color-fill-alter, #f4f4f5);
  color: var(--ant-color-text, #18181b);
  border: 1px solid var(--ant-color-border-secondary, #e4e4e7);
}

[data-theme='dark'] .matrix-status-pill.multi {
  background: #18181b;
  color: #f4f4f5;
  border-color: #27272a;
}

.matrix-status-pill.single {
  background: var(--ant-color-fill-alter, #fafafa);
  color: var(--ant-color-text-secondary, #71717a);
  border: 1px solid var(--ant-color-border-secondary, #e4e4e7);
}

[data-theme='dark'] .matrix-status-pill.single {
  background: #141416;
  color: #a1a1aa;
  border-color: #27272a;
}

.matrix-status-pill.unbound {
  background: transparent;
  color: var(--ant-color-text-tertiary, #a1a1aa);
  border: 1px dashed var(--ant-color-border, #d4d4d8);
}

[data-theme='dark'] .matrix-status-pill.unbound {
  background: transparent;
  color: #71717a;
  border-color: #3f3f46;
}

/* Channel List Container */
.matrix-channels-section {
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid var(--ant-color-border-secondary, rgba(0, 0, 0, 0.06));
  flex: 1;
  display: flex;
  flex-direction: column;
}

[data-theme='dark'] .matrix-channels-section {
  border-top-color: rgba(255, 255, 255, 0.06);
}

.matrix-channels-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}

.matrix-channels-label {
  font-size: 11px;
  font-weight: 600;
  color: var(--ant-color-text-tertiary, #a1a1aa);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  display: flex;
  align-items: center;
  gap: 5px;
}

/* Individual Channel Row */
.matrix-channel-rows {
  display: flex;
  flex-direction: column;
  gap: 5px;
}

.matrix-channel-row {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  padding: 5px 8px;
  border-radius: 7px;
  background: var(--ant-color-fill-alter, #fafafa);
  border: 1px solid var(--ant-color-border-secondary, #f0f0f0);
  transition: all 0.18s cubic-bezier(0.4, 0, 0.2, 1);
  cursor: pointer;
  user-select: none;
  min-width: 0;
}

.matrix-channel-row:hover {
  background: var(--ant-color-fill-secondary, #f4f4f5);
  border-color: var(--ant-color-border, #e4e4e7);
  transform: translateX(1px);
}

[data-theme='dark'] .matrix-channel-row {
  background: rgba(255, 255, 255, 0.03);
  border-color: rgba(255, 255, 255, 0.05);
}

[data-theme='dark'] .matrix-channel-row:hover {
  background: rgba(255, 255, 255, 0.07);
  border-color: rgba(255, 255, 255, 0.12);
}

.matrix-channel-row.disabled {
  opacity: 0.55;
  border-style: dashed;
}

/* Beacon Dot - shadcn ui monochrome */
.matrix-beacon-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
}

.matrix-beacon-dot.online {
  background: var(--ant-color-text, #18181b);
  box-shadow: 0 0 0 2px var(--ant-color-fill-secondary, #e4e4e7);
}

[data-theme='dark'] .matrix-beacon-dot.online {
  background: #f4f4f5;
  box-shadow: 0 0 0 2px rgba(255, 255, 255, 0.15);
}

.matrix-beacon-dot.offline {
  background: #a1a1aa;
  opacity: 0.4;
}

.matrix-channel-name {
  font-size: 11.5px;
  font-weight: 500;
  color: var(--ant-color-text, #09090b);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0;
}

.matrix-channel-row:hover .matrix-channel-name {
  color: var(--ant-color-text, #09090b);
  font-weight: 600;
}

[data-theme='dark'] .matrix-channel-row:hover .matrix-channel-name {
  color: #ffffff;
}

.matrix-channel-chips {
  display: flex;
  align-items: center;
  gap: 3px;
  flex-shrink: 0;
}

.matrix-ha-badge {
  font-size: 8.5px;
  font-weight: 700;
  background: var(--ant-color-text, #09090b);
  color: var(--ant-color-bg-container, #ffffff);
  padding: 1px 3px;
  border-radius: 3px;
  line-height: 1.1;
  letter-spacing: 0.02em;
}

[data-theme='dark'] .matrix-ha-badge {
  background: #fafafa;
  color: #09090b;
}

.matrix-chip-p {
  font-size: 9px;
  font-weight: 600;
  font-family: ui-monospace, SFMono-Regular, monospace;
  background: var(--ant-color-fill-alter, #f4f4f5);
  color: var(--ant-color-text, #18181b);
  border: 1px solid var(--ant-color-border-secondary, #e4e4e7);
  padding: 0 3px;
  border-radius: 3px;
}

[data-theme='dark'] .matrix-chip-p {
  background: #18181b;
  color: #f4f4f5;
  border-color: #27272a;
}

.matrix-chip-w {
  font-size: 9px;
  font-weight: 600;
  font-family: ui-monospace, SFMono-Regular, monospace;
  background: var(--ant-color-fill-alter, #f4f4f5);
  color: var(--ant-color-text-secondary, #71717a);
  border: 1px solid var(--ant-color-border-secondary, #e4e4e7);
  padding: 0 3px;
  border-radius: 3px;
}

[data-theme='dark'] .matrix-chip-w {
  background: #18181b;
  color: #a1a1aa;
  border-color: #27272a;
}

.matrix-channel-actions {
  display: flex;
  align-items: center;
  gap: 2px;
  opacity: 0;
  transition: opacity 0.15s ease;
}

.matrix-channel-row:hover .matrix-channel-actions {
  opacity: 1;
}

.matrix-action-btn {
  width: 16px;
  height: 16px;
  border-radius: 3px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--ant-color-text-secondary, #71717a);
  transition: all 0.15s ease;
}

.matrix-action-btn:hover {
  background: rgba(239, 68, 68, 0.15);
  color: #ef4444;
}

/* Card Bottom Bind Button */
.matrix-card-footer {
  margin-top: 10px;
  padding-top: 6px;
}

.matrix-bind-btn {
  width: 100%;
  border-radius: 7px;
  height: 28px;
  font-size: 11.5px;
  font-weight: 500;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 5px;
  border: 1px dashed var(--ant-color-border, #d1d5db);
  background: transparent;
  color: var(--ant-color-text-secondary, #71717a);
  transition: all 0.18s ease;
  cursor: pointer;
}

.matrix-bind-btn:hover {
  border-color: var(--ant-color-text, #09090b);
  color: var(--ant-color-text, #09090b);
  background: var(--ant-color-fill-quaternary, rgba(0, 0, 0, 0.02));
}

[data-theme='dark'] .matrix-bind-btn {
  border-color: rgba(255, 255, 255, 0.15);
  color: #a1a1aa;
}

[data-theme='dark'] .matrix-bind-btn:hover {
  border-color: #fafafa;
  color: #fafafa;
  background: rgba(255, 255, 255, 0.05);
}

/* Empty State Card */
.matrix-unbound-card {
  padding: 14px 10px;
  text-align: center;
  background: var(--ant-color-fill-quaternary, rgba(0, 0, 0, 0.02));
  border: 1px dashed var(--ant-color-border-secondary, #e5e7eb);
  border-radius: 8px;
  margin: 4px 0;
}

[data-theme='dark'] .matrix-unbound-card {
  background: rgba(255, 255, 255, 0.02);
  border-color: rgba(255, 255, 255, 0.08);
}
`;

const ModelChannelsDisplay: React.FC = () => {
  const [loading, setLoading] = useState(true);
  const [data, setData] = useState<ModelChannelGroup[]>([]);
  const [allChannels, setAllChannels] = useState<Channel[]>([]);
  const [searchQuery, setSearchQuery] = useState('');
  const [searchMode, setSearchMode] = useState<SearchMode>('fuzzy');
  const [sortOption, setSortOption] = useState<SortOption>('name_asc');
  const [copiedMid, setCopiedMid] = useState<string | null>(null);

  const navigate = useNavigate();
  const adminPath = localStorage.getItem('laiai_admin_path') || 'admin';

  const fetchData = useCallback(async () => {
    setLoading(true);
    try {
      const [channelsResp, modelsResp, typesResp] = await Promise.all([
        request.get('/channels') as unknown as Promise<{ data: Channel[] }>,
        request.get('/models') as unknown as Promise<{ data: ModelModel[] }>,
        request.get('/model-types') as Promise<any>
      ]);
      
      const channels = channelsResp.data || [];
      const models = modelsResp.data || [];
      const types = Array.isArray(typesResp) ? typesResp : (typesResp as any)?.data || [];
      
      setAllChannels(channels);

      const byMid = new Map<string, ModelModel>();
      const byModelId = new Map<string, ModelModel>();
      models.forEach(m => {
        byMid.set(String(m.mid), m);
        if (m.model_id) byModelId.set(String(m.model_id), m);
      });
      const resolveModel = (token: string) => byMid.get(token) || byModelId.get(token);

      const modelTypeMap: Record<string, { id: number, name: string, logo: string }> = {};
      models.forEach(m => {
        if (m.type_id) {
          const t = types.find((type: any) => type.id === m.type_id);
          if (t) {
            modelTypeMap[m.mid] = { id: t.id, name: t.name, logo: t.logo || '' };
          }
        }
      });

      const modelMap: Record<string, Channel[]> = {};
      models.forEach(m => {
        modelMap[m.mid] = [];
      });

      channels.forEach(channel => {
        if (!Array.isArray(channel.models)) return;
        channel.models.forEach(token => {
          const rec = resolveModel(String(token));
          const key = rec ? rec.mid : String(token);
          if (!modelMap[key]) modelMap[key] = [];
          if (!modelMap[key].some(c => c.id === channel.id)) {
            modelMap[key].push(channel);
          }
        });
      });

      const groupedData: ModelChannelGroup[] = Object.keys(modelMap).map(model => {
        const rec = resolveModel(model);
        const typeInfo = rec ? modelTypeMap[rec.mid] : modelTypeMap[model];
        return {
          model,
          modelName: rec?.name || model,
          modelId: rec?.model_id || '',
          modelIdAlias: rec?.model_id_alias || '',
          remark: rec?.remark,
          channels: modelMap[model].sort((a, b) => {
            const pDiff = (b.priority ?? 0) - (a.priority ?? 0);
            if (pDiff !== 0) return pDiff;
            const wDiff = (b.weight ?? 1) - (a.weight ?? 1);
            if (wDiff !== 0) return wDiff;
            return a.name.localeCompare(b.name, 'zh-CN');
          }),
          typeId: typeInfo?.id,
          typeName: typeInfo?.name,
          typeLogo: typeInfo?.logo
        };
      });

      setData(groupedData);
    } catch (error) {
      console.error(error);
      message.error('获取模型渠道拓扑数据失败，请检查网络');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  // Filter and sort data
  const filteredData = useMemo(() => {
    const q = searchQuery.trim();
    if (!q) return [];

    let result = data.filter(item => {
      if (item.channels.length === 0) return false;

      if (searchMode === 'exact') {
        const qLower = q.toLowerCase();
        return [item.model, item.modelName, item.modelId, item.modelIdAlias].some(
          v => (v || '').toLowerCase() === qLower
        );
      }

      const qLower = q.toLowerCase();
      return (
        modelMatchesKeyword(
          {
            name: item.modelName,
            model_id: item.modelId,
            mid: item.model,
            model_id_alias: item.modelIdAlias,
            remark: item.remark,
          },
          q,
        ) || item.channels.some(c => c.name.toLowerCase().includes(qLower))
      );
    });

    // Sorting
    result = [...result].sort((a, b) => {
      if (sortOption === 'channels_desc') {
        const diff = b.channels.length - a.channels.length;
        if (diff !== 0) return diff;
      } else if (sortOption === 'channels_asc') {
        const diff = a.channels.length - b.channels.length;
        if (diff !== 0) return diff;
      }
      return a.modelName.localeCompare(b.modelName, 'zh-CN', { numeric: true });
    });

    return result;
  }, [data, searchQuery, searchMode, sortOption]);

  const handleCopyModelId = (mid: string, e?: React.MouseEvent) => {
    e?.stopPropagation();
    copyToClipboard(mid).then((ok) => {
      if (ok) {
        setCopiedMid(mid);
        message.success(`已复制模型标识: ${mid}`);
        setTimeout(() => setCopiedMid(null), 1800);
      } else {
        message.info(`模型标识: ${mid}`);
      }
    });
  };

  const handleRemoveModelFromChannel = async (channel: Channel, item: ModelChannelGroup) => {
    const currentModels = Array.isArray(channel.models) ? channel.models : [];
    const updatedModels = currentModels.filter(
      m => m !== item.model && m !== item.modelId
    );
    
    setLoading(true);
    try {
      await request.put(`/channels/${channel.id}`, { models: updatedModels });
      message.success(`已成功从渠道 [${channel.name}] 中移除了该模型`);
      await fetchData();
    } catch (e) {
      console.error(e);
      setLoading(false);
    }
  };

  const handleAddModelToChannel = async (channel: Channel, item: ModelChannelGroup) => {
    const currentModels = Array.isArray(channel.models) ? channel.models : [];
    if (currentModels.includes(item.model) || (item.modelId && currentModels.includes(item.modelId))) return;

    const updatedModels = [...currentModels, item.model];
    
    setLoading(true);
    try {
      await request.put(`/channels/${channel.id}`, { models: updatedModels });
      message.success(`已成功为渠道 [${channel.name}] 绑定了该模型`);
      await fetchData();
    } catch (e) {
      console.error(e);
      setLoading(false);
    }
  };

  const getUnassociatedChannels = (item: ModelChannelGroup) => {
    return allChannels.filter(c => {
      const models = Array.isArray(c.models) ? c.models : [];
      return !models.includes(item.model) && !(item.modelId && models.includes(item.modelId));
    });
  };

  // Helper for model squircle styling and icons (shadcn ui monochrome zinc style)
  const renderModelSquircleIcon = (typeName?: string, typeLogo?: string) => {
    const lower = (typeName || '').toLowerCase();
    
    let IconComp: React.ComponentType<{ style?: React.CSSProperties }> = LayoutGrid;

    if (lower.includes('封装') || lower.includes('encapsulation')) {
      IconComp = Sparkles;
    } else if (lower.includes('视频') || lower.includes('video')) {
      IconComp = Video;
    } else if (lower.includes('图片') || lower.includes('image')) {
      IconComp = ImageIcon;
    } else if (lower.includes('聊天') || lower.includes('chat') || lower.includes('text')) {
      IconComp = MessageSquare;
    } else if (lower.includes('音频') || lower.includes('audio')) {
      IconComp = AudioLines;
    } else if (lower.includes('embedding') || lower.includes('向量')) {
      IconComp = Cuboid;
    } else if (lower.includes('rerank') || lower.includes('排序')) {
      IconComp = ListOrdered;
    }

    if (typeLogo) {
      return (
        <div className="matrix-type-squircle">
          <SmartSvgIcon 
            src={`/assets/icons/lobe/${typeLogo}.svg`} 
            style={{ width: 18, height: 18, objectFit: 'contain' }}
            onError={(e) => {
              (e.target as HTMLImageElement).style.display = 'none';
            }}
          />
        </div>
      );
    }

    return (
      <div className="matrix-type-squircle">
        <IconComp style={{ width: 16, height: 16 }} />
      </div>
    );
  };

  return (
    <div className="matrix-container">
      <style>{styles}</style>
      
      {/* Top Header Navigation & Controls */}
      <div style={{ marginBottom: 20, display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 14 }}>
          <Button 
            icon={<ArrowLeft style={{ width: 14, height: 14 }} />} 
            onClick={() => navigate(`/${adminPath}/channels`)}
            style={{ borderRadius: 8, display: 'inline-flex', alignItems: 'center', gap: 6 }}
          >
            返回渠道
          </Button>
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <Title level={4} style={{ margin: 0, fontSize: 20, fontWeight: 700, letterSpacing: '-0.02em' }}>
                模型对应可用渠道
              </Title>
              <span style={{ 
                fontSize: 11, 
                fontWeight: 600, 
                padding: '2px 8px', 
                borderRadius: 9999, 
                background: 'var(--ant-color-fill-secondary, #f4f4f5)',
                color: 'var(--ant-color-text-secondary, #71717a)'
              }}>
                {searchQuery.trim() ? `${filteredData.length} 个模型` : '搜索后展示'}
              </span>
            </div>
            <Text type="secondary" style={{ fontSize: 13 }}>
              全局拓扑概览：实时透视各模型承载渠道分布、高可用容灾冗余与调度优先级权重
            </Text>
          </div>
        </div>

        {/* Right Search, Sorter & Refresh Controls */}
        <Space wrap size={10}>
          <Select
            value={sortOption}
            onChange={setSortOption}
            size="small"
            style={{ width: 130 }}
            options={[
              { label: '模型名称 A-Z', value: 'name_asc' },
              { label: '渠道数 (多到少)', value: 'channels_desc' },
              { label: '渠道数 (少到多)', value: 'channels_asc' },
            ]}
          />

          <Segmented
            size="small"
            value={searchMode}
            onChange={(val) => setSearchMode(val as SearchMode)}
            options={[
              { label: '模糊', value: 'fuzzy' },
              { label: '精准', value: 'exact' },
            ]}
          />

          <Input
            placeholder={searchMode === 'exact' ? '精确匹配名称或 ID...' : '搜索名称、ID 或关联渠道...'}
            allowClear
            size="small"
            value={searchQuery}
            onChange={e => setSearchQuery(e.target.value)}
            style={{ width: 220, borderRadius: 6 }}
            prefix={<Search style={{ width: 14, height: 14, opacity: 0.45 }} />}
          />

          <Tooltip title="重新同步渠道拓扑数据">
            <Button 
              icon={<RefreshCw style={{ width: 14, height: 14 }} className={loading ? "animate-spin" : ""} />} 
              onClick={fetchData}
              size="small"
              style={{ borderRadius: 6, display: 'inline-flex', alignItems: 'center', gap: 6 }}
            >
              刷新
            </Button>
          </Tooltip>
        </Space>
      </div>

      {/* Grid Content with responsive stepped columns: Max 6, 4, 2 */}
      <Spin spinning={loading}>
        {filteredData.length === 0 ? (
          <div style={{ padding: '60px 0', textAlign: 'center' }}>
            <Empty 
              description={
                searchQuery.trim() ? (
                  <div>
                    <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 6 }}>未找到匹配的模型</div>
                    <Text type="secondary" style={{ fontSize: 13 }}>
                      请尝试清空搜索词
                    </Text>
                  </div>
                ) : (
                  <div>
                    <div style={{ fontWeight: 600, fontSize: 15, marginBottom: 6 }}>搜索后展示可用渠道</div>
                    <Text type="secondary" style={{ fontSize: 13 }}>
                      输入模型名称、模型ID或MID
                    </Text>
                  </div>
                )
              }
            >
              {searchQuery.trim() ? (
                <Button 
                  size="small" 
                  onClick={() => {
                    setSearchQuery('');
                  }}
                >
                  清空搜索
                </Button>
              ) : null}
            </Empty>
          </div>
        ) : (
          <List
            className="model-channels-grid-list"
            dataSource={filteredData}
            pagination={listPagination({
              size: 'small',
              pageSize: 24,
              pageSizeOptions: ['12', '24', '48', '96'],
              style: { textAlign: 'right', marginTop: 20 }
            })}
            renderItem={(item) => {
              const unassociated = getUnassociatedChannels(item);
              const isMulti = item.channels.length >= 2;
              const isSingle = item.channels.length === 1;
              const isUnbound = item.channels.length === 0;

              return (
                <List.Item>
                  <div className="matrix-card">
                    {/* Top: Model Header */}
                    <div>
                      <div className="matrix-card-header">
                        {renderModelSquircleIcon(item.typeName, item.typeLogo)}

                        <div className="matrix-card-title-group">
                          <div className="matrix-card-title-row">
                            <Tooltip title={item.modelName} placement="topLeft">
                              <span className="matrix-model-name">{item.modelName}</span>
                            </Tooltip>
                          </div>

                          {/* Meta strip with MID on left and redundancy status chip on right */}
                          <div className="matrix-meta-strip">
                            <div style={{ display: 'flex', alignItems: 'center', gap: 5, minWidth: 0, overflow: 'hidden', flex: 1 }}>
                              <Tooltip title={copiedMid === item.model ? "已复制！" : "点击复制模型 ID (mid)"}>
                                <span 
                                  className="matrix-id-badge"
                                  onClick={(e) => handleCopyModelId(item.model, e)}
                                >
                                  {copiedMid === item.model ? (
                                    <Check style={{ width: 10, height: 10, color: 'var(--ant-color-text, #18181b)' }} />
                                  ) : (
                                    <Copy style={{ width: 10, height: 10, opacity: 0.6 }} />
                                  )}
                                  {item.model}
                                </span>
                              </Tooltip>

                              {item.typeName && (
                                <span style={{ 
                                  fontSize: 9.5, 
                                  fontWeight: 500,
                                  padding: '1px 5px',
                                  borderRadius: 4,
                                  background: 'var(--ant-color-fill-secondary, #f4f4f5)',
                                  color: 'var(--ant-color-text-secondary, #71717a)',
                                  whiteSpace: 'nowrap'
                                }}>
                                  {item.typeName}
                                </span>
                              )}

                              {item.remark && (
                                <Tooltip title={`备注: ${item.remark}`}>
                                  <span className="matrix-remark-badge">
                                    {item.remark}
                                  </span>
                                </Tooltip>
                              )}
                            </div>

                            {/* Redundancy status chip in MID row */}
                            {isMulti && (
                              <Tooltip title={`高可用保障：当前由 ${item.channels.length} 个渠道集群共同承载，具备负载分担与故障转移`}>
                                <span className="matrix-status-pill multi">
                                  <ShieldCheck style={{ width: 10, height: 10 }} />
                                  {item.channels.length} 渠容灾
                                </span>
                              </Tooltip>
                            )}
                            {isSingle && (
                              <Tooltip title="单点风险：仅由 1 条渠道承载，若渠道额度耗尽或故障将无备用线路">
                                <span className="matrix-status-pill single">
                                  <AlertTriangle style={{ width: 10, height: 10 }} />
                                  单渠
                                </span>
                              </Tooltip>
                            )}
                            {isUnbound && (
                              <span className="matrix-status-pill unbound">
                                未绑
                              </span>
                            )}
                          </div>
                        </div>
                      </div>

                      {/* Middle: Associated Channels */}
                      <div className="matrix-channels-section">
                        <div className="matrix-channels-header">
                          <span className="matrix-channels-label">
                            <Layers style={{ width: 11, height: 11 }} />
                            支持渠道 ({item.channels.length})
                          </span>

                          {unassociated.length > 0 && (
                            <Dropdown
                              menu={{
                                items: unassociated.map(c => ({
                                  key: c.id,
                                  label: (
                                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 16, minWidth: 200, padding: '2px 0' }}>
                                      <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                                        <div className={`matrix-beacon-dot ${c.status === 1 ? 'online' : 'offline'}`} />
                                        <span style={{ fontWeight: 500 }}>{c.name}</span>
                                        {c.provider_type === 'high_availability_group' && (
                                          <span className="matrix-ha-badge">HA</span>
                                        )}
                                      </div>
                                      <div style={{ display: 'flex', gap: 4 }}>
                                        <span className="matrix-chip-p">P{c.priority ?? 0}</span>
                                        <span className="matrix-chip-w">W{c.weight ?? 1}</span>
                                      </div>
                                    </div>
                                  ),
                                  onClick: () => handleAddModelToChannel(c, item)
                                }))
                              }}
                              trigger={['click']}
                            >
                              <Button 
                                type="text" 
                                size="small" 
                                icon={<Plus style={{ width: 11, height: 11 }} />} 
                                style={{ fontSize: 11, padding: '0 4px', height: 20, color: 'var(--ant-color-text-secondary)', display: 'inline-flex', alignItems: 'center' }}
                              >
                                添加
                              </Button>
                            </Dropdown>
                          )}
                        </div>

                        {/* Channel Rows */}
                        {item.channels.length === 0 ? (
                          <div className="matrix-unbound-card">
                            <Text type="secondary" style={{ fontSize: 11.5, display: 'block', marginBottom: 8 }}>
                              ⚠️ 暂未关联可用渠道，模型无法被调用
                            </Text>
                            {unassociated.length > 0 && (
                              <Dropdown
                                menu={{
                                  items: unassociated.map(c => ({
                                    key: c.id,
                                    label: (
                                      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 16, minWidth: 180 }}>
                                        <span>{c.name}</span>
                                        <div style={{ display: 'flex', gap: 4 }}>
                                          <span className="matrix-chip-p">P{c.priority ?? 0}</span>
                                          <span className="matrix-chip-w">W{c.weight ?? 1}</span>
                                        </div>
                                      </div>
                                    ),
                                    onClick: () => handleAddModelToChannel(c, item)
                                  }))
                                }}
                                trigger={['click']}
                              >
                                <Button type="primary" size="small" icon={<Plus style={{ width: 12, height: 12 }} />} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
                                  立即绑定渠道
                                </Button>
                              </Dropdown>
                            )}
                          </div>
                        ) : (
                          <div className="matrix-channel-rows">
                            {item.channels.map(channel => {
                              const isActive = channel.status === 1;
                              const isHa = channel.provider_type === 'high_availability_group';

                              return (
                                <div 
                                  key={channel.id} 
                                  className={`matrix-channel-row ${isActive ? '' : 'disabled'}`}
                                  onClick={() => navigate(channelEditPath(adminPath, channel.id), { state: { from: 'model-display' } })}
                                  title="点击进入渠道编辑配置"
                                >
                                  {/* Left: Status & Name */}
                                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0, flex: 1 }}>
                                    <Tooltip title={isActive ? "渠道运行正常 (启用)" : "渠道当前处于停用状态"}>
                                      <div className={`matrix-beacon-dot ${isActive ? 'online' : 'offline'}`} />
                                    </Tooltip>

                                    <span className="matrix-channel-name">
                                      {channel.name}
                                    </span>
                                  </div>

                                  {/* Right: Chips & Actions */}
                                  <div className="matrix-channel-chips">
                                    {isHa && (
                                      <Tooltip title="高可用故障转移分组渠道 (HA Group)">
                                        <span className="matrix-ha-badge">HA</span>
                                      </Tooltip>
                                    )}

                                    <Tooltip title={`调度优先级 P: ${channel.priority ?? 0} (数值越大越优先调用)`}>
                                      <span className="matrix-chip-p">P{channel.priority ?? 0}</span>
                                    </Tooltip>

                                    <Tooltip title={`分发权重 W: ${channel.weight ?? 1} (同优先级下的加权轮询比率)`}>
                                      <span className="matrix-chip-w">W{channel.weight ?? 1}</span>
                                    </Tooltip>

                                    {/* Action Buttons */}
                                    <div className="matrix-channel-actions" onClick={e => e.stopPropagation()}>
                                      <Popconfirm
                                        title={`确定从渠道 [${channel.name}] 中移除模型吗？`}
                                        description={item.channels.length === 1 ? "⚠️ 这是当前模型的唯一支持渠道，移除后该模型将暂不可用。" : undefined}
                                        onConfirm={() => handleRemoveModelFromChannel(channel, item)}
                                        okText="确定移除"
                                        cancelText="取消"
                                        okButtonProps={{ danger: true }}
                                      >
                                        <Tooltip title="解绑此渠道">
                                          <span className="matrix-action-btn">
                                            <X style={{ width: 10, height: 10 }} />
                                          </span>
                                        </Tooltip>
                                      </Popconfirm>
                                    </div>
                                  </div>
                                </div>
                              );
                            })}
                          </div>
                        )}
                      </div>
                    </div>

                    {/* Bottom: Bind Channel Dropdown */}
                    {item.channels.length > 0 && (
                      <div className="matrix-card-footer">
                        {unassociated.length > 0 ? (
                          <Dropdown
                            menu={{
                              items: unassociated.map(c => ({
                                key: c.id,
                                label: (
                                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 16, minWidth: 200, padding: '2px 0' }}>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                                      <div className={`matrix-beacon-dot ${c.status === 1 ? 'online' : 'offline'}`} />
                                      <span style={{ fontWeight: 500 }}>{c.name}</span>
                                      {c.provider_type === 'high_availability_group' && (
                                        <span className="matrix-ha-badge">HA</span>
                                      )}
                                    </div>
                                    <div style={{ display: 'flex', gap: 4 }}>
                                      <span className="matrix-chip-p">P{c.priority ?? 0}</span>
                                      <span className="matrix-chip-w">W{c.weight ?? 1}</span>
                                    </div>
                                  </div>
                                ),
                                onClick: () => handleAddModelToChannel(c, item)
                              }))
                            }}
                            trigger={['click']}
                          >
                            <button className="matrix-bind-btn" type="button">
                              <Plus style={{ width: 11, height: 11 }} />
                              绑定新渠道
                            </button>
                          </Dropdown>
                        ) : (
                          <div style={{ 
                            fontSize: 11, 
                            color: 'var(--ant-color-text-quaternary, #a1a1aa)', 
                            textAlign: 'center', 
                            padding: '3px 0',
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            gap: 4
                          }}>
                            <CheckCircle2 style={{ color: 'var(--ant-color-text-secondary, #71717a)', width: 12, height: 12 }} />
                            已绑定全部可用渠道
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                </List.Item>
              );
            }}
          />
        )}
      </Spin>
    </div>
  );
};

export default ModelChannelsDisplay;
