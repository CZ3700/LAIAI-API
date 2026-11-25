import { useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';

const SLUG_TO_LABEL = {
  vision: '视觉',
  chat: '聊天',
  audio: '音频',
  embedding: '向量',
  rerank: '排序',
  other: '其它',
  all: '全部',
} as const;

type LogTypeSlug = keyof typeof SLUG_TO_LABEL;
type LogListKind = 'logs' | 'task-logs';

const DEFAULT_TYPE_STORAGE_KEY: Record<LogListKind, string> = {
  logs: 'default_log_type',
  'task-logs': 'default_task_log_type',
};

const LABEL_TO_SLUG: Record<string, LogTypeSlug> = {
  视觉: 'vision',
  聊天: 'chat',
  音频: 'audio',
  向量: 'embedding',
  排序: 'rerank',
  其它: 'other',
  全部: 'all',
  vision: 'vision',
  chat: 'chat',
  audio: 'audio',
  embedding: 'embedding',
  rerank: 'rerank',
  other: 'other',
  all: 'all',
};

function parseLogTypeSlug(raw: string | undefined | null): LogTypeSlug | null {
  if (!raw) return null;
  return LABEL_TO_SLUG[raw] ?? null;
}

function defaultLogTypeSlug(kind: LogListKind): LogTypeSlug {
  return parseLogTypeSlug(localStorage.getItem(DEFAULT_TYPE_STORAGE_KEY[kind])) ?? 'all';
}

function defaultLogTypeLabel(kind: LogListKind): string {
  return SLUG_TO_LABEL[defaultLogTypeSlug(kind)];
}

function logListBasePath(pathname: string, kind: LogListKind): string {
  const normalized = pathname.replace(/\/+$/, '') || '/';
  const marker = kind === 'task-logs' ? '/task-logs' : '/logs';
  const idx = normalized.lastIndexOf(marker);
  if (idx === -1) return normalized;
  return normalized.slice(0, idx + marker.length);
}

export function useLogTypeRoute(kind: LogListKind) {
  const { logType: logTypeParam } = useParams<{ logType?: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const resolvedSlug = parseLogTypeSlug(logTypeParam);
  const basePath = logListBasePath(location.pathname, kind);
  const actionTypeFilter = SLUG_TO_LABEL[resolvedSlug ?? defaultLogTypeSlug(kind)];
  const [defaultLogType, setDefaultLogTypeState] = useState(() => defaultLogTypeLabel(kind));

  const redirectTo =
    !resolvedSlug || logTypeParam !== resolvedSlug
      ? `${basePath}/${resolvedSlug ?? defaultLogTypeSlug(kind)}${location.search}`
      : null;

  const goLogType = (label: string) => {
    const slug = parseLogTypeSlug(label) ?? defaultLogTypeSlug(kind);
    const next = `${basePath}/${slug}${location.search}`;
    if (`${location.pathname}${location.search}` !== next) {
      navigate(next);
    }
  };

  const setDefaultLogType = (label: string) => {
    const slug = parseLogTypeSlug(label) ?? 'vision';
    const normalized = SLUG_TO_LABEL[slug];
    localStorage.setItem(DEFAULT_TYPE_STORAGE_KEY[kind], normalized);
    setDefaultLogTypeState(normalized);
  };

  return { actionTypeFilter, redirectTo, goLogType, defaultLogType, setDefaultLogType };
}
