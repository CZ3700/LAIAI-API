import React from 'react';
import { Avatar, Popover, Button, Tooltip } from 'antd';
import { DashboardOutlined, WalletOutlined, LogoutOutlined, UserOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import useAuthStore from '../../store/auth';
import { useThemeStore } from '../../store/theme';
import useSettingsStore from '../../store/settings';
import { solidAccent } from '../../theme/tokens';

interface UserAvatarMenuProps {
  isUserEnd?: boolean;
  agreement?: any;
  children?: React.ReactNode;
}

const UserAvatarMenu: React.FC<UserAvatarMenuProps> = ({ isUserEnd = true, agreement, children }) => {
  const { user, logout } = useAuthStore();
  const navigate = useNavigate();
  const { t, i18n } = useTranslation();
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';
  const { settings } = useSettingsStore();
  const adminPath = settings?.site?.admin_path || 'admin';

  const avatarAccent = solidAccent(isLight ? 'light' : 'dark');
  const avatarHoverBg = isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.08)';

  if (!user) {
    return (
      <>
        <style>
          {`
            .header-avatar-btn:hover {
              background: ${avatarHoverBg};
            }
          `}
        </style>
        <Tooltip
          title={t('menu.login', '登录')}
          placement="bottom"
          color={isLight ? '#fff' : '#2b2b2b'}
          styles={{ container: { color: isLight ? '#1f2937' : '#fff' } }}
        >
          <div
            className="header-avatar-btn"
            onClick={() => navigate('/login')}
            style={{
              display: 'flex', alignItems: 'center', cursor: 'pointer', padding: '4px',
              borderRadius: '50%', transition: 'background 0.2s',
              border: '2px solid transparent'
            }}
          >
            <Avatar
              size={34}
              icon={<UserOutlined />}
              style={{
                backgroundColor: avatarAccent.background,
                color: avatarAccent.color,
                fontSize: 16,
              }}
            />
          </div>
        </Tooltip>
      </>
    );
  }

  const showAgreement = (type: 'tos' | 'privacy') => {
    const isZh = Boolean(i18n.language && i18n.language.toLowerCase().startsWith('zh'));
    const fallbackPath = type === 'tos'
      ? (isZh ? '/legal/terms' : '/legal/terms?lang=en')
      : (isZh ? '/legal/privacy' : '/legal/privacy?lang=en');

    if (!agreement) {
      window.open(fallbackPath, '_blank');
      return;
    }
    
    if (type === 'tos') {
      const mode = isZh ? agreement.tos_mode : agreement.tos_mode_en;
      const rawLink = !isZh && agreement.tos_link_en ? agreement.tos_link_en : agreement.tos_link;
      const link = String(rawLink || '').trim();
      if (mode === 'link' && link) {
        const fullUrl = /^https?:\/\//i.test(link) || link.startsWith('/') ? link : `https://${link}`;
        window.open(fullUrl, '_blank');
      } else {
        window.open(fallbackPath, '_blank');
      }
    } else {
      const mode = isZh ? agreement.privacy_mode : agreement.privacy_mode_en;
      const rawLink = !isZh && agreement.privacy_link_en ? agreement.privacy_link_en : agreement.privacy_link;
      const link = String(rawLink || '').trim();
      if (mode === 'link' && link) {
        const fullUrl = /^https?:\/\//i.test(link) || link.startsWith('/') ? link : `https://${link}`;
        window.open(fullUrl, '_blank');
      } else {
        window.open(fallbackPath, '_blank');
      }
    }
  };

  const handleLogout = () => {
    logout();
    if (isUserEnd) {
      navigate('/login');
    } else {
      navigate(`/${adminPath}`);
    }
  };

  const userInitial = user?.nickname?.charAt(0)?.toUpperCase() || user?.username?.charAt(0)?.toUpperCase() || '?';
  const displayName = user?.nickname || user?.username || 'User';

  // Theme-aware colors
  const tc = {
    text: isLight ? '#1f2937' : '#e5e5e5',
    textSub: isLight ? '#6b7280' : 'rgba(255,255,255,0.45)',
    textMuted: isLight ? '#9ca3af' : 'rgba(255,255,255,0.2)',
    btnBg: isLight ? 'rgba(0,0,0,0.03)' : 'rgba(255,255,255,0.04)',
    btnBorder: isLight ? '#e5e7eb' : 'rgba(255,255,255,0.1)',
    btnText: isLight ? '#374151' : '#e5e5e5',
    btnHoverBg: isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.1)',
    btnHoverBorder: isLight ? '#d1d5db' : 'rgba(255,255,255,0.2)',
    btnHoverText: isLight ? '#111827' : '#fff',
    avatarHoverBg,
    levelBg: isLight ? 'rgba(0,0,0,0.04)' : 'rgba(255,255,255,0.08)',
    levelBorder: isLight ? 'rgba(0,0,0,0.1)' : 'rgba(255,255,255,0.2)',
    levelText: isLight ? '#4b5563' : '#e5e5e5',
  };

  const profileContent = (
    <div style={{ width: 300, padding: '12px 8px', display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
      <div style={{ marginTop: 8, marginBottom: 20, display: 'flex', alignItems: 'center', gap: 16, width: '100%', padding: '0 8px' }}>
        <Avatar 
          size={56} 
          src={user.avatar || undefined}
          style={{ backgroundColor: avatarAccent.background, color: avatarAccent.color, fontSize: 24, flexShrink: 0, cursor: 'pointer' }}
          onClick={() => { navigate('/profile'); }}
        >
          {userInitial}
        </Avatar>
        <div style={{ overflow: 'hidden', flex: 1 }}>
          <div style={{ fontWeight: 500, fontSize: 16, color: tc.text, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{displayName}</span>
            {(user?.level_name || user?.user_group) && (
              <span style={{ 
                fontSize: 11, padding: '0 6px', background: tc.levelBg, 
                color: tc.levelText, borderRadius: 4, fontWeight: 'normal', flexShrink: 0,
                border: `1px solid ${tc.levelBorder}`, lineHeight: '18px',
                userSelect: 'none'
              }}>
                {user.level_name || (user.user_group === 'default' ? t('profile.membership_default', '普通会员') : user.user_group)}
              </span>
            )}
          </div>
          <div style={{ fontSize: 14, color: tc.textSub, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', marginTop: 2 }}>
            {t('profile.uid', '用户 UID')}:{user?.uid || '-'}
          </div>
        </div>
      </div>

      <div style={{ width: '100%', display: 'flex', flexDirection: 'column', gap: 8 }}>
        {isUserEnd && (
          <Button 
            type="default"
            style={{ 
              height: 48, borderRadius: 24, display: 'flex', alignItems: 'center', justifyContent: 'center',
              background: tc.btnBg, borderColor: tc.btnBorder, color: tc.btnText, fontSize: 15,
              transition: 'all 0.2s'
            }}
            className="hover-bright-btn"
            icon={<DashboardOutlined style={{ fontSize: 18 }} />}
            onClick={() => { navigate('/dashboard'); }}
          >
            {t('menu.dashboard')}
          </Button>
        )}

        {isUserEnd && (
          <Button 
            type="default"
            style={{ 
              height: 48, borderRadius: 24, display: 'flex', alignItems: 'center', justifyContent: 'center',
              background: tc.btnBg, borderColor: tc.btnBorder, color: tc.btnText, fontSize: 15,
              transition: 'all 0.2s'
            }}
            className="hover-bright-btn"
            icon={<WalletOutlined style={{ fontSize: 18 }} />}
            onClick={() => { navigate('/wallet'); }}
          >
            {t('menu.wallet', '我的钱包')}
          </Button>
        )}
        
        <Button 
          type="default"
          style={{ 
            height: 48, borderRadius: 24, display: 'flex', alignItems: 'center', justifyContent: 'center',
            background: tc.btnBg, borderColor: tc.btnBorder, color: tc.btnText, fontSize: 15,
            transition: 'all 0.2s'
          }}
          className="hover-bright-btn"
          icon={<LogoutOutlined style={{ fontSize: 18 }} />}
          onClick={handleLogout}
        >
          {t('common.logout', '退出登录')}
        </Button>
      </div>

      <div style={{ marginTop: 24, display: 'flex', justifyContent: 'center', gap: 24, width: '100%' }}>
        <Button type="link" onClick={() => showAgreement('privacy')} style={{ color: tc.textSub, fontSize: 12, padding: 0 }}>{t('common.privacy_policy', '隐私政策')}</Button>
        <span style={{ color: tc.textMuted }}>•</span>
        <Button type="link" onClick={() => showAgreement('tos')} style={{ color: tc.textSub, fontSize: 12, padding: 0 }}>{t('common.terms_of_service', '服务条款')}</Button>
      </div>
    </div>
  );

  return (
    <>
      <style>
        {`
          .hover-bright-btn:hover {
            background: ${tc.btnHoverBg} !important;
            border-color: ${tc.btnHoverBorder} !important;
            color: ${tc.btnHoverText} !important;
          }
          .header-avatar-btn:hover {
            background: ${tc.avatarHoverBg};
          }
        `}
      </style>
      <Popover 
        content={profileContent} 
        trigger="click" 
        placement="bottomRight"
        overlayClassName="custom-premium-popover"
        forceRender
        destroyOnHidden={false}
        /* 毛玻璃样式由 index.css .custom-premium-popover 统一处理（根节点 blur，兼容 Edge/Chromium） */
        styles={{
          container: {
            padding: 0,
            background: 'transparent',
            boxShadow: 'none',
          },
        }}
        /* 关闭 zoom 动画，避免 Chromium opacity 切断 backdrop-filter */
        motion={{ motionName: '' }}
        arrow={false}
      >
        {children || (
          <div 
            className="header-avatar-btn"
            style={{ 
              display: 'flex', alignItems: 'center', cursor: 'pointer', padding: '4px', 
              borderRadius: '50%', transition: 'background 0.2s',
              border: '2px solid transparent'
            }} 
          >
            <Avatar size={34} src={user.avatar || undefined} style={{ backgroundColor: avatarAccent.background, color: avatarAccent.color, fontSize: 16 }}>
              {userInitial}
            </Avatar>
          </div>
        )}
      </Popover>
    </>
  );
};

export default UserAvatarMenu;
