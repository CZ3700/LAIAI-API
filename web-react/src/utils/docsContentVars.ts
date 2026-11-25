/** 文档树节点（与 DocsApi / Portal 公共字段对齐） */
export type DocsTreeNode = {
  id: number;
  slug?: string;
  is_dir: boolean;
  children?: DocsTreeNode[];
};

function findDocBySlug(nodes: DocsTreeNode[], slug: string): DocsTreeNode | null {
  for (const n of nodes) {
    if (!n.is_dir && n.slug === slug) return n;
    if (n.children?.length) {
      const found = findDocBySlug(n.children, slug);
      if (found) return found;
    }
  }
  return null;
}

function findParentSlug(nodes: DocsTreeNode[], targetId: number): string | null {
  for (const node of nodes) {
    if (node.children?.some((c) => c.id === targetId)) {
      return node.slug || null;
    }
    if (node.children?.length) {
      const found = findParentSlug(node.children, targetId);
      if (found) return found;
    }
  }
  return null;
}

const idToDocSlug = (docId: number) => `doc${String(docId).padStart(4, '0')}`;

/** 构建错误码专页等内置 slug 文档的站内链接 */
export function buildDocHref(
  tree: DocsTreeNode[],
  articleSlug: string,
  basePath: string,
  opts?: { numericId?: boolean },
): string {
  const doc = findDocBySlug(tree, articleSlug);
  if (!doc) return '#';
  if (opts?.numericId) return `${basePath}/${doc.id}`;
  const parentSlug = findParentSlug(tree, doc.id);
  const leaf = idToDocSlug(doc.id);
  return parentSlug ? `${basePath}/${parentSlug}/${leaf}` : `${basePath}/${leaf}`;
}

export function applyDocsContentVars(
  content: string,
  vars: Record<string, string>,
): string {
  let out = content;
  for (const [key, val] of Object.entries(vars)) {
    out = out.replaceAll(`{{${key}}}`, val);
  }
  return out;
}

const PRESET_DEFAULT_TITLES: Record<string, string[]> = {
  quickstart: ['快速开始', '快速開始', 'Quick Start', 'Quickstart', 'クイックスタート', '빠른 시작', 'Bắt đầu nhanh'],
  examples: ['常用调用示例', '调用示例', '呼叫範例', 'Common Usage Examples', 'Common Call Examples', 'Examples', 'よく使う呼び出し例', '常用呼び出し例', 'コード例', '자주 쓰는 호출 예제', '공통 호출 예시', '호출 예제', 'Ví dụ gọi phổ biến', 'Ví dụ'],
  openai: ['OpenAI 兼容协议', 'OpenAI 协议', 'OpenAI 協議', 'OpenAI Compatible Protocol', 'OpenAI Compatible', 'OpenAI 互換プロトコル', 'OpenAI 互換', 'OpenAI 호환 프로토콜', 'OpenAI 호환', 'Giao thức tương thích OpenAI', 'Chuẩn OpenAI'],
  'volcengine-ark': ['火山引擎(方舟)原生协议', '火山方舟', 'Volcengine (Ark) Native Protocol', 'Volcengine Ark Protocol', 'Volcengine Ark', 'Volcengine (Ark) ネイティブプロトコル', 'Volcengine Ark プロトコル', 'Volcengine (Ark) 네이티브 프로토콜', 'Volcengine Ark 프로토콜', 'Giao thức gốc Volcengine (Ark)', 'Giao thức Volcengine Ark'],
  'ali-kling': ['厂商原生协议(百炼/可灵/MiniMax/Vidu)', '厂商原生接口', '廠商原生介面', 'Vendor Native Protocols (DashScope/Kling/MiniMax/Vidu)', 'Native APIs', 'ベンダーネイティブプロトコル (DashScope/Kling/MiniMax/Vidu)', '各社ネイティブプロトコル (DashScope/Kling/MiniMax/Vidu)', 'ネイティブ API', '벤더 네이티브 프로토콜 (DashScope/Kling/MiniMax/Vidu)', '공급업체 네이티브 프로토콜 (DashScope/Kling/MiniMax/Vidu)', '네이티브 API', 'Giao thức gốc nhà cung cấp (DashScope/Kling/MiniMax/Vidu)', 'Giao thức gốc của nhà cung cấp (DashScope/Kling/MiniMax/Vidu)', 'API gốc hãng'],
  'google-anthropic': ['Google与Anthropic协议', 'Google & Anthropic', 'Google & Anthropic Protocols', 'Google & Anthropic Protocol', 'Google & Anthropic プロトコル', 'Google & Anthropic 프로토콜', 'Giao thức Google & Anthropic'],
  errors: ['错误码排查', '错误码', '錯誤碼', 'Error Code Troubleshooting', 'Troubleshooting', 'Error Codes', 'エラーコードとトラブルシューティング', 'トラブルシューティング', 'エラーコード', '오류 코드 및 문제 해결', '문제 해결', '오류 코드', 'Mã lỗi & Khắc phục sự cố', 'Khắc phục sự cố', 'Mã lỗi'],
  'site-api': ['站点功能API', '平台 API', 'Site Management API', 'Site Functions API', 'Platform API', 'サイト管理 API', 'サイト機能 API', 'プラットフォーム API', '사이트 기능 API', '플랫폼 API', 'API chức năng trang', 'API chức năng trang web', 'API nền tảng'],
  'best-practices': ['最佳实践', '最佳實踐', 'Best Practices', 'ベストプラクティス', '모범 사례', '베스트 프랙티스', 'Thực hành tốt nhất'],
  'auth-guide': ['快速开始与鉴权说明', '快速开始与鉴权', '快速開始與鑑權', 'Quick Start & Authentication', 'Quickstart & Auth', 'クイックスタートと認証ガイド', 'クイックスタートと認証仕様', '認証とクイックスタート', '빠른 시작 및 인증 안내', '빠른 시작 및 인증 설명', '인증 및 빠른 시작', 'Hướng dẫn bắt đầu nhanh & Xác thực', 'Bắt đầu nhanh và Hướng dẫn xác thực', 'Xác thực & Bắt đầu'],
  endpoints: ['开放端点一览表', '接口端点', '介面端點', 'Available Endpoints Overview', 'API Endpoints Overview', 'Endpoints', '利用可能なエンドポイント一覧', '公開エンドポイント一覧', 'エンドポイント', '오픈 엔드포인트 목록', '공개 엔드포인트 목록', '엔드포인트', 'Tổng quan các Endpoint mở', 'Danh sách các điểm cuối mở', 'Điểm cuối (Endpoints)'],
  'codex-claude-guide': ['Codex & Claude Code 接入指南', 'Codex与Claude Code接入指南', 'Codex与Claude Code', 'Codex 与 Claude Code', 'Codex 與 Claude Code', 'Codex & Claude Code Integration Guide', 'Codex & Claude Code', 'Codex & Claude Code 接続ガイド', 'Codex & Claude Code 連携ガイド', 'Codex & Claude Code 연동 가이드', 'Hướng dẫn tích hợp Codex & Claude Code'],
  'workbuddy-guide': ['WorkBuddy 接入指南', 'WorkBuddy Integration Guide', 'WorkBuddy 連携ガイド', 'WorkBuddy 연동 가이드', 'Hướng dẫn tích hợp WorkBuddy'],
  chat: ['聊天 Chat', '聊天 Chat（OpenAI 兼容）', '对话补全 (Chat)', '對話補全 (Chat)', 'Chat Completions', 'Chat Completions (Chat)', 'Chat (OpenAI Compatible)', 'Chat (OpenAI)'],
  'chat-anthropic': ['聊天 Anthropic', '聊天 Anthropic（原生 Messages）', '对话消息 (Anthropic)', '對話訊息 (Anthropic)', 'Anthropic Messages', 'Chat Messages (Anthropic)', 'Chat Anthropic', 'Chat Anthropic (Native Messages)', 'Messages (Anthropic)'],
  'gpt-image': ['gpt-image 图像生成', 'GPT-Image 图像生成与编辑接入指南', 'GPT 图像生成', 'GPT 圖像生成', 'gpt-image Generation', 'GPT-Image Generation', 'GPT Image Generation', 'GPT-Image Generation and Editing Integration Guide', 'GPT Images', 'GPT 画像生成', 'GPT-Image 画像生成・編集 連携ガイド', 'GPT 이미지 생성', 'GPT-Image 이미지 생성 및 편집 연동 가이드', 'Tạo ảnh GPT', 'Hướng dẫn tích hợp Tạo và Chỉnh sửa hình ảnh GPT-Image'],
  'google-image': ['Google 图像生成', 'gemini-3.1 图像生成', '谷歌 gemini-3.1 图像生成接入指南', 'Gemini 图像生成', 'Gemini 圖像生成', 'Google Image Generation', 'Google gemini-3.1 Generation', 'Google Gemini-3.1 Image Generation', 'Gemini Image Generation', 'Google Images'],
  'volc-image': ['Seedream图像生成', 'Seedream圖像生成', 'seedream图像生成', 'seedream圖像生成', '火山方舟 图像生成', 'doubao-seedream 图像生成', 'doubao-seedream 图像生成接入指南', 'Seedream 图像生成', '火山方舟图像', '火山方舟圖像', 'Volcengine Image Generation', 'Volcengine doubao-seedream Generation', 'doubao-seedream Image Generation Integration Guide', 'Seedream Image Generation', 'Volcengine Images'],
  'volc-video': ['Seedance视频生成', 'Seedance影片生成', 'seedance视频生成', 'seedance影片生成', '火山方舟 视频生成', '火山引擎(方舟)视频生成接入指南', 'Seedance 视频生成', '火山方舟视频', '火山方舟影片', 'Volcengine Video Generation', 'Volcengine Seedance Video Generation', 'Seedance Video Generation', 'Volcengine Video'],
  'kling-image': ['Kling图像生成', 'Kling圖像生成', '可灵 图像生成', 'Kling-v3 图像生成', '可灵 Kling-v3 图像生成接入指南', '可灵图像生成', '可靈圖像生成', 'Kling Image Generation', 'Kling-v3 Image Generation', 'Kling-v3 Image Generation Example', 'Kling Images'],
  'kling-video': ['Kling视频生成', 'Kling影片生成', '可灵 视频生成', 'Kling-v3 视频生成', '可灵 Kling 视频生成接入指南', '可灵视频生成', '可靈影片生成', 'Kling Video Generation', 'Kling-v3 Video Generation', 'Kling Video Generation Guide', 'Kling Video'],
  'wan-image': ['Qwen图片生成', 'Qwen圖片生成', '阿里百炼 图像生成', '千问 图像生成', '通义万相图像', '通義萬相圖像', 'DashScope Image Generation', 'Alibaba Qwen Image Generation', 'Qwen Image Generation', 'DashScope Images'],
  'wan-video': ['WAN视频生成', 'WAN影片生成', '阿里百炼 视频生成', '万相 视频生成', '通义万相视频', '通義萬相影片', 'DashScope Video Generation', 'Alibaba Wan Video Generation', 'WAN Video Generation', 'DashScope Video'],
  'vidu-video': ['Vidu视频生成', 'Vidu影片生成', 'Vidu 视频生成', 'Vidu Q4 视频生成', 'Vidu Q4 视频生成接入指南', 'Vidu 影片生成', 'Vidu Video Generation', 'Vidu Q4 Video Generation', 'Vidu Q4 Video Generation Integration Guide', 'Vidu Video'],
  'minimax-video': ['MiniMax视频生成', 'MiniMax影片生成', 'MiniMax 视频生成', 'MiniMax 视频生成接入指南', 'MiniMax 影片生成', 'MiniMax Video Generation', 'MiniMax Video Generation Example', 'MiniMax Video'],
  'chat-completions': ['聊天与响应(Completions&Responses)', '聊天与响应 (Completions & Responses)', '对话与响应 (Chat)', '對話與回應 (Chat)', 'Chat & Responses (Completions & Responses)', 'Chat & Responses', 'チャットと応答 (Completions & Responses)', 'チャットと応答', '채팅 및 응답 (Completions & Responses)', '채팅 및 응답', 'Trò chuyện & Phản hồi (Completions & Responses)', 'Trò chuyện và Phản hồi (Completions & Responses)', 'Chat & Phản hồi'],
  images: ['图像生成与编辑(Images)', '图像生成与编辑 (Images)', '图像生成与编辑', '圖像生成與編輯', 'Image Generation & Editing (Images)', 'Images', '画像生成と編集 (Images)', '画像生成・編集', '이미지 생성 및 편집 (Images)', '이미지 생성 및 편집', 'Tạo & Chỉnh sửa hình ảnh (Images)', 'Tạo và chỉnh sửa hình ảnh (Images)', 'Tạo & Sửa ảnh'],
  video: ['视频生成接口(Video)', '视频生成接口 (Video)', '视频生成 (Video)', '影片生成 (Video)', 'Video Generation API (Video)', 'Video Generation (Video)', 'Video Generation', '動画生成 API (Video)', 'ビデオ生成 (Video)', '動画生成 (Video)', '비디오 생성 API (Video)', '비디오 생성 인터페이스 (Video)', '비디오 생성', 'API tạo video (Video)', 'Giao diện tạo video (Video)', 'Tạo video'],
  'text-to-speech': ['语音合成(Text-to-Speech)', '语音合成 (Text-to-Speech)', '语音合成 (TTS)', '語音合成 (TTS)', 'Text-to-Speech (TTS)', 'Audio & TTS', '音声合成 (Text-to-Speech)', '音声合成 (TTS)', '음성 합성 (Text-to-Speech)', '음성 합성 (TTS)', 'Chuyển văn bản thành giọng nói (Text-to-Speech)', 'Tổng hợp giọng nói (Text-to-Speech)', 'Giọng nói (TTS)'],
  'balance-models': ['余额与可用模型查询(Info)', '余额与可用模型查询 (Info)', '余额与模型查询', '餘額與模型查詢', 'Balance & Available Models Query (Info)', 'Quota & Available Models (Info)', 'Models & Balance', '残高・利用可能モデル照会 (Info)', '残高と利用可能なモデルの照会 (Info)', '残高・モデル照会', '잔액 및 사용 가능한 모델 조회 (Info)', '잔액 및 모델 조회', 'Truy vấn số dư & Mô hình khả dụng (Info)', 'Tra cứu số dư và mô hình khả dụng (Info)', 'Số dư & Mô hình'],
  'volcengine-api': ['火山引擎(方舟)原生API接入', '火山引擎 (方舟) 原生 API 接入', '火山方舟原生接口', '火山方舟原生介面', 'Volcengine (Ark) Native API Integration', 'Volcengine Ark Native API', 'Ark Native API', 'Volcengine (Ark) ネイティブ API 接続', 'Volcengine Ark ネイティブ API 接続', 'Ark ネイティブ API', 'Volcengine (Ark) 네이티브 API 연동', 'Volcengine Ark 기본 API 접속', 'Ark 네이티브 API', 'Tích hợp API gốc Volcengine (Ark)', 'Kết nối API gốc Volcengine Ark', 'API gốc Ark'],
  'volcengine-mediakit': ['火山MediaKit媒体处理增强', '火山 MediaKit 媒体处理增强', '火山 MediaKit', 'Volcengine MediaKit Media Processing Enhancement', 'Volcengine MediaKit Enhancement', 'Volcengine MediaKit', 'Volcengine MediaKit メディア処理強化', 'Volcengine MediaKit メディア処理機能の强化', 'Volcengine MediaKit 미디어 처리 강화', 'Volcengine MediaKit 미디어 처리 향상', 'Tăng cường xử lý đa phương tiện Volcengine MediaKit', 'Tăng cường xử lý phương tiện Volcengine MediaKit'],
  'ali-dashscope': ['阿里百炼(DashScope)原生接入', '阿里百炼 (DashScope) 原生接入', '阿里百炼 (DashScope)', '阿里百煉 (DashScope)', 'Alibaba Bailian (DashScope) Native Integration', 'Ali DashScope Native API', 'Alibaba DashScope', 'Alibaba Bailian (DashScope) ネイティブ接続', 'アリ百煉 (DashScope) ネイティブ接続', 'Alibaba Bailian (DashScope) 네이티브 연동', '알리 백련 (DashScope) 기본 접속', 'Tích hợp gốc Alibaba Bailian (DashScope)', 'Kết nối gốc Ali DashScope'],
  'kling-ai': ['可灵AI(Kling)原生接入', '可灵 AI (Kling) 原生接入', '可灵 AI (Kling)', '可靈 AI (Kling)', 'Kling AI Native Integration', 'Kling AI Native Protocol', 'Kling AI', 'Kling AI ネイティブ接続', '可霊 AI (Kling) ネイティブプロトコル仕様', 'Kling AI 네이티브 연동', '클링 AI (Kling) 기본 프로토콜 설명', 'Tích hợp gốc Kling AI', 'Giao thức gốc Kling AI'],
  'minimax-native': ['MiniMax 视频原生接入', 'MiniMax 原生接口', 'MiniMax 原生介面', 'MiniMax Video Native Integration', 'MiniMax Native Video API', 'MiniMax Native', 'MiniMax 動画ネイティブ接続', 'MiniMax ネイティブ', 'MiniMax 비디오 네이티브 연동', 'MiniMax 네이티브', 'Tích hợp gốc Video MiniMax'],
  'vidu-native': ['Vidu 官方原生接入', 'Vidu 原生接口', 'Vidu 原生介面', 'Vidu Official Native Integration', 'Vidu Native Protocol Integration', 'Vidu Native', 'Vidu 公式ネイティブ接続', 'Vidu ネイティブ', 'Vidu 공식 네이티브 연동', 'Vidu 네이티브', 'Tích hợp gốc chính thức Vidu'],
  'google-gemini': ['GoogleGemini原生接入', 'Google Gemini 原生接入', 'Google Gemini', 'Google Gemini Native Integration', 'Google Gemini Native API', 'Google Gemini ネイティブ接続', 'Google Gemini 네이티브 연동', 'Google Gemini 기본 접속', 'Tích hợp gốc Google Gemini', 'Kết nối gốc Google Gemini'],
  'anthropic-claude': ['AnthropicClaude原生接入', 'Anthropic Claude 原生接入', 'Anthropic Claude', 'Anthropic Claude Native Integration', 'Anthropic Claude Native API', 'Anthropic Claude ネイティブ接続', 'Anthropic Claude 네이티브 연동', 'Anthropic Claude 기본 접속', 'Tích hợp gốc Anthropic Claude', 'Kết nối gốc Anthropic Claude'],
  'error-codes': ['网关常见错误码与排查', '网关错误码', '閘道錯誤碼', 'Common Gateway Error Codes & Troubleshooting', 'Gateway Error Codes & Troubleshooting', 'Gateway Error Codes', 'ゲートウェイの一般的なエラーコードと対処法', 'ゲートウェイの一般的なエラーコードとトラブルシューティング', 'ゲートウェイエラー', '게이트웨이 주요 오류 코드 및 해결 방법', '게이트웨이 공통 오류 코드 및 문제 해결', '게이트웨이 오류 코드', 'Mã lỗi cổng thường gặp & Cách khắc phục', 'Mã lỗi cổng thông tin phổ biến và khắc phục sự cố', 'Mã lỗi Gateway'],
  'site-api-overview': ['站点API概述与认证方式', '站点 API 概述与认证方式', '概述与认证', '概述與認證', 'Site API Overview & Authentication', 'Overview & Auth', 'サイト API の概要と認証方式', 'サイト API 概要と認証', '概要と認証', '사이트 API 개요 및 인증 방식', '사이트 API 개요 및 인증', '개요 및 인증', 'Tổng quan API trang & Phương thức xác thực', 'Tổng quan về API Trang web & Xác thực', 'Tổng quan & Xác thực'],
  'site-api-user': ['用户与钱包API', '用户资料与钱包API', '账户与钱包', '帳戶與錢包', 'User Profile & Wallet API', 'User Profile & Wallet APIs', 'Account & Wallet', 'ユーザープロフィールとウォレット API', 'アカウントとウォレット', '사용자 프로필 및 지갑 API', '계정 및 지갑', 'API Hồ sơ người dùng & Ví', 'Hồ sơ Người dùng & API Ví', 'Tài khoản & Ví'],
  'site-api-tokens-logs': ['令牌与使用日志API', 'API令牌与日志查询API', '令牌与日志', '權杖與日誌', 'API Tokens & Logs Query API', 'API Tokens & Usage Logs APIs', 'Tokens & Logs', 'API トークンとログ照会 API', 'API トークンと使用ログ API', 'トークンとログ', 'API 토큰 및 로그 조회 API', 'API 토큰 및 사용 로그 API', '토큰 및 로그', 'API Truy vấn Token & Nhật ký', 'API Quản lý Token & Nhật ký Sử dụng', 'Token & Nhật ký'],
  'site-api-models': ['模型列表查询', '模型列表查詢', '可用模型列表', 'Models List', 'Available Models List API', 'モデル一覧照会', '利用可能モデル一覧', '모델 목록 조회', 'Danh sách Mô hình'],
  'deploy-install': ['部署与安装指南', '部署与安装', '部署與安裝', 'Deployment & Installation Guide', 'Deployment', 'デプロイ＆インストールガイド', 'デプロイと導入', '배포 및 설치 가이드', '배포 및 설치', 'Hướng dẫn triển khai & cài đặt', 'Triển khai & Cài đặt'],
  'deploy-quickstart': ['一键快速部署指南', '快速部署'],
  'deploy-maintenance': ['版本升级与日常运维备份指南', '升级与运维', '升級與運維'],
  'model-management-guide': ['模型管理与计费配置指南', '模型与计费配置', '模型與計費配置'],
  'channel-management-guide': ['渠道管理与负载均衡配置指南', '渠道与负载均衡', '渠道與負載均衡'],
  business: ['商务合作', '商務合作'],
};

const normalizeTitleForMatch = (s: string) =>
  s.replace(/^\d+[\s.\-_、]+/, '').replace(/\s+/g, '').toLowerCase();

export function formatPresetDocTitle(
  title: string,
  slug: string | undefined,
  docsT: (key: string, opts?: any) => string,
): string {
  if (!title) return '';
  const stripped = title.replace(/^\d+[\s.\-_、]+/, '').replace(/\s+/g, ' ').trim();
  if (slug && PRESET_DEFAULT_TITLES[slug]) {
    const norm = normalizeTitleForMatch(stripped);
    const isDefault = PRESET_DEFAULT_TITLES[slug].some(
      (candidate) => normalizeTitleForMatch(candidate) === norm,
    );
    if (isDefault) {
      const translated = docsT(`preset_slugs.${slug}`, { defaultValue: '' });
      if (translated && translated !== `preset_slugs.${slug}`) {
        return translated;
      }
    }
  }
  return stripped;
}

export function formatPresetCategoryName(
  name: string,
  docsT: (key: string, opts?: any) => string,
): string {
  if (!name) return '';
  const norm = name.replace(/\s+/g, '').toLowerCase();
  if (norm === 'api参考' || norm === 'api參考' || norm === 'apireference') {
    return docsT('preset_portal_categories.api_reference', { defaultValue: name.trim() });
  }
  if (norm === '使用指南' || norm === 'userguide' || norm === 'guides') {
    return docsT('preset_portal_categories.user_guide', { defaultValue: name.trim() });
  }
  if (norm === '商务合作' || norm === '商務合作' || norm === 'business') {
    return docsT('preset_portal_categories.business', { defaultValue: name.trim() });
  }
  return name.trim();
}
