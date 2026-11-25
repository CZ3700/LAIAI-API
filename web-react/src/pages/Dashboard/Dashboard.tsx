import React, { useEffect, useState, useMemo, useRef } from 'react';
import { Link, useLocation, useNavigate, useOutletContext } from 'react-router-dom';
import { getAnnouncementLabel, getAnnouncementDisplayTime } from '../../utils/announcement';
import { Row, Col, Card, Typography, List, Progress, Alert, Grid, Spin, Modal, Button, Divider, Tooltip as AntTooltip, Radio, Tag } from 'antd';
import {
  CloseOutlined,
  BellOutlined,
} from '@ant-design/icons';
import {
  LineChart as LineChartIcon,
  PieChart as PieChartIcon,
  Calendar as CalendarIcon,
  ArrowRight,
  Activity,
  Zap,
  User as UserIcon,
  RefreshCw,
  TrendingUp,
  TrendingDown,
} from 'lucide-react';
import { useTranslation } from 'react-i18next';
import request from '../../utils/request';
import useSettingsStore from '../../store/settings';
import useAuthStore from '../../store/auth';
import { useThemeStore } from '../../store/theme';
import type { DashboardStats, RequestLog, Announcement, LiveMetricsResponse, LiveMetricsSnapshot } from '../../types';
import dayjs from 'dayjs';
import { statsCalendarNow, toCalendarDateRangeParams } from '../../utils/dateRangeParams';
import UsageAreaChart from './UsageAreaChart';

const { Title, Text } = Typography;

const getStatusMeta = (code: number, isLight: boolean) => {
  const isSuccess = code >= 200 && code < 300;
  return {
    dot: isSuccess 
      ? (isLight ? '#18181b' : '#fafafa') 
      : (isLight ? '#71717a' : '#a1a1aa'),
    text: isSuccess 
      ? (isLight ? '#09090b' : '#fafafa') 
      : (isLight ? '#71717a' : '#a1a1aa'),
  };
};

const getEndpointTag = (endpoint?: string) => {
  if (!endpoint) return null;
  const lower = endpoint.toLowerCase();
  if (lower.includes('/chat/completions')) return 'chat';
  if (lower.includes('/completions')) return 'completion';
  if (lower.includes('/embeddings')) return 'embed';
  if (lower.includes('/images') || lower.includes('/image')) return 'image';
  if (lower.includes('/audio') || lower.includes('/voice')) return 'audio';
  if (lower.includes('/video')) return 'video';
  if (lower.includes('/models')) return 'models';
  const parts = endpoint.split('/').filter(Boolean);
  return parts.length > 0 ? parts[parts.length - 1].slice(0, 10) : null;
};

const Dashboard: React.FC = () => {
  const { t, i18n } = useTranslation();
  const screens = Grid.useBreakpoint();
  const { settings } = useSettingsStore();
  const { user } = useAuthStore();
  const { themeMode } = useThemeStore();
  const _isLight = themeMode === 'light';
  const currencySymbol = settings?.currency?.currency_symbol || '$';
  const [stats, setStats] = useState<DashboardStats | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [liveMetrics, setLiveMetrics] = useState<LiveMetricsSnapshot | null>(null);
  const [liveMetricsReady, setLiveMetricsReady] = useState(false);
  const outletContext = useOutletContext<{
    announcements: Announcement[];
    notifyConsolePageReady?: () => void;
  } | null>();
  const notifyConsolePageReady = outletContext?.notifyConsolePageReady;
  const [currentPinnedIndex, setCurrentPinnedIndex] = useState<number>(0);
  const [isPinnedBannerHovered, setIsPinnedBannerHovered] = useState(false);
  const [selectedNoticeForModal, setSelectedNoticeForModal] = useState<Announcement | null>(null);
  const [isNoticeModalOpen, setIsNoticeModalOpen] = useState(false);
  const [isNoticeTruncated, setIsNoticeTruncated] = useState(false);
  const noticeTitleRef = useRef<HTMLSpanElement>(null);
  const noticeContentRef = useRef<HTMLDivElement>(null);
  const initializedPinnedRef = useRef(false);

  const handleOpenNoticeDetail = (notice: Announcement) => {
    setSelectedNoticeForModal(notice);
    setIsNoticeModalOpen(true);
  };

  const pinnedAnnouncements = useMemo(() => {
    const raw = outletContext?.announcements?.filter((a) => a.is_pinned === 1 && a.is_active === 1) || [];
    const seen = new Set<number>();
    const list: Announcement[] = [];
    for (const item of raw) {
      if (!seen.has(item.id)) {
        seen.add(item.id);
        list.push(item);
      }
    }
    return list;
  }, [outletContext?.announcements]);

  // 当置顶通知数据到达后，随机展示一条（优先不与上次展示的相同），并重置初始化标记
  useEffect(() => {
    if (pinnedAnnouncements.length === 0) {
      setCurrentPinnedIndex(0);
      initializedPinnedRef.current = false;
      return;
    }

    if (!initializedPinnedRef.current) {
      initializedPinnedRef.current = true;
      if (pinnedAnnouncements.length === 1) {
        setCurrentPinnedIndex(0);
        try {
          sessionStorage.setItem('last_dashboard_pinned_id', String(pinnedAnnouncements[0].id));
        } catch {}
        return;
      }

      const lastId = sessionStorage.getItem('last_dashboard_pinned_id');
      const candidateIndices = pinnedAnnouncements
        .map((a, idx) => ({ id: a.id, idx }))
        .filter((item) => String(item.id) !== lastId);

      const chosenIdx =
        candidateIndices.length > 0
          ? candidateIndices[Math.floor(Math.random() * candidateIndices.length)].idx
          : Math.floor(Math.random() * pinnedAnnouncements.length);

      setCurrentPinnedIndex(chosenIdx);
      try {
        sessionStorage.setItem('last_dashboard_pinned_id', String(pinnedAnnouncements[chosenIdx].id));
      } catch {}
    } else {
      setCurrentPinnedIndex((prev) => (prev < pinnedAnnouncements.length ? prev : 0));
    }
  }, [pinnedAnnouncements]);

  // 当存在多条置顶通知时，定时自动轮播切换，悬浮暂停
  useEffect(() => {
    if (pinnedAnnouncements.length <= 1 || isPinnedBannerHovered) return;

    const timer = setInterval(() => {
      setCurrentPinnedIndex((prev) => {
        const next = (prev + 1) % pinnedAnnouncements.length;
        try {
          if (pinnedAnnouncements[next]) {
            sessionStorage.setItem('last_dashboard_pinned_id', String(pinnedAnnouncements[next].id));
          }
        } catch {}
        return next;
      });
    }, 8000);

    return () => clearInterval(timer);
  }, [pinnedAnnouncements, isPinnedBannerHovered]);

  const pinnedAnnouncement = pinnedAnnouncements[currentPinnedIndex] || pinnedAnnouncements[0] || null;

  // 检测当前置顶通知是否内容超出/显示不完整（超出1行标题或2行正文或含隐藏图片/特殊标签）
  useEffect(() => {
    if (!pinnedAnnouncement) {
      setIsNoticeTruncated(false);
      return;
    }

    const checkTruncation = () => {
      let truncated = false;
      if (noticeTitleRef.current) {
        if (noticeTitleRef.current.scrollWidth > noticeTitleRef.current.clientWidth + 1) {
          truncated = true;
        }
      }
      if (noticeContentRef.current) {
        if (noticeContentRef.current.scrollHeight > noticeContentRef.current.clientHeight + 1) {
          truncated = true;
        }
      }
      const contentHtml = getAnnouncementLabel(pinnedAnnouncement.content) || '';
      if (/<img|<table|<pre|<blockquote|<hr/i.test(contentHtml)) {
        truncated = true;
      }
      setIsNoticeTruncated(truncated);
    };

    const animId = requestAnimationFrame(checkTruncation);
    window.addEventListener('resize', checkTruncation);

    return () => {
      cancelAnimationFrame(animId);
      window.removeEventListener('resize', checkTruncation);
    };
  }, [pinnedAnnouncement, currentPinnedIndex, i18n.language]);
  const isAdmin = user?.role === 'admin';
  const location = useLocation();
  const navigate = useNavigate();
  const configuredAdminPath = settings?.site?.admin_path || localStorage.getItem('laiai_admin_path') || 'admin';
  const pathFirstSegment = location.pathname.split('/')[1] || '';
  const currentAdminPath = (pathFirstSegment && pathFirstSegment !== 'dashboard') ? pathFirstSegment : configuredAdminPath;
  const isAdminView = location.pathname === `/${currentAdminPath}` || location.pathname.startsWith(`/${currentAdminPath}/`);
  const basePath = isAdminView ? `/${currentAdminPath}` : '';
  const logsPath = `${basePath}/logs`;
  const taskLogsPath = `${basePath}/task-logs`;
  const [dateRange, setDateRange] = useState<[any, any] | null>(() => {
    const now = statsCalendarNow();
    return [now.startOf('day'), now.endOf('day')];
  });
  const [quickRange, setQuickRange] = useState<string>('today');

  const handleQuickRangeChange = (e: any) => {
    const val = e.target.value;
    const now = statsCalendarNow();
    setQuickRange(val);
    if (val === 'today') {
      setDateRange([now.startOf('day'), now.endOf('day')]);
    } else if (val === 'yesterday') {
      const y = now.subtract(1, 'day');
      setDateRange([y.startOf('day'), y.endOf('day')]);
    } else if (val === 'week') {
      setDateRange([now.startOf('week'), now.endOf('week')]);
    } else if (val === 'month') {
      setDateRange([now.startOf('month'), now.endOf('month')]);
    } else if (val === 'last_month') {
      const prev = now.subtract(1, 'month');
      setDateRange([prev.startOf('month'), prev.endOf('month')]);
    }
  };

  const showDayComparison = quickRange === 'today' || quickRange === 'yesterday';

  const periodLabel = (() => {
    switch (quickRange) {
      case 'today':
        return t('dashboard.today');
      case 'yesterday':
        return t('dashboard.yesterday');
      case 'week':
        return t('dashboard.week');
      case 'month':
        return t('dashboard.month');
      case 'last_month':
        return t('dashboard.last_month');
      default:
        if (dateRange?.[0] && dateRange?.[1]) {
          const sameYear = dateRange[0].year() === dateRange[1].year();
          const fmt = sameYear ? 'MM-DD' : 'YYYY-MM-DD';
          return `${dateRange[0].format(fmt)} ~ ${dateRange[1].format(fmt)}`;
        }
        return t('dashboard.custom');
    }
  })();

  const requestsLabel = t('dashboard.period_requests', { period: periodLabel });
  const tokensLabel = t('dashboard.period_tokens', { period: periodLabel });
  const costLabel = t('dashboard.period_cost', { period: periodLabel });
  const scopeLabel = isAdmin
    ? t('dashboard.scope_admin')
    : t('dashboard.scope_user');
  const [cardHeight, setCardHeight] = useState(580);

  useEffect(() => {
    const handleResize = () => {
      const announcementOffset = pinnedAnnouncement ? 70 : 0;
      const calculated = window.innerHeight - 470 - announcementOffset;
      setCardHeight(Math.max(450, calculated));
    };

    handleResize();
    window.addEventListener('resize', handleResize);
    return () => window.removeEventListener('resize', handleResize);
  }, [pinnedAnnouncement]);

  const isDark = !_isLight;
  const textColor = isDark ? 'rgba(255, 255, 255, 0.85)' : 'rgba(0, 0, 0, 0.85)';

  const getRankBadgeStyle = (index: number): React.CSSProperties => {
    return {
      display: 'inline-flex',
      alignItems: 'center',
      justifyContent: 'center',
      width: 18,
      fontWeight: 600,
      fontSize: 11,
      flexShrink: 0,
      color: index === 0 
        ? (_isLight ? '#09090b' : '#fafafa') 
        : index === 1 
          ? (_isLight ? '#27272a' : '#e4e4e7') 
          : index === 2 
            ? (_isLight ? '#52525b' : '#d4d4d8') 
            : (_isLight ? '#a1a1aa' : '#71717a'),
    };
  };

  const getBarColor = (index: number) => {
    if (index === 0) {
      return _isLight ? '#18181b' : '#fafafa';
    }
    if (index === 1) {
      return _isLight ? '#3f3f46' : '#d4d4d8';
    }
    if (index === 2) {
      return _isLight ? '#71717a' : '#a1a1aa';
    }
    return _isLight 
      ? 'rgba(24, 24, 27, 0.25)' 
      : 'rgba(255, 255, 255, 0.25)';
  };

  const formatTokenCount = (tokens: number): string => {
    if (!tokens || tokens <= 0) return '0';
    if (tokens >= 1_000_000_000) return `${(tokens / 1_000_000_000).toFixed(2)}B`;
    if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(2)}M`;
    if (tokens >= 1_000) return `${(tokens / 1_000).toFixed(1)}k`;
    return `${tokens}`;
  };

  const statsSeq = useRef(0);
  const lastStatsAt = useRef(0);

  const fetchStats = async (force = false) => {
    const seq = ++statsSeq.current;
    try {
      const params: Record<string, string | boolean> = {};
      Object.assign(params, toCalendarDateRangeParams(dateRange));
      if (force) params.refresh = true;
      const data = await (request.get<DashboardStats>('/dashboard', { params }) as unknown as Promise<DashboardStats>);
      if (seq !== statsSeq.current) return;
      setStats(data);
      lastStatsAt.current = Date.now();
      if (data.live_metrics) {
        setLiveMetrics(data.live_metrics);
      }
    } catch (error) {
      if (seq !== statsSeq.current) return;
      console.error(error);
      void fetchLiveMetrics();
    }
  };

  const fetchLiveMetrics = async () => {
    if (typeof document !== 'undefined' && document.hidden) {
      return;
    }
    try {
      const data = await (request.get<LiveMetricsResponse>('/metrics/live') as unknown as Promise<LiveMetricsResponse>);
      setLiveMetrics(data.metrics);
    } catch (error) {
      // 实时指标失败不阻断看板
      console.error(error);
    }
  };

  const handleManualRefresh = async () => {
    if (refreshing || loading) return;
    setRefreshing(true);
    try {
      await fetchStats(true);
    } finally {
      setRefreshing(false);
    }
  };

  useEffect(() => {
    let alive = true;
    setLoading(true);
    fetchStats(false).finally(() => {
      if (alive) {
        setLoading(false);
        setLiveMetricsReady(true);
      }
    });
    const timer = setInterval(() => {
      if (document.hidden) return;
      void fetchStats(true);
    }, 180000);
    const onVis = () => {
      if (document.hidden) return;
      if (lastStatsAt.current > 0 && Date.now() - lastStatsAt.current >= 180000) {
        void fetchStats(true);
      }
    };
    document.addEventListener('visibilitychange', onVis);
    return () => {
      alive = false;
      statsSeq.current += 1;
      clearInterval(timer);
      document.removeEventListener('visibilitychange', onVis);
    };
  }, [dateRange]);

  useEffect(() => {
    const timer = setInterval(fetchLiveMetrics, 5000);
    const onVis = () => {
      if (!document.hidden) fetchLiveMetrics();
    };
    document.addEventListener('visibilitychange', onVis);
    return () => {
      clearInterval(timer);
      document.removeEventListener('visibilitychange', onVis);
    };
  }, []);

  // 看板主数据与实时指标都结束后再通知布局弹公告，避免加载过程中反复闪屏
  useEffect(() => {
    if (loading || !liveMetricsReady) return;
    notifyConsolePageReady?.();
  }, [loading, liveMetricsReady, notifyConsolePageReady]);

  const calculateTrend = (current: number = 0, previous: number = 0) => {
    if (previous <= 0) {
      if (current > 0) return { pct: '+100%', isUp: true, isNeutral: false };
      return { pct: '0.0%', isUp: true, isNeutral: true };
    }
    const diff = current - previous;
    const pct = (diff / previous) * 100;
    if (Math.abs(pct) < 0.05) {
      return { pct: '0.0%', isUp: true, isNeutral: true };
    }
    const sign = pct > 0 ? '+' : '';
    return {
      pct: `${sign}${pct.toFixed(1)}%`,
      isUp: pct > 0,
      isNeutral: false,
    };
  };

  const formatCostValue = (cost: number = 0) => {
    if (cost >= 100) {
      return cost.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
    }
    if (cost >= 1) {
      return cost.toFixed(4);
    }
    return cost.toFixed(6);
  };

  const renderTrendBadge = (trend: { pct: string; isUp: boolean; isNeutral: boolean }) => {
    return (
      <div
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 4,
          padding: '2px 8px',
          borderRadius: 9999,
          background: _isLight ? 'rgba(0, 0, 0, 0.03)' : 'rgba(255, 255, 255, 0.04)',
          border: _isLight ? '1px solid #e4e4e7' : '1px solid rgba(255, 255, 255, 0.12)',
          fontSize: 11.5,
          fontWeight: 500,
          color: _isLight ? '#18181b' : '#fafafa',
          lineHeight: '16px',
        }}
      >
        {!trend.isNeutral && (
          trend.isUp ? (
            <TrendingUp size={11} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa' }} />
          ) : (
            <TrendingDown size={11} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa' }} />
          )
        )}
        <span>{trend.pct}</span>
      </div>
    );
  };

  const cardStyle: React.CSSProperties = {
    minHeight: screens.xs ? 152 : 164,
    height: '100%',
    borderRadius: 16,
    overflow: 'hidden',
    border: 'none',
    boxShadow: 'none',
  };

  const cardTopSectionStyle: React.CSSProperties = {
    padding: screens.xs ? '14px 16px 12px 16px' : '18px 20px 14px 20px',
    background: _isLight ? '#ffffff' : '#0c0c0e',
    display: 'flex',
    flexDirection: 'column',
    justifyContent: 'space-between',
    flex: '1 1 auto',
  };

  const cardBottomSectionStyle: React.CSSProperties = {
    padding: screens.xs ? '11px 16px 13px 16px' : '13px 20px 15px 20px',
    background: _isLight ? '#fafafa' : '#141414', // 与左侧菜单同色 (Light: #fafafa / Dark: #141414)
    borderTop: _isLight ? '1px solid #e4e4e7' : '1px solid rgba(255, 255, 255, 0.08)',
    display: 'flex',
    flexDirection: 'column',
    gap: 3,
    flexShrink: 0,
  };

  const cardTitleStyle: React.CSSProperties = {
    color: _isLight ? '#71717a' : '#a1a1aa',
    fontSize: screens.xs ? 12.5 : 13.5,
    fontWeight: 500,
    letterSpacing: '-0.01em',
  };

  const cardBigValueStyle: React.CSSProperties = {
    margin: 0,
    marginBottom: 0,
    color: _isLight ? '#09090b' : '#fafafa',
    fontWeight: 700,
    fontSize: screens.xs ? 24 : 30,
    fontVariantNumeric: 'tabular-nums',
    letterSpacing: '-0.035em',
    lineHeight: 1.15,
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
  };

  const footerPrimaryStyle: React.CSSProperties = {
    display: 'inline-flex',
    alignItems: 'center',
    gap: 5,
    fontSize: screens.xs ? 12 : 13,
    fontWeight: 500,
    color: _isLight ? '#18181b' : '#fafafa',
    lineHeight: '18px',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  };

  const footerSecondaryStyle: React.CSSProperties = {
    fontSize: screens.xs ? 11 : 12,
    fontWeight: 400,
    color: _isLight ? '#71717a' : '#a1a1aa',
    lineHeight: '16px',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    whiteSpace: 'nowrap',
  };



  return (
    <div style={{ maxWidth: 1600, margin: '0 auto' }}>
      {pinnedAnnouncement && (
        <div
          className="compact-announcement-banner"
          onMouseEnter={() => setIsPinnedBannerHovered(true)}
          onMouseLeave={() => setIsPinnedBannerHovered(false)}
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            gap: '12px',
            height: screens.xs ? 'auto' : '64px',
            minHeight: '64px',
            padding: '8px 14px',
            marginBottom: 10,
            borderRadius: '8px',
            background: _isLight ? 'rgba(24, 24, 27, 0.03)' : 'rgba(250, 250, 250, 0.04)',
            border: _isLight ? '1px solid rgba(24, 24, 27, 0.08)' : '1px solid rgba(250, 250, 250, 0.1)',
            boxShadow: '0 1px 2px rgba(0, 0, 0, 0.02)',
            transition: 'all 0.2s ease',
            boxSizing: 'border-box',
          }}
        >
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '10px',
              flex: 1,
              minWidth: 0,
              cursor: isNoticeTruncated ? 'pointer' : 'default',
            }}
            onClick={() => {
              if (isNoticeTruncated) {
                handleOpenNoticeDetail(pinnedAnnouncement);
              }
            }}
          >
            {/* Custom Bell Icon */}
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                width: '32px',
                height: '32px',
                borderRadius: '6px',
                background: _isLight ? 'rgba(24, 24, 27, 0.06)' : 'rgba(255, 255, 255, 0.08)',
                color: _isLight ? '#18181b' : '#fafafa',
                flexShrink: 0,
              }}
            >
              <BellOutlined style={{ fontSize: '15px' }} />
            </div>

            <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', justifyContent: 'center', gap: '2px' }}>
              <span
                ref={noticeTitleRef}
                style={{
                  fontWeight: 600,
                  fontSize: '13px',
                  lineHeight: '18px',
                  color: _isLight ? '#18181b' : '#f4f4f5',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}
              >
                {getAnnouncementLabel(pinnedAnnouncement.title)}
              </span>
              <div
                ref={noticeContentRef}
                className="quill-content compact-announcement"
                dangerouslySetInnerHTML={{ __html: getAnnouncementLabel(pinnedAnnouncement.content) }}
                style={{
                  fontSize: '12px',
                  lineHeight: '15px',
                  maxHeight: '30px',
                  color: _isLight ? '#52525b' : '#a1a1aa',
                  display: '-webkit-box',
                  WebkitLineClamp: 2,
                  WebkitBoxOrient: 'vertical',
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  wordBreak: 'break-word',
                }}
              />
            </div>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: '8px', flexShrink: 0 }}>
            {isNoticeTruncated && (
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  handleOpenNoticeDetail(pinnedAnnouncement);
                }}
                style={{
                  padding: '0 2px',
                  fontSize: '12px',
                  fontWeight: 500,
                  color: _isLight ? '#52525b' : '#a1a1aa',
                  textDecoration: 'none',
                  background: 'transparent',
                  border: 'none',
                  outline: 'none',
                  cursor: 'pointer',
                  transition: 'color 0.2s ease',
                }}
                onMouseEnter={(e) => { (e.currentTarget as HTMLElement).style.color = _isLight ? '#18181b' : '#fafafa'; }}
                onMouseLeave={(e) => { (e.currentTarget as HTMLElement).style.color = _isLight ? '#52525b' : '#a1a1aa'; }}
              >
                {t('dashboard.view_detail', '详情')}
              </button>
            )}

            {/* 多条置顶通知时展示可点击切换的小圆点 */}
            {pinnedAnnouncements.length > 1 && (
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  gap: '5px',
                  flexShrink: 0,
                  paddingLeft: '2px',
                }}
              >
                {pinnedAnnouncements.map((_, idx) => {
                  const isActive = idx === currentPinnedIndex;
                  return (
                    <button
                      key={idx}
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        setCurrentPinnedIndex(idx);
                        try {
                          sessionStorage.setItem('last_dashboard_pinned_id', String(pinnedAnnouncements[idx]?.id));
                        } catch {}
                      }}
                      aria-label={t('dashboard.pinned_switch', { index: idx + 1 })}
                      style={{
                        padding: 0,
                        border: 'none',
                        cursor: 'pointer',
                        height: '5px',
                        width: isActive ? '14px' : '5px',
                        borderRadius: '9999px',
                        backgroundColor: isActive
                          ? _isLight
                            ? '#18181b'
                            : '#fafafa'
                          : _isLight
                            ? 'rgba(0, 0, 0, 0.18)'
                            : 'rgba(255, 255, 255, 0.22)',
                        transition: 'all 0.2s ease',
                      }}
                    />
                  );
                })}
              </div>
            )}
          </div>
        </div>
      )}

      {/* 系统概览标题与过滤工具栏 */}
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', justifyContent: 'space-between', gap: 16, marginBottom: 20 }}>
        <div style={{ display: 'flex', alignItems: 'center' }}>
          <Title level={2} style={{ margin: 0, fontWeight: 600, color: _isLight ? '#1f2937' : '#E8EAED' }}>
            {t('dashboard.title')}
          </Title>
          <Tag
            style={{
              marginLeft: 12,
              marginRight: 0,
              borderRadius: 6,
              border: _isLight ? '1px solid rgba(24,24,27,0.12)' : '1px solid rgba(255,255,255,0.16)',
              background: _isLight ? 'rgba(24,24,27,0.04)' : 'rgba(255,255,255,0.06)',
              color: _isLight ? '#52525b' : '#a1a1aa',
              fontWeight: 500,
            }}
          >
            {scopeLabel}
          </Tag>
          <AntTooltip title={t('dashboard.view_30d_trend', '查看 30 天趋势')}>
            <LineChartIcon
              size={24}
              strokeWidth={2}
              className="shadcn-icon-btn"
              style={{
                marginLeft: 12,
                color: _isLight ? '#000000' : '#ffffff',
                cursor: 'pointer',
              }}
              onClick={() => navigate(`${basePath}/dashboard/model-distribution`)}
            />
          </AntTooltip>
        </div>
        <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 12 }}>
          <Radio.Group value={quickRange} onChange={handleQuickRangeChange} style={{ display: 'flex', flexWrap: 'wrap' }}>
            <Radio.Button value="today">{t('dashboard.today', '今天')}</Radio.Button>
            <Radio.Button value="yesterday">{t('dashboard.yesterday', '昨日')}</Radio.Button>
            <Radio.Button value="week">{t('dashboard.week', '本周')}</Radio.Button>
            <Radio.Button value="month">{t('dashboard.month', '本月')}</Radio.Button>
            <Radio.Button value="last_month">{t('dashboard.last_month', '上月')}</Radio.Button>
          </Radio.Group>
        </div>
      </div>

      {/* 概览仪表盘：消耗 Token | 预估成本 | 请求+令牌 | 实时吞吐 */}
      {(() => {
        const tokensTrend = calculateTrend(stats?.today_tokens, stats?.yesterday_tokens);
        const costTrend = calculateTrend(stats?.today_cost, stats?.yesterday_cost);
        const requestsTrend = calculateTrend(stats?.today_requests, stats?.yesterday_requests);

        return (
          <Row gutter={[16, 16]}>
            {/* Card 1: 消耗 Token */}
            <Col xs={12} sm={12} lg={6} style={{ display: 'flex' }}>
              <Card
                variant="borderless"
                className="shadcn-metric-card"
                style={{ ...cardStyle, flex: 1, width: '100%' }}
                styles={{ body: { padding: 0, height: '100%', display: 'flex', flexDirection: 'column' } }}
              >
                <div style={cardTopSectionStyle}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                    <span style={cardTitleStyle}>{tokensLabel}</span>
                    {renderTrendBadge(tokensTrend)}
                  </div>
                  <div style={{ margin: '8px 0 0 0' }}>
                    <Title
                      level={2}
                      style={cardBigValueStyle}
                      title={stats?.total_tokens?.toLocaleString() || '0'}
                    >
                      {stats?.total_tokens?.toLocaleString() || 0}
                    </Title>
                  </div>
                </div>
                <div style={cardBottomSectionStyle}>
                  <div style={footerPrimaryStyle}>
                    <span>
                      {showDayComparison
                        ? (tokensTrend.isNeutral
                            ? t('dashboard.trend_flat', '与昨日消耗持平')
                            : `${t('dashboard.trend_compared_yesterday', '较昨日')} ${tokensTrend.pct}`)
                        : t('dashboard.tokens_total_scope', '周期 Token 消耗总额')}
                    </span>
                    {showDayComparison ? (
                      !tokensTrend.isNeutral && (
                        tokensTrend.isUp ? (
                          <TrendingUp size={13} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa', flexShrink: 0 }} />
                        ) : (
                          <TrendingDown size={13} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa', flexShrink: 0 }} />
                        )
                      )
                    ) : (
                      <TrendingUp size={13} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa', opacity: 0.8, flexShrink: 0 }} />
                    )}
                  </div>
                  <div style={footerSecondaryStyle}>
                    {showDayComparison
                      ? `${t('dashboard.today')}: ${(stats?.today_tokens ?? 0).toLocaleString()} · ${t('dashboard.yesterday')}: ${(stats?.yesterday_tokens ?? 0).toLocaleString()}`
                      : t('dashboard.scope_auto_aggregate', '输入与输出 Token 实时汇总计量')}
                  </div>
                </div>
              </Card>
            </Col>

            {/* Card 2: 预估成本 */}
            <Col xs={12} sm={12} lg={6} style={{ display: 'flex' }}>
              <Card
                variant="borderless"
                className="shadcn-metric-card"
                style={{ ...cardStyle, flex: 1, width: '100%' }}
                styles={{ body: { padding: 0, height: '100%', display: 'flex', flexDirection: 'column' } }}
              >
                <div style={cardTopSectionStyle}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                    <span style={cardTitleStyle}>{costLabel}</span>
                    {renderTrendBadge(costTrend)}
                  </div>
                  <div style={{ margin: '8px 0 0 0' }}>
                    <Title
                      level={2}
                      style={cardBigValueStyle}
                      title={`${currencySymbol}${stats?.total_cost?.toFixed(6) || 0}`}
                    >
                      {currencySymbol}{formatCostValue(stats?.total_cost ?? 0)}
                    </Title>
                  </div>
                </div>
                <div style={cardBottomSectionStyle}>
                  <div style={footerPrimaryStyle}>
                    <span>
                      {showDayComparison
                        ? (costTrend.isNeutral
                            ? t('dashboard.trend_flat', '与昨日成本持平')
                            : `${t('dashboard.trend_compared_yesterday', '较昨日')} ${costTrend.pct}`)
                        : t('dashboard.cost_desc', '所选范围消费金额')}
                    </span>
                    {showDayComparison ? (
                      !costTrend.isNeutral && (
                        costTrend.isUp ? (
                          <TrendingUp size={13} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa', flexShrink: 0 }} />
                        ) : (
                          <TrendingDown size={13} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa', flexShrink: 0 }} />
                        )
                      )
                    ) : (
                      <TrendingUp size={13} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa', opacity: 0.8, flexShrink: 0 }} />
                    )}
                  </div>
                  <div style={footerSecondaryStyle}>
                    {showDayComparison
                      ? `${t('dashboard.today')}: ${currencySymbol}${formatCostValue(stats?.today_cost ?? 0)} · ${t('dashboard.yesterday')}: ${currencySymbol}${formatCostValue(stats?.yesterday_cost ?? 0)}`
                      : t('dashboard.billing_live_metered', '按各模型配置费率精确计量')}
                  </div>
                </div>
              </Card>
            </Col>

            {/* Card 3: 请求总数 */}
            <Col xs={12} sm={12} lg={6} style={{ display: 'flex' }}>
              <Card
                variant="borderless"
                className="shadcn-metric-card"
                style={{ ...cardStyle, flex: 1, width: '100%' }}
                styles={{ body: { padding: 0, height: '100%', display: 'flex', flexDirection: 'column' } }}
              >
                <div style={cardTopSectionStyle}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                    <span style={cardTitleStyle}>{requestsLabel}</span>
                    {renderTrendBadge(requestsTrend)}
                  </div>
                  <div style={{ margin: '8px 0 0 0' }}>
                    <Title
                      level={2}
                      style={cardBigValueStyle}
                      title={String(stats?.total_requests || 0)}
                    >
                      {(stats?.total_requests ?? 0).toLocaleString()}
                    </Title>
                  </div>
                </div>
                <div style={cardBottomSectionStyle}>
                  <div style={footerPrimaryStyle}>
                    <span>
                      {showDayComparison
                        ? (requestsTrend.isNeutral
                            ? t('dashboard.trend_requests_flat', '与昨日请求量持平')
                            : `${t('dashboard.trend_compared_yesterday', '较昨日')} ${requestsTrend.pct}`)
                        : t('dashboard.total_requests_desc', 'API 请求调用总数')}
                    </span>
                    {showDayComparison ? (
                      !requestsTrend.isNeutral && (
                        requestsTrend.isUp ? (
                          <TrendingUp size={13} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa', flexShrink: 0 }} />
                        ) : (
                          <TrendingDown size={13} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa', flexShrink: 0 }} />
                        )
                      )
                    ) : (
                      <TrendingUp size={13} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa', opacity: 0.8, flexShrink: 0 }} />
                    )}
                  </div>
                  <div style={footerSecondaryStyle}>
                    {showDayComparison
                      ? `${t('dashboard.today')}: ${(stats?.today_requests ?? 0).toLocaleString()} ${t('dashboard.times_unit', '次')} · ${t('dashboard.active_tokens', '活跃令牌')} ${stats?.today_active_tokens || 0}`
                      : `${t('dashboard.today_active')}: ${stats?.today_active_tokens || 0} / ${t('dashboard.total_prefix', '共')} ${stats?.total_api_tokens || 0} ${t('dashboard.token_count_unit', '个')}`}
                  </div>
                </div>
              </Card>
            </Col>

            {/* Card 4: 实时吞吐 */}
            <Col xs={12} sm={12} lg={6} style={{ display: 'flex' }}>
              <Card
                variant="borderless"
                className="shadcn-metric-card"
                style={{ ...cardStyle, flex: 1, width: '100%' }}
                styles={{ body: { padding: 0, height: '100%', display: 'flex', flexDirection: 'column' } }}
              >
                <div style={cardTopSectionStyle}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8 }}>
                    <span style={cardTitleStyle}>{t('dashboard.live_throughput')}</span>
                    <div
                      style={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: 5,
                        padding: '2px 8px',
                        borderRadius: 9999,
                        background: _isLight ? 'rgba(0, 0, 0, 0.03)' : 'rgba(255, 255, 255, 0.04)',
                        border: _isLight ? '1px solid #e4e4e7' : '1px solid rgba(255, 255, 255, 0.12)',
                        fontSize: 11.5,
                        fontWeight: 500,
                        color: _isLight ? '#16a34a' : '#4ade80',
                        lineHeight: '16px',
                      }}
                    >
                      <span
                        style={{
                          width: 6,
                          height: 6,
                          borderRadius: '50%',
                          backgroundColor: '#22c55e',
                          boxShadow: '0 0 6px #22c55e',
                          display: 'inline-block',
                        }}
                      />
                      <span>LIVE</span>
                    </div>
                  </div>
                  <div style={{ margin: '8px 0 0 0', display: 'flex', alignItems: 'baseline', gap: 6 }}>
                    <Title
                      level={2}
                      style={{ ...cardBigValueStyle, display: 'inline-block' }}
                      title={`${liveMetrics?.qps ?? 0} QPS`}
                    >
                      {liveMetrics?.qps ?? 0}
                    </Title>
                    <span style={{ fontSize: 13, fontWeight: 500, color: _isLight ? '#71717a' : '#a1a1aa' }}>
                      QPS
                    </span>
                  </div>
                </div>
                <div style={cardBottomSectionStyle}>
                  <AntTooltip title={`${t('dashboard.live_rpm')}: ${liveMetrics?.rpm ?? 0} · ${t('dashboard.live_tpm')}: ${liveMetrics?.tpm ?? 0}`}>
                    <div style={footerPrimaryStyle}>
                      <span>
                        {(liveMetrics?.rpm ?? 0).toLocaleString()} RPM · ${formatTokenCount(liveMetrics?.tpm ?? 0)} TPM
                      </span>
                      <Zap size={13} strokeWidth={2.2} style={{ color: _isLight ? '#18181b' : '#fafafa', flexShrink: 0 }} />
                    </div>
                  </AntTooltip>
                  <div style={footerSecondaryStyle}>
                    {`${t('dashboard.live_task', 'Task')}: ${liveMetrics?.task ?? 0} · ${t('dashboard.live_pending_tasks', 'Pending')}: ${(liveMetrics?.pending_tasks ?? 0).toLocaleString()}`}
                  </div>
                </div>
              </Card>
            </Col>
          </Row>
        );
      })()}

      <UsageAreaChart />

      <Row gutter={[16, 16]} style={{ marginTop: 24 }}>
        <Col lg={14} xs={24}>
          <div style={{ height: 24, display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
            <div style={{ display: 'flex', alignItems: 'center' }}>
              <AntTooltip title={t('dashboard.view_all_logs', '查看全部日志')}>
                <Link 
                  to={logsPath} 
                  style={{ 
                    display: 'inline-flex', 
                    alignItems: 'center', 
                    gap: 6, 
                    textDecoration: 'none',
                    cursor: 'pointer',
                  }}
                  className="hover:opacity-80 transition-opacity"
                >
                  <Title level={5} style={{ color: _isLight ? '#1f2937' : '#fff', fontWeight: 500, margin: 0, cursor: 'pointer' }}>
                    {t('dashboard.recent_activity')}
                  </Title>
                  <ArrowRight size={15} className="shadcn-icon-btn" style={{ color: _isLight ? '#1f2937' : '#ffffff', cursor: 'pointer' }} />
                </Link>
              </AntTooltip>
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span
                className="inline-flex h-2 w-2 rounded-full"
                style={{
                  background: '#10b981',
                  boxShadow: '0 0 6px rgba(16, 185, 129, 0.6)',
                }}
              />
              <Text type="secondary" style={{ color: _isLight ? '#999' : '#666', fontSize: 12 }}>
                {t('dashboard.auto_refresh')}
              </Text>
              <AntTooltip title={t('dashboard.refresh_now', '立即刷新')}>
                <button
                  type="button"
                  onClick={handleManualRefresh}
                  disabled={refreshing || loading}
                  aria-label={t('dashboard.refresh_now')}
                  style={{
                    background: 'none',
                    border: 'none',
                    padding: 2,
                    cursor: refreshing || loading ? 'default' : 'pointer',
                    display: 'inline-flex',
                    alignItems: 'center',
                    color: _isLight ? '#71717a' : '#a1a1aa',
                    lineHeight: 1,
                    transition: 'color 0.15s ease',
                  }}
                  className="hover:text-foreground"
                >
                  <RefreshCw size={12} className={refreshing || loading ? 'animate-spin' : ''} />
                </button>
              </AntTooltip>
            </div>
          </div>
          <Card 
            variant="borderless" 
            style={{ 
              borderRadius: 12, 
              height: screens.xs ? 'auto' : cardHeight, 
              display: 'flex', 
              flexDirection: 'column',
              background: _isLight ? '#ffffff' : '#141414',
              border: 'none',
              boxShadow: 'none',
            }} 
            styles={{ body: { padding: '14px 16px', flex: 1, overflowY: 'hidden', display: 'flex', flexDirection: 'column' } }}
          >
            <Spin spinning={loading} wrapperClassName="flex-1 flex flex-col min-h-0" style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
              <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
                {/* Top Header Summary Strip */}
                <div 
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    paddingBottom: 10,
                    marginBottom: 8,
                    borderBottom: _isLight ? '1px solid #f4f4f5' : '1px solid rgba(255, 255, 255, 0.08)',
                    flexShrink: 0,
                  }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 11, color: _isLight ? '#71717a' : '#a1a1aa' }}>
                    <Activity size={12} style={{ opacity: 0.7 }} />
                    <span style={{ fontWeight: 500 }}>{t('dashboard.recent_requests', '最新请求')}</span>
                    <span style={{ opacity: 0.4 }}>·</span>
                    <span>
                      {t('dashboard.recent_logs_count', { count: stats?.recent_logs?.length || 0, defaultValue: `展示最近 ${stats?.recent_logs?.length || 0} 条记录` })}
                    </span>
                  </div>
                  <div>
                    <Link 
                      to={logsPath} 
                      style={{ 
                        fontSize: 11, 
                        color: _isLight ? '#71717a' : '#a1a1aa', 
                        display: 'inline-flex', 
                        alignItems: 'center', 
                        gap: 3, 
                        transition: 'color 0.15s ease',
                        cursor: 'pointer',
                        textDecoration: 'none',
                      }}
                      className="hover:text-foreground"
                    >
                      <span>{t('dashboard.all_logs', '全部日志')}</span>
                      <ArrowRight size={11} />
                    </Link>
                  </div>
                </div>

                {stats?.recent_logs && stats.recent_logs.length > 0 ? (
                  <div 
                    className="custom-scrollbar"
                    style={{ 
                      height: screens.xs ? 'auto' : cardHeight - 100, 
                      overflowY: 'auto', 
                      paddingRight: 4 
                    }}
                  >
                    <div style={{ display: 'flex', flexDirection: 'column' }}>
                      {stats.recent_logs.map((log, index) => {
                        const displayName = log.user_nickname || log.user_uid || (log.user_id ? String(log.user_id) : '');
                        const fullDateTime = dayjs(log.created_at).format('YYYY-MM-DD HH:mm:ss');
                        const dateMatch = fullDateTime.match(/^(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2})(.*)$/);
                        const dateStr = dateMatch ? dateMatch[1].slice(5) : dayjs(log.created_at).format('MM-DD');
                        const timeStr = dateMatch ? dateMatch[2] : dayjs(log.created_at).format('HH:mm:ss');
                        const statusMeta = getStatusMeta(log.status_code, _isLight);
                        const endpointTag = getEndpointTag(log.endpoint);
                        const latency = log.latency_ms ?? 0;
                        const latencyText = latency > 0 ? (latency < 1000 ? `${latency}ms` : `${(latency / 1000).toFixed(2)}s`) : null;
                        const latencyColor = _isLight ? '#71717a' : '#a1a1aa';
                        const promptTokens = log.prompt_tokens || 0;
                        const completionTokens = log.completion_tokens || 0;
                        const cachedTokens = log.cached_tokens || 0;
                        const costFormatted = (log.cost || 0).toFixed(6);

                        return (
                          <div 
                            key={log.id} 
                            className="shadcn-activity-row"
                            style={{
                              display: 'flex',
                              alignItems: 'center',
                              justifyContent: 'space-between',
                              gap: screens.xs ? 8 : 12,
                              padding: '8px 6px',
                              borderRadius: 6,
                              borderBottom: index === stats.recent_logs.length - 1 
                                ? 'none' 
                                : (_isLight ? '1px solid #f4f4f5' : '1px solid rgba(255, 255, 255, 0.05)'),
                            }}
                          >
                            {/* Left: Status + Time + Divider + Model + Endpoint + User */}
                            <div style={{ display: 'flex', alignItems: 'center', gap: screens.xs ? 8 : 10, minWidth: 0, flex: 1 }}>
                              {/* Status code */}
                              <span 
                                style={{
                                  display: 'inline-flex',
                                  alignItems: 'center',
                                  gap: 4,
                                  fontSize: 11,
                                  fontWeight: 600,
                                  fontVariantNumeric: 'tabular-nums',
                                  color: statusMeta.text,
                                  flexShrink: 0,
                                  minWidth: 32,
                                }}
                              >
                                <span style={{ width: 5, height: 5, borderRadius: '50%', background: statusMeta.dot, flexShrink: 0 }} />
                                <span>{log.status_code}</span>
                              </span>

                              {/* Time (Stacked HH:mm:ss / MM-DD) */}
                              <AntTooltip title={fullDateTime}>
                                <div style={{ display: 'flex', flexDirection: 'column', flexShrink: 0, minWidth: 54 }}>
                                  <span 
                                    style={{
                                      fontSize: 12,
                                      fontVariantNumeric: 'tabular-nums',
                                      color: _isLight ? '#27272a' : '#e4e4e7',
                                      fontWeight: 500,
                                      lineHeight: 1.2,
                                    }}
                                  >
                                    {timeStr}
                                  </span>
                                  <span 
                                    style={{
                                      fontSize: 10,
                                      color: _isLight ? '#a1a1aa' : '#71717a',
                                      lineHeight: 1.2,
                                      marginTop: 2,
                                    }}
                                  >
                                    {dateStr}
                                  </span>
                                </div>
                              </AntTooltip>

                              {/* Subtle Vertical Divider */}
                              <div 
                                style={{
                                  width: 1,
                                  height: 20,
                                  background: _isLight ? '#e4e4e7' : 'rgba(255, 255, 255, 0.08)',
                                  flexShrink: 0,
                                }}
                              />

                              {/* Model info + Endpoint + User chip */}
                              <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0, flex: 1 }}>
                                <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
                                  <span 
                                    title={log.model}
                                    style={{
                                      minWidth: 0,
                                      fontWeight: 600,
                                      fontSize: 13,
                                      color: _isLight ? '#09090b' : '#fafafa',
                                      whiteSpace: 'nowrap',
                                      overflow: 'hidden',
                                      textOverflow: 'ellipsis',
                                      letterSpacing: '-0.01em',
                                    }}
                                  >
                                    {log.model}
                                  </span>
                                  {endpointTag && (
                                    <span 
                                      style={{
                                        flexShrink: 0,
                                        fontSize: 10,
                                        lineHeight: '14px',
                                        color: _isLight ? '#a1a1aa' : '#71717a',
                                        textTransform: 'uppercase',
                                        fontWeight: 500,
                                        letterSpacing: '0.02em',
                                      }}
                                    >
                                      · {endpointTag}
                                    </span>
                                  )}
                                </div>

                                {isAdmin && displayName && (
                                  <div style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                                    <span 
                                      style={{
                                        display: 'inline-flex',
                                        alignItems: 'center',
                                        gap: 3,
                                        fontSize: 10,
                                        color: _isLight ? '#a1a1aa' : '#71717a',
                                        maxWidth: 160,
                                        overflow: 'hidden',
                                        textOverflow: 'ellipsis',
                                        whiteSpace: 'nowrap',
                                      }}
                                    >
                                      <UserIcon size={10} style={{ opacity: 0.6, flexShrink: 0 }} />
                                      <span>{displayName}</span>
                                    </span>
                                  </div>
                                )}
                              </div>
                            </div>

                            {/* Right: Latency + Tokens + Cost */}
                            <div style={{ display: 'flex', alignItems: 'center', gap: screens.xs ? 6 : 10, flexShrink: 0 }}>
                              {/* Latency */}
                              {latencyText && (
                                <AntTooltip title={t('dashboard.latency_tip', { ms: latency })}>
                                  <span 
                                    style={{
                                      display: 'inline-flex',
                                      alignItems: 'center',
                                      gap: 2,
                                      fontSize: 11,
                                      fontVariantNumeric: 'tabular-nums',
                                      color: latencyColor,
                                    }}
                                  >
                                    <Zap size={11} style={{ opacity: 0.8 }} />
                                    <span>{latencyText}</span>
                                  </span>
                                </AntTooltip>
                              )}

                              {/* Tokens (Prompt / Completion) */}
                              <AntTooltip title={`${t('dashboard.tokens_tip', { prompt: promptTokens.toLocaleString(), completion: completionTokens.toLocaleString() })}${cachedTokens > 0 ? ` · ${t('dashboard.tokens_cache', { count: cachedTokens.toLocaleString() })}` : ''}`}>
                                <span 
                                  style={{
                                    fontSize: 11,
                                    fontVariantNumeric: 'tabular-nums',
                                    color: _isLight ? '#71717a' : '#a1a1aa',
                                    whiteSpace: 'nowrap',
                                    display: screens.xs ? 'none' : 'inline-block',
                                  }}
                                >
                                  {promptTokens.toLocaleString()}
                                  <span style={{ margin: '0 2px', color: _isLight ? '#d4d4d8' : '#52525b' }}>/</span>
                                  {completionTokens.toLocaleString()}
                                </span>
                              </AntTooltip>

                              {/* Cost */}
                              <span 
                                style={{
                                  fontVariantNumeric: 'tabular-nums',
                                  fontWeight: 600,
                                  fontSize: 13,
                                  color: _isLight ? '#09090b' : '#fafafa',
                                  whiteSpace: 'nowrap',
                                  textAlign: 'right',
                                  minWidth: 70,
                                }}
                              >
                                {currencySymbol}{costFormatted}
                              </span>
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  </div>
                ) : (
                  <div 
                    style={{ 
                      height: '100%', 
                      minHeight: screens.xs ? 200 : cardHeight - 120, 
                      display: 'flex', 
                      flexDirection: 'column', 
                      alignItems: 'center', 
                      justifyContent: 'center', 
                      gap: 8,
                      color: _isLight ? '#a1a1aa' : '#71717a',
                      flex: 1,
                    }}
                  >
                    <div 
                      style={{
                        width: 44,
                        height: 44,
                        borderRadius: '50%',
                        background: _isLight ? '#f4f4f5' : 'rgba(255, 255, 255, 0.05)',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                        border: _isLight ? '1px solid #e4e4e7' : '1px solid rgba(255, 255, 255, 0.08)',
                      }}
                    >
                      <Activity size={20} style={{ opacity: 0.6 }} />
                    </div>
                    <span style={{ fontSize: 13, fontWeight: 500 }}>
                      {t('dashboard.no_data')}
                    </span>
                  </div>
                )}
              </div>
            </Spin>
          </Card>
        </Col>
        
        <Col lg={10} xs={24}>
          <div style={{ height: 24, display: 'flex', alignItems: 'center', marginBottom: 16 }}>
            <Title level={5} style={{ color: _isLight ? '#1f2937' : '#fff', fontWeight: 500, margin: 0 }}>{t('dashboard.model_distribution')}</Title>
          </div>
          <Card 
            variant="borderless" 
            style={{ 
              borderRadius: 12, 
              height: screens.xs ? 'auto' : cardHeight, 
              display: 'flex', 
              flexDirection: 'column',
              background: _isLight ? '#ffffff' : '#141414',
              border: 'none',
              boxShadow: 'none',
            }} 
            styles={{ body: { padding: '14px 16px', flex: 1, overflowY: 'hidden', display: 'flex', flexDirection: 'column' } }}
          >
            <Spin spinning={loading} wrapperClassName="flex-1 flex flex-col min-h-0" style={{ flex: 1, display: 'flex', flexDirection: 'column', minHeight: 0 }}>
              {(() => {
                const todayKey = statsCalendarNow().format('YYYY-MM-DD');
                const rangeStart = dateRange?.[0];
                const rangeEnd = dateRange?.[1];
                const costDateLabel = rangeStart && rangeEnd
                  ? (rangeStart.format('YYYY-MM-DD') === rangeEnd.format('YYYY-MM-DD')
                    ? rangeStart.format('MM-DD')
                    : `${rangeStart.format('MM-DD')}~${rangeEnd.format('MM-DD')}`)
                  : todayKey.slice(5);
                const modelStats = stats?.model_stats || [];
                const maxCost = modelStats.reduce((max, item) => Math.max(max, item.total_cost), 0) || 1;
                const totalModelCost = modelStats.reduce((sum, item) => sum + item.total_cost, 0);

                return (
                  <div style={{ display: 'flex', flexDirection: 'column', flex: 1, minHeight: 0 }}>
                    {/* Top Header Summary Strip (Shadcn style) */}
                    <div 
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        paddingBottom: 8,
                        marginBottom: 6,
                        borderBottom: _isLight ? '1px solid #f4f4f5' : '1px solid rgba(255, 255, 255, 0.08)',
                        flexShrink: 0,
                      }}
                    >
                      <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 11, color: _isLight ? '#71717a' : '#a1a1aa' }}>
                        <CalendarIcon size={12} style={{ opacity: 0.7 }} />
                        <span style={{ fontWeight: 500 }}>
                          {costDateLabel}
                        </span>
                        <span style={{ opacity: 0.4 }}>·</span>
                        <span>
                          {t('dashboard.model_count', { count: modelStats.length, defaultValue: `共 ${modelStats.length} 个模型` })}
                        </span>
                      </div>
                      <div style={{ textAlign: 'right' }}>
                        <Link 
                          to={taskLogsPath} 
                          style={{ 
                            fontSize: 11, 
                            color: _isLight ? '#71717a' : '#a1a1aa', 
                            display: 'inline-flex', 
                            alignItems: 'center', 
                            gap: 3, 
                            transition: 'color 0.15s ease',
                            cursor: 'pointer',
                            textDecoration: 'none',
                          }}
                          className="hover:text-foreground"
                        >
                          <span>{t('menu.task_logs', '任务列表')}</span>
                          <ArrowRight size={11} />
                        </Link>
                      </div>
                    </div>

                    {modelStats.length > 0 ? (
                      <div 
                        className="custom-scrollbar"
                        style={{ 
                          height: screens.xs ? 'auto' : cardHeight - 100, 
                          overflowY: 'auto', 
                          paddingRight: 4 
                        }}
                      >
                        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                          {modelStats.map((item, index) => {
                            const recentDays = (item.last_three_days || []).filter((day) => day.date !== todayKey);
                            const barPercentage = Math.max(3, Math.min(100, (item.total_cost / maxCost) * 100));
                            const sharePercentage = totalModelCost > 0 
                              ? ((item.total_cost / totalModelCost) * 100).toFixed(1) 
                              : '0.0';

                            return (
                              <div 
                                key={item.model} 
                                className="shadcn-model-row"
                                style={{
                                  display: 'flex',
                                  flexDirection: 'column',
                                  gap: 4,
                                  padding: '5px 6px',
                                  borderRadius: 6,
                                  borderBottom: index === modelStats.length - 1 
                                    ? 'none' 
                                    : (_isLight ? '1px solid #f4f4f5' : '1px solid rgba(255, 255, 255, 0.05)'),
                                }}
                              >
                                {/* Row Top: Rank + Model Name + Share tag | Cost + Times/Tokens */}
                                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, minWidth: 0 }}>
                                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0, flex: 1 }}>
                                    <div style={getRankBadgeStyle(index)}>
                                      {index < 9 ? `0${index + 1}` : index + 1}
                                    </div>
                                    <span 
                                      title={item.model}
                                      style={{
                                        minWidth: 0,
                                        fontWeight: 600,
                                        color: _isLight ? '#09090b' : '#fafafa',
                                        whiteSpace: 'nowrap',
                                        overflow: 'hidden',
                                        textOverflow: 'ellipsis',
                                        fontSize: 13,
                                        letterSpacing: '-0.01em',
                                      }}
                                    >
                                      {item.model}
                                    </span>
                                    <span 
                                      style={{
                                        flexShrink: 0,
                                        fontSize: 11,
                                        color: _isLight ? '#a1a1aa' : '#71717a',
                                      }}
                                    >
                                      {sharePercentage}%
                                    </span>
                                  </div>

                                  <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-end', gap: 1, flexShrink: 0, textAlign: 'right' }}>
                                    <span 
                                      style={{
                                        fontVariantNumeric: 'tabular-nums',
                                        fontWeight: 600,
                                        fontSize: 13,
                                        color: _isLight ? '#09090b' : '#fafafa',
                                        letterSpacing: '-0.02em',
                                      }}
                                    >
                                      {currencySymbol}{Number(item.total_cost).toFixed(6)}
                                    </span>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: 5, fontSize: 11, color: _isLight ? '#71717a' : '#a1a1aa' }}>
                                      {item.total_tokens > 0 && (
                                        <span 
                                          title={`${item.total_tokens.toLocaleString()} Tokens`}
                                          style={{ fontSize: 10, opacity: 0.85 }}
                                        >
                                          {formatTokenCount(item.total_tokens)}
                                        </span>
                                      )}
                                      {item.total_tokens > 0 && <span style={{ opacity: 0.4 }}>·</span>}
                                      <span>{t('dashboard.count_times', { count: item.count })}</span>
                                    </div>
                                  </div>
                                </div>

                                {/* Row Middle: Progress Bar */}
                                <div 
                                  style={{ 
                                    width: '100%',
                                    height: 3, 
                                    borderRadius: 9999, 
                                    background: _isLight ? '#f4f4f5' : 'rgba(255, 255, 255, 0.06)', 
                                    overflow: 'hidden' 
                                  }}
                                >
                                  <div 
                                    style={{ 
                                      height: '100%', 
                                      width: `${barPercentage}%`, 
                                      borderRadius: 9999, 
                                      background: getBarColor(index),
                                      transition: 'width 0.6s cubic-bezier(0.16, 1, 0.3, 1)' 
                                    }} 
                                  />
                                </div>

                                {/* Row Bottom: Recent Days Breakdown */}
                                {recentDays.length > 0 && (
                                  <div 
                                    style={{ 
                                      display: 'grid',
                                      gridTemplateColumns: screens.xs 
                                        ? '1fr' 
                                        : (recentDays.length > 1 ? '215px minmax(0, 1fr)' : '1fr'),
                                      alignItems: 'center',
                                      columnGap: 12,
                                      rowGap: 2,
                                      paddingLeft: 26,
                                      fontSize: 10.5,
                                      lineHeight: 1.25,
                                    }}
                                  >
                                    {[...recentDays].reverse().map((day) => {
                                      const dateLabel = day.date.slice(5);
                                      return (
                                        <div 
                                          key={day.date} 
                                          style={{ 
                                            display: 'inline-flex',
                                            alignItems: 'center',
                                            gap: 6,
                                            whiteSpace: 'nowrap',
                                            minWidth: 0,
                                          }}
                                        >
                                          <span style={{ color: _isLight ? '#71717a' : '#a1a1aa', fontWeight: 500, flexShrink: 0 }}>
                                            {dateLabel}
                                          </span>
                                          <span 
                                            style={{ 
                                              color: _isLight ? '#27272a' : '#d4d4d8', 
                                              fontVariantNumeric: 'tabular-nums', 
                                              fontWeight: 500,
                                              whiteSpace: 'nowrap',
                                            }}
                                          >
                                            {currencySymbol}{day.total_cost.toFixed(6)}
                                          </span>
                                          <span style={{ color: _isLight ? '#a1a1aa' : '#71717a', fontSize: 10, whiteSpace: 'nowrap', flexShrink: 0 }}>
                                            ({day.count})
                                          </span>
                                        </div>
                                      );
                                    })}
                                  </div>
                                )}
                              </div>
                            );
                          })}
                        </div>
                      </div>
                    ) : (
                      <div 
                        style={{ 
                          height: '100%', 
                          minHeight: screens.xs ? 200 : cardHeight - 120, 
                          display: 'flex', 
                          flexDirection: 'column', 
                          alignItems: 'center', 
                          justifyContent: 'center', 
                          gap: 8,
                          color: _isLight ? '#a1a1aa' : '#71717a',
                          flex: 1,
                        }}
                      >
                        <div 
                          style={{
                            width: 44,
                            height: 44,
                            borderRadius: '50%',
                            background: _isLight ? '#f4f4f5' : 'rgba(255, 255, 255, 0.05)',
                            display: 'flex',
                            alignItems: 'center',
                            justifyContent: 'center',
                            border: _isLight ? '1px solid #e4e4e7' : '1px solid rgba(255, 255, 255, 0.08)',
                          }}
                        >
                          <PieChartIcon size={20} style={{ opacity: 0.6 }} />
                        </div>
                        <span style={{ fontSize: 13, fontWeight: 500 }}>
                          {t('dashboard.no_data')}
                        </span>
                      </div>
                    )}
                  </div>
                );
              })()}
            </Spin>
          </Card>
        </Col>
      </Row>

      {/* Global override for table styles to ensure monochrome dark theme */}
      <style>{`
        .shadcn-icon-btn {
          opacity: 0.85;
          transition: all 0.2s ease-in-out;
        }
        .shadcn-icon-btn:hover {
          opacity: 1;
          transform: scale(1.08);
        }
        .shadcn-icon-btn:active {
          transform: scale(0.95);
        }
        .shadcn-model-row,
        .shadcn-activity-row {
          transition: background-color 0.15s ease;
        }
        .shadcn-model-row:hover,
        .shadcn-activity-row:hover {
          background-color: ${_isLight ? 'rgba(0, 0, 0, 0.025)' : 'rgba(255, 255, 255, 0.035)'} !important;
        }
        .shadcn-metric-card {
          border: none !important;
          box-shadow: none !important;
        }
        .ant-progress-text {
          color: ${_isLight ? '#666' : '#888'} !important;
        }
        .compact-announcement p,
        .compact-announcement h1,
        .compact-announcement h2,
        .compact-announcement h3,
        .compact-announcement h4,
        .compact-announcement h5,
        .compact-announcement h6,
        .compact-announcement ul,
        .compact-announcement ol,
        .compact-announcement li {
          margin: 0 !important;
          padding: 0 !important;
          display: inline !important;
          line-height: inherit !important;
          font-size: inherit !important;
          font-weight: inherit !important;
        }
        .compact-announcement img {
          display: none !important;
        }
        .custom-scrollbar::-webkit-scrollbar,
        .ant-card-body::-webkit-scrollbar {
          width: 6px;
        }
        .custom-scrollbar::-webkit-scrollbar-track,
        .ant-card-body::-webkit-scrollbar-track {
          background: transparent;
        }
        .custom-scrollbar::-webkit-scrollbar-thumb,
        .ant-card-body::-webkit-scrollbar-thumb {
          background: ${_isLight ? 'rgba(0,0,0,0.15)' : 'rgba(255,255,255,0.15)'};
          border-radius: 3px;
        }
        .custom-scrollbar::-webkit-scrollbar-thumb:hover,
        .ant-card-body::-webkit-scrollbar-hover {
          background: ${_isLight ? 'rgba(0,0,0,0.3)' : 'rgba(255,255,255,0.3)'};
        }
      `}</style>

      {/* 单条通知详情弹窗 */}
      <Modal
        closable={false}
        open={isNoticeModalOpen && !!selectedNoticeForModal}
        onCancel={() => {
          setIsNoticeModalOpen(false);
          setSelectedNoticeForModal(null);
        }}
        footer={null}
        width={640}
        centered
        styles={{
          content: {
            padding: 0,
            borderRadius: '12px',
            background: _isLight ? '#ffffff' : '#18181b',
            border: _isLight ? '1px solid #e4e4e7' : '1px solid #27272a',
            boxShadow: _isLight
              ? '0 20px 25px -5px rgba(0, 0, 0, 0.1), 0 8px 10px -6px rgba(0, 0, 0, 0.1)'
              : '0 20px 25px -5px rgba(0, 0, 0, 0.5), 0 8px 10px -6px rgba(0, 0, 0, 0.5)',
            overflow: 'hidden',
          },
          body: {
            padding: 0,
          },
        } as any}
      >
        {selectedNoticeForModal && (
          <div style={{ display: 'flex', flexDirection: 'column' }}>
            {/* 弹窗顶部 Header */}
            <div
              style={{
                display: 'flex',
                alignItems: 'flex-start',
                justifyContent: 'space-between',
                gap: 16,
                padding: '20px 24px 16px',
                borderBottom: _isLight ? '1px solid #f4f4f5' : '1px solid #27272a',
              }}
            >
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6, flex: 1, minWidth: 0 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                  <Tag
                    style={{
                      margin: 0,
                      borderRadius: 4,
                      fontSize: '12px',
                      fontWeight: 500,
                      background: _isLight ? 'rgba(24, 24, 27, 0.08)' : 'rgba(255, 255, 255, 0.12)',
                      color: _isLight ? '#18181b' : '#f4f4f5',
                      border: 'none',
                    }}
                  >
                    {t('dashboard.pinned_notice', '置顶通知')}
                  </Tag>
                  {getAnnouncementDisplayTime(selectedNoticeForModal) && (
                    <span style={{ fontSize: '12px', color: _isLight ? '#71717a' : '#a1a1aa' }}>
                      {dayjs(getAnnouncementDisplayTime(selectedNoticeForModal)).format('YYYY-MM-DD HH:mm')}
                    </span>
                  )}
                </div>
                <h3
                  style={{
                    margin: 0,
                    fontSize: '16px',
                    fontWeight: 600,
                    color: _isLight ? '#09090b' : '#fafafa',
                    lineHeight: '1.4',
                    wordBreak: 'break-word',
                  }}
                >
                  {getAnnouncementLabel(selectedNoticeForModal.title)}
                </h3>
              </div>
              <Button
                type="text"
                icon={<CloseOutlined />}
                onClick={() => {
                  setIsNoticeModalOpen(false);
                  setSelectedNoticeForModal(null);
                }}
                style={{
                  color: _isLight ? '#71717a' : '#a1a1aa',
                  width: 28,
                  height: 28,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  borderRadius: 6,
                }}
              />
            </div>

            {/* 弹窗内容主体 */}
            <div
              className="custom-scrollbar quill-content"
              style={{
                padding: '20px 24px',
                maxHeight: '55vh',
                overflowY: 'auto',
                fontSize: '14px',
                lineHeight: '1.6',
                color: _isLight ? '#27272a' : '#d4d4d8',
              }}
              dangerouslySetInnerHTML={{ __html: getAnnouncementLabel(selectedNoticeForModal.content) }}
            />

            {/* 弹窗底部操作 Footer */}
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                padding: '12px 24px',
                borderTop: _isLight ? '1px solid #f4f4f5' : '1px solid #27272a',
                background: _isLight ? '#fafafa' : '#141416',
              }}
            >
              <div>
                {pinnedAnnouncements.length > 1 && (
                  <span style={{ fontSize: '12px', color: _isLight ? '#71717a' : '#a1a1aa' }}>
                    {pinnedAnnouncements.findIndex((a) => a.id === selectedNoticeForModal.id) + 1} / {pinnedAnnouncements.length}
                  </span>
                )}
              </div>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                {pinnedAnnouncements.length > 1 && (
                  <>
                    <Button
                      size="small"
                      disabled={pinnedAnnouncements.findIndex((a) => a.id === selectedNoticeForModal.id) <= 0}
                      onClick={() => {
                        const curIdx = pinnedAnnouncements.findIndex((a) => a.id === selectedNoticeForModal.id);
                        if (curIdx > 0) {
                          setSelectedNoticeForModal(pinnedAnnouncements[curIdx - 1]);
                        }
                      }}
                      style={{
                        borderRadius: 6,
                        fontSize: '12px',
                      }}
                    >
                      {t('common.prev', '上一条')}
                    </Button>
                    <Button
                      size="small"
                      disabled={pinnedAnnouncements.findIndex((a) => a.id === selectedNoticeForModal.id) >= pinnedAnnouncements.length - 1}
                      onClick={() => {
                        const curIdx = pinnedAnnouncements.findIndex((a) => a.id === selectedNoticeForModal.id);
                        if (curIdx < pinnedAnnouncements.length - 1) {
                          setSelectedNoticeForModal(pinnedAnnouncements[curIdx + 1]);
                        }
                      }}
                      style={{
                        borderRadius: 6,
                        fontSize: '12px',
                      }}
                    >
                      {t('common.next', '下一条')}
                    </Button>
                  </>
                )}
                <Button
                  type="primary"
                  onClick={() => {
                    setIsNoticeModalOpen(false);
                    setSelectedNoticeForModal(null);
                  }}
                  style={{
                    borderRadius: 6,
                    fontWeight: 500,
                    fontSize: '13px',
                    height: '32px',
                    background: _isLight ? '#18181b' : '#fafafa',
                    color: _isLight ? '#fafafa' : '#18181b',
                    border: 'none',
                  }}
                >
                  {t('common.close', '关闭')}
                </Button>
              </div>
            </div>
          </div>
        )}
      </Modal>
    </div>
  );
};

export default Dashboard;
