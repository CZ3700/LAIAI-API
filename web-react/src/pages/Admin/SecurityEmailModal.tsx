import React, { useState, useMemo, useEffect } from 'react';
import {
  Modal,
  Table,
  Input,
  Button,
  Tag,
  Space,
  Typography,
  Tooltip,
  Popconfirm,
  Radio,
  Empty,
  Alert,
  message,
  theme,
} from 'antd';
import type { TableColumnsType } from 'antd';
import {
  SearchOutlined,
  PlusOutlined,
  DeleteOutlined,
  EditOutlined,
  CopyOutlined,
  ImportOutlined,
  ClearOutlined,
  CheckOutlined,
  CloseOutlined,
  MailOutlined,
  ReloadOutlined,
} from '@ant-design/icons';

const { Text, Paragraph } = Typography;
const { TextArea } = Input;

type EmailRuleType = 'domain' | 'wildcard_domain' | 'exact_email' | 'invalid';

interface EmailRuleItem {
  id: string;
  raw: string;
  type: EmailRuleType;
  scopeDesc: string;
  isValid: boolean;
}

interface SecurityEmailModalProps {
  open: boolean;
  mode: 'view' | 'edit';
  initialList: string[];
  onClose: () => void;
  onSave?: (newList: string[]) => void;
  onSaveAndApply?: (newList: string[]) => Promise<boolean | void>;
  onSwitchToEdit?: () => void;
}

// 默认推荐常用主流邮箱域名
const DEFAULT_COMMON_EMAIL_DOMAINS = [
  'qq.com',
  '163.com',
  '126.com',
  'foxmail.com',
  'outlook.com',
  'hotmail.com',
  'gmail.com',
  'aliyun.com',
  'icloud.com',
  'yeah.net',
  'sina.com',
  'sohu.com',
];

function isValidDomainName(domain: string): boolean {
  const d = domain.trim().toLowerCase();
  if (!d || d.length > 253 || !d.includes('.')) return false;
  const labels = d.split('.');
  if (labels.length < 2) return false;
  const tld = labels[labels.length - 1];
  if (!tld || tld.length < 2) return false;
  return labels.every(
    (label) =>
      label.length > 0 &&
      label.length <= 63 &&
      /^[a-z0-9-]+$/.test(label) &&
      !label.startsWith('-') &&
      !label.endsWith('-')
  );
}

function normalizeEmailRuleInput(rawStr: string, extractDomainOnly = false): string {
  const trimmed = rawStr.trim().toLowerCase();
  if (!trimmed) return '';
  if (trimmed.startsWith('@')) {
    return trimmed.slice(1).trim();
  }
  if (extractDomainOnly && trimmed.includes('@')) {
    const parts = trimmed.split('@');
    const domainPart = parts[parts.length - 1]?.trim() || '';
    return domainPart;
  }
  return trimmed;
}

function parseEmailRule(rawStr: string, index: number): EmailRuleItem {
  const trimmed = rawStr.trim();
  const id = `${index}-${trimmed}`;

  if (!trimmed) {
    return { id, raw: '', type: 'invalid', scopeDesc: '空内容', isValid: false };
  }

  // 检测 @domain.com 形式
  if (trimmed.startsWith('@')) {
    const domainPart = trimmed.slice(1).trim();
    if (isValidDomainName(domainPart)) {
      return {
        id,
        raw: trimmed,
        type: 'domain',
        scopeDesc: `允许 @${domainPart.toLowerCase()} 下所有邮箱`,
        isValid: true,
      };
    }
    return {
      id,
      raw: trimmed,
      type: 'invalid',
      scopeDesc: '域名格式异常 (无法命中生效)',
      isValid: false,
    };
  }

  // 检测 *.domain.com 通配子域名形式
  if (trimmed.startsWith('*.')) {
    const domainPart = trimmed.slice(2).trim();
    if (isValidDomainName(domainPart)) {
      return {
        id,
        raw: trimmed,
        type: 'wildcard_domain',
        scopeDesc: `允许 *.${domainPart.toLowerCase()} 所有子域邮箱`,
        isValid: true,
      };
    }
    return {
      id,
      raw: trimmed,
      type: 'invalid',
      scopeDesc: '通配域名格式异常',
      isValid: false,
    };
  }

  // 检测完整邮箱地址 user@domain.com
  if (trimmed.includes('@')) {
    const parts = trimmed.split('@');
    if (parts.length === 2) {
      const [local, domainPart] = parts;
      if (local.includes('+')) {
        return {
          id,
          raw: trimmed,
          type: 'invalid',
          scopeDesc: '不支持 + 号别名邮箱',
          isValid: false,
        };
      }
      if (
        local.length > 0 &&
        local.length <= 64 &&
        !/\s/.test(local) &&
        !local.startsWith('.') &&
        !local.endsWith('.') &&
        !local.includes('..') &&
        isValidDomainName(domainPart)
      ) {
        return {
          id,
          raw: trimmed,
          type: 'exact_email',
          scopeDesc: `仅允许该指定邮箱 (@${domainPart.toLowerCase()})`,
          isValid: true,
        };
      }
    }
    return {
      id,
      raw: trimmed,
      type: 'invalid',
      scopeDesc: '邮箱地址格式异常 (无法命中生效)',
      isValid: false,
    };
  }

  // 检测标准域名 qq.com
  if (isValidDomainName(trimmed)) {
    return {
      id,
      raw: trimmed,
      type: 'domain',
      scopeDesc: `允许 @${trimmed.toLowerCase()} 下所有邮箱`,
      isValid: true,
    };
  }

  return {
    id,
    raw: trimmed,
    type: 'invalid',
    scopeDesc: '格式异常 (无法命中生效)',
    isValid: false,
  };
}

export const SecurityEmailModal: React.FC<SecurityEmailModalProps> = ({
  open,
  mode,
  initialList,
  onClose,
  onSave,
  onSaveAndApply,
  onSwitchToEdit,
}) => {
  const { token } = theme.useToken();

  // 当前正在编辑的列表数据
  const [rules, setRules] = useState<string[]>([]);
  // 搜索关键字
  const [searchText, setSearchText] = useState('');
  // 分类过滤
  const [typeFilter, setTypeFilter] = useState<string>('all');
  // 批量选中的 keys
  const [selectedRowKeys, setSelectedRowKeys] = useState<React.Key[]>([]);

  // 单条录入输入框状态
  const [singleInput, setSingleInput] = useState('');
  const [singleInputError, setSingleInputError] = useState<string | null>(null);

  // 行内编辑状态
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editingText, setEditingText] = useState('');

  // 批量导入子对话框状态
  const [importModalOpen, setImportModalOpen] = useState(false);
  const [importText, setImportText] = useState('');
  const [importMode, setImportMode] = useState<'append' | 'overwrite'>('append');
  const [importParseMode, setImportParseMode] = useState<'keep' | 'extract_domain'>('keep');

  // 保存中状态（区分「保存更新」与「保存更新关闭」按钮 loading）
  const [savingMode, setSavingMode] = useState<'keep' | 'close' | null>(null);
  const saving = savingMode !== null;
  // 分页状态（用于跨页连续显示序号 #）
  const [currentPage, setCurrentPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);

  // 初始化列表（仅在弹窗打开时初始化，避免父组件每秒时钟刷新传入新数组引用导致输入被清空）
  useEffect(() => {
    if (open) {
      setRules([...initialList]);
      setSearchText('');
      setTypeFilter('all');
      setSelectedRowKeys([]);
      setSingleInput('');
      setSingleInputError(null);
      setEditingId(null);
      setCurrentPage(1);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  // 解析所有的规则条目
  const parsedItems: EmailRuleItem[] = useMemo(() => {
    return rules.map((r, i) => parseEmailRule(r, i));
  }, [rules]);

  // 筛选后的展示列表
  const filteredItems = useMemo(() => {
    return parsedItems.filter((item) => {
      if (searchText.trim()) {
        const query = searchText.trim().toLowerCase();
        if (!item.raw.toLowerCase().includes(query)) {
          return false;
        }
      }
      if (typeFilter === 'domain' && item.type !== 'domain') return false;
      if (typeFilter === 'wildcard_domain' && item.type !== 'wildcard_domain') return false;
      if (typeFilter === 'exact_email' && item.type !== 'exact_email') return false;
      if (typeFilter === 'invalid' && item.isValid) return false;
      return true;
    });
  }, [parsedItems, searchText, typeFilter]);

  // 统计概览
  const stats = useMemo(() => {
    let domainCount = 0;
    let wildcardCount = 0;
    let emailCount = 0;
    let invalidCount = 0;
    for (const item of parsedItems) {
      if (!item.isValid) {
        invalidCount++;
      } else if (item.type === 'domain') {
        domainCount++;
      } else if (item.type === 'wildcard_domain') {
        wildcardCount++;
      } else if (item.type === 'exact_email') {
        emailCount++;
      }
    }
    return { total: parsedItems.length, domainCount, wildcardCount, emailCount, invalidCount };
  }, [parsedItems]);

  // 单条添加规则
  const handleAddSingle = () => {
    const val = normalizeEmailRuleInput(singleInput);
    if (!val) {
      setSingleInputError('请输入邮箱域名（如 qq.com）或完整邮箱地址');
      return;
    }
    if (rules.some((r) => normalizeEmailRuleInput(r) === val.toLowerCase())) {
      setSingleInputError('该条规则已存在，请勿重复添加');
      return;
    }
    const testItem = parseEmailRule(val, 0);
    if (!testItem.isValid) {
      setSingleInputError(
        val.includes('+')
          ? '不支持使用 + 号别名邮箱，请填写标准邮箱或域名'
          : '格式不符合邮箱域名（如 qq.com、*.edu.cn）或完整邮箱规范'
      );
      return;
    }

    setRules([val, ...rules]);
    setSingleInput('');
    setSingleInputError(null);
    message.success(`已添加规则：${val}`);
  };

  // 快捷填入常用主流邮箱域名
  const handleApplyCommonDomains = () => {
    const existingLower = new Set(rules.map((r) => r.toLowerCase()));
    const toAdd = DEFAULT_COMMON_EMAIL_DOMAINS.filter((d) => !existingLower.has(d));
    if (toAdd.length === 0) {
      message.info('常用主流邮箱域名已全部在列表中');
      return;
    }
    setRules([...rules, ...toAdd]);
    message.success(`已追加 ${toAdd.length} 个常用主流邮箱域名`);
  };

  // 一键复制全部
  const handleCopyAll = () => {
    if (rules.length === 0) {
      message.info('当前规则列表为空');
      return;
    }
    const content = rules.join('\n');
    navigator.clipboard
      .writeText(content)
      .then(() => {
        message.success(`已成功复制全部 ${rules.length} 条规则至剪贴板`);
      })
      .catch(() => {
        message.error('复制失败，请手动选择复制');
      });
  };

  // 单项删除
  const handleDeleteRow = (raw: string) => {
    const next = rules.filter((r) => r !== raw);
    setRules(next);
    setSelectedRowKeys(selectedRowKeys.filter((k) => !String(k).endsWith(`-${raw}`)));
    message.success('已删除');
  };

  // 批量删除选中的项
  const handleBatchDelete = () => {
    if (selectedRowKeys.length === 0) return;
    const selectedRaws = new Set(
      filteredItems.filter((item) => selectedRowKeys.includes(item.id)).map((item) => item.raw)
    );
    const next = rules.filter((r) => !selectedRaws.has(r));
    setRules(next);
    setSelectedRowKeys([]);
    message.success(`已成功删除选中的 ${selectedRaws.size} 条规则`);
  };

  // 清空全部
  const handleClearAll = () => {
    setRules([]);
    setSelectedRowKeys([]);
    message.success('已清空全部规则');
  };

  // 保存行内编辑
  const handleSaveEdit = (oldRaw: string) => {
    const val = normalizeEmailRuleInput(editingText);
    if (!val) {
      message.error('域名或邮箱内容不能为空');
      return;
    }
    const testItem = parseEmailRule(val, 0);
    if (!testItem.isValid) {
      message.error(
        val.includes('+')
          ? '不支持使用 + 号别名邮箱，请填写标准邮箱或域名'
          : '格式不符合邮箱域名（如 qq.com、*.edu.cn）或完整邮箱规范'
      );
      return;
    }
    if (
      val.toLowerCase() !== normalizeEmailRuleInput(oldRaw) &&
      rules.some((r) => normalizeEmailRuleInput(r) === val.toLowerCase())
    ) {
      message.error('目标规则已存在，请勿重复');
      return;
    }
    const idx = rules.indexOf(oldRaw);
    if (idx !== -1) {
      const next = [...rules];
      next[idx] = val;
      setRules(next);
      setEditingId(null);
      setEditingText('');
      message.success('已更新规则');
    }
  };

  // 批量解析导入
  const handleConfirmImport = () => {
    if (!importText.trim()) {
      message.warning('请输入要导入的内容');
      return;
    }
    const extractOnly = importParseMode === 'extract_domain';
    const rawTokens = importText
      .split(/[\r\n,;，；\s]+/)
      .map((s) => normalizeEmailRuleInput(s, extractOnly))
      .filter(Boolean);

    if (rawTokens.length === 0) {
      message.warning('未识别到有效条目');
      return;
    }

    const validTokens: string[] = [];
    let invalidCount = 0;
    for (const tokenStr of rawTokens) {
      if (parseEmailRule(tokenStr, 0).isValid) {
        validTokens.push(tokenStr);
      } else {
        invalidCount++;
      }
    }

    if (validTokens.length === 0) {
      message.error(`未能导入有效规则：共 ${invalidCount} 条格式不符合域名或邮箱规范`);
      return;
    }

    if (importMode === 'overwrite') {
      const seen = new Set<string>();
      const unique: string[] = [];
      for (const tokenStr of validTokens) {
        const lower = tokenStr.toLowerCase();
        if (!seen.has(lower)) {
          seen.add(lower);
          unique.push(tokenStr);
        }
      }
      setRules(unique);
      setCurrentPage(1);
      message.success(
        `已覆盖导入 ${unique.length} 条规则${invalidCount > 0 ? `（已自动忽略 ${invalidCount} 条格式异常项）` : ''}`
      );
    } else {
      const seen = new Set(rules.map((r) => normalizeEmailRuleInput(r)));
      const next = [...rules];
      let added = 0;
      for (const tokenStr of validTokens) {
        const lower = tokenStr.toLowerCase();
        if (!seen.has(lower)) {
          seen.add(lower);
          next.push(tokenStr);
          added++;
        }
      }
      setRules(next);
      message.success(
        `批量追加完成：新增 ${added} 条，已自动去重${invalidCount > 0 ? `（忽略 ${invalidCount} 条格式异常项）` : ''}`
      );
    }

    setImportModalOpen(false);
    setImportText('');
  };

  // 若输入框还有未点击“+ 添加规则”的内容，保存前自动校验并并入列表
  const resolveRulesBeforeSave = (): string[] | null => {
    let candidateRules = rules;
    const val = normalizeEmailRuleInput(singleInput);
    if (val) {
      const testItem = parseEmailRule(val, 0);
      if (!testItem.isValid) {
        setSingleInputError(
          val.includes('+')
            ? '不支持使用 + 号别名邮箱，请填写标准邮箱或域名'
            : '格式不符合邮箱域名（如 qq.com、*.edu.cn）或完整邮箱规范'
        );
        return null;
      }
      if (!rules.some((r) => normalizeEmailRuleInput(r) === val.toLowerCase())) {
        candidateRules = [val, ...rules];
      }
      setRules(candidateRules);
      setSingleInput('');
      setSingleInputError(null);
    }
    const invalidItem = candidateRules.find((r, i) => !parseEmailRule(r, i).isValid);
    if (invalidItem) {
      message.error(`列表中存在格式异常的规则「${invalidItem}」，请先修正或删除后再保存`);
      return null;
    }
    return candidateRules;
  };

  // 直接保存写入数据库并立即生效（closeAfterSave 控制保存后是否关闭当前弹窗）
  const handlePersistSave = async (closeAfterSave: boolean) => {
    const finalRules = resolveRulesBeforeSave();
    if (!finalRules) return;
    if (!onSaveAndApply) {
      if (onSave) {
        onSave(finalRules);
      }
      if (closeAfterSave) {
        onClose();
      }
      return;
    }
    setSavingMode(closeAfterSave ? 'close' : 'keep');
    try {
      if (onSave) {
        onSave(finalRules);
      }
      const res = await onSaveAndApply(finalRules);
      if (res !== false && closeAfterSave) {
        onClose();
      }
    } catch {
      // 错误由请求全局拦截器弹出
    } finally {
      setSavingMode(null);
    }
  };

  // 渲染类型 Tag
  const renderTypeTag = (itemType: EmailRuleType, isValid: boolean) => {
    if (!isValid || itemType === 'invalid') {
      return <Tag color="error">格式异常</Tag>;
    }
    switch (itemType) {
      case 'domain':
        return <Tag color="processing">邮箱域名</Tag>;
      case 'wildcard_domain':
        return <Tag color="cyan">子域通配</Tag>;
      case 'exact_email':
        return <Tag color="purple">指定邮箱</Tag>;
      default:
        return <Tag>未知</Tag>;
    }
  };

  // 表格列定义
  const columns: TableColumnsType<EmailRuleItem> = [
    {
      title: '#',
      dataIndex: 'id',
      key: 'index',
      width: 60,
      align: 'center',
      render: (_: any, __: any, index: number) => (
        <Text type="secondary" style={{ fontSize: 12 }}>
          {(currentPage - 1) * pageSize + index + 1}
        </Text>
      ),
    },
    {
      title: '允许的邮箱域名 / 邮箱地址',
      dataIndex: 'raw',
      key: 'raw',
      render: (raw: string, record: EmailRuleItem) => {
        if (mode === 'edit' && editingId === record.id) {
          return (
            <Space size={4}>
              <Input
                size="small"
                value={editingText}
                onChange={(e) => setEditingText(e.target.value)}
                onPressEnter={() => handleSaveEdit(raw)}
                style={{ width: 240 }}
                autoFocus
              />
              <Tooltip title="保存修改">
                <Button
                  size="small"
                  type="text"
                  icon={<CheckOutlined style={{ color: token.colorSuccess }} />}
                  onClick={() => handleSaveEdit(raw)}
                />
              </Tooltip>
              <Tooltip title="取消">
                <Button
                  size="small"
                  type="text"
                  icon={<CloseOutlined />}
                  onClick={() => {
                    setEditingId(null);
                    setEditingText('');
                  }}
                />
              </Tooltip>
            </Space>
          );
        }

        return (
          <Space size={6}>
            <Text code copyable={{ text: raw }} style={{ fontSize: 13, fontWeight: 500 }}>
              {raw}
            </Text>
          </Space>
        );
      },
    },
    {
      title: '规则类型',
      dataIndex: 'type',
      key: 'type',
      width: 120,
      render: (_: any, record: EmailRuleItem) => renderTypeTag(record.type, record.isValid),
    },
    {
      title: '作用范围说明',
      dataIndex: 'scopeDesc',
      key: 'scopeDesc',
      width: 240,
      render: (scopeDesc: string, record: EmailRuleItem) => (
        <Text type={record.isValid ? 'secondary' : 'danger'} style={{ fontSize: 12 }}>
          {scopeDesc}
        </Text>
      ),
    },
    ...(mode === 'edit'
      ? [
          {
            title: '操作',
            key: 'action',
            width: 100,
            align: 'center' as const,
            render: (_: any, record: EmailRuleItem) => (
              <Space size={4}>
                <Tooltip title="编辑此条">
                  <Button
                    type="text"
                    size="small"
                    icon={<EditOutlined />}
                    onClick={() => {
                      setEditingId(record.id);
                      setEditingText(record.raw);
                    }}
                  />
                </Tooltip>
                <Tooltip title="删除">
                  <Popconfirm
                    title="确定移除该规则？"
                    okText="删除"
                    cancelText="取消"
                    okButtonProps={{ danger: true }}
                    onConfirm={() => handleDeleteRow(record.raw)}
                  >
                    <Button type="text" size="small" danger icon={<DeleteOutlined />} />
                  </Popconfirm>
                </Tooltip>
              </Space>
            ),
          },
        ]
      : []),
  ];

  return (
    <>
      <Modal
        open={open}
        title={
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, paddingBottom: 4 }}>
            <MailOutlined
              style={{
                fontSize: 20,
                color: token.colorPrimary,
              }}
            />
            <div>
              <div style={{ fontSize: 16, fontWeight: 600 }}>
                {mode === 'view' ? '查看明细：允许的注册邮箱白名单' : '配置允许的注册邮箱白名单'}
              </div>
              <Text type="secondary" style={{ fontSize: 12, fontWeight: 'normal' }}>
                仅允许匹配白名单规则的邮箱获取注册验证码与注册账号。支持填写邮箱域名（如 qq.com）、子域通配（如 *.edu.cn）或指定完整邮箱地址。
              </Text>
            </div>
          </div>
        }
        width={880}
        onCancel={onClose}
        styles={{
          body: {
            padding: '16px 20px 8px',
            maxHeight: 'calc(85vh - 120px)',
            overflowY: 'auto',
          },
        }}
        footer={
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', width: '100%' }}>
            <div>
              <Text type="secondary" style={{ fontSize: 12 }}>
                共 <Text strong>{rules.length}</Text> 条规则
                {stats.invalidCount > 0 && (
                  <Text type="danger" style={{ marginLeft: 8 }}>
                    ({stats.invalidCount} 条格式异常)
                  </Text>
                )}
              </Text>
            </div>
            <Space>
              {mode === 'view' ? (
                <>
                  <Button icon={<CopyOutlined />} onClick={handleCopyAll}>
                    复制全部
                  </Button>
                  {onSwitchToEdit && (
                    <Button type="primary" icon={<EditOutlined />} onClick={onSwitchToEdit}>
                      切换到配置编辑
                    </Button>
                  )}
                  <Button onClick={onClose}>关闭</Button>
                </>
              ) : (
                <>
                  <Button onClick={onClose} disabled={saving}>
                    取消
                  </Button>
                  <Button
                    loading={savingMode === 'keep'}
                    disabled={saving}
                    onClick={() => handlePersistSave(false)}
                  >
                    保存更新
                  </Button>
                  <Button
                    type="primary"
                    loading={savingMode === 'close'}
                    disabled={saving}
                    onClick={() => handlePersistSave(true)}
                  >
                    保存更新关闭
                  </Button>
                </>
              )}
            </Space>
          </div>
        }
      >
        <Space orientation="vertical" size="middle" style={{ width: '100%' }}>
          {/* 编辑模式下的单条快速录入条 */}
          {mode === 'edit' && (
            <div
              style={{
                padding: '12px 14px',
                background: token.colorFillAlter,
                border: `1px solid ${token.colorBorderSecondary}`,
                borderRadius: 8,
              }}
            >
              <div style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
                <div style={{ flex: 1 }}>
                  <Input
                    placeholder="输入邮箱域名（如 qq.com、*.edu.cn）或指定完整邮箱（如 user@example.com），按回车快速添加"
                    value={singleInput}
                    status={singleInputError ? 'error' : ''}
                    onChange={(e) => {
                      setSingleInput(e.target.value);
                      if (singleInputError) setSingleInputError(null);
                    }}
                    onPressEnter={handleAddSingle}
                    allowClear
                  />
                  {singleInputError && (
                    <div style={{ marginTop: 4 }}>
                      <Text type="danger" style={{ fontSize: 12 }}>
                        {singleInputError}
                      </Text>
                    </div>
                  )}
                </div>
                <Button type="primary" icon={<PlusOutlined />} onClick={handleAddSingle}>
                  添加规则
                </Button>
              </div>
            </div>
          )}

          {/* 工具栏与过滤栏 */}
          <div
            style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              flexWrap: 'wrap',
              gap: 8,
            }}
          >
            {/* 左侧搜索与分类过滤 */}
            <Space size="middle" wrap>
              <Input
                placeholder="快速搜索域名 / 邮箱..."
                prefix={<SearchOutlined style={{ color: token.colorTextSecondary }} />}
                value={searchText}
                onChange={(e) => setSearchText(e.target.value)}
                allowClear
                style={{ width: 200 }}
              />
              <Radio.Group
                size="small"
                value={typeFilter}
                onChange={(e) => setTypeFilter(e.target.value)}
                optionType="button"
                buttonStyle="solid"
              >
                <Radio.Button value="all">全部 ({stats.total})</Radio.Button>
                <Radio.Button value="domain">邮箱域名 ({stats.domainCount})</Radio.Button>
                {stats.wildcardCount > 0 && (
                  <Radio.Button value="wildcard_domain">子域通配 ({stats.wildcardCount})</Radio.Button>
                )}
                <Radio.Button value="exact_email">指定邮箱 ({stats.emailCount})</Radio.Button>
                {stats.invalidCount > 0 && (
                  <Radio.Button value="invalid">异常 ({stats.invalidCount})</Radio.Button>
                )}
              </Radio.Group>
            </Space>

            {/* 右侧批量与功能按钮 */}
            <Space size="small" wrap>
              {selectedRowKeys.length > 0 && mode === 'edit' && (
                <Popconfirm
                  title={`确认批量删除选中的 ${selectedRowKeys.length} 条规则？`}
                  onConfirm={handleBatchDelete}
                  okText="删除"
                  cancelText="取消"
                  okButtonProps={{ danger: true }}
                >
                  <Button danger size="small" icon={<DeleteOutlined />}>
                    批量删除 ({selectedRowKeys.length})
                  </Button>
                </Popconfirm>
              )}

              {mode === 'edit' && (
                <>
                  <Button
                    size="small"
                    icon={<ImportOutlined />}
                    onClick={() => setImportModalOpen(true)}
                  >
                    批量导入
                  </Button>
                  <Tooltip title="一键将 qq.com、163.com、126.com、gmail.com、outlook.com 等常用主流邮箱域名追加至当前列表">
                    <Button size="small" icon={<ReloadOutlined />} onClick={handleApplyCommonDomains}>
                      填入常用域名
                    </Button>
                  </Tooltip>
                  {rules.length > 0 && (
                    <Popconfirm
                      title="确定要清空全部规则？此操作不可逆！"
                      onConfirm={handleClearAll}
                      okText="确认清空"
                      cancelText="取消"
                      okButtonProps={{ danger: true }}
                    >
                      <Button size="small" danger icon={<ClearOutlined />}>
                        清空
                      </Button>
                    </Popconfirm>
                  )}
                </>
              )}

              <Button size="small" icon={<CopyOutlined />} onClick={handleCopyAll}>
                一键复制全部
              </Button>
            </Space>
          </div>

          {/* 表格区 */}
          <Table
            size="small"
            rowKey="id"
            columns={columns}
            dataSource={filteredItems}
            rowSelection={
              mode === 'edit'
                ? {
                    selectedRowKeys,
                    onChange: (keys) => setSelectedRowKeys(keys),
                  }
                : undefined
            }
            pagination={{
              current: currentPage,
              pageSize,
              pageSizeOptions: ['10', '20', '50', '100', '200'],
              showSizeChanger: true,
              showQuickJumper: true,
              showTotal: (total) => `共 ${total} 条规则`,
              onChange: (page, size) => {
                setCurrentPage(page);
                setPageSize(size);
              },
            }}
            locale={{
              emptyText: (
                <Empty
                  image={Empty.PRESENTED_IMAGE_SIMPLE}
                  description={
                    searchText ? (
                      <span>未匹配到符合 "{searchText}" 的规则</span>
                    ) : (
                      <span>暂未配置任何允许的邮箱域名或邮箱</span>
                    )
                  }
                />
              ),
            }}
          />
        </Space>
      </Modal>

      {/* 批量导入子对话框 */}
      <Modal
        open={importModalOpen}
        title={
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <ImportOutlined style={{ color: token.colorPrimary }} />
            <span>批量导入允许的邮箱域名 / 邮箱列表</span>
          </div>
        }
        width={640}
        onCancel={() => {
          setImportModalOpen(false);
          setImportText('');
        }}
        onOk={handleConfirmImport}
        okText="确认导入"
        cancelText="取消"
      >
        <Space orientation="vertical" size="small" style={{ width: '100%' }}>
          <Alert
            type="info"
            showIcon
            message="支持海量邮箱域名或完整邮箱地址一次性粘贴。支持每行一条，亦支持英文逗号、中文逗号、分号或空格分隔。"
          />
          <div style={{ margin: '8px 0 4px' }}>
            <Text strong>导入方式：</Text>
            <Radio.Group
              style={{ marginLeft: 10 }}
              value={importMode}
              onChange={(e) => setImportMode(e.target.value)}
            >
              <Radio value="append">追加到现有列表 (自动去重)</Radio>
              <Radio value="overwrite">覆盖当前列表</Radio>
            </Radio.Group>
          </div>
          <div style={{ margin: '4px 0 6px' }}>
            <Text strong>解析模式：</Text>
            <Radio.Group
              style={{ marginLeft: 10 }}
              value={importParseMode}
              onChange={(e) => setImportParseMode(e.target.value)}
            >
              <Radio value="keep">保留原样 (同时支持域名与指定完整邮箱)</Radio>
              <Radio value="extract_domain">仅提取域名后缀 (如 user@qq.com 自动转为 qq.com)</Radio>
            </Radio.Group>
          </div>
          <TextArea
            rows={10}
            placeholder={`示例：\nqq.com\n163.com\n@gmail.com\n*.edu.cn\nvip_user@company.com`}
            value={importText}
            onChange={(e) => setImportText(e.target.value)}
            style={{ fontFamily: 'monospace', fontSize: 13 }}
          />
          <Paragraph type="secondary" style={{ fontSize: 12, margin: 0 }}>
            粘贴后系统将自动剔除空行与重复项，带 @ 前缀的域名（如 @qq.com）会自动规范化为标准域名（qq.com）。
          </Paragraph>
        </Space>
      </Modal>
    </>
  );
};
