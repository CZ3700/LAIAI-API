import React from 'react';

export interface ArtPlayerPreviewProps {
  url: string;
  poster?: string;
  autoplay?: boolean;
  muted?: boolean;
  locale?: string;
  theme?: string;
  onReady?: () => void;
  onError?: () => void;
  onLoadedMetadata?: (info: { width: number; height: number; duration?: number }) => void;
  className?: string;
  style?: React.CSSProperties;
}

const ArtPlayerPreview: React.FC<ArtPlayerPreviewProps> = ({
  url,
  poster,
  autoplay = false,
  muted = false,
  onReady,
  onError,
  onLoadedMetadata,
  className,
  style,
}) => {
  return (
    <video
      src={url}
      poster={poster}
      controls
      autoPlay={autoplay}
      muted={muted}
      playsInline
      onCanPlay={() => onReady?.()}
      onError={() => onError?.()}
      onLoadedMetadata={(e) => {
        const v = e.currentTarget;
        onLoadedMetadata?.({
          width: v.videoWidth || 0,
          height: v.videoHeight || 0,
          duration: Number.isFinite(v.duration) ? v.duration : undefined,
        });
      }}
      className={className}
      style={{
        width: '100%',
        maxHeight: '72vh',
        borderRadius: 8,
        background: '#000',
        display: 'block',
        ...style,
      }}
    />
  );
};

export default ArtPlayerPreview;
