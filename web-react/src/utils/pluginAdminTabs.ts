/** 与 PluginConfig Tabs items 的 key / 文案对齐，供侧栏默认跳转与基本配置下拉使用 */

type PluginAdminTabOption = { key: string; label: string };

type DynamicPluginTabMeta = {
  title?: string;
  component?: unknown;
  tabs?: { key: string; label: string }[];
};

const BASIC: PluginAdminTabOption = { key: 'basic', label: '基本配置' };

export function getPluginAdminTabs(
  pluginName: string,
  dynamic?: DynamicPluginTabMeta,
  t?: (key: string, fallback: string) => string,
): PluginAdminTabOption[] {
  const basic: PluginAdminTabOption = {
    key: 'basic',
    label: t ? t('playground_2026:admin_tab_basic', '基本配置') : '基本配置',
  };

  if (dynamic) {
    const tabs: PluginAdminTabOption[] = [basic];
    if (dynamic.tabs && dynamic.tabs.length > 0) {
      if (pluginName === 'upstream_asset_relay') {
        dynamic.tabs.forEach((t) => {
          tabs.push({ key: t.key, label: t.label });
          if (t.key === 'uar_bindings') {
            tabs.push({ key: 'api_access', label: 'API 接口调用' });
          }
        });
      } else {
        tabs.push(...dynamic.tabs.map((t) => ({ key: t.key, label: t.label })));
      }
    } else if (dynamic.component) {
      tabs.push({ key: 'plugin_panel', label: dynamic.title || '插件面板' });
    }
    if (pluginName === 'upstream_asset_relay') {
      tabs.push({ key: 'storage', label: '存储配置' });
    }
    return tabs;
  }

  switch (pluginName) {
    case 'high_availability_channel':
      return [
        basic,
        { key: 'ha_config', label: '高可用参数配置' },
        { key: 'ha_logs', label: '使用日志记录' },
      ];
    case 'team_marketing':
      return [
        basic,
        { key: 'team_config', label: '团队配置' },
        { key: 'theme_promo', label: '主题推广' },
      ];
    case 'playground':
      return [
        basic,
        { key: 'pg_storage', label: '存储配置' },
        { key: 'playground_models', label: '创作模型管理' },
        { key: 'playground_schemes', label: '创作方案配置' },
        { key: 'playground_advanced_nodes', label: '高级节点配置' },
        { key: 'playground_agent_config', label: 'AI智能体配置' },
      ];
    case 'playground_2026':
      return [
        basic,
        { key: 'pg_storage', label: t ? t('playground_2026:admin_tab_storage', '存储配置') : '存储配置' },
        { key: 'playground_models', label: t ? t('playground_2026:admin_tab_models', '创作模型管理') : '创作模型管理' },
        { key: 'playground_schemes', label: t ? t('playground_2026:admin_tab_schemes', '创作方案配置') : '创作方案配置' },
        { key: 'playground_chat_config', label: t ? t('playground_2026:admin_tab_chat', '聊天配置') : '聊天配置' },
        { key: 'playground_workflow_config', label: t ? t('playground_2026:admin_tab_workflow', '工作流配置') : '工作流配置' },
        { key: 'playground_skill_config', label: t ? t('playground_2026:admin_tab_skill', 'Skill 配置') : 'Skill 配置' },
        { key: 'playground_prompt_optimize', label: t ? t('playground_2026:admin_tab_prompt_optimize', 'AI 优化提示词') : 'AI 优化提示词' },
        { key: 'playground_error_mappings', label: t ? t('playground_2026:admin_tab_error_mappings', '异常映射') : '异常映射' },
      ];
    case 'model_marketplace':
      return [
        BASIC,
        { key: 'marketplace_models', label: '模型列表' },
        { key: 'marketplace_trending', label: '热门推荐' },
      ];
    case 'site_icons':
      return [BASIC, { key: 'icon_library', label: '图标库管理' }];
    case 'site_portal':
      return [
        { key: 'portal_manager', label: '门户管理' },
        { key: 'style_selection', label: '风格选择' },
        BASIC,
        { key: 'storage', label: '门户存储配置' },
      ];
    case 'site_portal_pro':
      return [
        { key: 'portal_manager', label: '门户管理' },
        { key: 'article_manager', label: '栏目文章' },
        { key: 'docs_manager', label: 'DOCS文档' },
        { key: 'style_selection', label: '风格选择' },
        BASIC,
        { key: 'storage', label: '门户存储配置' },
      ];
    case 'docs_api':
      return [{ key: 'docs_manager', label: '文档管理' }, BASIC];
    case 'asset_manager':
    case 'asset_manager_intl':
      return [
        BASIC,
        { key: 'api_access', label: 'API 接口调用' },
        { key: 'storage', label: '存储配置' },
        { key: 'moderation', label: '审核配置' },
        { key: 'moderation_query', label: '风控查询' },
        { key: 'audit_log', label: '审核日志' },
        { key: 'preset', label: '预设素材' },
        { key: 'relay_convert', label: '转换素材' },
        { key: 'api_proxy', label: 'API 素材' },
        { key: 'cloud_assets', label: '云端素材' },
        { key: 'api_log', label: '接口日志' },
      ];
    default:
      return [BASIC];
  }
}

/** 侧栏跳转：已保存的 key 原样使用（含动态插件 tab）；空则第一个 tab */
export function pluginAdminMenuTabKey(
  pluginName: string,
  stored?: string | null,
): string {
  const saved = (stored || '').trim();
  if (saved) return saved;
  return getPluginAdminTabs(pluginName)[0]?.key ?? 'basic';
}

/** 配置页下拉：必须落在当前插件真实 tab 上，否则回落第一个 */
export function resolvePluginAdminDefaultTab(
  pluginName: string,
  stored?: string | null,
  dynamic?: DynamicPluginTabMeta,
): string {
  const tabs = getPluginAdminTabs(pluginName, dynamic);
  const saved = (stored || '').trim();
  if (saved && tabs.some((t) => t.key === saved)) return saved;
  return tabs[0]?.key ?? 'basic';
}
