"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Button, Card, DatePicker, Empty, Progress, Segmented, Skeleton, Tag, Typography } from "antd";
import {
  Activity,
  AlertCircle,
  Boxes,
  CalendarDays,
  Cpu,
  HardDrive,
  LoaderCircle,
  MemoryStick,
  Network,
  RefreshCw,
  UsersRound,
  Webhook,
  WandSparkles,
} from "lucide-react";
import { toast } from "sonner";

import {
  fetchDashboard,
  fetchImagePoolCapacity,
  fetchSchedulerDiagnostics,
  fetchSystemLoad,
  type DashboardSummary,
  type ImagePoolCapacity,
  type SchedulerDiagnostics,
  type SystemLoad,
} from "@/lib/api";
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

function limiterPercent(stats?: { active?: number; limit?: number }) {
  const active = finiteNumber(stats?.active);
  const limit = finiteNumber(stats?.limit);
  return limit > 0 ? percent(active, limit) : 0;
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
  { value: 7 * 24 * 60, label: "7天" },
  { value: 15 * 24 * 60, label: "15天" },
  { value: 30 * 24 * 60, label: "30天" },
  { value: "custom" as const, label: <span className="inline-flex items-center gap-1"><CalendarDays className="size-3" />自定义</span> },
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

type OperationsSummary = {
  accounts: {
    active: number;
    total: number;
    quota: string;
    limited: number;
  };
  calls: {
    total: number;
    failed: number;
    successPercent: number;
  };
  taskHistory: number;
};

function OperationsOverview({ scheduler, system, summary }: { scheduler: SchedulerDiagnostics; system: SystemLoad; summary: OperationsSummary }) {
  const queuedTasks = scheduler.tasks.queued_tasks ?? scheduler.tasks.by_status?.queued ?? 0;
  const runningTasks = scheduler.tasks.running_tasks ?? scheduler.tasks.by_status?.running ?? 0;
  const activeTasks = scheduler.tasks.active_tasks ?? queuedTasks + runningTasks;
  const queuePercent = percent(queuedTasks, scheduler.tasks.queue_capacity);
  const workerPercent = percent(runningTasks, scheduler.tasks.worker_limit);
  const postprocessPercent = percent(scheduler.postprocess.queue_depth, scheduler.postprocess.queue_capacity);
  const cpuPercent = loadPercent(system.cpu.usage_percent);
  const memoryPercent = loadPercent(system.memory.usage_percent);
  const diskPercent = loadPercent(system.disk.usage_percent);
  const schedulerSections = [
    {
      key: "gpt",
      title: "GPT账号",
      icon: UsersRound,
      iconClass: "bg-emerald-50 text-emerald-600",
      value: `${summary.accounts.active}/${summary.accounts.total}`,
      detail: `可调度 ${scheduler.gpt.dispatchable} · 额度 ${summary.accounts.quota} · 限流 ${summary.accounts.limited}`,
      progress: percent(summary.accounts.active, Math.max(1, summary.accounts.total)),
      color: "#10b981",
    },
    {
      key: "calls",
      title: "今日调用",
      icon: Activity,
      iconClass: summary.calls.failed > 0 ? "bg-amber-50 text-amber-600" : "bg-emerald-50 text-emerald-600",
      value: numberText(summary.calls.total),
      detail: `失败 ${summary.calls.failed} · 成功率 ${summary.calls.successPercent}%`,
      progress: summary.calls.successPercent,
      color: summary.calls.failed > 0 ? "#f59e0b" : "#10b981",
    },
    {
      key: "tasks",
      title: "任务队列",
      icon: Boxes,
      iconClass: "bg-sky-50 text-sky-600",
      value: `${numberText(activeTasks)} 项`,
      detail: `排队 ${queuedTasks} · 处理 ${runningTasks} · 通道 ${scheduler.tasks.queue_depth}/${scheduler.tasks.queue_capacity}`,
      progress: Math.max(queuePercent, workerPercent),
      color: "#0ea5e9",
    },
    {
      key: "postprocess",
      title: "高清队列",
      icon: WandSparkles,
      iconClass: "bg-violet-50 text-violet-600",
      value: scheduler.postprocess.enabled ? `${scheduler.postprocess.queue_depth}/${scheduler.postprocess.queue_capacity}` : "关闭",
      detail: `已处理 ${scheduler.postprocess.processed}，失败 ${scheduler.postprocess.failed}`,
      progress: postprocessPercent,
      color: "#8b5cf6",
    },
    {
      key: "callbacks",
      title: "任务回调",
      icon: Webhook,
      iconClass: "bg-rose-50 text-rose-600",
      value: String(scheduler.callbacks.delivered),
      detail: `失败 ${scheduler.callbacks.failed}，尝试 ${scheduler.callbacks.attempts}`,
      progress: percent(scheduler.callbacks.failed, Math.max(1, scheduler.callbacks.delivered + scheduler.callbacks.failed)),
      color: "#ef4444",
    },
  ];
  const resourceSections = [
    {
      key: "cpu",
      label: "CPU",
      icon: Cpu,
      iconClass: "bg-blue-50 text-blue-600",
      value: `${rateText(cpuPercent)}%`,
      detail: `${finiteNumber(system.cpu.cores)} 核 · Load ${rateText(system.cpu.load_1)}`,
      progress: cpuPercent,
      color: "#3b82f6",
    },
    {
      key: "memory",
      label: "内存",
      icon: MemoryStick,
      iconClass: "bg-emerald-50 text-emerald-600",
      value: `${rateText(memoryPercent)}%`,
      detail: `${formatBytes(system.memory.used_bytes)} / ${formatBytes(system.memory.total_bytes)}`,
      progress: memoryPercent,
      color: "#10b981",
    },
    {
      key: "disk",
      label: "硬盘",
      icon: HardDrive,
      iconClass: "bg-amber-50 text-amber-600",
      value: `${rateText(diskPercent)}%`,
      detail: `${formatBytes(system.disk.used_bytes)} / ${formatBytes(system.disk.total_bytes)}`,
      progress: diskPercent,
      color: "#f59e0b",
    },
  ];
  const sampledAt = system.sampled_at || scheduler.generated_at;

  return (
    <Card
      title={
        <div className="flex items-center gap-2">
          <Activity className="size-4 text-slate-500" />
          <span>运行状态</span>
          <Tag color="green" className="m-0 font-normal">实时</Tag>
        </div>
      }
      extra={<span className="font-mono text-xs text-slate-400">{formatShanghaiDateTime(sampledAt)}</span>}
      styles={{ body: { padding: 0 } }}
    >
      <div className="grid min-w-0 xl:grid-cols-[1.2fr_0.8fr]">
        <section className="min-w-0 p-5 xl:border-r xl:border-slate-100 xl:p-6">
          <div className="mb-5">
            <div className="text-sm font-semibold text-slate-800">业务与调度</div>
            <div className="mt-1 text-xs text-slate-400">账号、调用、任务与后台处理</div>
          </div>
          <div className="grid min-w-0 md:grid-cols-6">
            {schedulerSections.map((section, index) => {
              const Icon = section.icon;
              return (
                <div
                  key={section.key}
                  className={cn(
                    "min-w-0 py-4 md:min-h-[112px] md:px-5",
                    index > 0 && "border-t border-slate-100",
                    index < 3 && "md:col-span-2 md:border-t-0 md:pb-5 md:pt-0",
                    index >= 3 && "md:col-span-3 md:pt-5",
                    (index === 1 || index === 2 || index === 4) && "md:border-l md:border-slate-100",
                    (index === 0 || index === 3) && "md:pl-0",
                    (index === 2 || index === 4) && "md:pr-0",
                    index === schedulerSections.length - 1 && "pb-0",
                  )}
                >
                  <div className="flex min-w-0 items-center gap-2.5">
                    <span className={cn("flex size-8 shrink-0 items-center justify-center rounded-md", section.iconClass)}>
                      <Icon className="size-4" />
                    </span>
                    <span className="min-w-0 flex-1 truncate text-sm font-medium text-slate-600">{section.title}</span>
                    <span className="shrink-0 font-mono text-lg font-semibold text-slate-950 tabular-nums">{section.value}</span>
                  </div>
                  <div className="mt-3 truncate text-xs text-slate-400" title={section.detail}>{section.detail}</div>
                  <Progress className="!mb-0 !mt-3" percent={section.progress} showInfo={false} strokeColor={section.color} trailColor="#f1f5f9" size="small" />
                </div>
              );
            })}
          </div>
        </section>

        <section className="min-w-0 border-t border-slate-100 p-5 xl:border-t-0">
          <div className="mb-5">
            <div className="text-sm font-semibold text-slate-800">服务器资源</div>
            <div className="mt-1 text-xs text-slate-400">计算、存储与实时网络吞吐</div>
          </div>
          <div className="grid min-w-0 divide-y divide-slate-100 sm:grid-cols-3 sm:divide-x sm:divide-y-0">
            {resourceSections.map((section) => {
              const Icon = section.icon;
              return (
                <div key={section.key} className="min-w-0 py-3 first:pt-0 last:pb-0 sm:px-4 sm:py-0 sm:first:pl-0 sm:last:pr-0">
                  <div className="flex items-center gap-2">
                    <span className={cn("flex size-7 shrink-0 items-center justify-center rounded-md", section.iconClass)}><Icon className="size-3.5" /></span>
                    <span className="text-xs font-medium text-slate-500">{section.label}</span>
                  </div>
                  <div className="mt-2 font-mono text-xl font-semibold text-slate-950 tabular-nums">{section.value}</div>
                  <div className="mt-1 truncate text-[11px] text-slate-400" title={section.detail}>{section.detail}</div>
                  <Progress className="!mb-0 !mt-2" percent={section.progress} showInfo={false} strokeColor={section.color} trailColor="#f1f5f9" size="small" />
                </div>
              );
            })}
          </div>

          <div className="mt-5 flex min-w-0 flex-col gap-3 border-t border-slate-100 pt-4 sm:flex-row sm:items-center">
            <div className="flex min-w-0 flex-1 items-center gap-3">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-cyan-50 text-cyan-600"><Network className="size-4" /></span>
              <div className="min-w-0">
                <div className="text-sm font-medium text-slate-600">网络吞吐</div>
                <div className="mt-0.5 truncate text-[11px] text-slate-400" title={`累计接收 ${formatBytes(system.network.received_bytes)}，发送 ${formatBytes(system.network.sent_bytes)}`}>
                  累计 ↓ {formatBytes(system.network.received_bytes)} · ↑ {formatBytes(system.network.sent_bytes)}
                </div>
              </div>
            </div>
            <div className="grid shrink-0 grid-cols-2 gap-5 sm:min-w-[210px]">
              <div>
                <div className="text-[11px] text-slate-400">下载</div>
                <div className="mt-1 truncate font-mono text-sm font-semibold text-slate-900 tabular-nums">{formatRate(system.network.receive_bytes_per_second)}</div>
              </div>
              <div>
                <div className="text-[11px] text-slate-400">上传</div>
                <div className="mt-1 truncate font-mono text-sm font-semibold text-slate-900 tabular-nums">{formatRate(system.network.send_bytes_per_second)}</div>
              </div>
            </div>
          </div>
        </section>
      </div>
    </Card>
  );
}

function ImagePoolConcurrency({ capacity }: { capacity: ImagePoolCapacity | null }) {
  const concurrency = capacity?.concurrency;
  if (!concurrency) {
    return null;
  }
  const stages = [
    ["准备", concurrency.upstream.prepare],
    ["提交", concurrency.upstream.submit],
    ["轮询", concurrency.upstream.poll],
    ["下载", concurrency.upstream.download],
    ["上传", concurrency.upstream.upload],
  ] as const;
  const globalPercent = limiterPercent(concurrency.global);
  const taskPressure = capacity.tasks.queued + capacity.tasks.running;
  const estimate = capacity.estimate;
  const registration = capacity.registration;
  const dynamicSlots = Math.max(0, capacity.accounts.dynamic_slots ?? capacity.accounts.dispatchable_slots ?? 0);
  const leasedSlots = Math.max(0, capacity.accounts.leased_slots ?? capacity.accounts.leased);
  const idleSlots = Math.max(0, capacity.accounts.idle_slots ?? dynamicSlots - leasedSlots);
  const dynamicMin = capacity.accounts.dynamic_limit_min ?? 1;
  const dynamicMax = capacity.accounts.dynamic_limit_max ?? capacity.accounts.max_inflight_per_account ?? 1;
  const accountSlotPercent = dynamicSlots > 0 ? percent(leasedSlots, dynamicSlots) : 0;
  const dynamicRange = dynamicSlots > 0 ? `${numberText(dynamicMin)}～${numberText(dynamicMax)}` : "—";
  return (
    <Card
      title={
        <div className="flex items-center gap-2">
          <span className="flex size-8 items-center justify-center rounded-md bg-cyan-50 text-cyan-600"><Network className="size-4" /></span>
          <div>
            <div>生图号池并发</div>
            <div className="mt-0.5 text-xs font-normal text-slate-400">全局租约与上游阶段实时占用</div>
          </div>
        </div>
      }
      extra={<span className="font-mono text-xs text-slate-400">任务 {taskPressure} · 账号 {capacity.accounts.dispatchable} 可调度</span>}
    >
      <div className="grid gap-4 lg:grid-cols-2">
        <div className="rounded-lg border border-cyan-100 bg-cyan-50/60 p-4">
          <div className="flex items-center justify-between gap-3">
            <span className="text-sm font-medium text-slate-600">全局固定上限</span>
            <span className="font-mono text-lg font-semibold text-slate-900 tabular-nums">
              {numberText(concurrency.global.active)} / {concurrency.global.limit > 0 ? numberText(concurrency.global.limit) : "∞"}
            </span>
          </div>
          <Progress className="!mb-0 !mt-3" percent={globalPercent} showInfo={false} strokeColor="#06b6d4" trailColor="#cffafe" size="small" />
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500">
            <span>等待 {numberText(concurrency.global.waiting)}</span>
            <span>租约 {numberText(leasedSlots)}</span>
            <span>空闲并发槽 {numberText(idleSlots)}</span>
            <span>冷却 {numberText(capacity.accounts.cooling)}</span>
            <span>额度耗尽 {numberText(capacity.accounts.quota_exhausted)}</span>
            <span>卡住切号 {numberText(concurrency.stalled_attempts)}</span>
          </div>
        </div>
        <div className="rounded-lg border border-emerald-100 bg-emerald-50/60 p-4">
          <div className="flex items-center justify-between gap-3">
            <span className="text-sm font-medium text-slate-600">账号并发槽位</span>
            <span className="font-mono text-lg font-semibold text-slate-900 tabular-nums">
              {numberText(leasedSlots)} / {numberText(dynamicSlots)}
            </span>
          </div>
          <Progress className="!mb-0 !mt-3" percent={accountSlotPercent} showInfo={false} strokeColor="#10b981" trailColor="#d1fae5" size="small" />
          <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500">
            <span>可调度账号 {numberText(capacity.accounts.dispatchable)}</span>
            <span>空闲槽 {numberText(idleSlots)}</span>
            <span>槽位范围 {dynamicRange}</span>
            <span>限流账号 {numberText(capacity.accounts.limited)}</span>
          </div>
          <div className="mt-2 text-xs text-slate-400">每个账号独立管理槽位；异常只取消该账号在途任务。</div>
        </div>
      </div>
      <div className="mt-4 grid gap-3 sm:grid-cols-5">
        {stages.map(([label, stats]) => (
          <div key={label} className="rounded-lg border border-slate-200 bg-slate-50/70 p-3">
            <div className="text-xs font-medium text-slate-500">{label}</div>
            <div className="mt-2 font-mono text-base font-semibold text-slate-900 tabular-nums">
              {numberText(stats.active)} / {stats.limit > 0 ? numberText(stats.limit) : "∞"}
            </div>
            <Progress className="!mb-0 !mt-2" percent={limiterPercent(stats)} showInfo={false} strokeColor="#64748b" trailColor="#e2e8f0" size="small" />
            <div className="mt-1 text-[11px] text-slate-400">等待 {numberText(stats.waiting)}</div>
          </div>
        ))}
      </div>
      <div className="mt-4 rounded-lg border border-slate-200 bg-slate-50/70 px-4 py-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="text-sm font-medium text-slate-700">任务驱动号池预测</div>
          <div className="font-mono text-xs text-slate-500">
            当前 {numberText(estimate.current_effective_accounts)} · 需求 {numberText(estimate.recommended_required_usable_accounts)} · 建议补 {numberText(estimate.recommended_add_usable_accounts)}
          </div>
        </div>
        <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500">
          <span>排队/运行 {numberText(capacity.tasks.pending)}</span>
          <span>单号并发 {numberText(estimate.concurrent_slots_per_account)}</span>
          <span>当前并发槽 {numberText(estimate.current_effective_inflight_slots)}</span>
          <span>单号余量约 {numberText(estimate.estimated_images_per_usable_account)} 张</span>
          <span>号池余量约 {numberText(estimate.estimated_pool_images)} 张</span>
          <span>预估注册尝试 {numberText(estimate.recommended_register_accounts)}</span>
          {registration ? <span>自动补号：{registration.enabled ? registration.status : "关闭"}</span> : null}
          {registration?.inflight_registrations ? <span>注册中 {numberText(registration.inflight_registrations)}</span> : null}
        </div>
        {registration?.reason ? <div className="mt-2 text-xs text-slate-400">{registration.reason}</div> : null}
      </div>
    </Card>
  );
}

function sortedEntries(source?: Record<string, number>, limit = 5) {
  return Object.entries(source || {})
    .filter(([, value]) => Number(value) > 0)
    .sort((left, right) => right[1] - left[1])
    .slice(0, limit);
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
type RuntimeRange = { start: string; end: string };

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value));
}

function smoothRuntimeValues(values: number[]) {
  if (values.length < 3) {
    return values;
  }
  const radius = values.length <= 15 ? 1 : 2;
  return values.map((_, index) => {
    let weightedTotal = 0;
    let weightTotal = 0;
    for (let offset = -radius; offset <= radius; offset += 1) {
      const sourceIndex = index + offset;
      if (sourceIndex < 0 || sourceIndex >= values.length) {
        continue;
      }
      const weight = radius + 1 - Math.abs(offset);
      weightedTotal += values[sourceIndex] * weight;
      weightTotal += weight;
    }
    return weightTotal > 0 ? weightedTotal / weightTotal : values[index];
  });
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

function RuntimeTrendChart({ runtime }: { runtime: RuntimeHealthData }) {
  const points = runtime.series || [];
  const windowMinutes = runtime.window_minutes;
  const total = points.reduce((sum, item) => sum + Number(item.total ?? (Number(item.success || 0) + Number(item.failed || 0))), 0);
  const totalSuccess = points.reduce((sum, item) => sum + Number(item.success || 0), 0);
  const totalFailed = points.reduce((sum, item) => sum + Number(item.failed || 0), 0);
  const chartContainerRef = useRef<HTMLDivElement>(null);
  const [chartContainerWidth, setChartContainerWidth] = useState(720);
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null);
  const height = 240;
  const width = Math.max(360, chartContainerWidth);
  const paddingX = 48;
  const paddingTop = 18;
  const paddingBottom = 52;
  const plotWidth = width - paddingX * 2;
  const plotHeight = height - paddingTop - paddingBottom;
  const smoothedTotal = smoothRuntimeValues(points.map((item) => Number(item.total ?? (Number(item.success || 0) + Number(item.failed || 0)))));
  const smoothedSuccess = smoothRuntimeValues(points.map((item) => Number(item.success || 0)));
  const smoothedFailed = smoothRuntimeValues(points.map((item) => Number(item.failed || 0)));
  const peakValue = Math.max(1, Math.ceil(Math.max(...smoothedTotal, ...smoothedSuccess, ...smoothedFailed)));
  const tickStep = Math.max(1, Math.ceil(peakValue / 4));
  const maxValue = tickStep * 4;
  const yTicks = Array.from({ length: 5 }, (_, index) => maxValue - index * tickStep);
  const bottomY = paddingTop + plotHeight;
  const xFor = (index: number) => paddingX + (points.length <= 1 ? plotWidth / 2 : (index / (points.length - 1)) * plotWidth);
  const yFor = (value: number) => paddingTop + (1 - value / maxValue) * plotHeight;
  const maxLabelCount = Math.max(3, Math.min(7, Math.floor(plotWidth / 64)));
  const labelStep = Math.max(1, Math.ceil(Math.max(0, points.length - 1) / Math.max(1, maxLabelCount - 1)));
  const labelIndexes = points.map((_, index) => index).filter((index) => index % labelStep === 0 || index === points.length - 1);
  const chartPoints = points.map((item, index) => {
    const pointTotal = Number(item.total ?? (Number(item.success || 0) + Number(item.failed || 0)));
    const success = Number(item.success || 0);
    const failed = Number(item.failed || 0);
    return {
      item,
      index,
      x: xFor(index),
      total: pointTotal,
      success,
      failed,
      totalY: yFor(smoothedTotal[index]),
      successY: yFor(smoothedSuccess[index]),
      failedY: yFor(smoothedFailed[index]),
    };
  });
  const totalLinePoints = chartPoints.map((item) => ({ x: item.x, y: item.totalY }));
  const successLinePoints = chartPoints.map((item) => ({ x: item.x, y: item.successY }));
  const failedLinePoints = chartPoints.map((item) => ({ x: item.x, y: item.failedY }));
  const totalPath = smoothLinePath(totalLinePoints, paddingTop, bottomY);
  const successPath = smoothLinePath(successLinePoints, paddingTop, bottomY);
  const failedPath = smoothLinePath(failedLinePoints, paddingTop, bottomY);
  const hoveredPoint = hoveredIndex === null ? null : chartPoints[hoveredIndex];
  const tooltipWidth = 166;
  const tooltipHeight = 78;
  const tooltipX = hoveredPoint ? clamp(hoveredPoint.x - tooltipWidth / 2, paddingX + 6, width - paddingX - tooltipWidth - 6) : 0;
  const tooltipY = hoveredPoint
    ? clamp(Math.min(hoveredPoint.totalY, hoveredPoint.successY, hoveredPoint.failedY) - tooltipHeight - 12, paddingTop + 6, bottomY - tooltipHeight - 8)
    : 0;

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
      <div className="flex min-h-[278px] items-center justify-center bg-slate-50/60">
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={`最近 ${runtimeWindowText(windowMinutes)} 暂无调用`} />
      </div>
    );
  }

  return (
    <div className="min-h-[278px] bg-white">
      <div className="flex min-h-12 flex-wrap items-center gap-x-6 gap-y-2 border-b border-slate-100 bg-slate-50/40 px-5 py-2.5 text-xs">
        <span className="inline-flex items-center gap-2 text-slate-500">
          <i className="h-4 w-1 rounded-full bg-blue-500" />
          调用量
          <strong className="font-mono text-sm font-semibold text-slate-900 tabular-nums">{numberText(total)}</strong>
        </span>
        <span className="inline-flex items-center gap-2 text-slate-500">
          <i className="h-4 w-1 rounded-full bg-green-500" />
          成功
          <strong className="font-mono text-sm font-semibold text-green-600 tabular-nums">{numberText(totalSuccess)}</strong>
        </span>
        <span className="inline-flex items-center gap-2 text-slate-500">
          <i className="h-4 w-1 rounded-full bg-rose-500" />
          失败
          <strong className="font-mono text-sm font-semibold text-rose-600 tabular-nums">{numberText(totalFailed)}</strong>
        </span>
        <span
          className="inline-flex items-center gap-2 rounded-md bg-rose-50 px-2.5 py-1 text-rose-600"
          title={runtime.error_reasons[0]?.label || "暂无错误原因"}
        >
          <AlertCircle className="size-3.5" />
          错误率
          <strong className="font-mono text-sm font-semibold tabular-nums">{rateText(runtime.error_rate)}%</strong>
        </span>
      </div>
      <div
        ref={chartContainerRef}
        className="w-full px-2 pb-2 pt-1 sm:px-4"
        onMouseLeave={() => setHoveredIndex(null)}
        onMouseMove={(event) => {
          const bounds = event.currentTarget.getBoundingClientRect();
          const svgX = ((event.clientX - bounds.left) / bounds.width) * width;
          const ratio = clamp((svgX - paddingX) / plotWidth, 0, 1);
          setHoveredIndex(Math.round(ratio * Math.max(0, points.length - 1)));
        }}
      >
        <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label="调用量、成功和失败趋势图" className="h-[238px] w-full overflow-visible">
        <defs>
          <filter id="runtime-line-soft-shadow" x="-20%" y="-20%" width="140%" height="150%">
            <feDropShadow dx="0" dy="5" stdDeviation="4" floodColor="#0f172a" floodOpacity="0.08" />
          </filter>
        </defs>
        <rect x={paddingX} y={paddingTop} width={plotWidth} height={plotHeight} rx="12" fill="#f8fafc" />
        {yTicks.map((value, index) => {
          const ratio = index / (yTicks.length - 1);
          const y = paddingTop + ratio * plotHeight;
          return (
            <g key={value}>
              <line x1={paddingX} x2={width - paddingX} y1={y} y2={y} stroke="#e2e8f0" strokeDasharray={ratio === 1 ? "0" : "4 8"} />
              <text x={paddingX - 10} y={y + 4} textAnchor="end" fill="#64748b" fontSize="11" fontWeight="600">{numberText(value)}</text>
            </g>
          );
        })}
        <path d={totalPath} fill="none" stroke="#3b82f6" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" filter="url(#runtime-line-soft-shadow)" />
        <path d={successPath} fill="none" stroke="#22c55e" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />
        <path d={failedPath} fill="none" stroke="#f43f5e" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" strokeDasharray="6 5" />
        {hoveredPoint ? (
          <g pointerEvents="none">
            <line x1={hoveredPoint.x} x2={hoveredPoint.x} y1={paddingTop} y2={bottomY} stroke="#94a3b8" strokeDasharray="3 5" />
            <circle cx={hoveredPoint.x} cy={hoveredPoint.totalY} r="4" fill="#ffffff" stroke="#3b82f6" strokeWidth="2.5" />
            <circle cx={hoveredPoint.x} cy={hoveredPoint.successY} r="4" fill="#ffffff" stroke="#22c55e" strokeWidth="2.5" />
            <circle cx={hoveredPoint.x} cy={hoveredPoint.failedY} r="4" fill="#ffffff" stroke="#f43f5e" strokeWidth="2.5" />
            <rect x={tooltipX} y={tooltipY} width={tooltipWidth} height={tooltipHeight} rx="6" fill="#ffffff" stroke="#cbd5e1" />
            <text x={tooltipX + 10} y={tooltipY + 16} fill="#475569" fontSize="11" fontWeight="600">{hoveredPoint.item.label || "当前时段"}</text>
            <text x={tooltipX + 10} y={tooltipY + 35} fill="#3b82f6" fontSize="11">调用量 {numberText(hoveredPoint.total)}</text>
            <text x={tooltipX + 10} y={tooltipY + 53} fill="#22c55e" fontSize="11">成功 {numberText(hoveredPoint.success)}</text>
            <text x={tooltipX + 88} y={tooltipY + 53} fill="#f43f5e" fontSize="11">失败 {numberText(hoveredPoint.failed)}</text>
          </g>
        ) : null}
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

function RuntimeHealth({
  runtime,
  selectedWindow,
  isUpdating,
  onWindowChange,
  onCustomRangeChange,
}: {
  runtime: RuntimeHealthData;
  selectedWindow: number | "custom";
  isUpdating: boolean;
  onWindowChange: (value: number | "custom") => void;
  onCustomRangeChange: (range: RuntimeRange) => void;
}) {
  const [pendingCustomRange, setPendingCustomRange] = useState<RuntimeRange | null>(null);
  const firstPointTime = runtime.series[0]?.time || runtime.start_time;
  const lastPointTime = runtime.series[runtime.series.length - 1]?.time || runtime.end_time;

  return (
    <section className="space-y-4">
      <Card
        className="h-full [&_.ant-card-head-wrapper]:gap-3 max-sm:[&_.ant-card-extra]:ml-0 max-sm:[&_.ant-card-extra]:w-full max-sm:[&_.ant-card-head-title]:w-full max-sm:[&_.ant-card-head-title]:flex-none max-sm:[&_.ant-card-head-title]:text-left max-sm:[&_.ant-card-head-wrapper]:flex-col max-sm:[&_.ant-card-head-wrapper]:items-stretch"
        styles={{ header: { paddingTop: 12, paddingBottom: 12 }, body: { padding: 0 } }}
        title={
          <div>
            <div className="flex items-center gap-2">
              <span className="flex size-8 items-center justify-center rounded-md bg-blue-50 text-blue-600"><Activity className="size-4" /></span>
              <span>调用趋势</span>
            </div>
            <div className="mt-1 font-mono text-xs font-normal text-slate-400">
              {formatShanghaiDateTime(firstPointTime).slice(0, 10)} 至 {formatShanghaiDateTime(lastPointTime).slice(0, 10)}
            </div>
          </div>
        }
        extra={
          <div className="flex flex-wrap items-center justify-start gap-2 sm:justify-end">
            <Segmented
              aria-label="调用趋势统计范围"
              disabled={isUpdating}
              options={RUNTIME_WINDOW_OPTIONS}
              size="small"
              value={selectedWindow}
              onChange={(value) => onWindowChange(value === "custom" ? "custom" : Number(value))}
            />
            {selectedWindow === "custom" ? (
              <DatePicker.RangePicker
                aria-label="自定义调用趋势日期"
                allowClear={false}
                className="w-full sm:w-auto"
                disabled={isUpdating}
                format="YYYY-MM-DD"
                size="small"
                onCalendarChange={(dates) => {
                  if (!dates?.[0] || !dates[1]) return;
                  setPendingCustomRange({ start: dates[0].startOf("day").toISOString(), end: dates[1].endOf("day").toISOString() });
                }}
                onChange={(dates) => {
                  if (!dates?.[0] || !dates[1]) {
                    setPendingCustomRange(null);
                    return;
                  }
                  setPendingCustomRange({ start: dates[0].startOf("day").toISOString(), end: dates[1].endOf("day").toISOString() });
                }}
              />
            ) : null}
            {selectedWindow === "custom" ? (
              <Button size="small" type="primary" disabled={!pendingCustomRange || isUpdating} onClick={() => pendingCustomRange && onCustomRangeChange(pendingCustomRange)}>
                查询
              </Button>
            ) : null}
          </div>
        }
      >
        <RuntimeTrendChart runtime={runtime} />
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
        const title = String(record.error_title || "调用失败");
        const model = String(record.model || "-");
        const endpoint = String(record.endpoint || "").trim();
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
                <span className="min-w-0 truncate font-medium text-slate-900" title={title}>
                  {title}
                </span>
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-2">
                <Tag color="red" className="m-0">失败</Tag>
                {record.error_code ? <Tag className="m-0 font-mono">{record.error_code}</Tag> : null}
                {record.error_category_label ? <Tag color="blue" className="m-0">{record.error_category_label}</Tag> : null}
                <Tag color={record.retryable ? "green" : "default"} className="m-0">{record.retryable ? "可重试" : "需检查"}</Tag>
                <Tag className="m-0 font-mono">{model}</Tag>
                {endpoint ? <Tag color="blue" className="m-0 font-mono">{endpoint}</Tag> : null}
              </div>
            </div>

            <div className="min-w-0 rounded-md border border-rose-100 bg-rose-50 px-3 py-2">
              <div className="text-xs font-medium text-rose-500">错误信息</div>
              <div className="mt-1 line-clamp-2 text-sm text-rose-700" title={error}>
                {error}
              </div>
              {record.hint ? <div className="mt-1 text-xs text-rose-500">建议：{record.hint}</div> : null}
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
  const [scheduler, setScheduler] = useState<SchedulerDiagnostics | null>(null);
  const [capacity, setCapacity] = useState<ImagePoolCapacity | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [runtimeWindow, setRuntimeWindow] = useState<number | "custom">(7 * 24 * 60);
  const [customRuntimeRange, setCustomRuntimeRange] = useState<RuntimeRange | null>(null);

  const loadDashboard = useCallback(async (silent = false, window = runtimeWindow, range = customRuntimeRange) => {
    if (silent) {
      setIsRefreshing(true);
    } else {
      setIsLoading(true);
    }
    try {
      const dashboard = await fetchDashboard(typeof window === "number" ? window : 7 * 24 * 60, window === "custom" ? range : null);
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
  }, [customRuntimeRange, runtimeWindow]);

  useEffect(() => {
    void loadDashboard();
    void fetchSchedulerDiagnostics().then(setScheduler).catch(() => undefined);
    void fetchImagePoolCapacity().then(setCapacity).catch(() => undefined);
  }, [loadDashboard]);

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
        const [latest, latestScheduler] = await Promise.all([
          fetchSystemLoad(),
          fetchSchedulerDiagnostics().catch(() => null),
        ]);
        const latestCapacity = await fetchImagePoolCapacity().catch(() => null);
        if (active) {
          setSystemLoad((current) => newerSystemLoad(current, latest));
          if (latestScheduler) setScheduler(latestScheduler);
          if (latestCapacity) setCapacity(latestCapacity);
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
  }, [data, loadDashboard]);

  const handleRuntimeWindowChange = (value: number | "custom") => {
    setRuntimeWindow(value);
    if (value === "custom") {
      return;
    }
    setCustomRuntimeRange(null);
    void loadDashboard(true, value, null);
  };

  const handleCustomRuntimeRangeChange = (range: RuntimeRange) => {
    setCustomRuntimeRange(range);
    void loadDashboard(true, "custom", range);
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

      {scheduler && systemLoad ? (
        <OperationsOverview
          scheduler={scheduler}
          system={systemLoad}
          summary={{
            accounts: {
              active: data.accounts.active,
              total: totalAccounts,
              quota: data.accounts.unlimited_quota_count > 0 ? "不限" : numberText(data.accounts.total_quota),
              limited: data.accounts.limited,
            },
            calls: {
              total: totalCalls,
              failed: failedCalls,
              successPercent: callSuccessPercent,
            },
            taskHistory: data.tasks.total,
          }}
        />
      ) : null}

      <ImagePoolConcurrency capacity={capacity} />

      {data.calls.runtime ? (
        <RuntimeHealth
          runtime={data.calls.runtime}
          selectedWindow={runtimeWindow}
          isUpdating={isRefreshing}
          onWindowChange={handleRuntimeWindowChange}
          onCustomRangeChange={handleCustomRuntimeRangeChange}
        />
      ) : null}

      <section className="grid gap-4 xl:grid-cols-3">
        <Card title="今日接口分布">
          <EntryBars items={sortedEntries(todayEndpointEntries)} />
        </Card>
        <Card title="今日模型使用">
          <EntryBars items={sortedEntries(todayModelEntries)} />
        </Card>
        <Card title="GPT账号类型">
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
