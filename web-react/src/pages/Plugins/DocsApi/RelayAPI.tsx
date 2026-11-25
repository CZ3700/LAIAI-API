import React, { useState, useEffect, useMemo } from 'react';
import { getAnnouncementLabel, getAnnouncementDisplayTime } from '../../../utils/announcement';
import { applyDocsContentVars, buildDocHref, formatPresetDocTitle, formatPresetCategoryName } from '../../../utils/docsContentVars';
import {
  parseNotificationPreferences,
  shouldShowWebNotifications,
  maybeShowBrowserPush,
} from '../../../utils/notificationPrefs';
import { useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import {
  Popover, List, Badge, message, Spin, Empty, Dropdown, Button, Tooltip, Drawer,
  Layout, Grid
} from 'antd';
const { Header, Sider, Content } = Layout;
const { useBreakpoint } = Grid;
import {
  Sidebar as SidebarIcon, Bell, Folder, FolderOpen,
  FileText, ChevronRight, Search, ArrowLeft, Copy, Check, ExternalLink,
  Terminal, Rocket, BookOpen, Settings, Code, Sparkles, AlertTriangle,
  XCircle, CheckCircle, ChevronDown, Compass, FileCode, CheckCircle2,
  GalleryVerticalEnd, ClipboardList, Palette
} from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import rehypeHighlight from 'rehype-highlight';
import remarkGfm from 'remark-gfm';
import { RocketOutlined } from '@ant-design/icons';
import 'highlight.js/styles/github-dark.css';
import './DocsApi.css';

import request from '../../../utils/request';
import { fetchActivePlugins, peekActivePlugins } from '../../../utils/activePlugins';
import { persistUserLanguagePreference, LANG_NAME_MAP } from '../../../utils/language';
import { useThemeStore } from '../../../store/theme';
import useSettingsStore from '../../../store/settings';
import useAuthStore from '../../../store/auth';
import UserAvatarMenu from '../../../components/UserAvatarMenu';
import { formatApiDateTime } from '../../../utils/timedisplay';
import { copyToClipboard } from '../../../utils/clipboard';

interface Announcement {
  id: number;
  title: string;
  content: string;
  is_pinned: number;
  sort_order?: number;
  created_at: string;
  updated_at?: string;
  display_time_mode?: string;
}

interface DocTreeNode {
  id: number;
  parent_id: number | null;
  title: string;
  is_dir: boolean;
  sort_order: number;
  is_active: boolean;
  slug?: string;
  category_id?: number | null;
  default_expanded?: boolean;
  children: DocTreeNode[];
}

function treeHasExpandFlag(nodes: DocTreeNode[]): boolean {
  for (const node of nodes) {
    if (typeof node.default_expanded === 'boolean') return true;
    if (node.children?.length && treeHasExpandFlag(node.children)) return true;
  }
  return false;
}

function isQuickstartDir(node: DocTreeNode): boolean {
  const slug = (node.slug || '').toLowerCase();
  if (slug === 'quickstart') return true;
  const title = node.title.toLowerCase();
  return title.includes('快速开始') || title.includes('quick start') || title.includes('quickstart');
}

/** 用户端侧栏默认展开的目录。有配置时按配置；旧接口没有该字段时只展开快速开始。 */
function sidebarDefaultOpenKeys(nodes: DocTreeNode[]): string[] {
  const keys: string[] = [];
  const useFlag = treeHasExpandFlag(nodes);
  const walk = (list: DocTreeNode[], ancestors: string[]) => {
    for (const node of list) {
      if (!node.is_dir) continue;
      const key = `dir-${node.id}`;
      const chain = [...ancestors, key];
      const open = useFlag ? !!node.default_expanded : isQuickstartDir(node);
      if (open) keys.push(...chain);
      if (node.children?.length) walk(node.children, chain);
    }
  };
  walk(nodes, []);
  return Array.from(new Set(keys));
}

// ----------------------------------------------------
// 递归提取 React 节点纯文本，避免代码块高亮标签被 String() 强转导致 [object Object]
const extractText = (node: any): string => {
  if (!node) return '';
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(extractText).join('');
  if (node.props?.children) return extractText(node.props.children);
  return '';
};

// ----------------------------------------------------
// 辅助子组件：复制按钮代码块
// ----------------------------------------------------
const CodeBlock: React.FC<{ language: string; value: string; children: React.ReactNode }> = ({ language, value, children }) => {
  const { t: docsT } = useTranslation('docs_api');
  const [copied, setCopied] = useState(false);

  const handleCopy = async () => {
    const ok = await copyToClipboard(value);
    if (ok) {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } else {
      message.error(docsT('msg_copy_failed', '复制失败'));
    }
  };

  return (
    <div className="relative border border-slate-700/60 dark:border-zinc-800 rounded-xl overflow-hidden my-6 bg-[#0d1117] text-[#e6edf3] shadow-md">
      <div className="flex items-center justify-between px-4 py-2 border-b border-slate-800 bg-[#161b22] text-[11px] font-mono text-slate-400 select-none">
        <span className="uppercase font-semibold tracking-wider text-zinc-400">{language}</span>
        <button
          onClick={handleCopy}
          className="flex items-center gap-1.5 px-2 py-1 rounded hover:bg-slate-800 text-slate-300 hover:text-white transition-colors cursor-pointer text-xs"
        >
          {copied ? (
            <>
              <CheckCircle className="w-3.5 h-3.5 text-zinc-200" />
              <span className="text-zinc-200">{docsT('copied')}</span>
            </>
          ) : (
            <>
              <Copy className="w-3.5 h-3.5" />
              <span>{docsT('copy')}</span>
            </>
          )}
        </button>
      </div>
      <pre className="p-4 m-0 overflow-x-auto text-xs leading-relaxed font-mono bg-transparent! border-none!">
        <code className={`language-${language} hljs bg-transparent! border-none! p-0!`}>{children}</code>
      </pre>
    </div>
  );
};

// ----------------------------------------------------
// 辅助子组件：自定义标签组件 (Tabs)
// ----------------------------------------------------
const TabsComponent: React.FC<{
  items: { title: string; content: string }[];
  markdownComponents: any;
}> = ({ items, markdownComponents }) => {
  const [activeIdx, setActiveIdx] = useState(0);
  if (items.length === 0) return null;

  return (
    <div className="border border-border rounded-lg overflow-hidden my-6 bg-card/30">
      <div className="flex border-b border-border bg-zinc-50/50 dark:bg-zinc-900/20 px-2 gap-1">
        {items.map((item, idx) => (
          <button
            key={idx}
            onClick={() => setActiveIdx(idx)}
            className={`px-4 py-2.5 text-xs font-medium border-b-2 transition-all cursor-pointer ${
              activeIdx === idx
                ? 'border-zinc-900 dark:border-zinc-100 text-zinc-950 dark:text-zinc-50'
                : 'border-transparent text-zinc-500 hover:text-zinc-800 dark:text-zinc-400 dark:hover:text-zinc-200'
            }`}
          >
            {item.title}
          </button>
        ))}
      </div>
      <div className="p-5">
        <ReactMarkdown components={markdownComponents} remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeHighlight]}>
          {items[activeIdx]?.content || ''}
        </ReactMarkdown>
      </div>
    </div>
  );
};

// ----------------------------------------------------
// 辅助函数：解析多块自定义容器类型 (Cards, Steps, Tabs)
// ----------------------------------------------------
const parseMarkdownBlocks = (text: string) => {
  const sections: { type: string; content: string }[] = [];
  const lines = text.split('\n');
  let currentType = 'markdown';
  let currentContent: string[] = [];

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.trim().startsWith(':::')) {
      if (currentContent.length > 0) {
        sections.push({ type: currentType, content: currentContent.join('\n') });
        currentContent = [];
      }

      const typeMatch = line.match(/^:::\s*(\w+)/);
      if (typeMatch && currentType === 'markdown') {
        currentType = typeMatch[1];
      } else {
        currentType = 'markdown';
      }
    } else {
      currentContent.push(line);
    }
  }

  if (currentContent.length > 0) {
    sections.push({ type: currentType, content: currentContent.join('\n') });
  }

  return sections;
};

// 解析 Cards 内容块中的卡片
const parseCards = (content: string) => {
  const items: { title: string; desc: string; href: string; icon: string }[] = [];
  const cardBlocks = content.split('###').slice(1);

  for (const block of cardBlocks) {
    const lines = block.split('\n');
    const titleLine = lines[0].trim();
    let desc = '';
    let href = '';
    let icon = '';

    const linkMatch = block.match(/\[.*?\]\((.*?)\)/);
    if (linkMatch) {
      href = linkMatch[1];
    }

    const cleanBlock = block.replace(/\[.*?\]\(.*?\)/g, '');
    const descLines = cleanBlock.split('\n').slice(1).map(l => l.trim()).filter(l => l.length > 0);
    desc = descLines.join(' ');

    const iconMatch = titleLine.match(/\{icon:\s*(\w+)\}/);
    let title = titleLine;
    if (iconMatch) {
      icon = iconMatch[1];
      title = titleLine.replace(/\{icon:\s*(\w+)\}/, '').trim();
    }

    items.push({ title, desc, href, icon });
  }

  return items;
};

// 解析 Tabs 中的子选项卡
const parseTabs = (content: string) => {
  const tabs: { title: string; content: string }[] = [];
  const tabBlocks = content.split('===').slice(1);

  for (const block of tabBlocks) {
    const lines = block.split('\n');
    const title = lines[0].trim();
    const tabContent = lines.slice(1).join('\n').trim();
    tabs.push({ title, content: tabContent });
  }

  return tabs;
};

const renderCardIcon = (iconName: string) => {
  const name = iconName.toLowerCase();
  if (name === 'rocket' || name === 'quickstart') return <Rocket className="w-4 h-4 text-blue-500" />;
  if (name === 'api' || name === 'code') return <Code className="w-4 h-4 text-purple-500" />;
  if (name === 'settings' || name === 'config') return <Settings className="w-4 h-4 text-zinc-500" />;
  if (name === 'book' || name === 'guide') return <BookOpen className="w-4 h-4 text-zinc-500" />;
  if (name === 'terminal' || name === 'cli') return <Terminal className="w-4 h-4 text-amber-500" />;
  return <FileText className="w-4 h-4 text-zinc-400" />;
};

// ----------------------------------------------------
// 主组件 RelayAPI
// ----------------------------------------------------
/** API 教程（docs_api）与高级门户文档（site_portal_pro）数据源隔离，互不抢读 */
const DOCS_API_PREFIX = '/plugins/docs-api';
const SITE_PORTAL_PRO_PREFIX = '/plugins/site-portal-pro';

export interface RelayAPIProps {
  /** 未传时固定走 docs_api 的 /docs；门户文档走 /home-pro/devdocs */
  apiPrefix?: string;
  baseRoute?: string;
}

const RelayAPI: React.FC<RelayAPIProps> = ({ apiPrefix, baseRoute }) => {
  const resolvedApiPrefix = apiPrefix ?? DOCS_API_PREFIX;
  const isSitePortalPro = resolvedApiPrefix === SITE_PORTAL_PRO_PREFIX;
  const navigate = useNavigate();
  const { id } = useParams<{ id?: string }>();
  const { themeMode, toggleTheme } = useThemeStore();
  const { settings } = useSettingsStore();
  const { user } = useAuthStore();
  const { t: _t, i18n } = useTranslation();
  const { t: docsT } = useTranslation('docs_api');

  const screens = useBreakpoint();
  const [collapsed, setCollapsed] = useState(() => {
    try {
      if (typeof window !== 'undefined' && window.innerWidth <= 576) {
        return true;
      }
      const saved = localStorage.getItem('docs_api_sidebar_collapsed');
      return saved !== null ? JSON.parse(saved) : false;
    } catch {
      return false;
    }
  });
  const [openOutlineDrawer, setOpenOutlineDrawer] = useState(false);
  const [announcementsDrawerVisible, setAnnouncementsDrawerVisible] = useState(false);
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [unreadCount, setUnreadCount] = useState(0);
  const [activePlugins, setActivePlugins] = useState<any[]>(() => peekActivePlugins() ?? []);
  const [pluginsReady, setPluginsReady] = useState<boolean>(() => peekActivePlugins() !== null);

  useEffect(() => {
    if (screens.xs) {
      setCollapsed(true);
    }
  }, [screens.xs]);

  const handleCollapsedChange = (val: boolean) => {
    setCollapsed(val);
    localStorage.setItem('docs_api_sidebar_collapsed', JSON.stringify(val));
  };

  // 动态文档状态
  const [treeData, setTreeData] = useState<DocTreeNode[]>([]);
  const [loading, setLoading] = useState(true);
  const [pluginEnabled, setPluginEnabled] = useState(true);
  const [forbidden, setForbidden] = useState(false);
  const [docDetail, setDocDetail] = useState<any>(null);
  const [docCopied, setDocCopied] = useState(false);
  const [detailLoading, setDetailLoading] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [expandedMenuKeys, setExpandedMenuKeys] = useState<string[]>([]);
  const [activeAnchor, setActiveAnchor] = useState<string>('');

  // 分类状态
  const [categories, setCategories] = useState<any[]>([]);
  const [activeCategoryId, setActiveCategoryId] = useState<number | null>(null);

  // 查找某个文档 ID 的父级 slug
  const findParentSlug = (nodes: DocTreeNode[], targetId: number): string | null => {
    for (const node of nodes) {
      if (node.children && node.children.length > 0) {
        if (node.children.some(child => child.id === targetId)) {
          return node.slug || null;
        }
        const found = findParentSlug(node.children, targetId);
        if (found !== null) {
          return found;
        }
      }
    }
    return null;
  };

  // 转换数据库 ID 为 URL 别名 (例如 15 -> doc0015)
  const idToSlug = (docId: number): string => {
    return `doc${String(docId).padStart(4, '0')}`;
  };

  // 从 URL 别名或原始数字中提取数据库 ID
  const slugToId = (slug?: string): number | null => {
    if (!slug) return null;
    const match = slug.match(/^doc(\d{4,})$/);
    if (match) {
      return parseInt(match[1], 10);
    }
    const num = parseInt(slug, 10);
    return isNaN(num) ? null : num;
  };

  const selectedDocId = useMemo(() => {
    return slugToId(id);
  }, [id]);

  const basePath = useMemo(() => {
    if (baseRoute) return baseRoute;
    const path = window.location.pathname;
    if (path.includes('/docs')) {
      const idx = path.indexOf('/docs');
      return path.substring(0, idx) + '/docs';
    }
    return '/docs';
  }, [baseRoute]);

  const isEn = i18n.language === 'en';
  const enableThemeToggle = settings?.site?.enable_theme_toggle !== false;
  const enableMultilingual = settings?.site?.enable_multilingual !== false;
  const siteName = settings?.site?.name || 'laiai';
  const siteLogo = settings?.site?.logo || '';
  const siteTitle = settings?.site?.title || '';
  const agreement = settings?.agreement || null;

  const isLight = themeMode === 'light';
  const c = {
    siderBg: isLight ? '#ffffff' : '#141414',
    cardBorder: isLight ? '#eaeaea' : '#222225',
    text1: isLight ? '#1f2937' : 'rgba(255,255,255,0.95)',
    text2: isLight ? '#4b5563' : 'rgba(255,255,255,0.75)',
    text3: isLight ? '#6b7280' : 'rgba(255,255,255,0.5)',
    scrollThumb: isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.06)',
  };

  // 拉取公告 + 已启用插件（对齐控制台右上角）
  useEffect(() => {
    const fetchAnnouncements = async () => {
      const prefs = parseNotificationPreferences(
        user?.notification_preferences,
        settings?.notification?.low_balance_threshold ?? 100.0,
      );
      if (!shouldShowWebNotifications(prefs, settings?.notification)) {
        setAnnouncements([]);
        setUnreadCount(0);
        return;
      }
      try {
        const response = await (request.get('/announcements/public') as any);
        if (response.data) {
          setAnnouncements(response.data);
          setUnreadCount(response.data.length);
          if (response.data.length > 0) {
            const first = response.data[0];
            const seenKey = `notif_push_seen_${first.id}`;
            if (!sessionStorage.getItem(seenKey)) {
              sessionStorage.setItem(seenKey, '1');
              const title = getAnnouncementLabel(first.title || '') || _t('header.notifications', 'Notification');
              const body = getAnnouncementLabel(first.content || '').replace(/<[^>]+>/g, '').slice(0, 120);
              maybeShowBrowserPush(title, body, prefs, settings?.notification);
            }
          }
        }
      } catch (error) {
        console.error('Failed to fetch announcements:', error);
      }
    };
    const loadActivePlugins = async () => {
      try {
        const response = await fetchActivePlugins();
        if (response.active_plugins) {
          setActivePlugins(response.active_plugins);
        }
        setPluginsReady(true);
      } catch (error) {
        console.error('Failed to fetch active plugins:', error);
        setPluginsReady(true);
      }
    };
    fetchAnnouncements();
    loadActivePlugins();
  }, [user?.notification_preferences, settings?.notification?.low_balance_threshold, i18n.language]);

  const isPluginVisibleForUser = (pluginName: string) => {
    const plugin = activePlugins.find((p: any) => p.name === pluginName);
    if (!plugin) return false;
    if (pluginName === 'marketing_membership' && user?.role === 'admin') return true;
    if (!user) return !!plugin.mp_allow_guest;
    if (user?.role === 'admin' || plugin.allowed_levels === 'all') return true;
    const allowed = String(plugin.allowed_levels || '').split(',');
    const userGroup = user?.user_group || '';
    const levelId = user?.level_id != null ? String(user.level_id) : '';
    return allowed.includes(userGroup) || (levelId !== '' && allowed.includes(levelId));
  };

  const headerNavReady = pluginsReady;

  const cleanTitle = (title: string, slug?: string): string =>
    formatPresetDocTitle(title, slug, docsT);

  const currentDocTitle = useMemo(() => {
    if (docDetail?.title) {
      return cleanTitle(docDetail.title, docDetail.slug);
    }
    if (selectedDocId && treeData.length > 0) {
      const findNode = (nodes: DocTreeNode[]): DocTreeNode | null => {
        for (const n of nodes) {
          if (n.id === selectedDocId) return n;
          if (n.children && n.children.length > 0) {
            const found = findNode(n.children);
            if (found) return found;
          }
        }
        return null;
      };
      const node = findNode(treeData);
      if (node) return cleanTitle(node.title, node.slug);
    }
    return '';
  }, [docDetail?.title, docDetail?.slug, selectedDocId, treeData, i18n.language]);

  useEffect(() => {
    const docTitleLabel = docsT('client_doc_title', 'API 文档');
    const displaySiteName = settings?.site?.name || settings?.site?.title || 'laiai';
    if (currentDocTitle) {
      document.title = `${currentDocTitle}-${docTitleLabel}-${displaySiteName}`;
    } else {
      document.title = `${docTitleLabel}-${displaySiteName}`;
    }
  }, [currentDocTitle, i18n.language, settings?.site?.name, settings?.site?.title]);

  const findFirstArticle = (nodes: DocTreeNode[]): DocTreeNode | null => {
    for (const node of nodes) {
      if (!node.is_dir) {
        return node;
      }
      if (node.children && node.children.length > 0) {
        const found = findFirstArticle(node.children);
        if (found) return found;
      }
    }
    return null;
  };

  const isDocInTree = (nodes: DocTreeNode[], targetId: number): boolean => {
    for (const node of nodes) {
      if (!node.is_dir && node.id === targetId) return true;
      if (node.children && node.children.length > 0 && isDocInTree(node.children, targetId)) {
        return true;
      }
    }
    return false;
  };

  const cleanTreeTitles = (nodes: DocTreeNode[]): DocTreeNode[] => {
    return nodes.map(node => ({
      ...node,
      title: cleanTitle(node.title, node.slug),
      children: node.children ? cleanTreeTitles(node.children) : []
    }));
  };

  // 拉取文档树（按路由绑定的 apiPrefix，不再跨插件回退）
  useEffect(() => {
    const fetchDocTree = async () => {
      try {
        const res: any = await request.get(
          `${resolvedApiPrefix}/public/tree?lang=${i18n.language}`,
        );
        if (res && res.tree) {
          const cleanedTree = cleanTreeTitles(res.tree);
          setTreeData(cleanedTree);
          setExpandedMenuKeys(sidebarDefaultOpenKeys(cleanedTree));
        }

        if (isSitePortalPro) {
          try {
            const catRes = await request.get(`${resolvedApiPrefix}/public/docs/categories`, {
              skipErrorHandler: true,
            } as any) as any;
            if (catRes && catRes.categories) {
              // 同名分类只保留 id 最小的一条，避免「API 参考」重复展示
              const seen = new Set<string>();
              const cats = [...catRes.categories]
                .sort((a: any, b: any) => a.id - b.id)
                .filter((c: any) => {
                  if (seen.has(c.name)) return false;
                  seen.add(c.name);
                  return true;
                })
                .sort((a: any, b: any) => {
                  const aDef = a.is_default === 1 ? 1 : 0;
                  const bDef = b.is_default === 1 ? 1 : 0;
                  if (aDef !== bDef) return bDef - aDef;
                  return a.sort_order - b.sort_order || a.id - b.id;
                });
              setCategories(cats);
              if (cats.length > 0) {
                const defCat = cats.find((c: any) => c.is_default === 1) || cats[0];
                setActiveCategoryId(defCat.id);
              }
            }
          } catch (catErr) {
            console.warn('加载文档分类失败', catErr);
            setCategories([]);
            setActiveCategoryId(null);
          }
        }
      } catch (error: any) {
        const status = error?.response?.status || error?.status;
        if (status === 403) {
          setForbidden(true);
        } else {
          setPluginEnabled(false);
        }
      } finally {
        setLoading(false);
      }
    };
    fetchDocTree();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [basePath, i18n.language]);


  // 监听选中的文档 ID 并获取内容（仅当文档在当前公开树中可见时拉取）
  useEffect(() => {
    if (loading) return;
    if (selectedDocId && isDocInTree(treeData, selectedDocId)) {
      fetchDocContent(selectedDocId);
    } else {
      setDocDetail(null);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedDocId, i18n.language, loading, treeData]);

  // 查找选中文档的所有级目录 ID
  const findParentDirIds = (nodes: DocTreeNode[], targetId: number, currentParents: string[] = []): string[] => {
    for (const node of nodes) {
      if (node.id === targetId) {
        return currentParents;
      }
      if (node.is_dir && node.children) {
        const found = findParentDirIds(node.children, targetId, [...currentParents, `dir-${node.id}`]);
        if (found.length > 0) {
          return found;
        }
      }
    }
    return [];
  };

  // 当选择文档改变时，自动展开其父级目录
  useEffect(() => {
    if (selectedDocId && treeData.length > 0) {
      const parentIds = findParentDirIds(treeData, selectedDocId);
      if (parentIds.length > 0) {
        setExpandedMenuKeys(prev => {
          const unique = new Set([...prev, ...parentIds]);
          return Array.from(unique);
        });
      }
    }
  }, [selectedDocId, treeData]);

  // 当选中文档改变时，同步分类状态（例如直接访问某文档 URL）
  useEffect(() => {
    if (selectedDocId && treeData.length > 0 && categories.length > 0) {
      // 沿父链向上解析 category_id（子文档通常自身为空，需继承根目录分类）
      const resolveCategoryId = (targetId: number): number | null => {
        const byId = new Map<number, DocTreeNode>();
        const index = (nodes: DocTreeNode[]) => {
          nodes.forEach(n => {
            byId.set(n.id, n);
            if (n.children?.length) index(n.children);
          });
        };
        index(treeData);
        const visited = new Set<number>();
        let current: number | null = targetId;
        while (current != null && !visited.has(current)) {
          visited.add(current);
          const node = byId.get(current);
          if (!node) break;
          if (node.category_id != null && node.category_id !== undefined) {
            return node.category_id;
          }
          current = node.parent_id ?? null;
        }
        const defCat = categories.find(c => c.is_default === 1)
          || categories.find(c => c.name === 'API 参考' || c.name === 'API参考')
          || categories[0];
        return defCat?.id ?? null;
      };
      const catId = resolveCategoryId(selectedDocId);
      if (catId) {
        setActiveCategoryId(catId);
      }
    }
  }, [selectedDocId, treeData, categories]);

  const fetchDocContent = async (id: number) => {
    try {
      setDetailLoading(true);
      const res: any = await request.get(
        `${resolvedApiPrefix}/public/docs/${id}?lang=${i18n.language}`,
      );
      if (res && res.doc) {
        setDocDetail({
          ...res.doc,
          title: cleanTitle(res.doc.title, res.doc.slug)
        });
      }
    } catch (error) {
      message.error(docsT('client_msg_fetch_content_failed'));
    } finally {
      setDetailLoading(false);
    }
  };

  const errorCodesHref = useMemo(
    () => buildDocHref(treeData, 'error-codes', basePath),
    [treeData, basePath],
  );

  const processedContent = useMemo(() => {
    if (!docDetail?.content) return '';
    let content = docDetail.content;
    const domain = window.location.host;
    const protocol = window.location.protocol;
    const baseUrl = `${protocol}//${domain}`;

    content = applyDocsContentVars(content, {
      domain,
      baseUrl,
      error_codes_href: errorCodesHref,
    });

    return content;
  }, [docDetail, errorCodesHref]);

  useEffect(() => {
    setDocCopied(false);
    document.getElementById('docs-main-content')?.scrollTo({ top: 0 });
  }, [selectedDocId]);

  const handleCopyDoc = async () => {
    const rawContent = processedContent || docDetail?.content;
    if (!rawContent) return;
    const ok = await copyToClipboard(rawContent);
    if (ok) {
      setDocCopied(true);
      setTimeout(() => setDocCopied(false), 2000);
    } else {
      message.error(docsT('msg_copy_failed', '复制失败'));
    }
  };

  const changeLanguage = (lng: string) => {
    i18n.changeLanguage(lng);
    persistUserLanguagePreference(lng);
  };

  const supportedLanguages = settings?.site?.supported_languages?.length ? settings.site.supported_languages : ['zh', 'en'];
  const implementedLangs = i18n.options.resources ? Object.keys(i18n.options.resources) : ['zh', 'en'];

  const langItems = supportedLanguages
    .filter(lng => implementedLangs.includes(lng))
    .map(lng => ({
      key: lng,
      label: LANG_NAME_MAP[lng] || lng,
      onClick: () => changeLanguage(lng),
    }));

  const announcementContent = (
    <div style={{ width: 360, display: 'flex', flexDirection: 'column' }}>
      <div style={{
        padding: '16px 20px',
        borderBottom: `1px solid ${c.cardBorder}`,
        display: 'flex', alignItems: 'center', justifyContent: 'space-between'
      }}>
        <span style={{ color: c.text1, fontSize: 16, fontWeight: 500 }}>{_t('header.notifications', '通知')}</span>
      </div>
      <div style={{ maxHeight: 480, overflowY: 'auto', padding: announcements.length > 0 ? '16px' : '60px 20px' }}>
        {announcements.length > 0 ? (
          <List
            itemLayout="vertical"
            dataSource={announcements}
            split={false}
            renderItem={(item) => (
              <div
                style={{
                  background: isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)',
                  borderRadius: 12,
                  padding: 16,
                  marginBottom: 12,
                  transition: 'all 0.3s cubic-bezier(0.4, 0, 0.2, 1)',
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.background = isLight ? '#f3f4f6' : 'rgba(255, 255, 255, 0.08)';
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.background = isLight ? '#f9fafb' : 'rgba(255,255,255,0.04)';
                }}
              >
                <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 12 }}>
                  <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
                    {item.is_pinned === 1 && (
                      <div style={{
                        background: isLight ? 'rgba(24, 24, 27, 0.06)' : 'rgba(250, 250, 250, 0.1)',
                        color: isLight ? '#18181b' : '#fafafa',
                        border: isLight ? '1px solid rgba(24, 24, 27, 0.12)' : '1px solid rgba(250, 250, 250, 0.18)',
                        fontSize: 12,
                        padding: '2px 6px', borderRadius: 4, whiteSpace: 'nowrap', flexShrink: 0
                      }}>
                        {_t('common.pinned', '置顶')}
                      </div>
                    )}
                    <div style={{ color: c.text1, fontSize: 15, fontWeight: 500 }}>{getAnnouncementLabel(item.title)}</div>
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, color: c.text3, fontSize: 12 }}>
                    <Terminal className="w-3.5 h-3.5" />
                    {formatApiDateTime(getAnnouncementDisplayTime(item), 'YYYY-MM-DD HH:mm')}
                  </div>
                </div>
                <div dangerouslySetInnerHTML={{ __html: getAnnouncementLabel(item.content) }} style={{ color: c.text2, fontSize: 13, lineHeight: 1.6 }} />
              </div>
            )}
          />
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
            <Bell className="w-10 h-10 text-zinc-300 dark:text-zinc-700 mb-4" />
            <div style={{ color: c.text1, fontSize: 15, fontWeight: 500, marginBottom: 8 }}>{_t('header.no_notifications', '没有通知')}</div>
          </div>
        )}
      </div>
    </div>
  );

  const [openSearch, setOpenSearch] = useState(false);
  const [searchFocusIndex, setSearchFocusIndex] = useState(0);

  const defaultCategoryId = useMemo(() => {
    const defCat = categories.find(c => c.is_default === 1);
    if (defCat) return defCat.id;
    const apiRef = categories.find(c => c.name === 'API 参考' || c.name === 'API参考');
    return apiRef?.id ?? categories[0]?.id ?? null;
  }, [categories]);

  const handleCategoryChange = (categoryId: number) => {
    setActiveCategoryId(categoryId);
    const categoryDocs = treeData.filter(node =>
      node.category_id === categoryId ||
      ((node.category_id == null || node.category_id === undefined) && categoryId === defaultCategoryId)
    );
    const firstArticle = findFirstArticle(categoryDocs);
    if (firstArticle) {
      const parentSlug = findParentSlug(treeData, firstArticle.id);
      const path = parentSlug 
        ? `${basePath}/${parentSlug}/${idToSlug(firstArticle.id)}`
        : `${basePath}/${idToSlug(firstArticle.id)}`;
      navigate(path);
    } else {
      navigate(basePath);
    }
  };

  const filteredTree = useMemo(() => {
    let sourceData = treeData;
    // 有二级分类时按分类过滤根节点；无 category_id 的旧文档归入「API 参考」
    if (isSitePortalPro && categories.length > 0 && activeCategoryId !== null) {
      sourceData = treeData.filter(node =>
        node.category_id === activeCategoryId ||
        ((node.category_id == null || node.category_id === undefined) &&
          activeCategoryId === defaultCategoryId)
      );
    }
    if (!searchQuery) return sourceData;
    const filter = (nodes: DocTreeNode[]): DocTreeNode[] => {
      return nodes
        .map(node => {
          if (node.is_dir) {
            const filteredChildren = node.children ? filter(node.children) : [];
            if (filteredChildren.length > 0 || node.title.toLowerCase().includes(searchQuery.toLowerCase())) {
              return { ...node, children: filteredChildren };
            }
          } else {
            if (node.title.toLowerCase().includes(searchQuery.toLowerCase())) {
              return node;
            }
          }
          return null;
        })
        .filter((n): n is DocTreeNode => n !== null);
    };
    return filter(sourceData);
  }, [treeData, searchQuery, activeCategoryId, isSitePortalPro, categories.length, defaultCategoryId]);

  // 如果 URL 里没有带 ID（或当前 ID 所在分类已隐藏），且 filteredTree 已加载，则自动跳转到第一篇文章
  useEffect(() => {
    if (loading) return;
    const isUnavailable = !id || !selectedDocId || !isDocInTree(treeData, selectedDocId);
    if (isUnavailable) {
      if (filteredTree.length > 0) {
        const firstArticle = findFirstArticle(filteredTree);
        if (firstArticle) {
          const parentSlug = findParentSlug(treeData, firstArticle.id);
          const path = parentSlug 
            ? `${basePath}/${parentSlug}/${idToSlug(firstArticle.id)}`
            : `${basePath}/${idToSlug(firstArticle.id)}`;
          navigate(path, { replace: true });
          return;
        }
      }
      if (id) {
        navigate(basePath, { replace: true });
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, selectedDocId, loading, filteredTree, treeData, basePath, navigate]);

  const handleSelectSearchNode = (doc: { id: number; is_dir: boolean }) => {
    if (doc.is_dir) {
      const findDir = (nodes: DocTreeNode[]): DocTreeNode | null => {
        for (const n of nodes) {
          if (n.id === doc.id) return n;
          if (n.children?.length) {
            const found = findDir(n.children);
            if (found) return found;
          }
        }
        return null;
      };
      const dirNode = findDir(treeData);
      setExpandedMenuKeys(prev => Array.from(new Set([...prev, `dir-${doc.id}`])));
      const firstArt = dirNode?.children ? findFirstArticle(dirNode.children) : null;
      if (firstArt) {
        const parentSlug = findParentSlug(treeData, firstArt.id);
        const path = parentSlug
          ? `${basePath}/${parentSlug}/${idToSlug(firstArt.id)}`
          : `${basePath}/${idToSlug(firstArt.id)}`;
        navigate(path);
      }
    } else {
      const parentSlug = findParentSlug(treeData, doc.id);
      const path = parentSlug
        ? `${basePath}/${parentSlug}/${idToSlug(doc.id)}`
        : `${basePath}/${idToSlug(doc.id)}`;
      navigate(path);
    }
    setOpenSearch(false);
  };

  const visibleArticles = useMemo(() => {
    const list: DocTreeNode[] = [];
    const collect = (nodes: DocTreeNode[]) => {
      for (const n of nodes) {
        if (!n.is_dir) {
          list.push(n);
        }
        if (n.children?.length) {
          collect(n.children);
        }
      }
    };
    collect(filteredTree);
    return list;
  }, [filteredTree]);

  const { prevArticle, nextArticle } = useMemo(() => {
    if (!selectedDocId || visibleArticles.length === 0) {
      return { prevArticle: null, nextArticle: null };
    }
    const idx = visibleArticles.findIndex(a => a.id === selectedDocId);
    if (idx === -1) {
      return { prevArticle: null, nextArticle: null };
    }
    return {
      prevArticle: idx > 0 ? visibleArticles[idx - 1] : null,
      nextArticle: idx < visibleArticles.length - 1 ? visibleArticles[idx + 1] : null,
    };
  }, [selectedDocId, visibleArticles]);

  const flatList = useMemo(() => {
    const list: { id: number; title: string; is_dir: boolean }[] = [];
    const traverse = (nodes: DocTreeNode[]) => {
      nodes.forEach(n => {
        if (searchQuery) {
          if (n.title.toLowerCase().includes(searchQuery.toLowerCase())) {
            list.push({ id: n.id, title: n.title, is_dir: n.is_dir });
          }
        } else {
          list.push({ id: n.id, title: n.title, is_dir: n.is_dir });
        }
        if (n.children) traverse(n.children);
      });
    };
    traverse(treeData);
    return list.slice(0, 10);
  }, [treeData, searchQuery]);

  const breadcrumbs = useMemo(() => {
    if (!selectedDocId || treeData.length === 0) return [];
    const path: string[] = [];
    const findPath = (nodes: DocTreeNode[], targetId: number, currentPath: string[]): boolean => {
      for (const node of nodes) {
        if (node.id === targetId) {
          path.push(...currentPath, node.title);
          return true;
        }
        if (node.children && node.children.length > 0) {
          if (findPath(node.children, targetId, [...currentPath, node.title])) {
            return true;
          }
        }
      }
      return false;
    };
    findPath(treeData, selectedDocId, []);
    return path;
  }, [treeData, selectedDocId]);

  interface TocItem {
    text: string;
    level: number;
    anchor: string;
  }
  const tocList = useMemo<TocItem[]>(() => {
    if (!processedContent) return [];
    const lines = processedContent.split('\n');
    const list: TocItem[] = [];
    let inFence = false;
    lines.forEach((line: string) => {
      if (/^\s*(```|~~~)/.test(line)) {
        inFence = !inFence;
        return;
      }
      if (inFence) return;
      const match = line.match(/^(##|###)\s+(.+)$/);
      if (match) {
        const level = match[1].length;
        const text = match[2]
          .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
          .replace(/[`*_~]/g, '')
          .trim();
        const anchor = text.toLowerCase()
          .replace(/[^\w\u4e00-\u9fa5\s-]/g, '')
          .replace(/\s+/g, '-');
        list.push({ text, level, anchor });
      }
    });
    return list;
  }, [processedContent]);

  // Scroll Spy Effect: 监听内容滚动，高亮右侧目录大纲
  useEffect(() => {
    if (tocList.length === 0) return;

    const handleScroll = () => {
      const headingElements = tocList
        .map(item => document.getElementById(item.anchor))
        .filter(Boolean) as HTMLElement[];

      let currentActive = '';
      for (const el of headingElements) {
        const rect = el.getBoundingClientRect();
        if (rect.top <= 100) {
          currentActive = el.id;
        }
      }

      setActiveAnchor(currentActive || (tocList[0] ? tocList[0].anchor : ''));
    };

    const mainContent = document.getElementById('docs-main-content');
    if (mainContent) {
      mainContent.addEventListener('scroll', handleScroll);
      handleScroll();
    }

    return () => {
      if (mainContent) {
        mainContent.removeEventListener('scroll', handleScroll);
      }
    };
  }, [tocList]);

  // 监听全局 Cmd+K 弹窗事件
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault();
        setSearchQuery('');
        setOpenSearch(prev => !prev);
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, []);

  // 监听 Command Palette 键盘导航事件
  useEffect(() => {
    if (!openSearch) return;
    const handleNav = (e: KeyboardEvent) => {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setSearchFocusIndex(prev => (prev + 1) % Math.max(1, flatList.length));
      } else if (e.key === 'ArrowUp') {
        e.preventDefault();
        setSearchFocusIndex(prev => (prev - 1 + flatList.length) % Math.max(1, flatList.length));
      } else if (e.key === 'Enter') {
        e.preventDefault();
        if (flatList[searchFocusIndex]) {
          handleSelectSearchNode(flatList[searchFocusIndex]);
        }
      } else if (e.key === 'Escape') {
        e.preventDefault();
        setOpenSearch(false);
      }
    };
    window.addEventListener('keydown', handleNav);
    return () => window.removeEventListener('keydown', handleNav);
  }, [openSearch, flatList, searchFocusIndex]);

  useEffect(() => {
    setSearchFocusIndex(0);
  }, [searchQuery]);

  const handleTocClick = (anchorId: string) => {
    const element = document.getElementById(anchorId);
    if (element) {
      element.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  };

  const getSidebarIcon = (title: string, slug?: string) => {
    const sizeClass = 'w-3.5 h-3.5 shrink-0';
    const colorClass = 'transition-colors';
    const s = (slug || '').toLowerCase();
    if (s === 'quickstart') return <Rocket className={`${sizeClass} ${colorClass}`} />;
    if (s === 'examples') return <Sparkles className={`${sizeClass} ${colorClass}`} />;
    if (s === 'openai' || s === 'volcengine-ark' || s === 'ali-kling' || s === 'google-anthropic' || s === 'site-api') {
      return <Code className={`${sizeClass} ${colorClass}`} />;
    }
    if (s === 'errors') return <AlertTriangle className={`${sizeClass} ${colorClass}`} />;
    if (s === 'best-practices' || s === 'deploy-install') return <BookOpen className={`${sizeClass} ${colorClass}`} />;
    const t = title.toLowerCase();
    if (t.includes('example') || t.includes('示例') || t.includes('範例') || t.includes('コード例') || t.includes('예제') || t.includes('ví dụ')) return <Sparkles className={`${sizeClass} ${colorClass}`} />;
    if (t.includes('form') || t.includes('表单')) return <ClipboardList className={`${sizeClass} ${colorClass}`} />;
    if (t.includes('query') || t.includes('api') || t.includes('code') || t.includes('开发') || t.includes('代码') || t.includes('relay') || t.includes('协议') || t.includes('協議') || t.includes('openai')) return <Code className={`${sizeClass} ${colorClass}`} />;
    if (t.includes('icon') || t.includes('图标') || t.includes('paint') || t.includes('color') || t.includes('style') || t.includes('设计') || t.includes('样式')) return <Palette className={`${sizeClass} ${colorClass}`} />;
    if (t.includes('setting') || t.includes('config') || t.includes('配置') || t.includes('设置')) return <Settings className={`${sizeClass} ${colorClass}`} />;
    if (t.includes('guide') || t.includes('doc') || t.includes('指南') || t.includes('文档') || t.includes('入门') || t.includes('实践') || t.includes('實踐')) return <BookOpen className={`${sizeClass} ${colorClass}`} />;
    if (t.includes('quick') || t.includes('start') || t.includes('快速') || t.includes('クイック') || t.includes('빠른')) return <Rocket className={`${sizeClass} ${colorClass}`} />;
    return <Folder className={`${sizeClass} ${colorClass}`} />;
  };

  const hasSelectedDescendant = (node: DocTreeNode): boolean => {
    if (!selectedDocId || !node.children?.length) return false;
    return node.children.some((child) => child.id === selectedDocId || hasSelectedDescendant(child));
  };

  // 递归渲染自定义树状目录大纲（与控制台侧栏统一的灰阶导航）
  const renderSidebarTree = (nodes: DocTreeNode[], level: number = 0) => {
    return nodes.map((node) => {
      const isDir = node.is_dir;
      const isSelected = selectedDocId === node.id;
      const isOpen = expandedMenuKeys.includes(`dir-${node.id}`);
      const showIcon = level === 0;

      if (isDir) {
        const hasSelected = hasSelectedDescendant(node);
        return (
          <div key={`dir-${node.id}`} className="flex flex-col mb-0.5 select-none">
            <button
              onClick={() => {
                if (expandedMenuKeys.includes(`dir-${node.id}`)) {
                  setExpandedMenuKeys(expandedMenuKeys.filter(k => k !== `dir-${node.id}`));
                } else {
                  setExpandedMenuKeys([...expandedMenuKeys, `dir-${node.id}`]);
                }
              }}
              style={{ paddingLeft: showIcon ? '8px' : '10px' }}
              className={`docs-nav-dir group flex items-center justify-between w-full h-8 pr-2 text-left text-[14px] font-medium cursor-pointer ${
                hasSelected ? 'has-selected' : ''
              }`}
            >
              <div className="flex items-center gap-2 min-w-0">
                {showIcon && getSidebarIcon(node.title, node.slug)}
                <span className="truncate">{node.title}</span>
              </div>
              <ChevronRight
                className={`w-3.5 h-3.5 shrink-0 transition-transform duration-300 ease-in-out ${
                  isOpen ? 'rotate-90' : ''
                }`}
              />
            </button>
            {node.children && node.children.length > 0 && (
              <div className={`grid transition-all duration-300 ease-in-out ${
                isOpen ? 'grid-rows-[1fr] opacity-100 mt-0.5' : 'grid-rows-[0fr] opacity-0 overflow-hidden'
              }`}>
                <div
                  className="overflow-hidden flex flex-col ml-2.5 pl-1.5"
                  style={{ borderLeft: isLight ? '1px solid #e4e4e7' : '1px solid #1f1f23' }}
                >
                  {renderSidebarTree(node.children, level + 1)}
                </div>
              </div>
            )}
          </div>
        );
      } else {
        return (
          <button
            key={node.id}
            onClick={() => {
              const parentSlug = findParentSlug(treeData, node.id);
              const path = parentSlug 
                ? `${basePath}/${parentSlug}/${idToSlug(node.id)}`
                : `${basePath}/${idToSlug(node.id)}`;
              navigate(path);
              if (screens.xs) handleCollapsedChange(true);
            }}
            style={{ paddingLeft: showIcon ? '8px' : '10px' }}
            className={`docs-nav-item group flex items-center gap-2 w-full h-[32px] text-left text-[13.5px] cursor-pointer mb-0.5 select-none pr-2 ${
              isSelected ? 'selected' : ''
            }`}
          >
            {showIcon && getSidebarIcon(node.title, node.slug)}
            <span className="truncate">{node.title}</span>
          </button>
        );
      }
    });
  };

  const renderDocBody = () => {
    if (detailLoading) {
      return (
        <div className="flex items-center justify-center py-32">
          <Spin size="large" />
        </div>
      );
    }
    if (!docDetail) {
      return (
        <div className="flex flex-col items-center justify-center py-24 text-zinc-400">
          <Empty description={docsT('client_empty_desc')} image={Empty.PRESENTED_IMAGE_SIMPLE} />
        </div>
      );
    }

    const renderDocHeaderActions = () => (
      <div className="flex items-center gap-2 flex-shrink-0">
        <Tooltip title={docCopied ? docsT('copied', '已复制') : docsT('copy_md', '复制MD')} placement="bottom">
          <button
            type="button"
            onClick={handleCopyDoc}
            className={`inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-md border transition-all cursor-pointer shadow-xs ${
              docCopied
                ? 'text-zinc-900 dark:text-zinc-50 bg-zinc-200/90 dark:bg-zinc-700/90 border-zinc-300 dark:border-zinc-600'
                : 'text-zinc-700 dark:text-zinc-200 hover:text-zinc-900 dark:hover:text-white bg-zinc-100 dark:bg-zinc-800/90 hover:bg-zinc-200 dark:hover:bg-zinc-700 border-zinc-200/80 dark:border-zinc-700/80'
            }`}
            aria-label={docsT('copy_md', '复制MD')}
          >
            {docCopied ? (
              <>
                <Check className="w-3.5 h-3.5 text-zinc-900 dark:text-zinc-100" />
                <span className="font-medium text-zinc-900 dark:text-zinc-100">{docsT('copied', '已复制')}</span>
              </>
            ) : (
              <>
                <Copy className="w-3.5 h-3.5 text-zinc-500 dark:text-zinc-400" />
                <span className="font-medium">{docsT('copy_md', '复制MD')}</span>
              </>
            )}
          </button>
        </Tooltip>
        {pluginEnabled && tocList.length > 0 && (
          <Tooltip title={docsT('client_toc_title')} placement="bottom">
            <Button
              type="text"
              shape="circle"
              icon={<GalleryVerticalEnd className="w-4 h-4 text-zinc-500 dark:text-zinc-400" />}
              onClick={() => setOpenOutlineDrawer(true)}
              className="xl:!hidden flex items-center justify-center hover:bg-zinc-100 dark:hover:bg-zinc-900/60 hover:text-zinc-900 dark:hover:text-zinc-100 transition-colors cursor-pointer flex-shrink-0"
              style={{ width: 32, height: 32 }}
            />
          </Tooltip>
        )}
      </div>
    );

    const markdownComponents = {
      h1: ({ children, node, ...props }: any) => {
        const rawText = extractText(children).trim();
        const resolvedH1 =
          docDetail?.slug && docDetail.slug !== 'codex-claude-guide' && rawText
            ? cleanTitle(rawText, docDetail.slug)
            : null;
        return (
          <div className="flex items-center justify-between border-b border-border/80 pb-3 mb-6 mt-2 gap-4">
            <h1 {...props}>{resolvedH1 || children}</h1>
            {renderDocHeaderActions()}
          </div>
        );
      },
      h2: ({ children, ...props }: any) => {
        const text = extractText(children);
        const anchor = text.toLowerCase()
          .replace(/[^\w\u4e00-\u9fa5\s-]/g, '')
          .replace(/\s+/g, '-');
        return <h2 id={anchor} {...props}>{children}</h2>;
      },
      h3: ({ children, ...props }: any) => {
        const text = extractText(children);
        const anchor = text.toLowerCase()
          .replace(/[^\w\u4e00-\u9fa5\s-]/g, '')
          .replace(/\s+/g, '-');
        return <h3 id={anchor} {...props}>{children}</h3>;
      },
      a: ({ href, children, ...props }: any) => {
        const isExternal = typeof href === 'string' && /^https?:\/\//i.test(href);
        const isInternal = typeof href === 'string' && href.startsWith('/');
        return (
          <a
            href={href}
            target={isExternal ? '_blank' : undefined}
            rel={isExternal ? 'noopener noreferrer' : undefined}
            onClick={(e) => {
              if (isInternal && !e.metaKey && !e.ctrlKey) {
                e.preventDefault();
                navigate(href);
              }
            }}
            {...props}
          >
            {children}
          </a>
        );
      },
      code({ node, inline, className, children, ...props }: any) {
        const match = /language-(\w+)/.exec(className || '');
        const rawValue = extractText(children).replace(/\n$/, '');
        return !inline && match ? (
          <CodeBlock language={match[1]} value={rawValue} {...props}>
            {children}
          </CodeBlock>
        ) : (
          <code className={className} {...props}>
            {children}
          </code>
        );
      },
      blockquote: ({ children }: any) => {
        const textContent = extractText(children).trim();

        let type = 'info';
        let cleanText = textContent;
        if (textContent.startsWith('[!NOTE]') || textContent.startsWith('[!INFO]') || textContent.startsWith('[!TIP]')) {
          type = 'info';
          cleanText = textContent.replace(/^\[!(NOTE|INFO|TIP)\]\s*/i, '');
        } else if (textContent.startsWith('[!WARNING]') || textContent.startsWith('[!IMPORTANT]')) {
          type = 'warning';
          cleanText = textContent.replace(/^\[!(WARNING|IMPORTANT)\]\s*/i, '');
        } else if (textContent.startsWith('[!CAUTION]') || textContent.startsWith('[!DANGER]') || textContent.startsWith('[!FAILURE]')) {
          type = 'danger';
          cleanText = textContent.replace(/^\[!(CAUTION|DANGER|FAILURE)\]\s*/i, '');
        } else if (textContent.startsWith('[!SUCCESS]')) {
          type = 'success';
          cleanText = textContent.replace(/^\[!SUCCESS\]\s*/i, '');
        } else {
          return (
            <blockquote className="border-l-4 border-zinc-300 dark:border-zinc-700 bg-zinc-50 dark:bg-zinc-900/30 py-3 px-4 my-6 rounded-r-md font-sans italic text-zinc-600 dark:text-zinc-400">
              {children}
            </blockquote>
          );
        }

        const colors: Record<string, { bg: string; border: string; text: string; icon: React.ReactNode; title: string }> = {
          info: {
            bg: 'bg-blue-500/5 dark:bg-blue-500/5',
            border: 'border-blue-500/20 dark:border-blue-400/20',
            text: 'text-blue-600 dark:text-blue-400',
            title: docsT('callout_note', 'NOTE'),
            icon: <Sparkles className="w-4 h-4" />
          },
          warning: {
            bg: 'bg-amber-500/5 dark:bg-amber-500/5',
            border: 'border-amber-500/20 dark:border-amber-400/20',
            text: 'text-amber-600 dark:text-amber-400',
            title: docsT('callout_warning', 'WARNING'),
            icon: <AlertTriangle className="w-4 h-4" />
          },
          danger: {
            bg: 'bg-red-500/5 dark:bg-red-500/5',
            border: 'border-red-500/20 dark:border-red-400/20',
            text: 'text-red-600 dark:text-red-400',
            title: docsT('callout_danger', 'IMPORTANT'),
            icon: <XCircle className="w-4 h-4" />
          },
          success: {
            bg: 'bg-zinc-500/5 dark:bg-zinc-500/5',
            border: 'border-zinc-500/20 dark:border-zinc-400/20',
            text: 'text-zinc-700 dark:text-zinc-300',
            title: docsT('callout_success', 'TIP'),
            icon: <CheckCircle2 className="w-4 h-4" />
          }
        };

        const cur = colors[type];

        return (
          <div className={`my-6 p-4 rounded-lg border ${cur.bg} ${cur.border} flex gap-3 text-sm`}>
            <div className={`flex-shrink-0 ${cur.text} mt-0.5`}>{cur.icon}</div>
            <div className="flex-1">
              <div className={`font-bold ${cur.text} mb-1 tracking-wider text-[10px]`}>{cur.title}</div>
              <div className="text-zinc-700 dark:text-zinc-300 leading-relaxed text-xs">{cleanText}</div>
            </div>
          </div>
        );
      }
    };

    const hasH1 = /^#\s+/m.test(processedContent);
    const blocks = parseMarkdownBlocks(processedContent);

    return (
      <div className="docs-content-article space-y-6">
        {!hasH1 && docDetail?.title && (
          <div className="flex items-center justify-between border-b border-border/80 pb-3 mb-6 mt-2 gap-4">
            <h1>{currentDocTitle || docDetail.title}</h1>
            {renderDocHeaderActions()}
          </div>
        )}
        {blocks.map((block, idx) => {
          if (block.type === 'markdown') {
            return (
              <ReactMarkdown key={idx} components={markdownComponents} remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeHighlight]}>
                {block.content}
              </ReactMarkdown>
            );
          }
          if (block.type === 'steps') {
            return (
              <div key={idx} className="docs-steps-container">
                <ReactMarkdown key={idx} components={markdownComponents} remarkPlugins={[remarkGfm]} rehypePlugins={[rehypeHighlight]}>
                  {block.content}
                </ReactMarkdown>
              </div>
            );
          }
          if (block.type === 'cards') {
            const cardItems = parseCards(block.content);
            return (
              <div key={idx} className="grid grid-cols-1 md:grid-cols-2 gap-4 my-6">
                {cardItems.map((card, cidx) => (
                  <a
                    key={cidx}
                    href={card.href}
                    target={card.href.startsWith('http') ? '_blank' : '_self'}
                    rel="noopener noreferrer"
                    className="group p-5 rounded-lg border border-border bg-card/40 hover:bg-zinc-50 dark:hover:bg-zinc-900/30 transition-all duration-200 hover:border-zinc-400 dark:hover:border-zinc-700 flex flex-col gap-2 cursor-pointer no-underline"
                  >
                    <div className="flex items-center gap-2">
                      {renderCardIcon(card.icon)}
                      <span className="font-semibold text-xs text-foreground group-hover:text-primary transition-colors">
                        {card.title}
                      </span>
                    </div>
                    {card.desc && (
                      <span className="text-[11px] text-zinc-500 dark:text-zinc-400 leading-relaxed">
                        {card.desc}
                      </span>
                    )}
                  </a>
                ))}
              </div>
            );
          }
          if (block.type === 'tabs') {
            const tabItems = parseTabs(block.content);
            return (
              <TabsComponent
                key={idx}
                items={tabItems}
                markdownComponents={markdownComponents}
              />
            );
          }
          return null;
        })}
        {(prevArticle || nextArticle) && (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 pt-8 mt-10 border-t border-border/80 select-none">
            {prevArticle ? (
              <button
                type="button"
                onClick={() => {
                  const parentSlug = findParentSlug(treeData, prevArticle.id);
                  const path = parentSlug
                    ? `${basePath}/${parentSlug}/${idToSlug(prevArticle.id)}`
                    : `${basePath}/${idToSlug(prevArticle.id)}`;
                  navigate(path);
                }}
                className="group flex flex-col items-start gap-1 p-4 rounded-xl border border-border bg-card/30 hover:bg-zinc-50 dark:hover:bg-zinc-900/40 hover:border-zinc-400 dark:hover:border-zinc-700 transition-all text-left cursor-pointer"
              >
                <span className="text-[11px] text-zinc-400 dark:text-zinc-500">
                  ← {docsT('prev_doc', '上一篇')}
                </span>
                <span className="text-xs font-medium text-zinc-700 dark:text-zinc-200 group-hover:text-zinc-950 dark:group-hover:text-white truncate max-w-full">
                  {prevArticle.title}
                </span>
              </button>
            ) : <div />}
            {nextArticle ? (
              <button
                type="button"
                onClick={() => {
                  const parentSlug = findParentSlug(treeData, nextArticle.id);
                  const path = parentSlug
                    ? `${basePath}/${parentSlug}/${idToSlug(nextArticle.id)}`
                    : `${basePath}/${idToSlug(nextArticle.id)}`;
                  navigate(path);
                }}
                className="group flex flex-col items-end gap-1 p-4 rounded-xl border border-border bg-card/30 hover:bg-zinc-50 dark:hover:bg-zinc-900/40 hover:border-zinc-400 dark:hover:border-zinc-700 transition-all text-right cursor-pointer"
              >
                <span className="text-[11px] text-zinc-400 dark:text-zinc-500">
                  {docsT('next_doc', '下一篇')} →
                </span>
                <span className="text-xs font-medium text-zinc-700 dark:text-zinc-200 group-hover:text-zinc-950 dark:group-hover:text-white truncate max-w-full">
                  {nextArticle.title}
                </span>
              </button>
            ) : <div />}
          </div>
        )}
      </div>
    );
  };

  const renderOutline = (isMobile: boolean = false) => {
    // 1. 查找当前选中项的索引
    const activeIdx = tocList.findIndex(item => item.anchor === activeAnchor);
    
    // 2. 定义高精拼接路径生成函数
    const getPathD = (prev: number | null, curr: number, isLast: boolean) => {
      const xPrev = prev === 3 ? 20 : 8;
      const xCurr = curr === 3 ? 20 : 8;
      const yEnd = isLast ? 16 : 32;

      if (prev === null) {
        return `M${xCurr} 4 L${xCurr} ${yEnd}`;
      }
      if (xPrev === xCurr) {
        return `M${xCurr} 0 L${xCurr} ${yEnd}`;
      }
      if (xPrev === 8 && xCurr === 20) {
        return `M8 0 C8 12, 20 8, 20 20 L20 ${yEnd}`;
      }
      if (xPrev === 20 && xCurr === 8) {
        return `M20 0 C20 12, 8 8, 8 20 L8 ${yEnd}`;
      }
      return `M${xCurr} 0 L${xCurr} ${yEnd}`;
    };

    // 3. 计算流动高亮白线路径生成函数 (在当前聚焦项 y=16 处截断)
    const getActivePathD = (prev: number | null, curr: number, isLast: boolean, isTarget: boolean, idx: number) => {
      const xPrev = prev === 3 ? 20 : 8;
      const xCurr = curr === 3 ? 20 : 8;
      const yEnd = isTarget ? 16 : (isLast ? 16 : 32);

      // 如果在当前聚焦项之后，根本不画白线
      if (activeIdx === -1 || idx > activeIdx) {
        return "";
      }

      // 如果是第一个项目
      if (prev === null) {
        return `M${xCurr} 4 L${xCurr} ${yEnd}`;
      }
      // 同级
      if (xPrev === xCurr) {
        return `M${xCurr} 0 L${xCurr} ${yEnd}`;
      }
      // 一级到二级 (右移)
      if (xPrev === 8 && xCurr === 20) {
        return `M8 0 C8 12, 20 8, 20 16 L20 ${yEnd}`;
      }
      // 二级到一级 (左移)
      if (xPrev === 20 && xCurr === 8) {
        return `M20 0 C20 12, 8 8, 8 16 L8 ${yEnd}`;
      }
      return `M${xCurr} 0 L${xCurr} ${yEnd}`;
    };

    return tocList.map((item, idx) => {
      const isTarget = idx === activeIdx; // 当前聚焦的最终项
      const isActiveChain = activeIdx !== -1 && idx <= activeIdx; // 处于高亮白线流动的链路中
      
      const prevItem = idx > 0 ? tocList[idx - 1] : null;
      const prevLevel = prevItem ? prevItem.level : null;
      const currentLevel = item.level;
      const isLast = idx === tocList.length - 1;

      const pathD = getPathD(prevLevel, currentLevel, isLast);
      const activePathD = getActivePathD(prevLevel, currentLevel, isLast, isTarget, idx);

      // 完美跟随大纲轨线缩进：一级 padding 20px (线在 8px)，二级 padding 32px (线在 20px)
      const textPaddingLeft = currentLevel === 3 ? '32px' : '20px';

      return (
        <div 
          key={idx} 
          className="flex items-stretch h-8 group cursor-pointer relative"
          onClick={() => {
            handleTocClick(item.anchor);
            if (isMobile) {
              setOpenOutlineDrawer(false);
            }
          }}
        >
          {/* 左侧绝对定位的大纲指示线 */}
          <div className="absolute left-0 top-0 bottom-0 w-6 pointer-events-none">
            <svg className="w-full h-full" viewBox="0 0 24 32" fill="none">
              {/* 底层深灰/低调背景线 */}
              <path d={pathD} stroke="currentColor" className="text-zinc-200 dark:text-zinc-900/60" strokeWidth="1.5" />
              {/* 顶层高亮白色/主色线 */}
              {isActiveChain && activePathD && (
                <path d={activePathD} stroke="currentColor" className="text-zinc-900 dark:text-zinc-100" strokeWidth="1.5" />
              )}
              {/* 当前激活聚焦项的指示小圆点 (位于文字中线 y=16) */}
              {isTarget && (
                <circle 
                  cx={currentLevel === 3 ? 20 : 8} 
                  cy={16} 
                  r="2.2" 
                  fill="currentColor" 
                  className="text-zinc-900 dark:text-zinc-100" 
                />
              )}
            </svg>
          </div>

          {/* 右侧大纲标题文本 */}
          <div 
            style={{ paddingLeft: textPaddingLeft }}
            className={`flex items-center text-xs truncate transition-colors leading-relaxed select-none ${
              isTarget
                ? 'text-zinc-900 dark:text-zinc-50 font-semibold'
                : isActiveChain
                  ? 'text-zinc-700 dark:text-zinc-300 font-medium'
                  : 'text-zinc-400 group-hover:text-zinc-700 dark:text-zinc-600 dark:group-hover:text-zinc-300'
            }`}
          >
            {item.text}
          </div>
        </div>
      );
    });
  };

  const isCyberHacker = isSitePortalPro;
  const systemClass = isCyberHacker ? 'cyber-hacker-docs' : 'docs-api-system';
  const showHeaderBrand = !!(screens.xs || collapsed);

  return (
    <div className={`h-screen w-screen overflow-hidden bg-background text-foreground font-sans ${systemClass}`}>
      <style>{`
        .docs-top-nav-glass.ant-layout-header {
          line-height: inherit;
          -webkit-backdrop-filter: ${isLight ? 'blur(16px)' : 'blur(18px) saturate(180%)'} !important;
          backdrop-filter: ${isLight ? 'blur(16px)' : 'blur(18px) saturate(180%)'} !important;
        }
        .docs-sidebar-content { padding: 8px 0; overflow-y: auto; height: 100%; }
        .docs-sidebar-content::-webkit-scrollbar { width: 4px; }
        .docs-sidebar-content::-webkit-scrollbar-thumb { background: ${c.scrollThumb}; border-radius: 4px; }
        .docs-api-system .custom-sider.ant-layout-sider,
        .cyber-hacker-docs .custom-sider.ant-layout-sider {
          transition: all 0.28s cubic-bezier(0.4, 0, 0.2, 1) !important;
        }
        .docs-nav-dir,
        .docs-nav-item {
          color: ${isLight ? '#52525b' : 'rgba(255, 255, 255, 0.65)'};
          background: transparent;
          border-radius: 8px;
          transition: background-color 0.12s, color 0.12s;
        }
        .docs-nav-dir.has-selected {
          color: ${isLight ? '#18181b' : '#ffffff'};
        }
        .docs-nav-dir:hover,
        .docs-nav-item:hover {
          background: ${isLight ? 'rgba(0, 0, 0, 0.04)' : 'rgba(255, 255, 255, 0.08)'};
          color: ${isLight ? '#18181b' : '#ffffff'};
        }
        .docs-nav-dir:active,
        .docs-nav-item:active {
          background: ${isLight ? 'rgba(0, 0, 0, 0.1)' : 'rgba(255, 255, 255, 0.16)'};
        }
        .docs-nav-item.selected {
          background: ${isLight ? 'rgba(0, 0, 0, 0.06)' : 'rgba(255, 255, 255, 0.12)'} !important;
          color: ${isLight ? '#18181b' : '#ffffff'} !important;
          font-weight: 500;
        }
        .docs-search-btn {
          background: ${isLight ? 'rgba(0, 0, 0, 0.03)' : 'rgba(255, 255, 255, 0.04)'};
          border: 1px solid ${isLight ? '#e4e4e7' : '#1f1f23'};
          color: ${isLight ? '#52525b' : 'rgba(255, 255, 255, 0.65)'};
          border-radius: 8px;
          transition: background-color 0.12s, color 0.12s, border-color 0.12s;
        }
        .docs-search-btn:hover {
          background: ${isLight ? 'rgba(0, 0, 0, 0.06)' : 'rgba(255, 255, 255, 0.08)'};
          color: ${isLight ? '#18181b' : '#ffffff'};
        }
        .docs-search-kbd {
          background: ${isLight ? 'rgba(0, 0, 0, 0.05)' : 'rgba(255, 255, 255, 0.08)'};
          border: 1px solid ${isLight ? '#e4e4e7' : 'rgba(255, 255, 255, 0.12)'};
          color: ${isLight ? '#52525b' : 'rgba(255, 255, 255, 0.65)'};
        }
      `}</style>

      <Layout style={{ height: '100vh', overflow: 'hidden', background: 'var(--dashboard)' }}>
        <Sider
          trigger={null}
          collapsible
          collapsed={collapsed}
          theme={themeMode}
          width={240}
          collapsedWidth={0}
          style={{
            boxShadow: 'none',
            borderRight: isLight ? '1px solid #e4e4e7' : '1px solid #1f1f23',
            zIndex: screens.xs ? 1050 : 10,
            position: screens.xs ? 'fixed' : 'relative',
            height: '100%',
            left: 0,
            top: 0,
            bottom: 0,
            overflow: 'hidden',
            background: c.siderBg,
          }}
          className="custom-sider"
        >
          <div
            style={{
              display: 'flex',
              flexDirection: 'column',
              height: '100%',
              // 固定内容宽度，收拢时只被 sider overflow 裁切，文字不会被挤压变形
              width: 240,
              minWidth: 240,
              opacity: collapsed ? 0 : 1,
              transition: collapsed
                ? 'opacity 0.1s ease'
                : 'opacity 0.2s ease 0.16s',
              pointerEvents: collapsed ? 'none' : 'auto',
            }}
          >
            {/* Logo Area */}
            <div
              style={{
                height: screens.xs ? 48 : 56,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                padding: '0 8px',
                borderBottom: isLight ? '1px solid #e4e4e7' : '1px solid #1f1f23',
                cursor: 'pointer',
                flexShrink: 0,
              }}
              onClick={() => navigate(isCyberHacker ? '/home-pro' : '/dashboard')}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 6, width: '100%', justifyContent: 'center', minWidth: 0, overflow: 'hidden' }}>
                {siteLogo ? (
                  <img src={siteLogo} alt="logo" style={{ width: 20, height: 20, objectFit: 'contain', flexShrink: 0 }} />
                ) : (
                  <div className="flex items-center justify-center w-5 h-5 rounded bg-primary text-primary-foreground flex-shrink-0">
                    <Terminal className="w-3 h-3" />
                  </div>
                )}
                <div
                  style={{
                    color: isLight ? '#1f2937' : '#fff',
                    margin: 0,
                    fontSize: siteName.length > 14 ? 12 : siteName.length > 10 ? 13 : siteName.length > 6 ? 14 : 16,
                    fontWeight: 700,
                    lineHeight: 1.2,
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    minWidth: 0,
                  }}
                  title={siteName}
                >
                  {siteName}
                </div>
              </div>
            </div>

            {/* 快捷搜索 */}
            <div
              className="p-3"
              style={{ borderBottom: isLight ? '1px solid #e4e4e7' : '1px solid #1f1f23' }}
            >
              <button
                onClick={() => {
                  setSearchQuery('');
                  setOpenSearch(true);
                }}
                className="docs-search-btn flex items-center justify-between w-full h-8 px-3 text-xs cursor-pointer"
              >
                <span className="flex items-center gap-2">
                  <Search className="w-3.5 h-3.5" />
                  <span>{docsT('search_placeholder')}</span>
                </span>
                <kbd className="docs-search-kbd hidden sm:inline-block font-mono px-1.5 py-0.5 rounded scale-90 text-[10px]">⌘K</kbd>
              </button>
            </div>

            {/* 目录树 */}
            <div className="docs-sidebar-content docs-sidebar-scroll" style={{ flex: 1, overflowY: 'auto', overflowX: 'hidden', padding: '8px', transition: 'all 0.2s' }}>
              {loading ? (
                <div className="flex items-center justify-center pt-10"><Spin size="small" /></div>
              ) : treeData.length === 0 ? (
                <Empty description={docsT('no_docs')} image={Empty.PRESENTED_IMAGE_SIMPLE} className="mt-8" />
              ) : (
                renderSidebarTree(filteredTree)
              )}
            </div>
          </div>
        </Sider>

        <Layout style={{
          background: 'var(--dashboard)',
          position: 'relative',
          overflow: 'hidden',
          display: 'flex',
          flexDirection: 'column',
        }}>
          <Header
            className="docs-top-nav-glass select-none"
            style={{
              position: 'absolute',
              top: 0,
              left: 0,
              right: 0,
              zIndex: 100,
              padding: '0 12px',
              background: isLight ? 'rgba(255, 255, 255, 0.92)' : 'rgba(0, 0, 0, 0.48)',
              backdropFilter: isLight ? 'blur(16px)' : 'blur(18px) saturate(180%)',
              WebkitBackdropFilter: isLight ? 'blur(16px)' : 'blur(18px) saturate(180%)',
              height: screens.xs ? 48 : 56,
              lineHeight: (screens.xs ? 48 : 56) + 'px',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              paddingRight: screens.xs ? 8 : 24,
              borderBottom: isLight
                ? '1px solid rgba(0, 0, 0, 0.08)'
                : '1px solid rgba(255, 255, 255, 0.08)',
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', minWidth: 32, flexShrink: 1 }}>
              <Button
                type="text"
                icon={<SidebarIcon size={16} />}
                onClick={() => handleCollapsedChange(!collapsed)}
                style={{
                  width: 32,
                  height: 32,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  color: themeMode === 'light' ? '#71717a' : '#a1a1aa',
                  borderRadius: 6,
                  flexShrink: 0
                }}
              />

              <div
                style={{
                  overflow: 'hidden',
                  maxWidth: showHeaderBrand ? 220 : 0,
                  marginLeft: showHeaderBrand ? 6 : 0,
                  opacity: showHeaderBrand ? 1 : 0,
                  flexShrink: 0,
                  // 收拢：侧栏淡出后再显现；展开：先淡出再让位
                  transition: showHeaderBrand
                    ? 'opacity 0.2s ease 0.14s, max-width 0.28s cubic-bezier(0.4, 0, 0.2, 1) 0.08s, margin-left 0.28s cubic-bezier(0.4, 0, 0.2, 1) 0.08s'
                    : 'opacity 0.1s ease, max-width 0.22s cubic-bezier(0.4, 0, 0.2, 1), margin-left 0.22s cubic-bezier(0.4, 0, 0.2, 1)',
                }}
              >
                <div
                  role="button"
                  tabIndex={showHeaderBrand ? 0 : -1}
                  aria-hidden={!showHeaderBrand}
                  onClick={() => {
                    if (!showHeaderBrand) return;
                    navigate(isCyberHacker ? '/home-pro' : '/dashboard');
                  }}
                  onKeyDown={(e) => {
                    if (!showHeaderBrand) return;
                    if (e.key === 'Enter' || e.key === ' ') {
                      e.preventDefault();
                      navigate(isCyberHacker ? '/home-pro' : '/dashboard');
                    }
                  }}
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 6,
                    // 内层固定内容宽度，外层裁切，避免 max-width 动画挤压文字
                    width: 'max-content',
                    maxWidth: 220,
                    whiteSpace: 'nowrap',
                    cursor: showHeaderBrand ? 'pointer' : 'default',
                    pointerEvents: showHeaderBrand ? 'auto' : 'none',
                  }}
                >
                  {siteLogo ? (
                    <img src={siteLogo} alt="logo" style={{ width: 18, height: 18, objectFit: 'contain', flexShrink: 0 }} />
                  ) : (
                    <div className="flex items-center justify-center w-4.5 h-4.5 rounded bg-primary text-primary-foreground flex-shrink-0">
                      <Terminal className="w-3 h-3" />
                    </div>
                  )}
                  <span
                    style={{
                      color: themeMode === 'light' ? '#1f2937' : '#fff',
                      margin: 0,
                      fontSize: screens.xs
                        ? (siteName.length > 14 ? 11 : siteName.length > 10 ? 12 : 13)
                        : (siteName.length > 14 ? 12 : siteName.length > 10 ? 13 : siteName.length > 6 ? 14 : 16),
                      fontWeight: 600,
                      whiteSpace: 'nowrap',
                      lineHeight: 1.2,
                      flexShrink: 0,
                    }}
                    title={siteName}
                  >
                    {siteName}
                  </span>
                </div>
              </div>

              {!screens.xs && categories.length > 0 && (
                <div
                  className="flex items-center gap-2 overflow-x-auto no-scrollbar py-2 px-2"
                  style={{
                    marginLeft: 6,
                    minWidth: 0,
                    flexShrink: 1,
                    transition: 'margin-left 0.28s cubic-bezier(0.4, 0, 0.2, 1)',
                  }}
                >
                  {categories.map(cat => (
                    <button
                      key={cat.id}
                      className={`docs-category-btn px-3 py-1 text-sm rounded-full transition-all cursor-pointer whitespace-nowrap flex-shrink-0 ${
                        activeCategoryId === cat.id ? 'active' : ''
                      }`}
                      onClick={() => handleCategoryChange(cat.id)}
                    >
                      {formatPresetCategoryName(cat.name, docsT)}
                    </button>
                  ))}
                </div>
              )}
            </div>

            <div
              className="header-nav-actions flex items-center flex-shrink-0"
              style={{ position: 'relative', display: 'flex', alignItems: 'center', gap: screens.xs ? 2 : 6, flexShrink: 0, lineHeight: 1 }}
              ref={(container) => {
                if (!container) return;
                requestAnimationFrame(() => {
                  if (!container.isConnected) return;
                  const pill = container.querySelector('.header-nav-hover-pill') as HTMLElement | null;
                  if (!pill) return;
                  const pt = (window as any).__tkeHeaderPointer as { x: number; y: number } | undefined;
                  const hovered = (
                    (pt ? document.elementFromPoint(pt.x, pt.y)?.closest('.ant-btn') : null) ||
                    container.querySelector('.ant-btn:hover')
                  ) as HTMLElement | null;
                  if (hovered && container.contains(hovered) && !hovered.closest('.header-avatar-wrap')) {
                    const cRect = container.getBoundingClientRect();
                    const bRect = hovered.getBoundingClientRect();
                    if (bRect.width > 0 && bRect.height > 0) {
                      const x = bRect.left - cRect.left;
                      const y = bRect.top - cRect.top;
                      const isCircle = hovered.classList.contains('ant-btn-circle');
                      const radius = isCircle ? `${bRect.height / 2}px` : '10px';
                      const baseTransform = `translate3d(${x}px, ${y}px, 0)`;
                      pill.dataset.lastTransform = baseTransform;
                      if (pill.dataset.visible !== '1') {
                        pill.style.transition = 'none';
                      }
                      pill.style.width = `${bRect.width}px`;
                      pill.style.height = `${bRect.height}px`;
                      pill.style.borderRadius = radius;
                      pill.style.transform = `${baseTransform} scale(1)`;
                      pill.style.opacity = '1';
                      pill.dataset.visible = '1';
                    }
                  }
                });
              }}
              onMouseDown={(e) => {
                (window as any).__tkeHeaderPointer = { x: e.clientX, y: e.clientY };
              }}
              onMouseMove={(e) => {
                (window as any).__tkeHeaderPointer = { x: e.clientX, y: e.clientY };
              }}
              onMouseOver={(e) => {
                (window as any).__tkeHeaderPointer = { x: e.clientX, y: e.clientY };
                const container = e.currentTarget;
                const pill = container.querySelector('.header-nav-hover-pill') as HTMLElement | null;
                if (!pill) return;
                const btn = (e.target as HTMLElement).closest('.ant-btn') as HTMLElement | null;
                if (!btn || !container.contains(btn) || btn.closest('.header-avatar-wrap')) {
                  if ((e.target as HTMLElement).closest('.header-avatar-wrap')) {
                    pill.dataset.visible = '0';
                    pill.style.opacity = '0';
                    if (pill.dataset.lastTransform) {
                      pill.style.transform = `${pill.dataset.lastTransform} scale(0.92)`;
                    }
                  }
                  return;
                }
                const cRect = container.getBoundingClientRect();
                const bRect = btn.getBoundingClientRect();
                const x = bRect.left - cRect.left;
                const y = bRect.top - cRect.top;
                const isCircle = btn.classList.contains('ant-btn-circle');
                const radius = isCircle ? `${bRect.height / 2}px` : '10px';
                const baseTransform = `translate3d(${x}px, ${y}px, 0)`;
                pill.dataset.lastTransform = baseTransform;
                if (pill.dataset.visible !== '1') {
                  pill.style.transition = 'none';
                  pill.style.width = `${bRect.width}px`;
                  pill.style.height = `${bRect.height}px`;
                  pill.style.borderRadius = radius;
                  pill.style.transform = `${baseTransform} scale(0.92)`;
                  void pill.offsetWidth;
                  pill.style.transition = 'transform 0.32s cubic-bezier(0.22, 1, 0.36, 1), width 0.32s cubic-bezier(0.22, 1, 0.36, 1), height 0.32s cubic-bezier(0.22, 1, 0.36, 1), border-radius 0.32s cubic-bezier(0.22, 1, 0.36, 1), opacity 0.2s ease';
                  pill.style.transform = `${baseTransform} scale(1)`;
                  pill.style.opacity = '1';
                  pill.dataset.visible = '1';
                } else {
                  pill.style.transition = 'transform 0.32s cubic-bezier(0.22, 1, 0.36, 1), width 0.32s cubic-bezier(0.22, 1, 0.36, 1), height 0.32s cubic-bezier(0.22, 1, 0.36, 1), border-radius 0.32s cubic-bezier(0.22, 1, 0.36, 1), opacity 0.2s ease';
                  pill.style.width = `${bRect.width}px`;
                  pill.style.height = `${bRect.height}px`;
                  pill.style.borderRadius = radius;
                  pill.style.transform = `${baseTransform} scale(1)`;
                  pill.style.opacity = '1';
                }
              }}
              onMouseLeave={(e) => {
                (window as any).__tkeHeaderPointer = { x: e.clientX, y: e.clientY };
                const pill = e.currentTarget.querySelector('.header-nav-hover-pill') as HTMLElement | null;
                if (!pill) return;
                pill.dataset.visible = '0';
                pill.style.opacity = '0';
                if (pill.dataset.lastTransform) {
                  pill.style.transform = `${pill.dataset.lastTransform} scale(0.92)`;
                }
              }}
            >
              <style>{`
                .docs-api-system .header-badge.ant-badge {
                  display: flex !important;
                  align-items: center;
                  justify-content: center;
                  height: 40px;
                }
                .docs-api-system .header-nav-hover-pill {
                  position: absolute;
                  top: 0;
                  left: 0;
                  opacity: 0;
                  pointer-events: none;
                  z-index: 0;
                  will-change: transform, width, height, border-radius, opacity;
                  background: ${
                    themeMode === 'light'
                      ? 'rgba(0, 0, 0, 0.075)'
                      : 'rgba(255, 255, 255, 0.085)'
                  };
                  backdrop-filter: blur(10px) saturate(150%);
                  -webkit-backdrop-filter: blur(10px) saturate(150%);
                }
                .docs-api-system .header-nav-actions .ant-btn:not(.ant-btn-primary) {
                  position: relative;
                  z-index: 1;
                  display: inline-flex !important;
                  align-items: center !important;
                  justify-content: center !important;
                  line-height: 1 !important;
                  background: transparent !important;
                  transition: color 0.22s ease, transform 0.22s ease !important;
                }
                .docs-api-system .header-nav-actions .ant-btn:not(.ant-btn-primary):hover,
                .docs-api-system .header-nav-actions .ant-btn:not(.ant-btn-primary):focus,
                .docs-api-system .header-nav-actions .ant-btn:not(.ant-btn-primary):active {
                  background: transparent !important;
                }
                .docs-api-system .header-nav-actions .ant-btn .ant-btn-icon {
                  display: inline-flex !important;
                  align-items: center !important;
                  justify-content: center !important;
                  line-height: 1 !important;
                  margin-inline-end: 0 !important;
                }
                .docs-api-system .header-nav-actions .ant-btn > span:not(.ant-btn-icon) {
                  display: inline-flex !important;
                  align-items: center !important;
                  line-height: 1 !important;
                  margin-inline-start: 0 !important;
                }
                .docs-api-system .header-nav-actions .ant-btn svg {
                  display: block;
                  transition: transform 0.28s cubic-bezier(0.34, 1.56, 0.64, 1);
                }
                .docs-api-system .header-nav-actions .ant-btn:hover svg {
                  transform: scale(1.06);
                }
                .docs-api-system .header-nav-actions .ant-btn:active svg {
                  transform: scale(0.95);
                }
              `}</style>
              <div className="header-nav-hover-pill" />
              <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                {headerNavReady && isPluginVisibleForUser('model_marketplace') && (
                  <Button
                    type="text"
                    href="/home/models"
                    icon={
                      <svg
                        width="20"
                        height="20"
                        viewBox="0 0 24 24"
                        fill="none"
                        xmlns="http://www.w3.org/2000/svg"
                      >
                        <path d="M12 2.5L19.5 6.5L12 10.5L4.5 6.5Z" fill={themeMode === 'light' ? '#e0e0e0' : '#424242'} />
                        <path d="M3.5 8L11 12V20.5L3.5 16.5Z" fill={themeMode === 'light' ? '#b0b0b0' : '#555555'} />
                        <path d="M13 12L20.5 8V16.5L13 20.5Z" fill={themeMode === 'light' ? '#757575' : '#9e9e9e'} />
                      </svg>
                    }
                    style={{
                      color: themeMode === 'light' ? '#1f2937' : '#fff',
                      gap: 4,
                      fontSize: 14,
                      fontWeight: 500,
                      height: screens.xs ? 34 : 40,
                      width: screens.xs ? 34 : undefined,
                      padding: screens.xs ? 0 : '0 10px 0 7px',
                    }}
                    onClick={(e) => {
                      if (!e.metaKey && !e.ctrlKey) {
                        e.preventDefault();
                        navigate('/home/models');
                      }
                    }}
                  >
                    {!screens.xs && (
                      <span>{_t('menu.model_marketplace', 'Models')}</span>
                    )}
                  </Button>
                )}

                {headerNavReady && (
                  <Button
                    type="text"
                    href="/docs"
                    icon={
                      <svg
                        width="20"
                        height="20"
                        viewBox="0 0 24 24"
                        fill="none"
                        xmlns="http://www.w3.org/2000/svg"
                      >
                        <rect x="8" y="2.5" width="2.6" height="5.8" rx="1.2" fill={themeMode === 'light' ? '#757575' : '#9e9e9e'} />
                        <rect x="13.4" y="2.5" width="2.6" height="5.8" rx="1.2" fill={themeMode === 'light' ? '#757575' : '#9e9e9e'} />
                        <path d="M4.5 7.5H19.5V10H4.5V7.5Z" fill={themeMode === 'light' ? '#e0e0e0' : '#2e2e2e'} />
                        <path d="M5 10H12V21C7.8 21 5 18.6 5 15.2V10Z" fill={themeMode === 'light' ? '#b0b0b0' : '#555555'} />
                        <path d="M12 10H19V15.2C19 18.6 16.2 21 12 21V10Z" fill={themeMode === 'light' ? '#757575' : '#9e9e9e'} />
                        <path d="M8.5 12.2V16.8" stroke={themeMode === 'light' ? '#757575' : '#2e2e2e'} strokeWidth="1.4" strokeLinecap="round" />
                        <path d="M15.5 12.2V16.8" stroke={themeMode === 'light' ? '#b0b0b0' : '#555555'} strokeWidth="1.4" strokeLinecap="round" />
                      </svg>
                    }
                    style={{
                      color: themeMode === 'light' ? '#1f2937' : '#fff',
                      gap: 4,
                      fontSize: 14,
                      fontWeight: 500,
                      height: screens.xs ? 34 : 40,
                      width: screens.xs ? 34 : undefined,
                      padding: screens.xs ? 0 : '0 10px 0 7px',
                    }}
                    onClick={(e) => {
                      if (!e.metaKey && !e.ctrlKey) {
                        e.preventDefault();
                        navigate('/docs');
                      }
                    }}
                  >
                    {!screens.xs && (
                      <span>{_t('menu.relay_api', 'API教程')}</span>
                    )}
                  </Button>
                )}
              </div>

              {enableThemeToggle && (
                <Tooltip
                  title={themeMode === 'light' ? _t('header.switch_dark_mode', '切换暗色模式') : _t('header.switch_light_mode', '切换亮色模式')}
                  placement="bottom"
                  color={themeMode === 'light' ? '#fff' : '#2b2b2b'}
                  styles={{ container: { color: themeMode === 'light' ? '#1f2937' : '#fff' } }}
                >
                  <Button
                    type="text"
                    shape="circle"
                    onClick={toggleTheme}
                    icon={
                      themeMode === 'light'
                        ? (
                          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
                            <path d="M21 12.79A9 9 0 1111.21 3 7 7 0 0021 12.79Z" fill="#757575" />
                          </svg>
                        )
                        : (
                          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
                            <circle cx="12" cy="12" r="6" fill="#555555" />
                            <path d="M12 2v2m0 16v2M4.93 4.93l1.41 1.41m11.32 11.32l1.41 1.41M2 12h2m16 0h2M4.93 19.07l1.41-1.41m11.32-11.32l1.41-1.41" stroke="#9e9e9e" strokeWidth="2.2" strokeLinecap="round" />
                          </svg>
                        )
                    }
                    style={{ color: themeMode === 'light' ? '#1f2937' : '#fff', width: screens.xs ? 34 : 40, height: screens.xs ? 34 : 40, padding: 0 }}
                  />
                </Tooltip>
              )}

              {enableMultilingual && (
                <Dropdown menu={{ items: langItems, style: { maxHeight: 176, overflowY: 'auto' } }} placement="bottomRight">
                  <Button
                    type="text"
                    shape="circle"
                    icon={
                      <svg width="20" height="20" viewBox="0 0 24 24" fill="none" xmlns="http://www.w3.org/2000/svg">
                        <circle cx="12" cy="12" r="8.5" stroke={themeMode === 'light' ? '#757575' : '#9e9e9e'} strokeWidth="2" />
                        <path d="M3.5 12h17" stroke={themeMode === 'light' ? '#b0b0b0' : '#555555'} strokeWidth="2" strokeLinecap="round" />
                        <ellipse cx="12" cy="12" rx="3.5" ry="8.5" stroke={themeMode === 'light' ? '#b0b0b0' : '#555555'} strokeWidth="2" />
                      </svg>
                    }
                    style={{ color: themeMode === 'light' ? '#1f2937' : '#fff', width: screens.xs ? 34 : 40, height: screens.xs ? 34 : 40, padding: 0 }}
                  />
                </Dropdown>
              )}

              <Popover
                content={announcementContent}
                trigger="click"
                placement="bottomRight"
                overlayClassName="custom-premium-popover"
                open={announcementsDrawerVisible}
                onOpenChange={setAnnouncementsDrawerVisible}
                styles={{ container: { padding: 0, background: 'transparent', boxShadow: 'none' } }}
                motion={{ motionName: '' }}
                arrow={false}
              >
                <Tooltip
                  title={_t('header.notifications', '通知')}
                  placement="bottom"
                  color={themeMode === 'light' ? '#fff' : '#2b2b2b'}
                  styles={{ container: { color: themeMode === 'light' ? '#1f2937' : '#fff' } }}
                >
                  <Badge count={unreadCount} overflowCount={99} offset={[-4, 4]} className="header-badge">
                    <Button
                      type="text"
                      shape="circle"
                      icon={
                        <svg
                          width="20"
                          height="20"
                          viewBox="0 0 24 24"
                          fill="none"
                          xmlns="http://www.w3.org/2000/svg"
                        >
                          <path d="M19 16v-6a7 7 0 00-14 0v6l-2 2h18l-2-2z" fill={themeMode === 'light' ? '#757575' : '#9e9e9e'} stroke={themeMode === 'light' ? '#757575' : '#9e9e9e'} strokeWidth="1.5" strokeLinejoin="round" />
                          <path d="M10 19a2 2 0 004 0" stroke={themeMode === 'light' ? '#b0b0b0' : '#555555'} strokeWidth="2.5" strokeLinecap="round" />
                        </svg>
                      }
                      style={{ color: themeMode === 'light' ? '#1f2937' : '#fff', width: screens.xs ? 34 : 40, height: screens.xs ? 34 : 40, padding: 0 }}
                      onClick={() => {
                        setUnreadCount(0);
                      }}
                    />
                  </Badge>
                </Tooltip>
              </Popover>

              <div className="header-avatar-wrap" style={{ display: 'flex', alignItems: 'center' }}>
                <UserAvatarMenu isUserEnd={true} agreement={agreement} />
              </div>
            </div>
          </Header>

          <Content style={{
            position: 'relative',
            zIndex: 1,
            margin: 0,
            padding: 0,
            flex: 1,
            overflow: 'hidden',
            background: 'var(--dashboard)',
            display: 'flex',
            flexDirection: 'column',
          }}>
            <div className="flex-1 flex overflow-hidden min-h-0">
              <main
                id="docs-main-content"
                className="flex-1 overflow-y-auto px-6 pb-6 pt-[80px] md:px-10 md:pb-10 md:pt-[96px] xl:pr-16 xl:pl-8 no-scrollbar"
              >
                <div className="max-w-[820px] mx-auto pb-20">
                  {forbidden ? (
                    <div className="max-w-md mx-auto text-center border border-border bg-card p-10 rounded-xl mt-16 shadow-sm">
                      <AlertTriangle className="text-4xl text-amber-500 mb-6 mx-auto" />
                      <h3 className="text-base font-bold mb-2">{_t('marketplace.no_access', '无访问权限')}</h3>
                      <p className="text-xs text-zinc-500 mb-6 leading-relaxed">
                        {_t('marketplace.no_access_desc', '您当前所在的用户等级暂未开放此功能访问权限')}
                      </p>
                      <button
                        onClick={() => navigate(isCyberHacker ? '/home-pro' : '/dashboard')}
                        className="px-4 h-9 text-xs font-medium bg-zinc-900 hover:bg-zinc-800 text-white dark:bg-zinc-50 dark:hover:bg-zinc-100 dark:text-zinc-950 rounded-md transition-colors cursor-pointer"
                      >
                        {docsT('client_back_to_dashboard')}
                      </button>
                    </div>
                  ) : pluginEnabled ? (
                    <>
                      {breadcrumbs.length > 0 && (
                        <nav className="flex items-center gap-1 text-[11px] text-zinc-400 mb-6 select-none">
                          {breadcrumbs.map((crumb, idx) => (
                            <React.Fragment key={idx}>
                              {idx > 0 && <span className="text-[9px] text-zinc-300 dark:text-zinc-700 mx-1">/</span>}
                              <span className={idx === breadcrumbs.length - 1 ? "text-zinc-700 dark:text-zinc-300 font-medium" : ""}>
                                {crumb}
                              </span>
                            </React.Fragment>
                          ))}
                        </nav>
                      )}
                      <div className="py-2 px-1">
                        {renderDocBody()}
                      </div>
                    </>
                  ) : (
                    <div className="max-w-md mx-auto text-center border border-border bg-card p-10 rounded-xl mt-16 shadow-sm">
                      <BookOpen className="text-4xl text-red-500 mb-6 mx-auto" />
                      <h3 className="text-base font-bold mb-2">{docsT('client_plugin_disabled')}</h3>
                      <p className="text-xs text-zinc-500 mb-6 leading-relaxed">
                        {docsT('client_plugin_disabled_desc')}
                      </p>
                      <button
                        onClick={() => navigate(isCyberHacker ? '/home-pro' : '/dashboard')}
                        className="px-4 h-9 text-xs font-medium bg-zinc-900 hover:bg-zinc-800 text-white dark:bg-zinc-50 dark:hover:bg-zinc-100 dark:text-zinc-950 rounded-md transition-colors cursor-pointer"
                      >
                        {docsT('client_back_to_dashboard')}
                      </button>
                    </div>
                  )}
                </div>
              </main>

              {pluginEnabled && tocList.length > 0 && (
                <aside className="hidden xl:block w-[280px] flex-shrink-0 pt-[88px] pb-8 pl-4 pr-4 select-none overflow-y-auto no-scrollbar">
                  <div className="sticky top-0">
                    <div className="flex items-center gap-2 text-zinc-500 dark:text-zinc-400 mb-5">
                      <GalleryVerticalEnd className="w-4 h-4 text-zinc-400 dark:text-zinc-500" />
                      <h4 className="text-xs font-semibold m-0 text-zinc-700 dark:text-zinc-300">{docsT('client_toc_title')}</h4>
                    </div>
                    <div className="flex flex-col">
                      {renderOutline(false)}
                    </div>
                  </div>
                </aside>
              )}
            </div>
          </Content>

          {screens.xs && !collapsed && (
            <div
              style={{
                position: 'fixed',
                top: 0,
                left: 0,
                right: 0,
                bottom: 0,
                background: 'rgba(0,0,0,0.5)',
                zIndex: 1040,
              }}
              onClick={() => handleCollapsedChange(true)}
            />
          )}
        </Layout>
      </Layout>

      {/* 全局命令调色板搜索弹窗 Command Palette */}
      {openSearch && (
        <div
          className="fixed inset-0 bg-black/40 backdrop-blur-xs z-[999] flex items-start justify-center pt-24 px-4 select-none"
          onClick={() => setOpenSearch(false)}
        >
          <div
            className="w-full max-w-lg bg-card border border-border rounded-xl shadow-2xl overflow-hidden flex flex-col max-h-[400px] animate-in fade-in zoom-in-95 duration-150"
            onClick={e => e.stopPropagation()}
          >
            {/* 顶层搜索输入区 */}
            <div className="flex items-center px-4 border-b border-border h-12 gap-3">
              <Search className="w-4 h-4 text-zinc-400" />
              <input
                autoFocus
                type="text"
                placeholder={docsT('client_palette_placeholder')}
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                className="flex-1 bg-transparent text-foreground text-xs placeholder-zinc-400 border-none outline-none focus:ring-0 focus:border-none focus:outline-none"
              />
              <kbd className="text-[9px] text-zinc-400 bg-zinc-100 dark:bg-zinc-900 border border-border px-1.5 py-0.5 rounded shadow-sm">ESC</kbd>
            </div>

            {/* 搜索结果区 */}
            <div className="flex-1 overflow-y-auto p-2 docs-sidebar-scroll">
              {flatList.length === 0 ? (
                <div className="text-center py-10 text-xs text-zinc-400">{docsT('client_palette_no_results')}</div>
              ) : (
                flatList.map((doc, idx) => {
                  const isFocused = searchFocusIndex === idx;
                  return (
                    <div
                      key={doc.id}
                      onClick={() => handleSelectSearchNode(doc)}
                      onMouseEnter={() => setSearchFocusIndex(idx)}
                      className={`flex items-center gap-3 px-3 py-2.5 rounded-lg text-xs transition-colors cursor-pointer ${
                        isFocused
                          ? 'bg-zinc-50 dark:bg-zinc-900/60 text-foreground font-semibold'
                          : 'text-zinc-600 dark:text-zinc-400'
                      }`}
                    >
                      {doc.is_dir ? <Folder className="w-3.5 h-3.5 text-zinc-400" /> : <FileText className="w-3.5 h-3.5 text-zinc-400" />}
                      <span className="flex-1 truncate">{doc.title}</span>
                      {isFocused && <span className="text-[10px] text-zinc-400 font-sans">Enter ↩</span>}
                    </div>
                  );
                })
              )}
            </div>

            {/* 操作提示页脚 */}
            <div className="px-4 py-2 border-t border-border bg-zinc-50/50 dark:bg-zinc-900/20 flex items-center justify-between text-[10px] text-zinc-400 select-none">
              <div className="flex items-center gap-3">
                <span><kbd className="bg-zinc-100 dark:bg-zinc-800 px-1 py-0.5 rounded border border-border">↑↓</kbd> {docsT('client_palette_move')}</span>
                <span><kbd className="bg-zinc-100 dark:bg-zinc-800 px-1 py-0.5 rounded border border-border">↵</kbd> {docsT('client_palette_select')}</span>
              </div>
              <span>{docsT('client_palette_exit')}</span>
            </div>
          </div>
        </div>
      )}

      {/* 手机/小屏幕端大纲抽屉 Mobile Outline Drawer */}
      <Drawer
        title={docsT('client_toc_title')}
        placement="right"
        onClose={() => setOpenOutlineDrawer(false)}
        open={openOutlineDrawer}
        width={280}
        styles={{
          body: {
            padding: '24px 16px',
            background: themeMode === 'light' ? '#fff' : '#09090b',
          },
          header: {
            background: themeMode === 'light' ? '#fff' : '#09090b',
            borderBottom: themeMode === 'light' ? '1px solid rgba(0,0,0,0.06)' : '1px solid rgba(255,255,255,0.08)',
          }
        }}
      >
        <div className="flex flex-col no-scrollbar overflow-y-auto max-h-full">
          {renderOutline(true)}
        </div>
      </Drawer>
    </div>
  );
};

export default RelayAPI;
