import React, { useEffect, useState } from 'react';
import { Progress, Spin, message } from 'antd';
import { Tag as TagIcon, MonitorPlay, Server, Cuboid, CornerDownRight } from 'lucide-react';
import request from '../../utils/request';
import { useThemeStore } from '../../store/theme';

interface RuntimeInfo {
  instance_name: string;
  instance_id: string;
  status: string;
  role: string;
  is_docker?: boolean;
  cpu_percent: number;
  cpu_cores?: number;
  cpu_used_cores?: number;
  memory_percent: number;
  memory_used_bytes?: number;
  memory_total_bytes?: number;
  host_cpu_percent?: number;
  host_cpu_cores?: number;
  host_cpu_used_cores?: number;
  host_memory_percent?: number;
  host_memory_used_bytes?: number;
  host_memory_total_bytes?: number;
  host_started_at?: string;
  host_os?: string;
  host_platform?: string;
  docker_cpu_percent?: number;
  docker_cpu_cores?: number;
  docker_cpu_used_cores?: number;
  docker_cpu_quota_cores?: number | null;
  docker_host_cpu_percent?: number;
  docker_memory_percent?: number;
  docker_memory_used_bytes?: number;
  docker_memory_total_bytes?: number;
  docker_memory_limit_bytes?: number | null;
  docker_host_memory_percent?: number;
  disk_percent: number;
  disk_used_bytes?: number;
  disk_total_bytes?: number;
  platform: string;
  started_at: string;
}

/** 与控制台仪表盘灰底（--dashboard / #f0f4f9）一致 */
const SITE_SURFACE = 'bg-dashboard';
const SITE_SURFACE_SOFT = 'bg-dashboard/80 dark:bg-muted/40';

const formatPct = (v: number) => {
  if (!Number.isFinite(v)) return '0%';
  const rounded = Math.round(v * 10) / 10;
  return `${rounded}%`;
};

/** 容量按 1024 进位，10 G 及以上取整，更小则保留一位。 */
const formatCapacity = (bytes?: number): string => {
  if (!bytes || !Number.isFinite(bytes) || bytes <= 0) return '';
  const g = bytes / (1024 ** 3);
  if (g >= 10) return `${Math.round(g)}G`;
  if (g >= 1) {
    const rounded = Math.round(g * 10) / 10;
    return `${Number.isInteger(rounded) ? rounded.toFixed(0) : rounded.toFixed(1)}G`;
  }
  const m = bytes / (1024 ** 2);
  return `${Math.max(1, Math.round(m))}M`;
};

const formatUsedBytes = (bytes?: number): string => {
  if (bytes === undefined || !Number.isFinite(bytes) || bytes < 0) return '';
  if (bytes === 0) return '0M';
  const g = bytes / (1024 ** 3);
  if (g >= 1) {
    const rounded = Math.round(g * 10) / 10;
    return `${Number.isInteger(rounded) ? rounded.toFixed(0) : rounded.toFixed(1)}G`;
  }
  const m = bytes / (1024 ** 2);
  return `${Math.max(1, Math.round(m))}M`;
};

const formatBytesUsage = (usedBytes?: number, totalBytes?: number): string => {
  const used = formatUsedBytes(usedBytes);
  const total = formatCapacity(totalBytes);
  if (!used || !total) return '';
  return `${used} / ${total}`;
};

const cpuCoreLabel = (cores?: number): string => {
  if (!cores || !Number.isFinite(cores) || cores <= 0) return '';
  const rounded = Math.round(cores * 100) / 100;
  return `${rounded}核`;
};

const formatCpuUsage = (usedCores?: number, totalCores?: number): string => {
  if (usedCores === undefined || !Number.isFinite(usedCores) || !totalCores || totalCores <= 0) return '';
  const used = Math.round(Math.max(0, usedCores) * 100) / 100;
  const total = Math.round(totalCores * 100) / 100;
  return `${used} / ${total}核`;
};

const formatTimeWithTz = (raw?: string | null, fallbackTz?: string): string => {
  if (!raw) return '-';
  const trimmed = raw.trim();
  if (!trimmed || trimmed === '-') return '-';
  if (trimmed.includes('(UTC') || trimmed.endsWith('UTC')) {
    return trimmed;
  }
  const match = trimmed.match(/^(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2})\s+([+-]\d{2}):?(\d{2})$/);
  if (match) {
    const [, dt, signHours, minutesStr] = match;
    const sign = signHours[0];
    const hours = parseInt(signHours.slice(1), 10);
    const minutes = parseInt(minutesStr, 10);
    if (hours === 0 && minutes === 0) {
      return `${dt} (UTC)`;
    }
    if (minutes === 0) {
      return `${dt} (UTC${sign}${hours})`;
    }
    return `${dt} (UTC${sign}${hours}:${minutesStr})`;
  }
  const zMatch = trimmed.match(/^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2}:\d{2})Z$/);
  if (zMatch) {
    return `${zMatch[1]} ${zMatch[2]} (UTC)`;
  }
  if (fallbackTz && /^\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}$/.test(trimmed)) {
    return `${trimmed} (${fallbackTz})`;
  }
  return trimmed;
};

const ringStroke = (pct: number, isLight: boolean) => {
  if (pct >= 90) return '#ef4444';
  if (pct >= 70) return '#f59e0b';
  return isLight ? '#16a34a' : '#4ade80';
};

const MetricRing: React.FC<{
  percent: number;
  isLight: boolean;
  label?: string;
  detail?: string;
  subText?: string;
  extraSubText?: string;
  stacked?: boolean;
}> = ({ percent, isLight, label, detail, subText, extraSubText, stacked }) => {
  const pct = Math.max(0, Math.min(100, Number.isFinite(percent) ? percent : 0));
  return (
    <div className={stacked ? 'flex flex-col items-center gap-1' : 'flex items-center'}>
      {label && (
        <span className="text-[10px] text-muted-foreground leading-none">{label}</span>
      )}
      <div className="flex items-center gap-1.5">
        <Progress
          type="circle"
          percent={pct}
          size={stacked ? 48 : 44}
          strokeWidth={8}
          strokeColor={ringStroke(pct, isLight)}
          trailColor={isLight ? '#e2e8f0' : 'rgba(255,255,255,0.1)'}
          format={() => (
            detail ? (
              <span className={`${detail.length >= 4 ? 'text-[10px]' : 'text-[11px]'} font-medium leading-none text-foreground`}>{detail}</span>
            ) : null
          )}
        />
        <div className="flex flex-col justify-center gap-0.5 leading-none">
          <span className="text-[11px] font-medium tabular-nums leading-none text-foreground">{formatPct(pct)}</span>
          {!stacked && subText && (
            <span className="text-[10px] tabular-nums leading-none text-muted-foreground whitespace-nowrap">{subText}</span>
          )}
          {!stacked && extraSubText && (
            <span className="text-[10px] tabular-nums leading-none text-muted-foreground/80 whitespace-nowrap">{extraSubText}</span>
          )}
        </div>
      </div>
      {stacked && subText && (
        <span className="text-[10px] tabular-nums leading-none text-muted-foreground whitespace-nowrap">{subText}</span>
      )}
      {stacked && extraSubText && (
        <span className="text-[10px] tabular-nums leading-none text-muted-foreground/80 whitespace-nowrap">{extraSubText}</span>
      )}
    </div>
  );
};

const Badge: React.FC<{
  children: React.ReactNode;
  variant?: 'default' | 'secondary' | 'outline' | 'success';
}> = ({ children, variant = 'secondary' }) => {
  const styles = {
    default: 'border-transparent bg-primary text-primary-foreground',
    secondary: 'border-border bg-muted text-muted-foreground',
    outline: 'border-border bg-background text-foreground',
    success: 'border-transparent bg-emerald-500/10 text-emerald-700 dark:text-emerald-400',
  }[variant];
  return (
    <span className={`inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[11px] font-medium leading-none transition-colors ${styles}`}>
      {children}
    </span>
  );
};

const Card: React.FC<{ children: React.ReactNode; className?: string }> = ({ children, className = '' }) => (
  <div className={`rounded-lg border border-border bg-card text-card-foreground shadow-sm ${className}`}>
    {children}
  </div>
);

interface RuntimeRowView {
  key: string;
  name: string;
  subId: string;
  online: boolean;
  statusText: string;
  roleBadge: string;
  typeBadge: string;
  isChild?: boolean;
  hasChild?: boolean;
  cpuPercent: number;
  cpuDetail: string;
  cpuSub: string;
  cpuExtraSub?: string;
  memPercent: number;
  memDetail: string;
  memSub: string;
  memExtraSub?: string;
  diskPercent: number;
  diskDetail: string;
  diskSub: string;
  startedAt: string;
}

const RuntimePanel: React.FC<{ runtime: RuntimeInfo; isLight: boolean }> = ({ runtime, isLight }) => {
  const online = runtime.status === 'online';
  const diskDetail = formatCapacity(runtime.disk_total_bytes);
  const diskSub = formatBytesUsage(runtime.disk_used_bytes, runtime.disk_total_bytes);

  const hostCpuPercent = runtime.host_cpu_percent ?? runtime.cpu_percent;
  const hostCpuCores = runtime.host_cpu_cores ?? runtime.cpu_cores;
  const hostCpuUsed = runtime.host_cpu_used_cores ?? runtime.cpu_used_cores;
  const hostMemPercent = runtime.host_memory_percent ?? runtime.memory_percent;
  const hostMemTotal = runtime.host_memory_total_bytes ?? runtime.memory_total_bytes;
  const hostMemUsed = runtime.host_memory_used_bytes ?? runtime.memory_used_bytes;

  const dockerCpuPercent = runtime.docker_cpu_percent ?? runtime.cpu_percent;
  const dockerCpuCores = runtime.docker_cpu_cores ?? runtime.cpu_cores;
  const dockerCpuUsed = runtime.docker_cpu_used_cores ?? runtime.cpu_used_cores;
  const dockerMemPercent = runtime.docker_memory_percent ?? runtime.memory_percent;
  const dockerMemTotal = runtime.docker_memory_total_bytes ?? runtime.memory_total_bytes;
  const dockerMemUsed = runtime.docker_memory_used_bytes ?? runtime.memory_used_bytes;

  const dockerHostCpuPct =
    runtime.docker_host_cpu_percent !== undefined
      ? runtime.docker_host_cpu_percent
      : hostCpuCores && hostCpuCores > 0 && dockerCpuUsed !== undefined
        ? (dockerCpuUsed / hostCpuCores) * 100
        : undefined;
  const dockerHostMemPct =
    runtime.docker_host_memory_percent !== undefined
      ? runtime.docker_host_memory_percent
      : hostMemTotal && hostMemTotal > 0 && dockerMemUsed !== undefined
        ? (dockerMemUsed / hostMemTotal) * 100
        : undefined;

  const rows: RuntimeRowView[] = runtime.is_docker
    ? [
        {
          key: 'host',
          name: '服务器 (宿主机)',
          subId: 'host',
          online: true,
          statusText: '在线',
          roleBadge: 'host',
          typeBadge: `${runtime.host_platform || runtime.host_os || runtime.platform || '-'} (服务器)`,
          hasChild: true,
          cpuPercent: hostCpuPercent,
          cpuDetail: cpuCoreLabel(hostCpuCores),
          cpuSub: formatCpuUsage(hostCpuUsed, hostCpuCores),
          memPercent: hostMemPercent,
          memDetail: formatCapacity(hostMemTotal),
          memSub: formatBytesUsage(hostMemUsed, hostMemTotal),
          diskPercent: runtime.disk_percent,
          diskDetail,
          diskSub,
          startedAt: formatTimeWithTz(runtime.host_started_at || runtime.started_at, 'UTC'),
        },
        {
          key: 'docker',
          name: runtime.instance_name || '-',
          subId: runtime.instance_id || '-',
          online,
          statusText: online ? '在线' : (runtime.status || '未知'),
          roleBadge: runtime.role || 'master',
          typeBadge: `${runtime.platform || '-'} (Docker)`,
          isChild: true,
          cpuPercent: dockerCpuPercent,
          cpuDetail: cpuCoreLabel(dockerCpuCores),
          cpuSub: formatCpuUsage(dockerCpuUsed, dockerCpuCores),
          cpuExtraSub: dockerHostCpuPct !== undefined ? `占服务器 ${formatPct(dockerHostCpuPct)}` : undefined,
          memPercent: dockerMemPercent,
          memDetail: formatCapacity(dockerMemTotal),
          memSub: formatBytesUsage(dockerMemUsed, dockerMemTotal),
          memExtraSub: dockerHostMemPct !== undefined ? `占服务器 ${formatPct(dockerHostMemPct)}` : undefined,
          diskPercent: runtime.disk_percent,
          diskDetail,
          diskSub,
          startedAt: formatTimeWithTz(runtime.started_at, 'UTC'),
        },
      ]
    : [
        {
          key: 'server',
          name: runtime.instance_name || '-',
          subId: runtime.instance_id || '-',
          online,
          statusText: online ? '在线' : (runtime.status || '未知'),
          roleBadge: runtime.role || 'master',
          typeBadge: runtime.host_platform || runtime.host_os || runtime.platform || '-',
          cpuPercent: runtime.cpu_percent,
          cpuDetail: cpuCoreLabel(runtime.cpu_cores),
          cpuSub: formatCpuUsage(runtime.cpu_used_cores, runtime.cpu_cores),
          memPercent: runtime.memory_percent,
          memDetail: formatCapacity(runtime.memory_total_bytes),
          memSub: formatBytesUsage(runtime.memory_used_bytes, runtime.memory_total_bytes),
          diskPercent: runtime.disk_percent,
          diskDetail,
          diskSub,
          startedAt: formatTimeWithTz(runtime.started_at, 'UTC'),
        },
      ];

  const primaryRow = rows[0];
  const childRow = rows[1];

  return (
    <Card>

      {/* 手机端：宿主卡片内嵌二级 Docker 容器卡片 */}
      <div className="md:hidden p-3">
        <div className={`rounded-md border border-border px-3 py-2.5 space-y-2.5 ${SITE_SURFACE_SOFT}`}>
          <div className="flex items-center justify-between gap-2">
            <div className="flex items-center gap-2 min-w-0">
              <div className="w-7 h-7 rounded-md border border-border bg-background flex items-center justify-center shrink-0 relative">
                <Server className="w-3.5 h-3.5 text-foreground" />
                <span
                  className={`absolute -bottom-0.5 -right-0.5 w-2 h-2 rounded-full ring-2 ring-card ${
                    primaryRow.online ? 'bg-emerald-500' : 'bg-muted-foreground/40'
                  }`}
                />
              </div>
              <div className="min-w-0">
                <div className="text-xs font-semibold text-foreground truncate">
                  {primaryRow.name}
                </div>
                <div className="text-[10px] font-mono text-muted-foreground truncate leading-tight">
                  {primaryRow.subId}
                </div>
              </div>
            </div>
            <div className="flex items-center gap-1 shrink-0">
              <Badge variant={primaryRow.online ? 'success' : 'secondary'}>
                <span className={`w-1 h-1 rounded-full ${primaryRow.online ? 'bg-emerald-500' : 'bg-muted-foreground'}`} />
                {primaryRow.statusText}
              </Badge>
              <Badge variant="outline">{primaryRow.roleBadge}</Badge>
              {primaryRow.typeBadge && <Badge variant="secondary">{primaryRow.typeBadge}</Badge>}
            </div>
          </div>

          <div className="grid grid-cols-3 gap-1.5 pt-2 border-t border-border">
            <div className={`rounded-md border border-border py-1.5 ${SITE_SURFACE}`}>
              <MetricRing percent={primaryRow.cpuPercent} isLight={isLight} label="CPU" detail={primaryRow.cpuDetail} subText={primaryRow.cpuSub} extraSubText={primaryRow.cpuExtraSub} stacked />
            </div>
            <div className={`rounded-md border border-border py-1.5 ${SITE_SURFACE}`}>
              <MetricRing percent={primaryRow.memPercent} isLight={isLight} label="内存" detail={primaryRow.memDetail} subText={primaryRow.memSub} extraSubText={primaryRow.memExtraSub} stacked />
            </div>
            <div className={`rounded-md border border-border py-1.5 ${SITE_SURFACE}`}>
              <MetricRing percent={primaryRow.diskPercent} isLight={isLight} label="存储" detail={primaryRow.diskDetail} subText={primaryRow.diskSub} stacked />
            </div>
          </div>

          <div className="space-y-1.5 pt-2 border-t border-border">
            <div className="flex items-center justify-between gap-2 text-[11px]">
              <span className="text-muted-foreground shrink-0">启动时间</span>
              <span className="tabular-nums text-foreground whitespace-nowrap">{primaryRow.startedAt}</span>
            </div>
          </div>

          {childRow && (
            <div className="pt-2 border-t border-dashed border-border">
              <div className="flex items-center gap-1 text-[10px] text-muted-foreground mb-1.5">
                <CornerDownRight className="w-3 h-3 shrink-0" />
                <span>所属二级容器实例</span>
              </div>
              <div className="ml-1.5 pl-2.5 border-l-2 border-border">
                <div className={`rounded-md border border-border/80 p-2.5 space-y-2 ${SITE_SURFACE}`}>
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-2 min-w-0">
                      <div className="w-6 h-6 rounded-md border border-border/80 bg-background/80 flex items-center justify-center shrink-0 relative">
                        <Cuboid className="w-3 h-3 text-muted-foreground" />
                        <span
                          className={`absolute -bottom-0.5 -right-0.5 w-1.5 h-1.5 rounded-full ring-2 ring-card ${
                            childRow.online ? 'bg-emerald-500' : 'bg-muted-foreground/40'
                          }`}
                        />
                      </div>
                      <div className="min-w-0">
                        <div className="text-xs font-medium text-foreground truncate">
                          {childRow.name}
                        </div>
                        <div className="text-[10px] font-mono text-muted-foreground truncate leading-tight">
                          容器 · {childRow.subId}
                        </div>
                      </div>
                    </div>
                    <div className="flex items-center gap-1 shrink-0">
                      <Badge variant={childRow.online ? 'success' : 'secondary'}>
                        <span className={`w-1 h-1 rounded-full ${childRow.online ? 'bg-emerald-500' : 'bg-muted-foreground'}`} />
                        {childRow.statusText}
                      </Badge>
                      <Badge variant="outline">{childRow.roleBadge}</Badge>
                      {childRow.typeBadge && <Badge variant="secondary">{childRow.typeBadge}</Badge>}
                    </div>
                  </div>

                  <div className="grid grid-cols-3 gap-1.5 pt-2 border-t border-border/70">
                    <div className="rounded-md border border-border/70 py-1.5 bg-card/60">
                      <MetricRing percent={childRow.cpuPercent} isLight={isLight} label="CPU" detail={childRow.cpuDetail} subText={childRow.cpuSub} extraSubText={childRow.cpuExtraSub} stacked />
                    </div>
                    <div className="rounded-md border border-border/70 py-1.5 bg-card/60">
                      <MetricRing percent={childRow.memPercent} isLight={isLight} label="内存" detail={childRow.memDetail} subText={childRow.memSub} extraSubText={childRow.memExtraSub} stacked />
                    </div>
                    <div className="rounded-md border border-border/70 py-1.5 bg-card/60">
                      <MetricRing percent={childRow.diskPercent} isLight={isLight} label="存储" detail={childRow.diskDetail} subText={childRow.diskSub} stacked />
                    </div>
                  </div>

                  <div className="flex items-center justify-between gap-2 pt-1.5 border-t border-border/70 text-[11px]">
                    <span className="text-muted-foreground shrink-0">启动时间</span>
                    <span className="tabular-nums text-foreground whitespace-nowrap">{childRow.startedAt}</span>
                  </div>
                </div>
              </div>
            </div>
          )}
        </div>
      </div>

      {/* 桌面端：table 保证表头与数据列严格对齐，Docker 作为宿主机二级子行树形嵌套 */}
      <div className="hidden md:block overflow-x-auto p-3">
        <div className={`rounded-md border border-border overflow-hidden ${SITE_SURFACE_SOFT}`}>
          <table className="w-full border-collapse text-left">
            <thead>
              <tr className="text-[11px] text-muted-foreground border-b border-border">
                <th className="font-normal px-3 py-1.5 whitespace-nowrap">实例</th>
                <th className="font-normal px-2.5 py-1.5 whitespace-nowrap">状态</th>
                <th className="font-normal px-2.5 py-1.5 whitespace-nowrap">角色</th>
                <th className="font-normal px-2.5 py-1.5 whitespace-nowrap">CPU</th>
                <th className="font-normal px-2.5 py-1.5 whitespace-nowrap">内存</th>
                <th className="font-normal px-2.5 py-1.5 whitespace-nowrap">存储</th>
                <th className="font-normal px-2.5 py-1.5 whitespace-nowrap">启动时间</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row, idx) => (
                <tr
                  key={row.key}
                  className={`align-middle ${
                    row.isChild
                      ? 'border-t border-dashed border-border/80 bg-muted/30 dark:bg-black/20'
                      : idx > 0
                        ? 'border-t border-border'
                        : 'bg-card/60 dark:bg-card/30'
                  }`}
                >
                  <td className={`relative min-w-[190px] ${row.isChild ? 'pl-[25px] pr-2.5 py-2' : 'px-3 py-2.5'}`}>
                    {row.hasChild && (
                      <span className="absolute left-[25px] top-[38px] bottom-0 w-px bg-border pointer-events-none" />
                    )}
                    {row.isChild ? (
                      <>
                        <span className="absolute left-[25px] top-0 h-1/2 w-3.5 border-l border-b border-border rounded-bl-md pointer-events-none" />
                        <div className="pl-5 flex items-center gap-2 min-w-0">
                          <div className="w-6 h-6 rounded-md border border-border/80 bg-background/80 flex items-center justify-center shrink-0 relative">
                            <Cuboid className="w-3 h-3 text-muted-foreground" />
                            <span
                              className={`absolute -bottom-0.5 -right-0.5 w-1.5 h-1.5 rounded-full ring-2 ring-card ${
                                row.online ? 'bg-emerald-500' : 'bg-muted-foreground/40'
                              }`}
                            />
                          </div>
                          <div className="min-w-0 leading-tight">
                            <div className="text-xs font-medium text-foreground truncate max-w-[160px]">
                              {row.name}
                            </div>
                            <div className="text-[10px] font-mono text-muted-foreground truncate max-w-[160px]">
                              容器 · {row.subId}
                            </div>
                          </div>
                        </div>
                      </>
                    ) : (
                      <div className="flex items-center gap-2 min-w-0">
                        <div className="w-7 h-7 rounded-md border border-border bg-background flex items-center justify-center shrink-0 relative">
                          <Server className="w-3.5 h-3.5 text-foreground" />
                          <span
                            className={`absolute -bottom-0.5 -right-0.5 w-2 h-2 rounded-full ring-2 ring-card ${
                              row.online ? 'bg-emerald-500' : 'bg-muted-foreground/40'
                            }`}
                          />
                        </div>
                        <div className="min-w-0 leading-tight">
                          <div className="text-xs font-semibold text-foreground truncate max-w-[180px]">
                            {row.name}
                          </div>
                          <div className="text-[10px] font-mono text-muted-foreground truncate max-w-[180px]">
                            {row.subId}
                          </div>
                        </div>
                      </div>
                    )}
                  </td>
                  <td className="px-2.5 py-2 whitespace-nowrap">
                    <Badge variant={row.online ? 'success' : 'secondary'}>
                      <span className={`w-1 h-1 rounded-full ${row.online ? 'bg-emerald-500' : 'bg-muted-foreground'}`} />
                      {row.statusText}
                    </Badge>
                  </td>
                  <td className="px-2.5 py-2 whitespace-nowrap">
                    <div className="flex items-center gap-1">
                      <Badge variant="outline">{row.roleBadge}</Badge>
                      {row.typeBadge && <Badge variant="secondary">{row.typeBadge}</Badge>}
                    </div>
                  </td>
                  <td className="px-2.5 py-2 whitespace-nowrap">
                    <MetricRing percent={row.cpuPercent} isLight={isLight} detail={row.cpuDetail} subText={row.cpuSub} extraSubText={row.cpuExtraSub} />
                  </td>
                  <td className="px-2.5 py-2 whitespace-nowrap">
                    <MetricRing percent={row.memPercent} isLight={isLight} detail={row.memDetail} subText={row.memSub} extraSubText={row.memExtraSub} />
                  </td>
                  <td className="px-2.5 py-2 whitespace-nowrap">
                    <MetricRing percent={row.diskPercent} isLight={isLight} detail={row.diskDetail} subText={row.diskSub} />
                  </td>
                  <td className="px-2.5 py-2 text-xs tabular-nums text-foreground whitespace-nowrap">
                    {row.startedAt}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </Card>
  );
};

const SystemAbout: React.FC = () => {
  const { themeMode } = useThemeStore();
  const isLight = themeMode === 'light';
  const [loading, setLoading] = useState(true);
  const [version, setVersion] = useState<string | null>(null);
  const [runtime, setRuntime] = useState<RuntimeInfo | null>(null);

  useEffect(() => {
    const fetchInfo = async () => {
      try {
        const res = await request.get('/system/about') as any;
        if (res?.success) {
          setVersion(res.version || null);
          setRuntime(res.runtime || null);
        } else {
          message.error('获取系统信息失败');
        }
      } catch (e: any) {
        message.error(e.message || '获取系统信息失败');
      } finally {
        setLoading(false);
      }
    };
    fetchInfo();
  }, []);

  if (loading) {
    return (
      <div className="flex items-center justify-center h-[60vh]">
        <Spin size="large" />
      </div>
    );
  }

  return (
    <div className="max-w-6xl mx-auto space-y-4 pb-8 text-foreground">
      <div className="flex flex-col gap-0.5 pb-3 mb-1 border-b border-border">
        <div className="text-[13px] font-bold text-foreground leading-tight">系统关于</div>
        <div className="text-[10.5px] text-muted-foreground leading-normal">
          查看 laiai 运行状态和当前构建版本
        </div>
      </div>

      {runtime && <RuntimePanel runtime={runtime} isLight={isLight} />}

      {version && (
        <Card className="flex items-center gap-3 px-5 py-4">
          <TagIcon className="w-5 h-5 text-muted-foreground" />
          <h2 className="text-sm font-semibold m-0">系统版本</h2>
          <Badge variant="secondary">laiai {version}</Badge>
        </Card>
      )}
    </div>
  );
};

export default SystemAbout;
