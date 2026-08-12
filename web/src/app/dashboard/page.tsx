"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Alert, Button, Card, Empty, Progress, Skeleton, Tag, Typography } from "antd";
import {
  Activity,
  AlertCircle,
  Boxes,
  Cpu,
  HardDrive,
  LoaderCircle,
  MemoryStick,
  Network,
  RefreshCw,
  UsersRound,
  Webhook,
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

  const loadDashboard = useCallback(async (silent = false) => {
    if (silent) {
      setIsRefreshing(true);
    } else {
      setIsLoading(true);
    }
    try {
      const dashboard = await fetchDashboard();
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
  }, []);

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
  const todayCalls = data.calls;
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
              <Tag color="red" className="m-0">{data.calls.recent_failed?.length || 0}</Tag>
            </div>
          }
          styles={{ body: { padding: 0 } }}
        >
          <RecentFailures items={data.calls.recent_failed || []} />
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
