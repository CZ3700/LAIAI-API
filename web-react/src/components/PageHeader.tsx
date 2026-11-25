import React from 'react';
import { Typography } from 'antd';
import { useThemeStore } from '../store/theme';

const { Title, Text } = Typography;

export interface PageHeaderProps {
  /** 页面主标题（对应 Title level={2}, 700 粗体, -0.025em 字间距） */
  title: React.ReactNode;
  /** 页面副标题/解释说明文本（字号严格固定为 15px, subText 颜色） */
  description?: React.ReactNode;
  /** 头部右侧操作区（如按钮、操作组等） */
  extra?: React.ReactNode;
  /** 底部间距（默认 24px） */
  marginBottom?: number | string;
  /** 容器额外样式类名 */
  className?: string;
  /** 容器行内样式 */
  style?: React.CSSProperties;
  /** 标题行内样式覆盖 */
  titleStyle?: React.CSSProperties;
  /** 副标题行内样式覆盖 */
  descriptionStyle?: React.CSSProperties;
}

/**
 * 全站统一标准页面头部组件 (PageHeader)
 * 严格以「个人中心」标题（Title level 2, 700, -0.025em）与副标题（fontSize: 15, subText）为基准规范。
 * 后续新加页面或调整样式只需修改本组件，全站各页面头部统一联动生效。
 */
export const PageHeader: React.FC<PageHeaderProps> = ({
  title,
  description,
  extra,
  marginBottom = 24,
  className = '',
  style,
  titleStyle,
  descriptionStyle,
}) => {
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';

  // 严格参照 Profile.tsx「个人中心」的标准配色规范
  const mainText = isLight ? '#09090b' : '#fafafa';
  const subText = isLight ? '#71717a' : '#a1a1aa';

  return (
    <div
      className={`flex flex-col sm:flex-row sm:items-center justify-between gap-3 ${className}`}
      style={{ marginBottom, ...style }}
    >
      <div className="min-w-0 flex-1">
        <Title
          level={2}
          style={{
            margin: 0,
            fontWeight: 700,
            color: mainText,
            letterSpacing: '-0.025em',
            lineHeight: 1.25,
            ...titleStyle,
          }}
        >
          {title}
        </Title>
        {description && (
          <Text
            style={{
              color: subText,
              fontSize: 15,
              marginTop: 4,
              display: 'block',
              lineHeight: 1.5,
              ...descriptionStyle,
            }}
          >
            {description}
          </Text>
        )}
      </div>
      {extra && <div className="flex items-center gap-2.5 shrink-0 flex-wrap">{extra}</div>}
    </div>
  );
};

export default PageHeader;
