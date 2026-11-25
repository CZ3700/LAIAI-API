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
import { copyWithFeedback } from '../../utils/clipboard';
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
  SafetyCertificateOutlined,
  EyeOutlined,
  ReloadOutlined,
} from '@ant-design/icons';

const { Text, Paragraph } = Typography;
const { TextArea } = Input;

type IpRuleType = 'ipv4' | 'ipv4_cidr' | 'ipv6' | 'ipv6_cidr' | 'invalid';

interface IpRuleItem {
  id: string;
  raw: string;
  type: IpRuleType;
  scopeDesc: string;
  isValid: boolean;
}

interface SecurityIpModalProps {
  open: boolean;
  type: 'blacklist' | 'whitelist';
  mode: 'view' | 'edit';
  initialList: string[];
  oppositeList?: string[];
  onClose: () => void;
  onSave?: (newList: string[]) => void;
  onSaveAndApply?: (newList: string[]) => Promise<boolean | void>;
  onSwitchToEdit?: () => void;
}

// 默认推荐内网段
const DEFAULT_WHITELIST_CIDRS = ['10.0.0.0/24', '192.168.1.0/24', '172.16.0.0/24', '127.0.0.0/24'];

interface ParsedIpRange {
  family: 4 | 6;
  start: bigint;
  end: bigint;
}

function isValidIpv4Part(part: string): boolean {
  if (!/^\d+$/.test(part)) return false;
  const n = parseInt(part, 10);
  if (n < 0 || n > 255) return false;
  if (part.length > 1 && part.startsWith('0')) return false;
  return true;
}

function isValidIpv4(ip: string): boolean {
  const parts = ip.split('.');
  if (parts.length !== 4) return false;
  return parts.every(isValidIpv4Part);
}

function parseIpv4ToBigInt(ip: string): bigint | null {
  if (!isValidIpv4(ip)) return null;
  const [a, b, c, d] = ip.split('.').map((p) => BigInt(parseInt(p, 10)));
  return (a << 24n) | (b << 16n) | (c << 8n) | d;
}

function parseIpv6ToBigInt(rawIp: string): { family: 4 | 6; value: bigint } | null {
  const s = rawIp.trim().toLowerCase();
  if (!s.includes(':')) return null;

  if (s.startsWith('::ffff:') && s.includes('.')) {
    const v4Part = s.slice('::ffff:'.length);
    const v4Val = parseIpv4ToBigInt(v4Part);
    if (v4Val !== null) {
      return { family: 4, value: v4Val };
    }
  }

  const doubleColonParts = s.split('::');
  if (doubleColonParts.length > 2) return null;

  const parseHextets = (segment: string): number[] | null => {
    if (!segment) return [];
    const tokens = segment.split(':');
    const out: number[] = [];
    for (let i = 0; i < tokens.length; i++) {
      const tok = tokens[i];
      if (tok.includes('.')) {
        if (i !== tokens.length - 1) return null;
        const v4 = parseIpv4ToBigInt(tok);
        if (v4 === null) return null;
        out.push(Number((v4 >> 16n) & 0xffffn), Number(v4 & 0xffffn));
      } else {
        if (!/^[0-9a-f]{1,4}$/.test(tok)) return null;
        out.push(parseInt(tok, 16));
      }
    }
    return out;
  };

  let hextets: number[];
  if (doubleColonParts.length === 2) {
    const left = parseHextets(doubleColonParts[0]);
    const right = parseHextets(doubleColonParts[1]);
    if (!left || !right || left.length + right.length >= 8) return null;
    const missing = 8 - (left.length + right.length);
    hextets = [...left, ...new Array(missing).fill(0), ...right];
  } else {
    const full = parseHextets(s);
    if (!full || full.length !== 8) return null;
    hextets = full;
  }

  let val = 0n;
  for (const h of hextets) {
    val = (val << 16n) | BigInt(h);
  }
  return { family: 6, value: val };
}

function parseIpRange(rawStr: string): ParsedIpRange | null {
  const trimmed = rawStr.trim();
  if (!trimmed) return null;

  if (trimmed.includes('/')) {
    const parts = trimmed.split('/');
    if (parts.length !== 2) return null;
    const [ipPart, maskPart] = parts;
    if (!/^\d+$/.test(maskPart)) return null;
    const mask = parseInt(maskPart, 10);

    if (!ipPart.includes(':')) {
      if (mask < 0 || mask > 32) return null;
      const ipVal = parseIpv4ToBigInt(ipPart);
      if (ipVal === null) return null;
      const allOnes = (1n << 32n) - 1n;
      const netMask = mask === 0 ? 0n : ((allOnes << BigInt(32 - mask)) & allOnes);
      const start = ipVal & netMask;
      const end = start | (allOnes ^ netMask);
      return { family: 4, start, end };
    } else {
      if (mask < 0 || mask > 128) return null;
      const parsed = parseIpv6ToBigInt(ipPart);
      if (!parsed) return null;
      if (parsed.family === 4) {
        const v4Mask = mask >= 96 ? mask - 96 : -1;
        if (v4Mask < 0 || v4Mask > 32) return null;
        const allOnes = (1n << 32n) - 1n;
        const netMask = v4Mask === 0 ? 0n : ((allOnes << BigInt(32 - v4Mask)) & allOnes);
        const start = parsed.value & netMask;
        const end = start | (allOnes ^ netMask);
        return { family: 4, start, end };
      }
      const allOnes = (1n << 128n) - 1n;
      const netMask = mask === 0 ? 0n : ((allOnes << BigInt(128 - mask)) & allOnes);
      const start = parsed.value & netMask;
      const end = start | (allOnes ^ netMask);
      return { family: 6, start, end };
    }
  }

  if (!trimmed.includes(':')) {
    const ipVal = parseIpv4ToBigInt(trimmed);
    if (ipVal === null) return null;
    return { family: 4, start: ipVal, end: ipVal };
  }

  const parsed = parseIpv6ToBigInt(trimmed);
  if (!parsed) return null;
  return { family: parsed.family, start: parsed.value, end: parsed.value };
}

function ipRangesOverlap(a: ParsedIpRange, b: ParsedIpRange): boolean {
  return a.family === b.family && a.start <= b.end && b.start <= a.end;
}

function ipRangesEqual(a: ParsedIpRange, b: ParsedIpRange): boolean {
  return a.family === b.family && a.start === b.start && a.end === b.end;
}

function ipRangeContains(outer: ParsedIpRange, inner: ParsedIpRange): boolean {
  return outer.family === inner.family && outer.start <= inner.start && outer.end >= inner.end;
}

function canonicalizeIpOrCidr(rawStr: string): string {
  const trimmed = rawStr.trim();
  if (trimmed.includes('/') && !trimmed.includes(':')) {
    const [ipPart, maskPart] = trimmed.split('/');
    if (maskPart && /^\d+$/.test(maskPart) && isValidIpv4(ipPart)) {
      const mask = parseInt(maskPart, 10);
      if (mask >= 0 && mask <= 32) {
        const ipVal = parseIpv4ToBigInt(ipPart);
        if (ipVal !== null) {
          const allOnes = (1n << 32n) - 1n;
          const netMask = mask === 0 ? 0n : ((allOnes << BigInt(32 - mask)) & allOnes);
          const net = ipVal & netMask;
          const a = Number((net >> 24n) & 255n);
          const b = Number((net >> 16n) & 255n);
          const c = Number((net >> 8n) & 255n);
          const d = Number(net & 255n);
          return `${a}.${b}.${c}.${d}/${mask}`;
        }
      }
    }
  }
  return trimmed;
}

export function findExactIpRule(candidate: string, list: string[]): string | null {
  const candidateRange = parseIpRange(candidate);
  if (!candidateRange) return null;
  for (const item of list) {
    const r = parseIpRange(item);
    if (r && ipRangesEqual(candidateRange, r)) {
      return item.trim();
    }
  }
  return null;
}

export function findCoveringIpRule(candidate: string, list: string[]): string | null {
  const candidateRange = parseIpRange(candidate);
  if (!candidateRange) return null;
  for (const item of list) {
    const r = parseIpRange(item);
    if (r && ipRangeContains(r, candidateRange)) {
      return item.trim();
    }
  }
  return null;
}

function findOverlappingDiffRule(candidate: string, list: string[]): string | null {
  const candidateRange = parseIpRange(candidate);
  if (!candidateRange) return null;
  for (const item of list) {
    const r = parseIpRange(item);
    if (r && ipRangesOverlap(candidateRange, r) && !ipRangesEqual(candidateRange, r)) {
      return item.trim();
    }
  }
  return null;
}

export function validateSiteIpLists(blacklist: string[], whitelist: string[]): string | null {
  const checkSelf = (items: string[], label: string): { raw: string; range: ParsedIpRange }[] | string => {
    const parsed: { raw: string; range: ParsedIpRange }[] = [];
    for (const raw of items) {
      const trimmed = canonicalizeIpOrCidr(raw);
      if (!trimmed) continue;
      const range = parseIpRange(trimmed);
      if (!range) {
        return `${label}中存在格式无效的 IP 或网段：${trimmed}`;
      }
      const conflict = parsed.find((p) => ipRangeContains(p.range, range) || ipRangeContains(range, p.range));
      if (conflict) {
        return `${label}中存在重复或包含关系的 IP/网段：「${conflict.raw}」与「${trimmed}」`;
      }
      parsed.push({ raw: trimmed, range });
    }
    return parsed;
  };

  const blackParsed = checkSelf(blacklist, '注册 IP 黑名单');
  if (typeof blackParsed === 'string') return blackParsed;
  const whiteParsed = checkSelf(whitelist, '注册 IP 白名单');
  if (typeof whiteParsed === 'string') return whiteParsed;

  for (const b of blackParsed) {
    const wConflict = whiteParsed.find((w) => ipRangesEqual(b.range, w.range));
    if (wConflict) {
      return `注册 IP 黑白名单同级互斥：黑名单「${b.raw}」与白名单「${wConflict.raw}」表示完全相同的 IP 或网段，不能同时出现`;
    }
  }
  return null;
}

function parseIpRule(rawStr: string, index: number): IpRuleItem {
  const trimmed = rawStr.trim();
  const id = `${index}-${trimmed}`;

  if (!trimmed) {
    return { id, raw: '', type: 'invalid', scopeDesc: '空内容', isValid: false };
  }

  // 检测 IPv4 CIDR
  if (trimmed.includes('/') && !trimmed.includes(':')) {
    const [ipPart, maskPart] = trimmed.split('/');
    if (maskPart && /^\d+$/.test(maskPart)) {
      const mask = parseInt(maskPart, 10);
      if (mask >= 0 && mask <= 32 && isValidIpv4(ipPart)) {
        let countText = '';
        if (mask === 32) {
          countText = '1 个主机';
        } else if (mask === 0) {
          countText = '全网 IPv4 (42.9亿)';
        } else {
          const count = Math.pow(2, 32 - mask);
          countText = `${count.toLocaleString()} 个地址`;
        }
        return {
          id,
          raw: trimmed,
          type: 'ipv4_cidr',
          scopeDesc: countText,
          isValid: true,
        };
      }
    }
  }

  // 检测 IPv4 单主机
  if (isValidIpv4(trimmed)) {
    return {
      id,
      raw: trimmed,
      type: 'ipv4',
      scopeDesc: '1 个主机',
      isValid: true,
    };
  }

  // 检测 IPv6 CIDR
  if (trimmed.includes(':') && trimmed.includes('/')) {
    const [, maskPart] = trimmed.split('/');
    if (maskPart && /^\d+$/.test(maskPart) && parseIpRange(trimmed) !== null) {
      const mask = parseInt(maskPart, 10);
      return {
        id,
        raw: trimmed,
        type: 'ipv6_cidr',
        scopeDesc: `IPv6 子网 (/${mask})`,
        isValid: true,
      };
    }
  }

  // 检测 IPv6 单地址
  if (trimmed.includes(':') && !trimmed.includes('/') && parseIpRange(trimmed) !== null) {
    return {
      id,
      raw: trimmed,
      type: 'ipv6',
      scopeDesc: '1 个主机',
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

export const SecurityIpModal: React.FC<SecurityIpModalProps> = ({
  open,
  type,
  mode,
  initialList,
  oppositeList = [],
  onClose,
  onSave,
  onSaveAndApply,
  onSwitchToEdit,
}) => {
  const { token } = theme.useToken();

  const isBlacklist = type === 'blacklist';
  const titlePrefix = isBlacklist ? '注册 IP 黑名单' : '注册 IP 白名单';
  const oppositeTitle = isBlacklist ? '注册 IP 白名单' : '注册 IP 黑名单';

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

  // 批量导入抽屉/子对话框状态
  const [importModalOpen, setImportModalOpen] = useState(false);
  const [importText, setImportText] = useState('');
  const [importMode, setImportMode] = useState<'append' | 'overwrite'>('append');

  // 保存中状态（区分「保存更新」与「保存更新关闭」按钮 loading）
  const [savingMode, setSavingMode] = useState<'keep' | 'close' | null>(null);
  const saving = savingMode !== null;
  // 分页状态（用于跨页连续显示序号 #）
  const [currentPage, setCurrentPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);

  // 初始化列表（仅在弹窗打开或切换黑白名单类型时初始化，避免父组件每秒时钟刷新传入新数组引用导致输入被清空）
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
  }, [open, type]);

  // 解析所有的规则条目
  const parsedItems: IpRuleItem[] = useMemo(() => {
    return rules.map((r, i) => parseIpRule(r, i));
  }, [rules]);

  // 筛选后的展示列表
  const filteredItems = useMemo(() => {
    return parsedItems.filter((item) => {
      // 关键字搜索
      if (searchText.trim()) {
        const query = searchText.trim().toLowerCase();
        if (!item.raw.toLowerCase().includes(query)) {
          return false;
        }
      }
      // 分类筛选
      if (typeFilter === 'ipv4' && item.type !== 'ipv4') return false;
      if (typeFilter === 'ipv4_cidr' && item.type !== 'ipv4_cidr') return false;
      if (typeFilter === 'ipv6' && item.type !== 'ipv6' && item.type !== 'ipv6_cidr') return false;
      if (typeFilter === 'invalid' && item.isValid) return false;
      return true;
    });
  }, [parsedItems, searchText, typeFilter]);

  // 统计概览
  const stats = useMemo(() => {
    let ipv4Count = 0;
    let cidrCount = 0;
    let ipv6Count = 0;
    let invalidCount = 0;
    for (const item of parsedItems) {
      if (!item.isValid) {
        invalidCount++;
      } else if (item.type === 'ipv4') {
        ipv4Count++;
      } else if (item.type === 'ipv4_cidr') {
        cidrCount++;
      } else if (item.type === 'ipv6' || item.type === 'ipv6_cidr') {
        ipv6Count++;
      }
    }
    return { total: parsedItems.length, ipv4Count, cidrCount, ipv6Count, invalidCount };
  }, [parsedItems]);

  // 单条添加规则
  const handleAddSingle = () => {
    const rawVal = singleInput.trim();
    if (!rawVal) {
      setSingleInputError('请输入 IP 或 CIDR 网段');
      return;
    }
    const val = canonicalizeIpOrCidr(rawVal);
    const testItem = parseIpRule(val, 0);
    if (!testItem.isValid) {
      setSingleInputError('格式不符合 IPv4/IPv6 或 CIDR 规范');
      return;
    }
    const selfCovering = findCoveringIpRule(val, rules);
    if (selfCovering) {
      setSingleInputError(
        selfCovering === val
          ? '该条规则已存在，请勿重复添加'
          : `已被当前列表中的「${selfCovering}」包含覆盖，无需重复添加`
      );
      return;
    }
    const oppositeExact = findExactIpRule(val, oppositeList);
    if (oppositeExact) {
      setSingleInputError(
        `黑白名单同级互斥：与${oppositeTitle}中的「${oppositeExact}」表示完全相同的 IP 或网段，不能同时出现`
      );
      return;
    }

    const candRange = parseIpRange(val)!;
    const absorbed: string[] = [];
    const remaining = rules.filter((r) => {
      const rr = parseIpRange(r);
      if (rr && ipRangeContains(candRange, rr)) {
        absorbed.push(r);
        return false;
      }
      return true;
    });

    setRules([val, ...remaining]);
    setSingleInput('');
    setSingleInputError(null);

    const oppDiff = findOverlappingDiffRule(val, oppositeList);
    const notes: string[] = [];
    if (absorbed.length > 0) {
      notes.push(`已自动合并列表内被覆盖的 ${absorbed.length} 条子规则`);
    }
    if (oppDiff) {
      notes.push(`与${oppositeTitle}「${oppDiff}」构成子网例外，按最长前缀精确匹配优先`);
    }
    message.success(notes.length > 0 ? `已添加规则：${val}（${notes.join('；')}）` : `已添加规则：${val}`);
  };

  // 快捷填入白名单默认网段
  const handleApplyDefaultWhitelist = () => {
    let next = [...rules];
    const skippedOpposite: string[] = [];
    let addedCount = 0;
    for (const cidr of DEFAULT_WHITELIST_CIDRS) {
      if (findCoveringIpRule(cidr, next)) {
        continue;
      }
      if (findExactIpRule(cidr, oppositeList)) {
        skippedOpposite.push(cidr);
        continue;
      }
      const cidrRange = parseIpRange(cidr);
      if (cidrRange) {
        next = next.filter((r) => {
          const rr = parseIpRange(r);
          return !(rr && ipRangeContains(cidrRange, rr));
        });
      }
      next.push(cidr);
      addedCount++;
    }
    setRules(next);
    if (skippedOpposite.length > 0) {
      message.warning(
        `已填入 ${addedCount} 条推荐网段；其中 ${skippedOpposite.join('、')} 与${oppositeTitle}同级互斥已跳过`
      );
    } else if (addedCount > 0) {
      message.success('已填入默认推荐内网段');
    } else {
      message.info('推荐内网段已全部在当前列表中');
    }
  };

  // 一键复制全部
  const handleCopyAll = () => {
    if (rules.length === 0) {
      message.info('当前规则列表为空');
      return;
    }
    const content = rules.join('\n');
    void copyWithFeedback(content, `已成功复制全部 ${rules.length} 条规则至剪贴板`, '复制失败，请手动选择复制');
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
    const rawVal = editingText.trim();
    if (!rawVal) {
      message.error('IP 或网段内容不能为空');
      return;
    }
    const val = canonicalizeIpOrCidr(rawVal);
    const testItem = parseIpRule(val, 0);
    if (!testItem.isValid) {
      message.error('格式不符合 IPv4/IPv6 或 CIDR 规范');
      return;
    }
    const idx = rules.indexOf(oldRaw);
    if (idx !== -1) {
      const otherRules = rules.filter((_, i) => i !== idx);
      const selfCovering = findCoveringIpRule(val, otherRules);
      if (selfCovering) {
        message.error(
          selfCovering === val
            ? '目标规则已存在，请勿重复'
            : `已被当前列表中的「${selfCovering}」包含覆盖`
        );
        return;
      }
      const oppositeExact = findExactIpRule(val, oppositeList);
      if (oppositeExact) {
        message.error(
          `黑白名单同级互斥：与${oppositeTitle}中的「${oppositeExact}」表示完全相同的 IP 或网段，不能同时出现`
        );
        return;
      }
      const candRange = parseIpRange(val)!;
      const next: string[] = [];
      rules.forEach((r, i) => {
        if (i === idx) {
          next.push(val);
          return;
        }
        const rr = parseIpRange(r);
        if (rr && ipRangeContains(candRange, rr)) {
          return;
        }
        next.push(r);
      });
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
    // 支持按换行、分号、逗号、空格分割
    const lines = importText
      .split(/[\r\n,;\s]+/)
      .map((s) => canonicalizeIpOrCidr(s))
      .filter(Boolean);

    if (lines.length === 0) {
      message.warning('未识别到有效条目');
      return;
    }

    let baseList = importMode === 'overwrite' ? [] : [...rules];
    let added = 0;
    let invalidCount = 0;
    let overlapSelfCount = 0;
    let conflictOppositeCount = 0;

    for (const line of lines) {
      if (!parseIpRule(line, 0).isValid) {
        invalidCount++;
        continue;
      }
      if (findExactIpRule(line, oppositeList)) {
        conflictOppositeCount++;
        continue;
      }
      if (findCoveringIpRule(line, baseList)) {
        overlapSelfCount++;
        continue;
      }
      const lineRange = parseIpRange(line);
      if (lineRange) {
        baseList = baseList.filter((r) => {
          const rr = parseIpRange(r);
          return !(rr && ipRangeContains(lineRange, rr));
        });
      }
      baseList.push(line);
      added++;
    }

    if (added === 0 && (invalidCount > 0 || overlapSelfCount > 0 || conflictOppositeCount > 0)) {
      const reasons: string[] = [];
      if (conflictOppositeCount > 0) reasons.push(`${conflictOppositeCount} 条与${oppositeTitle}同级互斥冲突`);
      if (overlapSelfCount > 0) reasons.push(`${overlapSelfCount} 条重复或已被现有网段覆盖`);
      if (invalidCount > 0) reasons.push(`${invalidCount} 条格式无效`);
      message.error(`未能导入有效规则：${reasons.join('，')}`);
      return;
    }

    setRules(baseList);
    const skipParts: string[] = [];
    if (conflictOppositeCount > 0) skipParts.push(`${conflictOppositeCount} 条与${oppositeTitle}同级互斥已跳过`);
    if (overlapSelfCount > 0) skipParts.push(`${overlapSelfCount} 条重复/已被覆盖项已去重`);
    if (invalidCount > 0) skipParts.push(`${invalidCount} 条格式无效已忽略`);
    const suffix = skipParts.length > 0 ? `（${skipParts.join('，')}）` : '';

    if (importMode === 'overwrite') {
      message.success(`已覆盖导入 ${added} 条规则${suffix}`);
    } else {
      message.success(`批量追加完成：新增 ${added} 条规则${suffix}`);
    }

    setImportModalOpen(false);
    setImportText('');
  };

  // 若输入框中仍有未点击「添加规则」的待添加内容，在保存前自动校验并合并
  const resolveRulesBeforeSave = (): string[] | null => {
    let candidateRules = rules;
    const rawVal = singleInput.trim();
    if (rawVal) {
      const val = canonicalizeIpOrCidr(rawVal);
      const testItem = parseIpRule(val, 0);
      if (!testItem.isValid) {
        setSingleInputError('格式不符合 IPv4/IPv6 或 CIDR 规范');
        return null;
      }
      const selfCovering = findCoveringIpRule(val, rules);
      if (selfCovering) {
        setSingleInputError(
          selfCovering === val
            ? '该条规则已存在，请勿重复添加'
            : `已被当前列表中的「${selfCovering}」包含覆盖`
        );
        return null;
      }
      const oppositeExact = findExactIpRule(val, oppositeList);
      if (oppositeExact) {
        setSingleInputError(
          `黑白名单同级互斥：与${oppositeTitle}中的「${oppositeExact}」表示完全相同的 IP 或网段，不能同时出现`
        );
        return null;
      }
      const candRange = parseIpRange(val)!;
      const remaining = rules.filter((r) => {
        const rr = parseIpRange(r);
        return !(rr && ipRangeContains(candRange, rr));
      });
      candidateRules = [val, ...remaining];
    }

    const validationError = validateSiteIpLists(
      isBlacklist ? candidateRules : oppositeList,
      isBlacklist ? oppositeList : candidateRules
    );
    if (validationError) {
      message.error(validationError);
      return null;
    }

    if (rawVal) {
      setRules(candidateRules);
      setSingleInput('');
      setSingleInputError(null);
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
  const renderTypeTag = (itemType: IpRuleType, isValid: boolean) => {
    if (!isValid || itemType === 'invalid') {
      return <Tag color="error">格式异常</Tag>;
    }
    switch (itemType) {
      case 'ipv4':
        return <Tag color="processing">IPv4 单地址</Tag>;
      case 'ipv4_cidr':
        return <Tag color="cyan">IPv4 CIDR</Tag>;
      case 'ipv6':
        return <Tag color="purple">IPv6 单地址</Tag>;
      case 'ipv6_cidr':
        return <Tag color="magenta">IPv6 CIDR</Tag>;
      default:
        return <Tag>未知</Tag>;
    }
  };

  // 表格列定义
  const columns: TableColumnsType<IpRuleItem> = [
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
      title: 'IP / CIDR 网段',
      dataIndex: 'raw',
      key: 'raw',
      render: (raw: string, record: IpRuleItem) => {
        if (mode === 'edit' && editingId === record.id) {
          return (
            <Space size={4}>
              <Input
                size="small"
                value={editingText}
                onChange={(e) => setEditingText(e.target.value)}
                onPressEnter={() => handleSaveEdit(raw)}
                style={{ width: 220 }}
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
      width: 130,
      render: (_: any, record: IpRuleItem) => renderTypeTag(record.type, record.isValid),
    },
    {
      title: '预估作用范围',
      dataIndex: 'scopeDesc',
      key: 'scopeDesc',
      width: 180,
      render: (scopeDesc: string, record: IpRuleItem) => (
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
            render: (_: any, record: IpRuleItem) => (
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
            <SafetyCertificateOutlined
              style={{
                fontSize: 20,
                color: isBlacklist ? token.colorError : token.colorSuccess,
              }}
            />
            <div>
              <div style={{ fontSize: 16, fontWeight: 600 }}>
                {mode === 'view' ? `查看明细：${titlePrefix}` : `配置 ${titlePrefix} 规则`}
              </div>
              <Text type="secondary" style={{ fontSize: 12, fontWeight: 'normal' }}>
                {isBlacklist
                  ? '拦截名单内的 IP 访问登录、注册与找回密码等高危接口。支持单个 IP 与 CIDR 网段；黑白名单重叠时按最长前缀精确匹配（LPM）优先生效。'
                  : '白名单内的 IP 免受注册频次限制；与黑名单构成子网包含关系时按最长前缀精确匹配（LPM）决定放行或拦截。'}
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
                    placeholder="输入单个 IP 或 CIDR 网段，例如：192.168.1.100 或 10.0.0.0/8，按回车快速添加"
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
                placeholder="快速搜索 IP / 网段..."
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
                <Radio.Button value="ipv4">单 IP ({stats.ipv4Count})</Radio.Button>
                <Radio.Button value="ipv4_cidr">CIDR 网段 ({stats.cidrCount})</Radio.Button>
                {stats.ipv6Count > 0 && <Radio.Button value="ipv6">IPv6 ({stats.ipv6Count})</Radio.Button>}
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
                  {!isBlacklist && (
                    <Tooltip title="一键将 10.0.0.0/24、192.168.1.0/24、172.16.0.0/24、127.0.0.0/24 追加至当前白名单">
                      <Button size="small" icon={<ReloadOutlined />} onClick={handleApplyDefaultWhitelist}>
                        填入推荐内网段
                      </Button>
                    </Tooltip>
                  )}
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
                      <span>暂未配置任何规则</span>
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
            <span>批量导入 IP / CIDR 网段规则</span>
          </div>
        }
        width={620}
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
            message="支持海量数据一次性粘贴。支持每行一条，亦支持英文逗号、中文逗号、分号或空格分隔。"
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
          <TextArea
            rows={10}
            placeholder={`示例：\n192.168.1.100\n10.0.0.0/8\n172.16.0.0/16\n2001:db8::/32`}
            value={importText}
            onChange={(e) => setImportText(e.target.value)}
            style={{ fontFamily: 'monospace', fontSize: 13 }}
          />
          <Paragraph type="secondary" style={{ fontSize: 12, margin: 0 }}>
            粘贴后系统将自动剔除空行并校验格式，异常或重复条目将给出友好统计。
          </Paragraph>
        </Space>
      </Modal>
    </>
  );
};
