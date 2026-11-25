export const DEFAULT_TOS_ZH = `<h2>服务条款 (Terms of Service)</h2>
<p>欢迎使用本平台服务。本服务条款（以下简称“本条款”）是由用户（以下简称“您”）与本平台运营方就本平台提供的各项服务所订立的具有法律约束力的协议。在注册、登录或使用本平台服务之前，请您务必认真阅读并充分理解本条款的全部内容。当您使用本平台提供的任何服务时，即表示您已阅读、理解并同意接受本条款的全部约束。</p>

<h3>一、服务内容与范围</h3>
<p>1. 本平台致力于为开发者与企业提供一站式人工智能（AI）接口中继分发、网关调度、API 令牌管理、在线创作（Playground）以及相关配套工具与技术支持服务。</p>
<p>2. 平台支持多模型统一接入与智能路由，包括但不限于大语言模型（LLM）、图像生成、视频生成、语音合成及自定义工作流接口。</p>
<p>3. 平台有权根据业务发展、技术升级或政策变化，对现有服务内容进行调整、升级、暂停或终止，并在合理时间内通过站内公告或系统消息予以通知。</p>

<h3>二、账户注册与安全规范</h3>
<p>1. 您在注册账户时应提供真实、准确、有效的注册信息（如邮箱地址），并对账户信息的真实性负责。</p>
<p>2. 您应妥善保管自己的账户凭据及在平台内创建的所有 API 令牌（API Keys / Access Tokens）。您对利用该账户或 API 令牌所进行的一切调用、操作及产生的所有结果承担完全责任。</p>
<p>3. 若您发现任何未经授权使用账户或 API 令牌的安全漏洞，应立即通知平台管理员并及时在控制台重置或删除相关令牌。</p>

<h3>三、服务使用规范与合规承诺</h3>
<p>1. 您承诺严格遵守所在国家或地区适用的法律法规、行业规范及公共道德准则，不得利用本平台从事任何违法、侵权、有害或危害网络安全的活动。</p>
<p>2. <strong>严禁行为包括但不限于：</strong></p>
<ul>
  <li>输入、生成或传播含有违法违规、暴力恐吓、淫秽色情、虚假谣言、侵犯他人隐私或知识产权的内容；</li>
  <li>对平台基础设施或上游接口实施恶意扫描、逆向工程、DDoS 攻击、高频恶意刷量等破坏系统稳定性的行为；</li>
  <li>绕过平台的额度限制、并发限制或安全风控策略；</li>
  <li>转售、滥用未经授权的系统资源或从事侵犯第三方合法权益的行为。</li>
</ul>
<p>3. 如用户违反上述规定，平台有权不经事先通知即采取限制调用速率、冻结 API 令牌、扣减违规所得、暂停或永久封禁账户等措施，并保留依法追究法律责任的权利。</p>

<h3>四、计费、充值与消费规则</h3>
<p>1. 本平台服务采用基于实际调用量（如 Token 数、请求次数、计算时长等）的计费模式，具体费率以平台各模型和服务的实时定价规则为准。</p>
<p>2. 您可通过平台支持的在线充值方式或兑换码（Redemption Code）为账户充值。充值金额将计入您的账户可用余额。</p>
<p>3. 除法律法规另有强制性规定或平台明确约定的特殊情形外，账户充值与兑换码一旦核销成功即视为消费履行，原则上不支持退款、提现或转让。</p>

<h3>五、免责声明与服务限制</h3>
<p>1. <strong>第三方模型输出免责：</strong> 本平台作为技术中继与调度网关，所接入的各 AI 模型由独立第三方提供商运行。平台不对任何模型生成内容的真实性、准确性、完整性、合法性或特定用途适用性作任何明示或暗示的保证。生成内容仅供参考，不代表平台立场。</p>
<p>2. <strong>服务可用性保障：</strong> 平台将尽合理商业努力保障系统的高可用性与稳定性。但对于因上游服务商接口波动、网络故障、设备维护、不可抗力等非平台可控原因造成的服务中断、延迟或数据异常，平台不承担违约及赔偿责任。</p>

<h3>六、协议修改与解释权</h3>
<p>平台保留随时修改本服务条款的权利。修改后的条款一旦公布即生效。若您在条款修改后继续使用本服务，即视为您已接受修改后的条款。本条款的最终解释权归平台所有。</p>`;

export const DEFAULT_PRIVACY_ZH = `<h2>隐私协议 (Privacy Policy)</h2>
<p>本平台非常重视用户的个人信息与隐私安全。本隐私协议（以下简称“本协议”）详细阐述了在您注册、登录、调用 API 及使用本平台各项功能时，我们如何收集、使用、存储、保护及处理您的个人信息和数据。请您在使用我们的服务前仔细阅读并确认已充分理解本协议。</p>

<h3>一、我们收集的信息</h3>
<p>为了向您提供稳定、安全的人工智能 API 中继与管理服务，我们仅收集实现业务功能所必需的最少信息：</p>
<ul>
  <li><strong>账户与身份信息：</strong> 注册及登录时提供的用户名、电子邮箱地址、认证凭据或经您授权的第三方登录信息（如 GitHub、Google、微信等授权标识）。</li>
  <li><strong>API 令牌与配置：</strong> 您在控制台创建的 API 令牌（API Keys）、调用权限配置、自定义模型分组、IP 白名单及限流策略。</li>
  <li><strong>调用与日志数据：</strong> 为保障服务调用链路追踪、计费结算及安全审计，系统会自动记录 API 请求时间戳、调用的模型名称、请求与响应消耗的 Token 数量/额度、客户端 IP 地址、HTTP 状态码及响应耗时。</li>
  <li><strong>交易与资产信息：</strong> 账户余额充值记录、订单流水、兑换码核销记录、消费明细以及您在创作中心或素材库中主动上传和保存的资源。</li>
</ul>

<h3>二、信息的使用目的</h3>
<p>我们收集的信息将严格用于以下用途：</p>
<ol>
  <li><strong>提供与执行服务：</strong> 完成 API 请求的身份鉴权、实时中继转发、响应流推送及创作工具交互；</li>
  <li><strong>计费与账单核算：</strong> 准确计算每次调用的 Token 消耗与费用扣除，维护账户余额真实准确；</li>
  <li><strong>安全防护与防滥用：</strong> 识别异常高频调用、恶意刷量、未授权访问等安全威胁，执行速率限制与风控策略；</li>
  <li><strong>系统运维与优化：</strong> 分析接口调用性能、可用性指标，排查系统故障并改进产品体验；</li>
  <li><strong>客户服务与合规：</strong> 处理您的技术支持工单，并在符合法律法规要求的前提下配合监管部门的必要安全审计。</li>
</ol>

<h3>三、Prompt 与生成内容的数据隐私</h3>
<p>1. 您通过 API 接口或创作中心（Playground）提交的 Prompt（提示词）、上下文文本、图片或音频数据，仅用于实时转发给您所选定的上游 AI 模型供应商进行推理处理。</p>
<p>2. 本平台<strong>不会</strong>将您的提示词和业务内容出售给任何第三方，亦不会将您的私有业务数据用于未经许可的模型训练或商业挖掘。</p>
<p>3. 系统仅在必要范围内对请求进行安全合规过滤或为调试排错提供短期技术日志，日志在保存周期结束后将自动清理。</p>

<h3>四、信息的共享、转让与披露</h3>
<p>1. <strong>必要服务共享：</strong> 为完成您发起的 AI 调用请求，您的请求数据将传输给对应的上游第三方模型提供商（如 OpenAI、Anthropic、Google、智谱、阿里云等）。该等提供商将依据其各自的隐私政策和数据保护协议处理请求。</p>
<p>2. 除上述必要服务履行、获得您的明确同意，或根据法律法规、司法裁决、政府监管机构的法定强制要求外，我们不会向任何无关第三方共享、出售或披露您的个人信息。</p>

<h3>五、数据安全与保护措施</h3>
<p>1. 我们采用行业标准的安全技术措施（如 HTTPS/TLS 全链路传输加密、数据库敏感字段哈希存储、多层防火墙与访问隔离机制）保护您的数据免受未经授权的访问、泄露、篡改或损毁。</p>
<p>2. 用户的密码均经过强加密哈希算法（如 bcrypt/argon2）存储，系统管理员亦无法明文查看您的密码。</p>

<h3>六、您的权利与联系我们</h3>
<p>1. 您有权随时在管理面板中查看、修改您的个人资料，随时生成、禁用或删除您的 API 令牌，以及导出调用明细与账单记录。</p>
<p>2. 如果您对本隐私协议有任何疑问、意见或希望申请注销账户及删除关联数据，请通过平台官方工单系统或管理员联络渠道与我们取得联系。</p>`;

export const DEFAULT_TOS_EN = `<h2>Terms of Service</h2>
<p>Welcome to our platform. These Terms of Service ("Terms") constitute a legally binding agreement between you ("User", "you") and the operator of this platform ("we", "us", "our"). By registering, accessing, calling our APIs, or otherwise using any services provided by this platform, you acknowledge that you have read, understood, and agreed to be bound by all terms and conditions set forth herein.</p>

<h3>1. Services Provided</h3>
<p>1.1. Our platform provides unified Artificial Intelligence (AI) API relay, intelligent dispatching, API token management, online creative playground, asset management, and related technical infrastructure services.</p>
<p>1.2. The platform aggregates multiple upstream AI models, including Large Language Models (LLMs), image generation, video generation, voice synthesis, and custom workflow relays.</p>
<p>1.3. We reserve the right to modify, upgrade, suspend, or discontinue any feature of the services at our discretion, with reasonable prior notice provided through the platform where feasible.</p>

<h3>2. Account Registration and Security</h3>
<p>2.1. You must provide accurate, current, and complete information during registration and maintain the confidentiality of your account credentials.</p>
<p>2.2. You are solely responsible for all actions, API invocations, and billing incurred under your account and associated API Keys (Access Tokens), whether authorized by you or not.</p>
<p>2.3. If you suspect any compromise, leakage, or unauthorized use of your credentials or API keys, you must immediately revoke or reset the affected keys via the dashboard and notify platform administrators.</p>

<h3>3. Acceptable Use Policy</h3>
<p>3.1. You agree to comply with all applicable local, national, and international laws, regulations, and industry standards when using our services.</p>
<p>3.2. <strong>Prohibited activities include, but are not limited to:</strong></p>
<ul>
  <li>Submitting, generating, or distributing unlawful, violent, sexually explicit, defamatory, fraudulent, or infringing content;</li>
  <li>Conducting cyber attacks, unauthorized vulnerability scanning, reverse engineering, DDoS attacks, or excessive abusive scraping against our infrastructure or upstream providers;</li>
  <li>Circumventing platform rate limits, quota controls, or security monitoring mechanisms;</li>
  <li>Reselling, sublicensing, or exploiting platform resources without explicit written authorization.</li>
</ul>
<p>3.3. Any breach of this policy may result in immediate rate throttling, API key suspension, balance forfeiture, or permanent account termination without prior notice, along with potential legal action.</p>

<h3>4. Billing, Pricing and Payments</h3>
<p>4.1. Usage is billed based on real-time consumption (e.g., token count, request volume, or compute duration) in accordance with the current pricing schedule published on the platform.</p>
<p>4.2. You may add funds to your account balance via supported payment methods or redeem authorized coupon/redemption codes.</p>
<p>4.3. All purchases, balance top-ups, and coupon redemptions are final and non-refundable once credited, except where mandatory applicable law provides otherwise.</p>

<h3>5. Disclaimers and Limitation of Liability</h3>
<p>5.1. <strong>Third-Party Model Outputs:</strong> The platform acts as a technology gateway and proxy. AI model outputs are generated dynamically by third-party upstream providers. We make no warranties, express or implied, regarding the accuracy, completeness, validity, or fitness for a particular purpose of any generated content.</p>
<p>5.2. <strong>Service Availability:</strong> While we strive for high availability and low latency, we do not guarantee uninterrupted or error-free service. We are not liable for upstream service disruptions, latency spikes, force majeure events, or downstream indirect losses.</p>

<h3>6. Amendments and Governing Law</h3>
<p>We reserve the right to revise these Terms at any time. Continued use of the platform following the posting of updated Terms constitutes your binding acceptance of the revised Terms.</p>`;

export const DEFAULT_PRIVACY_EN = `<h2>Privacy Policy</h2>
<p>We are committed to safeguarding your privacy and personal data. This Privacy Policy explains how we collect, use, process, disclose, and protect your information when you access our platform, register an account, utilize our APIs, or interact with our services. Please review this policy carefully.</p>

<h3>1. Information We Collect</h3>
<p>We collect only the minimum necessary data required to deliver reliable and secure AI API relay services:</p>
<ul>
  <li><strong>Account & Profile Data:</strong> Username, email address, authentication credentials, or authorization identifiers from third-party OAuth providers (e.g., GitHub, Google, WeChat).</li>
  <li><strong>API Keys & Configurations:</strong> API tokens created in your dashboard, permission scopes, model routing rules, IP whitelists, and rate limit settings.</li>
  <li><strong>Usage & Operational Logs:</strong> Request timestamps, requested model names, token consumption metrics, client IP addresses, HTTP status codes, and request durations for billing and auditing purposes.</li>
  <li><strong>Billing & Assets:</strong> Top-up order history, balance ledgers, redemption code records, and files or assets uploaded in your creative workspace.</li>
</ul>

<h3>2. How We Use Your Information</h3>
<p>Your information is used strictly for the following purposes:</p>
<ol>
  <li><strong>Service Delivery:</strong> Authenticating API requests, performing real-time request routing, streaming responses, and managing platform features;</li>
  <li><strong>Billing & Account Management:</strong> Calculating real-time token and credit consumption to maintain accurate ledger balances;</li>
  <li><strong>Security & Abuse Prevention:</strong> Detecting fraudulent calls, enforcing rate limits, mitigating malicious traffic, and ensuring platform integrity;</li>
  <li><strong>Infrastructure Optimization:</strong> Monitoring system reliability, debugging performance bottlenecks, and improving user experience;</li>
  <li><strong>Compliance & Support:</strong> Responding to support inquiries and complying with statutory audit and legal retention requirements.</li>
</ol>

<h3>3. Data Privacy of Prompts & Generated Content</h3>
<p>3.1. Prompts, messages, images, and other inputs submitted through our APIs or playground are transmitted in real time to the selected upstream AI model providers solely for inference execution.</p>
<p>3.2. We do <strong>NOT</strong> sell your prompts or proprietary business data to third parties, nor do we use your private data for unconsented model training.</p>
<p>3.3. Technical logs are retained only for the duration necessary for audit, debugging, and billing validation, after which they are systematically purged.</p>

<h3>4. Data Sharing & Third-Party Providers</h3>
<p>4.1. <strong>Upstream AI Providers:</strong> In order to fulfill your API requests, necessary payload data is forwarded to the designated upstream providers (such as OpenAI, Anthropic, Google, Zhipu, Alibaba Cloud, etc.), who process data in accordance with their respective privacy policies.</p>
<p>4.2. We do not sell, rent, or trade your personal data to third parties. We disclose data only when legally required by judicial or regulatory authorities.</p>

<h3>5. Data Security Measures</h3>
<p>We implement robust technical and organizational security measures, including end-to-end TLS/HTTPS encryption, strong cryptographic password hashing (e.g., bcrypt/argon2), and strict role-based access controls to prevent unauthorized access, alteration, or disclosure of your data.</p>

<h3>6. Your Rights and Contact</h3>
<p>You have the right to access, update, or revoke your API keys and profile information at any time via the console. If you have questions regarding this Privacy Policy or wish to request account deletion, please contact platform support.</p>`;

export const DEFAULT_AGREEMENT_SETTINGS = {
  tos_mode: 'text' as const,
  tos_mode_en: 'text' as const,
  tos_content: DEFAULT_TOS_ZH,
  tos_content_en: DEFAULT_TOS_EN,
  tos_link: '',
  tos_link_en: '',
  privacy_mode: 'text' as const,
  privacy_mode_en: 'text' as const,
  privacy_content: DEFAULT_PRIVACY_ZH,
  privacy_content_en: DEFAULT_PRIVACY_EN,
  privacy_link: '',
  privacy_link_en: '',
  tos_enabled: true,
  privacy_enabled: true,
};
