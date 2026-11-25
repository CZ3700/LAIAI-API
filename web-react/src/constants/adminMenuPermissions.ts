/** 管理后台「可见基础菜单」权限树：一级分类 + 可选二级菜单 */

export type AdminMenuPermChild = {
  label: string;
  labelKey: string;
  value: string;
};

export type AdminMenuPermNode = {
  label: string;
  labelKey: string;
  value: string;
  children?: AdminMenuPermChild[];
};

export const ADMIN_MENU_PERMISSIONS: AdminMenuPermNode[] = [
  { label: '控制台', labelKey: 'menu.dashboard', value: 'dashboard' },
  { label: 'API教程', labelKey: 'menu.relay_api', value: 'relay_api' },
  { label: '令牌管理', labelKey: 'menu.apikeys', value: 'tokens' },
  {
    label: '日志记录',
    labelKey: 'menu.logs',
    value: 'logs',
    children: [
      { label: '日志记录', labelKey: 'menu.usage_logs', value: 'logs.usage' },
      { label: '任务列表', labelKey: 'menu.task_logs', value: 'logs.tasks' },
    ],
  },
  {
    label: '渠道管理',
    labelKey: 'menu.channels',
    value: 'channels',
    children: [
      { label: '模型渠道分组', labelKey: 'menu.channel_groups', value: 'channels.groups' },
      { label: '上游渠道配置', labelKey: 'menu.channel_configs', value: 'channels.configs' },
    ],
  },
  {
    label: '模型管理',
    labelKey: 'menu.models',
    value: 'models',
    children: [
      { label: '模型列表', labelKey: 'menu.model_list', value: 'models.list' },
      { label: '计费配置', labelKey: 'menu.billing_rules', value: 'models.billing_rules' },
      { label: '转发规则', labelKey: 'menu.forward_rules', value: 'models.forward_rules' },
    ],
  },
  {
    label: '营销管理',
    labelKey: 'menu.marketing',
    value: 'marketing',
    children: [
      { label: '兑换券管理', labelKey: 'menu.redemptions', value: 'marketing.redemptions' },
      { label: '注册充值好礼', labelKey: 'menu.registration_gifts', value: 'marketing.registration_gifts' },
      { label: '提示通知', labelKey: 'menu.announcements', value: 'marketing.announcements' },
    ],
  },
  {
    label: '用户管理',
    labelKey: 'menu.users',
    value: 'users',
    children: [
      { label: '普通用户列表', labelKey: 'menu.user_list', value: 'users.list' },
      { label: '管理员列表', labelKey: 'menu.admin_list', value: 'users.admins' },
      { label: '用户等级', labelKey: 'menu.user_levels', value: 'users.levels' },
      { label: '管理员等级', labelKey: 'menu.admin_groups', value: 'admin_groups' },
      { label: '用户数据分析', labelKey: 'menu.user_analytics', value: 'users.analytics' },
    ],
  },
  {
    label: '财务管理',
    labelKey: 'menu.finance',
    value: 'finance',
    children: [
      { label: '系统资金明细', labelKey: 'menu.finance_recharges', value: 'finance.recharges' },
      { label: '在线充值明细', labelKey: 'menu.finance_orders', value: 'finance.orders' },
      { label: '订单明细', labelKey: 'menu.finance_purchase_orders', value: 'finance.purchase_orders' },
      { label: '赠送金明细', labelKey: 'menu.finance_gifts', value: 'finance.gifts' },
      { label: '佣金明细', labelKey: 'menu.finance_commissions', value: 'finance.commissions' },
      { label: '发票申请审核', labelKey: 'menu.finance_invoices', value: 'finance.invoices' },
      { label: '财务数据分析', labelKey: 'menu.finance_analysis', value: 'finance.analysis' },
    ],
  },
  {
    label: '站点设置',
    labelKey: 'menu.settings',
    value: 'settings',
    children: [
      { label: '基础设置', labelKey: 'menu.basic_settings', value: 'settings.basic' },
      { label: '支付设置', labelKey: 'menu.payment_settings', value: 'settings.payment' },
      { label: '消息通知', labelKey: 'menu.message_notification', value: 'settings.message_notification' },
      { label: '授权登录', labelKey: 'menu.oauth_settings', value: 'settings.oauth' },
      { label: '存储设置', labelKey: 'menu.database_settings', value: 'settings.database' },
    ],
  },
];

type AdminPermPolicy = {
  view: string[];
  edit: string[];
};

/** 兼容旧数组与 { view, edit } */
export function parseAdminGroupPermissions(raw?: string | null): AdminPermPolicy {
  if (!raw) return { view: [], edit: [] };
  try {
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed)) {
      const view = parsed.filter((p): p is string => typeof p === 'string');
      return { view, edit: [...view] };
    }
    if (parsed && Array.isArray(parsed.view)) {
      const view = parsed.view.filter((p: unknown): p is string => typeof p === 'string');
      const edit = Array.isArray(parsed.edit)
        ? parsed.edit.filter((p: unknown): p is string => typeof p === 'string' && view.includes(p))
        : [...view];
      return { view, edit };
    }
  } catch {
    /* ignore */
  }
  return { view: [], edit: [] };
}

export function normalizeAdminEditPermissions(view: string[], edit: string[]): string[] {
  const viewSet = new Set(view);
  const next = new Set(edit.filter((k) => viewSet.has(k)));
  for (const group of ADMIN_MENU_PERMISSIONS) {
    if (!group.children?.length) continue;
    const viewed = group.children.map((c) => c.value).filter((k) => viewSet.has(k));
    if (viewed.length === 0) continue;
    if (!viewed.every((k) => next.has(k))) next.delete(group.value);
  }
  return Array.from(next);
}

/** 扁平化所有可勾选权限值（含一级与二级） */
export function flattenAdminMenuPermissions(
  nodes: AdminMenuPermNode[] = ADMIN_MENU_PERMISSIONS,
): string[] {
  return nodes.flatMap((n) => [n.value, ...(n.children?.map((c) => c.value) || [])]);
}

/** 旧数据仅有一级权限时，编辑回显时展开为「一级 + 全部二级」 */
export function expandLegacyAdminMenuPermissions(perms: string[]): string[] {
  const result = new Set(perms);
  for (const group of ADMIN_MENU_PERMISSIONS) {
    if (!group.children?.length) continue;
    const hasAnyChild = group.children.some((c) => result.has(c.value));
    if (result.has(group.value) && !hasAnyChild) {
      group.children.forEach((c) => {
        // admin_groups 历史需单独勾选，回显时不要因一级 users 自动勾上
        if (c.value === 'admin_groups') return;
        result.add(c.value);
      });
    }
  }
  return Array.from(result);
}

/**
 * 保存前规范化：若勾选了任一二级，确保写入对应一级；
 * 若一级下全部二级都勾选，保留一级与全部二级。
 */
export function normalizeAdminMenuPermissions(perms: string[]): string[] {
  const result = new Set(perms.filter((p) => !p.startsWith('plugin:')));
  for (const group of ADMIN_MENU_PERMISSIONS) {
    if (!group.children?.length) continue;
    const selectedChildren = group.children.filter((c) => result.has(c.value));
    if (selectedChildren.length > 0) {
      result.add(group.value);
    } else if (result.has(group.value)) {
      // 仅勾一级、未勾二级：视为全部二级（兼容旧逻辑；admin_groups 除外）
      group.children.forEach((c) => {
        if (c.value === 'admin_groups') return;
        result.add(c.value);
      });
    }
  }
  return Array.from(result);
}

/** 权限标签展示用短名 */
export function getAdminMenuPermissionLabel(
  value: string,
  t?: any,
): string {
  const tx = (key: string, fallback: string) => {
    if (!t) return fallback;
    return String(t(key, { defaultValue: fallback }) ?? fallback);
  };
  for (const group of ADMIN_MENU_PERMISSIONS) {
    if (group.value === value) return tx(group.labelKey, group.label);
    const child = group.children?.find((c) => c.value === value);
    if (child) return `${tx(group.labelKey, group.label)} / ${tx(child.labelKey, child.label)}`;
  }
  return value;
}

/**
 * 子菜单可见性：
 * - 超级管理员：放行
 * - 若 permissions 中存在该一级下任一二级 key：按二级精确匹配
 * - 否则回退为仅检查一级（兼容旧数据）
 */
export function hasAdminChildMenuPermission(
  permissions: string[] | undefined | null,
  parentKey: string,
  childKey: string,
  isSuperAdmin = false,
): boolean {
  if (isSuperAdmin) return true;
  if (!permissions) return false;
  const group = ADMIN_MENU_PERMISSIONS.find((g) => g.value === parentKey);
  const childKeys = group?.children?.map((c) => c.value) || [];
  const hasGranular = childKeys.some((k) => permissions.includes(k));
  if (hasGranular) {
    return permissions.includes(childKey);
  }
  // 旧数据仅有一级权限：放开该分类下二级；admin_groups 历史上一向需单独授权
  if (childKey === 'admin_groups') {
    return permissions.includes('admin_groups');
  }
  return permissions.includes(parentKey);
}
