import React, { useState, useEffect, useRef } from 'react';
import { Typography, Button, Space, Input, Select } from 'antd';
import { useTranslation } from 'react-i18next';
import { 
  RightOutlined, 
  LeftOutlined,
  SearchOutlined, 
  HeartOutlined, 
  HeartFilled,
  VideoCameraOutlined,
  PictureOutlined,
  FireOutlined,
  ThunderboltOutlined,
  SlidersOutlined,
  ReadOutlined,
  AudioOutlined,
  ClusterOutlined
} from '@ant-design/icons';

const { Title, Text, Paragraph } = Typography;

interface TrendingPageProps {
  config: any;
  models: any[];
  providers: any[];
  onSelectModel: (model: any) => void;
  isLight: boolean;
  c: any;
  lobeIconSrc: (logo?: string | null, providerLogo?: string | null, fallbackName?: string | null) => string;
  handleLobeIconError: (e: React.SyntheticEvent<HTMLImageElement>) => void;
  getLogoFilter: (logoName: string | undefined, isLight: boolean, resolvedSrc?: string) => string;
  formatPrice: (price: number | string | undefined | null, model?: any) => React.ReactNode;
  onViewAllModels?: (searchQuery?: string) => void;
  onSelectProvider?: (provider: any) => void;
  displayClassName?: (name?: string, nameEn?: string, fallback?: string) => string;
  localizedModelDescription?: (model: any) => string;
}

const isVideoUrl = (url?: string): boolean => {
  if (!url) return false;
  const cleanUrl = url.split('?')[0].split('#')[0].toLowerCase();
  if (/\.(mp4|webm|ogg|mov|m3u8|m4v|flv)$/i.test(cleanUrl)) return true;
  if (url.startsWith('data:video/')) return true;
  if (url.includes('video/mp4') || url.includes('video/webm')) return true;
  return false;
};

const DEFAULT_HERO_SLIDES = [
  {
    id: 'hero-1',
    category: '图生视频',
    title: '可灵 Kling Video v3 图生视频 [Pro]',
    description: 'Kling 3.0 Pro：顶级图生视频模型，具备电影级视觉效果、流畅的动作生成以及原生音频支持。',
    try_model_id: '',
    docs_url: ''
  },
  {
    id: 'hero-2',
    category: 'SOTA 级视频',
    title: 'Seedance 2.0 旗舰视频生成大模型',
    description: '字节跳动推出的全新 SOTA 级视频模型，支持多参考图、视频与音频同步控制生成。',
    try_model_id: '',
    docs_url: ''
  },
  {
    id: 'hero-3',
    category: '高清图像生成',
    title: 'FLUX 1.1 Pro 高清图像创作引擎',
    description: 'Black Forest Labs 打造的顶级文生图模型，高清晰度细节呈现与极佳的提示词遵循度。',
    try_model_id: '',
    docs_url: ''
  }
];

interface HorizontalCarouselProps {
  children: React.ReactNode;
  isLight: boolean;
  itemCount: number;
  gap?: number;
}

const HorizontalCarousel: React.FC<HorizontalCarouselProps> = ({ children, isLight, itemCount, gap }) => {
  const { t: tp } = useTranslation('model_marketplace');
  const outerRef = useRef<HTMLDivElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const [hoverSide, setHoverSide] = useState<'left' | 'right' | null>(null);
  const [canScrollLeft, setCanScrollLeft] = useState(false);
  const [canScrollRight, setCanScrollRight] = useState(false);

  const checkScroll = () => {
    if (!containerRef.current) return;
    const { scrollLeft, scrollWidth, clientWidth } = containerRef.current;
    setCanScrollLeft(scrollLeft > 6);
    setCanScrollRight(scrollLeft + clientWidth < scrollWidth - 6);
  };

  useEffect(() => {
    checkScroll();
    const timer = setTimeout(checkScroll, 120);
    window.addEventListener('resize', checkScroll);
    return () => {
      clearTimeout(timer);
      window.removeEventListener('resize', checkScroll);
    };
  }, [children, itemCount]);

  const handleMouseMove = (e: React.MouseEvent<HTMLDivElement>) => {
    if (!outerRef.current) return;
    const rect = outerRef.current.getBoundingClientRect();
    const mouseX = e.clientX - rect.left;
    if (mouseX < rect.width / 2) {
      setHoverSide('left');
    } else {
      setHoverSide('right');
    }
  };

  const handleMouseLeave = () => {
    setHoverSide(null);
  };

  const handleScrollLeft = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!containerRef.current) return;
    const distance = containerRef.current.clientWidth * 0.75;
    containerRef.current.scrollBy({ left: -distance, behavior: 'smooth' });
  };

  const handleScrollRight = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (!containerRef.current) return;
    const distance = containerRef.current.clientWidth * 0.75;
    containerRef.current.scrollBy({ left: distance, behavior: 'smooth' });
  };

  const showLeftArrow = hoverSide === 'left' && canScrollLeft;
  const showRightArrow = hoverSide === 'right' && canScrollRight;

  return (
    <div 
      ref={outerRef}
      style={{ position: 'relative', width: '100%' }}
      onMouseMove={handleMouseMove}
      onMouseLeave={handleMouseLeave}
    >
      {/* Subtle Left Side Gradient Mask on Hover */}
      <div style={{
        position: 'absolute',
        left: 0,
        top: -4,
        bottom: -4,
        width: 80,
        zIndex: 15,
        pointerEvents: 'none',
        background: `linear-gradient(to right, ${isLight ? 'rgba(249, 250, 251, 0.9)' : 'rgba(9, 9, 11, 0.9)'}, transparent)`,
        opacity: showLeftArrow ? 1 : 0,
        transition: 'opacity 0.25s ease'
      }} />

      {/* Left Navigation Arrow */}
      <button
        type="button"
        onClick={handleScrollLeft}
        title={tp('scroll_left', '向左滑动')}
        style={{
          position: 'absolute',
          left: 0,
          top: '50%',
          transform: `translateY(-50%) ${showLeftArrow ? 'scale(1)' : 'scale(0.9)'}`,
          zIndex: 25,
          width: 38,
          height: 76,
          borderRadius: 8,
          background: isLight ? 'rgba(255, 255, 255, 0.65)' : 'rgba(26, 26, 32, 0.65)',
          backdropFilter: 'blur(12px)',
          WebkitBackdropFilter: 'blur(12px)',
          color: isLight ? '#334155' : '#a1a1aa',
          border: 'none',
          boxShadow: isLight ? '0 6px 20px rgba(0,0,0,0.08)' : '0 8px 24px rgba(0,0,0,0.4)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          cursor: 'pointer',
          opacity: showLeftArrow ? 1 : 0,
          pointerEvents: showLeftArrow ? 'auto' : 'none',
          transition: 'opacity 0.25s ease, transform 0.2s cubic-bezier(0.34, 1.56, 0.64, 1), background-color 0.2s ease',
          fontSize: 18
        }}
      >
        <LeftOutlined />
      </button>

      {/* Track */}
      <div 
        ref={containerRef}
        onScroll={checkScroll}
        style={{
          display: 'flex',
          gap: gap ?? 16,
          overflowX: 'auto',
          paddingTop: 16,
          marginTop: -16,
          paddingBottom: 16,
          scrollbarWidth: 'none',
          WebkitOverflowScrolling: 'touch',
          scrollBehavior: 'smooth'
        }}
      >
        {children}
      </div>

      {/* Subtle Right Side Gradient Mask on Hover */}
      <div style={{
        position: 'absolute',
        right: 0,
        top: -4,
        bottom: -4,
        width: 80,
        zIndex: 15,
        pointerEvents: 'none',
        background: `linear-gradient(to left, ${isLight ? 'rgba(249, 250, 251, 0.9)' : 'rgba(9, 9, 11, 0.9)'}, transparent)`,
        opacity: showRightArrow ? 1 : 0,
        transition: 'opacity 0.25s ease'
      }} />

      {/* Right Navigation Arrow */}
      <button
        type="button"
        onClick={handleScrollRight}
        title={tp('scroll_right', '向右滑动')}
        style={{
          position: 'absolute',
          right: 0,
          top: '50%',
          transform: `translateY(-50%) ${showRightArrow ? 'scale(1)' : 'scale(0.9)'}`,
          zIndex: 25,
          width: 38,
          height: 76,
          borderRadius: 8,
          background: isLight ? 'rgba(255, 255, 255, 0.65)' : 'rgba(26, 26, 32, 0.65)',
          backdropFilter: 'blur(12px)',
          WebkitBackdropFilter: 'blur(12px)',
          color: isLight ? '#334155' : '#a1a1aa',
          border: 'none',
          boxShadow: isLight ? '0 6px 20px rgba(0,0,0,0.08)' : '0 8px 24px rgba(0,0,0,0.4)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          cursor: 'pointer',
          opacity: showRightArrow ? 1 : 0,
          pointerEvents: showRightArrow ? 'auto' : 'none',
          transition: 'opacity 0.25s ease, transform 0.2s cubic-bezier(0.34, 1.56, 0.64, 1), background-color 0.2s ease',
          fontSize: 18
        }}
      >
        <RightOutlined />
      </button>
    </div>
  );
};

const DEFAULT_MODEL_LABS = [
  { key: 'kling', name: 'Kling', logo: 'kling' },
  { key: 'ltx', name: 'LTX', logo: 'ltx' },
  { key: 'xai', name: 'xAI', logo: 'xai' },
  { key: 'openai', name: 'OpenAI', logo: 'openai' },
  { key: 'krea', name: 'Krea', logo: 'krea' },
  { key: 'elevenlabs', name: 'ElevenLabs', logo: 'elevenlabs' },
  { key: 'bytedance', name: 'Bytedance', logo: 'bytedance' },
  { key: 'alibaba', name: 'Alibaba', logo: 'alibaba' },
  { key: 'google', name: 'Google', logo: 'google' },
  { key: 'bria', name: 'Bria AI', logo: 'bria' },
  { key: 'blackforestlabs', name: 'Black Forest Labs', logo: 'flux' },
  { key: 'anthropic', name: 'Anthropic', logo: 'anthropic' },
  { key: 'deepseek', name: 'DeepSeek', logo: 'deepseek' },
  { key: 'minimax', name: 'Hailuo AI', logo: 'minimax' },
  { key: 'midjourney', name: 'Midjourney', logo: 'midjourney' },
  { key: 'runway', name: 'Runway', logo: 'runway' }
];

const TrendingPage: React.FC<TrendingPageProps> = ({ 
  config,
  models, 
  providers, 
  onSelectModel, 
  isLight, 
  c, 
  lobeIconSrc, 
  handleLobeIconError, 
  getLogoFilter,
  onViewAllModels,
  onSelectProvider,
  displayClassName,
  localizedModelDescription
}) => {
  const { t: tp } = useTranslation('model_marketplace');
  const localizeProviderName = (name?: string, nameEn?: string) =>
    displayClassName ? displayClassName(name, nameEn, name || '') : (name || '');
  const localizeSectionTitle = (title?: string) => {
    if (!title) return '';
    if (title === '模型厂家') return tp('model_providers', '模型厂家');
    if (title === 'Seedance 2.0 专题') return tp('fallback_seedance_title', 'Seedance 2.0 专题');
    if (title === 'Grok Imagine 专题') return tp('fallback_grok_title', 'Grok Imagine 专题');
    return displayClassName ? displayClassName(title, undefined, title) : title;
  };
  const localizeSectionDesc = (desc?: string) => {
    if (!desc) return '';
    if (desc === '探索为平台提供强大底层支持的顶尖AI服务商') return tp('fallback_labs_desc', desc);
    if (desc === '字节跳动推出的全新 SOTA 级视频生成模型，即刻体验惊艳的视听合一生成能力。') return tp('fallback_seedance_desc', desc);
    if (desc === '由 xAI 强力驱动的高品质视频、图像与自然语音生成模型系列。') return tp('fallback_grok_desc', desc);
    return desc;
  };
  const [favorites, setFavorites] = useState<{ [key: string]: boolean }>({});
  const [searchQuery, setSearchQuery] = useState<string>('');
  const [currentHeroIndex, setCurrentHeroIndex] = useState(() => Math.floor(Math.random() * 100));
  const [heroProgress, setHeroProgress] = useState<number>(0);
  const [isHeroHovered, setIsHeroHovered] = useState<boolean>(false);
  const [hoveredIndicatorIdx, setHoveredIndicatorIdx] = useState<number | null>(null);

  const [searchHistory, setSearchHistory] = useState<string[]>(() => {
    try {
      const saved = localStorage.getItem('mp_trending_search_history');
      return saved ? JSON.parse(saved) : [];
    } catch (e) {
      return [];
    }
  });

  const handleAddSearchHistory = (keyword: string) => {
    if (!keyword.trim()) return;
    const trimmed = keyword.trim();
    const filtered = searchHistory.filter(item => item !== trimmed);
    const updated = [trimmed, ...filtered].slice(0, 8);
    setSearchHistory(updated);
    try {
      localStorage.setItem('mp_trending_search_history', JSON.stringify(updated));
    } catch (e) {}
  };

  const handleClearSearchHistory = () => {
    setSearchHistory([]);
    try {
      localStorage.removeItem('mp_trending_search_history');
    } catch (e) {}
  };

  const toggleFavorite = (e: React.MouseEvent, key: string) => {
    e.stopPropagation();
    setFavorites(prev => ({ ...prev, [key]: !prev[key] }));
  };

  const validModels = models.filter(m => m.name || m.original_id || m.model_id);

  // Hero Slides (from config or default 3 items)
  const heroSlidesList = config?.hero_slides && config.hero_slides.length > 0 
    ? config.hero_slides 
    : (config?.hero?.title ? [{ id: 'hero-single', ...config.hero }, ...DEFAULT_HERO_SLIDES.slice(1)] : DEFAULT_HERO_SLIDES);

  const activeSlideIndex = (currentHeroIndex % heroSlidesList.length + heroSlidesList.length) % heroSlidesList.length;
  const activeSlide = heroSlidesList[activeSlideIndex] || heroSlidesList[0];
  const activeHeroTryModel = (() => {
    const rawTryId = activeSlide.try_model_id;
    if (rawTryId) {
      const tryId = typeof rawTryId === 'string' && rawTryId.startsWith('orig:') ? rawTryId.replace('orig:', '') : rawTryId;
      const found = models.find(m => 
        m.mid === tryId || 
        m.id?.toString() === tryId || 
        m.original_id === tryId || 
        m.model_id === tryId || 
        m.name === tryId ||
        (m.original_id && m.original_id.toLowerCase() === tryId.toLowerCase()) ||
        (m.model_id && m.model_id.toLowerCase() === tryId.toLowerCase()) ||
        (m.name && m.name.toLowerCase() === tryId.toLowerCase()) ||
        (m.mid && m.mid.toLowerCase() === tryId.toLowerCase()) ||
        (m.variants && m.variants.some((v: any) => (v.original_id || v.model_id || '').toLowerCase() === tryId.toLowerCase()))
      );
      if (found) return found;
      // Partial match fallback if try_model_id contains or is contained in model identifiers
      const partialMatch = models.find(m => 
        (m.original_id && m.original_id.toLowerCase().includes(tryId.toLowerCase())) ||
        (m.model_id && m.model_id.toLowerCase().includes(tryId.toLowerCase())) ||
        (m.name && m.name.toLowerCase().includes(tryId.toLowerCase()))
      );
      if (partialMatch) return partialMatch;
    }

    // Smart fallback by slide title keywords if try_model_id is empty or unmatched
    const titleLower = (activeSlide.title || '').toLowerCase();
    if (titleLower.includes('kling') || titleLower.includes('可灵')) {
      const foundKling = models.find(m => (m.original_id || m.model_id || m.name || '').toLowerCase().includes('kling'));
      if (foundKling) return foundKling;
    }
    if (titleLower.includes('seedance')) {
      const foundSeedance = models.find(m => (m.original_id || m.model_id || m.name || '').toLowerCase().includes('seedance'));
      if (foundSeedance) return foundSeedance;
    }
    if (titleLower.includes('flux')) {
      const foundFlux = models.find(m => (m.original_id || m.model_id || m.name || '').toLowerCase().includes('flux'));
      if (foundFlux) return foundFlux;
    }

    return validModels.length > 0 ? validModels[0] : null;
  })();

  // Auto-play progress bar for hero slides (7000ms duration per slide)
  useEffect(() => {
    if (!heroSlidesList || heroSlidesList.length <= 1 || isHeroHovered) return;

    const DURATION = 7000;
    const INTERVAL = 50;
    const step = (INTERVAL / DURATION) * 100;

    const timer = setInterval(() => {
      setHeroProgress(prev => {
        const nextProgress = prev + step;
        if (nextProgress >= 100) {
          return 100;
        }
        return nextProgress;
      });
    }, INTERVAL);

    return () => clearInterval(timer);
  }, [currentHeroIndex, heroSlidesList, heroSlidesList?.length, isHeroHovered]);

  useEffect(() => {
    if (heroProgress >= 100) {
      setCurrentHeroIndex(prev => (prev + 1) % (heroSlidesList?.length || 1));
      setHeroProgress(0);
    }
  }, [heroProgress, heroSlidesList?.length]);

  const handleHeroSlideSelect = (idx: number) => {
    setCurrentHeroIndex(idx);
    setHeroProgress(0);
  };

  const [perSectionLimit, setPerSectionLimit] = useState<number | string>(16);

  // Section 1: Trending (Latest updated/added models)
  const limitNum = perSectionLimit === 'all' ? validModels.length : Number(perSectionLimit);
  const trendingModels = validModels.slice(0, limitNum);
  
  // Section 2: Model Labs (Providers from Backend)
  const topProviders = providers;

  const renderLabIcon = (lab: any) => {
    const logo = lab.logo;

    // 1. 如果服务商配置的 logo 是完整 URL 或 Data URI，则直接加载
    if (typeof logo === 'string' && (logo.startsWith('http://') || logo.startsWith('https://') || logo.startsWith('data:'))) {
      return (
        <img
          src={logo}
          onError={handleLobeIconError}
          alt={lab.name || ''}
          style={{ width: 34, height: 34, objectFit: 'contain' }}
        />
      );
    }

    // 2. 统一通过 lobeIconSrc 解析图标，使用站点图标库里的原文件
    const src = lobeIconSrc(logo, undefined, lab.name);
    const filter = getLogoFilter(logo || lab.name, isLight, src);
    return (
      <img
        src={src}
        onError={handleLobeIconError}
        alt={lab.name || ''}
        style={{ width: 34, height: 34, objectFit: 'contain', filter }}
      />
    );
  };

  const labsList = React.useMemo(() => {
    if (Array.isArray(providers) && providers.length > 0) {
      return providers.map(p => ({
        id: p.id,
        key: p.name,
        name: p.name,
        logo: p.logo
      }));
    }
    return DEFAULT_MODEL_LABS;
  }, [providers]);

  // Seedance 2.0 Default Items
  const defaultSeedanceModels = [
    {
      id: 'sd-1',
      name: 'seedance-2.0/reference-to-video',
      provider_name: 'bytedance',
      description: '字节跳动旗舰参考图生视频模型。最高支持 9 张参考图、3 段视频与 3 段音频混合控制生成...',
      tags: ['艺术风格化', '动作迁移', '对口型/音频同步'],
      category: '图生视频',
      type_name: '图生视频'
    },
    {
      id: 'sd-2',
      name: 'seedance-2.0/fast/text-to-video',
      provider_name: 'bytedance',
      description: '字节跳动先进文生视频极速版。低延迟低成本，具备电影级镜头感与多镜头合成能力...',
      tags: ['文生视频', '极速生成', '电影质感'],
      category: '文生视频',
      type_name: '文生视频'
    },
    {
      id: 'sd-3',
      name: 'seedance-2.0/image-to-video',
      provider_name: 'bytedance',
      description: '字节跳动旗舰图生视频模型。可将静态图像赋予动态生命力，完美同步音频与首尾帧控制...',
      tags: ['图生视频', '首尾帧控制', '声画同步'],
      category: '图生视频',
      type_name: '图生视频'
    },
    {
      id: 'sd-4',
      name: 'seedance-2.0/fast/reference-to-video',
      provider_name: 'bytedance',
      description: '字节跳动参考图生视频极速版。支持多图与视频参考，更快的响应速度与经济的推理成本...',
      tags: ['多图参考', '极速生成', '动作捕捉'],
      category: '图生视频',
      type_name: '图生视频'
    },
    {
      id: 'sd-5',
      name: 'seedance-2.0/fast/image-to-video',
      provider_name: 'bytedance',
      description: '字节跳动图生视频极速版。支持音视频同步渲染，起始帧与结束帧精细控制...',
      tags: ['单图生成', '极速渲染', '高清镜头'],
      category: '图生视频',
      type_name: '图生视频'
    }
  ];

  // Grok Imagine Default Items
  const defaultGrokModels = [
    {
      id: 'gk-1',
      name: 'grok-imagine-image/quality/text-to-image',
      provider_name: 'xai',
      description: 'xAI 出品的 Grok Imagine 高清画质版。支持精准提示词理解与高质量艺术排版图像生成...',
      tags: ['高清晰度', '文字排版', '艺术创作'],
      category: '文生图',
      type_name: '文生图'
    },
    {
      id: 'gk-2',
      name: 'grok-imagine-video/extend-video',
      provider_name: 'xai',
      description: '使用 xAI Grok Imagine 视频模型对已有视频进行无缝长视频无缝延长与镜头续写...',
      tags: ['视频延长', '镜头续写', 'Grok引擎'],
      category: '视频生视频',
      type_name: '视频生视频'
    },
    {
      id: 'gk-3',
      name: 'tts/v1',
      provider_name: 'xai',
      description: 'xAI 出品的高品质语音合成模型，提供极具情感表达力与逼真度的多语言自然语音...',
      tags: ['语音合成', '情感配音', '自然发音'],
      category: '文本转语音',
      type_name: '文本转语音'
    },
    {
      id: 'gk-4',
      name: 'grok-imagine-video/edit-video',
      provider_name: 'xai',
      description: '利用 xAI Grok Imagine 引擎对视频内容进行风格重绘、局部修改与重构...',
      tags: ['视频重繪', '风格转换', 'Grok引擎'],
      category: '视频生视频',
      type_name: '视频生视频'
    },
    {
      id: 'gk-5',
      name: 'grok-imagine-video/reference-to-video',
      provider_name: 'xai',
      description: '结合多张参考图像，通过 Grok Imagine 视频模型生成连贯顺畅的高清动态视频...',
      tags: ['参考图生成', '连贯动作', 'Grok引擎'],
      category: '图生视频',
      type_name: '图生视频'
    }
  ];

  // Configured Sections or Default Fallback
  const customSections = config?.sections || [];

  const searchableModels = React.useMemo(() => {
    const map = new Map<string, any>();
    validModels.forEach(m => {
      const key = m.mid || m.original_id || m.name || m.id?.toString();
      if (key) map.set(key, m);
    });
    [...defaultSeedanceModels, ...defaultGrokModels].forEach(m => {
      const key = m.id || m.name;
      if (key && !map.has(key)) map.set(key, m);
    });
    return Array.from(map.values());
  }, [validModels]);

  const matchSearchModel = (m: any, query: string): boolean => {
    if (!query) return true;
    const q = query.trim().toLowerCase();
    if (!q) return true;

    const keywords = q.split(/\s+/).filter(Boolean);

    const name = (m.name || '').toLowerCase();
    const origId = (m.original_id || '').toLowerCase();
    const mid = (m.mid || '').toLowerCase();
    const provider = (m.provider_name || '').toLowerCase();
    const category = (m.category || m.type_name || '').toLowerCase();
    const desc = (m.description || m.model_description || '').toLowerCase();
    const tagsList = Array.isArray(m.tags) ? m.tags.join(' ').toLowerCase() : '';

    const fullText = `${name} ${origId} ${mid} ${provider} ${category} ${desc} ${tagsList}`;

    return keywords.every(kw => {
      if (fullText.includes(kw)) return true;

      // Category / Tag Aliases for recommendation pills
      if (kw.includes('图生视频') || kw.includes('视频')) {
        if (category.includes('video') || category.includes('视频') || tagsList.includes('视频') || name.includes('video') || name.includes('kling') || name.includes('seedance') || name.includes('sora') || name.includes('runway') || name.includes('luma') || name.includes('hailuo') || name.includes('pika') || name.includes('hunyuan') || name.includes('grok')) return true;
      }
      if (kw.includes('flux') || kw.includes('图像') || kw.includes('文生图') || kw.includes('画质')) {
        if (name.includes('flux') || category.includes('image') || category.includes('图') || tagsList.includes('flux') || tagsList.includes('画质') || desc.includes('flux')) return true;
      }
      if (kw.includes('3d') || kw.includes('3维')) {
        if (fullText.includes('3d') || fullText.includes('mesh') || fullText.includes('tripo') || fullText.includes('rodin')) return true;
      }
      if (kw.includes('音乐') || kw.includes('音频') || kw.includes('语音') || kw.includes('audio') || kw.includes('music') || kw.includes('tts')) {
        if (category.includes('audio') || category.includes('speech') || category.includes('语音') || category.includes('音乐') || tagsList.includes('音乐') || tagsList.includes('语音') || name.includes('suno') || name.includes('udio') || name.includes('tts') || name.includes('elevenlabs')) return true;
      }
      if (kw.includes('抠图') || kw.includes('去背景') || kw.includes('微调') || kw.includes('试衣')) {
        if (fullText.includes(kw) || tagsList.includes(kw)) return true;
      }
      return false;
    });
  };

  const isSearchActive = searchQuery.trim().length > 0;
  const searchResults = isSearchActive
    ? searchableModels.filter(m => matchSearchModel(m, searchQuery))
    : [];

  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setSearchQuery(e.target.value);
  };

  const handleClearSearch = () => {
    setSearchQuery('');
  };

  const getCardCoverBackground = (index: number) => {
    const gradients = [
      'linear-gradient(135deg, #1e3c72 0%, #2a5298 100%)',
      'linear-gradient(135deg, #3a1c71 0%, #d76d77 50%, #ffaf7b 100%)',
      'linear-gradient(135deg, #0f2027 0%, #203a43 50%, #2c5364 100%)',
      'linear-gradient(135deg, #1f1c2c 0%, #928dab 100%)',
      'linear-gradient(135deg, #2b5876 0%, #4e4376 100%)',
      'linear-gradient(135deg, #141e30 0%, #243b55 100%)',
    ];
    return gradients[index % gradients.length];
  };

  const getCategoryLabel = (rawCategory?: string) => {
    const source = rawCategory || '';
    if (!source || source.includes('image-to-image') || source.includes('图生图')) return tp('cat_image_to_image');
    if (source.includes('image-to-video') || source.includes('图生视频')) return tp('cat_image_to_video');
    if (source.includes('text-to-video') || source.includes('文生视频')) return tp('cat_text_to_video');
    if (source.includes('video-to-video') || source.includes('视频生视频')) return tp('cat_video_to_video');
    if (source.includes('text-to-image') || source.includes('文生图')) return tp('cat_text_to_image');
    if (source.includes('speech') || source.includes('audio') || source.includes('语音') || source.includes('文本转语音')) return tp('cat_text_to_speech');
    if (source.includes('推荐模型')) return tp('cat_recommended');
    return displayClassName ? displayClassName(source, undefined, source) : source;
  };

  const renderModelCard = (model: any, index: number, sectionPrefix: string = 'card') => {
    const cardKey = `${sectionPrefix}-${model.mid || model.id || model.original_id || model.name || index}-${index}`;
    const isFav = !!favorites[cardKey];
    const tagsList = model.tags || ['高清画质', '艺术表现', '声音同步'];
    const rawCat = model.type_name || model.category || '';
    const categoryName = getCategoryLabel(rawCat);
    const isAudioCategory = rawCat.includes('语音') || rawCat.toLowerCase().includes('speech') || rawCat.toLowerCase().includes('audio');
    const cardDesc = (localizedModelDescription ? localizedModelDescription(model) : (model.description || model.model_description)) || tp('no_desc');
    const resolvedModelLogo = model.logo || model.variants?.find((v: any) => v.logo)?.logo;
    const cardIconSrc = lobeIconSrc(resolvedModelLogo, model.provider_logo, model.name || model.original_id);
    const logoFilter = getLogoFilter(resolvedModelLogo || model.provider_logo || model.name, isLight, cardIconSrc);
    const filterStyle = logoFilter && logoFilter !== 'none' 
      ? `${logoFilter} drop-shadow(0 8px 16px rgba(0,0,0,0.4))` 
      : 'drop-shadow(0 8px 16px rgba(0,0,0,0.4))';

    return (
      <div 
        key={cardKey}
        style={{
          minWidth: 260,
          width: 260,
          flexShrink: 0,
          cursor: 'pointer',
          display: 'flex',
          flexDirection: 'column'
        }}
        className="mp-trending-card"
        onClick={() => onSelectModel(model)}
      >
        {/* Card Cover Image with Heart Overlay */}
        <div style={{
          height: 146,
          background: getCardCoverBackground(index),
          position: 'relative',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          overflow: 'hidden',
          borderRadius: '5px 5px 0 0'
        }}>
          <img 
            src={cardIconSrc}
            onError={handleLobeIconError}
            alt={model.name}
            style={{ 
              width: 56, 
              height: 56, 
              objectFit: 'contain',
              filter: filterStyle, 
              opacity: 0.9,
              transform: 'translateY(4px)'
            }}
          />


        </div>

        {/* Card Body */}
        <div style={{ padding: '16px 18px', flex: 1, display: 'flex', flexDirection: 'column', justifyContent: 'space-between' }}>
          <div>
            {/* Model Name */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
              <Text style={{ fontSize: 14, fontWeight: 400, color: isLight ? '#0f172a' : '#fafafa', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {model.name || model.original_id}
              </Text>
            </div>

            {/* Description */}
            <Paragraph 
              ellipsis={{ rows: 2 }} 
              style={{ 
                fontSize: 14, 
                color: isLight ? '#475569' : '#a1a1aa', 
                marginBottom: 16, 
                height: 42, 
                lineHeight: '21px',
                margin: '0 0 16px 0',
                fontWeight: 400
              }}
            >
              {cardDesc}
            </Paragraph>
          </div>

          {/* Bottom Tags (Provider Tag + Category Tag) */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
            {/* Official Provider Tag */}
            {model.provider_name && (
              <span style={{
                fontSize: 11,
                padding: '3px 8px',
                borderRadius: 5,
                border: `1px solid ${isLight ? '#e2e8f0' : 'rgba(255,255,255,0.12)'}`,
                background: isLight ? '#f8fafc' : 'rgba(255,255,255,0.06)',
                color: isLight ? '#475569' : '#a1a1aa',
                fontWeight: 500,
                display: 'inline-flex',
                alignItems: 'center'
              }}>
                {localizeProviderName(model.provider_name, model.provider_name_en)}
              </span>
            )}

            {/* Model Category Tag */}
            <span style={{
              fontSize: 11,
              padding: '3px 8px',
              borderRadius: 5,
              border: `1px solid ${isLight ? '#cbd5e1' : 'rgba(255,255,255,0.1)'}`,
              background: 'transparent',
              color: isLight ? '#64748b' : '#a1a1aa',
              fontWeight: 500,
              display: 'inline-flex',
              alignItems: 'center',
              gap: 4
            }}>
              {isAudioCategory ? <AudioOutlined style={{ fontSize: 11 }} /> : <PictureOutlined style={{ fontSize: 11 }} />}
              {categoryName}
            </span>
          </div>
        </div>
      </div>
    );
  };

  const renderSectionTopic = (sec: any, secIdx: number) => {
    let itemsToRender: any[] = [];

    if (sec.type === 'models' || sec.type === 'groups') {
      if (Array.isArray(sec.items) && sec.items.length > 0) {
        const origItems = sec.items
          .filter((it: string) => typeof it === 'string' && it.startsWith('orig:'))
          .map((it: string) => it.replace('orig:', '').toLowerCase());

        const groupItems = sec.items
          .filter((it: string) => typeof it === 'string' && it.startsWith('group:'))
          .map((it: string) => it.replace('group:', ''));

        const directItems = sec.items.filter((it: string) => typeof it !== 'string' || (!it.startsWith('group:') && !it.startsWith('orig:')));

        itemsToRender = validModels.filter(m => {
          const mOrigId = (m.original_id || m.model_id || '').toLowerCase();
          const mMid = (m.mid || m.id?.toString() || '').toLowerCase();

          // 1. Original ID group match (orig: prefix or exact original_id)
          if (origItems.length > 0) {
            const isOrigMatch = origItems.some((o: string) => 
              mOrigId === o || 
              mMid === o ||
              (m.variants && m.variants.some((v: any) => (v.original_id || v.model_id || '').toLowerCase() === o))
            );
            if (isOrigMatch) return true;
          }

          // 2. Direct ID or Name match
          const directMatch = directItems.includes(m.mid) || 
            directItems.includes(m.id?.toString()) || 
            directItems.includes(m.original_id) ||
            directItems.includes(m.model_id) ||
            directItems.includes(m.name);
          if (directMatch) return true;

          // 3. Capability group / category match
          if (groupItems.length > 0) {
            const cat = (m.type_name || m.category || '').toLowerCase();
            return groupItems.some((g: string) => {
              const gLower = g.toLowerCase();
              return cat.includes(gLower) || gLower.includes(cat);
            });
          }

          return false;
        });
      }
      if (itemsToRender.length === 0) {
        const titleStr = sec.title || '';
        itemsToRender = titleStr.includes('Seedance') ? defaultSeedanceModels : (titleStr.includes('Grok') ? defaultGrokModels : validModels.slice(0, 6));
      }

      const displayItems = perSectionLimit === 'all' ? itemsToRender : itemsToRender.slice(0, Number(perSectionLimit));

      return (
        <div key={sec.id || secIdx} style={{ marginBottom: 48 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <Title level={3} style={{ margin: 0, fontSize: 22, fontWeight: 700, color: c.text1 }}>
                {localizeSectionTitle(sec.title)}
              </Title>
              <span style={{ 
                fontSize: 12, 
                padding: '3px 10px', 
                borderRadius: 12, 
                background: isLight ? '#e0f2fe' : 'rgba(56, 189, 248, 0.15)', 
                color: isLight ? '#0284c7' : '#38bdf8',
                fontWeight: 600
              }}>
                {tp('items_count', { count: displayItems.length })}
              </span>
            </div>
          </div>
          
          {sec.description && (
            <Paragraph style={{ color: c.text3, fontSize: 14, marginBottom: 20 }}>
              {localizeSectionDesc(sec.description)}
            </Paragraph>
          )}

          <HorizontalCarousel isLight={isLight} itemCount={displayItems.length}>
            {displayItems.map((model, idx) => renderModelCard(model, secIdx * 100 + idx, `sec-${sec.id || secIdx}`))}
          </HorizontalCarousel>
        </div>
      );
    } else {
      let providersToRender: any[] = [];
      if (Array.isArray(sec.items) && sec.items.length > 0) {
        providersToRender = providers.filter(p => sec.items.includes(p.id?.toString()) || sec.items.includes(p.name));
      }
      if (providersToRender.length === 0) {
        providersToRender = topProviders;
      }

      return (
        <div key={sec.id || secIdx} style={{ marginBottom: 48 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <Title level={3} style={{ margin: 0, fontSize: 22, fontWeight: 700, color: c.text1 }}>
                {localizeSectionTitle(sec.title)}
              </Title>
              <span style={{ 
                fontSize: 12, 
                padding: '3px 10px', 
                borderRadius: 12, 
                background: isLight ? '#f1f5f9' : 'rgba(255,255,255,0.1)', 
                color: isLight ? '#475569' : '#a1a1aa',
                fontWeight: 600
              }}>
                {tp('providers_count', { count: providersToRender.length })}
              </span>
            </div>
          </div>
          
          {sec.description && (
            <Paragraph style={{ color: c.text3, fontSize: 14, marginBottom: 20 }}>
              {localizeSectionDesc(sec.description)}
            </Paragraph>
          )}

          <HorizontalCarousel isLight={isLight} itemCount={providersToRender.length}>
            {providersToRender.map((provider, idx) => (
              <div 
                key={`sec-prov-${sec.id || secIdx}-${provider.id || provider.name || idx}-${idx}`}
                className="mp-lab-tile"
                style={{
                  minWidth: 84,
                  width: 84,
                  height: 96,
                  borderRadius: 5,
                  background: isLight ? '#ffffff' : '#141417',
                  border: `1px solid ${isLight ? '#e5e7eb' : '#222226'}`,
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'center',
                  justifyContent: 'center',
                  gap: 8,
                  flexShrink: 0
                }}
              >
                <div
                  style={{
                    width: 40,
                    height: 40,
                    background: 'transparent',
                    border: 'none',
                    boxShadow: 'none',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    overflow: 'hidden'
                  }}
                >
                {(() => {
                  const pLogo = provider.logo || (provider.name?.toLowerCase().includes('kimi') ? 'kimi' : undefined);
                  const pSrc = lobeIconSrc(pLogo, undefined, provider.name);
                  return (
                    <img 
                      src={pSrc}
                      onError={handleLobeIconError}
                      alt={provider.name}
                      style={{ 
                        width: 26, 
                        height: 26, 
                        objectFit: 'contain', 
                        filter: getLogoFilter(pLogo || provider.name, isLight, pSrc) 
                      }}
                    />
                  );
                })()}
                </div>
                <Text style={{ fontSize: 11, color: c.text2, fontWeight: 500, textAlign: 'center', maxWidth: 76, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {localizeProviderName(provider.name, provider.name_en)}
                </Text>
              </div>
            ))}
          </HorizontalCarousel>
        </div>
      );
    }
  };

  return (
    <div style={{ 
      padding: 0, 
      width: '100%', 
      background: 'transparent', 
      minHeight: '100%',
      color: c.text1 
    }}>
      <style>{`
        @keyframes heroSlideEnter {
          0% { opacity: 0; transform: translateY(22px) scale(0.97); filter: blur(6px); }
          100% { opacity: 1; transform: translateY(0) scale(1); filter: blur(0); }
        }
        @keyframes heroButtonSlideUp {
          0% { opacity: 0; transform: translateY(28px); filter: blur(6px); }
          100% { opacity: 1; transform: translateY(0); filter: blur(0); }
        }
        .hero-stagger-1 { animation: heroSlideEnter 0.5s cubic-bezier(0.16, 1, 0.3, 1) 0.04s both; }
        .hero-stagger-2 { animation: heroSlideEnter 0.55s cubic-bezier(0.16, 1, 0.3, 1) 0.1s both; }
        .hero-stagger-3 { animation: heroSlideEnter 0.6s cubic-bezier(0.16, 1, 0.3, 1) 0.16s both; }
        .hero-stagger-4 { animation: heroButtonSlideUp 0.65s cubic-bezier(0.16, 1, 0.3, 1) 0.22s both; }
        .mp-trending-card {
          border: 1px solid ${isLight ? '#e5e7eb' : 'rgba(255,255,255,0.06)'} !important;
          background: ${isLight ? '#ffffff' : '#0f0f12'} !important;
          border-radius: 5px !important;
          transition: all 0.2s cubic-bezier(0.4, 0, 0.2, 1);
          overflow: hidden;
        }
        .mp-trending-card:hover {
          transform: translateY(-4px);
          box-shadow: 0 16px 40px -8px ${isLight ? 'rgba(0,0,0,0.1)' : 'rgba(0, 0, 0, 0.8)'} !important;
        }
        .mp-quick-tag {
          transition: all 0.2s cubic-bezier(0.4, 0, 0.2, 1);
          cursor: pointer;
        }
        .mp-quick-tag:hover {
          border-color: ${isLight ? '#2563eb' : 'rgba(255,255,255,0.3)'} !important;
          color: ${isLight ? '#2563eb' : '#ffffff'} !important;
          background: ${isLight ? '#eff6ff' : 'rgba(255,255,255,0.05)'} !important;
        }
        .mp-lab-tile {
          transition: all 0.2s ease;
          cursor: pointer;
          border: 1px solid ${isLight ? '#e5e7eb' : 'rgba(255,255,255,0.06)'} !important;
          border-radius: 5px !important;
          background: ${isLight ? '#ffffff' : '#0f0f12'} !important;
        }
        .mp-lab-tile:hover {
          transform: translateY(-2px);
          border-color: ${isLight ? '#cbd5e1' : 'rgba(255,255,255,0.2)'} !important;
          background: ${isLight ? '#ffffff' : '#18181b'} !important;
        }
        .mp-lab-tile-card {
          transition: all 0.2s cubic-bezier(0.4, 0, 0.2, 1);
          cursor: pointer;
          border: 1px solid ${isLight ? '#e5e7eb' : 'rgba(255,255,255,0.08)'} !important;
          border-radius: 5px !important;
          background: ${isLight ? '#ffffff' : '#141417'} !important;
        }
        .mp-lab-tile-card:hover {
          transform: translateY(-3px);
          border-color: ${isLight ? '#cbd5e1' : 'rgba(255,255,255,0.25)'} !important;
          background: ${isLight ? '#ffffff' : '#1c1c20'} !important;
          box-shadow: 0 8px 24px ${isLight ? 'rgba(0,0,0,0.06)' : 'rgba(0,0,0,0.5)'} !important;
        }
        .mp-lab-tile-card:hover span {
          color: ${isLight ? '#0f172a' : '#ffffff'} !important;
        }
        .mp-search-container .ant-input,
        .mp-search-container .ant-input:focus,
        .mp-search-container .ant-input-focused,
        .mp-search-container .ant-input-affix-wrapper,
        .mp-search-container .ant-input-affix-wrapper:focus,
        .mp-search-container .ant-input-affix-wrapper-focused,
        .mp-search-container .ant-input-affix-wrapper:focus-within,
        .mp-search-container input,
        .mp-search-container input:focus {
          border: none !important;
          outline: none !important;
          box-shadow: none !important;
          background: transparent !important;
        }
        .hero-chevron-btn {
          transition: all 0.2s cubic-bezier(0.4, 0, 0.2, 1) !important;
        }
        .hero-chevron-btn:hover {
          color: ${isLight ? '#0f172a' : '#ffffff'} !important;
          transform: translateY(-50%) scale(1.2) !important;
          opacity: 1 !important;
        }
      `}</style>

      {/* Hero Section */}
      <div 
        onMouseEnter={() => setIsHeroHovered(true)}
        onMouseLeave={() => setIsHeroHovered(false)}
        style={{
          position: 'relative',
          width: '100%',
          minHeight: '420px',
          padding: '44px 48px 32px 48px',
          background: isLight 
            ? 'linear-gradient(180deg, #f8fafc 0%, #ffffff 100%)' 
            : 'radial-gradient(ellipse at top, #1c1c24 0%, #000000 70%)',
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'space-between',
          borderBottom: `1px solid ${isLight ? '#e2e8f0' : 'rgba(255,255,255,0.05)'}`,
          overflow: 'hidden',
          boxSizing: 'border-box'
        }}
      >
        {/* Smooth Sequential Cross-fading Slide Background Layers */}
        {heroSlidesList.map((slideItem: any, idx: number) => {
          const isActive = idx === activeSlideIndex;
          const hasMedia = Boolean(slideItem.bg_image);
          const isVideo = isVideoUrl(slideItem.bg_image);

          // Opaque base background so previous slide background images NEVER leak through
          const opaqueBaseBg = isLight 
            ? 'linear-gradient(180deg, #f8fafc 0%, #ffffff 100%)' 
            : 'linear-gradient(180deg, #111116 0%, #09090b 100%)';

          // Rich themed gradient overlay per slide (6 distinct themes)
          const themeGradients = [
            isLight 
              ? 'radial-gradient(circle at 75% 30%, rgba(124, 58, 237, 0.16) 0%, rgba(59, 130, 246, 0.08) 40%, transparent 75%)'
              : 'radial-gradient(circle at 75% 30%, rgba(124, 58, 237, 0.35) 0%, rgba(59, 130, 246, 0.18) 45%, transparent 75%)',
            isLight 
              ? 'radial-gradient(circle at 75% 30%, rgba(16, 185, 129, 0.16) 0%, rgba(14, 165, 233, 0.08) 40%, transparent 75%)'
              : 'radial-gradient(circle at 75% 30%, rgba(16, 185, 129, 0.35) 0%, rgba(14, 165, 233, 0.18) 45%, transparent 75%)',
            isLight 
              ? 'radial-gradient(circle at 75% 30%, rgba(244, 63, 94, 0.16) 0%, rgba(249, 115, 22, 0.08) 40%, transparent 75%)'
              : 'radial-gradient(circle at 75% 30%, rgba(244, 63, 94, 0.35) 0%, rgba(249, 115, 22, 0.18) 45%, transparent 75%)',
            isLight 
              ? 'radial-gradient(circle at 75% 30%, rgba(99, 102, 241, 0.16) 0%, rgba(168, 85, 247, 0.08) 40%, transparent 75%)'
              : 'radial-gradient(circle at 75% 30%, rgba(99, 102, 241, 0.35) 0%, rgba(168, 85, 247, 0.18) 45%, transparent 75%)',
            isLight 
              ? 'radial-gradient(circle at 75% 30%, rgba(245, 158, 11, 0.16) 0%, rgba(234, 179, 8, 0.08) 40%, transparent 75%)'
              : 'radial-gradient(circle at 75% 30%, rgba(245, 158, 11, 0.35) 0%, rgba(234, 179, 8, 0.18) 45%, transparent 75%)',
            isLight 
              ? 'radial-gradient(circle at 75% 30%, rgba(20, 184, 166, 0.16) 0%, rgba(6, 182, 212, 0.08) 40%, transparent 75%)'
              : 'radial-gradient(circle at 75% 30%, rgba(20, 184, 166, 0.35) 0%, rgba(6, 182, 212, 0.18) 45%, transparent 75%)',
          ];
          const accentGradient = themeGradients[idx % themeGradients.length];

          return (
            <div
              key={slideItem.id ? `hero-bg-${slideItem.id}-${idx}` : `hero-bg-idx-${idx}`}
              style={{
                position: 'absolute',
                inset: 0,
                opacity: isActive ? 1 : 0,
                zIndex: isActive ? 2 : 1,
                pointerEvents: 'none',
                transition: 'opacity 0.7s cubic-bezier(0.4, 0, 0.2, 1)',
                overflow: 'hidden'
              }}
            >
              {/* Opaque Base Color Layer - Ensures Previous Slide Images Are 100% Blocked */}
              <div style={{
                position: 'absolute',
                inset: 0,
                background: opaqueBaseBg
              }} />

              {/* Dynamic Accent Gradient Layer */}
              <div style={{
                position: 'absolute',
                inset: 0,
                background: accentGradient
              }} />

              {/* Background Media Container (Video or Image) with Smooth Scale Cross-fade */}
              {hasMedia && (
                isVideo ? (
                  <video
                    src={slideItem.bg_image}
                    autoPlay
                    loop
                    muted
                    playsInline
                    aria-hidden="true"
                    style={{
                      position: 'absolute',
                      top: '50%',
                      left: '50%',
                      width: '100%',
                      height: '100%',
                      minWidth: '100%',
                      minHeight: '100%',
                      objectFit: 'cover',
                      objectPosition: 'center',
                      transform: isActive ? 'translate(-50%, -50%) scale(1.06)' : 'translate(-50%, -50%) scale(1.12)',
                      transition: 'opacity 0.7s ease-in-out, transform 0.9s cubic-bezier(0.16, 1, 0.3, 1)',
                      willChange: 'transform, opacity',
                      pointerEvents: 'none'
                    }}
                  />
                ) : (
                  <div 
                    style={{
                      position: 'absolute',
                      top: '50%',
                      left: '50%',
                      width: '100%',
                      height: '100%',
                      minWidth: '100%',
                      minHeight: '100%',
                      backgroundImage: `url(${slideItem.bg_image})`,
                      backgroundSize: 'cover',
                      backgroundPosition: 'center',
                      transform: isActive ? 'translate(-50%, -50%) scale(1.06)' : 'translate(-50%, -50%) scale(1.12)',
                      transition: 'opacity 0.7s ease-in-out, transform 0.9s cubic-bezier(0.16, 1, 0.3, 1)',
                      willChange: 'transform, opacity'
                    }}
                  />
                )
              )}

              {/* Text Contrast Mask Overlay */}
              <div style={{
                position: 'absolute',
                inset: 0,
                background: hasMedia 
                  ? (isLight 
                      ? 'linear-gradient(90deg, rgba(255,255,255,0.96) 0%, rgba(255,255,255,0.82) 50%, rgba(255,255,255,0.3) 100%)'
                      : 'linear-gradient(90deg, rgba(9,9,11,0.95) 0%, rgba(9,9,11,0.78) 50%, rgba(9,9,11,0.4) 100%)')
                  : 'transparent'
              }} />
            </div>
          );
        })}

        <div key={`hero-content-${activeSlideIndex}`} style={{ maxWidth: '720px', position: 'relative', zIndex: 2, marginTop: '56px' }}>
          {/* Category Badge Pill */}
          {(() => {
            const rawCategory = activeHeroTryModel?.type_name || activeHeroTryModel?.category || activeSlide?.category;
            const badgeCategory = rawCategory ? getCategoryLabel(rawCategory) : tp('cat_recommended');
            const modelDisplayName = activeHeroTryModel?.name || activeHeroTryModel?.original_id || activeSlide?.try_model_id || '';
            return (
              <div className="hero-stagger-1" style={{ display: 'inline-flex', alignItems: 'center', gap: 6, marginTop: '-14px', marginBottom: 16 }}>
                <span style={{
                  fontSize: 11,
                  padding: '3px 10px',
                  borderRadius: 5,
                  background: isLight ? '#f1f5f9' : 'rgba(255,255,255,0.1)',
                  border: `1px solid ${isLight ? '#e2e8f0' : 'rgba(255,255,255,0.05)'}`,
                  color: isLight ? '#0f172a' : '#ffffff',
                  fontWeight: 500,
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 6
                }}>
                  <VideoCameraOutlined style={{ fontSize: 12, opacity: 0.8 }} />
                  {badgeCategory}
                </span>
                {modelDisplayName && (
                  <Text style={{ fontSize: 13, color: isLight ? '#64748b' : '#a1a1aa', fontWeight: 500 }}>{modelDisplayName}</Text>
                )}
              </div>
            );
          })()}

          {/* Large Title */}
          <div className="hero-stagger-2" style={{ marginBottom: 12, display: 'flex', alignItems: 'flex-start' }}>
            <Title level={1} style={{ 
              color: isLight ? '#0f172a' : '#ffffff', 
              fontSize: '38px', 
              fontWeight: 700, 
              margin: 0, 
              letterSpacing: '-1.2px',
              lineHeight: 1.18,
              display: '-webkit-box',
              WebkitLineClamp: 2,
              WebkitBoxOrient: 'vertical',
              overflow: 'hidden',
              wordBreak: 'break-word'
            }}>
              {activeSlide.title}
            </Title>
          </div>

          {/* Subtitle */}
          <div className="hero-stagger-3" style={{ marginBottom: 32, display: 'flex', alignItems: 'flex-start' }}>
            <Paragraph style={{ 
              color: isLight ? '#475569' : '#a1a1aa', 
              fontSize: '15px', 
              lineHeight: '1.5', 
              margin: 0,
              maxWidth: '640px',
              fontWeight: 400,
              display: '-webkit-box',
              WebkitLineClamp: 2,
              WebkitBoxOrient: 'vertical',
              overflow: 'hidden',
              wordBreak: 'break-word'
            }}>
              {activeSlide.description}
            </Paragraph>
          </div>

          {/* Buttons */}
          <div className="hero-stagger-4" style={{ minHeight: 40, display: 'flex', alignItems: 'center', marginTop: 66 }}>
            <Space size="middle">
              <Button 
                type="primary" 
                size="middle" 
                onClick={() => {
                  const targetModel = activeHeroTryModel || (validModels.length > 0 ? validModels[0] : null);
                  if (targetModel) {
                    onSelectModel(targetModel);
                  }
                }} 
                style={{ 
                  height: 40, 
                  padding: '0 24px', 
                  fontSize: 14, 
                  fontWeight: 600, 
                  borderRadius: 5,
                  background: isLight ? '#0f172a' : '#ffffff',
                  color: isLight ? '#ffffff' : '#000000',
                  border: 'none',
                  boxShadow: '0 4px 14px 0 rgba(0,0,0,0.1)'
                }}
              >
                {tp('try_now', '立即体验')}
              </Button>
              {activeSlide.docs_url && (
                <Button 
                  size="middle" 
                  onClick={() => window.open(activeSlide.docs_url, '_blank')}
                  style={{ 
                    height: 40, 
                    padding: '0 20px', 
                    fontSize: 14, 
                    fontWeight: 500, 
                    borderRadius: 5, 
                    background: isLight ? 'transparent' : 'rgba(255,255,255,0.05)', 
                    color: isLight ? '#475569' : '#e2e8f0', 
                    border: `1px solid ${isLight ? '#cbd5e1' : 'rgba(255,255,255,0.15)'}` 
                  }}
                >
                  {tp('view_docs', '查看文档')}
                </Button>
              )}
            </Space>
          </div>
        </div>

        {/* Interactive Carousel Indicator Segments at Bottom */}
        <div style={{ 
          display: 'flex', 
          alignItems: 'flex-end', 
          gap: 8, 
          marginTop: 20, 
          position: 'relative', 
          zIndex: 2,
          minHeight: 34
        }}>
          {heroSlidesList.map((slideItem: any, idx: number) => {
            const activeIdx = activeSlideIndex;
            const isActive = idx === activeIdx;
            const isHovered = hoveredIndicatorIdx === idx;

            let fillPercent = 0;
            if (idx < activeIdx) {
              fillPercent = 100;
            } else if (idx === activeIdx) {
              fillPercent = heroProgress;
            } else {
              fillPercent = 0;
            }

            const tryId = slideItem.try_model_id;
            const heroTryModel = tryId ? models.find(m => 
              m.mid === tryId || 
              m.id?.toString() === tryId || 
              m.original_id === tryId || 
              m.model_id === tryId || 
              m.name === tryId ||
              (m.original_id && m.original_id.toLowerCase() === tryId.toLowerCase()) ||
              (m.model_id && m.model_id.toLowerCase() === tryId.toLowerCase()) ||
              (m.name && m.name.toLowerCase() === tryId.toLowerCase())
            ) : null;

            const displayModelId = heroTryModel?.original_id || heroTryModel?.model_id || heroTryModel?.mid || tryId || slideItem.title || `Slide ${idx + 1}`;

            return (
              <div 
                key={slideItem.id ? `hero-ind-${slideItem.id}-${idx}` : `hero-ind-idx-${idx}`}
                onClick={() => handleHeroSlideSelect(idx)}
                onMouseEnter={() => setHoveredIndicatorIdx(idx)}
                onMouseLeave={() => setHoveredIndicatorIdx(null)}
                style={{ 
                  width: isActive ? 160 : 44, 
                  display: 'flex', 
                  flexDirection: 'column', 
                  alignItems: 'flex-start',
                  cursor: 'pointer',
                  position: 'relative',
                  transition: 'width 0.4s cubic-bezier(0.16, 1, 0.3, 1)',
                  flexShrink: 0
                }} 
                title={displayModelId}
              >
                {/* Purple Floating Badge: Absolute position floating above track on hover */}
                <div 
                  style={{
                    position: 'absolute',
                    bottom: 'calc(100% + 8px)',
                    left: 0,
                    zIndex: 10,
                    height: 26,
                    visibility: isHovered ? 'visible' : 'hidden',
                    opacity: isHovered ? 1 : 0,
                    transform: isHovered ? 'translateY(0)' : 'translateY(4px)',
                    transition: 'all 0.2s cubic-bezier(0.16, 1, 0.3, 1)',
                    background: '#7c3aed',
                    color: '#ffffff',
                    fontSize: 12,
                    fontWeight: 600,
                    padding: '3px 10px',
                    borderRadius: 4,
                    whiteSpace: 'nowrap',
                    maxWidth: '260px',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    boxShadow: '0 4px 14px rgba(124, 58, 237, 0.55)',
                    display: 'flex',
                    alignItems: 'center',
                    pointerEvents: 'none'
                  }}
                >
                  {displayModelId}
                </div>

                {/* Progress Bar Track - Sharp Square Rectangle */}
                <div 
                  style={{ 
                    height: 6, 
                    width: '100%', 
                    borderRadius: 0, 
                    background: isLight ? 'rgba(0, 0, 0, 0.15)' : 'rgba(255, 255, 255, 0.25)',
                    overflow: 'hidden',
                    position: 'relative'
                  }}
                >
                  <div 
                    style={{
                      height: '100%',
                      width: `${fillPercent}%`,
                      background: '#ffffff',
                      borderRadius: 0,
                      transition: idx === activeIdx ? 'none' : 'width 0.2s linear'
                    }}
                  />
                </div>
              </div>
            );
          })}
        </div>
      </div>

      {/* Main Content Area */}
      <div style={{ padding: '24px 48px' }}>
        
        {/* Search & Tag Filter Bar matching reference design */}
        <div style={{ marginBottom: 40 }}>
          {/* Integrated Outer Search Input Bar */}
          <div 
            className="mp-search-container"
            style={{ 
              display: 'flex', 
              alignItems: 'center', 
              gap: 12, 
              padding: '6px 8px 6px 16px', 
              background: isLight ? '#ffffff' : '#0e0e11', 
              border: `1px solid ${isLight ? '#cbd5e1' : 'rgba(255,255,255,0.08)'}`, 
              borderRadius: 5,
              boxShadow: isLight ? '0 2px 8px rgba(0,0,0,0.02)' : '0 4px 20px rgba(0,0,0,0.3)',
              transition: 'all 0.2s ease'
            }}
          >
            <SearchOutlined style={{ color: isLight ? '#94a3b8' : '#71717a', fontSize: 18 }} />
            <Input 
              placeholder={tp('search_placeholder_trending')}
              value={searchQuery}
              onChange={handleInputChange}
              onPressEnter={() => {
                if (searchQuery.trim()) handleAddSearchHistory(searchQuery.trim());
              }}
              variant="borderless"
              allowClear
              style={{
                flex: 1,
                background: 'transparent',
                color: c.text1,
                fontSize: 15,
                padding: '4px 0'
              }}
            />
            <Button 
              onClick={() => {
                if (searchQuery.trim()) handleAddSearchHistory(searchQuery.trim());
                onViewAllModels?.(searchQuery);
              }}
              style={{
                height: 38,
                padding: '0 20px',
                borderRadius: 5,
                background: isLight ? '#f1f5f9' : 'rgba(255,255,255,0.05)',
                color: isLight ? '#0f172a' : '#ffffff',
                border: `1px solid ${isLight ? '#cbd5e1' : 'rgba(255,255,255,0.12)'}`,
                fontWeight: 500,
                fontSize: 13,
                whiteSpace: 'nowrap'
              }}
            >
              {tp('view_all_models')}
            </Button>
          </div>

          {/* Search History Tags Below Search Bar (Only shown when searchHistory has records) */}
          {searchHistory.length > 0 && (() => {
            const colors = ['#10b981', '#8b5cf6', '#3b82f6', '#ec4899', '#06b6d4', '#38bdf8', '#f43f5e', '#64748b'];
            const icons = [<VideoCameraOutlined />, <ThunderboltOutlined />, <SlidersOutlined />, <AudioOutlined />, <PictureOutlined />, <SearchOutlined />, <FireOutlined />, <ReadOutlined />];

            const displayTags = searchHistory.map((text, i) => ({
              text,
              color: colors[i % colors.length],
              icon: icons[i % icons.length]
            }));

            return (
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginTop: 14, flexWrap: 'wrap' }}>
                <span style={{ fontSize: 13, color: isLight ? '#64748b' : '#71717a', fontWeight: 500 }}>
                  {tp('search_history')}
                </span>
                {displayTags.map((tagItem, idx) => (
                  <button
                    type="button"
                    key={`hist-${tagItem.text}-${idx}`}
                    onClick={() => {
                      setSearchQuery(tagItem.text);
                      handleAddSearchHistory(tagItem.text);
                    }}
                    style={{
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: 6,
                      padding: '4px 10px',
                      borderRadius: 5,
                      fontSize: 12,
                      fontWeight: 500,
                      background: isLight ? `${tagItem.color}0a` : `${tagItem.color}15`,
                      color: tagItem.color,
                      border: `1px dashed ${tagItem.color}60`,
                      cursor: 'pointer',
                      transition: 'all 0.2s cubic-bezier(0.4, 0, 0.2, 1)',
                      outline: 'none'
                    }}
                  >
                    <span style={{ fontSize: 12, display: 'inline-flex', alignItems: 'center' }}>{tagItem.icon}</span>
                    <span>{tagItem.text}</span>
                  </button>
                ))}
                <button
                  type="button"
                  onClick={handleClearSearchHistory}
                  style={{
                    fontSize: 12,
                    color: isLight ? '#94a3b8' : '#71717a',
                    background: 'transparent',
                    border: 'none',
                    cursor: 'pointer',
                    padding: '2px 6px',
                    marginLeft: 4
                  }}
                >
                  {tp('clear_history')}
                </button>
              </div>
            );
          })()}
        </div>

        {/* Model Labs / Provider Logo Classification Bar */}
        <div style={{ marginBottom: 40 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 14 }}>
            <div 
              style={{ display: 'inline-flex', alignItems: 'center', gap: 6, cursor: 'pointer' }}
              onClick={() => onViewAllModels?.()}
            >
              <Title level={4} style={{ margin: 0, fontSize: 20, fontWeight: 700, color: c.text1, display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                {tp('model_providers')} <RightOutlined style={{ fontSize: 14, color: isLight ? '#64748b' : '#a1a1aa' }} />
              </Title>
            </div>
          </div>

          <HorizontalCarousel isLight={isLight} itemCount={labsList.length} gap={8}>
            {labsList.map((lab, idx) => (
              <div
                key={`brand-lab-${(lab as any).id || (lab as any).key || lab.name || idx}-${idx}`}
                className="mp-lab-tile-card"
                onClick={() => {
                  if (onSelectProvider) {
                    onSelectProvider(lab);
                  } else if (onViewAllModels) {
                    onViewAllModels(lab.name);
                  }
                }}
                style={{
                  minWidth: 80,
                  width: 80,
                  height: 102,
                  borderRadius: 5,
                  background: isLight ? '#ffffff' : '#141417',
                  border: `1px solid ${isLight ? '#e5e7eb' : 'rgba(255,255,255,0.08)'}`,
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'center',
                  justifyContent: 'center',
                  gap: 6,
                  padding: '6px 4px',
                  cursor: 'pointer',
                  flexShrink: 0
                }}
                title={tp('view_provider_models', { name: localizeProviderName(lab.name, (lab as any).name_en) })}
              >
                {/* Logo Icon Container */}
                <div
                  style={{
                    width: 52,
                    height: 52,
                    background: 'transparent',
                    border: 'none',
                    boxShadow: 'none',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    overflow: 'hidden',
                    flexShrink: 0
                  }}
                >
                  {renderLabIcon(lab)}
                </div>

                {/* Provider Name */}
                <Text style={{
                  fontSize: 11,
                  fontWeight: 500,
                  color: isLight ? '#334155' : '#a1a1aa',
                  textAlign: 'center',
                  maxWidth: 74,
                  overflow: 'hidden',
                  textOverflow: 'ellipsis',
                  whiteSpace: 'nowrap'
                }}>
                  {localizeProviderName(lab.name, (lab as any).name_en)}
                </Text>
              </div>
            ))}
          </HorizontalCarousel>
        </div>

        {/* Real-time Search Results Area */}
        {isSearchActive ? (
          <div style={{ marginBottom: 48, animation: 'fadeIn 0.25s ease-in-out' }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, flexWrap: 'wrap', gap: 12 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                <Title level={3} style={{ margin: 0, fontSize: 20, fontWeight: 700, color: c.text1 }}>
                  {tp('search_results', '搜索结果')}
                </Title>
                <span style={{ 
                  fontSize: 13, 
                  padding: '2px 10px', 
                  borderRadius: 5, 
                  background: isLight ? '#e0f2fe' : 'rgba(56, 189, 248, 0.15)', 
                  color: isLight ? '#0284c7' : '#38bdf8',
                  fontWeight: 600
                }}>
                  {tp('items_count', { count: searchResults.length })}
                </span>
              </div>
              <Button 
                type="link" 
                size="small" 
                onClick={handleClearSearch}
                style={{ color: c.text3 }}
              >
                {tp('clear_search', '清空搜索')}
              </Button>
            </div>

            {searchResults.length > 0 ? (
              <div style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(auto-fill, minmax(260px, 1fr))',
                gap: 16,
                paddingBottom: 16
              }}>
                {searchResults.map((model, idx) => renderModelCard(model, idx, 'search'))}
              </div>
            ) : (
              <div style={{
                padding: '48px 24px',
                textAlign: 'center',
                background: isLight ? '#f8fafc' : '#141416',
                borderRadius: 5,
                border: `1px solid ${isLight ? '#e2e8f0' : '#222226'}`
              }}>
                <div style={{ fontSize: 40, marginBottom: 12, opacity: 0.6 }}>🔍</div>
                <Text strong style={{ fontSize: 16, display: 'block', color: c.text1, marginBottom: 8 }}>
                  {tp('no_search_results_for', { query: searchQuery })}
                </Text>
                <Text style={{ fontSize: 13, color: c.text3, display: 'block', marginBottom: 20 }}>
                  {tp('no_search_results_hint', '您可以尝试更换搜索词或查看全量模型列表。')}
                </Text>
                <Space>
                  <Button onClick={handleClearSearch}>{tp('clear_search', '清空搜索')}</Button>
                  <Button type="primary" onClick={() => onViewAllModels?.(searchQuery)}>{tp('view_all_models', '查看全部模型')}</Button>
                </Space>
              </div>
            )}
          </div>
        ) : (
          <>
            {/* Section 1: Trending Always Present */}
            <div style={{ marginBottom: 48 }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                  <Title level={3} style={{ margin: 0, fontSize: 22, fontWeight: 700, color: c.text1 }}>
                    {tp('popular_models', '热门模型')}
                  </Title>
                  <span style={{ 
                    fontSize: 12, 
                    padding: '3px 10px', 
                    borderRadius: 5, 
                    background: isLight ? '#e0f2fe' : 'rgba(56, 189, 248, 0.15)', 
                    color: isLight ? '#0284c7' : '#38bdf8',
                    fontWeight: 600
                  }}>
                    {tp('items_count', { count: trendingModels.length })}
                  </span>
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                  <Text style={{ fontSize: 13, color: c.text3 }}>{tp('per_section')}</Text>
                  <Select
                    value={perSectionLimit}
                    onChange={setPerSectionLimit}
                    size="small"
                    style={{ width: 84 }}
                    options={[
                      { value: 8, label: '8' },
                      { value: 12, label: '12' },
                      { value: 16, label: '16' },
                      { value: 24, label: '24' },
                      { value: 32, label: '32' },
                      { value: 'all', label: tp('filter_all') }
                    ]}
                  />
                </div>
              </div>
              
              <Paragraph style={{ color: c.text3, fontSize: 14, marginBottom: 20 }}>
                {tp('popular_models_desc', '当前平台最新上线与最受开发者欢迎的 AI 大模型。')}
              </Paragraph>

              {/* Horizontal Scroll List for Trending Models */}
              <HorizontalCarousel isLight={isLight} itemCount={trendingModels.length}>
                {trendingModels.map((model, idx) => renderModelCard(model, idx, 'trending'))}
              </HorizontalCarousel>
            </div>

            {/* Render Custom Configured Sections or Default Fallback Sections */}
            {customSections.length > 0 ? (
              customSections.map((sec: any, idx: number) => renderSectionTopic(sec, idx))
            ) : (
              <>
                {/* Section 2: Model Labs Fallback */}
                {renderSectionTopic({
                  id: 'fallback-labs',
                  title: tp('model_providers', '模型厂家'),
                  description: tp('fallback_labs_desc', '探索为平台提供强大底层支持的顶尖AI服务商'),
                  type: 'providers',
                  items: []
                }, 1)}

                {/* Section 3: Seedance 2.0 Fallback */}
                {renderSectionTopic({
                  id: 'fallback-seedance',
                  title: tp('fallback_seedance_title', 'Seedance 2.0 专题'),
                  description: tp('fallback_seedance_desc', '字节跳动推出的全新 SOTA 级视频生成模型，即刻体验惊艳的视听合一生成能力。'),
                  type: 'models',
                  items: []
                }, 2)}

                {/* Section 4: Grok Imagine Fallback */}
                {renderSectionTopic({
                  id: 'fallback-grok',
                  title: tp('fallback_grok_title', 'Grok Imagine 专题'),
                  description: tp('fallback_grok_desc', '由 xAI 强力驱动的高品质视频、图像与自然语音生成模型系列。'),
                  type: 'models',
                  items: []
                }, 3)}
              </>
            )}
          </>
        )}

      </div>
    </div>
  );
};

export default TrendingPage;
