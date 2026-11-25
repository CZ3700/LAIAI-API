import React, { useEffect, useRef, useState } from 'react';
import { Modal, Button } from 'antd';
import {
  MobileOutlined,
  MailOutlined,
  SafetyCertificateOutlined,
  CheckCircleFilled,
  CloseOutlined,
} from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import useAuthStore from '../store/auth';
import useSettingsStore from '../store/settings';
import { useThemeStore } from '../store/theme';
import {
  bindPromptDescription,
  dismissBindPromptToday,
  hasValidEmail,
  hasValidMobile,
  isBindPolicyActive,
  isBindPromptDismissedToday,
  isBindSatisfied,
} from '../utils/bindPolicy';
import { fetchConsoleBootstrap } from '../utils/consoleBootstrap';
import type { User } from '../types';

/**
 * 用户端登录后绑定提醒：可关闭，当天不再弹；不强制当场绑定。
 * 视觉对齐右上角头像 Popover（毛玻璃 + 胶囊按钮）。
 */
const BindPromptModal: React.FC = () => {
  const navigate = useNavigate();
  const { user, setUser, isLoggedIn } = useAuthStore();
  const { settings } = useSettingsStore();
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';
  const [open, setOpen] = useState(false);
  const [displayUser, setDisplayUser] = useState<User | null>(user);
  const checkedKeyRef = useRef<string>('');

  const reg = settings?.registration;
  const timeZone = settings?.site?.default_timezone;

  useEffect(() => {
    let cancelled = false;

    const run = async () => {
      if (!isLoggedIn || !user || user.role === 'admin') {
        setOpen(false);
        return;
      }
      if (!isBindPolicyActive(reg)) {
        setOpen(false);
        return;
      }

      const checkKey = `${user.id}|${!!reg?.require_bind_mobile}|${!!reg?.require_bind_email}|${reg?.bind_enforcement || 'all'}`;
      if (checkedKeyRef.current === checkKey) return;
      checkedKeyRef.current = checkKey;

      if (isBindPromptDismissedToday(user.id, timeZone)) {
        setOpen(false);
        return;
      }

      let latest: User = user;
      try {
        const boot = await fetchConsoleBootstrap();
        if (boot?.user && !cancelled) {
          latest = { ...user, ...boot.user };
          setUser(latest, !!sessionStorage.getItem('token'));
          setDisplayUser(latest);
        }
      } catch {
        setDisplayUser(user);
      }
      if (cancelled) return;

      setOpen(!isBindSatisfied(reg, latest));
    };

    void run();
    return () => {
      cancelled = true;
    };
  }, [
    isLoggedIn,
    user?.id,
    user?.role,
    reg?.require_bind_mobile,
    reg?.require_bind_email,
    reg?.bind_enforcement,
    timeZone,
    setUser,
  ]);

  const handleDismiss = () => {
    if (user?.id) dismissBindPromptToday(user.id, timeZone);
    setOpen(false);
  };

  const handleGoBind = (target?: 'mobile' | 'email') => {
    setOpen(false);
    if (target) {
      navigate(`/profile?bind=${target}`);
      return;
    }
    if (needMobile && !hasValidMobile(u?.mobile)) {
      navigate('/profile?bind=mobile');
    } else if (needEmail && !hasValidEmail(u?.email)) {
      navigate('/profile?bind=email');
    } else {
      navigate('/profile');
    }
  };

  const needMobile = !!reg?.require_bind_mobile;
  const needEmail = !!reg?.require_bind_email;
  const u = displayUser || user;

  const tc = {
    text: isLight ? '#1f2937' : '#e5e5e5',
    textSub: isLight ? '#6b7280' : 'rgba(255,255,255,0.45)',
    textMuted: isLight ? '#9ca3af' : 'rgba(255,255,255,0.28)',
    btnBg: isLight ? 'rgba(0,0,0,0.03)' : 'rgba(255,255,255,0.04)',
    btnBorder: isLight ? '#e5e7eb' : 'rgba(255,255,255,0.1)',
    btnText: isLight ? '#374151' : '#e5e5e5',
    btnHoverBg: isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.1)',
    btnHoverBorder: isLight ? '#d1d5db' : 'rgba(255,255,255,0.2)',
    btnHoverText: isLight ? '#111827' : '#fff',
    primaryBg: isLight ? '#18181b' : '#fafafa',
    primaryText: isLight ? '#fafafa' : '#18181b',
    rowBg: isLight ? 'rgba(0,0,0,0.03)' : 'rgba(255,255,255,0.05)',
    rowBorder: isLight ? 'rgba(0,0,0,0.06)' : 'rgba(255,255,255,0.08)',
    ok: isLight ? '#16a34a' : '#4ade80',
    pending: isLight ? '#9ca3af' : 'rgba(255,255,255,0.35)',
    iconWrapBg: isLight ? 'rgba(0,0,0,0.05)' : 'rgba(255,255,255,0.08)',
  };

  const statusRow = (
    key: 'mobile' | 'email',
    icon: React.ReactNode,
    label: string,
    bound: boolean,
  ) => (
    <div
      key={key}
      onClick={bound ? undefined : () => handleGoBind(key)}
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        padding: '12px 14px',
        borderRadius: 14,
        background: tc.rowBg,
        border: `1px solid ${tc.rowBorder}`,
        cursor: bound ? 'default' : 'pointer',
      }}
    >
      <div
        style={{
          width: 36,
          height: 36,
          borderRadius: 10,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          background: tc.iconWrapBg,
          color: bound ? tc.ok : tc.textSub,
          fontSize: 16,
          flexShrink: 0,
        }}
      >
        {icon}
      </div>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: 14, fontWeight: 500, color: tc.text }}>{label}</div>
        <div style={{ fontSize: 12, color: bound ? tc.ok : tc.pending, marginTop: 2 }}>
          {bound ? '已绑定' : '未绑定'}
        </div>
      </div>
      {bound ? (
        <CheckCircleFilled style={{ color: tc.ok, fontSize: 16 }} />
      ) : (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            handleGoBind(key);
          }}
          style={{
            fontSize: 12,
            padding: '3px 10px',
            borderRadius: 999,
            border: `1px solid ${tc.btnBorder}`,
            background: tc.btnBg,
            color: tc.btnText,
            cursor: 'pointer',
            lineHeight: '18px',
            fontWeight: 500,
            whiteSpace: 'nowrap',
            transition: 'all 0.2s',
          }}
          className="bind-prompt-hover-btn"
        >
          直接去绑定
        </button>
      )}
    </div>
  );

  return (
    <>
      <style>
        {`
          .bind-prompt-hover-btn:hover {
            background: ${tc.btnHoverBg} !important;
            border-color: ${tc.btnHoverBorder} !important;
            color: ${tc.btnHoverText} !important;
          }
        `}
      </style>
      <Modal
        open={open}
        onCancel={handleDismiss}
        footer={null}
        closable={false}
        destroyOnClose
        maskClosable
        centered
        width={360}
        className="custom-premium-bind-modal"
        styles={{
          mask: {
            backdropFilter: 'blur(6px)',
            WebkitBackdropFilter: 'blur(6px)',
            background: isLight ? 'rgba(15,15,18,0.28)' : 'rgba(0,0,0,0.55)',
          },
          container: {
            padding: 0,
            background: 'transparent',
            boxShadow: 'none',
          },
          body: {
            padding: 0,
          },
        }}
        modalRender={(node) => <div className="custom-premium-bind-modal-shell">{node}</div>}
      >
        <div
          style={{
            width: '100%',
            padding: '20px 18px 18px',
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'stretch',
            position: 'relative',
          }}
        >
          <button
            type="button"
            onClick={handleDismiss}
            aria-label="关闭"
            style={{
              position: 'absolute',
              top: 14,
              right: 14,
              width: 28,
              height: 28,
              borderRadius: '50%',
              border: `1px solid ${tc.btnBorder}`,
              background: tc.btnBg,
              color: tc.textSub,
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              cursor: 'pointer',
              transition: 'all 0.2s',
            }}
            className="bind-prompt-hover-btn"
          >
            <CloseOutlined style={{ fontSize: 12 }} />
          </button>

          <div
            style={{
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              textAlign: 'center',
              padding: '4px 12px 16px',
            }}
          >
            <div
              style={{
                width: 52,
                height: 52,
                borderRadius: 16,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                background: tc.iconWrapBg,
                border: `1px solid ${tc.rowBorder}`,
                color: tc.text,
                fontSize: 24,
                marginBottom: 14,
              }}
            >
              <SafetyCertificateOutlined />
            </div>
            <div style={{ fontWeight: 600, fontSize: 17, color: tc.text, letterSpacing: '-0.01em' }}>
              账号安全绑定提醒
            </div>
            <div
              style={{
                marginTop: 8,
                fontSize: 13,
                lineHeight: 1.55,
                color: tc.textSub,
                maxWidth: 280,
              }}
            >
              {bindPromptDescription(reg)}
            </div>
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 16 }}>
            {needMobile &&
              statusRow('mobile', <MobileOutlined />, '手机号', hasValidMobile(u?.mobile))}
            {needEmail &&
              statusRow('email', <MailOutlined />, '邮箱', hasValidEmail(u?.email))}
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            <Button
              type="default"
              onClick={() => handleGoBind()}
              style={{
                height: 48,
                borderRadius: 24,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                background: tc.primaryBg,
                borderColor: tc.primaryBg,
                color: tc.primaryText,
                fontSize: 15,
                fontWeight: 500,
                transition: 'opacity 0.2s',
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.opacity = '0.88';
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.opacity = '1';
              }}
            >
              去绑定
            </Button>
            <Button
              type="default"
              className="bind-prompt-hover-btn"
              onClick={handleDismiss}
              style={{
                height: 48,
                borderRadius: 24,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                background: tc.btnBg,
                borderColor: tc.btnBorder,
                color: tc.btnText,
                fontSize: 15,
                transition: 'all 0.2s',
              }}
            >
              稍后再说
            </Button>
          </div>
        </div>
      </Modal>
    </>
  );
};

export default BindPromptModal;
