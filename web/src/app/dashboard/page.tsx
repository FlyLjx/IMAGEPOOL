"use client";

import { useEffect, useRef, useState } from "react";
import { Alert, Button, Card, Empty, Progress, Select, Skeleton, Tag, Typography } from "antd";
import {
  Activity,
  AlertCircle,
  Cpu,
  Gauge,
  HardDrive,
  LoaderCircle,
  MemoryStick,
  Network,
  RefreshCw,
  TimerReset,
  UsersRound,
} from "lucide-react";
import { toast } from "sonner";

import { fetchDashboard, fetchSystemLoad, type DashboardSummary, type SystemLoad } from "@/lib/api";
import { formatShanghaiDateTime, formatShanghaiDateTimeParts } from "@/lib/datetime";
import { useAuthGuard } from "@/lib/use-auth-guard";
import { cn } from "@/lib/utils";

function numberText(value: unknown) {
  const numeric = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(numeric)) {
    return "0";
  }
  if (numeric >= 10000) {
    return `${(numeric / 10000).toFixed(1)}w`;
  }
  if (numeric >= 1000) {
    return `${(numeric / 1000).toFixed(1)}k`;
  }
  return String(numeric);
}

function percent(value: number, total: number) {
  if (total <= 0) {
    return 0;
  }
  return Math.round((value / total) * 100);
}

function rateText(value: unknown) {
  const numeric = typeof value === "number" ? value : Number(value);
  if (!Number.isFinite(numeric) || numeric === 0) {
    return "0";
  }

  const digits = Math.abs(numeric) < 10 ? 2 : 1;
  return numeric
    .toFixed(digits)
    .replace(/\.0+$/, "")
    .replace(/(\.\d*?)0+$/, "$1");
}

function finiteNumber(value: unknown) {
  const numeric = typeof value === "number" ? value : Number(value);
  return Number.isFinite(numeric) ? numeric : 0;
}

function formatBytes(value: unknown) {
  const bytes = Math.max(0, finiteNumber(value));
  if (bytes < 1024) {
    return `${Math.round(bytes)} B`;
  }

  const units = ["KB", "MB", "GB", "TB", "PB"];
  const unitIndex = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)) - 1, units.length - 1);
  const normalized = bytes / 1024 ** (unitIndex + 1);
  const digits = normalized >= 100 ? 0 : normalized >= 10 ? 1 : 2;
  return `${normalized.toFixed(digits).replace(/\.0+$/, "").replace(/(\.\d*?)0+$/, "$1")} ${units[unitIndex]}`;
}

function formatRate(value: unknown) {
  return `${formatBytes(value)}/s`;
}

function loadPercent(value: unknown) {
  return clamp(finiteNumber(value), 0, 100);
}

function newerSystemLoad(current: SystemLoad | null, next: SystemLoad) {
  if (!current) {
    return next;
  }
  const currentTime = Date.parse(current.sampled_at);
  const nextTime = Date.parse(next.sampled_at);
  if (Number.isFinite(currentTime) && Number.isFinite(nextTime) && nextTime < currentTime) {
    return current;
  }
  return next;
}

const RUNTIME_WINDOW_OPTIONS = [
  { value: 60, label: "最近 60 分钟" },
  { value: 24 * 60, label: "最近 24 小时" },
  { value: 7 * 24 * 60, label: "最近 7 天" },
  { value: 30 * 24 * 60, label: "最近 30 天" },
];

function runtimeWindowText(minutes: number) {
  if (minutes === 60) {
    return "60 分钟";
  }
  if (minutes > 0 && minutes % (24 * 60) === 0) {
    return `${minutes / (24 * 60)} 天`;
  }
  if (minutes > 0 && minutes % 60 === 0) {
    return `${minutes / 60} 小时`;
  }
  return `${minutes} 分钟`;
}

function sortedEntries(source?: Record<string, number>, limit = 5) {
  return Object.entries(source || {})
    .filter(([, value]) => Number(value) > 0)
    .sort((left, right) => right[1] - left[1])
    .slice(0, limit);
}

function MetricCard({
  title,
  value,
  helper,
  icon: Icon,
  tone = "blue",
}: {
  title: string;
  value: string | number;
  helper: string;
  icon: typeof Activity;
  tone?: "blue" | "green" | "amber" | "rose" | "slate";
}) {
  const toneClass = {
    blue: "bg-blue-50 text-blue-600",
    green: "bg-emerald-50 text-emerald-600",
    amber: "bg-amber-50 text-amber-600",
    rose: "bg-rose-50 text-rose-600",
    slate: "bg-slate-100 text-slate-600",
  }[tone];

  return (
    <Card className="h-full">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="text-sm font-medium text-slate-500">{title}</div>
          <div className="mt-3 text-3xl font-semibold tracking-normal text-slate-950">{value}</div>
          <div className="mt-2 text-sm text-slate-400">{helper}</div>
        </div>
        <div className={cn("flex size-10 shrink-0 items-center justify-center rounded-lg", toneClass)}>
          <Icon className="size-5" />
        </div>
      </div>
    </Card>
  );
}

function EntryBars({ items, emptyText = "暂无数据" }: { items: Array<[string, number]>; emptyText?: string }) {
  const maxValue = Math.max(...items.map(([, value]) => value), 0);
  if (!items.length) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={emptyText} />;
  }
  return (
    <div className="space-y-3">
      {items.map(([label, value]) => (
        <div key={label} className="space-y-1.5">
          <div className="flex items-center justify-between gap-3 text-sm">
            <span className="min-w-0 truncate text-slate-600">{label}</span>
            <span className="font-mono text-xs font-semibold text-slate-500">{numberText(value)}</span>
          </div>
          <div className="h-2 overflow-hidden rounded-full bg-slate-100">
            <div className="h-full rounded-full bg-blue-500" style={{ width: `${maxValue > 0 ? Math.max(6, Math.round((value / maxValue) * 100)) : 0}%` }} />
          </div>
        </div>
      ))}
    </div>
  );
}

type RuntimeHealthData = NonNullable<DashboardSummary["calls"]["runtime"]>;

function runtimeStatusColor(status: string) {
  return {
    success: "#10b981",
    failed: "#f43f5e",
    running: "#3b82f6",
    other: "#94a3b8",
  }[status] || "#94a3b8";
}

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value));
}

function smoothLinePath(points: Array<{ x: number; y: number }>, minY: number, maxY: number) {
  if (points.length === 0) return "";
  if (points.length === 1) return `M ${points[0].x} ${points[0].y}`;
  let path = `M ${points[0].x} ${points[0].y}`;
  for (let index = 0; index < points.length - 1; index += 1) {
    const previous = points[Math.max(0, index - 1)];
    const current = points[index];
    const next = points[index + 1];
    const afterNext = points[Math.min(points.length - 1, index + 2)];
    const cp1x = current.x + (next.x - previous.x) / 6;
    const cp1y = clamp(current.y + (next.y - previous.y) / 6, minY, maxY);
    const cp2x = next.x - (afterNext.x - current.x) / 6;
    const cp2y = clamp(next.y - (afterNext.y - current.y) / 6, minY, maxY);
    path += ` C ${cp1x} ${cp1y}, ${cp2x} ${cp2y}, ${next.x} ${next.y}`;
  }
  return path;
}

function smoothAreaPath(points: Array<{ x: number; y: number }>, baselineY: number, minY: number, maxY: number) {
  if (points.length === 0) return "";
  if (points.length === 1) {
    return `M ${points[0].x} ${baselineY} L ${points[0].x} ${points[0].y} L ${points[0].x} ${baselineY} Z`;
  }
  const line = smoothLinePath(points, minY, maxY);
  const first = points[0];
  const last = points[points.length - 1];
  return `${line} L ${last.x} ${baselineY} L ${first.x} ${baselineY} Z`;
}

function RuntimeTrendChart({ series, windowMinutes }: { series: RuntimeHealthData["series"]; windowMinutes: number }) {
  const points = series || [];
  const total = points.reduce((sum, item) => sum + Number(item.success || 0) + Number(item.failed || 0), 0);
  const chartContainerRef = useRef<HTMLDivElement>(null);
  const [chartContainerWidth, setChartContainerWidth] = useState(720);
  const height = 240;
  const width = Math.max(720, chartContainerWidth);
  const paddingX = 38;
  const paddingTop = 18;
  const paddingBottom = 52;
  const plotWidth = width - paddingX * 2;
  const plotHeight = height - paddingTop - paddingBottom;
  const maxValue = Math.max(1, ...points.flatMap((item) => [Number(item.success || 0), Number(item.failed || 0)]));
  const bottomY = paddingTop + plotHeight;
  const xFor = (index: number) => paddingX + (points.length <= 1 ? plotWidth / 2 : (index / (points.length - 1)) * plotWidth);
  const yFor = (value: number) => paddingTop + (1 - value / maxValue) * plotHeight;
  const labelIndexes = Array.from(new Set([0, Math.floor((points.length - 1) / 2), points.length - 1])).filter((index) => index >= 0);
  const nonZeroBucketCount = points.filter((item) => Number(item.success || 0) > 0 || Number(item.failed || 0) > 0).length;
  const showValueLabels = points.length <= 60 && nonZeroBucketCount <= 14;
  const showPointMarkers = points.length <= 360;
  const chartPoints = points.map((item, index) => {
    const success = Number(item.success || 0);
    const failed = Number(item.failed || 0);
    return {
      item,
      index,
      x: xFor(index),
      success,
      failed,
      successY: yFor(success),
      failedY: yFor(failed),
    };
  });
  const successLinePoints = chartPoints.map((item) => ({ x: item.x, y: item.successY }));
  const failedLinePoints = chartPoints.map((item) => ({ x: item.x, y: item.failedY }));
  const successPath = smoothLinePath(successLinePoints, paddingTop, bottomY);
  const failedPath = smoothLinePath(failedLinePoints, paddingTop, bottomY);
  const successAreaPath = smoothAreaPath(successLinePoints, bottomY, paddingTop, bottomY);
  const failedAreaPath = smoothAreaPath(failedLinePoints, bottomY, paddingTop, bottomY);

  useEffect(() => {
    const element = chartContainerRef.current;
    if (!element) return;

    const updateWidth = () => {
      const nextWidth = Math.round(element.getBoundingClientRect().width);
      if (nextWidth > 0) {
        setChartContainerWidth((currentWidth) => currentWidth === nextWidth ? currentWidth : nextWidth);
      }
    };

    updateWidth();
    const observer = new ResizeObserver(updateWidth);
    observer.observe(element);
    return () => observer.disconnect();
  }, [total]);

  if (!points.length || total <= 0) {
    return (
      <div className="flex min-h-[290px] items-center justify-center rounded-lg bg-slate-50">
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={`最近 ${runtimeWindowText(windowMinutes)} 暂无调用`} />
      </div>
    );
  }

  return (
    <div className="min-h-[290px] rounded-xl border border-slate-100 bg-white px-3 pb-3 pt-3 shadow-sm">
      <div className="mb-2 flex flex-wrap items-center gap-4 text-xs">
        <span className="inline-flex items-center gap-1.5 text-slate-600"><i className="size-2 rounded-full bg-emerald-500" />成功 / 分钟</span>
        <span className="inline-flex items-center gap-1.5 text-slate-600"><i className="size-2 rounded-full bg-rose-500" />失败 / 分钟</span>
        <span className="rounded-full bg-slate-100 px-2 py-0.5 font-medium text-slate-500">曲线视图</span>
        <span className="ml-auto font-mono text-slate-400">max {maxValue}</span>
      </div>
      <div ref={chartContainerRef} className="w-full">
        <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label="每分钟成功和失败调用曲线图" className="h-[255px] w-full overflow-visible">
        <defs>
          <linearGradient id="runtime-success-area" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stopColor="#10b981" stopOpacity="0.2" />
            <stop offset="100%" stopColor="#10b981" stopOpacity="0.02" />
          </linearGradient>
          <linearGradient id="runtime-failed-area" x1="0" x2="0" y1="0" y2="1">
            <stop offset="0%" stopColor="#f43f5e" stopOpacity="0.18" />
            <stop offset="100%" stopColor="#f43f5e" stopOpacity="0.015" />
          </linearGradient>
          <filter id="runtime-line-soft-shadow" x="-20%" y="-20%" width="140%" height="150%">
            <feDropShadow dx="0" dy="5" stdDeviation="4" floodColor="#0f172a" floodOpacity="0.08" />
          </filter>
        </defs>
        <rect x={paddingX} y={paddingTop} width={plotWidth} height={plotHeight} rx="12" fill="#f8fafc" />
        {[0, 0.25, 0.5, 0.75, 1].map((ratio) => {
          const y = paddingTop + ratio * plotHeight;
          const value = Math.round((1 - ratio) * maxValue);
          return (
            <g key={ratio}>
              <line x1={paddingX} x2={width - paddingX} y1={y} y2={y} stroke="#e2e8f0" strokeDasharray={ratio === 1 ? "0" : "4 8"} />
              <text x={10} y={y + 4} className="fill-slate-400 text-[10px]">{value}</text>
            </g>
          );
        })}
        <path d={successAreaPath} fill="url(#runtime-success-area)" />
        <path d={failedAreaPath} fill="url(#runtime-failed-area)" />
        <path d={successPath} fill="none" stroke="#10b981" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" filter="url(#runtime-line-soft-shadow)" />
        <path d={failedPath} fill="none" stroke="#f43f5e" strokeWidth="3" strokeLinecap="round" strokeLinejoin="round" filter="url(#runtime-line-soft-shadow)" />
        {showPointMarkers ? chartPoints.map((point) => {
          const hasBoth = Boolean(point.success && point.failed && Math.abs(point.successY - point.failedY) < 14);
          const failedLabelY = hasBoth && point.failedY + 16 < bottomY ? point.failedY + 16 : point.failedY - 8;
          if (!point.success && !point.failed) return null;
          return (
            <g key={`${point.item.time}-${point.index}`}>
              <title>{`${point.item.label || point.item.time}：成功 ${point.success}，失败 ${point.failed}`}</title>
              {point.success ? (
                <>
                  <circle cx={point.x} cy={point.successY} r="4" fill="#10b981" stroke="#ffffff" strokeWidth="2" />
                  {showValueLabels ? <text x={point.x} y={point.successY - 8} textAnchor="middle" className="fill-emerald-600 text-[10px] font-semibold">{point.success}</text> : null}
                </>
              ) : null}
              {point.failed ? (
                <>
                  <circle cx={point.x} cy={point.failedY} r="4" fill="#f43f5e" stroke="#ffffff" strokeWidth="2" />
                  {showValueLabels ? <text x={point.x} y={failedLabelY} textAnchor="middle" className="fill-rose-500 text-[10px] font-semibold">{point.failed}</text> : null}
                </>
              ) : null}
            </g>
          );
        }) : null}
        {labelIndexes.map((index) => (
          <text key={index} x={xFor(index)} y={height - 16} textAnchor={index === 0 ? "start" : index === points.length - 1 ? "end" : "middle"} className="fill-slate-400 text-[11px]">
            {points[index]?.label || ""}
          </text>
        ))}
        </svg>
      </div>
    </div>
  );
}

function ServerLoadCard({ system }: { system: SystemLoad }) {
  const cpuPercent = loadPercent(system.cpu.usage_percent);
  const memoryPercent = loadPercent(system.memory.usage_percent);
  const diskPercent = loadPercent(system.disk.usage_percent);

  const sections = [
    {
      key: "cpu",
      label: "CPU",
      icon: Cpu,
      iconClass: "bg-blue-50 text-blue-600",
      value: `${rateText(cpuPercent)}%`,
      detail: `${finiteNumber(system.cpu.cores)} 核 · Load 1m ${rateText(system.cpu.load_1)}`,
      progress: cpuPercent,
      progressColor: "#3b82f6",
    },
    {
      key: "memory",
      label: "内存",
      icon: MemoryStick,
      iconClass: "bg-emerald-50 text-emerald-600",
      value: `${rateText(memoryPercent)}%`,
      detail: `${formatBytes(system.memory.used_bytes)} / ${formatBytes(system.memory.total_bytes)}`,
      progress: memoryPercent,
      progressColor: "#10b981",
    },
    {
      key: "network",
      label: "网络",
      icon: Network,
      iconClass: "bg-cyan-50 text-cyan-600",
      receiveRate: formatRate(system.network.receive_bytes_per_second),
      sendRate: formatRate(system.network.send_bytes_per_second),
      received: formatBytes(system.network.received_bytes),
      sent: formatBytes(system.network.sent_bytes),
    },
    {
      key: "disk",
      label: "硬盘",
      icon: HardDrive,
      iconClass: "bg-amber-50 text-amber-600",
      value: `${rateText(diskPercent)}%`,
      detail: `${formatBytes(system.disk.used_bytes)} / ${formatBytes(system.disk.total_bytes)}`,
      progress: diskPercent,
      progressColor: "#f59e0b",
    },
  ] as const;

  return (
    <Card
      title="服务器负载"
      extra={<span className="font-mono text-xs text-slate-400">{formatShanghaiDateTime(system.sampled_at)}</span>}
    >
      <div className="grid min-w-0 grid-cols-1 gap-y-5 sm:grid-cols-2 xl:grid-cols-4 xl:gap-y-0">
        {sections.map((section, index) => {
          const Icon = section.icon;
          const isNetwork = section.key === "network";
          return (
            <div
              key={section.key}
              className={cn(
                "min-w-0 px-0 sm:px-5",
                index % 2 === 1 && "sm:border-l sm:border-slate-100",
                index === 0 && "sm:pl-0",
                index === 2 && "sm:pl-0 xl:border-l xl:border-slate-100 xl:pl-5",
                index === 3 && "xl:pr-0",
              )}
            >
              <div className="flex min-w-0 items-center gap-3">
                <span className={cn("flex size-9 shrink-0 items-center justify-center rounded-lg", section.iconClass)}>
                  <Icon className="size-[18px]" />
                </span>
                <span className="truncate text-sm font-medium text-slate-500">{section.label}</span>
              </div>

              {isNetwork ? (
                <div className="mt-4 min-w-0 space-y-2.5">
                  <div className="flex min-w-0 items-baseline justify-between gap-3">
                    <span className="shrink-0 text-xs text-slate-400">下载</span>
                    <span className="min-w-0 truncate font-mono text-lg font-semibold text-slate-950 tabular-nums" title={section.receiveRate}>{section.receiveRate}</span>
                  </div>
                  <div className="flex min-w-0 items-baseline justify-between gap-3">
                    <span className="shrink-0 text-xs text-slate-400">上传</span>
                    <span className="min-w-0 truncate font-mono text-lg font-semibold text-slate-950 tabular-nums" title={section.sendRate}>{section.sendRate}</span>
                  </div>
                  <div className="truncate text-xs text-slate-400" title={`累计接收 ${section.received}，发送 ${section.sent}`}>
                    累计 ↓ {section.received} · ↑ {section.sent}
                  </div>
                </div>
              ) : (
                <div className="mt-4 min-w-0">
                  <div className="font-mono text-2xl font-semibold text-slate-950 tabular-nums">{section.value}</div>
                  <div className="mt-2 truncate text-xs text-slate-400" title={section.detail}>{section.detail}</div>
                  <Progress
                    className="!mt-3 !mb-0"
                    percent={section.progress}
                    showInfo={false}
                    strokeColor={section.progressColor}
                    trailColor="#f1f5f9"
                    size="small"
                  />
                </div>
              )}
            </div>
          );
        })}
      </div>
    </Card>
  );
}

function ErrorRateDonut({ runtime }: { runtime: RuntimeHealthData }) {
  const segments = (runtime.status_pie || []).filter((item) => Number(item.value || 0) > 0);
  const total = segments.reduce((sum, item) => sum + Number(item.value || 0), 0);
  const totalCalls = runtime.total || total;
  const failedCount = Number(runtime.totals.failed || 0);
  const successCount = Number(runtime.totals.success || 0);
  const errorRateText = rateText(runtime.error_rate);
  const successRateText = rateText(runtime.success_rate);
  const radius = 54;
  const circumference = 2 * Math.PI * radius;

  if (total <= 0) {
    return (
      <div className="flex min-h-[290px] items-center justify-center rounded-xl bg-slate-50">
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无错误率数据" />
      </div>
    );
  }

  return (
    <div className="min-h-[290px] rounded-xl border border-slate-100 bg-gradient-to-b from-white to-slate-50/60 p-4">
      <div className="grid gap-4 md:grid-cols-[170px_1fr] xl:grid-cols-1 2xl:grid-cols-[170px_1fr]">
        <div className="relative mx-auto flex size-[170px] items-center justify-center rounded-full bg-white shadow-sm ring-1 ring-slate-100">
          <svg viewBox="0 0 160 160" className="size-[158px] -rotate-90">
            <circle cx="80" cy="80" r={radius} fill="none" stroke="#eef2f7" strokeWidth="16" />
            {segments.map((segment, index) => {
              const length = (Number(segment.value || 0) / total) * circumference;
              const currentOffset = segments.slice(0, index).reduce(
                (sum, item) => sum + (Number(item.value || 0) / total) * circumference,
                0,
              );
              return (
                <circle
                  key={segment.status}
                  cx="80"
                  cy="80"
                  r={radius}
                  fill="none"
                  stroke={runtimeStatusColor(segment.status)}
                  strokeWidth="16"
                  strokeLinecap="round"
                  strokeDasharray={`${length} ${circumference - length}`}
                  strokeDashoffset={-currentOffset}
                />
              );
            })}
          </svg>
          <div className="absolute text-center">
            <div className="font-mono text-[28px] font-bold leading-none tracking-tight text-slate-950 tabular-nums">
              {errorRateText}<span className="ml-0.5 text-sm font-semibold text-slate-500">%</span>
            </div>
            <div className="mt-1 text-xs font-medium text-slate-400">错误率</div>
          </div>
        </div>

        <div className="flex min-w-0 flex-col justify-center gap-3">
          <div className="grid grid-cols-3 gap-2">
            <div className="rounded-xl bg-emerald-50 px-3 py-2">
              <div className="text-[11px] text-emerald-600">成功率</div>
              <div className="mt-1 font-mono text-lg font-bold text-emerald-700 tabular-nums">{successRateText}%</div>
            </div>
            <div className="rounded-xl bg-rose-50 px-3 py-2">
              <div className="text-[11px] text-rose-500">失败</div>
              <div className="mt-1 font-mono text-lg font-bold text-rose-600 tabular-nums">{numberText(failedCount)}</div>
            </div>
            <div className="rounded-xl bg-slate-100 px-3 py-2">
              <div className="text-[11px] text-slate-500">总调用</div>
              <div className="mt-1 font-mono text-lg font-bold text-slate-700 tabular-nums">{numberText(totalCalls)}</div>
            </div>
          </div>

          <div className="space-y-2">
            {segments.map((segment) => {
              const value = Number(segment.value || 0);
              const ratio = total > 0 ? (value / total) * 100 : 0;
              return (
                <div key={segment.status} className="rounded-lg bg-white px-3 py-2 shadow-sm ring-1 ring-slate-100">
                  <div className="mb-1 flex items-center justify-between gap-3 text-xs">
                    <span className="inline-flex min-w-0 items-center gap-2 font-medium text-slate-600">
                      <i className="size-2.5 shrink-0 rounded-full" style={{ backgroundColor: runtimeStatusColor(segment.status) }} />
                      <span className="truncate">{segment.label}</span>
                    </span>
                    <span className="font-mono font-semibold text-slate-900 tabular-nums">{numberText(value)}</span>
                  </div>
                  <div className="h-1.5 overflow-hidden rounded-full bg-slate-100">
                    <div className="h-full rounded-full" style={{ width: `${ratio}%`, backgroundColor: runtimeStatusColor(segment.status) }} />
                  </div>
                </div>
              );
            })}
          </div>

          <div className="rounded-lg border border-dashed border-slate-200 bg-white/70 px-3 py-2 text-xs text-slate-500">
            最近 {runtimeWindowText(runtime.window_minutes)}：成功 {numberText(successCount)} 次，失败 {numberText(failedCount)} 次
          </div>
        </div>
      </div>
    </div>
  );
}

function RuntimeHealth({
  runtime,
  selectedWindowMinutes,
  isUpdating,
  onWindowChange,
}: {
  runtime: RuntimeHealthData;
  selectedWindowMinutes: number;
  isUpdating: boolean;
  onWindowChange: (minutes: number) => void;
}) {
  return (
    <section className="grid items-stretch gap-4 xl:grid-cols-[1.45fr_0.9fr]">
      <Card
        className="h-full"
        title={
          <div className="flex flex-wrap items-center gap-2">
            <span>运行状况</span>
            <Select
              aria-label="运行状况统计窗口"
              className="w-[132px]"
              loading={isUpdating}
              options={RUNTIME_WINDOW_OPTIONS}
              popupMatchSelectWidth={false}
              size="small"
              value={selectedWindowMinutes}
              onChange={(value) => onWindowChange(Number(value))}
            />
          </div>
        }
        extra={<span className="font-mono text-xs text-slate-400">{formatShanghaiDateTime(runtime.start_time)} → {formatShanghaiDateTime(runtime.end_time)}</span>}
      >
        <RuntimeTrendChart series={runtime.series} windowMinutes={runtime.window_minutes} />
      </Card>
      <Card
        className="h-full"
        title={
          <div className="flex items-center gap-2">
            <span>错误率</span>
            <Tag color={runtime.error_rate > 0 ? "red" : "green"} className="m-0">{rateText(runtime.error_rate)}%</Tag>
          </div>
        }
      >
        <ErrorRateDonut runtime={runtime} />
        {runtime.error_reasons.length ? (
          <div className="mt-4 min-w-0 overflow-hidden border-t border-slate-100 pt-4">
            <div className="mb-2 text-xs font-medium text-slate-400">主要错误原因</div>
            <div className="max-h-32 space-y-2 overflow-y-auto pr-1">
              {runtime.error_reasons.map((item) => (
                <div key={item.label} className="flex items-center justify-between gap-3 text-sm">
                  <span className="min-w-0 truncate text-slate-600" title={item.label}>{item.label}</span>
                  <span className="font-mono font-semibold text-rose-600">{item.value}</span>
                </div>
              ))}
            </div>
          </div>
        ) : null}
      </Card>
    </section>
  );
}

function RecentFailures({ items }: { items: DashboardSummary["calls"]["recent_failed"] }) {
  if (!items.length) {
    return (
      <div className="py-10">
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="今日暂无失败调用" />
      </div>
    );
  }

  return (
    <div className="divide-y divide-slate-100">
      {items.map((record) => {
        const time = formatShanghaiDateTimeParts(record.time);
        const summary = String(record.summary || "调用失败");
        const model = String(record.model || "-");
        const endpoint = String(record.endpoint || "").trim();
        const accountEmail = String(record.account_email || "").trim();
        const error = String(record.error || "-");

        return (
          <div
            key={String(record.id || `${record.time}-${record.error}`)}
            className="grid gap-4 px-5 py-4 transition-colors hover:bg-slate-50/80 md:grid-cols-[150px_minmax(0,1fr)_minmax(260px,36%)] md:items-center"
          >
            <div className="flex items-baseline gap-2 md:block">
              <div className="font-mono text-sm font-semibold text-slate-900">{time.time || time.date}</div>
              <div className="font-mono text-xs text-slate-400 md:mt-1">{time.time ? time.date : ""}</div>
            </div>

            <div className="min-w-0">
              <div className="flex min-w-0 items-center gap-2">
                <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-rose-50 text-rose-600">
                  <AlertCircle className="size-4" />
                </span>
                <span className="min-w-0 truncate font-medium text-slate-900" title={summary}>
                  {summary}
                </span>
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <Tag color="red" className="m-0">失败</Tag>
                <Tag className="m-0 font-mono">{model}</Tag>
                {endpoint ? <Tag color="blue" className="m-0 font-mono">{endpoint}</Tag> : null}
                {accountEmail ? (
                  <span className="max-w-full truncate rounded-md bg-slate-100 px-2 py-0.5 font-mono text-xs text-slate-500" title={accountEmail}>
                    {accountEmail}
                  </span>
                ) : null}
              </div>
            </div>

            <div className="min-w-0 rounded-md border border-rose-100 bg-rose-50 px-3 py-2">
              <div className="text-xs font-medium text-rose-500">错误</div>
              <div className="mt-1 truncate font-mono text-sm text-rose-700" title={error}>
                {error}
              </div>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function DashboardContent() {
  const [data, setData] = useState<DashboardSummary | null>(null);
  const [systemLoad, setSystemLoad] = useState<SystemLoad | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [runtimeWindowMinutes, setRuntimeWindowMinutes] = useState(60);

  const loadDashboard = async (silent = false, windowMinutes = runtimeWindowMinutes) => {
    if (silent) {
      setIsRefreshing(true);
    } else {
      setIsLoading(true);
    }
    try {
      const dashboard = await fetchDashboard(windowMinutes);
      setData(dashboard);
      if (dashboard.system) {
        setSystemLoad((current) => newerSystemLoad(current, dashboard.system));
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载总览失败");
    } finally {
      setIsLoading(false);
      setIsRefreshing(false);
    }
  };

  useEffect(() => {
    void loadDashboard();
  }, []);

  useEffect(() => {
    if (!data) {
      return;
    }

    let active = true;
    let inFlight = false;
    const refreshSystemLoad = async () => {
      if (inFlight) {
        return;
      }
      inFlight = true;
      try {
        const latest = await fetchSystemLoad();
        if (active) {
          setSystemLoad((current) => newerSystemLoad(current, latest));
        }
      } catch {
        // Keep the last successful sample when the lightweight poll fails.
      } finally {
        inFlight = false;
      }
    };
    const timer = window.setInterval(() => void refreshSystemLoad(), 5000);

    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [data]);

  const handleRuntimeWindowChange = (windowMinutes: number) => {
    setRuntimeWindowMinutes(windowMinutes);
    void loadDashboard(true, windowMinutes);
  };

  if (isLoading && !data) {
    return (
      <div className="dashboard-console">
        <Skeleton active paragraph={{ rows: 8 }} />
      </div>
    );
  }

  if (!data) {
    return (
      <Card>
        <Empty description="暂时无法加载系统总览" />
      </Card>
    );
  }

  const totalAccounts = data.accounts.total;
  const todayCalls = data.calls.today;
  const totalCalls = todayCalls?.total ?? 0;
  const todayTotals = todayCalls?.totals;
  const failedCalls = todayTotals?.failed ?? todayCalls?.by_status.failed ?? 0;
  const successCalls = todayTotals?.success ?? Math.max(0, totalCalls - failedCalls);
  const availabilityCalls = todayCalls?.availability_total ?? successCalls + failedCalls;
  const runningTasks = (data.tasks.by_status.running || 0) + (data.tasks.by_status.queued || 0);
  const storageHealthy = data.storage.health.status === "healthy";
  const callSuccessPercent = availabilityCalls > 0 ? percent(successCalls, availabilityCalls) : 100;
  const todayEndpointEntries = todayCalls?.by_endpoint ?? {};
  const todayModelEntries = todayCalls?.by_model ?? {};

  return (
    <div className="dashboard-console">
      <section className="flex flex-col gap-4 rounded-lg border border-slate-200 bg-white px-5 py-5 shadow-sm lg:flex-row lg:items-center lg:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <Tag color={storageHealthy ? "green" : "red"} className="m-0">{storageHealthy ? "运行正常" : "需要检查"}</Tag>
            <span className="text-sm text-slate-400">v{data.version}</span>
          </div>
          <Typography.Title level={2} className="!mt-3 !mb-1 !text-2xl">
            系统总览
          </Typography.Title>
          <Typography.Text type="secondary">最后更新：{formatShanghaiDateTime(data.generated_at)}</Typography.Text>
        </div>
        <Button
          type="primary"
          icon={isRefreshing ? <LoaderCircle className="size-4 animate-spin" /> : <RefreshCw className="size-4" />}
          onClick={() => void loadDashboard(true)}
          disabled={isRefreshing}
        >
          刷新
        </Button>
      </section>

      {!storageHealthy ? (
        <Alert
          type="error"
          showIcon
          message="存储后端异常"
          description={String(data.storage.health.error || "请检查数据库连接和容器状态。")}
        />
      ) : null}

      <section className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <MetricCard title="可用账号" value={`${numberText(data.accounts.active)}/${numberText(totalAccounts)}`} helper={`限流 ${data.accounts.limited}，异常 ${data.accounts.abnormal}`} icon={UsersRound} tone="green" />
        <MetricCard title="剩余额度" value={data.accounts.unlimited_quota_count > 0 ? "不限" : numberText(data.accounts.total_quota)} helper={`调度冷却 ${data.accounts.cooling} 个账号`} icon={Gauge} tone="blue" />
        <MetricCard title="今日调用" value={numberText(totalCalls)} helper={`失败 ${failedCalls}，成功率 ${callSuccessPercent}%`} icon={Activity} tone={failedCalls ? "amber" : "green"} />
        <MetricCard title="队列任务" value={numberText(runningTasks)} helper={`历史任务 ${data.tasks.total} 条`} icon={TimerReset} tone={runningTasks ? "amber" : "slate"} />
      </section>

      {systemLoad ? <ServerLoadCard system={systemLoad} /> : null}

      {data.calls.runtime ? (
        <RuntimeHealth
          runtime={data.calls.runtime}
          selectedWindowMinutes={runtimeWindowMinutes}
          isUpdating={isRefreshing}
          onWindowChange={handleRuntimeWindowChange}
        />
      ) : null}

      <section className="grid gap-4 xl:grid-cols-3">
        <Card title="今日接口分布">
          <EntryBars items={sortedEntries(todayEndpointEntries)} />
        </Card>
        <Card title="今日模型使用">
          <EntryBars items={sortedEntries(todayModelEntries)} />
        </Card>
        <Card title="账号类型">
          <EntryBars items={sortedEntries(data.accounts.by_type)} />
        </Card>
      </section>

      <section>
        <Card
          title={
            <div className="flex items-center gap-2">
              <span>最近失败</span>
              <Tag color="red" className="m-0">{data.calls.recent_failed.length}</Tag>
            </div>
          }
          styles={{ body: { padding: 0 } }}
        >
          <RecentFailures items={data.calls.recent_failed} />
        </Card>
      </section>
    </div>
  );
}

export default function DashboardPage() {
  const { isCheckingAuth, session } = useAuthGuard(["admin"]);

  if (isCheckingAuth || !session || session.role !== "admin") {
    return (
      <div className="flex min-h-[40vh] items-center justify-center">
        <LoaderCircle className="size-5 animate-spin text-stone-400" />
      </div>
    );
  }

  return <DashboardContent />;
}
