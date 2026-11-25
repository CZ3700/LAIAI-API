import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal } from 'antd';
import {
  Pin,
  Calendar,
  X,
  ChevronLeft,
  ChevronRight,
  Check,
} from 'lucide-react';
import type { Announcement } from '../types';
import { getAnnouncementLabel, getAnnouncementDisplayTime } from '../utils/announcement';
import { toDisplayLocale } from '../utils/language';
import { softAccent } from '../theme/tokens';

interface AnnouncementPopupModalProps {
  announcements: Announcement[];
  open: boolean;
  onClose: (dontShowToday: boolean, noticeIds: number[]) => void;
  themeMode: 'light' | 'dark';
}

const AnnouncementPopupModal: React.FC<AnnouncementPopupModalProps> = ({
  announcements,
  open,
  onClose,
  themeMode,
}) => {
  const { t, i18n } = useTranslation();
  const [currentIndex, setCurrentIndex] = useState(0);
  const [dontShowToday, setDontShowToday] = useState(false);

  const isLight = themeMode === 'light';

  // 主题色与右上角头像 Popover (custom-premium-popover) 保持一致
  const tc = {
    text: isLight ? '#1f2937' : '#e5e5e5',
    textSub: isLight ? '#6b7280' : 'rgba(255, 255, 255, 0.45)',
    btnBg: isLight ? 'rgba(0, 0, 0, 0.03)' : 'rgba(255, 255, 255, 0.04)',
    btnBorder: isLight ? '#e5e7eb' : 'rgba(255, 255, 255, 0.1)',
    btnText: isLight ? '#374151' : '#e5e5e5',
    btnHoverBg: isLight ? 'rgba(0, 0, 0, 0.06)' : 'rgba(255, 255, 255, 0.1)',
    btnHoverBorder: isLight ? '#d1d5db' : 'rgba(255, 255, 255, 0.2)',
    btnHoverText: isLight ? '#111827' : '#ffffff',
    primaryBg: isLight ? '#18181b' : '#fafafa',
    primaryText: isLight ? '#fafafa' : '#18181b',
    separator: isLight ? '1px solid rgba(0, 0, 0, 0.06)' : '1px solid rgba(255, 255, 255, 0.08)',
  };

  // 重置状态
  useEffect(() => {
    if (open) {
      setCurrentIndex(0);
      setDontShowToday(false);
    }
  }, [open]);

  // 键盘快捷键导航
  useEffect(() => {
    if (!open) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        handleClose();
      } else if (e.key === 'ArrowLeft') {
        setCurrentIndex((prev) => Math.max(0, prev - 1));
      } else if (e.key === 'ArrowRight') {
        setCurrentIndex((prev) => Math.min(announcements.length - 1, prev + 1));
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [open, announcements.length]);

  const handleClose = () => {
    onClose(dontShowToday, announcements.map((a) => a.id));
  };

  if (!announcements.length) return null;

  const currentNotice = announcements[currentIndex] || announcements[0];
  const totalCount = announcements.length;

  const handlePrev = () => {
    setCurrentIndex((prev) => Math.max(0, prev - 1));
  };

  const handleNext = () => {
    setCurrentIndex((prev) => Math.min(totalCount - 1, prev + 1));
  };

  const formatNoticeTime = (createdAt: string) =>
    new Date(createdAt).toLocaleString(toDisplayLocale(i18n.language), {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    });

  return (
    <>
      <style>{`
        .announcement-custom-scrollbar::-webkit-scrollbar {
          width: 5px;
        }
        .announcement-custom-scrollbar::-webkit-scrollbar-track {
          background: transparent;
        }
        .announcement-custom-scrollbar::-webkit-scrollbar-thumb {
          background: ${isLight ? 'rgba(0,0,0,0.15)' : 'rgba(255,255,255,0.18)'};
          border-radius: 9999px;
          transition: background 0.2s ease;
        }
        .announcement-custom-scrollbar::-webkit-scrollbar-thumb:hover {
          background: ${isLight ? 'rgba(0,0,0,0.25)' : 'rgba(255,255,255,0.28)'};
        }
        @keyframes announcement-check-pop {
          0% {
            transform: scale(0.85);
          }
          60% {
            transform: scale(1.15);
          }
          100% {
            transform: scale(1);
          }
        }
        .announcement-check-active {
          animation: announcement-check-pop 0.22s cubic-bezier(0.16, 1, 0.3, 1) forwards;
        }
      `}</style>

      <Modal
        open={open}
        onCancel={handleClose}
        footer={null}
        closable={false}
        maskClosable
        centered
        width="min(560px, 92vw)"
        className="custom-premium-announcement-modal"
        rootClassName="custom-premium-announcement-modal-root"
        transitionName="announcement-zoom"
        maskTransitionName="announcement-mask-fade"
        mousePosition={null}
        styles={{
          container: {
            padding: 0,
            background: 'transparent',
            boxShadow: 'none',
          },
          body: {
            padding: 0,
            background: 'transparent',
          },
        }}
        modalRender={(node) => (
          <div className="custom-premium-announcement-modal-shell">{node}</div>
        )}
      >
        <div style={{ width: '100%', display: 'flex', flexDirection: 'column' }}>
          {/* Header: 多条时竖排目录；单条时标题放在顶部与关闭按钮同一区域 */}
          <div
            style={{
              padding: '16px 20px',
              display: 'flex',
              alignItems: totalCount > 1 ? 'flex-start' : 'center',
              justifyContent: 'space-between',
              gap: '12px',
              borderBottom: tc.separator,
            }}
          >
            {totalCount > 1 ? (
              <div
                style={{
                  minWidth: 0,
                  flex: 1,
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'flex-start',
                  gap: '4px',
                }}
              >
                {announcements.map((item, idx) => {
                  const isActive = idx === currentIndex;
                  const rawTitle = getAnnouncementLabel(item.title);
                  return (
                    <button
                      key={item.id || idx}
                      onClick={() => setCurrentIndex(idx)}
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: '6px',
                        maxWidth: '100%',
                        padding: '3px 8px',
                        margin: '0 -8px',
                        borderRadius: '6px',
                        border: 'none',
                        background: isActive
                          ? isLight
                            ? 'rgba(0, 0, 0, 0.05)'
                            : 'rgba(255, 255, 255, 0.08)'
                          : 'transparent',
                        cursor: 'pointer',
                        fontSize: '13px',
                        lineHeight: 1.45,
                        fontWeight: isActive ? 500 : 400,
                        color: isActive ? tc.text : tc.textSub,
                        textAlign: 'left',
                        transition: 'all 0.2s cubic-bezier(0.16, 1, 0.3, 1)',
                      }}
                      onMouseEnter={(e) => {
                        if (!isActive) {
                          e.currentTarget.style.backgroundColor = isLight
                            ? 'rgba(0, 0, 0, 0.03)'
                            : 'rgba(255, 255, 255, 0.04)';
                          e.currentTarget.style.color = tc.text;
                        }
                      }}
                      onMouseLeave={(e) => {
                        if (!isActive) {
                          e.currentTarget.style.backgroundColor = 'transparent';
                          e.currentTarget.style.color = tc.textSub;
                        }
                      }}
                    >
                      {item.is_pinned === 1 && (
                        <Pin
                          size={12}
                          fill={isActive ? (isLight ? '#18181b' : '#ffffff') : 'none'}
                          style={{
                            flexShrink: 0,
                            transformOrigin: 'center',
                            transform: isActive ? 'rotate(-90deg)' : 'rotate(0deg)',
                            transition: 'transform 0.3s cubic-bezier(0.34, 1.56, 0.64, 1), fill 0.25s ease, color 0.2s ease',
                            color: isActive ? (isLight ? '#18181b' : '#ffffff') : 'inherit',
                          }}
                        />
                      )}
                      <span
                        style={{
                          overflow: 'hidden',
                          textOverflow: 'ellipsis',
                          whiteSpace: 'nowrap',
                          minWidth: 0,
                        }}
                      >
                        {rawTitle || `通知 ${idx + 1}`}
                      </span>
                      {getAnnouncementDisplayTime(item) && (
                        <span
                          style={{
                            flexShrink: 0,
                            fontSize: '12px',
                            fontWeight: isActive ? 500 : 400,
                            color: 'inherit',
                            transition: 'color 0.2s ease',
                          }}
                        >
                          {formatNoticeTime(getAnnouncementDisplayTime(item))}
                        </span>
                      )}
                    </button>
                  );
                })}
              </div>
            ) : (
              <div
                style={{
                  minWidth: 0,
                  flex: 1,
                  display: 'flex',
                  alignItems: 'center',
                  flexWrap: 'wrap',
                  gap: '8px',
                }}
              >
                {currentNotice.is_pinned === 1 && (
                  <div
                    style={{
                      ...softAccent(themeMode === 'light' ? 'light' : 'dark'),
                      fontSize: 12,
                      padding: '2px 6px',
                      borderRadius: 4,
                      whiteSpace: 'nowrap',
                      flexShrink: 0,
                    }}
                  >
                    {t('common.pinned', '置顶')}
                  </div>
                )}
                <span
                  style={{
                    fontSize: '15px',
                    fontWeight: 500,
                    color: tc.text,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    minWidth: 0,
                  }}
                >
                  {getAnnouncementLabel(currentNotice.title)}
                </span>
                {getAnnouncementDisplayTime(currentNotice) && (
                  <div
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 4,
                      fontSize: 12,
                      color: tc.textSub,
                      flexShrink: 0,
                    }}
                  >
                    <Calendar size={12} />
                    <span>{formatNoticeTime(getAnnouncementDisplayTime(currentNotice))}</span>
                  </div>
                )}
              </div>
            )}

            {/* Close Button */}
            <button
              onClick={handleClose}
              aria-label="Close"
              style={{
                width: '28px',
                height: '28px',
                borderRadius: '50%',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                backgroundColor: tc.btnBg,
                border: `1px solid ${tc.btnBorder}`,
                cursor: 'pointer',
                color: tc.textSub,
                transition: 'all 0.2s cubic-bezier(0.16, 1, 0.3, 1)',
                flexShrink: 0,
              }}
              onMouseEnter={(e) => {
                e.currentTarget.style.backgroundColor = tc.btnHoverBg;
                e.currentTarget.style.borderColor = tc.btnHoverBorder;
                e.currentTarget.style.color = tc.btnHoverText;
                e.currentTarget.style.transform = 'scale(1.06) rotate(90deg)';
              }}
              onMouseLeave={(e) => {
                e.currentTarget.style.backgroundColor = tc.btnBg;
                e.currentTarget.style.borderColor = tc.btnBorder;
                e.currentTarget.style.color = tc.textSub;
                e.currentTarget.style.transform = 'scale(1) rotate(0deg)';
              }}
              onMouseDown={(e) => {
                e.currentTarget.style.transform = 'scale(0.92) rotate(90deg)';
              }}
              onMouseUp={(e) => {
                e.currentTarget.style.transform = 'scale(1.06) rotate(90deg)';
              }}
            >
              <X size={14} />
            </button>
          </div>

          {/* Modal Body: 多条时保留置顶+标题+序号；单条时标题已在顶部 Header 展示 */}
          <div
            key={currentNotice.id || currentIndex}
            style={{
              padding: '18px 20px',
              overflowY: 'auto',
              maxHeight: '460px',
              flex: 1,
            }}
            className="announcement-custom-scrollbar announcement-content-switch"
          >
            {totalCount > 1 && (
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  flexWrap: 'wrap',
                  gap: '8px',
                  marginBottom: 12,
                }}
              >
                {currentNotice.is_pinned === 1 && (
                  <div
                    style={{
                      ...softAccent(themeMode === 'light' ? 'light' : 'dark'),
                      fontSize: 12,
                      padding: '2px 6px',
                      borderRadius: 4,
                      whiteSpace: 'nowrap',
                      flexShrink: 0,
                    }}
                  >
                    {t('common.pinned', '置顶')}
                  </div>
                )}
                <span
                  style={{
                    fontSize: '16px',
                    fontWeight: 500,
                    color: tc.text,
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                    whiteSpace: 'nowrap',
                    minWidth: 0,
                  }}
                >
                  {getAnnouncementLabel(currentNotice.title)}
                </span>
                <span
                  style={{
                    fontSize: '11px',
                    fontWeight: 500,
                    padding: '2px 7px',
                    borderRadius: '10px',
                    flexShrink: 0,
                    backgroundColor: tc.btnBg,
                    color: tc.textSub,
                    border: `1px solid ${tc.btnBorder}`,
                  }}
                >
                  {currentIndex + 1}/{totalCount}
                </span>
              </div>
            )}

            {/* 富文本内容区 */}
            <div
              className="quill-content"
              dangerouslySetInnerHTML={{
                __html: getAnnouncementLabel(currentNotice.content),
              }}
              style={{
                color: isLight ? 'rgba(0, 0, 0, 0.72)' : 'rgba(255, 255, 255, 0.75)',
                fontSize: 13,
                lineHeight: 1.6,
                background: 'transparent',
                padding: 0,
                overflowWrap: 'break-word',
                wordBreak: 'break-all',
              }}
            />
          </div>

          {/* Modal Footer */}
          <div
            style={{
              padding: '12px 20px',
              borderTop: tc.separator,
              display: 'flex',
              flexWrap: 'wrap',
              alignItems: 'center',
              justifyContent: 'space-between',
              gap: '12px',
            }}
          >
            {/* Left: Checkbox */}
            <label
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: '6px',
                cursor: 'pointer',
                userSelect: 'none',
                fontSize: '13px',
                color: tc.textSub,
              }}
            >
              <div
                className={dontShowToday ? 'announcement-check-active' : ''}
                style={{
                  width: '15px',
                  height: '15px',
                  borderRadius: '4px',
                  border: dontShowToday
                    ? 'none'
                    : `1.5px solid ${tc.btnBorder}`,
                  backgroundColor: dontShowToday
                    ? tc.primaryBg
                    : 'transparent',
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  color: tc.primaryText,
                  transition: 'all 0.2s cubic-bezier(0.16, 1, 0.3, 1)',
                }}
              >
                {dontShowToday && <Check size={10} strokeWidth={3} />}
              </div>
              <input
                type="checkbox"
                checked={dontShowToday}
                onChange={(e) => setDontShowToday(e.target.checked)}
                style={{ display: 'none' }}
              />
              <span>{t('header.dont_show_today', '今日不再弹出')}</span>
            </label>

            {/* Right: Buttons */}
            <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
              {totalCount > 1 && (
                <div style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                  <button
                    onClick={handlePrev}
                    disabled={currentIndex === 0}
                    style={{
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: '4px',
                      padding: '5px 11px',
                      borderRadius: '6px',
                      fontSize: '12px',
                      fontWeight: 500,
                      cursor: currentIndex === 0 ? 'not-allowed' : 'pointer',
                      opacity: currentIndex === 0 ? 0.35 : 1,
                      backgroundColor: tc.btnBg,
                      color: tc.btnText,
                      border: `1px solid ${tc.btnBorder}`,
                      transition: 'all 0.2s cubic-bezier(0.16, 1, 0.3, 1)',
                      userSelect: 'none',
                    }}
                    onMouseEnter={(e) => {
                      if (currentIndex !== 0) {
                        e.currentTarget.style.backgroundColor = tc.btnHoverBg;
                        e.currentTarget.style.borderColor = tc.btnHoverBorder;
                        e.currentTarget.style.color = tc.btnHoverText;
                        e.currentTarget.style.transform = 'translateY(-1px)';
                        e.currentTarget.style.boxShadow = isLight
                          ? '0 2px 6px rgba(0,0,0,0.06)'
                          : '0 2px 6px rgba(0,0,0,0.3)';
                      }
                    }}
                    onMouseLeave={(e) => {
                      e.currentTarget.style.backgroundColor = tc.btnBg;
                      e.currentTarget.style.borderColor = tc.btnBorder;
                      e.currentTarget.style.color = tc.btnText;
                      e.currentTarget.style.transform = 'translateY(0)';
                      e.currentTarget.style.boxShadow = 'none';
                    }}
                    onMouseDown={(e) => {
                      if (currentIndex !== 0) {
                        e.currentTarget.style.transform = 'translateY(0) scale(0.96)';
                      }
                    }}
                    onMouseUp={(e) => {
                      if (currentIndex !== 0) {
                        e.currentTarget.style.transform = 'translateY(-1px)';
                      }
                    }}
                  >
                    <ChevronLeft size={13} />
                    <span>{t('common.prev', '上一条')}</span>
                  </button>

                  <button
                    onClick={handleNext}
                    disabled={currentIndex === totalCount - 1}
                    style={{
                      display: 'inline-flex',
                      alignItems: 'center',
                      gap: '4px',
                      padding: '5px 11px',
                      borderRadius: '6px',
                      fontSize: '12px',
                      fontWeight: 500,
                      cursor: currentIndex === totalCount - 1 ? 'not-allowed' : 'pointer',
                      opacity: currentIndex === totalCount - 1 ? 0.35 : 1,
                      backgroundColor: tc.btnBg,
                      color: tc.btnText,
                      border: `1px solid ${tc.btnBorder}`,
                      transition: 'all 0.2s cubic-bezier(0.16, 1, 0.3, 1)',
                      userSelect: 'none',
                    }}
                    onMouseEnter={(e) => {
                      if (currentIndex !== totalCount - 1) {
                        e.currentTarget.style.backgroundColor = tc.btnHoverBg;
                        e.currentTarget.style.borderColor = tc.btnHoverBorder;
                        e.currentTarget.style.color = tc.btnHoverText;
                        e.currentTarget.style.transform = 'translateY(-1px)';
                        e.currentTarget.style.boxShadow = isLight
                          ? '0 2px 6px rgba(0,0,0,0.06)'
                          : '0 2px 6px rgba(0,0,0,0.3)';
                      }
                    }}
                    onMouseLeave={(e) => {
                      e.currentTarget.style.backgroundColor = tc.btnBg;
                      e.currentTarget.style.borderColor = tc.btnBorder;
                      e.currentTarget.style.color = tc.btnText;
                      e.currentTarget.style.transform = 'translateY(0)';
                      e.currentTarget.style.boxShadow = 'none';
                    }}
                    onMouseDown={(e) => {
                      if (currentIndex !== totalCount - 1) {
                        e.currentTarget.style.transform = 'translateY(0) scale(0.96)';
                      }
                    }}
                    onMouseUp={(e) => {
                      if (currentIndex !== totalCount - 1) {
                        e.currentTarget.style.transform = 'translateY(-1px)';
                      }
                    }}
                  >
                    <span>{t('common.next', '下一条')}</span>
                    <ChevronRight size={13} />
                  </button>
                </div>
              )}

              <button
                onClick={handleClose}
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                  padding: '6px 18px',
                  borderRadius: '8px',
                  fontSize: '13px',
                  fontWeight: 500,
                  cursor: 'pointer',
                  backgroundColor: tc.primaryBg,
                  color: tc.primaryText,
                  border: 'none',
                  boxShadow: isLight
                    ? '0 1px 2px rgba(0, 0, 0, 0.08), 0 2px 6px rgba(0, 0, 0, 0.04)'
                    : '0 1px 2px rgba(0, 0, 0, 0.4), 0 2px 6px rgba(0, 0, 0, 0.2)',
                  transition: 'all 0.2s cubic-bezier(0.16, 1, 0.3, 1)',
                  userSelect: 'none',
                }}
                onMouseEnter={(e) => {
                  e.currentTarget.style.opacity = '0.92';
                  e.currentTarget.style.transform = 'translateY(-1px)';
                  e.currentTarget.style.boxShadow = isLight
                    ? '0 4px 12px rgba(0, 0, 0, 0.12)'
                    : '0 4px 14px rgba(0, 0, 0, 0.5)';
                }}
                onMouseLeave={(e) => {
                  e.currentTarget.style.opacity = '1';
                  e.currentTarget.style.transform = 'translateY(0)';
                  e.currentTarget.style.boxShadow = isLight
                    ? '0 1px 2px rgba(0, 0, 0, 0.08), 0 2px 6px rgba(0, 0, 0, 0.04)'
                    : '0 1px 2px rgba(0, 0, 0, 0.4), 0 2px 6px rgba(0, 0, 0, 0.2)';
                }}
                onMouseDown={(e) => {
                  e.currentTarget.style.transform = 'translateY(0) scale(0.97)';
                }}
                onMouseUp={(e) => {
                  e.currentTarget.style.transform = 'translateY(-1px)';
                }}
              >
                {t('header.i_know', '我知道了')}
              </button>
            </div>
          </div>
        </div>
      </Modal>
    </>
  );
};

export default AnnouncementPopupModal;
