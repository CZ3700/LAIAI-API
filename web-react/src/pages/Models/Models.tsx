import React, { useEffect, useState, useMemo, useRef } from 'react';
import { Table, Button, Space, Tag, Modal, Form, Input, InputNumber, message, Popconfirm, Card, Typography, Select, Row, Col, Switch, Grid, Radio, Empty, Tooltip, Checkbox, Segmented } from 'antd';
import MobileCardList, { MobileCard, CardRow, CardActions } from '../../components/MobileCardList';
import ListPagination, { listPagination, useListPager } from '../../components/ListPagination';
import { PlusOutlined, EditOutlined, DeleteOutlined, SearchOutlined, RightOutlined, ArrowLeftOutlined, ArrowRightOutlined, CloseOutlined, FilterOutlined, FolderOutlined, UnorderedListOutlined, SettingOutlined, CheckSquareOutlined, CheckCircleOutlined, StopOutlined, FileTextOutlined, FileOutlined, CloudUploadOutlined, CloudDownloadOutlined, CopyOutlined } from '@ant-design/icons';
import { useTranslation } from 'react-i18next';
import { localizeClassificationName, localizeFeatureTag } from '../../utils/language';
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import request from '../../utils/request';
import { QueryGuard, isRequestAborted } from '../../utils/queryGuard';
import { modelMatchesKeyword } from '../../utils/modelKeywordMatch';
import { buildClassificationParams, unclassifiedChip, type ModelSourceFilter } from '../../utils/classificationParams';
import useSettingsStore from '../../store/settings';
import { type ModelModel } from '../../types';
import ClassificationFilter from '../../components/Models/ClassificationFilter';
import ClassificationManager from '../../components/Models/ClassificationManager';
import IconPicker from '../../components/IconPicker';
import RateDisplay, { isClaudeRule } from './RateDisplay';
import DiscountScheduleEditor from './DiscountScheduleEditor';
import { discountOverOneItems, discountScheduleOn, getSeedance2Resolutions, isDiscountScheduleExpired, isLimitedDiscountExpired, limitedDiscountOn, limitedDiscountRiskItems, packDiscountSchedule, readDiscountSchedule, sortResolutionKeys, unpackDiscountSchedule, validateDiscountScheduleForm, validateLimitedDiscountForm } from './discountSchedule';
import { formatRate } from '../../utils/timeMultipliers';
import { convertScheduleSlots, convertWallDateTime, scheduleInputTzName } from './scheduleTzConvert';
import dayjs from 'dayjs';
import { billingRulesEditPath, modelEditPath, modelListPath, modelNewPath } from './modelPaths';
import { useThemeStore } from '../../store/theme';
import SmartSvgIcon from '../../components/SmartSvgIcon';
import { Image as ImageIcon, Video, AudioLines, MessageSquare, Cuboid, ListOrdered, Sparkles } from 'lucide-react';
import { imageFeatureOptions, normalizeImageFeatureAttrs } from '../Plugins/components/scheme/modelFeatures';


const { Title, Text } = Typography;

const renderTypeSystemIcon = (name: string, size = 18) => {
  const lowerName = (name || '').toLowerCase();
  if (lowerName.includes('封装') || lowerName.includes('encapsulation')) return <Sparkles size={size} />;
  if (lowerName.includes('图片') || lowerName.includes('image')) return <ImageIcon size={size} />;
  if (lowerName.includes('视频') || lowerName.includes('video')) return <Video size={size} />;
  if (lowerName.includes('音频') || lowerName.includes('audio')) return <AudioLines size={size} />;
  if (lowerName.includes('聊天') || lowerName.includes('chat') || lowerName.includes('text')) return <MessageSquare size={size} />;
  if (lowerName.includes('embedding') || lowerName.includes('向量')) return <Cuboid size={size} />;
  if (lowerName.includes('rerank') || lowerName.includes('排序')) return <ListOrdered size={size} />;
  return null;
}

function modelRowKey(record: ModelModel) {
  return record.library_kind ? `${record.library_kind}-${record.id}` : String(record.id);
}

const ResizableHeaderCell = (props: any) => {
  const { onResize, width: initialWidth, minWidth, ...restProps } = props;
  const thRef = useRef<HTMLTableCellElement>(null);

  const handleMouseDown = (e: React.MouseEvent) => {
    e.stopPropagation();
    e.preventDefault();
    if (!thRef.current) return;
    
    const startX = e.clientX;
    const startWidth = thRef.current.getBoundingClientRect().width;

    const doDrag = (moveEvent: MouseEvent) => {
      document.body.style.userSelect = 'none';
      document.body.style.cursor = 'col-resize';
      const deltaX = moveEvent.clientX - startX;
      const newWidth = Math.max(minWidth || 60, startWidth + deltaX);
      if (onResize) {
        onResize(null, { size: { width: newWidth } });
      }
    };

    const stopDrag = () => {
      document.body.style.userSelect = '';
      document.body.style.cursor = '';
      document.removeEventListener('mousemove', doDrag);
      document.removeEventListener('mouseup', stopDrag);
    };

    document.addEventListener('mousemove', doDrag);
    document.addEventListener('mouseup', stopDrag);
  };

  if (!initialWidth) {
    return <th {...restProps} />;
  }

  return (
    <th 
      {...restProps} 
      ref={thRef}
      style={{ 
        ...restProps.style, 
        width: initialWidth,
        position: 'relative',
        overflow: 'hidden',
        textOverflow: 'ellipsis',
        whiteSpace: 'nowrap'
      }}
    >
      <div style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', paddingRight: 10 }}>
        {restProps.children}
      </div>
      <div
        onMouseDown={handleMouseDown}
        onClick={(e) => e.stopPropagation()}
        style={{
          position: 'absolute',
          right: 0,
          top: 0,
          bottom: 0,
          width: 6,
          cursor: 'col-resize',
          zIndex: 100,
          userSelect: 'none'
        }}
        className="resizable-trigger"
      />
    </th>
  );
};
const { Option } = Select;
const { useBreakpoint } = Grid;

const renderModelLogo = (logo?: string | null, fallbackName?: string) => {
  if (logo) {
    return (
      <div style={{ 
        width: 32, height: 32, borderRadius: 8, 
        background: 'rgba(128,128,128,0.05)', 
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        border: '1px solid rgba(128,128,128,0.15)',
        flexShrink: 0
      }}>
        <SmartSvgIcon
          src={`/assets/icons/lobe/${logo}.svg`}
          alt=""
          style={{ width: 20, height: 20, objectFit: 'contain' }}
          onError={e => { (e.target as HTMLImageElement).style.display = 'none'; }}
        />
      </div>
    );
  }
  return (
    <div style={{ 
      width: 32, height: 32, borderRadius: 8, 
      background: 'rgba(128,128,128,0.04)', 
      display: 'flex', alignItems: 'center', justifyContent: 'center',
      border: '1px dashed rgba(128,128,128,0.2)',
      color: 'var(--text-secondary, #8c8c8c)',
      flexShrink: 0,
      fontSize: 14,
      fontWeight: 'bold',
      textTransform: 'uppercase'
    }}>
      {(fallbackName || 'M').charAt(0)}
    </div>
  );
};

// ===== 生成模型组 GMID (6位数字，以5开头) =====
const getGmid = (originalId: string): string => {
  if (!originalId || originalId.trim() === '') return '';
  let hash = 0;
  for (let i = 0; i < originalId.length; i++) {
    hash = (hash << 5) - hash + originalId.charCodeAt(i);
    hash |= 0;
  }
  const suffix = (Math.abs(hash) % 100000).toString().padStart(5, '0');
  return `5${suffix}`;
};

// ===== 分组展示组件 =====
const GroupedModelTable: React.FC<{ group: { key: string; model_id: string; children: ModelModel[]; count: number }; columns: any[]; isLight: boolean; forceCollapsed?: boolean; onRefresh?: () => void; setModels?: React.Dispatch<React.SetStateAction<ModelModel[]>>; isBatchEditMode?: boolean; selectedRowKeys?: React.Key[]; setSelectedRowKeys?: React.Dispatch<React.SetStateAction<React.Key[]>>; isLibrary?: boolean }> = ({ group, columns, isLight, forceCollapsed, onRefresh, setModels, isBatchEditMode, selectedRowKeys = [], setSelectedRowKeys, isLibrary }) => {
  const [expanded, setExpanded] = useState(true);
  const [modalVisible, setModalVisible] = useState(false);
  const [desc, setDesc] = useState('');
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (forceCollapsed) {
      setExpanded(false);
    } else {
      setExpanded(true);
    }
  }, [forceCollapsed]);


  const firstChild = group.children[0];
  const [groupDesc, setGroupDesc] = useState(firstChild?.description || '');

  useEffect(() => {
    setGroupDesc(firstChild?.description || '');
  }, [firstChild?.description]);

  const handleOpenModal = (e: React.MouseEvent) => {
    e.stopPropagation();
    setDesc(firstChild?.description || '');
    setModalVisible(true);
  };

  const handleSaveDescription = async () => {
    // 1. 立即关闭弹窗并更新前端本地简介和父状态，提供即时无感响应
    setGroupDesc(desc);
    setModalVisible(false);
    if (setModels) {
      const childrenIds = new Set(group.children.map(c => c.id));
      setModels(prev => prev.map(m => {
        if (childrenIds.has(m.id)) {
          return { ...m, description: desc };
        }
        return m;
      }));
    }

    // 2. 后台异步调用 API 进行持久化，不让页面刷新或闪烁
    try {
      await Promise.all(
        group.children.map(m =>
          request.put(`/models/${m.id}`, { description: desc })
        )
      );
    } catch (err: any) {
      console.error('更新模型组简介失败:', err);
    }
  };

  const applyGroupPatch = async (patch: { is_active?: number; enable_log_content?: number }) => {
    const ids = group.children.map(c => c.id);
    setModels?.(prev => prev.map(m => (ids.includes(m.id) ? { ...m, ...patch } : m)));
    try {
      await Promise.all(ids.map(id => request.put(`/models/${id}`, patch)));
    } catch (err) {
      console.error(err);
      onRefresh?.();
    }
  };

  return (
    <div 
      style={{ 
        marginBottom: 16,
        borderRadius: 16,
        border: isLight ? '1px solid #d9d9d9' : '1px solid #303030',
        background: isLight ? '#ffffff' : '#141414',
        boxShadow: isLight ? '0 2px 8px rgba(0,0,0,0.04)' : '0 2px 8px rgba(0,0,0,0.2)',
        transition: 'all 0.4s cubic-bezier(0.175, 0.885, 0.32, 1.275)',
        transform: 'scale(1)',
        overflow: 'hidden',
        position: 'relative'
      }}
      onMouseEnter={(e) => {
        if (!expanded) {
          e.currentTarget.style.transform = 'translateY(-2px) scale(1.002)';
          e.currentTarget.style.boxShadow = isLight ? '0 12px 24px rgba(0,0,0,0.08)' : '0 12px 24px rgba(0,0,0,0.2)';
          e.currentTarget.style.borderColor = isLight ? '#d9d9d9' : '#404040';
        }
      }}
      onMouseLeave={(e) => {
        if (!expanded) {
          e.currentTarget.style.transform = 'scale(1)';
          e.currentTarget.style.boxShadow = '0 2px 8px rgba(0,0,0,0.04)';
          e.currentTarget.style.borderColor = isLight ? '#d9d9d9' : '#303030';
        }
      }}
    >
      <Modal
        title="设置模型组简介说明"
        open={modalVisible}
        onCancel={(e) => {
          e.stopPropagation();
          setModalVisible(false);
        }}
        footer={[
          <Button key="cancel" onClick={(e) => { e.stopPropagation(); setModalVisible(false); }}>
            取消
          </Button>,
          <Button key="submit" type="primary" loading={saving} onClick={(e) => { e.stopPropagation(); handleSaveDescription(); }}>
            保存
          </Button>
        ]}
        destroyOnClose
        centered
        width={500}
      >
        <div style={{ padding: '12px 0' }} onClick={(e) => e.stopPropagation()}>
          <div style={{ marginBottom: 8 }}>
            <Text type="secondary">此说明将同步应用到模型组 {group.model_id} 下的所有模型变体 ({group.count} 个)：</Text>
          </div>
          <Input.TextArea
            rows={4}
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
            placeholder="请输入模型组的简介说明..."
            maxLength={1000}
            showCount
          />
        </div>
      </Modal>

      {/* 组头 */}
      <div
        onClick={() => setExpanded(!expanded)}
        style={{
          display: 'flex', alignItems: 'center', gap: 12,
          padding: '10px 16px', cursor: 'pointer',
          background: expanded ? (isLight ? '#fafafa' : '#1a1a1a') : 'transparent',
          borderBottom: expanded ? (isLight ? '1px solid #f0f0f0' : '1px solid #303030') : '1px solid transparent',
          transition: 'all 0.3s ease',
          userSelect: 'none',
        }}
      >
        <span style={{ 
          color: expanded ? (isLight ? '#000' : '#fff') : '#8c8c8c', 
          fontSize: 14, 
          transition: 'all 0.4s cubic-bezier(0.68, -0.55, 0.265, 1.55)', 
          transform: expanded ? 'rotate(90deg)' : 'rotate(0deg)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          width: 24, height: 24, borderRadius: '50%',
          background: expanded ? (isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.1)') : 'transparent',
        }}>
          <RightOutlined />
        </span>

        {/* Logo */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 16, flex: 1, minWidth: 0 }}>
          <div style={{ 
            padding: 4, borderRadius: 12, 
            background: isLight ? '#fff' : '#141414',
            boxShadow: isLight ? '0 2px 8px rgba(0,0,0,0.06)' : '0 2px 8px rgba(0,0,0,0.2)',
            border: isLight ? '1px solid #f0f0f0' : '1px solid #303030'
          }}>
            {renderModelLogo(group.children.find(m => m.logo)?.logo || firstChild.logo, firstChild.name || group.model_id)}
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span style={{ 
                fontFamily: 'var(--font-family, Inter, sans-serif)', 
                fontSize: 16, 
                fontWeight: 700, 
                color: 'var(--text-color, inherit)',
                letterSpacing: '-0.3px'
              }}>
                {group.model_id}
              </span>
              <span style={{
                fontSize: 12, fontWeight: 500, lineHeight: '20px',
                padding: '0 8px', borderRadius: 4,
                background: isLight ? '#f5f5f5' : '#1f1f1f', 
                color: isLight ? '#595959' : '#a6a6a6',
                border: isLight ? '1px solid #e8e8e8' : '1px solid #303030'
              }}>
                GMID: {getGmid(group.model_id)}
              </span>
              <span style={{
                fontSize: 12, fontWeight: 500, lineHeight: '20px',
                padding: '0 8px', borderRadius: 4,
                background: isLight ? '#f5f5f5' : '#1f1f1f', 
                color: isLight ? '#595959' : '#a6a6a6',
                border: isLight ? '1px solid #e8e8e8' : '1px solid #303030'
              }}>
                {group.count} 个变体
              </span>
            </div>


            {groupDesc && (
              <div 
                style={{ 
                  fontSize: 12, 
                  color: 'var(--text-secondary, #8c8c8c)', 
                  marginTop: 4, 
                  whiteSpace: 'nowrap', 
                  overflow: 'hidden', 
                  textOverflow: 'ellipsis', 
                  maxWidth: '500px'
                }}
                title={groupDesc}
              >
                简介: {groupDesc}
              </div>
            )}
          </div>
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 10, paddingRight: 8, flexShrink: 0 }} onClick={(e) => e.stopPropagation()}>
          <Tooltip title="设置模型组简介说明">
            <Button
              type="text"
              icon={<SettingOutlined style={{ fontSize: 16 }} />}
              onClick={handleOpenModal}
              style={{ 
                display: 'flex', 
                alignItems: 'center', 
                justifyContent: 'center',
                width: 28,
                height: 28,
                borderRadius: '50%',
                color: isLight ? '#8c8c8c' : '#8c8c8c',
              }}
            />
          </Tooltip>
          {!isLibrary && (
            <div style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <Text type="secondary" style={{ fontSize: 12, lineHeight: '20px', whiteSpace: 'nowrap' }}>启用</Text>
              <Switch size="small" checked={group.children.every(m => m.is_active === 1)} onChange={(on) => applyGroupPatch({ is_active: on ? 1 : 0 })} />
            </div>
          )}
          {!isLibrary && (
            <div style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <Text type="secondary" style={{ fontSize: 12, lineHeight: '20px', whiteSpace: 'nowrap' }}>上下文</Text>
              <Switch size="small" checked={group.children.every(m => m.enable_log_content === 1)} onChange={(on) => applyGroupPatch({ enable_log_content: on ? 1 : 0 })} />
            </div>
          )}
        </div>
      </div>

      {/* 展开的子表 */}
      {expanded && (
        <div 
          className="slide-down-enter"
          style={{ 
            background: isLight ? '#fafafa' : '#0a0a0a',
            padding: '12px 0',
            boxShadow: 'inset 0 6px 12px -8px rgba(0, 0, 0, 0.1)',
            transformOrigin: 'top',
          }}
        >
          <style>{`
            @keyframes slideDownEnter {
              0% { opacity: 0; transform: translateY(-10px) scaleY(0.95); }
              100% { opacity: 1; transform: translateY(0) scaleY(1); }
            }
            .slide-down-enter {
              animation: slideDownEnter 0.4s cubic-bezier(0.16, 1, 0.3, 1) forwards;
            }
            .expanded-model-table .ant-table {
              background: transparent !important;
            }
            .expanded-model-table .ant-table-container {
              background: transparent !important;
              border-radius: 8px;
              overflow: hidden;
            }
            .expanded-model-table .ant-table-cell {
              background: transparent;
            }

            .expanded-model-table .ant-table-tbody > tr > td {
              border-bottom: 1px solid ${isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.04)'} !important;
              transition: background 0.3s;
            }
            .expanded-model-table .ant-table-tbody > tr:hover > td {
              background: ${isLight ? '#ffffff' : '#1f1f1f'} !important;
              box-shadow: ${isLight ? '0 2px 8px rgba(0,0,0,0.05)' : '0 2px 8px rgba(0,0,0,0.2)'};
            }
            .expanded-model-table .ant-table-tbody > tr:last-child > td {
              border-bottom: none !important;
            }
          `}</style>
          <Table
            className="expanded-model-table compact-table"
            dataSource={group.children}
            columns={columns}
            rowKey={modelRowKey}
            size="small"
            pagination={false}
            showHeader={false}
            tableLayout="fixed"
            style={{ marginBottom: 0 }}
            rowSelection={isBatchEditMode ? {
              selectedRowKeys,
              onChange: (newKeys: React.Key[]) => {
                if (setSelectedRowKeys) {
                  setSelectedRowKeys(prev => {
                    const currentGroupRowKeys = new Set(group.children.map(c => modelRowKey(c)));
                    const otherGroupKeys = prev.filter(k => !currentGroupRowKeys.has(String(k)));
                    return [...otherGroupKeys, ...newKeys];
                  });
                }
              }
            } : undefined}
          />
        </div>
      )}
    </div>
  );
};

const FeatureTagsSelect = ({ value = [], onChange, options = [] }: any) => {
  const { t } = useTranslation();
  const [inputValue, setInputValue] = useState('');
  
  const toggleOption = (opt: string) => {
    const next = value.includes(opt) ? value.filter((v: string) => v !== opt) : [...value, opt];
    onChange?.(next);
  };
  
  const handleAddCustom = (e: any) => {
    if (e.key === 'Enter' && inputValue.trim()) {
      e.preventDefault();
      const newTag = inputValue.trim();
      if (!value.includes(newTag)) {
        onChange?.([...value, newTag]);
      }
      setInputValue('');
    }
  };

  const removeCustom = (opt: string) => {
    onChange?.(value.filter((v: string) => v !== opt));
  };

  const customTags = value.filter((v: string) => !options.includes(v));

  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
      {options.map((opt: string) => (
        <Tag.CheckableTag 
          key={opt}
          checked={value.includes(opt)} 
          onChange={() => toggleOption(opt)}
          style={{ 
            border: '1px solid', 
            borderColor: value.includes(opt) ? 'transparent' : 'var(--border-color, rgba(128,128,128,0.2))', 
            padding: '4px 12px', 
            fontSize: 13,
            lineHeight: '22px'
          }}
        >
          {localizeFeatureTag(opt, t)}
        </Tag.CheckableTag>
      ))}
      {customTags.map((opt: string) => (
        <Tag 
          key={opt} 
          closable 
          onClose={(e) => { e.preventDefault(); removeCustom(opt); }}
          color="blue"
          style={{ padding: '4px 12px', fontSize: 13, border: 'none', lineHeight: '22px' }}
        >
          {localizeFeatureTag(opt, t)}
        </Tag>
      ))}
      <Input 
        size="small" 
        style={{ width: 148, height: 32, borderRadius: 6 }} 
        placeholder={t('models.custom_feature_placeholder', '+ 自定义并回车')} 
        value={inputValue}
        onChange={e => setInputValue(e.target.value)}
        onKeyDown={handleAddCustom}
      />
    </div>
  );
};

const Models: React.FC = () => {
  const { t, i18n } = useTranslation();
  const tableContainerRef = useRef<HTMLDivElement>(null);
  const { page: currentPage, pageSize, setPage: setCurrentPage, onChange } = useListPager();
  const isEn = i18n.language === 'en';
  const { settings } = useSettingsStore();
  const adminPath = settings?.site?.admin_path || 'admin';
  const operationTz = settings?.site?.default_timezone?.trim() || 'Asia/Shanghai';
  const adminTz = settings?.site?.admin_display_timezone?.trim() || operationTz;
  const navigate = useNavigate();
  const location = useLocation();
  const { id: routeParamId } = useParams<{ id?: string }>();
  const [searchParams, setSearchParams] = useSearchParams();
  const normalizedPath = location.pathname.replace(/\/$/, '');
  const isNewRoute = /\/models\/new$/.test(normalizedPath);
  const isLibraryEditRoute = /\/models\/library\/edit\/[^/]+$/.test(normalizedPath);
  const isEditRoute = /\/models\/edit\/[^/]+$/.test(normalizedPath);
  const routeEditId = (isEditRoute || isLibraryEditRoute) ? routeParamId : undefined;
  const isEditorOpen = isNewRoute || isEditRoute || isLibraryEditRoute;
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';
  const currencySymbol = settings?.currency?.currency_symbol || '$';
  const auxiliaryCurrencies = useMemo(() => {
    const list = settings?.currency?.auxiliary_currencies;
    return Array.isArray(list) ? list.filter(c => c.enabled) : [];
  }, [settings?.currency?.auxiliary_currencies]);
  const [selectedCurrencyCode, setSelectedCurrencyCode] = useState<string>('');
  const [models, setModels] = useState<ModelModel[]>([]);
  const [loading, setLoading] = useState(true);
  const [showGroupsOnly, setShowGroupsOnly] = useState(false);
  const [editingModel, setEditingModel] = useState<ModelModel | null>(null);
  const [billingType, setBillingType] = useState('tokens');
  const [saving, setSaving] = useState(false);
  const [aliasEnabled, setAliasEnabled] = useState(false);  // 模型ID别名映射开关（本地UI状态）
  const [form] = Form.useForm();
  const screens = useBreakpoint();

  // Classification State
  const [allProviders, setAllProviders] = useState<any[]>([]);
  const [allApiProviders, setAllApiProviders] = useState<any[]>([]);
  const [allTypes, setAllTypes] = useState<any[]>([]);
  const [providersStats, setProvidersStats] = useState<any[]>([]);
  const [apiProvidersStats, setApiProvidersStats] = useState<any[]>([]);
  const [typesStats, setTypesStats] = useState<any[]>([]);
  const [allForwardRules, setAllForwardRules] = useState<any[]>([]);
  const [allBillingRules, setAllBillingRules] = useState<any[]>([]);
  const [selectedProvider, setSelectedProvider] = useState<number | null>(null);
  const [selectedApiProvider, setSelectedApiProvider] = useState<number | null>(null);
  const [selectedType, setSelectedType] = useState<number | null>(null);
  const [isProviderManagerVisible, setIsProviderManagerVisible] = useState(false);
  const [isApiProviderManagerVisible, setIsApiProviderManagerVisible] = useState(false);
  const [isTypeManagerVisible, setIsTypeManagerVisible] = useState(false);
  const [searchKeyword, setSearchKeyword] = useState('');
  const [sourceFilter, setSourceFilter] = useState<ModelSourceFilter>(() => (
    /\/models\/library\/edit\//.test(window.location.pathname) || new URLSearchParams(window.location.search).get('source') === 'library'
      ? 'library'
      : 'custom'
  ));
  const [publishingId, setPublishingId] = useState<string | null>(null);
  const [sourceCounts, setSourceCounts] = useState<{ custom: number; library: number } | null>(null);
  const isLibrary = sourceFilter === 'library';
  type ActiveRightPanel = 'billing' | 'forwarding' | 'provider' | 'api_provider' | 'type' | 'billing_rule' | 'forward_rules';
  const [activeRightPanel, setActiveRightPanel] = useState<ActiveRightPanel | null>('provider');
  const [billingTypeFilter, setBillingTypeFilter] = useState<string>('tokens');
  const [billingRuleFilter, setBillingRuleFilter] = useState<string>('all');
  const [billingSearchKeyword, setBillingSearchKeyword] = useState<string>('');
  const [forwardRuleSearchKeyword, setForwardRuleSearchKeyword] = useState<string>('');
  const [forwardRuleCategoryFilter, setForwardRuleCategoryFilter] = useState<string>('all');
  const [sortType, setSortType] = useState<string>('order_desc');
  const [recentlyAddedId, setRecentlyAddedId] = useState<number | null>(null);
  const [tableBillingTypeFilter, setTableBillingTypeFilter] = useState<string>('all');
  const [tableStatusFilter, setTableStatusFilter] = useState<string>('all');
  const modelsQueryGuard = useRef(new QueryGuard());
  const statsQueryGuard = useRef(new QueryGuard());
  const newFormReadyRef = useRef(false);
  const hydratedEditKeyRef = useRef<string | null>(null);
  const logoManuallyEditedRef = useRef(false);
  const [classificationsReady, setClassificationsReady] = useState(false);

  // 列表只按分类拉 /models；关键词本地筛
  const fetchModels = async () => {
    const signal = modelsQueryGuard.current.begin();
    setLoading(true);
    try {
      const params = buildClassificationParams(selectedProvider, selectedApiProvider, selectedType, sourceFilter);
      const resp = await (request.get('/models', { params, signal }) as unknown as Promise<{ data: ModelModel[] }>);
      if (!modelsQueryGuard.current.isCurrent(signal)) return;
      setModels(Array.isArray(resp?.data) ? resp.data : []);
    } catch (e) {
      if (isRequestAborted(e)) return;
      console.error(e);
      if (modelsQueryGuard.current.isCurrent(signal)) setModels([]);
    } finally {
      if (modelsQueryGuard.current.isCurrent(signal)) setLoading(false);
    }
  };

  // 分类角标交叉计数
  const fetchClassificationsStats = async () => {
    const signal = statsQueryGuard.current.begin();
    try {
      const params = buildClassificationParams(selectedProvider, selectedApiProvider, selectedType, sourceFilter);
      const resp = await (request.get('/classifications/stats', { params, signal }) as any);
      if (!statsQueryGuard.current.isCurrent(signal)) return;
      setProvidersStats([...(resp.providers || []), unclassifiedChip(resp.unclassified_providers)]);
      setApiProvidersStats([...(resp.api_providers || []), unclassifiedChip(resp.unclassified_api_providers)]);
      setTypesStats([...(resp.types || []), unclassifiedChip(resp.unclassified_types)]);
      const counts = resp.source_counts;
      if (counts) {
        setSourceCounts({
          custom: Number(counts.custom) || 0,
          library: Number(counts.library) || 0,
        });
      }
    } catch (e) {
      if (isRequestAborted(e)) return;
      console.error(e);
    }
  };

  const fetchAllClassifications = async () => {
    try {
      const [provResp, apiProvResp, typeResp, rules, brs] = await Promise.all([
        request.get('/model-providers') as Promise<any>,
        request.get('/model-api-providers') as Promise<any>,
        request.get('/model-types') as Promise<any>,
        request.get('/forward-rules') as Promise<any>,
        request.get('/billing-rules') as Promise<any>
      ]);
      const activeProviders = Array.isArray(provResp) ? provResp.filter(p => p.is_active) : [];
      const activeApiProviders = Array.isArray(apiProvResp) ? apiProvResp.filter(p => p.is_active) : [];
      const activeTypes = Array.isArray(typeResp) ? typeResp.filter(t => t.is_active) : [];
      setAllProviders(activeProviders);
      setAllApiProviders(activeApiProviders);
      setAllTypes(activeTypes);
      setAllForwardRules(Array.isArray(rules) ? rules.filter(r => r.is_active) : []);
      setAllBillingRules(Array.isArray(brs) ? brs.filter(b => b.is_active === 1) : []);
      return { providers: activeProviders, apiProviders: activeApiProviders, types: activeTypes };
    } catch (e) {
      console.error(e);
      return null;
    } finally {
      setClassificationsReady(true);
    }
  };

  const handleClassificationUpdate = async (kind: 'provider' | 'api_provider' | 'type') => {
    const fresh = await fetchAllClassifications();
    if (fresh) {
      if (kind === 'provider' && selectedProvider != null && selectedProvider > 0 && !fresh.providers.some((p: any) => p.id === selectedProvider)) {
        setSelectedProvider(null);
      }
      if (kind === 'api_provider' && selectedApiProvider != null && selectedApiProvider > 0 && !fresh.apiProviders.some((p: any) => p.id === selectedApiProvider)) {
        setSelectedApiProvider(null);
      }
      if (kind === 'type' && selectedType != null && selectedType > 0 && !fresh.types.some((tp: any) => tp.id === selectedType)) {
        setSelectedType(null);
      }
    }
    fetchClassificationsStats();
    fetchModels();
  };

  useEffect(() => {
    fetchModels();
    fetchClassificationsStats();
  }, [selectedProvider, selectedApiProvider, selectedType, sourceFilter]);

  useEffect(() => {
    setCurrentPage(1);
  }, [selectedProvider, selectedApiProvider, selectedType, searchKeyword, sourceFilter]);

  useEffect(() => {
    fetchAllClassifications();
    return () => {
      modelsQueryGuard.current.dispose();
      statsQueryGuard.current.dispose();
    };
  }, []);

  useEffect(() => {
    if (isEditorOpen) return;
    const src = searchParams.get('source') === 'library' ? 'library' : 'custom';
    setSourceFilter(src);
  }, [isEditorOpen, searchParams]);

  useEffect(() => {
    if (isLibraryEditRoute || (isEditRoute && searchParams.get('source') === 'library')) {
      setSourceFilter('library');
    }
  }, [isLibraryEditRoute, isEditRoute, searchParams]);


  const formatPrice = (price: number | string | undefined | null) => {
    if (price === undefined || price === null || price === '') return '-';
    const num = Number(price);
    if (isNaN(num)) return String(price);
    if (selectedCurrencyCode === '') {
      return `${currencySymbol}${num}`;
    }
    const curr = auxiliaryCurrencies.find(c => c.code === selectedCurrencyCode);
    if (curr) {
      return `${curr.symbol}${(num * curr.exchange_rate).toFixed(6).replace(/\\.?0+$/, '')}`;
    }
    return `${currencySymbol}${num}`;
  };

  const handleCloseEditor = () => {
    navigate(modelListPath(adminPath, sourceFilter));
  };

  const initNewModelForm = () => {
    setEditingModel(null);
    setBillingType('tokens');
    setBillingTypeFilter('tokens');
    setBillingRuleFilter('all');
    setSaving(false);
    setAliasEnabled(false);
    setActiveRightPanel('provider');
    logoManuallyEditedRef.current = false;
    form.resetFields();
    form.setFieldsValue({
      is_active: true,
      enable_log_content: false,
      pre_deduction: 1,
      sort_order: 0,
      site_discount_enabled: true,
      site_discount: 1.0,
      global_discount_enabled: false,
      global_discount: 1.0,
    });
  };

  const handleAdd = () => {
    navigate(modelNewPath(adminPath));
  };

  const hydrateModelEditor = (record: ModelModel) => {
    setEditingModel(record);
    setBillingType('tokens');
    const matchedBr = allBillingRules.find((item) => item.id === record.billing_rule_id);
    setBillingTypeFilter(matchedBr?.billing_type || 'tokens');
    setBillingRuleFilter('all');
    setSaving(false);
    setActiveRightPanel('provider');
    const currentProvider = allProviders.find((item) => item.id === record.provider_id);
    logoManuallyEditedRef.current = Boolean(record.logo && (!currentProvider || currentProvider.logo !== record.logo));

    let ruleIds: number[] = [];
    try {
      if (record.forward_rule_ids) {
        const parsed = JSON.parse(record.forward_rule_ids);
        // 过滤掉已删除/禁用的规则 ID，消除脏数据
        ruleIds = Array.isArray(parsed) ? parsed.filter((id: number) => allForwardRules.some(r => r.id === id)) : [];
      }
    } catch (e) { }

    let featureAttributes: string[] = [];
    try {
      if (record.feature_attributes) {
        const parsed = JSON.parse(record.feature_attributes);
        featureAttributes = Array.isArray(parsed) ? parsed.filter((item) => typeof item === 'string') : [];
      }
    } catch (e) { }
    const typeName = allTypes.find((item) => item.id === record.type_id)?.name || '';
    if (typeName.includes('图片')) featureAttributes = normalizeImageFeatureAttrs(featureAttributes);

    const discountSchedule = unpackDiscountSchedule(record.discount_schedule);
    form.setFieldsValue({
      ...record,
      forward_rule_ids: ruleIds,
      feature_attributes: featureAttributes,
      sort_order: record.sort_order ?? 0,
      is_active: record.is_active === 1,
      enable_log_content: record.enable_log_content === 1,
      site_discount_enabled: record.site_discount_enabled === 1,
      pre_deduction: record.pre_deduction != null && Number(record.pre_deduction) > 0 ? Number(record.pre_deduction) : 1,
      site_discount: record.site_discount != null && Number(record.site_discount) > 0 ? Number(record.site_discount) : 1.0,
      global_discount_enabled: record.global_discount_enabled === 1,
      global_discount: record.global_discount != null && Number(record.global_discount) > 0 ? Number(record.global_discount) : 1.0,
      limited_discount_enabled: discountSchedule.limited.enabled,
      limited_discount_rate: discountSchedule.limited.discount,
      limited_site_discount: discountSchedule.limited.site_discount,
      limited_resolution_discounts: discountSchedule.limited.resolution_discounts,
      limited_discount_has_end: discountSchedule.limited.has_end,
      limited_discount_input_tz: discountSchedule.limited.edit_tz,
      limited_discount_end_at: discountSchedule.limited.has_end
        ? dayjs(convertWallDateTime(
          discountSchedule.limited.end_at_raw,
          operationTz,
          scheduleInputTzName(discountSchedule.limited.edit_tz, operationTz, adminTz),
        ))
        : null,
      discount_schedule_enabled: discountSchedule.enabled,
      discount_schedule_input_tz: discountSchedule.edit_tz,
      discount_schedule_has_end: !!discountSchedule.end_at_raw,
      discount_schedule_end_at: discountSchedule.end_at_raw
        ? dayjs(convertWallDateTime(
          discountSchedule.end_at_raw,
          operationTz,
          scheduleInputTzName(discountSchedule.edit_tz, operationTz, adminTz),
        ))
        : null,
      discount_slots: convertScheduleSlots(
        discountSchedule.slots,
        operationTz,
        scheduleInputTzName(discountSchedule.edit_tz, operationTz, adminTz),
      ),
      logo: record.logo || undefined,
      remark: record.remark,
      description: record.description,
    });
    setAliasEnabled(!!(record.model_id_alias && record.model_id_alias.length > 0));
  };

  const handleEdit = (record: ModelModel) => {
    navigate(modelEditPath(adminPath, record.mid || record.id, record.library_kind));
  };

  useEffect(() => {
    if (!isNewRoute) {
      newFormReadyRef.current = false;
      return;
    }
    if (newFormReadyRef.current) return;
    newFormReadyRef.current = true;
    initNewModelForm();
  }, [isNewRoute]);

  useEffect(() => {
    if (!routeEditId) {
      hydratedEditKeyRef.current = null;
      return;
    }
    if (loading || !classificationsReady) return;
    if (isLibraryEditRoute && sourceFilter !== 'library') return;
    if (isEditRoute && searchParams.get('source') === 'library' && sourceFilter !== 'library') return;
    const found = models.find((m) => {
      if (isLibraryEditRoute && m.library_kind !== 'catalog') return false;
      if (!isLibraryEditRoute && m.library_kind === 'catalog') return false;
      return m.mid && String(m.mid) === String(routeEditId);
    }) || models.find((m) => {
      if (isLibraryEditRoute && m.library_kind !== 'catalog') return false;
      if (!isLibraryEditRoute && m.library_kind === 'catalog') return false;
      return String(m.id) === String(routeEditId);
    });
    if (found) {
      const key = `${found.library_kind || 'listed'}-${found.mid || found.id}`;
      if (hydratedEditKeyRef.current === key) return;
      hydratedEditKeyRef.current = key;
      hydrateModelEditor(found);
    } else {
      if (selectedProvider !== null || selectedApiProvider !== null || selectedType !== null) {
        setSelectedProvider(null);
        setSelectedApiProvider(null);
        setSelectedType(null);
        return;
      }
      message.error('未找到指定模型');
      handleCloseEditor();
    }
  }, [routeEditId, models, loading, classificationsReady, isLibraryEditRoute, isEditRoute, sourceFilter, searchParams, selectedProvider, selectedApiProvider, selectedType]);

  const isFromLibrary = (record: ModelModel) =>
    typeof record.library_mid === 'string' && record.library_mid.trim() !== '';

  const handleDelete = async (record: ModelModel) => {
    try {
      const url = record.library_kind === 'catalog' ? `/models/library/${record.id}` : `/models/${record.id}`;
      await request.delete(url);
      message.success(t('common.success'));
      fetchModels();
      fetchClassificationsStats();
      fetchAllClassifications();
    } catch (e) {
      console.error(e);
    }
  };

  const handlePublish = async (record: ModelModel) => {
    const isUnlisted = record.library_kind === 'unlisted';
    setPublishingId(modelRowKey(record));
    try {
      if (isUnlisted) {
        await request.post(`/models/${record.id}/relist`);
      } else {
        await request.post(`/models/library/${record.id}/publish`);
      }
      fetchModels();
      fetchClassificationsStats();
    } catch (e) {
      console.error(e);
    } finally {
      setPublishingId(null);
    }
  };

  const handleUnlist = async (record: ModelModel) => {
    setPublishingId(modelRowKey(record));
    try {
      await request.post(`/models/${record.id}/unlist`);
      fetchModels();
      fetchClassificationsStats();
    } catch (e) {
      console.error(e);
    } finally {
      setPublishingId(null);
    }
  };

  const [copyTarget, setCopyTarget] = useState<ModelModel | null>(null);
  const [copyName, setCopyName] = useState('');
  const [copying, setCopying] = useState(false);

  const openCopyModal = (record: ModelModel) => {
    setCopyTarget(record);
    setCopyName(record.name ? `${record.name} 副本` : '副本');
  };

  const handleCopy = async () => {
    if (!copyTarget || copying) return;
    setCopying(true);
    try {
      const url = copyTarget.library_kind === 'catalog'
        ? `/models/library/${copyTarget.id}/copy`
        : `/models/${copyTarget.id}/copy`;
      await request.post(url, { name: copyName.trim() });
      message.success(t('models.copy_ok'));
      setCopyTarget(null);
      fetchModels();
      fetchClassificationsStats();
    } catch (e) {
      console.error(e);
    } finally {
      setCopying(false);
    }
  };

  const handleSave = async (values: any) => {
    try {
      const preDeductionNum = Number(values.pre_deduction);
      if (
        values.pre_deduction === undefined ||
        values.pre_deduction === null ||
        values.pre_deduction === '' ||
        !Number.isFinite(preDeductionNum) ||
        preDeductionNum <= 0
      ) {
        message.error('预扣费必须大于 0');
        return;
      }
      values.pre_deduction = preDeductionNum;
      // Cleanup arrays internally managed
      values.is_active = values.is_active ? 1 : 0;
      values.enable_log_content = values.enable_log_content ? 1 : 0;
      // 统一折扣限价 / 全站折扣：必须大于 0，不填跟随计费规则原价 1.0（免费只能在高级设置里配置）
      const positiveRate = (v: unknown): number | null => {
        if (v == null || v === '') return null;
        const n = Number(v);
        return Number.isFinite(n) && n > 0 ? n : null;
      };
      const prevSiteDiscount = positiveRate(editingModel?.site_discount) ?? 1.0;
      const prevGlobalDiscount = positiveRate(editingModel?.global_discount) ?? 1.0;
      values.site_discount = positiveRate(values.site_discount) ?? (values.site_discount_enabled ? 1.0 : prevSiteDiscount);
      values.global_discount = positiveRate(values.global_discount) ?? (values.global_discount_enabled ? 1.0 : prevGlobalDiscount);
      const br = allBillingRules.find(b => b.id === values.billing_rule_id);
      const isSeedance2 = br?.billing_rule === 'seedance2.0';
      const activeSd2Res = isSeedance2 ? getSeedance2Resolutions(br) : [];
      const filteredResDiscounts: Record<string, number | null | undefined> = {};
      if (isSeedance2 && values.limited_resolution_discounts && typeof values.limited_resolution_discounts === 'object') {
        activeSd2Res.forEach((k) => {
          if (values.limited_resolution_discounts[k] !== undefined) {
            filteredResDiscounts[k] = values.limited_resolution_discounts[k];
          }
        });
      }
      const limitedOn = !!values.global_discount_enabled && !!values.limited_discount_enabled;
      const limitedError = validateLimitedDiscountForm(
        limitedOn,
        !!values.global_discount_enabled,
        isSeedance2,
        values.limited_discount_rate,
        filteredResDiscounts,
        !!values.limited_discount_has_end,
        values.limited_discount_end_at,
      );
      if (limitedError) {
        message.error(limitedError);
        return;
      }
      const rawLimitedSite = values.limited_site_discount;
      const limitedSiteDiscount = rawLimitedSite == null || rawLimitedSite === '' ? null : Number(rawLimitedSite);
      if (limitedOn && limitedSiteDiscount != null && !(Number.isFinite(limitedSiteDiscount) && limitedSiteDiscount >= 0)) {
        message.error('限时折扣期间折扣限价必须大于等于 0（0 为免费）');
        return;
      }
      // 倍率大于 1 即高于原价收费：新出现的项需二次确认（已保存过的同样配置不重复弹）
      {
        const prevSchedule = editingModel ? unpackDiscountSchedule(editingModel.discount_schedule) : null;
        const overNow = discountOverOneItems({
          siteOn: !!values.site_discount_enabled,
          globalOn: !!values.global_discount_enabled,
          site: values.site_discount,
          global: values.global_discount,
          limitedOn,
          isSeedance2,
          limitedRate: values.limited_discount_rate,
          limitedSite: limitedSiteDiscount,
          resolutionDiscounts: filteredResDiscounts,
          scheduleOn: !limitedOn && !!values.global_discount_enabled && !!values.discount_schedule_enabled,
          slots: values.discount_slots,
        });
        const overPrev = editingModel && prevSchedule
          ? discountOverOneItems({
            siteOn: editingModel.site_discount_enabled === 1,
            globalOn: editingModel.global_discount_enabled === 1,
            site: editingModel.site_discount,
            global: editingModel.global_discount,
            limitedOn: editingModel.global_discount_enabled === 1 && prevSchedule.limited.enabled,
            isSeedance2,
            limitedRate: prevSchedule.limited.discount,
            limitedSite: prevSchedule.limited.site_discount,
            resolutionDiscounts: prevSchedule.limited.resolution_discounts,
            scheduleOn: prevSchedule.enabled,
            slots: prevSchedule.slots,
          })
          : [];
        const newOver = overNow.filter((item) => !overPrev.includes(item));
        if (newOver.length > 0) {
          const confirmed = await new Promise<boolean>((resolve) => {
            Modal.confirm({
              title: '确认高于原价的倍率',
              content: (
                <div>
                  <div style={{ marginBottom: 6 }}>以下倍率大于 1，将按高于计费规则原价向用户收费，请确认不是误填（如 0.1 填成 10）：</div>
                  {newOver.map((item) => (
                    <div key={item}>· {item}</div>
                  ))}
                </div>
              ),
              okText: '确认保存',
              cancelText: '返回修改',
              okButtonProps: { danger: true },
              onOk: () => resolve(true),
              onCancel: () => resolve(false),
            });
          });
          if (!confirmed) return;
        }
      }
      if (limitedOn) {
        const siteOn = !!values.site_discount_enabled;
        const prev = editingModel ? unpackDiscountSchedule(editingModel.discount_schedule).limited : null;
        const floorNow = limitedSiteDiscount ?? values.site_discount;
        const floorPrev = prev?.site_discount ?? values.site_discount;
        const riskItems = limitedDiscountRiskItems(isSeedance2, values.limited_discount_rate, filteredResDiscounts, siteOn, floorNow);
        // 已保存过的同样配置不再重复确认
        const prevItems = prev?.enabled
          ? limitedDiscountRiskItems(isSeedance2, prev.discount, prev.resolution_discounts, siteOn, floorPrev)
          : [];
        const newItems = riskItems.filter((item) => !prevItems.includes(item));
        if (newItems.length > 0) {
          const confirmed = await new Promise<boolean>((resolve) => {
            Modal.confirm({
              title: '确认限时折扣倍率',
              content: (
                <div>
                  <div style={{ marginBottom: 6 }}>以下限时折扣为免费或低于折扣限价，生效期间所有调用将按此结算：</div>
                  {newItems.map((item) => (
                    <div key={item}>· {item}</div>
                  ))}
                </div>
              ),
              okText: '确认保存',
              cancelText: '返回修改',
              okButtonProps: { danger: true },
              onOk: () => resolve(true),
              onCancel: () => resolve(false),
            });
          });
          if (!confirmed) return;
        }
      }
      const scheduleOn = !limitedOn && !!values.global_discount_enabled && !!values.discount_schedule_enabled;
      const scheduleError = validateDiscountScheduleForm(
        scheduleOn,
        values.discount_slots,
        !!values.site_discount_enabled,
        true,
        values.site_discount,
        values.global_discount,
      );
      if (scheduleError) {
        message.error(scheduleError);
        return;
      }
      const editTz = values.discount_schedule_input_tz === 'admin_display' ? 'admin_display' : 'operation';
      const storedSlots = convertScheduleSlots(
        values.discount_slots,
        scheduleInputTzName(editTz, operationTz, adminTz),
        operationTz,
      );
      const storedError = validateDiscountScheduleForm(
        scheduleOn,
        storedSlots,
        !!values.site_discount_enabled,
        true,
        values.site_discount,
        values.global_discount,
      );
      if (storedError) {
        message.error(`时段折算到站点运营时区后无效：${storedError}`);
        return;
      }
      // 按天规划结束时间与时段共用所选时区，落库统一折算为站点运营时区
      const rawScheduleEnd = values.discount_schedule_has_end ? values.discount_schedule_end_at : null;
      const scheduleEndInput = rawScheduleEnd && typeof rawScheduleEnd.format === 'function'
        ? rawScheduleEnd.format('YYYY-MM-DD HH:mm:ss')
        : String(rawScheduleEnd ?? '').trim();
      if (scheduleOn && values.discount_schedule_has_end && (!scheduleEndInput || !dayjs(scheduleEndInput).isValid())) {
        message.error('请选择有效的时段折扣结束时间，或切换为长期有效');
        return;
      }
      const scheduleEndStored = scheduleEndInput
        ? convertWallDateTime(scheduleEndInput, scheduleInputTzName(editTz, operationTz, adminTz), operationTz)
        : '';
      // 限时折扣结束时间按所选时区填写，落库统一折算为站点运营时区
      const limitedTzMode = values.limited_discount_input_tz === 'admin_display' ? 'admin_display' : 'operation';
      const rawLimitedEnd = values.limited_discount_end_at;
      const limitedEndInput = rawLimitedEnd && typeof rawLimitedEnd.format === 'function'
        ? rawLimitedEnd.format('YYYY-MM-DD HH:mm:ss')
        : String(rawLimitedEnd ?? '').trim();
      const limitedEndStored = limitedEndInput
        ? convertWallDateTime(limitedEndInput, scheduleInputTzName(limitedTzMode, operationTz, adminTz), operationTz)
        : '';
      values.discount_schedule = packDiscountSchedule(
        scheduleOn,
        storedSlots,
        editTz,
        {
          enabled: limitedOn,
          isSeedance2,
          discount: values.limited_discount_rate,
          siteDiscount: values.site_discount_enabled ? limitedSiteDiscount : null,
          resolutionDiscounts: filteredResDiscounts,
          hasEnd: !!values.limited_discount_has_end,
          endAt: limitedEndStored,
          editTz: limitedTzMode,
        },
        values.site_discount,
        values.global_discount,
        scheduleEndStored,
      );
      delete values.limited_discount_enabled;
      delete values.limited_discount_rate;
      delete values.limited_site_discount;
      delete values.limited_resolution_discounts;
      delete values.limited_discount_has_end;
      delete values.limited_discount_end_at;
      delete values.limited_discount_input_tz;
      delete values.discount_schedule_enabled;
      delete values.discount_schedule_input_tz;
      delete values.discount_schedule_has_end;
      delete values.discount_schedule_end_at;
      delete values.discount_slots;
      values.site_discount_enabled = values.site_discount_enabled ? 1 : 0;
      values.global_discount_enabled = values.global_discount_enabled ? 1 : 0;
      values.feature_attributes = JSON.stringify(values.feature_attributes || []);
      values.sort_order = typeof values.sort_order === 'number' ? values.sort_order : (Number(values.sort_order) || 0);
      // 模型ID别名映射：开关关闭时清空值
      if (!aliasEnabled) {
        values.model_id_alias = '';
      }

      if (editingModel) {
        if (editingModel.library_kind === 'catalog') {
          await request.put(`/models/library/${editingModel.id}`, values);
        } else {
          await request.put(`/models/${editingModel.id}`, values);
        }
      } else {
        const created = await request.post('/models', values) as ModelModel;
        setRecentlyAddedId(created?.id ?? null);
        setCurrentPage(1);
      }
      message.success(t('common.success'));
      handleCloseEditor();
      fetchModels();
      fetchClassificationsStats();
      fetchAllClassifications();
    } catch (e) {
      console.error(e);
    } finally {
      setSaving(false);
    }
  };

  const handleUpdateRemark = async (id: number, remark: string) => {
    // 乐观更新：本地状态立刻变更，达到瞬时生效且页面绝无闪烁的绝佳体验
    setModels(prev => prev.map(m => m.id === id ? { ...m, remark } : m));
    try {
      await request.put(`/models/${id}`, { remark });
    } catch (e) {
      console.error(e);
      fetchModels();
    }
  };

  const handleUpdateSortOrder = async (id: number, sort_order: number) => {
    // 乐观更新：本地状态立刻变更，瞬时生效
    setModels(prev => prev.map(m => m.id === id ? { ...m, sort_order } : m));
    try {
      await request.put(`/models/${id}`, { sort_order });
    } catch (e) {
      console.error(e);
      fetchModels();
    }
  };

  // ===== 批量选择与编辑状态 Handler =====
  const [isBatchEditMode, setIsBatchEditMode] = useState<boolean>(false);
  const [selectedRowKeys, setSelectedRowKeys] = useState<React.Key[]>([]);
  const [batchLoading, setBatchLoading] = useState<boolean>(false);

  const getSelectedModels = (): ModelModel[] => {
    const keySet = new Set(selectedRowKeys.map(String));
    return models.filter(m => keySet.has(modelRowKey(m)) || keySet.has(String(m.id)));
  };

  const handleBatchUnlist = async () => {
    const targetModels = getSelectedModels();
    if (targetModels.length === 0) return;
    setBatchLoading(true);
    try {
      await Promise.all(
        targetModels.map(m => request.post(`/models/${m.id}/unlist`))
      );
      setSelectedRowKeys([]);
      fetchModels();
      fetchClassificationsStats();
    } catch (e) {
      console.error(e);
      fetchModels();
    } finally {
      setBatchLoading(false);
    }
  };

  const handleBatchPublish = async () => {
    const targetModels = getSelectedModels();
    if (targetModels.length === 0) return;
    setBatchLoading(true);
    try {
      await Promise.all(
        targetModels.map(m => {
          if (m.library_kind === 'unlisted') {
            return request.post(`/models/${m.id}/relist`);
          } else {
            return request.post(`/models/library/${m.id}/publish`);
          }
        })
      );
      setSelectedRowKeys([]);
      fetchModels();
      fetchClassificationsStats();
    } catch (e) {
      console.error(e);
      fetchModels();
    } finally {
      setBatchLoading(false);
    }
  };

  const handleBatchChangeStatus = async (active: number) => {
    const targetModels = getSelectedModels();
    if (targetModels.length === 0) return;
    setBatchLoading(true);
    try {
      await Promise.all(
        targetModels.map(m => {
          const url = m.library_kind === 'catalog' ? `/models/library/${m.id}` : `/models/${m.id}`;
          return request.put(url, { is_active: active });
        })
      );
      message.success(`已成功${active === 1 ? '启用' : '禁用'} ${targetModels.length} 个模型`);
      const targetRowKeySet = new Set(targetModels.map(modelRowKey));
      setModels(prev => prev.map(m => targetRowKeySet.has(modelRowKey(m)) ? { ...m, is_active: active } : m));
      fetchClassificationsStats();
    } catch (e) {
      console.error(e);
      fetchModels();
    } finally {
      setBatchLoading(false);
    }
  };

  const handleBatchChangeLogContent = async (enable: number) => {
    const targetModels = getSelectedModels();
    if (targetModels.length === 0) return;
    setBatchLoading(true);
    try {
      await Promise.all(
        targetModels.map(m => {
          const url = m.library_kind === 'catalog' ? `/models/library/${m.id}` : `/models/${m.id}`;
          return request.put(url, { enable_log_content: enable });
        })
      );
      message.success(`已成功${enable === 1 ? '开启' : '关闭'} ${targetModels.length} 个模型的日志记录`);
      const targetRowKeySet = new Set(targetModels.map(modelRowKey));
      setModels(prev => prev.map(m => targetRowKeySet.has(modelRowKey(m)) ? { ...m, enable_log_content: enable } : m));
    } catch (e) {
      console.error(e);
      fetchModels();
    } finally {
      setBatchLoading(false);
    }
  };

  const handleBatchDelete = async () => {
    const targetModels = getSelectedModels();
    const deletable = isLibrary ? targetModels : targetModels.filter(m => !isFromLibrary(m));
    const kept = targetModels.length - deletable.length;
    if (deletable.length === 0) {
      if (kept > 0) {
        message.warning('来自模型仓库的模型不能删除，请下架');
      }
      return;
    }
    setBatchLoading(true);
    try {
      await Promise.all(
        deletable.map(m => {
          const url = m.library_kind === 'catalog' ? `/models/library/${m.id}` : `/models/${m.id}`;
          return request.delete(url);
        })
      );
      message.success(
        kept > 0
          ? `已删除 ${deletable.length} 个模型，${kept} 个来自模型仓库的模型已保留，请下架`
          : `已成功删除 ${deletable.length} 个模型`
      );
      const targetRowKeySet = new Set(deletable.map(modelRowKey));
      setModels(prev => prev.filter(m => !targetRowKeySet.has(modelRowKey(m))));
      setSelectedRowKeys([]);
      fetchClassificationsStats();
      fetchAllClassifications();
    } catch (e) {
      console.error(e);
      fetchModels();
    } finally {
      setBatchLoading(false);
    }
  };

  const getProviderName = (id?: number) => {
    const p = providersStats.find(p => p.id === id) || allProviders.find(p => p.id === id);
    return p ? localizeClassificationName(p, i18n.language, t) : null;
  };

  const getApiProviderName = (id?: number) => {
    const p = apiProvidersStats.find(p => p.id === id) || allApiProviders.find(p => p.id === id);
    return p ? localizeClassificationName(p, i18n.language, t) : null;
  };

  const getTypeName = (id?: number) => {
    const item = typesStats.find(tp => tp.id === id) || allTypes.find(tp => tp.id === id);
    return item ? localizeClassificationName(item, i18n.language, t) : null;
  };

  const colWidths = isLibrary
    ? [100, 200, 240, 130, 250, 96, 128]
    : [100, 200, 240, 130, 250, 96, 104, 128];
  const currentWidthsRef = useRef([...colWidths]);
  const libraryLayoutRef = useRef(isLibrary);
  if (libraryLayoutRef.current !== isLibrary) {
    libraryLayoutRef.current = isLibrary;
    currentWidthsRef.current = [...colWidths];
  }

  const handleResize = useMemo(() => {
    let animationFrameId: number | null = null;
    return (index: number, newWidth: number) => {
      if (animationFrameId !== null) {
        cancelAnimationFrame(animationFrameId);
      }
      animationFrameId = requestAnimationFrame(() => {
        const minWidth = index === 0 ? 60 : (index === 5 || index === 6) ? 60 : 80;
        const validWidth = Math.max(minWidth, newWidth);
        
        currentWidthsRef.current[index] = validWidth;
        const total = currentWidthsRef.current.reduce((a, b) => a + b, 0);

        if (tableContainerRef.current) {
          tableContainerRef.current.style.setProperty(`--col-width-${index}`, `${validWidth}px`);
          tableContainerRef.current.style.setProperty('--table-scroll-x', `${total}px`);
        }
      });
    };
  }, []);

  const tableComponents = useMemo(() => ({
    header: {
      cell: ResizableHeaderCell,
    },
  }), []);

  // ===== PC端鼠标左键按下拖拽滚动表格功能 =====
  useEffect(() => {
    const container = tableContainerRef.current;
    if (!container) return;

    let isDown = false;
    let startX: number;
    let scrollLeft: number;

    const handleMouseDown = (e: MouseEvent) => {
      // 避开可操作性子项（按钮、表单、复制图标、下拉菜单、拉伸触碰区、备注编辑按钮等）
      const target = e.target as HTMLElement;
      if (
        target.closest('button') || 
        target.closest('a') || 
        target.closest('input') || 
        target.closest('textarea') ||
        target.closest('.ant-typography-copy') ||
        target.closest('.resizable-trigger') ||
        target.closest('.ant-select') ||
        target.closest('.ant-table-filter-trigger') ||
        target.closest('.ant-table-column-sorters') ||
        target.closest('.ant-typography-edit') ||
        target.closest('.ant-typography-editable-single-line') ||
        target.closest('.model-remark-text') ||
        target.closest('.ant-switch') ||
        window.getSelection()?.toString()
      ) {
        return;
      }

      isDown = true;
      startX = e.pageX - container.offsetLeft;
      scrollLeft = container.scrollLeft;
    };

    const handleMouseLeave = () => {
      isDown = false;
    };

    const handleMouseUp = () => {
      isDown = false;
    };

    const handleMouseMove = (e: MouseEvent) => {
      if (!isDown) return;
      e.preventDefault();
      const x = e.pageX - container.offsetLeft;
      const walk = (x - startX) * 1.5; // 滚动速度倍率
      container.scrollLeft = scrollLeft - walk;
    };

    container.addEventListener('mousedown', handleMouseDown);
    container.addEventListener('mouseleave', handleMouseLeave);
    container.addEventListener('mouseup', handleMouseUp);
    container.addEventListener('mousemove', handleMouseMove);

    return () => {
      container.removeEventListener('mousedown', handleMouseDown);
      container.removeEventListener('mouseleave', handleMouseLeave);
      container.removeEventListener('mouseup', handleMouseUp);
      container.removeEventListener('mousemove', handleMouseMove);
    };
  }, [loading]);

  const columns = [
    {
      title: '模型(MID)',
      dataIndex: 'mid',
      key: 'mid',
      width: 100,
      render: (text: string, record: ModelModel) => (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0, maxWidth: '100%' }}>
          <div style={{ display: 'flex', alignItems: 'center', minWidth: 0, maxWidth: '100%' }}>
            <Text 
              className="monochrome-copy" 
              copyable={text ? { text: String(text) } : false} 
              style={{ 
                fontFamily: 'monospace', 
                fontSize: 12, 
                lineHeight: 1.2, 
                color: 'var(--text-secondary, #8c8c8c)',
                display: 'inline-flex',
                alignItems: 'center',
              }}
            >
              {text || '-'}
            </Text>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', minWidth: 0, maxWidth: '100%' }}>
            <Text
              className="model-remark-text"
              type="secondary"
              style={{ 
                fontSize: 12, 
                width: '100%',
                color: record.remark ? undefined : 'var(--text-tertiary, #8c8c8c)',
                cursor: isLibrary ? 'default' : 'pointer',
              }}
              ellipsis={{ tooltip: record.remark || false }}
              editable={isLibrary ? undefined : {
                tooltip: false,
                text: record.remark || '',
                triggerType: ['icon', 'text'],
                onChange: (val) => handleUpdateRemark(record.id, val),
              }}
            >
              {record.remark || (isLibrary ? '-' : '添加备注')}
            </Text>
          </div>
        </div>
      ),
    },
    {
      title: t('models.model_name'),
      dataIndex: 'name',
      key: 'name',
      width: 200,
      filterDropdown: ({ setSelectedKeys, confirm }: any) => (
        <div style={{ padding: 8 }}>
          <Radio.Group 
            onChange={(e) => {
              setRecentlyAddedId(null);
              setSortType(e.target.value);
              setSelectedKeys([e.target.value]);
              confirm();
            }} 
            value={sortType}
            style={{ display: 'flex', flexDirection: 'column', gap: 8 }}
          >
            <Radio value="order_desc">按排序权重 (从大到小)</Radio>
            <Radio value="name_asc">按名称 (A-Z)</Radio>
            <Radio value="name_desc">按名称 (Z-A)</Radio>
            <Radio value="time_desc">按添加时间 (最新)</Radio>
            <Radio value="time_asc">按添加时间 (最早)</Radio>
          </Radio.Group>
        </div>
      ),
      filterIcon: () => (
        <FilterOutlined style={{ color: sortType !== 'order_desc' ? '#1677ff' : undefined }} />
      ),
      render: (text: string, record: ModelModel) => {
        const providerName = record.provider_id ? getProviderName(record.provider_id) : null;
        const apiProviderName = record.api_provider_id ? getApiProviderName(record.api_provider_id) : null;
        const typeName = record.type_id ? getTypeName(record.type_id) : null;
        const parts = [providerName, apiProviderName, typeName].filter(Boolean);

        return (
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8 }}>
            {renderModelLogo(record.logo, text || record.model_id)}
            <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0, flex: 1 }}>
              <span 
                title={text}
                onClick={() => handleEdit(record)}
                style={{ fontWeight: 600, color: 'var(--text-color, inherit)', fontSize: 13, lineHeight: 1.2, wordBreak: 'break-all', cursor: 'pointer' }}
              >
                {text}
              </span>
              {!isLibrary && record.library_mid && (
                <Tag color="blue" style={{ margin: 0, fontSize: 10, lineHeight: '16px', padding: '0 5px', width: 'fit-content' }}>
                  {t('models.from_library')}
                </Tag>
              )}
              <div style={{ display: 'flex', alignItems: 'center', gap: 4, fontSize: 11, lineHeight: 1.2, color: 'var(--text-secondary, #8c8c8c)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {parts.map((part, idx) => (
                  <React.Fragment key={idx}>
                    {idx > 0 && <span style={{ opacity: 0.3 }}>•</span>}
                    <span>{part}</span>
                  </React.Fragment>
                ))}
              </div>
            </div>
          </div>
        );
      },
    },
    {
      title: t('models.model_id'),
      dataIndex: 'model_id',
      key: 'model_id',
      width: 240,
      render: (text: string, record: ModelModel) => {
        let rules: { name: string; eid?: string }[] = [];
        try {
          if (record.forward_rule_ids) {
            const ruleIds = JSON.parse(record.forward_rule_ids);
            if (Array.isArray(ruleIds)) {
              rules = ruleIds.map((id: number) => {
                const r = allForwardRules.find(rule => rule.id === id);
                return r ? { name: r.name, eid: r.eid } : null;
              }).filter(Boolean) as { name: string; eid?: string }[];
            }
          }
        } catch (e) { }

        return (
          <Space direction="vertical" size={2} style={{ display: 'flex', width: '100%', overflow: 'hidden' }}>
            <div style={{ display: 'block', width: '100%', wordBreak: 'break-all', whiteSpace: 'normal' }}>
              <Text 
                className="monochrome-copy" 
                copyable={{ text: text }} 
                style={{ 
                  fontFamily: 'var(--font-family, Inter, sans-serif)', 
                  fontSize: 13, 
                  lineHeight: 1.2, 
                  color: 'var(--text-color, inherit)',
                  wordBreak: 'break-all',
                  whiteSpace: 'normal',
                  display: 'inline'
                }}
              >
                {text}
              </Text>
            </div>
            {rules.length > 0 && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 2, width: '100%' }}>
                {rules.map((rule, idx) => (
                  <div key={idx} style={{ display: 'flex', flexDirection: 'column', gap: 2, width: '100%' }}>
                    <div style={{ 
                      display: 'flex', alignItems: 'center', fontSize: 11, lineHeight: 1.2, color: 'var(--text-secondary, #595959)', 
                      width: 'fit-content', maxWidth: '100%'
                    }}>
                      <span style={{ flexShrink: 0 }}>已挂载: </span>
                      <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', margin: '0 4px' }} title={rule.name}>
                        {rule.name}
                      </span>
                    </div>
                    {rule.eid && (
                      <div style={{ fontSize: 11, lineHeight: 1.2, color: 'var(--text-secondary, #8c8c8c)' }}>
                        EID: <span style={{ fontFamily: 'monospace' }}>{rule.eid}</span>
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
            {record.model_id_alias && (
              <div style={{ fontSize: 11, lineHeight: 1.2, color: 'var(--text-secondary, #8c8c8c)' }}>
                别名: <span style={{ fontFamily: 'monospace' }}>{record.model_id_alias}</span>
              </div>
            )}
          </Space>
        );
      },
    },
    {
      title: t('models.billing_type'),
      key: 'billing_type',
      width: 130,
      filterDropdown: ({ setSelectedKeys, confirm }: any) => (
        <div style={{ padding: 8 }}>
          <Radio.Group 
            onChange={(e) => {
              setTableBillingTypeFilter(e.target.value);
              setSelectedKeys([e.target.value]);
              confirm();
            }} 
            value={tableBillingTypeFilter}
            style={{ display: 'flex', flexDirection: 'column', gap: 8 }}
          >
            <Radio value="all">全部</Radio>
            <Radio value="tokens">按量 (Tokens)</Radio>
            <Radio value="requests">按次 (Requests)</Radio>
            <Radio value="duration">按时 (Duration)</Radio>
          </Radio.Group>
        </div>
      ),
      filterIcon: () => (
        <FilterOutlined style={{ color: tableBillingTypeFilter !== 'all' ? '#1677ff' : undefined }} />
      ),
      render: (_: any, record: ModelModel) => {
        const br = allBillingRules.find(b => b.id === record.billing_rule_id);
        const type = br ? br.billing_type : 'tokens';
        return (
          <Space direction="vertical" size={0} style={{ width: '100%', overflow: 'hidden' }}>
            <span style={{ fontSize: '11px', lineHeight: 1.2, margin: 0, color: 'var(--text-secondary, #595959)' }}>{t(`models.type_${type}`)}</span>
            {br && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 0, width: '100%', overflow: 'hidden' }}>
                <a
                  href={billingRulesEditPath(adminPath, br.pid || br.id)}
                  target="_blank"
                  rel="noreferrer"
                  title={br.name}
                  style={{
                    fontSize: '11px',
                    lineHeight: 1.2,
                    color: 'var(--text-secondary, #8c8c8c)',
                    textDecoration: 'underline',
                    display: 'block',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    maxWidth: '100%'
                  }}
                >
                  {br.name}
                </a>
                {br.pid && <Text type="secondary" style={{ fontSize: '11px', lineHeight: 1.2 }}>PID: {br.pid}</Text>}
              </div>
            )}
          </Space>
        );
      },
    },
    {
      title: t('models.rates'),
      key: 'rates',
      width: 250,
      render: (_: any, record: ModelModel) => {
        const br = allBillingRules.find((b: any) => b.id === record.billing_rule_id);
        if (!br) return <Text type="secondary" italic>未挂载费用模板</Text>;

        return (
          <Space direction="vertical" size={0}>
            <RateDisplay rule={br} currencySymbol={currencySymbol} formatPrice={formatPrice} />
            {(record.pre_deduction ?? 0) > 0 && (
              <Text style={{ fontSize: '11px', lineHeight: 1.2, color: 'var(--text-secondary, #8c8c8c)' }}>
                预扣: {formatPrice(record.pre_deduction)}{record.type_id && getTypeName(record.type_id)?.includes('视频') ? '/秒 (按秒预扣)' : ''}
              </Text>
            )}
            {(record.site_discount_enabled === 1 || record.global_discount_enabled === 1 || discountScheduleOn(record.discount_schedule) || limitedDiscountOn(record.discount_schedule)) && (
              <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 6 }}>
                {record.site_discount_enabled === 1 && (
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 2, padding: '0 4px', borderRadius: 3, fontSize: 10, background: 'rgba(250, 84, 28, 0.08)', color: '#fa541c', border: '1px solid rgba(250, 84, 28, 0.2)', lineHeight: '16px' }}>
                    {`折扣限价 ${formatRate(Number(record.site_discount || 1))} 倍率`}
                  </span>
                )}
                {record.global_discount_enabled === 1 && (
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 2, padding: '0 4px', borderRadius: 3, fontSize: 10, background: 'rgba(114, 46, 209, 0.08)', color: '#722ed1', border: '1px solid rgba(114, 46, 209, 0.2)', lineHeight: '16px' }}>
                    {`全站折扣 ${formatRate(Number(record.global_discount || 1))} 倍率`}
                  </span>
                )}
                {record.global_discount_enabled === 1 && limitedDiscountOn(record.discount_schedule) && (() => {
                  const lim = readDiscountSchedule(record.discount_schedule).limited;
                  const resKeys = sortResolutionKeys(Object.keys(lim.resolution_discounts || {}));
                  const baseDesc = resKeys.length > 0
                    ? resKeys.map((k) => `${k}:${formatRate(Number(lim.resolution_discounts[k]))}`).join(' / ')
                    : (lim.discount != null ? `${formatRate(Number(lim.discount))} 倍率` : '');
                  const desc = record.site_discount_enabled === 1 && lim.site_discount != null
                    ? `${baseDesc}${baseDesc ? ' · ' : ''}限价 ${lim.site_discount === 0 ? '免费' : formatRate(lim.site_discount)}`
                    : baseDesc;
                  const isExpired = isLimitedDiscountExpired(record.discount_schedule, operationTz);
                  const endText = lim.end_at_raw ? (isExpired ? `已于 ${lim.end_at_raw} 到期` : `截止 ${lim.end_at_raw}`) : '长期有效';
                  return (
                    <Tooltip title={isExpired ? `限时折扣已到期 (${endText})，当前已恢复全站折扣倍率` : `已启用限时折扣 (${desc ? `${desc} · ` : ''}${endText})`}>
                      <span style={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: 2,
                        padding: '0 4px',
                        borderRadius: 3,
                        fontSize: 10,
                        background: isExpired ? 'rgba(140, 140, 140, 0.08)' : 'rgba(235, 47, 150, 0.08)',
                        color: isExpired ? '#8c8c8c' : '#eb2f96',
                        border: isExpired ? '1px solid rgba(140, 140, 140, 0.25)' : '1px solid rgba(235, 47, 150, 0.25)',
                        lineHeight: '16px',
                        cursor: 'default',
                      }}>
                        {isExpired ? '限时折扣已到期' : '限时折扣'}
                      </span>
                    </Tooltip>
                  );
                })()}
                {record.global_discount_enabled === 1 && discountScheduleOn(record.discount_schedule) && (() => {
                  const expired = isDiscountScheduleExpired(record.discount_schedule, operationTz);
                  const endRaw = readDiscountSchedule(record.discount_schedule).end_at_raw;
                  const endText = endRaw ? (expired ? `已于 ${endRaw} 到期` : `截止 ${endRaw}`) : '长期有效';
                  return (
                    <Tooltip title={expired ? `分时段折扣已到期 (${endText})，当前已恢复全站折扣倍率` : `已启用模型分时段折扣规划 (${endText})`}>
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 2, padding: '0 4px', borderRadius: 3, fontSize: 10, background: expired ? 'rgba(140, 140, 140, 0.08)' : 'rgba(19, 194, 194, 0.08)', color: expired ? '#8c8c8c' : '#13c2c2', border: expired ? '1px solid rgba(140, 140, 140, 0.25)' : '1px solid rgba(19, 194, 194, 0.25)', lineHeight: '16px', cursor: 'default' }}>
                        {expired ? '分时段折扣已到期' : '分时段折扣'}
                      </span>
                    </Tooltip>
                  );
                })()}
              </div>
            )}
          </Space>
        );
      }
    },
    {
      title: '页面排序',
      dataIndex: 'sort_order',
      key: 'sort_order',
      width: 96,
      align: 'center' as const,
      sorter: true,
      sortDirections: ['descend', 'ascend'] as ('descend' | 'ascend')[],
      sortOrder: sortType === 'order_desc' ? ('descend' as const) : sortType === 'order_asc' ? ('ascend' as const) : null,
      render: (val: number, record: ModelModel) => {
        const order = record.sort_order ?? 0;
        return (
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            <Text
              editable={isLibrary ? undefined : {
                tooltip: false,
                text: String(order),
                onChange: (newVal) => {
                  const num = parseInt(newVal.trim(), 10);
                  handleUpdateSortOrder(record.id, isNaN(num) ? 0 : num);
                },
              }}
              style={{
                fontFamily: 'monospace',
                fontSize: 12,
                fontWeight: 600,
                padding: '2px 8px',
                borderRadius: 6,
                background: order > 0 ? 'rgba(59, 130, 246, 0.08)' : 'rgba(128, 128, 128, 0.06)',
                color: order > 0 ? '#2563eb' : 'var(--text-secondary, #8c8c8c)',
                border: order > 0 ? '1px solid rgba(59, 130, 246, 0.2)' : '1px solid rgba(128, 128, 128, 0.15)',
                cursor: 'pointer',
                transition: 'all 0.2s',
              }}
            >
              {order}
            </Text>
          </div>
        );
      },
    },
    ...(isLibrary ? [] : [{
      title: '模型状态',
      dataIndex: 'is_active',
      key: 'is_active',
      width: 104,
      filterDropdown: ({ setSelectedKeys, confirm }: any) => (
        <div style={{ padding: 8 }}>
          <Radio.Group 
            onChange={(e) => {
              setTableStatusFilter(e.target.value);
              setSelectedKeys([e.target.value]);
              confirm();
            }} 
            value={tableStatusFilter}
            style={{ display: 'flex', flexDirection: 'column', gap: 8 }}
          >
            <Radio value="all">全部</Radio>
            <Radio value="active">激活</Radio>
            <Radio value="disabled">禁用</Radio>
          </Radio.Group>
        </div>
      ),
      filterIcon: () => (
        <FilterOutlined style={{ color: tableStatusFilter !== 'all' ? '#1677ff' : undefined }} />
      ),
      render: (active: boolean, record: ModelModel) => (
        <Space direction="vertical" size={4} style={{ display: 'flex', alignItems: 'flex-start' }}>
          <span style={{
            display: 'inline-flex', alignItems: 'center', gap: 4,
            padding: '2px 6px', borderRadius: 4, fontSize: 12,
            background: active ? 'rgba(128,128,128,0.06)' : 'transparent',
            color: active ? 'var(--text-color, inherit)' : '#8c8c8c',
            border: active ? '1px solid rgba(128,128,128,0.15)' : '1px dashed rgba(128,128,128,0.3)'
          }}>
            <span style={{ width: 6, height: 6, borderRadius: '50%', background: active ? '#8c8c8c' : 'transparent', border: active ? 'none' : '1px solid #8c8c8c' }} />
            {active ? '激活' : '禁用'}
          </span>
          {record.enable_log_content === 1 && (
            <span style={{
              display: 'inline-flex', alignItems: 'center',
              padding: '1px 6px', borderRadius: 4, fontSize: 10,
              background: 'rgba(22,119,255,0.08)',
              color: '#1677ff',
              border: '1px solid rgba(22,119,255,0.2)',
              fontWeight: 500,
              lineHeight: 1.2
            }}>
              上下文
            </span>
          )}
        </Space>
      ),
    }]),
    {
      title: t('common.actions'),
      key: 'actions',
      width: 168,
      render: (_: unknown, record: ModelModel) => (
        <Space size={4}>
          {isLibrary ? (
            <>
              <Button icon={<EditOutlined />} onClick={() => handleEdit(record)} />
              <Tooltip title={t('models.copy_model')}>
                <Button icon={<CopyOutlined />} onClick={() => openCopyModal(record)} />
              </Tooltip>
              <Tooltip title={t('models.publish_model')}>
                <Button
                  icon={<CloudUploadOutlined />}
                  loading={publishingId === modelRowKey(record)}
                  onClick={() => handlePublish(record)}
                />
              </Tooltip>
              <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record)}>
                <Button icon={<DeleteOutlined />} danger />
              </Popconfirm>
            </>
          ) : (
            <>
              <Button icon={<EditOutlined />} onClick={() => handleEdit(record)} />
              <Tooltip title={t('models.copy_model')}>
                <Button icon={<CopyOutlined />} onClick={() => openCopyModal(record)} />
              </Tooltip>
              <Tooltip title={t('models.unlist_model')}>
                <Button
                  icon={<CloudDownloadOutlined />}
                  loading={publishingId === modelRowKey(record)}
                  onClick={() => handleUnlist(record)}
                />
              </Tooltip>
              {!isFromLibrary(record) && (
                <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record)}>
                  <Button icon={<DeleteOutlined />} danger />
                </Popconfirm>
              )}
            </>
          )}
        </Space>
      ),
    },
  ];

  const resizableColumns = useMemo(() => {
    return columns.map((col: any, idx) => {
      const minWidth = idx === 0 ? 60 : (idx === 5 || idx === 6) ? 60 : 80;
      const colClassName = `col-idx-${idx}`;
      const existingClassName = col.className || '';
      const combinedClassName = existingClassName ? `${existingClassName} ${colClassName}` : colClassName;

      const baseCol = {
        ...col,
        className: combinedClassName,
        width: `var(--col-width-${idx}, ${colWidths[idx]}px)` as any,
        onCell: () => ({
          className: combinedClassName,
        }),
      };

      if (col.key === 'actions') {
        return baseCol;
      }
      return {
        ...baseCol,
        onHeaderCell: (column: any) => ({
          width: colWidths[idx],
          minWidth,
          className: combinedClassName,
          onResize: (e: any, { size }: any) => handleResize(idx, size.width),
        }),
      };
    });
  }, [colWidths, columns, handleResize]);

  const sortedModels = useMemo(() => {
    let list = [...models];

    const keyword = searchKeyword.trim();
    if (keyword) {
      const isGmid = keyword.length === 6 && keyword.startsWith('5') && /^\d+$/.test(keyword);
      if (isGmid) {
        list = list.filter(m => getGmid(m.original_id || '') === keyword);
      } else {
        list = list.filter(m => modelMatchesKeyword(m, keyword));
      }
    }

    if (tableBillingTypeFilter !== 'all') {
      list = list.filter(m => {
        const br = allBillingRules.find(b => b.id === m.billing_rule_id);
        const type = br ? br.billing_type : 'tokens';
        return type === tableBillingTypeFilter;
      });
    }

    if (!isLibrary && tableStatusFilter !== 'all') {
      const active = tableStatusFilter === 'active' ? 1 : 0;
      list = list.filter(m => m.is_active === active);
    }

    list.sort((a, b) => {
      if (recentlyAddedId != null) {
        if (a.id === recentlyAddedId) return -1;
        if (b.id === recentlyAddedId) return 1;
      }
      if (sortType === 'order_desc') {
        const diff = (b.sort_order ?? 0) - (a.sort_order ?? 0);
        if (diff !== 0) return diff;
        return b.id - a.id;
      }
      if (sortType === 'order_asc') {
        const diff = (a.sort_order ?? 0) - (b.sort_order ?? 0);
        if (diff !== 0) return diff;
        return a.id - b.id;
      }
      if (sortType === 'name_desc') {
        return (b.name || '').localeCompare(a.name || '', 'zh', { sensitivity: 'base', numeric: true });
      }
      if (sortType === 'name_asc') {
        return (a.name || '').localeCompare(b.name || '', 'zh', { sensitivity: 'base', numeric: true });
      }
      if (sortType === 'time_asc') {
        return a.id - b.id;
      }
      if (sortType === 'time_desc') {
        return b.id - a.id;
      }
      const diff = (b.sort_order ?? 0) - (a.sort_order ?? 0);
      if (diff !== 0) return diff;
      return b.id - a.id;
    });
    return list;
  }, [models, searchKeyword, sortType, tableBillingTypeFilter, tableStatusFilter, allBillingRules, recentlyAddedId, isLibrary]);

  // ===== 获取所有真实模型组 =====
  const allModelGroups = useMemo(() => {
    const map = new Map<string, ModelModel[]>();
    const order: string[] = [];
    sortedModels.forEach(m => {
      if (m.original_id && m.original_id.trim() !== '') {
        const key = m.original_id;
        if (!map.has(key)) {
          order.push(key);
          map.set(key, []);
        }
        map.get(key)!.push(m);
      }
    });
    return order
      .map(key => ({
        key,
        model_id: key,
        children: map.get(key)!,
        count: map.get(key)!.length,
        type: 'group' as const,
      }))
      .filter(g => g.count > 1);
  }, [sortedModels]);

  // ===== 获取总条数（用于分页） =====
  const totalCount = useMemo(() => {
    return showGroupsOnly ? allModelGroups.length : sortedModels.length;
  }, [showGroupsOnly, allModelGroups.length, sortedModels.length]);

  // ===== 扁平模型分页切片 =====
  const pagedModels = useMemo(() => {
    if (showGroupsOnly) return [];
    const startIndex = (currentPage - 1) * pageSize;
    return sortedModels.slice(startIndex, startIndex + pageSize);
  }, [sortedModels, currentPage, pageSize, showGroupsOnly]);

  // ===== 按 original_id / model_id 分组逻辑 =====
  const displayGroups = useMemo(() => {
    if (showGroupsOnly) {
      const startIndex = (currentPage - 1) * pageSize;
      return allModelGroups.slice(startIndex, startIndex + pageSize);
    }

    const map = new Map<string, ModelModel[]>();
    const order: string[] = [];
    pagedModels.forEach(m => {
      const key = m.original_id || m.model_id || `__no_id_${m.id}`;
      if (!map.has(key)) {
        order.push(key);
        map.set(key, []);
      }
      map.get(key)!.push(m);
    });
    const groupedData = order.map(key => ({
      key,
      model_id: key.startsWith('__no_id_') ? '' : key,
      children: map.get(key)!,
      count: map.get(key)!.length,
    }));

    const result: any[] = [];
    let currentSingles: any[] = [];
    groupedData.forEach(g => {
      if (g.count === 1) {
        currentSingles.push(g.children[0]);
      } else {
        if (currentSingles.length > 0) {
          result.push({ type: 'singles', children: currentSingles });
          currentSingles = [];
        }
        result.push({ type: 'group', ...g });
      }
    });
    if (currentSingles.length > 0) {
      result.push({ type: 'singles', children: currentSingles });
    }
    return result;
  }, [pagedModels, showGroupsOnly, allModelGroups, currentPage, pageSize]);

  // ===== 当前页模型 ID 集合（全选时仅全选当前页） =====
  const currentPageModelIds = useMemo(() => {
    if (showGroupsOnly) {
      const startIndex = (currentPage - 1) * pageSize;
      const pagedGroups = allModelGroups.slice(startIndex, startIndex + pageSize);
      return pagedGroups.flatMap(g => g.children.map(m => modelRowKey(m)));
    }
    return pagedModels.map(m => modelRowKey(m));
  }, [showGroupsOnly, currentPage, pageSize, allModelGroups, pagedModels]);

  useEffect(() => {
    setCurrentPage(1);
  }, [tableBillingTypeFilter, tableStatusFilter, showGroupsOnly]);

  const hasActiveFilters =
    selectedProvider !== null ||
    selectedApiProvider !== null ||
    selectedType !== null ||
    searchKeyword.trim() !== '' ||
    tableBillingTypeFilter !== 'all' ||
    (!isLibrary && tableStatusFilter !== 'all');

  const resetAllFilters = () => {
    setSelectedProvider(null);
    setSelectedApiProvider(null);
    setSelectedType(null);
    setSearchKeyword('');
    setTableBillingTypeFilter('all');
    setTableStatusFilter('all');
    setCurrentPage(1);
  };

  const handleHeaderTableChange = (_pagination: any, _filters: any, sorter: any) => {
    if (sorter && sorter.columnKey === 'sort_order') {
      setRecentlyAddedId(null);
      setSortType(sorter.order === 'ascend' ? 'order_asc' : 'order_desc');
    }
  };

  // 防御性校准：当数据被删除或过滤导致总页数变小，且 currentPage 大于最大页数时，自动校准页码
  useEffect(() => {
    const maxPage = Math.ceil(totalCount / pageSize);
    if (maxPage > 0 && currentPage > maxPage) {
      setCurrentPage(maxPage);
    }
  }, [totalCount, pageSize, currentPage]);

  return (
    <Card variant="borderless" style={{ width: '100%', minWidth: 0 }}>
      <style>{`
        .monochrome-copy .ant-typography-copy {
          color: var(--text-secondary, #8c8c8c) !important;
          transition: color 0.3s;
        }
        .monochrome-copy .ant-typography-copy:hover {
          color: ${isLight ? '#262626' : '#d9d9d9'} !important;
        }
        .header-only-table .ant-table-tbody {
          display: none !important;
        }
        .header-only-table .ant-table-placeholder {
          display: none !important;
        }
        
        /* 让 table 总是充满 100% 宽度，但通过 min-width 强制阻挡在各列最小拉伸宽度之和 */
        .compact-table .ant-table table {
          width: 100% !important;
          min-width: var(--table-scroll-x, 1170px) !important;
          table-layout: fixed !important;
        }
        
        /* 强制每一列的宽度使用对应的 CSS 变量，确保即使内容较长也会被剪切/折行，而非撑开 */
        .compact-table .col-idx-0 {
          width: var(--col-width-0, 100px) !important;
          min-width: var(--col-width-0, 100px) !important;
          max-width: var(--col-width-0, 100px) !important;
          overflow: hidden !important;
        }
        .compact-table .col-idx-1 {
          width: var(--col-width-1, 200px) !important;
          min-width: var(--col-width-1, 200px) !important;
          max-width: var(--col-width-1, 200px) !important;
          overflow: hidden !important;
        }
        .compact-table .col-idx-2 {
          width: var(--col-width-2, 240px) !important;
          min-width: var(--col-width-2, 240px) !important;
          max-width: var(--col-width-2, 240px) !important;
          overflow: hidden !important;
        }
        .compact-table .col-idx-3 {
          width: var(--col-width-3, 130px) !important;
          min-width: var(--col-width-3, 130px) !important;
          max-width: var(--col-width-3, 130px) !important;
          overflow: hidden !important;
        }
        .compact-table .col-idx-4 {
          width: var(--col-width-4, 250px) !important;
          min-width: var(--col-width-4, 250px) !important;
          overflow: hidden !important;
        }
        .compact-table .col-idx-5 {
          width: var(--col-width-5, 96px) !important;
          min-width: var(--col-width-5, 96px) !important;
          max-width: var(--col-width-5, 96px) !important;
          overflow: hidden !important;
        }
        .compact-table .col-idx-6 {
          width: var(--col-width-6, 104px) !important;
          min-width: var(--col-width-6, 104px) !important;
          max-width: var(--col-width-6, 104px) !important;
          overflow: hidden !important;
        }
        .compact-table .col-idx-7 {
          width: var(--col-width-7, 128px) !important;
          min-width: var(--col-width-7, 128px) !important;
          max-width: var(--col-width-7, 128px) !important;
          overflow: hidden !important;
        }
        

        
        /* 拖拽指示条 hover 高亮效果：只显示单条右侧蓝色竖线，解决双竖线显示冗余问题 */
        .resizable-trigger {
          transition: background-color 0.2s;
        }
        /* 针对 PC 端滚动容器，强制美化并永久显现水平滚动条 */
        .table-scroll-container::-webkit-scrollbar {
          height: 8px !important;
          background-color: transparent;
        }
        .table-scroll-container::-webkit-scrollbar-thumb {
          background-color: ${isLight ? 'rgba(0,0,0,0.18)' : 'rgba(255,255,255,0.18)'} !important;
          border-radius: 4px !important;
        }
        .table-scroll-container::-webkit-scrollbar-thumb:hover {
          background-color: ${isLight ? 'rgba(0,0,0,0.3)' : 'rgba(255,255,255,0.3)'} !important;
        }
        .table-scroll-container::-webkit-scrollbar-track {
          background-color: transparent;
        }
      `}</style>
      {!isEditorOpen ? (
        <>
        <div style={{ display: 'flex', flexDirection: screens.xs ? 'column' : 'row', justifyContent: 'space-between', marginBottom: 12, gap: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap' }}>
            <Title level={4} style={{ margin: 0, fontSize: screens.xs ? 18 : 20, fontWeight: 600 }}>{t('models.title')}</Title>
            <Space size={8} wrap align="center">
              <Text type="secondary" style={{ fontSize: 13, whiteSpace: 'nowrap' }}>{t('models.source_category')}</Text>
              <Segmented
                value={sourceFilter}
                onChange={(val) => {
                  const next = val as ModelSourceFilter;
                  setSourceFilter(next);
                  setModels([]);
                  setIsBatchEditMode(false);
                  setSelectedRowKeys([]);
                  setTableStatusFilter('all');
                  setSearchParams(next === 'library' ? { source: 'library' } : {}, { replace: true });
                }}
                options={[
                  {
                    label: (
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                        {t('models.source_custom')}
                        {sourceCounts && (
                          <span style={{ opacity: 0.65, fontSize: 12, fontVariantNumeric: 'tabular-nums' }}>{sourceCounts.custom}</span>
                        )}
                      </span>
                    ),
                    value: 'custom',
                  },
                  {
                    label: (
                      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                        {t('models.source_system')}
                        {sourceCounts && (
                          <span style={{ opacity: 0.65, fontSize: 12, fontVariantNumeric: 'tabular-nums' }}>{sourceCounts.library}</span>
                        )}
                      </span>
                    ),
                    value: 'library',
                  },
                ]}
              />
            </Space>
          </div>
          <Space wrap size={6}>
            <Input
              prefix={<SearchOutlined />}
              placeholder="搜索模型名 / Model ID / MID / 备注"
              allowClear
              value={searchKeyword}
              onChange={e => setSearchKeyword(e.target.value)}
              style={{ width: 240 }}
            />
            {hasActiveFilters && (
              <Button icon={<CloseOutlined />} onClick={resetAllFilters}>
                {t('models.reset_filters', '重置筛选')}
              </Button>
            )}
            {auxiliaryCurrencies.length > 0 && (
              <Select 
                value={selectedCurrencyCode} 
                onChange={setSelectedCurrencyCode}
                style={{ width: 140 }}
                options={[
                  { value: '', label: `默认货币 (${currencySymbol})` },
                  ...auxiliaryCurrencies.map(c => ({ value: c.code, label: `${c.code} (${c.symbol})` }))
                ]}
              />
            )}
            <Button
              icon={showGroupsOnly ? <UnorderedListOutlined /> : <FolderOutlined />}
              onClick={() => setShowGroupsOnly(!showGroupsOnly)}
            >
              {showGroupsOnly ? "显示全部模型" : "显示模型组"}
            </Button>
            <Button
              icon={<CheckSquareOutlined />}
              type={isBatchEditMode ? 'primary' : 'default'}
              danger={isBatchEditMode}
              onClick={() => {
                setIsBatchEditMode(!isBatchEditMode);
                if (isBatchEditMode) setSelectedRowKeys([]);
              }}
            >
              {isBatchEditMode ? "退出选择" : "选择编辑"}
            </Button>
            {!isLibrary && (
              <Button type="primary" icon={<PlusOutlined />} onClick={handleAdd}>{t('models.add_model')}</Button>
            )}
          </Space>
        </div>
        {isLibrary && (
          <Text type="secondary" style={{ display: 'block', marginBottom: 12, fontSize: 13 }}>
            {t('models.library_hint')}
          </Text>
        )}

      <ClassificationFilter
        providers={providersStats}
        apiProviders={apiProvidersStats}
        types={typesStats}
        selectedProvider={selectedProvider}
        selectedApiProvider={selectedApiProvider}
        selectedType={selectedType}
        onProviderChange={setSelectedProvider}
        onApiProviderChange={setSelectedApiProvider}
        onTypeChange={setSelectedType}
        onManageProviders={() => setIsProviderManagerVisible(true)}
        onManageApiProviders={() => setIsApiProviderManagerVisible(true)}
        onManageTypes={() => setIsTypeManagerVisible(true)}
      />

      {isBatchEditMode && (
        <div style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          flexWrap: 'wrap',
          gap: 8,
          padding: '8px 14px',
          marginBottom: 12,
          borderRadius: 8,
          backgroundColor: isLight ? '#e6f4ff' : '#111b26',
          border: isLight ? '1px solid #91caff' : '1px solid #154173',
          transition: 'all 0.3s ease'
        }}>
          <Space wrap size={10} style={{ alignItems: 'center' }}>
            <Button
              size="small"
              onClick={() => {
                setSelectedRowKeys(prev => Array.from(new Set([...prev, ...currentPageModelIds])));
              }}
            >
              全选
            </Button>
            <Button
              size="small"
              disabled={selectedRowKeys.length === 0}
              onClick={() => setSelectedRowKeys([])}
            >
              取消选择
            </Button>
            <Text strong style={{ fontSize: 13, color: isLight ? '#0958d9' : '#1677ff', marginLeft: 4 }}>
              已选择 <span style={{ fontSize: 15, fontWeight: 700 }}>{selectedRowKeys.length}</span> 项
            </Text>
          </Space>

          <Space wrap size={8}>
            <Button
              size="small"
              type="primary"
              icon={<CheckCircleOutlined />}
              disabled={selectedRowKeys.length === 0}
              loading={batchLoading}
              onClick={() => handleBatchChangeStatus(1)}
            >
              模型激活
            </Button>
            <Button
              size="small"
              icon={<StopOutlined />}
              disabled={selectedRowKeys.length === 0}
              loading={batchLoading}
              onClick={() => handleBatchChangeStatus(0)}
            >
              模型禁用
            </Button>
            <Button
              size="small"
              icon={<FileTextOutlined />}
              disabled={selectedRowKeys.length === 0}
              loading={batchLoading}
              onClick={() => handleBatchChangeLogContent(1)}
            >
              上下文开启
            </Button>
            <Button
              size="small"
              icon={<FileOutlined />}
              disabled={selectedRowKeys.length === 0}
              loading={batchLoading}
              onClick={() => handleBatchChangeLogContent(0)}
            >
              上下文关闭
            </Button>
            {isLibrary ? (
              <Button
                size="small"
                type="primary"
                icon={<CloudUploadOutlined />}
                disabled={selectedRowKeys.length === 0}
                loading={batchLoading}
                onClick={handleBatchPublish}
              >
                上架模型
              </Button>
            ) : (
              <Button
                size="small"
                icon={<CloudDownloadOutlined />}
                disabled={selectedRowKeys.length === 0}
                loading={batchLoading}
                onClick={handleBatchUnlist}
              >
                下架模型
              </Button>
            )}
            <Popconfirm
              title="确定要批量删除选中的模型吗？"
              description={`将一次性删除选中的 ${selectedRowKeys.length} 个模型，此操作不可撤销。`}
              onConfirm={handleBatchDelete}
              okText="确认删除"
              cancelText="取消"
              okButtonProps={{ danger: true, loading: batchLoading }}
              disabled={selectedRowKeys.length === 0}
            >
              <Button
                size="small"
                danger
                type="primary"
                icon={<DeleteOutlined />}
                disabled={selectedRowKeys.length === 0}
                loading={batchLoading}
              >
                批量删除
              </Button>
            </Popconfirm>
          </Space>
        </div>
      )}

      {screens.xs ? (
        <MobileCardList
          dataSource={pagedModels}
          loading={loading}
          rowKey={modelRowKey}
          pagination={listPagination({
            current: currentPage,
            pageSize,
            total: totalCount,
            onChange,
          })}
          renderCard={(record: any) => {
            const br = allBillingRules.find((b: any) => b.id === record.billing_rule_id);
            const billingTypeVal = br ? br.billing_type : 'tokens';
            const colors: Record<string, string> = { tokens: 'cyan', requests: 'orange', duration: 'purple' };
            return (
              <MobileCard
                title={
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    {isBatchEditMode && (
                      <Checkbox
                        checked={selectedRowKeys.includes(modelRowKey(record))}
                        onChange={(e) => {
                          const key = modelRowKey(record);
                          if (e.target.checked) {
                            setSelectedRowKeys(prev => [...prev, key]);
                          } else {
                            setSelectedRowKeys(prev => prev.filter(k => k !== key));
                          }
                        }}
                      />
                    )}
                    {record.logo && (
                      <SmartSvgIcon src={`/assets/icons/lobe/${record.logo}.svg`} alt="" style={{ width: 24, height: 24, objectFit: 'contain', borderRadius: 4 }} onError={e => { (e.target as HTMLImageElement).style.display = 'none'; }} />
                    )}
                    <div>
                      <Text strong>{record.name}</Text>
                      <div style={{ marginTop: 4 }}>
                        {record.provider_id && <Tag color="default" style={{ fontSize: 10 }}>{getProviderName(record.provider_id)}</Tag>}
                        {record.type_id && <Tag color="blue" style={{ fontSize: 10 }}>{getTypeName(record.type_id)}</Tag>}
                      </div>
                    </div>
                  </div>
                }
                extra={
                  <Space size={4}>
                    {!isLibrary && record.library_mid && <Tag color="blue">{t('models.from_library')}</Tag>}
                    {!isLibrary && (
                      <Tag color={record.is_active ? 'success' : 'error'}>{record.is_active ? t('common.active') : t('common.disabled')}</Tag>
                    )}
                  </Space>
                }
              >
                <CardRow label="模型(MID)">
                  <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 4, maxWidth: '100%', minWidth: 0 }}>
                    <Tag color="purple" style={{ fontFamily: 'monospace', fontSize: 11, margin: 0, marginLeft: -7 }}>{record.mid || '-'}</Tag>
                    <div style={{ display: 'flex', alignItems: 'center', marginLeft: 1, maxWidth: '100%', minWidth: 0 }}>
                      <Text
                        className="model-remark-text"
                        type="secondary"
                        style={{ 
                          fontSize: 11, 
                          color: record.remark ? undefined : 'var(--text-tertiary, #8c8c8c)',
                          cursor: isLibrary ? 'default' : 'pointer',
                        }}
                        ellipsis={{ tooltip: record.remark || false }}
                        editable={isLibrary ? undefined : {
                          tooltip: false,
                          text: record.remark || '',
                          triggerType: ['icon', 'text'],
                          onChange: (val) => handleUpdateRemark(record.id, val),
                        }}
                      >
                        {record.remark || (isLibrary ? '-' : '添加备注')}
                      </Text>
                    </div>
                  </div>
                </CardRow>
                <CardRow label="模型ID">
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                    <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 4 }}>
                      <Tag color="blue" style={{ fontSize: 11, width: 'fit-content', maxWidth: '100%', margin: 0, whiteSpace: 'normal', wordBreak: 'break-all' }}>{record.model_id}</Tag>
                      {record.model_id_alias && (
                        <Text style={{ fontSize: 11, color: 'var(--text-secondary, #8c8c8c)' }}>({record.model_id_alias})</Text>
                      )}
                    </div>
                    {(() => {
                      let rules: { name: string; eid?: string }[] = [];
                      try {
                        if (record.forward_rule_ids) {
                          const ruleIds = JSON.parse(record.forward_rule_ids);
                          if (Array.isArray(ruleIds)) {
                            rules = ruleIds.map((id: number) => {
                              const r = allForwardRules.find(rule => rule.id === id);
                              return r ? { name: r.name, eid: r.eid } : null;
                            }).filter(Boolean) as { name: string; eid?: string }[];
                          }
                        }
                      } catch (e) { }
                      if (rules.length > 0) {
                        return (
                          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                            {rules.map((rule, idx) => (
                              <div key={idx} style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                                <div style={{ fontSize: 11, color: '#1677ff' }}>已挂载: {rule.name}</div>
                                {rule.eid && (
                                  <div style={{ fontSize: 11, color: 'var(--text-secondary, #8c8c8c)' }}>
                                    EID: <span style={{ fontFamily: 'monospace' }}>{rule.eid}</span>
                                  </div>
                                )}
                              </div>
                            ))}
                          </div>
                        );
                      }
                      return null;
                    })()}
                  </div>
                </CardRow>
                <CardRow label="计费类型">
                  <Space direction="vertical" size={4}>
                    <Tag color={colors[billingTypeVal]} style={{ margin: 0 }}>{t(`models.type_${billingTypeVal}`)}</Tag>
                    {br && (
                      <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                        <a
                          href={billingRulesEditPath(adminPath, br.pid || br.id)}
                          target="_blank"
                          rel="noreferrer"
                          style={{ fontSize: 11, color: 'var(--text-secondary, #8c8c8c)', textDecoration: 'underline' }}
                        >
                          {br.name}
                        </a>
                        {br.pid && <Text type="secondary" style={{ fontSize: 11 }}>PID: {br.pid}</Text>}
                      </div>
                    )}
                  </Space>
                </CardRow>
                <CardRow label="费率">
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                    {br ? <RateDisplay rule={br} currencySymbol={currencySymbol} formatPrice={formatPrice} /> : <Text type="secondary" italic>未挂载</Text>}
                    {(record.site_discount_enabled === 1 || record.global_discount_enabled === 1 || discountScheduleOn(record.discount_schedule) || limitedDiscountOn(record.discount_schedule)) && (
                      <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', gap: 4 }}>
                        {record.site_discount_enabled === 1 && (
                          <Tag color="volcano" variant="filled" style={{ fontSize: '10px', margin: 0, padding: '0 4px', lineHeight: '16px' }}>
                            {`折扣限价 ${formatRate(Number(record.site_discount || 1))} 倍率`}
                          </Tag>
                        )}
                        {record.global_discount_enabled === 1 && (
                          <Tag color="purple" variant="filled" style={{ fontSize: '10px', margin: 0, padding: '0 4px', lineHeight: '16px' }}>
                            {`全站折扣 ${formatRate(Number(record.global_discount || 1))} 倍率`}
                          </Tag>
                        )}
                        {record.global_discount_enabled === 1 && limitedDiscountOn(record.discount_schedule) && (
                          <Tag color={isLimitedDiscountExpired(record.discount_schedule, operationTz) ? 'default' : 'magenta'} variant="filled" style={{ fontSize: '10px', margin: 0, padding: '0 4px', lineHeight: '16px' }}>
                            {isLimitedDiscountExpired(record.discount_schedule, operationTz) ? '限时折扣已到期' : '限时折扣'}
                          </Tag>
                        )}
                        {record.global_discount_enabled === 1 && discountScheduleOn(record.discount_schedule) && (
                          <Tooltip title="已启用模型分时段折扣规划">
                            <Tag color={isDiscountScheduleExpired(record.discount_schedule, operationTz) ? 'default' : 'cyan'} variant="filled" style={{ fontSize: '10px', margin: 0, padding: '0 4px', lineHeight: '16px' }}>
                              {isDiscountScheduleExpired(record.discount_schedule, operationTz) ? '分时段折扣已到期' : '分时段折扣'}
                            </Tag>
                          </Tooltip>
                        )}
                      </div>
                    )}
                  </div>
                </CardRow>
                {(record.pre_deduction ?? 0) > 0 && (
                  <CardRow label="预扣">
                    <Text style={{ fontSize: 11, color: '#faad14' }}>
                      {formatPrice(record.pre_deduction)}{record.type_id && getTypeName(record.type_id)?.includes('视频') ? '/秒 (按秒预扣)' : ''}
                    </Text>
                  </CardRow>
                )}
                <CardRow label="页面排序">
                  <div style={{ display: 'flex', alignItems: 'center' }}>
                    <Text
                      editable={isLibrary ? undefined : {
                        tooltip: false,
                        text: String(record.sort_order ?? 0),
                        onChange: (val) => {
                          const num = parseInt(val.trim(), 10);
                          handleUpdateSortOrder(record.id, isNaN(num) ? 0 : num);
                        },
                      }}
                      style={{
                        fontFamily: 'monospace',
                        fontSize: 12,
                        fontWeight: 600,
                        color: (record.sort_order ?? 0) > 0 ? '#1677ff' : undefined,
                      }}
                    >
                      {record.sort_order ?? 0}
                    </Text>
                  </div>
                </CardRow>
                <CardActions>
                  {isLibrary ? (
                    <>
                      <Button size="small" icon={<EditOutlined />} onClick={() => handleEdit(record)} />
                      <Button size="small" icon={<CopyOutlined />} onClick={() => openCopyModal(record)} />
                      <Button
                        size="small"
                        icon={<CloudUploadOutlined />}
                        loading={publishingId === modelRowKey(record)}
                        onClick={() => handlePublish(record)}
                      >
                        {t('models.publish_model')}
                      </Button>
                      <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record)}>
                        <Button size="small" icon={<DeleteOutlined />} danger />
                      </Popconfirm>
                    </>
                  ) : (
                    <>
                      <Button size="small" icon={<EditOutlined />} onClick={() => handleEdit(record)} />
                      <Button size="small" icon={<CopyOutlined />} onClick={() => openCopyModal(record)} />
                      <Button
                        size="small"
                        icon={<CloudDownloadOutlined />}
                        loading={publishingId === modelRowKey(record)}
                        onClick={() => handleUnlist(record)}
                      >
                        {t('models.unlist_model')}
                      </Button>
                      {!isFromLibrary(record) && (
                        <Popconfirm title={t('common.confirm_delete')} onConfirm={() => handleDelete(record)}>
                          <Button size="small" icon={<DeleteOutlined />} danger />
                        </Popconfirm>
                      )}
                    </>
                  )}
                </CardActions>
              </MobileCard>
            );
          }}
        />
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12, width: '100%', minWidth: 0 }}>
          <div 
            ref={tableContainerRef}
            className="table-scroll-container"
            style={{ 
              width: '100%', 
              maxWidth: '100%',
              overflowX: 'auto',
              '--col-width-0': `${currentWidthsRef.current[0]}px`,
              '--col-width-1': `${currentWidthsRef.current[1]}px`,
              '--col-width-2': `${currentWidthsRef.current[2]}px`,
              '--col-width-3': `${currentWidthsRef.current[3]}px`,
              '--col-width-4': `${currentWidthsRef.current[4]}px`,
              '--col-width-5': `${currentWidthsRef.current[5]}px`,
              '--col-width-6': `${currentWidthsRef.current[6]}px`,
              '--col-width-7': `${currentWidthsRef.current[7]}px`,
              '--table-scroll-x': `${currentWidthsRef.current.reduce((a, b) => a + b, 0)}px`
            } as React.CSSProperties}
          >
            <div style={{ minWidth: 'var(--table-scroll-x, 1170px)', display: 'flex', flexDirection: 'column', gap: 0 }}>
              {loading ? (
                <Table className="compact-table" dataSource={[]} columns={resizableColumns} loading rowKey="id" tableLayout="fixed" onChange={handleHeaderTableChange} />
              ) : displayGroups.length === 0 ? (
                <Table className="compact-table" dataSource={[]} columns={resizableColumns} rowKey="id" tableLayout="fixed" onChange={handleHeaderTableChange} />
              ) : (
                <>
                  {displayGroups.length > 0 && (
                    <div style={{ marginBottom: 12 }}>
                      <Table
                        className="compact-table header-only-table"
                        dataSource={[]}
                        columns={resizableColumns}
                        components={tableComponents}
                        onChange={handleHeaderTableChange}
                        rowKey="id"
                        size="small"
                        tableLayout="fixed"
                        pagination={false}
                        style={{ marginBottom: 0 }}
                      />
                    </div>
                  )}
                  {displayGroups.map((item, index) => {
                    if (item.type === 'singles') {
                      return (
                        <div
                          key={`singles-${index}`}
                          style={{
                            marginBottom: 12,
                          }}
                        >
                          <Table
                            className="compact-table"
                            dataSource={item.children}
                            columns={resizableColumns}
                            rowKey={modelRowKey}
                            size="small"
                            pagination={false}
                            showHeader={false}
                            tableLayout="fixed"
                            style={{ marginBottom: 0 }}
                            rowSelection={isBatchEditMode ? {
                              selectedRowKeys,
                              onChange: (newKeys: React.Key[]) => {
                                const currentSinglesRowKeys = new Set(item.children.map((c: any) => modelRowKey(c)));
                                setSelectedRowKeys(prev => {
                                  const otherKeys = prev.filter(k => !currentSinglesRowKeys.has(String(k)));
                                  return [...otherKeys, ...newKeys];
                                });
                              }
                            } : undefined}
                          />
                        </div>
                      );
                    }
                    // 多个相同 model_id 的模型分组展示
                    return (
                      <GroupedModelTable
                        key={item.key}
                        group={item}
                        columns={resizableColumns}
                        isLight={isLight}
                        forceCollapsed={showGroupsOnly}
                        onRefresh={fetchModels}
                        setModels={setModels}
                        isBatchEditMode={isBatchEditMode}
                        selectedRowKeys={selectedRowKeys}
                        setSelectedRowKeys={setSelectedRowKeys}
                        isLibrary={isLibrary}
                      />
                    );
                  })}
                </>
              )}
            </div>
          </div>
          
          {!loading && totalCount > 0 && (
            <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '0 8px' }}>
              <ListPagination
                current={currentPage}
                pageSize={pageSize}
                total={totalCount}
                onChange={onChange}
                size={screens.xs ? 'small' : undefined}
              />
            </div>
          )}
        </div>
      )}
      </>
      ) : (
        <div style={{ animation: 'fadeIn 0.3s' }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
              <Button icon={<ArrowLeftOutlined />} onClick={handleCloseEditor}>{t('common.back', '返回')}</Button>
              <Title level={3} style={{ margin: 0 }}>
                {editingModel ? t('models.edit_model') : t('models.add_model')}
              </Title>
            </div>
            <Space>
              <Button onClick={handleCloseEditor}>{t('common.cancel', '取消')}</Button>
              <Button
                type="primary"
                loading={saving}
                onClick={() => {
                  if (saving) return;
                  setSaving(true);
                  form.submit();
                }}
              >
                {t('models.save_model', '保存模型')}
              </Button>
            </Space>
          </div>
          <div style={{ width: '100%' }}>
            <Form 
              form={form} 
              layout="vertical" 
              onFinish={handleSave} 
              onFinishFailed={(info) => {
                setSaving(false);
                if (info.errorFields && info.errorFields.length > 0) {
                  message.error(info.errorFields[0].errors[0]);
                }
              }} 
              initialValues={{ is_active: true, enable_log_content: 0, pre_deduction: 1 }}
            >
              {/* Hidden inputs to preserve form data for the custom fields */}
              <Form.Item name="provider_id" hidden><Input /></Form.Item>
              <Form.Item name="api_provider_id" hidden><Input /></Form.Item>
              <Form.Item name="type_id" hidden rules={[{ required: true, message: t('models.type_required', '请选择模型类型') }]}><Input /></Form.Item>
              <Form.Item name="billing_rule_id" hidden rules={[{ required: true, message: t('models.billing_rule_required', '请选择计费模版') }]}><Input /></Form.Item>
              <Form.Item 
                name="forward_rule_ids" 
                hidden 
                rules={[
                  {
                    validator: async (_, value) => {
                      if (!value || !Array.isArray(value) || value.length === 0) {
                        return Promise.reject(new Error(t('models.forward_rule_required', '请选择挂载转发规则组合')));
                      }
                      if (value.length > 1) {
                        return Promise.reject(new Error(t('models.forward_rule_single', '只能选择一个转发规则组合')));
                      }
                      return Promise.resolve();
                    }
                  }
                ]}
              >
                <Input />
              </Form.Item>

              <Row gutter={24}>
                {/* 左侧基本配置栏 */}
                <Col xs={24} md={10} xl={10}>
                  <div style={{ padding: 16, background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)', borderRadius: 8, height: 'calc(100vh - 220px)', overflowY: 'auto' }}>
                    <Form.Item name="name" label={<Text strong>{t('models.model_name')}</Text>} rules={[{ required: true }]} style={{ marginBottom: 12 }}>
                      <Input placeholder="e.g. GPT-4o" />
                    </Form.Item>
                    
                    <Form.Item name="model_id" label={<Text strong>{t('models.model_request_id_label', `${t('models.model_id')}(模型请求ID)`)}</Text>} rules={[{ required: true }]} style={{ marginBottom: 12 }}>
                      <Input placeholder="e.g. gpt-4o" />
                    </Form.Item>

                    {/* 模型ID别名映射 */}
                    <div style={{ marginBottom: 12, padding: '8px 12px', borderRadius: 6, border: '1px solid var(--border-color, #f0f0f0)', background: isLight ? '#fafafa' : 'rgba(255,255,255,0.02)' }}>
                      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: aliasEnabled ? 8 : 0 }}>
                        <Text style={{ fontSize: 13 }}>{t('models.alias_mapping', '模型ID别名映射')}</Text>
                        <Switch size="small" checked={aliasEnabled} onChange={(checked) => { setAliasEnabled(checked); if (!checked) form.setFieldsValue({ model_id_alias: '' }); }} />
                      </div>
                      {aliasEnabled && (
                        <>
                          <Form.Item name="model_id_alias" style={{ marginBottom: 4 }}>
                            <Input placeholder={t('models.alias_placeholder', '填写真实的上游模型ID，如 Kling@3.0')} />
                          </Form.Item>
                          <div style={{ fontSize: 11, color: 'var(--text-secondary, #8c8c8c)' }}>
                            {t('models.alias_hint', '渠道的模型映射优先级最高。只有渠道未设置映射时，才会使用此处的模型别名。')}
                          </div>
                        </>
                      )}
                    </div>
                    
                    <Form.Item name="original_id" label={<Text strong>{t('models.original_id_label', '模型原始ID(把所有的相同的模型合并显示)')}</Text>} style={{ marginBottom: 12 }} extra={<span style={{ fontSize: 11, color: 'var(--text-secondary, #8c8c8c)' }}>{t('models.original_id_hint', '如果不填，默认使用模型请求ID合并。')}</span>}>
                      <Input placeholder="e.g. gpt-4o" />
                    </Form.Item>
                    
                    <Form.Item noStyle shouldUpdate={(prevValues, currentValues) => prevValues.model_id !== currentValues.model_id || prevValues.original_id !== currentValues.original_id}>
                      {({ getFieldValue }) => {
                        if (editingModel) return null;
                        const currentModelId = getFieldValue('model_id');
                        const currentOriginalId = getFieldValue('original_id');
                        const targetId = currentOriginalId || currentModelId;
                        if (targetId) {
                          const existingCount = models.filter(m => (m.original_id || m.model_id) === targetId).length;
                          if (existingCount > 0) {
                            return (
                              <div style={{ marginTop: -8, marginBottom: 12, padding: '6px 10px', borderRadius: 6, background: 'rgba(22,119,255,0.08)', border: '1px solid rgba(22,119,255,0.2)', fontSize: 12, color: '#1677ff' }}>
                                {t('models.original_id_merge_tip', { id: targetId, count: existingCount, defaultValue: `💡 该ID (${targetId}) 已有 ${existingCount} 个模型，新模型将在模型广场中与其自动归为同组显示。` })}
                              </div>
                            );
                          }
                        }
                        return null;
                      }}
                    </Form.Item>

                    {/* 官方服务商与 API 服务商选择 */}
                    <Row gutter={8} style={{ marginBottom: 12 }}>
                      <Col span={12}>
                        <Form.Item shouldUpdate={(prev, cur) => prev.provider_id !== cur.provider_id} noStyle>
                          {() => {
                            const val = form.getFieldValue('provider_id');
                            const provider = allProviders.find(p => p.id === val);
                            const isActive = activeRightPanel === 'provider';
                            return (
                              <div onClick={() => setActiveRightPanel('provider')} style={{ padding: '8px 12px', borderRadius: 6, border: isActive ? '1px solid var(--text)' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), background: isActive ? (isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)') : (isLight ? '#fff' : 'rgba(255,255,255,0.02)'), cursor: 'pointer', transition: 'all 0.2s' }}>
                                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
                                  <Text strong={isActive} style={{ color: isActive ? 'var(--text)' : 'inherit', flexShrink: 0 }}>{t('models.provider', '官方服务商')}</Text>
                                  <span style={{ fontSize: 13, color: isActive ? 'var(--text)' : 'var(--text-secondary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                    {provider ? localizeClassificationName(provider, i18n.language, t) : <span style={{ opacity: 0.5 }}>{t('models.click_to_select', '点击选择')}</span>} <ArrowRightOutlined style={{ marginLeft: 4 }} />
                                  </span>
                                </div>
                              </div>
                            );
                          }}
                        </Form.Item>
                      </Col>
                      <Col span={12}>
                        <Form.Item shouldUpdate={(prev, cur) => prev.api_provider_id !== cur.api_provider_id} noStyle>
                          {() => {
                            const val = form.getFieldValue('api_provider_id');
                            const isActive = activeRightPanel === 'api_provider';
                            const apiProvider = allApiProviders.find(p => p.id === val);
                            return (
                              <div onClick={() => setActiveRightPanel('api_provider')} style={{ padding: '8px 12px', borderRadius: 6, border: isActive ? '1px solid var(--text)' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), background: isActive ? (isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)') : (isLight ? '#fff' : 'rgba(255,255,255,0.02)'), cursor: 'pointer', transition: 'all 0.2s' }}>
                                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8 }}>
                                  <Text strong={isActive} style={{ color: isActive ? 'var(--text)' : 'inherit', flexShrink: 0 }}>{t('models.api_provider', 'API服务商')}</Text>
                                  <span style={{ fontSize: 13, color: isActive ? 'var(--text)' : 'var(--text-secondary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                    {apiProvider ? localizeClassificationName(apiProvider, i18n.language, t) : <span style={{ opacity: 0.5 }}>{t('models.click_to_select', '点击选择')}</span>} <ArrowRightOutlined style={{ marginLeft: 4 }} />
                                  </span>
                                </div>
                              </div>
                            );
                          }}
                        </Form.Item>
                      </Col>
                    </Row>

                    <Form.Item
                      name="logo"
                      label={<Text strong>{t('models.model_logo', '模型 Logo')}</Text>}
                      style={{ marginBottom: 12 }}
                      getValueFromEvent={(icon) => icon?.name || undefined}
                    >
                      <IconPicker
                        onChange={(icon) => {
                          logoManuallyEditedRef.current = Boolean(icon?.name);
                          form.setFieldsValue({ logo: icon?.name || undefined });
                        }}
                        placeholder={t('models.select_model_icon', '选择模型图标')}
                      />
                    </Form.Item>

                    <Form.Item name="description" label={<Text strong>{t('models.model_desc_label', '模型简介')}</Text>} style={{ marginBottom: 12 }}>
                      <Input.TextArea placeholder={t('models.model_desc_placeholder', '模型简介，可以在前台展示给用户')} rows={1} />
                    </Form.Item>

                    <Form.Item name="remark" label={<Text strong>{t('models.internal_remark', '内部备注')}</Text>} style={{ marginBottom: 12 }}>
                      <Input.TextArea placeholder={t('models.internal_remark_placeholder', '内部备注信息，仅管理员可见')} rows={1} />
                    </Form.Item>



                    <div style={{ marginBottom: 16 }}>
                      <Text strong style={{ display: 'block', marginBottom: 8 }}>{t('models.association_settings', '关联设置')}</Text>

                      {/* Type */}
                      <Form.Item shouldUpdate={(prev, cur) => prev.type_id !== cur.type_id} noStyle>
                        {() => {
                          const val = form.getFieldValue('type_id');
                          const type = allTypes.find(p => p.id === val);
                          const isActive = activeRightPanel === 'type';
                          return (
                            <div onClick={() => setActiveRightPanel('type')} style={{ padding: '8px 12px', borderRadius: 6, marginBottom: 6, border: isActive ? '1px solid var(--text)' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), background: isActive ? (isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)') : (isLight ? '#fff' : 'rgba(255,255,255,0.02)'), cursor: 'pointer', transition: 'all 0.2s' }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                                <Text strong={isActive} style={{ color: isActive ? 'var(--text)' : 'inherit' }}>
                                  <span style={{ color: '#ff4d4f', marginRight: 4, fontFamily: 'SimSun, sans-serif' }}>*</span>{t('models.type', '模型类型')}
                                </Text>
                                <span style={{ fontSize: 13, color: isActive ? 'var(--text)' : 'var(--text-secondary)' }}>
                                  {type ? localizeClassificationName(type, i18n.language, t) : <span style={{ opacity: 0.5 }}>{t('models.click_to_select', '点击选择')}</span>} <ArrowRightOutlined style={{ marginLeft: 4 }} />
                                </span>
                              </div>
                            </div>
                          );
                        }}
                      </Form.Item>

                      {/* Billing Rule */}
                      <Form.Item shouldUpdate={(prev, cur) => prev.billing_rule_id !== cur.billing_rule_id} noStyle>
                        {() => {
                          const val = form.getFieldValue('billing_rule_id');
                          const br = allBillingRules.find(p => p.id === val);
                          const isActive = activeRightPanel === 'billing_rule';
                          return (
                            <div onClick={() => setActiveRightPanel('billing_rule')} style={{ padding: '8px 12px', borderRadius: 6, marginBottom: 6, border: isActive ? '1px solid var(--text)' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), background: isActive ? (isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)') : (isLight ? '#fff' : 'rgba(255,255,255,0.02)'), cursor: 'pointer', transition: 'all 0.2s' }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                                <Text strong={isActive} style={{ color: isActive ? 'var(--text)' : 'inherit' }}>
                                  <span style={{ color: '#ff4d4f', marginRight: 4, fontFamily: 'SimSun, sans-serif' }}>*</span>{t('models.billing_template', '计费模板')}
                                </Text>
                                <span style={{ fontSize: 13, color: isActive ? 'var(--text)' : 'var(--text-secondary)' }}>
                                  {br ? (
                                    <>
                                      {br.name}
                                      {br.pid && <span style={{ opacity: 0.6, marginLeft: 6 }}>(PID: {br.pid})</span>}
                                    </>
                                  ) : val ? (
                                    <span style={{ color: '#ff4d4f' }}>{t('models.billing_template_invalid', '原模板已停用或不存在，请重新选择')}</span>
                                  ) : (
                                    <span style={{ opacity: 0.5 }}>{t('models.click_to_select', '点击选择')}</span>
                                  )} <ArrowRightOutlined style={{ marginLeft: 4 }} />
                                </span>
                              </div>
                            </div>
                          );
                        }}
                      </Form.Item>

                      {/* Forward Rules */}
                      <Form.Item shouldUpdate={(prev, cur) => prev.forward_rule_ids !== cur.forward_rule_ids} noStyle>
                        {() => {
                          const val = form.getFieldValue('forward_rule_ids') || [];
                          const selectedRule = allForwardRules.find(r => val.includes(r.id));
                          const isActive = activeRightPanel === 'forward_rules';
                          return (
                            <div onClick={() => setActiveRightPanel('forward_rules')} style={{ padding: '8px 12px', borderRadius: 6, marginBottom: 6, border: isActive ? '1px solid var(--text)' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), background: isActive ? (isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)') : (isLight ? '#fff' : 'rgba(255,255,255,0.02)'), cursor: 'pointer', transition: 'all 0.2s' }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                                <Text strong={isActive} style={{ color: isActive ? 'var(--text)' : 'inherit' }}>
                                  <span style={{ color: '#ff4d4f', marginRight: 4, fontFamily: 'SimSun, sans-serif' }}>*</span>{t('models.mount_forward_rules', '挂载转发规则组合')}
                                </Text>
                                <span style={{ fontSize: 13, color: isActive ? 'var(--text)' : 'var(--text-secondary)' }}>
                                  {selectedRule ? (
                                    <>
                                      {selectedRule.name}
                                      {selectedRule.eid && <span style={{ opacity: 0.6, marginLeft: 6 }}>(EID: {selectedRule.eid})</span>}
                                    </>
                                  ) : (
                                    <span style={{ opacity: 0.5 }}>{t('models.click_to_select', '点击选择')}</span>
                                  )} <ArrowRightOutlined style={{ marginLeft: 4 }} />
                                </span>
                              </div>
                            </div>
                          );
                        }}
                      </Form.Item>
                    </div>

                    <Row gutter={16}>
                       <Col span={12}>
                           <Form.Item name="sort_order" label={<Text strong>{t('models.sort_order', '页面排序')}</Text>} initialValue={0} extra={<span style={{ fontSize: 11, color: 'var(--text-secondary, #8c8c8c)' }}>{t('models.sort_order_hint', '数值越大在列表中显示越靠前')}</span>}>
                              <InputNumber style={{ width: '100%' }} precision={0} placeholder={t('models.sort_order_placeholder', '越大越靠前')} />
                           </Form.Item>
                       </Col>
                       <Col span={12}>
                          <Form.Item noStyle shouldUpdate={(prev: any, cur: any) => prev.type_id !== cur.type_id}>
                            {({ getFieldValue }: any) => {
                              const typeId = getFieldValue('type_id');
                              const matchedType = allTypes.find(item => item.id === typeId) || typesStats.find(item => item.id === typeId);
                              const isVideo = Boolean(
                                matchedType && (matchedType.name?.includes('视频') || matchedType.name_en?.toLowerCase().includes('video'))
                              );
                              return (
                                <Form.Item
                                  name="pre_deduction"
                                  label={<Text strong>{t('models.pre_deduction_with_currency', { currency: settings?.currency?.default_currency || 'USD', defaultValue: `预扣费 (${settings?.currency?.default_currency || 'USD'})` })}</Text>}
                                  initialValue={1}
                                  rules={[
                                    { required: true, message: t('models.pre_deduction_required', '请填写预扣费') },
                                    {
                                      validator: async (_, value) => {
                                        if (value === undefined || value === null || value === '') {
                                          return Promise.resolve();
                                        }
                                        const num = Number(value);
                                        if (!Number.isFinite(num) || num <= 0) {
                                          return Promise.reject(new Error(t('models.pre_deduction_gt_zero', '预扣费必须大于 0')));
                                        }
                                        return Promise.resolve();
                                      },
                                    },
                                  ]}
                                  extra={isVideo ? <span style={{ fontSize: 11, color: '#faad14' }}>{t('models.pre_deduction_video_tip', '提示：视频模型按「预扣费 × 视频秒数」进行预扣冻结，防止透支')}</span> : undefined}
                                >
                                  <InputNumber style={{ width: '100%' }} precision={6} min={0} placeholder="1" />
                                </Form.Item>
                              );
                            }}
                          </Form.Item>
                       </Col>
                       <Col span={12}>
                           <Form.Item name="is_active" htmlFor={undefined} label={<Text strong>{t('common.status')}</Text>} valuePropName="checked" initialValue={true}>
                              <Switch />
                           </Form.Item>
                       </Col>
                       <Col span={12}>
                           <Form.Item name="enable_log_content" htmlFor={undefined} label={<Text strong>{t('models.enable_log_content', '记录上下文')}</Text>} valuePropName="checked" initialValue={false}>
                              <Switch />
                           </Form.Item>
                       </Col>
                       <Col span={12}>
                           <Form.Item name="site_discount_enabled" htmlFor={undefined} label={<Text strong>{t('models.site_discount_floor', '折扣限价')}</Text>} valuePropName="checked" initialValue={true}>
                              <Switch checkedChildren={t('common.on', '开启')} unCheckedChildren={t('common.off', '关闭')} />
                           </Form.Item>
                       </Col>
                    </Row>

                    <Form.Item noStyle shouldUpdate={(prev: any, cur: any) => prev.site_discount_enabled !== cur.site_discount_enabled}>
                      {({ getFieldValue }: any) => getFieldValue('site_discount_enabled') ? (
                        <Form.Item
                          name="site_discount"
                          label={<Text strong>{t('models.site_discount_rate', '折扣限价倍率')}</Text>}
                          initialValue={1.0}
                          extra={t('models.site_discount_rate_hint', '已勾选折扣项取最低值后，不低于此倍率保底。不填为原价 1.0，不可填 0（免费请用下方高级设置）')}
                          rules={[{
                            validator: (_: any, value: number) => {
                              if (value == null || (value as unknown) === '') {
                                return Promise.resolve();
                              }
                              const num = Number(value);
                              if (!Number.isFinite(num) || num <= 0) {
                                return Promise.reject(t('models.site_discount_rate_gt_zero', '折扣限价倍率必须大于 0（不填代表跟随计费规则原价；免费请在下方高级设置中配置）'));
                              }
                              return Promise.resolve();
                            },
                          }]}
                        >
                          <InputNumber style={{ width: '100%' }} precision={3} step={0.1} min={0} placeholder={t('models.discount_rate_placeholder', '不填为原价 1.0')} />
                        </Form.Item>
                      ) : null}
                    </Form.Item>

                    <Row gutter={16}>
                       <Col span={12}>
                           <Form.Item name="global_discount_enabled" htmlFor={undefined} label={<Text strong>{t('models.global_discount', '全站折扣')}</Text>} valuePropName="checked" initialValue={false}>
                              <Switch checkedChildren={t('common.on', '开启')} unCheckedChildren={t('common.off', '关闭')} />
                           </Form.Item>
                       </Col>
                    </Row>

                    <Form.Item noStyle shouldUpdate={(prev: any, cur: any) => prev.global_discount_enabled !== cur.global_discount_enabled || prev.global_discount !== cur.global_discount || prev.site_discount_enabled !== cur.site_discount_enabled || prev.site_discount !== cur.site_discount}>
                      {({ getFieldValue }: any) => {
                        if (!getFieldValue('global_discount_enabled')) return null;
                        const siteEnabled = getFieldValue('site_discount_enabled');
                        const rawSiteDiscount = getFieldValue('site_discount');
                        const siteDiscount = rawSiteDiscount != null && rawSiteDiscount !== '' && Number.isFinite(Number(rawSiteDiscount)) && Number(rawSiteDiscount) > 0
                          ? Number(rawSiteDiscount)
                          : 1.0;
                        const rawGlobal = getFieldValue('global_discount');
                        const isGlobalEmpty = rawGlobal === undefined || rawGlobal === null || rawGlobal === '';
                        const globalNum = isGlobalEmpty ? 1.0 : Number(rawGlobal);
                        const belowSiteFloor = Boolean(siteEnabled && Number.isFinite(globalNum) && globalNum > 0 && globalNum < siteDiscount);
                        return (
                          <Form.Item
                            name="global_discount"
                            label={<Text strong>{t('models.global_discount_rate', '全站折扣倍率')}</Text>}
                            initialValue={1.0}
                            dependencies={['site_discount', 'site_discount_enabled']}
                            extra={
                              <div>
                                {belowSiteFloor && (
                                  <div style={{ marginBottom: 4 }}>
                                    <Button
                                      type="link"
                                      size="small"
                                      style={{ padding: 0, height: 'auto', fontSize: 12 }}
                                      onClick={() => {
                                        form.setFieldValue('site_discount', globalNum);
                                        form.validateFields(['site_discount', 'global_discount']).catch(() => {});
                                      }}
                                    >
                                      {t('models.sync_site_discount_tip', { rate: globalNum, defaultValue: `👉 点击将上方「折扣限价倍率」同步设为 ${globalNum}` })}
                                    </Button>
                                  </div>
                                )}
                                <div>{t('models.global_discount_rate_hint', '与用户等级已勾选折扣项取最低值，再受折扣限价保底。不填为原价 1.0，不可填 0（免费请用下方高级设置）')}</div>
                              </div>
                            }
                            rules={[{
                              validator: (_: any, value: number) => {
                                const isEmpty = value == null || (value as unknown) === '';
                                const num = isEmpty ? 1.0 : Number(value);
                                if (!isEmpty && (!Number.isFinite(num) || num <= 0)) {
                                  return Promise.reject(t('models.global_discount_rate_gt_zero', '全站折扣倍率必须大于 0（不填代表跟随计费规则原价；免费请在下方高级设置中配置）'));
                                }
                                if (siteEnabled && num < siteDiscount) {
                                  return Promise.reject(t('models.global_discount_below_floor', { global: num, floor: siteDiscount, defaultValue: `全站折扣 ${num} 低于折扣限价 ${siteDiscount}，设置后将被限价覆盖，请调整` }));
                                }
                                return Promise.resolve();
                              },
                            }]}
                          >
                            <InputNumber style={{ width: '100%' }} precision={3} step={0.1} min={0} placeholder={t('models.discount_rate_placeholder', '不填为原价 1.0')} />
                          </Form.Item>
                        );
                      }}
                    </Form.Item>

                    <DiscountScheduleEditor
                      isLight={isLight}
                      operationTz={operationTz}
                      adminTz={adminTz}
                      allBillingRules={allBillingRules}
                    />
                    
                  </div>
                </Col>

                {/* 右侧交互选择栏 */}
                <Col xs={24} md={14} xl={14}>
                  <div style={{ padding: '24px', background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.02)', borderRadius: 8, height: 'calc(100vh - 220px)', overflowY: 'auto', border: isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.05)' }}>
                    <Form.Item shouldUpdate noStyle>
                      {() => {
                        if (activeRightPanel === 'provider') {
                          return (
                            <div style={{ animation: 'fadeIn 0.2s' }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
                                <Title level={4} style={{ margin: 0 }}>{t('models.select_provider', '选择官方服务商')}</Title>
                                <Button type="dashed" size="small" onClick={() => setIsProviderManagerVisible(true)}>{t('models.manage_providers', '管理官方服务商')}</Button>
                              </div>
                              <Row gutter={[6, 6]}>
                                <Col xs={6} sm={4} md={3}>
                                  <div 
                                    onClick={() => {
                                      const nextValues: Record<string, any> = { provider_id: null };
                                      if (!logoManuallyEditedRef.current) {
                                        nextValues.logo = undefined;
                                      }
                                      form.setFieldsValue(nextValues);
                                    }}
                                    style={{ 
                                      aspectRatio: '1 / 1', 
                                      padding: '6px 4px', 
                                      borderRadius: 6, 
                                      cursor: 'pointer', 
                                      border: !form.getFieldValue('provider_id') ? '1.5px solid #1677ff' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), 
                                      background: !form.getFieldValue('provider_id') ? (isLight ? '#e6f4ff' : 'rgba(22,119,255,0.15)') : (isLight ? '#fff' : 'rgba(255,255,255,0.03)'), 
                                      display: 'flex', 
                                      flexDirection: 'column', 
                                      alignItems: 'center', 
                                      justifyContent: 'center', 
                                      gap: 4, 
                                      transition: 'all 0.2s' 
                                    }}
                                  >
                                    <CloseOutlined style={{ fontSize: 16, color: 'var(--text-secondary)' }} />
                                    <span style={{ fontWeight: !form.getFieldValue('provider_id') ? 600 : 400, fontSize: 11, color: 'var(--text-secondary)', textAlign: 'center' }}>{t('models.clear_selection', '清除选择')}</span>
                                  </div>
                                </Col>
                                {allProviders.map(p => {
                                  const selected = form.getFieldValue('provider_id') === p.id;
                                  const label = localizeClassificationName(p, i18n.language, t);
                                  return (
                                    <Col xs={6} sm={4} md={3} key={p.id}>
                                      <div 
                                        onClick={() => {
                                          const nextValues: Record<string, any> = { provider_id: p.id };
                                          if (!logoManuallyEditedRef.current) {
                                            nextValues.logo = p.logo || undefined;
                                          }
                                          form.setFieldsValue(nextValues);
                                        }}
                                        style={{ 
                                          aspectRatio: '1 / 1', 
                                          padding: '6px 4px', 
                                          borderRadius: 6, 
                                          cursor: 'pointer', 
                                          border: selected ? '1.5px solid #1677ff' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), 
                                          background: selected ? (isLight ? '#e6f4ff' : 'rgba(22,119,255,0.15)') : (isLight ? '#fff' : 'rgba(255,255,255,0.03)'), 
                                          display: 'flex', 
                                          flexDirection: 'column', 
                                          alignItems: 'center', 
                                          justifyContent: 'center', 
                                          gap: 4, 
                                          transition: 'all 0.2s' 
                                        }}
                                      >
                                        {p.logo ? (
                                          <SmartSvgIcon src={`/assets/icons/lobe/${p.logo}.svg`} alt="" style={{ width: 20, height: 20, flexShrink: 0, objectFit: 'contain' }} onError={e => { (e.target as HTMLImageElement).style.display = 'none'; }} />
                                        ) : (
                                          <div style={{ width: 20, height: 20, borderRadius: 4, flexShrink: 0, background: 'rgba(128,128,128,0.15)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 11, fontWeight: 600 }}>{(p.name_en || p.name).charAt(0)}</div>
                                        )}
                                        <span title={label} style={{ fontWeight: selected ? 600 : 400, fontSize: 11.5, textAlign: 'center', width: '100%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{label}</span>
                                      </div>
                                    </Col>
                                  );
                                })}
                              </Row>
                            </div>
                          );
                        }


                        if (activeRightPanel === 'api_provider') {
                          return (
                            <div style={{ animation: 'fadeIn 0.2s' }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
                                <Title level={4} style={{ margin: 0 }}>{t('models.select_api_provider', '选择API服务商')}</Title>
                                <Button type="dashed" size="small" onClick={() => setIsApiProviderManagerVisible(true)}>{t('models.manage_api_providers', '管理API服务商')}</Button>
                              </div>
                              <Row gutter={[6, 6]}>
                                <Col xs={6} sm={4} md={3}>
                                  <div
                                    onClick={() => form.setFieldsValue({ api_provider_id: null })}
                                    style={{ 
                                      aspectRatio: '1 / 1', 
                                      padding: '6px 4px', 
                                      borderRadius: 6, 
                                      cursor: 'pointer', 
                                      border: form.getFieldValue('api_provider_id') === null ? '1.5px solid #1677ff' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), 
                                      background: form.getFieldValue('api_provider_id') === null ? (isLight ? '#e6f4ff' : 'rgba(22,119,255,0.15)') : (isLight ? '#fff' : 'rgba(255,255,255,0.03)'), 
                                      display: 'flex', 
                                      flexDirection: 'column', 
                                      alignItems: 'center', 
                                      justifyContent: 'center', 
                                      gap: 4, 
                                      transition: 'all 0.2s' 
                                    }}
                                  >
                                    <CloseOutlined style={{ fontSize: 16, color: 'var(--text-secondary)' }} />
                                    <span style={{ fontSize: 11, fontWeight: form.getFieldValue('api_provider_id') === null ? 600 : 400, color: 'var(--text-secondary)', textAlign: 'center' }}>{t('models.clear_selection', '清除选择')}</span>
                                  </div>
                                </Col>
                                {allApiProviders.map(p => {
                                  const selected = form.getFieldValue('api_provider_id') === p.id;
                                  const label = localizeClassificationName(p, i18n.language, t);
                                  return (
                                    <Col xs={6} sm={4} md={3} key={p.id}>
                                      <div
                                        onClick={() => form.setFieldsValue({ api_provider_id: p.id })}
                                        style={{ 
                                          aspectRatio: '1 / 1', 
                                          padding: '6px 4px', 
                                          borderRadius: 6, 
                                          cursor: 'pointer', 
                                          border: selected ? '1.5px solid #1677ff' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), 
                                          background: selected ? (isLight ? '#e6f4ff' : 'rgba(22,119,255,0.15)') : (isLight ? '#fff' : 'rgba(255,255,255,0.03)'), 
                                          display: 'flex', 
                                          flexDirection: 'column', 
                                          alignItems: 'center', 
                                          justifyContent: 'center', 
                                          gap: 4, 
                                          transition: 'all 0.2s' 
                                        }}
                                      >
                                        {p.logo ? (
                                          <SmartSvgIcon src={`/assets/icons/lobe/${p.logo}.svg`} alt="" style={{ width: 20, height: 20, flexShrink: 0, objectFit: 'contain' }} onError={e => { (e.target as HTMLImageElement).style.display = 'none'; }} />
                                        ) : (
                                          <div style={{ width: 20, height: 20, borderRadius: 4, flexShrink: 0, background: 'rgba(128,128,128,0.15)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 11, fontWeight: 600 }}>{(p.name_en || p.name).charAt(0)}</div>
                                        )}
                                        <div title={label} style={{ fontSize: 11.5, fontWeight: selected ? 600 : 400, textAlign: 'center', width: '100%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                          {label}
                                        </div>
                                      </div>
                                    </Col>
                                  );
                                })}
                              </Row>
                            </div>
                          );
                        }

                        if (activeRightPanel === 'type') {
                          return (
                            <div style={{ animation: 'fadeIn 0.2s' }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
                                <Title level={4} style={{ margin: 0 }}>{t('models.select_type', '选择模型类型')}</Title>
                                <Button type="dashed" size="small" onClick={() => setIsTypeManagerVisible(true)}>{t('models.manage_types', '管理类型')}</Button>
                              </div>
                              <Row gutter={[6, 6]}>
                                {allTypes.map(item => {
                                  const selected = form.getFieldValue('type_id') === item.id;
                                  const sysIcon = renderTypeSystemIcon(item.name_en || item.name, 18);
                                  const label = localizeClassificationName(item, i18n.language, t);
                                  return (
                                    <Col xs={6} sm={4} md={3} key={item.id}>
                                      <div 
                                        onClick={() => {
                                          if (form.getFieldValue('type_id') !== item.id) {
                                            form.setFieldsValue({ type_id: item.id, feature_attributes: [] });
                                          }
                                        }}
                                        style={{ 
                                          aspectRatio: '1 / 1', 
                                          padding: '6px 4px', 
                                          borderRadius: 6, 
                                          cursor: 'pointer', 
                                          border: selected ? '1.5px solid #1677ff' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), 
                                          background: selected ? (isLight ? '#e6f4ff' : 'rgba(22,119,255,0.15)') : (isLight ? '#fff' : 'rgba(255,255,255,0.03)'), 
                                          display: 'flex', 
                                          flexDirection: 'column', 
                                          alignItems: 'center', 
                                          justifyContent: 'center', 
                                          gap: 4, 
                                          transition: 'all 0.2s' 
                                        }}
                                      >
                                        {sysIcon ? (
                                          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', width: 20, height: 20, color: selected ? '#1677ff' : (isLight ? 'rgba(0,0,0,0.7)' : 'rgba(255,255,255,0.8)') }}>
                                            {sysIcon}
                                          </div>
                                        ) : item.logo ? (
                                          <SmartSvgIcon src={`/assets/icons/lobe/${item.logo}.svg`} alt="" style={{ width: 20, height: 20, flexShrink: 0, objectFit: 'contain' }} onError={e => { (e.target as HTMLImageElement).style.display = 'none'; }} />
                                        ) : (
                                          <div style={{ width: 20, height: 20, borderRadius: 4, flexShrink: 0, background: 'rgba(128,128,128,0.15)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 11, fontWeight: 600 }}>{(item.name_en || item.name).charAt(0)}</div>
                                        )}
                                        <span title={label} style={{ fontWeight: selected ? 600 : 400, fontSize: 11.5, textAlign: 'center', width: '100%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{label}</span>
                                      </div>
                                    </Col>
                                  );
                                })}
                              </Row>

                              {/* 二级功能属性选择 */}
                              <Form.Item noStyle shouldUpdate={(prev, cur) => prev.type_id !== cur.type_id}>
                                {({ getFieldValue }) => {
                                  const typeId = getFieldValue('type_id');
                                  if (!typeId) return null; // 只有选择了类型才显示

                                  const typeObj = allTypes.find(tp => tp.id === typeId);
                                  let options: {label: string, value: string}[] = [];
                                  
                                  if (typeObj && typeObj.default_features) {
                                    try {
                                      const defaultFeatures = JSON.parse(typeObj.default_features);
                                      if (Array.isArray(defaultFeatures)) {
                                        const values = defaultFeatures.filter((v) => typeof v === 'string');
                                        const list = (typeObj.name || '').includes('图片') ? imageFeatureOptions(values) : values;
                                        options = list.map(v => ({ label: v, value: v }));
                                      }
                                    } catch (e) {
                                      console.error("Failed to parse default_features:", e);
                                    }
                                  }
                                  return (
                                    <div style={{ marginTop: 24, padding: '16px', background: isLight ? '#fff' : 'rgba(255,255,255,0.02)', borderRadius: 8, border: isLight ? '1px solid #e5e4e7' : '1px solid #303030' }}>
                                      <div style={{ marginBottom: 12, display: 'flex', justifyContent: 'space-between', alignItems: 'center', flexWrap: 'wrap', gap: 8 }}>
                                        <Text strong>{t('models.secondary_features_title', '二级功能属性选择')}</Text>
                                        <Space size={8}>
                                          {options.length > 0 && (
                                            <>
                                              <Button
                                                type="link"
                                                size="small"
                                                style={{ padding: 0, height: 'auto', fontSize: 12 }}
                                                onClick={() => form.setFieldsValue({ feature_attributes: options.map(o => o.value) })}
                                              >
                                                {t('models.select_all_default', '全选默认')}
                                              </Button>
                                              <Button
                                                type="link"
                                                size="small"
                                                style={{ padding: 0, height: 'auto', fontSize: 12 }}
                                                onClick={() => form.setFieldsValue({ feature_attributes: [] })}
                                              >
                                                {t('common.clear', '清空')}
                                              </Button>
                                            </>
                                          )}
                                          <Text type="secondary" style={{ fontSize: 12 }}>{t('models.custom_feature_hint', '支持自定义输入添加')}</Text>
                                        </Space>
                                      </div>
                                      <Form.Item name="feature_attributes" style={{ marginBottom: 0 }}>
                                        <FeatureTagsSelect options={options.map(o => o.value)} />
                                      </Form.Item>
                                    </div>
                                  );
                                }}
                              </Form.Item>

                            </div>
                          );
                        }

                        if (activeRightPanel === 'billing_rule') {
                          const filteredBillingRules = allBillingRules.filter(b => {
                            const matchType = billingTypeFilter === 'all' || b.billing_type === billingTypeFilter;
                            const matchRule = billingTypeFilter === 'all' || billingRuleFilter === 'all'
                              || (billingRuleFilter === 'claude' ? isClaudeRule(b) : (billingRuleFilter === 'standard' ? (b.billing_rule === 'standard' && !isClaudeRule(b)) : b.billing_rule === billingRuleFilter));
                            const matchKeyword = !billingSearchKeyword || b.name.toLowerCase().includes(billingSearchKeyword.toLowerCase());
                            return matchType && matchRule && matchKeyword;
                          });

                          return (
                            <div style={{ animation: 'fadeIn 0.2s' }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10, gap: 8, flexWrap: 'wrap' }}>
                                <Title level={4} style={{ margin: 0 }}>{t('models.select_billing_template', '选择计费基础定价模板')}</Title>
                                <Input 
                                  placeholder={t('models.search_template_placeholder', '搜索模板名称...')} 
                                  prefix={<SearchOutlined style={{ color: 'var(--text-secondary)' }} />} 
                                  style={{ width: 170 }} 
                                  value={billingSearchKeyword}
                                  onChange={e => setBillingSearchKeyword(e.target.value)}
                                  allowClear
                                />
                              </div>
                              <div style={{ marginBottom: 8 }}>
                                <div style={{ color: 'var(--text-secondary)', fontSize: 12, lineHeight: '18px', marginBottom: 4 }}>
                                  <span style={{ color: '#ff4d4f', marginRight: 3, fontFamily: 'SimSun, sans-serif' }}>*</span>{t('models.billing_type_label', '计费类型')}
                                </div>
                                <Radio.Group 
                                  value={billingTypeFilter} 
                                  onChange={e => {
                                    setBillingTypeFilter(e.target.value);
                                    setBillingRuleFilter('all');
                                  }} 
                                  buttonStyle="solid"
                                  size="small"
                                >
                                  <Radio.Button value="all" style={{ fontSize: 11.5, paddingInline: 8, height: 22, lineHeight: '20px' }}>{t('common.all', '全部')}</Radio.Button>
                                  <Radio.Button value="tokens" style={{ fontSize: 11.5, paddingInline: 8, height: 22, lineHeight: '20px' }}>{t('models.billing_type_tokens', 'Token计费')}</Radio.Button>
                                  <Radio.Button value="requests" style={{ fontSize: 11.5, paddingInline: 8, height: 22, lineHeight: '20px' }}>{t('models.billing_type_requests', '按次计费')}</Radio.Button>
                                  <Radio.Button value="duration" style={{ fontSize: 11.5, paddingInline: 8, height: 22, lineHeight: '20px' }}>{t('models.billing_type_duration', '时长计费')}</Radio.Button>
                                </Radio.Group>
                              </div>
                              {billingTypeFilter !== 'all' && (
                                <div style={{ marginBottom: 10 }}>
                                  <div style={{ color: 'var(--text-secondary)', fontSize: 12, lineHeight: '18px', marginBottom: 4 }}>
                                    <span style={{ color: '#ff4d4f', marginRight: 3, fontFamily: 'SimSun, sans-serif' }}>*</span>
                                    {billingTypeFilter === 'tokens'
                                      ? t('models.token_billing_rule_label', 'token计费规则')
                                      : billingTypeFilter === 'requests'
                                        ? t('models.request_billing_rule_label', '按次计费规则')
                                        : t('models.duration_billing_rule_label', '时长计费规则')}
                                  </div>
                                  <Radio.Group
                                    value={billingRuleFilter}
                                    onChange={e => setBillingRuleFilter(e.target.value)}
                                    buttonStyle="solid"
                                    size="small"
                                    style={{ display: 'flex', flexWrap: 'wrap', rowGap: 4 }}
                                  >
                                    <Radio.Button value="all" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('common.all', '全部')}</Radio.Button>
                                    {billingTypeFilter === 'tokens' && (
                                      <>
                                        <Radio.Button value="standard" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_standard')}</Radio.Button>
                                        <Radio.Button value="claude" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_claude', 'Claude 计费')}</Radio.Button>
                                        <Radio.Button value="kimi_chat" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_kimi_cache', 'Kimi 缓存')}</Radio.Button>
                                        <Radio.Button value="multimodal" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_multimodal')}</Radio.Button>
                                        <Radio.Button value="gpt_billing" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_gpt_image', 'GPT图片计费')}</Radio.Button>
                                        <Radio.Button value="tiered" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_tiered')}</Radio.Button>
                                        <Radio.Button value="doubao_chat" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_doubao_chat', '豆包聊天阶梯')}</Radio.Button>
                                        <Radio.Button value="seedance2.0" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_seedance2', 'Seedance 2 规则')}</Radio.Button>
                                        <Radio.Button value="seedance1.5pro" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_seedance15', 'Seedance 1.5 规则')}</Radio.Button>
                                        <Radio.Button value="seedance1.0" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_seedance1', 'Seedance 1 规则')}</Radio.Button>
                                        <Radio.Button value="glm_5_3" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>glm 5.3</Radio.Button>
                                      </>
                                    )}
                                    {billingTypeFilter === 'requests' && (
                                      <>
                                        <Radio.Button value="fixed" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_fixed_rate', '固定费率')}</Radio.Button>
                                        <Radio.Button value="per_image" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_per_image', '按张收费')}</Radio.Button>
                                        <Radio.Button value="image_resolution" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_image_resolution', '分辨率 K')}</Radio.Button>
                                        <Radio.Button value="image_resolution_io" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_image_resolution_io', '分辨率 K(双向)')}</Radio.Button>
                                        <Radio.Button value="image_size_pixel" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_image_size_pixel', '分辨率像素')}</Radio.Button>
                                        <Radio.Button value="vidu_image" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_vidu_image', 'Vidu 图片')}</Radio.Button>
                                        <Radio.Button value="volc_seedream_pro" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_volc_seedream_pro', '火山 Seedream 5.0 Pro')}</Radio.Button>
                                        <Radio.Button value="characters" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_characters', '字符计费')}</Radio.Button>
                                      </>
                                    )}
                                    {billingTypeFilter === 'duration' && (
                                      <>
                                        <Radio.Button value="standard" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_fixed_duration', '固定时长')}</Radio.Button>
                                        <Radio.Button value="video_resolution" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_video_resolution', '分辨率阶梯')}</Radio.Button>
                                        <Radio.Button value="minimax_h3" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_minimax_h3', 'MiniMax (秒价+参考图)')}</Radio.Button>
                                        <Radio.Button value="video_seconds_io" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_video_seconds_io', '双向秒价 + 参考图')}</Radio.Button>
                                        <Radio.Button value="video_seconds_ref" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_video_seconds_ref', '双向秒价 + 参考视频')}</Radio.Button>
                                        <Radio.Button value="fal_ref_video" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>fal H3-MAX</Radio.Button>
                                        <Radio.Button value="video_quality" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_video_quality', '画质与帧率')}</Radio.Button>
                                        <Radio.Button value="kling_video" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_kling_video', '可灵视频')}</Radio.Button>
                                        <Radio.Button value="kling4_video" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_kling4_video', '可灵 4.0 视频')}</Radio.Button>
                                        <Radio.Button value="vidu_video" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_vidu_video', 'Vidu 视频')}</Radio.Button>
                                        {allBillingRules.some(b => b.billing_rule === 'volc_enhance_cascade') && (
                                          <Radio.Button value="volc_enhance_cascade" style={{ fontSize: 11.5, paddingInline: 7, height: 22, lineHeight: '20px' }}>{t('models.rule_volc_enhance_cascade', '火山级联封装')}</Radio.Button>
                                        )}
                                      </>
                                    )}
                                  </Radio.Group>
                                </div>
                              )}
                              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                                {filteredBillingRules.length > 0 ? filteredBillingRules.map(b => {
                                  const selected = form.getFieldValue('billing_rule_id') === b.id;
                                  return (
                                    <div 
                                      key={b.id}
                                      onClick={() => form.setFieldsValue({ billing_rule_id: b.id })}
                                      style={{ 
                                        padding: '7px 12px', 
                                        borderRadius: 6, 
                                        cursor: 'pointer', 
                                        border: selected ? '1.5px solid #1677ff' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), 
                                        background: selected ? (isLight ? '#e6f4ff' : 'rgba(22,119,255,0.15)') : (isLight ? '#fff' : 'rgba(255,255,255,0.03)'), 
                                        display: 'flex', 
                                        justifyContent: 'space-between', 
                                        alignItems: 'center', 
                                        transition: 'all 0.2s' 
                                      }}
                                    >
                                      <div style={{ display: 'flex', flexDirection: 'column', gap: 4, flex: 1, overflow: 'hidden' }}>
                                        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                                          <span style={{ fontWeight: selected ? 600 : 500, fontSize: 13.5, color: selected ? '#1677ff' : 'inherit' }}>{b.name}</span>
                                          {b.pid && <Tag color="blue" variant="filled" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '16px' }}>PID: {b.pid}</Tag>}
                                          <Tag color={b.billing_type === 'tokens' ? 'green' : (b.billing_type === 'requests' ? 'orange' : 'purple')} variant="filled" style={{ margin: 0, fontSize: 10, padding: '0 4px', lineHeight: '16px' }}>
                                            {b.billing_type === 'tokens'
                                              ? t('models.billing_tag_tokens', '按Token')
                                              : b.billing_type === 'requests'
                                                ? t('models.billing_tag_requests', '按次')
                                                : t('models.billing_tag_duration', '按时长')}
                                          </Tag>
                                        </div>
                                        <div style={{ background: isLight ? 'rgba(0,0,0,0.025)' : 'rgba(255,255,255,0.03)', padding: '3px 8px', borderRadius: 4, fontSize: 11.5, lineHeight: '1.4' }}>
                                          <RateDisplay rule={b} currencySymbol={currencySymbol} formatPrice={formatPrice} />
                                        </div>
                                      </div>
                                      <div style={{ 
                                        width: 16, 
                                        height: 16, 
                                        borderRadius: '50%', 
                                        border: selected ? '4.5px solid #1677ff' : (isLight ? '1px solid #d9d9d9' : '1px solid rgba(255,255,255,0.2)'), 
                                        background: selected ? '#fff' : 'transparent', 
                                        transition: 'all 0.2s', 
                                        flexShrink: 0, 
                                        marginLeft: 12 
                                      }} />
                                    </div>
                                  );
                                }) : <Empty description={t('models.no_matching_billing_templates', '未找到匹配的计费模板')} style={{ margin: '40px 0' }} />}
                              </div>
                            </div>
                          );
                        }

                        if (activeRightPanel === 'forward_rules') {
                          const val = form.getFieldValue('forward_rule_ids') || [];
                          const categories = Array.from(new Set(allForwardRules.map(r => r.category).filter(Boolean)));

                          const filteredForwardRules = allForwardRules.filter(r => {
                            const matchCategory = forwardRuleCategoryFilter === 'all' || r.category === forwardRuleCategoryFilter;
                            if (!matchCategory) return false;
                            if (!forwardRuleSearchKeyword) return true;
                            const keyword = forwardRuleSearchKeyword.toLowerCase();
                            return (
                              r.name.toLowerCase().includes(keyword) || 
                              (r.eid && String(r.eid).toLowerCase().includes(keyword)) ||
                              (r.category && String(r.category).toLowerCase().includes(keyword))
                            );
                          });

                          return (
                            <div style={{ animation: 'fadeIn 0.2s' }}>
                              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 10, gap: 8, flexWrap: 'wrap' }}>
                                <Title level={4} style={{ margin: 0 }}>{t('models.mount_forward_rules', '挂载转发规则组合')}</Title>
                                <Input 
                                  placeholder={t('models.search_name_or_eid', '搜索名称或 EID...')} 
                                  prefix={<SearchOutlined style={{ color: 'var(--text-secondary)' }} />} 
                                  style={{ width: 170 }} 
                                  value={forwardRuleSearchKeyword}
                                  onChange={e => setForwardRuleSearchKeyword(e.target.value)}
                                  allowClear
                                />
                              </div>
                              {categories.length > 0 && (
                                <div style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
                                  <span style={{ color: 'var(--text-secondary)', fontSize: 13, flexShrink: 0 }}>
                                    {t('models.category_label', '所属分类')}
                                  </span>
                                  <Radio.Group 
                                    value={forwardRuleCategoryFilter} 
                                    onChange={e => setForwardRuleCategoryFilter(e.target.value)} 
                                    buttonStyle="solid"
                                    size="small"
                                  >
                                    <Radio.Button value="all">{t('common.all', '全部')}</Radio.Button>
                                    {categories.map(cat => (
                                       <Radio.Button key={String(cat)} value={String(cat)}>{String(cat)}</Radio.Button>
                                    ))}
                                  </Radio.Group>
                                </div>
                              )}
                              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                                {filteredForwardRules.length > 0 ? filteredForwardRules.map(r => {
                                  const isSelected = val.includes(r.id);
                                  return (
                                    <div 
                                      key={r.id}
                                      onClick={() => {
                                        form.setFieldsValue({ forward_rule_ids: [r.id] });
                                      }}
                                      style={{ 
                                        padding: '6px 12px', 
                                        borderRadius: 6, 
                                        cursor: 'pointer', 
                                        border: isSelected ? '1.5px solid #1677ff' : (isLight ? '1px solid #e5e4e7' : '1px solid rgba(255,255,255,0.08)'), 
                                        background: isSelected ? (isLight ? '#e6f4ff' : 'rgba(22,119,255,0.15)') : (isLight ? '#fff' : 'rgba(255,255,255,0.03)'), 
                                        display: 'flex', 
                                        justifyContent: 'space-between', 
                                        alignItems: 'center', 
                                        minHeight: 36,
                                        transition: 'all 0.2s' 
                                      }}
                                    >
                                      <div style={{ display: 'flex', alignItems: 'center', gap: 8, overflow: 'hidden', flex: 1, marginRight: 12 }}>
                                        {r.eid && (
                                          <Tag color="blue" variant="filled" style={{ margin: 0, fontSize: 11, padding: '0 5px', lineHeight: '18px', flexShrink: 0 }}>
                                            EID: {r.eid}
                                          </Tag>
                                        )}
                                        {r.category && (
                                          <Tag color="purple" variant="filled" style={{ margin: 0, fontSize: 10, padding: '0 5px', lineHeight: '18px', flexShrink: 0 }}>
                                            {r.category}
                                          </Tag>
                                        )}
                                        <span style={{ fontWeight: isSelected ? 600 : 500, fontSize: 13.5, color: isSelected ? '#1677ff' : 'inherit', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                          {r.name}
                                        </span>
                                      </div>
                                      <div style={{ 
                                        width: 16, 
                                        height: 16, 
                                        borderRadius: '50%', 
                                        border: isSelected ? '4.5px solid #1677ff' : (isLight ? '1px solid #d9d9d9' : '1px solid rgba(255,255,255,0.2)'), 
                                        background: isSelected ? '#fff' : 'transparent', 
                                        transition: 'all 0.2s', 
                                        flexShrink: 0 
                                      }} />
                                    </div>
                                  );
                                }) : <Empty description={t('models.no_matching_forward_rules', '未找到匹配的转发规则')} style={{ margin: '40px 0' }} />}
                              </div>
                            </div>
                          );
                        }


                        return (
                          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--text-secondary)' }}>
                            <Space direction="vertical" align="center" size={16}>
                              <div style={{ fontSize: 48, opacity: 0.2 }}>👈</div>
                              <span>{t('models.click_left_item_hint', '请在左侧点击配置项，以在此处查看更多选项')}</span>
                            </Space>
                          </div>
                        );
                      }}
                    </Form.Item>
                  </div>
                </Col>
              </Row>
            </Form>
          </div>
      </div>
      )}

      <Modal
        title={t('models.copy_model')}
        open={!!copyTarget}
        onCancel={() => setCopyTarget(null)}
        onOk={handleCopy}
        confirmLoading={copying}
        okText={t('common.confirm')}
        cancelText={t('common.cancel')}
        destroyOnHidden
        centered
        width={460}
      >
        <div style={{ padding: '8px 0' }}>
          <Text type="secondary">{t('models.copy_hint')}</Text>
          <Input
            style={{ marginTop: 12 }}
            value={copyName}
            onChange={(e) => setCopyName(e.target.value)}
            placeholder={copyTarget?.name}
            onPressEnter={handleCopy}
          />
        </div>
      </Modal>

      <ClassificationManager
        type="api_provider"
        visible={isApiProviderManagerVisible}
        onClose={() => setIsApiProviderManagerVisible(false)}
        onUpdate={() => handleClassificationUpdate('api_provider')}
      />

      <ClassificationManager
        type="provider"
        visible={isProviderManagerVisible}
        onClose={() => setIsProviderManagerVisible(false)}
        onUpdate={() => handleClassificationUpdate('provider')}
      />
      
      <ClassificationManager
        type="type"
        visible={isTypeManagerVisible}
        onClose={() => setIsTypeManagerVisible(false)}
        onUpdate={() => handleClassificationUpdate('type')}
      />
    </Card>
  );
};

export default Models;
