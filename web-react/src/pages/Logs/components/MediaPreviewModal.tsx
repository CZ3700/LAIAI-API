import React, { useState, useEffect, useMemo, useRef, useCallback } from 'react';
import { createPortal } from 'react-dom';
import { Modal, Spin, message, Tooltip, Grid } from 'antd';
import {
  Image as ImageIcon,
  Video,
  Copy,
  Check,
  Download,
  ExternalLink,
  Info,
  Clock,
  Cpu,
  Sparkles,
  Maximize2,
  Minimize2,
  ZoomIn,
  ZoomOut,
  RotateCw,
  RotateCcw,
  FlipHorizontal2,
  AlertCircle,
  ChevronLeft,
  ChevronRight,
  Ratio,
  Film,
  HardDrive,
  X,
  Coins,
  Loader2
} from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { useThemeStore } from '../../../store/theme';
import useSettingsStore from '../../../store/settings';
import { formatApiDateTime } from '../../../utils/timedisplay';
import { copyToClipboard } from '../../../utils/clipboard';
import request from '../../../utils/request';
import ArtPlayerPreview from '../../../components/ArtPlayerPreview';

const { useBreakpoint } = Grid;

interface TaskLogItem {
  id: number;
  log_id?: string;
  user_id: string;
  user_uid?: string;
  channel_id: number | null;
  model: string;
  endpoint?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  cached_tokens?: number;
  cost?: number;
  latency_ms?: number;
  status_code?: number;
  error_message?: string | null;
  request_content?: string | null;
  response_content?: string | null;
  post_response?: string | null;
  billing_detail?: string | null;
  channel_name?: string | null;
  channel_group_aid?: string | null;
  user_nickname?: string | null;
  task_id?: string | null;
  action_type?: string | null;
  preview_urls?: string[];
  created_at: string;
}

interface MediaPreviewModalProps {
  open: boolean;
  onClose: () => void;
  record: TaskLogItem | null;
  urls: string[];
  type: '图片' | '视频';
}

interface MediaAttributes {
  width?: number;
  height?: number;
  aspectRatio?: string;
  duration?: number;
  format?: string;
  fileSize?: string;
}

// 智能计算宽高比标签
function calculateAspectRatio(w: number, h: number): string {
  if (!w || !h) return '-';
  const ratio = w / h;
  if (Math.abs(ratio - 16 / 9) < 0.03) return '16:9';
  if (Math.abs(ratio - 9 / 16) < 0.03) return '9:16';
  if (Math.abs(ratio - 4 / 3) < 0.03) return '4:3';
  if (Math.abs(ratio - 3 / 4) < 0.03) return '3:4';
  if (Math.abs(ratio - 1) < 0.03) return '1:1';
  if (Math.abs(ratio - 21 / 9) < 0.03) return '21:9';
  if (Math.abs(ratio - 3 / 2) < 0.03) return '3:2';
  if (Math.abs(ratio - 2 / 3) < 0.03) return '2:3';
  if (Math.abs(ratio - 4 / 5) < 0.03) return '4:5';

  const gcd = (a: number, b: number): number => (b === 0 ? a : gcd(b, a % b));
  const d = gcd(Math.round(w), Math.round(h));
  const rw = Math.round(w / d);
  const rh = Math.round(h / d);
  if (rw <= 25 && rh <= 25) {
    return `${rw}:${rh}`;
  }
  return `${ratio.toFixed(2)}:1`;
}

// 从 URL 探测媒体文件格式扩展名
function detectFormat(url: string, fallbackType: '图片' | '视频'): string {
  try {
    const pathname = new URL(url).pathname;
    const ext = pathname.split('.').pop()?.toLowerCase();
    if (ext && ['png', 'jpg', 'jpeg', 'webp', 'gif', 'svg', 'avif', 'bmp', 'mp4', 'mov', 'webm', 'mkv', 'avi'].includes(ext)) {
      return ext === 'jpeg' ? 'JPG' : ext.toUpperCase();
    }
  } catch {}
  return fallbackType === '图片' ? 'IMAGE' : 'VIDEO';
}

// 格式化时长
function formatDuration(sec?: number): string {
  if (sec === undefined || sec === null || isNaN(sec) || sec <= 0) return '-';
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  if (m > 0) {
    return `${m}m ${s}s`;
  }
  return `${sec.toFixed(1)}s`;
}

// 格式化文件大小
function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

// 解析 request_content 中的提示词与关键生成参数
function parseTaskParams(rawJson?: string | null, t?: (key: string, defaultValue: string) => string) {
  if (!rawJson) return { prompt: '', negativePrompt: '', extraParams: [] as { label: string; value: string }[] };
  const tr = (k: string, def: string) => (typeof t === 'function' ? t(k, def) : def);

  try {
    const obj = JSON.parse(rawJson);
    let prompt = '';
    let negativePrompt = '';
    const extraParams: { label: string; value: string }[] = [];

    // 1. 尝试提取 Prompt（兼容 OpenAI / 火山方舟 content 数组 / Google Vertex instances 等格式）
    if (typeof obj.prompt === 'string') {
      prompt = obj.prompt;
    } else if (Array.isArray(obj.prompt)) {
      prompt = obj.prompt.map((p: any) => (typeof p === 'string' ? p : p.text || '')).join(', ');
    } else if (obj.input?.prompt && typeof obj.input.prompt === 'string') {
      prompt = obj.input.prompt;
    } else if (obj.params?.prompt && typeof obj.params.prompt === 'string') {
      prompt = obj.params.prompt;
    } else if (Array.isArray(obj.content)) {
      const textParts = obj.content
        .map((c: any) => (typeof c === 'string' ? c : c?.type === 'text' || typeof c?.text === 'string' ? c.text : ''))
        .filter(Boolean);
      if (textParts.length > 0) {
        prompt = textParts.join('\n');
      }
    } else if (Array.isArray(obj.instances) && obj.instances[0]) {
      const firstInst = obj.instances[0];
      if (typeof firstInst.prompt === 'string') {
        prompt = firstInst.prompt;
      } else if (typeof firstInst.text === 'string') {
        prompt = firstInst.text;
      }
    } else if (Array.isArray(obj.messages)) {
      const lastUser = [...obj.messages].reverse().find((m: any) => m.role === 'user');
      if (lastUser) {
        if (typeof lastUser.content === 'string') {
          prompt = lastUser.content;
        } else if (Array.isArray(lastUser.content)) {
          const textItem = lastUser.content.find((c: any) => c.type === 'text' || typeof c === 'string');
          prompt = typeof textItem === 'string' ? textItem : textItem?.text || '';
        }
      }
    } else if (Array.isArray(obj.text_prompts)) {
      prompt = obj.text_prompts.map((p: any) => p.text || '').filter(Boolean).join(', ');
    }

    // 2. 尝试提取 Negative Prompt
    const firstInstance = Array.isArray(obj.instances) && obj.instances[0] ? obj.instances[0] : {};
    if (typeof obj.negative_prompt === 'string') {
      negativePrompt = obj.negative_prompt;
    } else if (typeof obj.negativePrompt === 'string') {
      negativePrompt = obj.negativePrompt;
    } else if (obj.input?.negative_prompt && typeof obj.input.negative_prompt === 'string') {
      negativePrompt = obj.input.negative_prompt;
    } else if (obj.params?.negative_prompt && typeof obj.params.negative_prompt === 'string') {
      negativePrompt = obj.params.negative_prompt;
    } else if (obj.parameters?.negativePrompt && typeof obj.parameters.negativePrompt === 'string') {
      negativePrompt = obj.parameters.negativePrompt;
    } else if (typeof firstInstance.negativePrompt === 'string') {
      negativePrompt = firstInstance.negativePrompt;
    }

    // 3. 提取常见生成参数（分辨率已在上方「媒体基本属性」中展示，此处不重复显示）
    const extractFields = [
      { keys: ['aspect_ratio', 'aspectRatio', 'ratio'], label: tr('task_logs.param_ratio', '比例') },
      { keys: ['seed'], label: tr('task_logs.param_seed', 'Seed') },
      { keys: ['steps', 'num_inference_steps', 'step'], label: tr('task_logs.param_steps', '步数') },
      { keys: ['cfg_scale', 'guidance_scale', 'cfg'], label: tr('task_logs.param_cfg', 'CFG') },
      { keys: ['quality', 'resolution'], label: tr('task_logs.param_quality', '画质') },
      { keys: ['style'], label: tr('task_logs.param_style', '风格') },
      { keys: ['sampler_name', 'sampler'], label: tr('task_logs.param_sampler', '采样器') },
      { keys: ['duration', 'video_length', 'durationSeconds'], label: tr('task_logs.param_duration', '生成时长') },
      { keys: ['fps', 'frame_rate'], label: tr('task_logs.param_fps', 'FPS') },
      { keys: ['mode'], label: tr('task_logs.param_mode', '模式') },
      { keys: ['model_version', 'version'], label: tr('task_logs.param_version', '版本') },
    ];

    const searchSource = {
      ...obj,
      ...firstInstance,
      ...(obj.input || {}),
      ...(obj.params || {}),
      ...(obj.parameters || {}),
    };

    extractFields.forEach(({ keys, label }) => {
      for (const k of keys) {
        if (searchSource[k] !== undefined && searchSource[k] !== null && searchSource[k] !== '') {
          extraParams.push({
            label,
            value: String(searchSource[k]),
          });
          break;
        }
      }
    });

    // 4. 尝试从火山方舟等尾缀命令行参数（如 --ratio 16:9 --dur 5 --rs 720p --fps 24 --seed 123）补齐参数徽章
    if (prompt && prompt.includes('--')) {
      const flagMap: { regex: RegExp; label: string }[] = [
        { regex: /(?:^|\s)--(?:ratio|rt)\s+([^\s]+)/i, label: tr('task_logs.param_ratio', '比例') },
        { regex: /(?:^|\s)--(?:dur|duration)\s+([^\s]+)/i, label: tr('task_logs.param_duration', '生成时长') },
        { regex: /(?:^|\s)--(?:rs|resolution)\s+([^\s]+)/i, label: tr('task_logs.param_quality', '画质') },
        { regex: /(?:^|\s)--(?:fps|framespersecond)\s+([^\s]+)/i, label: tr('task_logs.param_fps', 'FPS') },
        { regex: /(?:^|\s)--seed\s+([^\s]+)/i, label: tr('task_logs.param_seed', 'Seed') },
      ];
      flagMap.forEach(({ regex, label }) => {
        if (!extraParams.some((p) => p.label === label)) {
          const m = prompt.match(regex);
          if (m && m[1]) {
            extraParams.push({ label, value: m[1] });
          }
        }
      });
    }

    return { prompt, negativePrompt, extraParams };
  } catch {
    return { prompt: '', negativePrompt: '', extraParams: [] as { label: string; value: string }[] };
  }
}

const MediaPreviewModal: React.FC<MediaPreviewModalProps> = ({
  open,
  onClose,
  record,
  urls,
  type,
}) => {
  const { t, i18n } = useTranslation();
  const { themeMode } = useThemeStore();
  const { settings, fetchSettings } = useSettingsStore();
  const currencySymbol = settings?.currency?.currency_symbol || '$';
  const isLight = themeMode === 'light';
  const screens = useBreakpoint();
  const isMobile = !screens.md;

  const [currentIndex, setCurrentIndex] = useState(0);
  const [mediaLoading, setMediaLoading] = useState(true);
  const [mediaError, setMediaError] = useState(false);
  const [mediaAttrs, setMediaAttrs] = useState<MediaAttributes>({});
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const [downloadState, setDownloadState] = useState<'idle' | 'downloading' | 'success'>('idle');
  const [downloadProgress, setDownloadProgress] = useState<number | null>(null);
  const blobCacheRef = useRef<Map<string, Blob>>(new Map());
  const downloadTimerRef = useRef<number | null>(null);

  useEffect(() => {
    if (open && !settings) {
      void fetchSettings();
    }
  }, [open, settings, fetchSettings]);

  const currentUrl = urls[currentIndex] || '';

  // 全屏与图像变换状态
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [zoomScale, setZoomScale] = useState(1);
  const [rotation, setRotation] = useState(0);
  const [isFlipped, setIsFlipped] = useState(false);
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const [isDragging, setIsDragging] = useState(false);
  const [dragStart, setDragStart] = useState({ x: 0, y: 0 });

  const fullscreenContainerRef = useRef<HTMLDivElement | null>(null);
  const fullscreenImageRef = useRef<HTMLDivElement | null>(null);
  const modalImageWrapRef = useRef<HTMLDivElement | null>(null);
  const videoStateRef = useRef<{ currentTime: number; paused: boolean; muted: boolean; volume: number } | null>(null);

  const captureActiveVideoState = useCallback(() => {
    if (type !== '视频') return;
    const v = document.querySelector(
      '.task-preview-fullscreen-container video, .task-preview-modal-body video',
    ) as HTMLVideoElement | null;
    if (v && Number.isFinite(v.currentTime)) {
      videoStateRef.current = {
        currentTime: v.currentTime,
        paused: v.paused,
        muted: v.muted,
        volume: v.volume,
      };
    }
  }, [type]);

  const restoreActiveVideoState = useCallback(() => {
    const saved = videoStateRef.current;
    if (!saved || type !== '视频') return;
    videoStateRef.current = null;
    window.requestAnimationFrame(() => {
      const v = document.querySelector(
        '.task-preview-fullscreen-container video, .task-preview-modal-body video',
      ) as HTMLVideoElement | null;
      if (!v) return;
      try {
        v.muted = saved.muted;
        v.volume = saved.volume;
        if (saved.currentTime > 0) {
          v.currentTime = saved.currentTime;
        }
        if (!saved.paused) {
          void v.play().catch(() => {});
        } else {
          v.pause();
        }
      } catch {}
    });
  }, [type]);

  // 重置缩放、旋转和位移
  const resetTransform = useCallback(() => {
    setZoomScale(1);
    setRotation(0);
    setIsFlipped(false);
    setPan({ x: 0, y: 0 });
  }, []);

  // 切换产物或关闭时重置图形变换与下载状态
  useEffect(() => {
    resetTransform();
    videoStateRef.current = null;
    setDownloadState('idle');
    setDownloadProgress(null);
    if (downloadTimerRef.current) {
      window.clearTimeout(downloadTimerRef.current);
      downloadTimerRef.current = null;
    }
  }, [currentUrl, open, resetTransform]);

  // 全屏切换处理函数（支持真全屏 API 与平滑降级，并保留视频播放进度与状态）
  const toggleFullscreen = useCallback(() => {
    captureActiveVideoState();
    if (!isFullscreen) {
      setIsFullscreen(true);
      setZoomScale(1);
      setPan({ x: 0, y: 0 });
      // 尝试调用真全屏 API
      if (fullscreenContainerRef.current?.requestFullscreen) {
        fullscreenContainerRef.current.requestFullscreen().catch(() => {});
      } else if (document.documentElement.requestFullscreen) {
        document.documentElement.requestFullscreen().catch(() => {});
      }
    } else {
      if (document.fullscreenElement) {
        document.exitFullscreen().catch(() => {});
      }
      setIsFullscreen(false);
      setZoomScale(1);
      setPan({ x: 0, y: 0 });
    }
  }, [isFullscreen, captureActiveVideoState]);

  // 监听原生全屏变化事件
  useEffect(() => {
    const handler = () => {
      if (!document.fullscreenElement && isFullscreen) {
        captureActiveVideoState();
        setIsFullscreen(false);
        setZoomScale(1);
        setPan({ x: 0, y: 0 });
      }
    };
    document.addEventListener('fullscreenchange', handler);
    return () => document.removeEventListener('fullscreenchange', handler);
  }, [isFullscreen, captureActiveVideoState]);

  // 关闭弹窗时退出全屏
  useEffect(() => {
    if (!open) {
      if (document.fullscreenElement) {
        document.exitFullscreen().catch(() => {});
      }
      setIsFullscreen(false);
      resetTransform();
    }
  }, [open, resetTransform]);

  // 键盘快捷键监听 (Esc 退出全屏, F 全屏切换, 方向键切图, +/-/0 缩放, R 旋转)
  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      const tagName = (e.target as HTMLElement)?.tagName;
      if (['INPUT', 'TEXTAREA'].includes(tagName)) return;

      if (e.key === 'Escape' && isFullscreen) {
        e.preventDefault();
        e.stopPropagation();
        toggleFullscreen();
      } else if ((e.key === 'f' || e.key === 'F') && !e.metaKey && !e.ctrlKey) {
        e.preventDefault();
        toggleFullscreen();
      } else if (e.key === 'ArrowLeft' && urls.length > 1) {
        e.preventDefault();
        setCurrentIndex((prev) => Math.max(0, prev - 1));
      } else if (e.key === 'ArrowRight' && urls.length > 1) {
        e.preventDefault();
        setCurrentIndex((prev) => Math.min(urls.length - 1, prev + 1));
      } else if (isFullscreen && type === '图片') {
        if (e.key === '+' || e.key === '=') {
          e.preventDefault();
          setZoomScale((prev) => Math.min(5, Number((prev + 0.25).toFixed(2))));
        } else if (e.key === '-' || e.key === '_') {
          e.preventDefault();
          setZoomScale((prev) => {
            const next = Math.max(0.25, Number((prev - 0.25).toFixed(2)));
            if (next <= 1) setPan({ x: 0, y: 0 });
            return next;
          });
        } else if (e.key === '0') {
          e.preventDefault();
          resetTransform();
        } else if (e.key === 'r' || e.key === 'R') {
          e.preventDefault();
          setRotation((prev) => prev + 90);
        }
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [open, isFullscreen, urls.length, type, toggleFullscreen, resetTransform]);

  // 全屏图片滚轮缩放监听 (passive: false 以便阻止页面滚动)
  useEffect(() => {
    const node = fullscreenImageRef.current;
    if (!node || !isFullscreen || type !== '图片') return;

    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      e.stopPropagation();
      const delta = e.deltaY < 0 ? 0.2 : -0.2;
      setZoomScale((prev) => {
        const next = Math.max(0.25, Math.min(5, Number((prev + delta).toFixed(2))));
        if (next <= 1) setPan({ x: 0, y: 0 });
        return next;
      });
    };

    node.addEventListener('wheel', onWheel, { passive: false });
    return () => node.removeEventListener('wheel', onWheel);
  }, [isFullscreen, type]);

  // 图片拖拽平移事件
  const handleMouseDown = (e: React.MouseEvent) => {
    if (type !== '图片' || zoomScale <= 1) return;
    e.preventDefault();
    setIsDragging(true);
    setDragStart({ x: e.clientX - pan.x, y: e.clientY - pan.y });
  };

  const handleMouseMove = (e: React.MouseEvent) => {
    if (!isDragging) return;
    setPan({
      x: e.clientX - dragStart.x,
      y: e.clientY - dragStart.y,
    });
  };

  const handleMouseUp = useCallback(() => {
    setIsDragging(false);
  }, []);

  // 防止拖拽移出窗口后松开鼠标导致拖拽状态粘连
  useEffect(() => {
    if (!isDragging) return;
    window.addEventListener('mouseup', handleMouseUp);
    window.addEventListener('blur', handleMouseUp);
    return () => {
      window.removeEventListener('mouseup', handleMouseUp);
      window.removeEventListener('blur', handleMouseUp);
    };
  }, [isDragging, handleMouseUp]);

  const handleDoubleClick = () => {
    if (type !== '图片') return;
    if (zoomScale > 1.05) {
      setZoomScale(1);
      setPan({ x: 0, y: 0 });
    } else {
      setZoomScale(2);
    }
  };

  // 切换产物或重置索引
  useEffect(() => {
    if (open) {
      setCurrentIndex(0);
    }
  }, [open, urls]);

  // 当当前 URL 改变时重置媒体属性并多渠道探测文件大小与加载状态
  useEffect(() => {
    if (!currentUrl) return;
    setMediaLoading(true);
    setMediaError(false);
    const format = detectFormat(currentUrl, type);
    setMediaAttrs({ format });

    let isMounted = true;

    // 0. 特殊 URL 处理：base64 与 blob
    if (currentUrl.startsWith('data:')) {
      const base64Str = currentUrl.split(',')[1] || '';
      const bytes = Math.round((base64Str.length * 3) / 4);
      if (bytes > 0) {
        setMediaAttrs((prev) => ({ ...prev, fileSize: formatFileSize(bytes) }));
        return;
      }
    }

    if (currentUrl.startsWith('blob:')) {
      fetch(currentUrl)
        .then((r) => r.blob())
        .then((b) => {
          if (isMounted && b.size > 0) {
            setMediaAttrs((prev) => ({ ...prev, fileSize: formatFileSize(b.size) }));
          }
        })
        .catch(() => {});
      return;
    }

    // 1. 如果响应详情或产物参数中带有文件大小数据，优先解析使用
    if (record?.response_content) {
      try {
        const obj = JSON.parse(record.response_content);
        const findSize = (o: any): number | null => {
          if (!o || typeof o !== 'object') return null;
          if (typeof o.file_size === 'number' && o.file_size > 0) return o.file_size;
          if (typeof o.size_bytes === 'number' && o.size_bytes > 0) return o.size_bytes;
          if (typeof o.filesize === 'number' && o.filesize > 0) return o.filesize;
          if (typeof o.size === 'number' && o.size > 1024) return o.size;
          for (const k of Object.keys(o)) {
            const res = findSize(o[k]);
            if (res) return res;
          }
          return null;
        };
        const s = findSize(obj);
        if (s && isMounted) {
          setMediaAttrs((prev) => ({ ...prev, fileSize: formatFileSize(s) }));
        }
      } catch {}
    }

    // 2. 异步网络探测（支持缓存命中、HEAD、Range 1字节、图片 GET Blob 及后端同源中转）
    const detectSize = async () => {
      // (0) 若内存缓存已有该 URL 的 Blob，直接复用其真实字节大小
      const cachedBlob = blobCacheRef.current.get(currentUrl);
      if (cachedBlob && cachedBlob.size > 0) {
        if (isMounted) setMediaAttrs((prev) => ({ ...prev, fileSize: formatFileSize(cachedBlob.size) }));
        return;
      }

      // (a) 检查浏览器 PerformanceResourceTiming 条目
      try {
        const entries = performance.getEntriesByName(currentUrl);
        if (entries && entries.length > 0) {
          const last = entries[entries.length - 1] as PerformanceResourceTiming;
          const sz = last.encodedBodySize || last.decodedBodySize || last.transferSize;
          if (sz && sz > 0) {
            if (isMounted) setMediaAttrs((prev) => ({ ...prev, fileSize: formatFileSize(sz) }));
            return;
          }
        }
      } catch {}

      // (b) 尝试轻量 HEAD 请求
      try {
        const headRes = await fetch(currentUrl, { method: 'HEAD', mode: 'cors' });
        const cl = headRes.headers.get('content-length');
        if (cl) {
          const bytes = parseInt(cl, 10);
          if (!isNaN(bytes) && bytes > 0) {
            if (isMounted) setMediaAttrs((prev) => ({ ...prev, fileSize: formatFileSize(bytes) }));
            return;
          }
        }
      } catch {}

      // (c) 尝试 Range: bytes=0-0 只请求 1 字节（绝大多数云存储如阿里云OSS、腾讯云COS、S3均支持返回 Content-Range）
      try {
        const rangeRes = await fetch(currentUrl, {
          method: 'GET',
          headers: { Range: 'bytes=0-0' },
          mode: 'cors',
        });
        const cr = rangeRes.headers.get('content-range');
        if (cr) {
          const totalStr = cr.split('/')[1];
          if (totalStr && totalStr !== '*') {
            const bytes = parseInt(totalStr, 10);
            if (!isNaN(bytes) && bytes > 0) {
              if (isMounted) setMediaAttrs((prev) => ({ ...prev, fileSize: formatFileSize(bytes) }));
              return;
            }
          }
        }
        const cl = rangeRes.headers.get('content-length');
        if (cl && rangeRes.status === 200) {
          const bytes = parseInt(cl, 10);
          if (!isNaN(bytes) && bytes > 0) {
            if (isMounted) setMediaAttrs((prev) => ({ ...prev, fileSize: formatFileSize(bytes) }));
            return;
          }
        }
      } catch {}

      // (d) 若是图片，尝试直接拉取已在浏览器缓存中的 blob（无需二次从远端传输），失败则走后端同源中转兜底
      // 注意：视频文件体积较大，打开预览时不自动全量下载整个视频 Blob，避免抢占视频流加载带宽
      if (type === '图片') {
        try {
          const getRes = await fetch(currentUrl, { method: 'GET', mode: 'cors' });
          if (getRes.ok) {
            const blob = await getRes.blob();
            if (blob && blob.size > 0) {
              blobCacheRef.current.set(currentUrl, blob);
              if (isMounted) setMediaAttrs((prev) => ({ ...prev, fileSize: formatFileSize(blob.size) }));
              return;
            }
          }
        } catch {}

        try {
          const data = await request.get('/playground/download-file', {
            params: { url: currentUrl },
            responseType: 'blob',
            timeout: 10000,
            ...({ skipErrorHandler: true } as any),
          });
          if (data instanceof Blob && data.size > 0 && !data.type?.includes('json')) {
            blobCacheRef.current.set(currentUrl, data);
            if (isMounted) setMediaAttrs((prev) => ({ ...prev, fileSize: formatFileSize(data.size) }));
            return;
          }
        } catch {}
      }
    };

    detectSize();

    return () => {
      isMounted = false;
    };
  }, [currentUrl, type, record?.response_content]);

  // 图片加载完成回调
  const handleImageLoad = (e: React.SyntheticEvent<HTMLImageElement>) => {
    const img = e.currentTarget;
    const w = img.naturalWidth || img.width;
    const h = img.naturalHeight || img.height;
    setMediaLoading(false);
    setMediaAttrs((prev) => ({
      ...prev,
      width: w,
      height: h,
      aspectRatio: calculateAspectRatio(w, h),
    }));
  };

  // 视频加载元数据回调
  const handleArtVideoMeta = (info: { width: number; height: number; duration?: number }) => {
    const w = info.width;
    const h = info.height;
    setMediaLoading(false);
    setMediaAttrs((prev) => ({
      ...prev,
      width: w,
      height: h,
      aspectRatio: calculateAspectRatio(w, h),
      duration: info.duration,
    }));
    restoreActiveVideoState();
  };

  // 计算图片旋转 90°/270° 时的自适应缩放比例，防止横竖图旋转后超出视口裁切
  const getRotatedFitScale = (containerW?: number, containerH?: number): number => {
    if (Math.abs(rotation) % 180 !== 90) return 1;
    const imgW = mediaAttrs.width;
    const imgH = mediaAttrs.height;
    if (!imgW || !imgH) return 0.72;
    const cw = containerW && containerW > 0 ? containerW : imgW;
    const ch = containerH && containerH > 0 ? containerH : imgH;
    const baseScale = Math.min(cw / imgW, ch / imgH);
    const boxW = imgW * baseScale;
    const boxH = imgH * baseScale;
    return Math.min(1, cw / boxH, ch / boxW);
  };

  // 复制文本辅助函数
  const handleCopy = async (text: string, key: string) => {
    const ok = await copyToClipboard(text);
    if (ok) {
      setCopiedKey(key);
      message.success(t('logs.copy_success', '已复制'));
      setTimeout(() => setCopiedKey(null), 2000);
    } else {
      message.error(t('logs.copy_failed', '复制失败'));
    }
  };

  // 解析并规范化下载文件名与扩展名
  const resolveDownloadFilename = (url: string, blob?: Blob | null): string => {
    const validExts = ['png', 'jpg', 'jpeg', 'webp', 'gif', 'svg', 'avif', 'bmp', 'mp4', 'mov', 'webm', 'mkv', 'avi'];
    let ext = '';
    if (blob?.type) {
      const mime = blob.type.toLowerCase();
      if (mime.includes('png')) ext = 'png';
      else if (mime.includes('jpeg') || mime.includes('jpg')) ext = 'jpg';
      else if (mime.includes('webp')) ext = 'webp';
      else if (mime.includes('gif')) ext = 'gif';
      else if (mime.includes('svg')) ext = 'svg';
      else if (mime.includes('mp4')) ext = 'mp4';
      else if (mime.includes('webm')) ext = 'webm';
      else if (mime.includes('quicktime') || mime.includes('mov')) ext = 'mov';
    }
    if (!ext && !url.startsWith('data:')) {
      try {
        const pathname = new URL(url, window.location.origin).pathname;
        const candidate = pathname.split('.').pop()?.toLowerCase() || '';
        if (validExts.includes(candidate)) {
          ext = candidate === 'jpeg' ? 'jpg' : candidate;
        }
      } catch {}
    }
    if (!ext) {
      const fmt = mediaAttrs.format?.toLowerCase();
      if (fmt && validExts.includes(fmt)) {
        ext = fmt === 'jpeg' ? 'jpg' : fmt;
      } else {
        ext = type === '图片' ? 'png' : 'mp4';
      }
    }

    if (!url.startsWith('data:') && !url.startsWith('blob:')) {
      try {
        const pathname = new URL(url, window.location.origin).pathname;
        const rawSegment = decodeURIComponent(pathname.split('/').pop() || '').trim();
        const sanitized = rawSegment.replace(/[\\/:*?"<>|\u0000-\u001f]+/g, '_');
        if (
          sanitized &&
          sanitized.length <= 80 &&
          !/^(file|download|output|content|result|media|video|image)$/i.test(sanitized.replace(/\.[a-z0-9]+$/i, ''))
        ) {
          if (/\.[a-z0-9]{2,5}$/i.test(sanitized)) {
            return sanitized.replace(/\.jpeg$/i, '.jpg');
          }
          return `${sanitized}.${ext}`;
        }
      } catch {}
    }

    const prefix = type === '图片' ? 'image' : 'video';
    const idPart = (record?.task_id || record?.log_id || String(record?.id || Date.now()))
      .replace(/[\\/:*?"<>|\u0000-\u001f]+/g, '_')
      .slice(-24);
    const idxSuffix = urls.length > 1 ? `_${currentIndex + 1}` : '';
    return `${prefix}_${idPart}${idxSuffix}.${ext}`;
  };

  // 下载文件辅助函数（带按钮实时进度/状态反馈、内存缓存直取与后端中转兜底）
  const handleDownload = async (url: string) => {
    if (!url || downloadState === 'downloading') return;
    if (downloadTimerRef.current) {
      window.clearTimeout(downloadTimerRef.current);
      downloadTimerRef.current = null;
    }

    setDownloadState('downloading');
    setDownloadProgress(null);

    try {
      let blob: Blob | null = blobCacheRef.current.get(url) || null;

      if (!blob || !blob.size) {
        if (url.startsWith('data:') || url.startsWith('blob:')) {
          const res = await fetch(url);
          if (res.ok) blob = await res.blob();
        } else {
          try {
            const res = await fetch(url, { mode: 'cors', credentials: 'omit' });
            if (res.ok) {
              const b = await res.blob();
              if (b.size > 0) blob = b;
            }
          } catch {}
        }
      }

      if (!blob || !blob.size) {
        const fallbackName = resolveDownloadFilename(url);
        const resolvedUrl =
          url.startsWith('http://') || url.startsWith('https://')
            ? url
            : new URL(url, window.location.origin).href;
        try {
          const data = await request.get('/playground/download-file', {
            params: { url: resolvedUrl, filename: fallbackName },
            responseType: 'blob',
            timeout: 300000,
            onDownloadProgress: (evt) => {
              if (evt.total && evt.total > 0) {
                const pct = Math.min(99, Math.max(1, Math.round((evt.loaded / evt.total) * 100)));
                setDownloadProgress(pct);
              }
            },
            ...({ skipErrorHandler: true } as any),
          });
          if (data instanceof Blob && data.size > 0 && !data.type?.includes('json')) {
            blob = data;
          }
        } catch {}
      }

      if (blob && blob.size > 0) {
        blobCacheRef.current.set(url, blob);
        setMediaAttrs((prev) => (prev.fileSize ? prev : { ...prev, fileSize: formatFileSize(blob!.size) }));

        const filename = resolveDownloadFilename(url, blob);
        const downloadBlob = new Blob([blob], { type: 'application/octet-stream' });
        const blobUrl = URL.createObjectURL(downloadBlob);
        const a = document.createElement('a');
        a.href = blobUrl;
        a.download = filename;
        a.rel = 'noopener';
        a.style.display = 'none';
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        window.setTimeout(() => URL.revokeObjectURL(blobUrl), 10000);

        setDownloadState('success');
        setDownloadProgress(null);
        downloadTimerRef.current = window.setTimeout(() => {
          setDownloadState('idle');
          downloadTimerRef.current = null;
        }, 2500);
        return;
      }
    } catch {}

    setDownloadState('idle');
    setDownloadProgress(null);
    const fallbackFilename = resolveDownloadFilename(url);
    const a = document.createElement('a');
    a.href = url;
    a.target = '_blank';
    a.rel = 'noopener noreferrer';
    a.download = fallbackFilename;
    a.style.display = 'none';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
  };

  const parsedParams = useMemo(() => {
    return parseTaskParams(record?.request_content, t);
  }, [record?.request_content, t]);

  // shadcn UI 纯正黑白灰 Zinc 配色系统
  const colors = useMemo(() => {
    return {
      modalBg: isLight ? '#ffffff' : '#09090b',
      viewportBg: isLight ? '#0c0c0e' : '#050506', // 沉浸式视口，无冗余灰色大外框
      border: isLight ? '#e4e4e7' : '#27272a',
      borderSubtle: isLight ? '#f4f4f5' : '#18181b',
      cardBg: isLight ? '#f8fafc' : '#111113',
      subtleBg: isLight ? '#f4f4f5' : '#18181b',
      textPrimary: isLight ? '#09090b' : '#fafafa',
      textSecondary: isLight ? '#71717a' : '#a1a1aa',
      textMuted: isLight ? '#a1a1aa' : '#71717a',
      hoverBg: isLight ? '#f4f4f5' : '#27272a',
      badgeBg: isLight ? '#f4f4f5' : '#27272a',
      badgeText: isLight ? '#18181b' : '#f4f4f5',
    };
  }, [isLight]);

  if (!open) return null;

  return (
    <>
      <Modal
      title={null}
      open={open}
      onCancel={onClose}
      keyboard={!isFullscreen}
      footer={null}
      closable={false}
      className="task-preview-modal"
      rootClassName="task-preview-modal-root"
      width={isMobile ? '95vw' : 1000}
      style={{
        maxWidth: isMobile ? '100vw' : 1000,
        padding: 0,
        margin: isMobile ? '0 auto' : undefined,
        top: isMobile ? 12 : undefined,
      }}
      destroyOnClose
      centered={!isMobile}
      styles={{
        mask: { backgroundColor: 'rgba(0, 0, 0, 0.75)', backdropFilter: 'blur(10px)' },
        wrapper: { padding: 0 },
        body: {
          backgroundColor: 'transparent',
          background: 'transparent',
          padding: 0,
          margin: 0,
        },
      }}
    >
      <style>{`
        .task-preview-modal.ant-modal,
        .task-preview-modal-root .ant-modal {
          padding: 0 !important;
          max-width: 100vw !important;
        }
        .task-preview-modal .ant-modal-content,
        .task-preview-modal-root .ant-modal-content,
        .task-preview-modal .ant-modal-container,
        .task-preview-modal-root .ant-modal-container,
        div.task-preview-modal div.ant-modal-content {
          background: transparent !important;
          background-color: transparent !important;
          padding: 0 !important;
          margin: 0 !important;
          box-shadow: none !important;
          border: none !important;
          border-radius: 0 !important;
        }
        .task-preview-modal .ant-modal-body,
        .task-preview-modal-root .ant-modal-body,
        div.task-preview-modal div.ant-modal-body {
          padding: 0 !important;
          margin: 0 !important;
          background: transparent !important;
          background-color: transparent !important;
        }
        .task-preview-modal .ant-modal-close,
        .task-preview-modal-root .ant-modal-close,
        .task-preview-modal .ant-modal-header,
        .task-preview-modal-root .ant-modal-header {
          display: none !important;
        }
        .task-preview-modal-body {
          display: grid;
          grid-template-columns: minmax(360px, 1fr) 360px;
          height: calc(85vh - 58px);
          max-height: 720px;
          min-height: 500px;
          overflow: hidden;
        }
        @media (max-width: 768px) {
          .task-preview-modal-body {
            display: flex !important;
            flex-direction: column !important;
            height: auto !important;
            max-height: calc(88vh - 52px) !important;
            min-height: auto !important;
            overflow-y: auto !important;
            -webkit-overflow-scrolling: touch;
          }
          .task-preview-media-pane {
            min-height: 220px !important;
            max-height: 320px !important;
            padding: 8px !important;
            border-right: none !important;
            border-bottom: 1px solid ${colors.border} !important;
            flex-shrink: 0;
          }
          .task-preview-info-pane {
            padding: 16px !important;
            gap: 16px !important;
          }
        }
      `}</style>

      {/* 真正且唯一的弹窗外壳：纯黑白灰 shadcn 风格 */}
      <div
        style={{
          width: '100%',
          backgroundColor: colors.modalBg,
          borderRadius: isMobile ? 12 : 16,
          border: `1px solid ${colors.border}`,
          overflow: 'hidden',
          boxShadow: isLight
            ? '0 25px 50px -12px rgba(0, 0, 0, 0.15)'
            : '0 25px 50px -12px rgba(0, 0, 0, 0.8)',
          display: 'flex',
          flexDirection: 'column',
        }}
      >
        {/* 顶部 Header：标题、模型 Tag 与快捷操作 (shadcn 纯黑白灰风格) */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'space-between',
            padding: isMobile ? '10px 14px' : '12px 20px',
            borderBottom: `1px solid ${colors.border}`,
            background: colors.modalBg,
          }}
        >
        <div style={{ display: 'flex', alignItems: 'center', gap: isMobile ? 8 : 12, overflow: 'hidden' }}>
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              width: 28,
              height: 28,
              borderRadius: 6,
              background: colors.subtleBg,
              border: `1px solid ${colors.border}`,
              color: colors.textPrimary,
              flexShrink: 0,
            }}
          >
            {type === '图片' ? <ImageIcon size={15} /> : <Video size={15} />}
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, overflow: 'hidden' }}>
            <span style={{ fontSize: isMobile ? 13 : 14, fontWeight: 600, color: colors.textPrimary, whiteSpace: 'nowrap', letterSpacing: '-0.01em' }}>
              {type === '图片' ? t('task_logs.image_preview', '图片预览') : t('task_logs.video_preview', '视频预览')}
            </span>
            {record?.model && (
              <span
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  fontSize: 11,
                  fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
                  borderRadius: 6,
                  padding: '2px 7px',
                  background: colors.badgeBg,
                  color: colors.badgeText,
                  border: `1px solid ${colors.border}`,
                  fontWeight: 500,
                  maxWidth: isMobile ? 110 : 200,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap',
                }}
              >
                {record.model}
              </span>
            )}
          </div>
        </div>

        {/* 顶部操作按钮 (shadcn outline/ghost button) */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexShrink: 0 }}>
          {type === '图片' && (
            <>
              <Tooltip title={t('task_logs.mirror_image', '镜像图片')}>
                <button
                  onClick={() => setIsFlipped((prev) => !prev)}
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    gap: 5,
                    padding: isMobile ? '0 8px' : '0 10px',
                    height: 28,
                    fontSize: 12,
                    borderRadius: 6,
                    border: `1px solid ${colors.border}`,
                    background: isFlipped ? colors.hoverBg : 'transparent',
                    color: colors.textPrimary,
                    cursor: 'pointer',
                    transition: 'all 0.15s ease',
                  }}
                  onMouseEnter={(e) => (e.currentTarget.style.background = colors.hoverBg)}
                  onMouseLeave={(e) => (e.currentTarget.style.background = isFlipped ? colors.hoverBg : 'transparent')}
                >
                  <FlipHorizontal2 size={13} />
                  {!isMobile && <span>{t('task_logs.mirror_btn', '镜像')}</span>}
                </button>
              </Tooltip>

              <Tooltip title={t('task_logs.rotate_image', '旋转图片')}>
                <button
                  onClick={() => setRotation((prev) => prev + 90)}
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    gap: 5,
                    padding: isMobile ? '0 8px' : '0 10px',
                    height: 28,
                    fontSize: 12,
                    borderRadius: 6,
                    border: `1px solid ${colors.border}`,
                    background: 'transparent',
                    color: colors.textPrimary,
                    cursor: 'pointer',
                    transition: 'all 0.15s ease',
                  }}
                  onMouseEnter={(e) => (e.currentTarget.style.background = colors.hoverBg)}
                  onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                >
                  <RotateCw size={13} />
                  {!isMobile && <span>{t('task_logs.rotate_btn', '旋转')}</span>}
                </button>
              </Tooltip>
            </>
          )}

          <Tooltip title={t('task_logs.fullscreen_hint', '全屏放大观看 (Esc 退出)')}>
            <button
              onClick={toggleFullscreen}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                gap: 5,
                padding: isMobile ? '0 8px' : '0 10px',
                height: 28,
                fontSize: 12,
                borderRadius: 6,
                border: `1px solid ${colors.border}`,
                background: 'transparent',
                color: colors.textPrimary,
                cursor: 'pointer',
                transition: 'all 0.15s ease',
              }}
              onMouseEnter={(e) => (e.currentTarget.style.background = colors.hoverBg)}
              onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
            >
              <Maximize2 size={13} />
              {!isMobile && <span>{t('task_logs.zoom_btn', '放大')}</span>}
            </button>
          </Tooltip>

          <Tooltip title={t('task_logs.copy_url', '复制媒体链接')}>
            <button
              onClick={() => handleCopy(currentUrl, 'url')}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                gap: 5,
                padding: isMobile ? '0 8px' : '0 10px',
                height: 28,
                fontSize: 12,
                borderRadius: 6,
                border: `1px solid ${colors.border}`,
                background: 'transparent',
                color: colors.textPrimary,
                cursor: 'pointer',
                transition: 'all 0.15s ease',
              }}
              onMouseEnter={(e) => (e.currentTarget.style.background = colors.hoverBg)}
              onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
            >
              {copiedKey === 'url' ? <Check size={13} style={{ color: colors.textPrimary }} /> : <Copy size={13} />}
              {!isMobile && <span>{copiedKey === 'url' ? t('common.copied', '已复制') : t('task_logs.copy_link', '复制链接')}</span>}
            </button>
          </Tooltip>

          <Tooltip
            title={
              downloadState === 'downloading'
                ? t('task_logs.downloading_tip', '正在下载媒体文件，请稍候...')
                : downloadState === 'success'
                  ? t('task_logs.download_started', '已开始下载')
                  : t('task_logs.download', '下载媒体')
            }
          >
            <button
              disabled={downloadState === 'downloading'}
              onClick={() => handleDownload(currentUrl)}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                gap: 5,
                padding: isMobile ? '0 8px' : '0 10px',
                height: 28,
                fontSize: 12,
                borderRadius: 6,
                border: `1px solid ${colors.border}`,
                background: downloadState === 'downloading' ? colors.hoverBg : 'transparent',
                color: colors.textPrimary,
                cursor: downloadState === 'downloading' ? 'wait' : 'pointer',
                opacity: downloadState === 'downloading' ? 0.85 : 1,
                transition: 'all 0.15s ease',
              }}
              onMouseEnter={(e) => {
                if (downloadState !== 'downloading') e.currentTarget.style.background = colors.hoverBg;
              }}
              onMouseLeave={(e) => {
                if (downloadState !== 'downloading') e.currentTarget.style.background = 'transparent';
              }}
            >
              {downloadState === 'downloading' ? (
                <Loader2 size={13} className="animate-spin" />
              ) : downloadState === 'success' ? (
                <Check size={13} style={{ color: colors.textPrimary }} />
              ) : (
                <Download size={13} />
              )}
              {!isMobile && (
                <span>
                  {downloadState === 'downloading'
                    ? downloadProgress !== null
                      ? `${t('task_logs.downloading_btn', '下载中')} ${downloadProgress}%`
                      : t('task_logs.downloading_btn', '下载中...')
                    : downloadState === 'success'
                      ? t('task_logs.downloaded_btn', '已下载')
                      : t('task_logs.download_btn', '下载')}
                </span>
              )}
            </button>
          </Tooltip>

          <Tooltip title={t('common.close', '关闭')}>
            <button
              onClick={onClose}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                width: 28,
                height: 28,
                padding: 0,
                borderRadius: 6,
                border: `1px solid ${colors.border}`,
                background: 'transparent',
                color: colors.textSecondary,
                cursor: 'pointer',
                marginLeft: 2,
                transition: 'all 0.15s ease',
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.background = colors.hoverBg;
                e.currentTarget.style.color = colors.textPrimary;
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.background = 'transparent';
                e.currentTarget.style.color = colors.textSecondary;
              }}
            >
              <X size={14} />
            </button>
          </Tooltip>
        </div>
      </div>

      {/* 主内容区域：左侧纯净沉浸式媒体区（无冗余灰色外框），右侧 shadcn 属性面板 */}
      <div className="task-preview-modal-body">
        {/* 左侧媒体展示区 (沉浸式黑色底，去除厚重灰色大外框与生硬阴影) */}
        <div
          className="task-preview-media-pane"
          style={{
            display: 'flex',
            flexDirection: 'column',
            justifyContent: 'center',
            alignItems: 'center',
            background: colors.viewportBg,
            position: 'relative',
            overflow: 'hidden',
            borderRight: `1px solid ${colors.border}`,
            padding: 16,
          }}
        >
          {mediaLoading && !mediaError && type !== '视频' && (
            <div style={{ position: 'absolute', zIndex: 10, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 10 }}>
              <Spin size="default" />
              <span style={{ fontSize: 12, color: '#a1a1aa', letterSpacing: '0.02em' }}>
                {t('task_logs.loading_media', '正在加载媒体...')}
              </span>
            </div>
          )}

          {mediaError ? (
            <div
              style={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                justifyContent: 'center',
                textAlign: 'center',
                padding: 24,
                maxWidth: 360,
                gap: 12,
              }}
            >
              <AlertCircle size={32} style={{ color: '#ef4444' }} />
              <span style={{ fontSize: 13, fontWeight: 500, color: '#fafafa' }}>
                {type === '图片'
                  ? t('task_logs.image_load_failed', '图片加载失败或链接已失效')
                  : t('task_logs.video_load_failed', '视频加载失败或链接已失效')}
              </span>
              <span style={{ fontSize: 11, color: '#71717a', wordBreak: 'break-all', fontFamily: 'monospace' }}>
                {currentUrl}
              </span>
              <a
                href={currentUrl}
                target="_blank"
                rel="noreferrer"
                style={{
                  fontSize: 12,
                  color: '#e4e4e7',
                  textDecoration: 'underline',
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 4,
                  marginTop: 4,
                }}
              >
                <ExternalLink size={12} /> {t('task_logs.open_in_new_tab', '在新窗口中尝试打开')}
              </a>
            </div>
          ) : (
            <div
              ref={modalImageWrapRef}
              style={{
                width: '100%',
                height: '100%',
                display: 'flex',
                justifyContent: 'center',
                alignItems: 'center',
                position: 'relative',
              }}
            >
              {type === '图片' ? (
                <img
                  src={currentUrl}
                  alt="preview"
                  onLoad={handleImageLoad}
                  onError={() => {
                    setMediaLoading(false);
                    setMediaError(true);
                  }}
                  style={{
                    maxWidth: '100%',
                    maxHeight: '100%',
                    objectFit: 'contain',
                    borderRadius: 6,
                    opacity: mediaLoading ? 0 : 1,
                    transform: `scale(${getRotatedFitScale(modalImageWrapRef.current?.clientWidth, modalImageWrapRef.current?.clientHeight)}) rotate(${rotation}deg) scaleX(${isFlipped ? -1 : 1})`,
                    transition: 'opacity 0.2s ease-in-out, transform 0.2s ease-in-out',
                    userSelect: 'none',
                  }}
                />
              ) : !isFullscreen ? (
                <ArtPlayerPreview
                  url={currentUrl}
                  autoplay={false}
                  muted
                  locale={i18n.language}
                  theme={isLight ? '#18181b' : '#e4e4e7'}
                  onReady={() => {
                    setMediaLoading(false);
                    restoreActiveVideoState();
                  }}
                  onError={() => {
                    setMediaLoading(false);
                    setMediaError(true);
                  }}
                  onLoadedMetadata={handleArtVideoMeta}
                />
              ) : null}
            </div>
          )}

          {/* 多媒体产物缩略图切换栏 (shadcn 悬浮胶囊设计) */}
          {urls.length > 1 && (
            <div
              style={{
                position: 'absolute',
                bottom: 12,
                display: 'flex',
                alignItems: 'center',
                gap: 6,
                padding: '4px 10px',
                background: 'rgba(24, 24, 27, 0.85)',
                backdropFilter: 'blur(12px)',
                borderRadius: 9999,
                border: '1px solid rgba(255, 255, 255, 0.12)',
                boxShadow: '0 8px 20px rgba(0, 0, 0, 0.4)',
                zIndex: 20,
              }}
            >
              <button
                disabled={currentIndex === 0}
                onClick={() => setCurrentIndex((prev) => Math.max(0, prev - 1))}
                style={{
                  border: 'none',
                  background: 'transparent',
                  cursor: currentIndex === 0 ? 'not-allowed' : 'pointer',
                  opacity: currentIndex === 0 ? 0.3 : 1,
                  color: '#fafafa',
                  display: 'flex',
                  alignItems: 'center',
                  padding: 2,
                }}
              >
                <ChevronLeft size={15} />
              </button>

              <div style={{ display: 'flex', gap: 6, overflowX: 'auto', maxWidth: isMobile ? 200 : 280, padding: '2px 0' }}>
                {urls.map((u, idx) => {
                  const isCur = idx === currentIndex;
                  return (
                    <div
                      key={idx}
                      onClick={() => setCurrentIndex(idx)}
                      style={{
                        width: 30,
                        height: 30,
                        borderRadius: 6,
                        overflow: 'hidden',
                        cursor: 'pointer',
                        border: isCur ? '2px solid #ffffff' : '1px solid rgba(255, 255, 255, 0.2)',
                        opacity: isCur ? 1 : 0.5,
                        transition: 'all 0.15s ease',
                        flexShrink: 0,
                        background: '#18181b',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                      }}
                    >
                      {type === '图片' ? (
                        <img src={u} alt="" style={{ width: '100%', height: '100%', objectFit: 'cover' }} />
                      ) : (
                        <span style={{ fontSize: 10, fontWeight: 600, color: '#ffffff' }}>#{idx + 1}</span>
                      )}
                    </div>
                  );
                })}
              </div>

              <button
                disabled={currentIndex === urls.length - 1}
                onClick={() => setCurrentIndex((prev) => Math.min(urls.length - 1, prev + 1))}
                style={{
                  border: 'none',
                  background: 'transparent',
                  cursor: currentIndex === urls.length - 1 ? 'not-allowed' : 'pointer',
                  opacity: currentIndex === urls.length - 1 ? 0.3 : 1,
                  color: '#fafafa',
                  display: 'flex',
                  alignItems: 'center',
                  padding: 2,
                }}
              >
                <ChevronRight size={15} />
              </button>

              <span style={{ fontSize: 11, color: '#a1a1aa', marginLeft: 4, fontFamily: 'monospace' }}>
                {currentIndex + 1}/{urls.length}
              </span>
            </div>
          )}
        </div>

        {/* 右侧属性与生成信息面板 (shadcn 纯黑白灰、极简层级排版，无厚重杂乱外框) */}
        <div
          className="task-preview-info-pane"
          style={{
            padding: '18px 20px',
            overflowY: 'auto',
            background: colors.modalBg,
            display: 'flex',
            flexDirection: 'column',
            gap: 20,
          }}
        >
          {/* 1. 媒体基本属性 (Media Metadata) */}
          <div>
            <div
              style={{
                fontSize: 11,
                fontWeight: 600,
                color: colors.textSecondary,
                textTransform: 'uppercase',
                letterSpacing: '0.06em',
                marginBottom: 10,
                display: 'flex',
                alignItems: 'center',
                gap: 5,
              }}
            >
              <Info size={13} />
              <span>{t('task_logs.media_properties', '媒体基本属性')}</span>
            </div>

            <div
              style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(2, 1fr)',
                gap: 8,
              }}
            >
              {/* 分辨率 */}
              <div
                style={{
                  background: colors.cardBg,
                  border: `1px solid ${colors.border}`,
                  borderRadius: 8,
                  padding: '8px 12px',
                }}
              >
                <div style={{ fontSize: 11, color: colors.textSecondary, display: 'flex', alignItems: 'center', gap: 4 }}>
                  <Maximize2 size={12} />
                  <span>{t('task_logs.prop_dimensions', '分辨率')}</span>
                </div>
                <div style={{ fontSize: 13, fontWeight: 600, color: colors.textPrimary, marginTop: 3, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace' }}>
                  {mediaAttrs.width && mediaAttrs.height ? `${mediaAttrs.width} × ${mediaAttrs.height}` : '-'}
                </div>
              </div>

              {/* 宽高比 */}
              <div
                style={{
                  background: colors.cardBg,
                  border: `1px solid ${colors.border}`,
                  borderRadius: 8,
                  padding: '8px 12px',
                }}
              >
                <div style={{ fontSize: 11, color: colors.textSecondary, display: 'flex', alignItems: 'center', gap: 4 }}>
                  <Ratio size={12} />
                  <span>{t('task_logs.prop_aspect_ratio', '宽高比')}</span>
                </div>
                <div style={{ fontSize: 13, fontWeight: 600, color: colors.textPrimary, marginTop: 3, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace' }}>
                  {mediaAttrs.aspectRatio || '-'}
                </div>
              </div>

              {/* 文件格式 */}
              <div
                style={{
                  background: colors.cardBg,
                  border: `1px solid ${colors.border}`,
                  borderRadius: 8,
                  padding: '8px 12px',
                }}
              >
                <div style={{ fontSize: 11, color: colors.textSecondary, display: 'flex', alignItems: 'center', gap: 4 }}>
                  <Film size={12} />
                  <span>{t('task_logs.prop_format', '文件格式')}</span>
                </div>
                <div style={{ fontSize: 13, fontWeight: 600, color: colors.textPrimary, marginTop: 3, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace' }}>
                  {mediaAttrs.format || '-'}
                </div>
              </div>

              {/* 文件大小（图片与视频均清晰展示大小） */}
              <div
                style={{
                  background: colors.cardBg,
                  border: `1px solid ${colors.border}`,
                  borderRadius: 8,
                  padding: '8px 12px',
                }}
              >
                <div style={{ fontSize: 11, color: colors.textSecondary, display: 'flex', alignItems: 'center', gap: 4 }}>
                  <HardDrive size={12} />
                  <span>{t('task_logs.prop_file_size', '文件大小')}</span>
                </div>
                <div style={{ fontSize: 13, fontWeight: 600, color: colors.textPrimary, marginTop: 3, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace' }}>
                  {mediaAttrs.fileSize || '-'}
                </div>
              </div>

              {/* 视频时长（仅视频显示，横跨两列） */}
              {type === '视频' && (
                <div
                  style={{
                    gridColumn: 'span 2',
                    background: colors.cardBg,
                    border: `1px solid ${colors.border}`,
                    borderRadius: 8,
                    padding: '8px 12px',
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                  }}
                >
                  <div style={{ fontSize: 11, color: colors.textSecondary, display: 'flex', alignItems: 'center', gap: 4 }}>
                    <Clock size={12} />
                    <span>{t('task_logs.prop_duration', '视频时长')}</span>
                  </div>
                  <div style={{ fontSize: 13, fontWeight: 600, color: colors.textPrimary, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace' }}>
                    {formatDuration(mediaAttrs.duration)}
                  </div>
                </div>
              )}
            </div>
          </div>

          {/* 2. 任务与生成信息 (Task Details) */}
          <div>
            <div
              style={{
                fontSize: 11,
                fontWeight: 600,
                color: colors.textSecondary,
                textTransform: 'uppercase',
                letterSpacing: '0.06em',
                marginBottom: 10,
                display: 'flex',
                alignItems: 'center',
                gap: 5,
              }}
            >
              <Cpu size={13} />
              <span>{t('task_logs.task_info', '任务生成信息')}</span>
            </div>

            <div
              style={{
                background: colors.cardBg,
                border: `1px solid ${colors.border}`,
                borderRadius: 8,
                padding: '6px 12px',
                display: 'flex',
                flexDirection: 'column',
                fontSize: 12,
              }}
            >
              {/* 用户 UID */}
              {(record?.user_uid || record?.user_id) && (
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '6px 0', borderBottom: `1px solid ${colors.borderSubtle}` }}>
                  <span style={{ color: colors.textSecondary }}>{t('logs.user_uid', '用户 UID')}</span>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace', color: colors.textPrimary, fontSize: 11 }}>
                      {record.user_uid ? `UID: ${record.user_uid}` : record.user_id}
                    </span>
                    <button
                      onClick={() => handleCopy(String(record.user_uid || record.user_id), 'user_uid')}
                      style={{
                        border: 'none',
                        background: 'transparent',
                        padding: 2,
                        cursor: 'pointer',
                        color: colors.textMuted,
                        display: 'inline-flex',
                        alignItems: 'center',
                      }}
                      title={t('logs.copy', '复制')}
                    >
                      {copiedKey === 'user_uid' ? <Check size={12} style={{ color: colors.textPrimary }} /> : <Copy size={12} />}
                    </button>
                  </div>
                </div>
              )}

              {/* 任务 ID */}
              {record?.task_id && (
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '6px 0', borderBottom: `1px solid ${colors.borderSubtle}` }}>
                  <span style={{ color: colors.textSecondary }}>{t('task_logs.task_id', '任务 ID')}</span>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace', color: colors.textPrimary, fontSize: 11 }}>
                      {record.task_id}
                    </span>
                    <button
                      onClick={() => handleCopy(record.task_id!, 'task_id')}
                      style={{
                        border: 'none',
                        background: 'transparent',
                        padding: 2,
                        cursor: 'pointer',
                        color: colors.textMuted,
                        display: 'inline-flex',
                        alignItems: 'center',
                      }}
                      title={t('logs.copy', '复制')}
                    >
                      {copiedKey === 'task_id' ? <Check size={12} style={{ color: colors.textPrimary }} /> : <Copy size={12} />}
                    </button>
                  </div>
                </div>
              )}

              {/* 日志 ID */}
              {record?.log_id && (
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '6px 0', borderBottom: `1px solid ${colors.borderSubtle}` }}>
                  <span style={{ color: colors.textSecondary }}>{t('logs.log_id', '日志 ID')}</span>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace', color: colors.textPrimary, fontSize: 11 }}>
                      {record.log_id}
                    </span>
                    <button
                      onClick={() => handleCopy(record.log_id!, 'log_id')}
                      style={{
                        border: 'none',
                        background: 'transparent',
                        padding: 2,
                        cursor: 'pointer',
                        color: colors.textMuted,
                        display: 'inline-flex',
                        alignItems: 'center',
                      }}
                      title={t('logs.copy', '复制')}
                    >
                      {copiedKey === 'log_id' ? <Check size={12} style={{ color: colors.textPrimary }} /> : <Copy size={12} />}
                    </button>
                  </div>
                </div>
              )}

              {/* 生成耗时 */}
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '6px 0', borderBottom: `1px solid ${colors.borderSubtle}` }}>
                <span style={{ color: colors.textSecondary }}>{t('task_logs.time_spent', '生成耗时')}</span>
                <span style={{ fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace', color: colors.textPrimary, fontWeight: 500 }}>
                  {record?.latency_ms ? `${(record.latency_ms / 1000).toFixed(1)}s` : '-'}
                </span>
              </div>

              {/* 扣费消耗 */}
              {record?.cost !== undefined && record.cost > 0 && (
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '6px 0', borderBottom: `1px solid ${colors.borderSubtle}` }}>
                  <span style={{ color: colors.textSecondary, display: 'flex', alignItems: 'center', gap: 4 }}>
                    <Coins size={12} />
                    {t('task_logs.cost', '消耗费用')}
                  </span>
                  <span style={{ color: colors.textPrimary, fontWeight: 600, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace' }}>
                    {currencySymbol}{Number(Number(record.cost).toFixed(6))}
                  </span>
                </div>
              )}

              {/* 提交时间 */}
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '6px 0' }}>
                <span style={{ color: colors.textSecondary }}>{t('task_logs.submit_time', '提交时间')}</span>
                <span style={{ color: colors.textPrimary, fontSize: 11, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace' }}>
                  {record?.created_at ? formatApiDateTime(record.created_at) : '-'}
                </span>
              </div>
            </div>
          </div>

          {/* 3. 提示词与生成参数 (Prompt & Parameters) */}
          {(parsedParams.prompt || parsedParams.extraParams.length > 0) && (
            <div>
              <div
                style={{
                  fontSize: 11,
                  fontWeight: 600,
                  color: colors.textSecondary,
                  textTransform: 'uppercase',
                  letterSpacing: '0.06em',
                  marginBottom: 10,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                  <Sparkles size={13} />
                  <span>{t('task_logs.prompt_params', '提示词与参数')}</span>
                </div>
                {parsedParams.prompt && (
                  <button
                    onClick={() => handleCopy(parsedParams.prompt, 'prompt')}
                    style={{
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: 4,
                      fontSize: 11,
                      border: 'none',
                      background: 'transparent',
                      color: colors.textSecondary,
                      cursor: 'pointer',
                      padding: '2px 4px',
                      borderRadius: 4,
                    }}
                    onMouseEnter={(e) => (e.currentTarget.style.color = colors.textPrimary)}
                    onMouseLeave={(e) => (e.currentTarget.style.color = colors.textSecondary)}
                  >
                    {copiedKey === 'prompt' ? <Check size={11} style={{ color: colors.textPrimary }} /> : <Copy size={11} />}
                    <span>{copiedKey === 'prompt' ? t('common.copied', '已复制') : t('task_logs.copy_prompt', '复制提示词')}</span>
                  </button>
                )}
              </div>

              {/* 正向提示词 */}
              {parsedParams.prompt && (
                <div
                  style={{
                    background: colors.cardBg,
                    border: `1px solid ${colors.border}`,
                    borderRadius: 8,
                    padding: '10px 12px',
                    fontSize: 12,
                    lineHeight: 1.6,
                    color: colors.textPrimary,
                    maxHeight: 130,
                    overflowY: 'auto',
                    whiteSpace: 'pre-wrap',
                    wordBreak: 'break-all',
                    marginBottom: 10,
                    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
                  }}
                >
                  {parsedParams.prompt}
                </div>
              )}

              {/* 反向提示词 */}
              {parsedParams.negativePrompt && (
                <div style={{ marginBottom: 10 }}>
                  <div style={{ fontSize: 11, color: colors.textMuted, marginBottom: 4 }}>
                    {t('task_logs.negative_prompt', '反向提示词 (Negative Prompt)')}
                  </div>
                  <div
                    style={{
                      background: colors.cardBg,
                      border: `1px solid ${colors.border}`,
                      borderRadius: 8,
                      padding: '8px 10px',
                      fontSize: 11,
                      lineHeight: 1.5,
                      color: colors.textSecondary,
                      maxHeight: 80,
                      overflowY: 'auto',
                      whiteSpace: 'pre-wrap',
                      wordBreak: 'break-all',
                      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
                    }}
                  >
                    {parsedParams.negativePrompt}
                  </div>
                </div>
              )}

              {/* 关键参数徽章 (shadcn 纯黑白灰徽章流) */}
              {parsedParams.extraParams.length > 0 && (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                  {parsedParams.extraParams.map((p, idx) => (
                    <div
                      key={idx}
                      style={{
                        display: 'inline-flex',
                        alignItems: 'center',
                        gap: 5,
                        background: colors.subtleBg,
                        border: `1px solid ${colors.border}`,
                        borderRadius: 6,
                        padding: '3px 8px',
                        fontSize: 11,
                      }}
                    >
                      <span style={{ color: colors.textSecondary }}>{p.label}:</span>
                      <span style={{ fontWeight: 600, color: colors.textPrimary, fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace' }}>
                        {p.value}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
    </Modal>

    {/* 沉浸式全屏放大视图 (支持真全屏 API 与平滑 Portal 降级) */}
    {isFullscreen &&
      createPortal(
        <div
          ref={fullscreenContainerRef}
          className="task-preview-fullscreen-container"
          style={{
            position: 'fixed',
            top: 0,
            left: 0,
            right: 0,
            bottom: 0,
            width: '100vw',
            height: '100vh',
            zIndex: 100000,
            backgroundColor: '#050506',
            backdropFilter: 'blur(20px)',
            display: 'flex',
            flexDirection: 'column',
            justifyContent: 'center',
            alignItems: 'center',
            overflow: 'hidden',
          }}
        >
          {/* 顶部悬浮控制栏 (shadcn 纯黑白灰玻璃胶囊) */}
          <div
            style={{
              position: 'absolute',
              top: isMobile ? 10 : 16,
              left: isMobile ? 10 : 20,
              right: isMobile ? 10 : 20,
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              zIndex: 120,
              pointerEvents: 'none',
              gap: 8,
            }}
          >
            {/* 左侧：类型标识与序号 */}
            <div
              style={{
                pointerEvents: 'auto',
                display: 'flex',
                alignItems: 'center',
                gap: 8,
                padding: '5px 12px',
                borderRadius: 8,
                background: 'rgba(24, 24, 27, 0.8)',
                backdropFilter: 'blur(12px)',
                border: '1px solid rgba(255, 255, 255, 0.12)',
                color: '#fafafa',
                boxShadow: '0 4px 16px rgba(0,0,0,0.5)',
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, fontWeight: 600 }}>
                {type === '图片' ? <ImageIcon size={15} /> : <Video size={15} />}
                <span>{type === '图片' ? t('task_logs.image', '图片') : t('task_logs.video', '视频')}</span>
              </div>
              {urls.length > 1 && (
                <span style={{ fontSize: 12, color: '#a1a1aa', fontFamily: 'monospace' }}>
                  {currentIndex + 1} / {urls.length}
                </span>
              )}
              {record?.model && !isMobile && (
                <span
                  style={{
                    fontSize: 11,
                    padding: '1px 6px',
                    borderRadius: 4,
                    background: 'rgba(255, 255, 255, 0.1)',
                    color: '#e4e4e7',
                    fontFamily: 'monospace',
                  }}
                >
                  {record.model}
                </span>
              )}
            </div>

            {/* 中间：图片缩放控制工具栏 (仅图片显示) */}
            {type === '图片' && (
              <div
                style={{
                  pointerEvents: 'auto',
                  display: 'flex',
                  alignItems: 'center',
                  gap: 4,
                  padding: '4px 8px',
                  borderRadius: 8,
                  background: 'rgba(24, 24, 27, 0.8)',
                  backdropFilter: 'blur(12px)',
                  border: '1px solid rgba(255, 255, 255, 0.12)',
                  boxShadow: '0 4px 16px rgba(0,0,0,0.5)',
                }}
              >
                <Tooltip title={t('task_logs.zoom_out', '缩小 (-)')}>
                  <button
                    onClick={() =>
                      setZoomScale((prev) => {
                        const next = Math.max(0.25, Number((prev - 0.25).toFixed(2)));
                        if (next <= 1) setPan({ x: 0, y: 0 });
                        return next;
                      })
                    }
                    style={{
                      border: 'none',
                      background: 'transparent',
                      color: '#fafafa',
                      cursor: 'pointer',
                      padding: '4px 6px',
                      borderRadius: 4,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                    }}
                    onMouseEnter={(e) => (e.currentTarget.style.background = 'rgba(255, 255, 255, 0.12)')}
                    onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                  >
                    <ZoomOut size={15} />
                  </button>
                </Tooltip>

                <Tooltip title={t('task_logs.reset_zoom', '点击重置为 100% (0)')}>
                  <button
                    onClick={resetTransform}
                    style={{
                      border: 'none',
                      background: 'transparent',
                      color: '#fafafa',
                      cursor: 'pointer',
                      padding: '3px 8px',
                      borderRadius: 4,
                      fontSize: 12,
                      fontWeight: 500,
                      fontFamily: 'monospace',
                      minWidth: 46,
                      textAlign: 'center',
                    }}
                    onMouseEnter={(e) => (e.currentTarget.style.background = 'rgba(255, 255, 255, 0.12)')}
                    onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                  >
                    {Math.round(zoomScale * 100)}%
                  </button>
                </Tooltip>

                <Tooltip title={t('task_logs.zoom_in', '放大 (+)')}>
                  <button
                    onClick={() => setZoomScale((prev) => Math.min(5, Number((prev + 0.25).toFixed(2))))}
                    style={{
                      border: 'none',
                      background: 'transparent',
                      color: '#fafafa',
                      cursor: 'pointer',
                      padding: '4px 6px',
                      borderRadius: 4,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                    }}
                    onMouseEnter={(e) => (e.currentTarget.style.background = 'rgba(255, 255, 255, 0.12)')}
                    onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                  >
                    <ZoomIn size={15} />
                  </button>
                </Tooltip>

                <div style={{ width: 1, height: 16, background: 'rgba(255, 255, 255, 0.15)', margin: '0 2px' }} />

                <Tooltip title={t('task_logs.mirror_image', '镜像图片')}>
                  <button
                    onClick={() => setIsFlipped((prev) => !prev)}
                    style={{
                      border: 'none',
                      background: isFlipped ? 'rgba(255, 255, 255, 0.16)' : 'transparent',
                      color: '#fafafa',
                      cursor: 'pointer',
                      padding: '4px 6px',
                      borderRadius: 4,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                    }}
                    onMouseEnter={(e) => (e.currentTarget.style.background = 'rgba(255, 255, 255, 0.16)')}
                    onMouseLeave={(e) => (e.currentTarget.style.background = isFlipped ? 'rgba(255, 255, 255, 0.16)' : 'transparent')}
                  >
                    <FlipHorizontal2 size={15} />
                  </button>
                </Tooltip>

                <Tooltip title={t('task_logs.rotate_cw', '顺时针旋转 90° (R)')}>
                  <button
                    onClick={() => setRotation((prev) => prev + 90)}
                    style={{
                      border: 'none',
                      background: 'transparent',
                      color: '#fafafa',
                      cursor: 'pointer',
                      padding: '4px 6px',
                      borderRadius: 4,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                    }}
                    onMouseEnter={(e) => (e.currentTarget.style.background = 'rgba(255, 255, 255, 0.12)')}
                    onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                  >
                    <RotateCw size={15} />
                  </button>
                </Tooltip>

                <Tooltip title={t('task_logs.reset_all', '复位原始画面')}>
                  <button
                    onClick={resetTransform}
                    style={{
                      border: 'none',
                      background: 'transparent',
                      color: '#fafafa',
                      cursor: 'pointer',
                      padding: '4px 6px',
                      borderRadius: 4,
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'center',
                    }}
                    onMouseEnter={(e) => (e.currentTarget.style.background = 'rgba(255, 255, 255, 0.12)')}
                    onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                  >
                    <RotateCcw size={15} />
                  </button>
                </Tooltip>
              </div>
            )}

            {/* 右侧：下载与退出全屏操作 */}
            <div
              style={{
                pointerEvents: 'auto',
                display: 'flex',
                alignItems: 'center',
                gap: 6,
                padding: '4px 6px',
                borderRadius: 8,
                background: 'rgba(24, 24, 27, 0.8)',
                backdropFilter: 'blur(12px)',
                border: '1px solid rgba(255, 255, 255, 0.12)',
                boxShadow: '0 4px 16px rgba(0,0,0,0.5)',
              }}
            >
              <Tooltip title={t('task_logs.copy_url', '复制媒体链接')}>
                <button
                  onClick={() => handleCopy(currentUrl, 'fs_url')}
                  style={{
                    border: 'none',
                    background: 'transparent',
                    color: '#fafafa',
                    cursor: 'pointer',
                    padding: '5px 8px',
                    borderRadius: 6,
                    display: 'flex',
                    alignItems: 'center',
                    gap: 4,
                    fontSize: 12,
                  }}
                  onMouseEnter={(e) => (e.currentTarget.style.background = 'rgba(255, 255, 255, 0.12)')}
                  onMouseLeave={(e) => (e.currentTarget.style.background = 'transparent')}
                >
                  {copiedKey === 'fs_url' ? <Check size={14} /> : <Copy size={14} />}
                  {!isMobile && <span>{copiedKey === 'fs_url' ? t('common.copied', '已复制') : t('task_logs.copy_link', '复制链接')}</span>}
                </button>
              </Tooltip>

              <Tooltip
                title={
                  downloadState === 'downloading'
                    ? t('task_logs.downloading_tip', '正在下载媒体文件，请稍候...')
                    : downloadState === 'success'
                      ? t('task_logs.download_started', '已开始下载')
                      : t('task_logs.download', '下载媒体')
                }
              >
                <button
                  disabled={downloadState === 'downloading'}
                  onClick={() => handleDownload(currentUrl)}
                  style={{
                    border: 'none',
                    background: downloadState === 'downloading' ? 'rgba(255, 255, 255, 0.12)' : 'transparent',
                    color: '#fafafa',
                    cursor: downloadState === 'downloading' ? 'wait' : 'pointer',
                    opacity: downloadState === 'downloading' ? 0.85 : 1,
                    padding: '5px 8px',
                    borderRadius: 6,
                    display: 'flex',
                    alignItems: 'center',
                    gap: 4,
                    fontSize: 12,
                  }}
                  onMouseEnter={(e) => {
                    if (downloadState !== 'downloading') e.currentTarget.style.background = 'rgba(255, 255, 255, 0.12)';
                  }}
                  onMouseLeave={(e) => {
                    if (downloadState !== 'downloading') e.currentTarget.style.background = 'transparent';
                  }}
                >
                  {downloadState === 'downloading' ? (
                    <Loader2 size={14} className="animate-spin" />
                  ) : downloadState === 'success' ? (
                    <Check size={14} />
                  ) : (
                    <Download size={14} />
                  )}
                  {!isMobile && (
                    <span>
                      {downloadState === 'downloading'
                        ? downloadProgress !== null
                          ? `${t('task_logs.downloading_btn', '下载中')} ${downloadProgress}%`
                          : t('task_logs.downloading_btn', '下载中...')
                        : downloadState === 'success'
                          ? t('task_logs.downloaded_btn', '已下载')
                          : t('task_logs.download_btn', '下载')}
                    </span>
                  )}
                </button>
              </Tooltip>

              <button
                onClick={toggleFullscreen}
                style={{
                  border: '1px solid rgba(255, 255, 255, 0.2)',
                  background: 'rgba(255, 255, 255, 0.1)',
                  color: '#ffffff',
                  cursor: 'pointer',
                  padding: '5px 10px',
                  borderRadius: 6,
                  display: 'flex',
                  alignItems: 'center',
                  gap: 5,
                  fontSize: 12,
                  fontWeight: 500,
                  transition: 'all 0.15s ease',
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.background = 'rgba(255, 255, 255, 0.25)';
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.background = 'rgba(255, 255, 255, 0.1)';
                }}
                title={t('task_logs.exit_fullscreen_hint', '退出全屏 (Esc)')}
              >
                <Minimize2 size={14} />
                <span>{t('task_logs.exit_fullscreen', '退出全屏')}</span>
              </button>
            </div>
          </div>

          {/* 中心媒体主视口 */}
          <div
            style={{
              width: '100%',
              height: '100%',
              display: 'flex',
              justifyContent: 'center',
              alignItems: 'center',
              position: 'relative',
              overflow: 'hidden',
            }}
          >
            {type === '图片' ? (
              <div
                ref={fullscreenImageRef}
                onMouseDown={handleMouseDown}
                onMouseMove={handleMouseMove}
                onMouseUp={handleMouseUp}
                onMouseLeave={handleMouseUp}
                onDoubleClick={handleDoubleClick}
                style={{
                  width: '100%',
                  height: '100%',
                  display: 'flex',
                  justifyContent: 'center',
                  alignItems: 'center',
                  cursor: zoomScale > 1 ? (isDragging ? 'grabbing' : 'grab') : 'zoom-in',
                  userSelect: 'none',
                  touchAction: 'none',
                }}
              >
                <img
                  src={currentUrl}
                  alt="fullscreen-preview"
                  draggable={false}
                  style={{
                    maxWidth: '96vw',
                    maxHeight: '92vh',
                    objectFit: 'contain',
                    borderRadius: 0,
                    transform: `translate(${pan.x}px, ${pan.y}px) scale(${Number((zoomScale * getRotatedFitScale(window.innerWidth * 0.96, window.innerHeight * 0.92)).toFixed(4))}) rotate(${rotation}deg) scaleX(${isFlipped ? -1 : 1})`,
                    transition: isDragging ? 'none' : 'transform 0.15s cubic-bezier(0.2, 0, 0.2, 1)',
                    pointerEvents: 'auto',
                  }}
                />
              </div>
            ) : (
              <div
                style={{
                  width: '100%',
                  height: '100%',
                  display: 'flex',
                  justifyContent: 'center',
                  alignItems: 'center',
                  padding: isMobile ? 8 : 32,
                }}
              >
                <ArtPlayerPreview
                  url={currentUrl}
                  autoplay
                  muted
                  locale={i18n.language}
                  theme={isLight ? '#18181b' : '#e4e4e7'}
                  onReady={restoreActiveVideoState}
                  onLoadedMetadata={handleArtVideoMeta}
                />
              </div>
            )}
          </div>

          {/* 左右切页浮动按钮 (多图/多视频时) */}
          {urls.length > 1 && (
            <>
              <button
                disabled={currentIndex === 0}
                onClick={() => setCurrentIndex((prev) => Math.max(0, prev - 1))}
                style={{
                  position: 'absolute',
                  left: isMobile ? 8 : 24,
                  top: '50%',
                  transform: 'translateY(-50%)',
                  zIndex: 120,
                  width: isMobile ? 38 : 46,
                  height: isMobile ? 38 : 46,
                  borderRadius: '50%',
                  background: 'rgba(24, 24, 27, 0.75)',
                  backdropFilter: 'blur(12px)',
                  border: '1px solid rgba(255, 255, 255, 0.15)',
                  color: '#fafafa',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  cursor: currentIndex === 0 ? 'not-allowed' : 'pointer',
                  opacity: currentIndex === 0 ? 0.2 : 0.85,
                  boxShadow: '0 4px 20px rgba(0,0,0,0.5)',
                  transition: 'all 0.15s ease',
                }}
                onMouseEnter={(e) => currentIndex > 0 && (e.currentTarget.style.opacity = '1')}
                onMouseLeave={(e) => currentIndex > 0 && (e.currentTarget.style.opacity = '0.85')}
                title={t('task_logs.prev', '上一个 (←)')}
              >
                <ChevronLeft size={isMobile ? 20 : 24} />
              </button>

              <button
                disabled={currentIndex === urls.length - 1}
                onClick={() => setCurrentIndex((prev) => Math.min(urls.length - 1, prev + 1))}
                style={{
                  position: 'absolute',
                  right: isMobile ? 8 : 24,
                  top: '50%',
                  transform: 'translateY(-50%)',
                  zIndex: 120,
                  width: isMobile ? 38 : 46,
                  height: isMobile ? 38 : 46,
                  borderRadius: '50%',
                  background: 'rgba(24, 24, 27, 0.75)',
                  backdropFilter: 'blur(12px)',
                  border: '1px solid rgba(255, 255, 255, 0.15)',
                  color: '#fafafa',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  cursor: currentIndex === urls.length - 1 ? 'not-allowed' : 'pointer',
                  opacity: currentIndex === urls.length - 1 ? 0.2 : 0.85,
                  boxShadow: '0 4px 20px rgba(0,0,0,0.5)',
                  transition: 'all 0.15s ease',
                }}
                onMouseEnter={(e) => currentIndex < urls.length - 1 && (e.currentTarget.style.opacity = '1')}
                onMouseLeave={(e) => currentIndex < urls.length - 1 && (e.currentTarget.style.opacity = '0.85')}
                title={t('task_logs.next', '下一个 (→)')}
              >
                <ChevronRight size={isMobile ? 20 : 24} />
              </button>

              {/* 底部悬浮缩略图栏 */}
              <div
                style={{
                  position: 'absolute',
                  bottom: isMobile ? 12 : 24,
                  left: '50%',
                  transform: 'translateX(-50%)',
                  zIndex: 120,
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  padding: '6px 14px',
                  background: 'rgba(24, 24, 27, 0.85)',
                  backdropFilter: 'blur(16px)',
                  borderRadius: 9999,
                  border: '1px solid rgba(255, 255, 255, 0.15)',
                  boxShadow: '0 10px 30px rgba(0, 0, 0, 0.6)',
                  maxWidth: '85vw',
                  overflowX: 'auto',
                }}
              >
                {urls.map((u, idx) => {
                  const isCur = idx === currentIndex;
                  return (
                    <div
                      key={idx}
                      onClick={() => setCurrentIndex(idx)}
                      style={{
                        width: 36,
                        height: 36,
                        borderRadius: 8,
                        overflow: 'hidden',
                        cursor: 'pointer',
                        border: isCur ? '2px solid #ffffff' : '1px solid rgba(255, 255, 255, 0.2)',
                        opacity: isCur ? 1 : 0.5,
                        transition: 'all 0.15s ease',
                        flexShrink: 0,
                        background: '#18181b',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                      }}
                    >
                      {type === '图片' ? (
                        <img src={u} alt="" style={{ width: '100%', height: '100%', objectFit: 'cover' }} />
                      ) : (
                        <span style={{ fontSize: 11, fontWeight: 600, color: '#ffffff' }}>#{idx + 1}</span>
                      )}
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </div>,
        document.body
      )}
  </>
);
};

export default MediaPreviewModal;

