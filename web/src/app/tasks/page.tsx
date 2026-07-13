"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Button, Card, Empty, Modal, Progress, Space, Table, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { Activity, Ban, LoaderCircle, RefreshCw, TimerReset } from "lucide-react";
import { toast } from "sonner";

import { cancelImageTask, fetchImageTasks, fetchImageTaskStatus, type ImageTask, type ImageTaskStatusLog } from "@/lib/api";
import { formatShanghaiDateTime } from "@/lib/datetime";
import { useAuthGuard } from "@/lib/use-auth-guard";

function statusTag(status: ImageTask["status"]) {
  const color = {
    queued: "blue",
    running: "processing",
    success: "green",
    error: "red",
  }[status] || "default";
  const label = {
    queued: "排队",
    running: "运行中",
    success: "成功",
    error: "失败",
  }[status] || status;
  return <Tag color={color}>{label}</Tag>;
}

function modeTag(mode: ImageTask["mode"]) {
  return <Tag>{mode === "edit" ? "图生图" : "文生图"}</Tag>;
}

function taskProgress(item: ImageTask) {
  const percent = typeof item.progress_percent === "number" ? item.progress_percent : item.status === "success" ? 100 : 0;
  const progressStatus = item.status === "error" ? "exception" : item.status === "success" ? "success" : "active";

  return (
    <div className="min-w-[180px]">
      <Progress percent={percent} size="small" status={progressStatus} />
      <Typography.Text type="secondary" className="block text-xs">
        {item.status === "success" ? "已完成" : item.progress || (item.status === "queued" ? "排队中" : item.status === "error" ? "失败" : "处理中")}
      </Typography.Text>
    </div>
  );
}

function levelColor(level?: string) {
  return {
    processing: "text-blue-600 bg-blue-50 border-blue-100",
    success: "text-emerald-700 bg-emerald-50 border-emerald-100",
    warning: "text-amber-700 bg-amber-50 border-amber-100",
    error: "text-rose-700 bg-rose-50 border-rose-100",
  }[level || ""] || "text-slate-700 bg-slate-50 border-slate-100";
}

function renderDetailValue(value: unknown) {
  if (value === null || value === undefined || value === "") {
    return "-";
  }
  if (typeof value === "object") {
    try {
      return JSON.stringify(value);
    } catch {
      return String(value);
    }
  }
  return String(value);
}

function statusLogList(logs: ImageTaskStatusLog[] | undefined) {
  if (!logs || logs.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无实时日志" />;
  }
  return (
    <div className="max-h-[52vh] space-y-2 overflow-y-auto pr-1">
      {logs.map((log, index) => (
        <div key={`${log.time}-${index}`} className={`rounded-lg border px-3 py-2 ${levelColor(log.level)}`}>
          <div className="flex flex-col gap-1 sm:flex-row sm:items-start sm:justify-between">
            <Typography.Text strong className="!text-inherit">
              {log.message}
            </Typography.Text>
            <Typography.Text className="shrink-0 !text-xs !text-inherit opacity-70">
              {formatShanghaiDateTime(log.time)}
            </Typography.Text>
          </div>
          {log.details && Object.keys(log.details).length > 0 ? (
            <div className="mt-2 grid gap-1 text-xs sm:grid-cols-2">
              {Object.entries(log.details).map(([key, value]) => (
                <div key={key} className="rounded bg-white/70 px-2 py-1">
                  <span className="text-slate-500">{key}: </span>
                  <span className="break-all text-slate-700">{renderDetailValue(value)}</span>
                </div>
              ))}
            </div>
          ) : null}
        </div>
      ))}
    </div>
  );
}

function TasksContent() {
  const [items, setItems] = useState<ImageTask[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [pendingId, setPendingId] = useState("");
  const [statusModalOpen, setStatusModalOpen] = useState(false);
  const [statusTaskId, setStatusTaskId] = useState("");
  const [statusTask, setStatusTask] = useState<ImageTask | null>(null);
  const [statusLoading, setStatusLoading] = useState(false);

  const load = useCallback(async (silent = false) => {
    if (!silent) {
      setIsLoading(true);
    }
    try {
      const data = await fetchImageTasks([]);
      setItems(data.items);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载任务失败");
    } finally {
      setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const hasRunning = items.some((item) => item.status === "queued" || item.status === "running");
    if (!hasRunning) {
      return;
    }
    const timer = window.setInterval(() => {
      void load(true);
    }, 2500);
    return () => window.clearInterval(timer);
  }, [items, load]);

  const loadTaskStatus = useCallback(async (taskId: string, silent = false) => {
    if (!taskId) {
      return;
    }
    if (!silent) {
      setStatusLoading(true);
    }
    try {
      const data = await fetchImageTaskStatus(taskId);
      setStatusTask(data);
      setItems((prev) => prev.map((item) => (item.id === data.id ? { ...item, ...data, status_logs: item.status_logs } : item)));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载实时状态失败");
    } finally {
      if (!silent) {
        setStatusLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    if (!statusModalOpen || !statusTaskId) {
      return;
    }
    void loadTaskStatus(statusTaskId, true);
    const timer = window.setInterval(() => {
      void loadTaskStatus(statusTaskId, true);
    }, 1500);
    return () => window.clearInterval(timer);
  }, [loadTaskStatus, statusModalOpen, statusTaskId]);

  const summary = useMemo(() => {
    return {
      total: items.length,
      running: items.filter((item) => item.status === "running").length,
      queued: items.filter((item) => item.status === "queued").length,
      error: items.filter((item) => item.status === "error").length,
    };
  }, [items]);

  const handleCancel = useCallback((item: ImageTask) => {
    Modal.confirm({
      title: "取消任务",
      content: `确认取消任务 ${item.id} 吗？已经发往上游的请求可能仍会消耗时间，但本地任务会停止更新。`,
      okText: "取消任务",
      cancelText: "返回",
      okButtonProps: { danger: true },
      onOk: async () => {
        setPendingId(item.id);
        try {
          await cancelImageTask(item.id);
          toast.success("任务已取消");
          await load(true);
        } catch (error) {
          toast.error(error instanceof Error ? error.message : "取消任务失败");
        } finally {
          setPendingId("");
        }
      },
    });
  }, [load]);

  const openStatusModal = useCallback((item: ImageTask) => {
    setStatusTaskId(item.id);
    setStatusTask(item);
    setStatusModalOpen(true);
    void loadTaskStatus(item.id);
  }, [loadTaskStatus]);

  const columns = useMemo<ColumnsType<ImageTask>>(
    () => [
      { title: "任务 ID", dataIndex: "id", ellipsis: true },
      { title: "模式", dataIndex: "mode", width: 96, render: modeTag },
      { title: "状态", dataIndex: "status", width: 96, render: statusTag },
      { title: "模型", dataIndex: "model", width: 150, ellipsis: true },
      { title: "进度", dataIndex: "progress_percent", width: 230, render: (_, item) => taskProgress(item) },
      {
        title: "实时状态",
        dataIndex: "realtime_status",
        width: 240,
        render: (_, item) => (
          <div className="min-w-[180px]">
            <Typography.Text ellipsis className="block max-w-[210px]">
              {item.realtime_status || item.progress || (item.status === "queued" ? "排队中" : item.status === "success" ? "已完成" : item.status === "error" ? "失败" : "处理中")}
            </Typography.Text>
            <Typography.Text type="secondary" className="block text-xs">
              日志 {item.status_log_count ?? 0} 条
              {typeof item.used_account_count === "number" ? ` · 账号 ${item.used_account_count}` : ""}
              {typeof item.failed_account_count === "number" ? ` · 失败 ${item.failed_account_count}` : ""}
            </Typography.Text>
          </div>
        ),
      },
      { title: "耗时", dataIndex: "duration_ms", width: 110, render: (value) => typeof value === "number" ? `${(value / 1000).toFixed(1)}s` : "-" },
      { title: "创建时间", dataIndex: "created_at", width: 180, render: (value) => formatShanghaiDateTime(value) },
      { title: "更新时间", dataIndex: "updated_at", width: 180, render: (value) => formatShanghaiDateTime(value) },
      {
        title: "操作",
        width: 210,
        render: (_, item) => {
          const cancellable = item.status === "queued" || item.status === "running";
          return (
            <Space size={6}>
              <Button
                size="small"
                icon={<Activity className="size-3.5" />}
                onClick={() => openStatusModal(item)}
              >
                实时状态
              </Button>
              <Button
                size="small"
                danger
                icon={pendingId === item.id ? <LoaderCircle className="size-3.5 animate-spin" /> : <Ban className="size-3.5" />}
                disabled={!cancellable || Boolean(pendingId)}
                onClick={() => handleCancel(item)}
              >
                取消
              </Button>
            </Space>
          );
        },
      },
    ],
    [handleCancel, openStatusModal, pendingId],
  );

  return (
    <div className="dashboard-console">
      <section className="flex flex-col gap-4 rounded-lg border border-slate-200 bg-white px-5 py-5 shadow-sm lg:flex-row lg:items-center lg:justify-between">
        <div>
          <Typography.Title level={2} className="!mb-1 !text-2xl">任务队列</Typography.Title>
          <Typography.Text type="secondary">统一查看任务状态，并取消排队或运行中的任务。</Typography.Text>
        </div>
        <Button icon={<RefreshCw className="size-4" />} onClick={() => void load(true)}>
          刷新
        </Button>
      </section>

      <section className="grid gap-4 md:grid-cols-4">
        <Card><Space><TimerReset className="size-5 text-blue-500" /><span>总任务</span><strong>{summary.total}</strong></Space></Card>
        <Card><Space><LoaderCircle className="size-5 text-blue-500" /><span>运行中</span><strong>{summary.running}</strong></Space></Card>
        <Card><Space><TimerReset className="size-5 text-amber-500" /><span>排队</span><strong>{summary.queued}</strong></Space></Card>
        <Card><Space><Ban className="size-5 text-rose-500" /><span>失败</span><strong>{summary.error}</strong></Space></Card>
      </section>

      <Card styles={{ body: { padding: 0 } }}>
        <Table
          rowKey="id"
          columns={columns}
          dataSource={items}
          loading={isLoading}
          size="small"
          pagination={{ pageSize: 20, showSizeChanger: true }}
          scroll={{ x: 1480 }}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无任务" /> }}
        />
      </Card>

      <Modal
        title="实时处理状态"
        open={statusModalOpen}
        onCancel={() => setStatusModalOpen(false)}
        width={820}
        footer={
          <div className="flex items-center justify-end gap-2">
            <Button
              className="inline-flex items-center justify-center"
              icon={<RefreshCw className="size-4" />}
              loading={statusLoading}
              onClick={() => void loadTaskStatus(statusTaskId)}
            >
              刷新
            </Button>
            <Button className="inline-flex items-center justify-center" type="primary" onClick={() => setStatusModalOpen(false)}>
              关闭
            </Button>
          </div>
        }
      >
        <div className="space-y-4">
          <div className="rounded-lg border border-slate-200 bg-slate-50 px-4 py-3">
            <div className="flex flex-wrap items-center gap-2">
              {statusTask ? statusTag(statusTask.status) : null}
              {statusTask ? modeTag(statusTask.mode) : null}
              <Typography.Text strong>{statusTask?.id || statusTaskId}</Typography.Text>
            </div>
            <div className="mt-3 grid gap-2 text-sm sm:grid-cols-2">
              <div>当前状态：<Typography.Text>{statusTask?.realtime_status || "-"}</Typography.Text></div>
              <div>创建时间：<Typography.Text>{formatShanghaiDateTime(statusTask?.created_at)}</Typography.Text></div>
              <div>模型：<Typography.Text>{statusTask?.model || "-"}</Typography.Text></div>
              <div>更新时间：<Typography.Text>{formatShanghaiDateTime(statusTask?.updated_at)}</Typography.Text></div>
              <div>会话：<Typography.Text copyable={Boolean(statusTask?.conversation_id)}>{statusTask?.conversation_id || "-"}</Typography.Text></div>
              <div>进度：<Typography.Text>{statusTask?.progress_percent ?? 0}%</Typography.Text></div>
              <div>账号：<Typography.Text>{statusTask?.used_account_count ?? 0} 个 / 失败 {statusTask?.failed_account_count ?? 0}</Typography.Text></div>
            </div>
          </div>
          {statusLogList(statusTask?.status_logs)}
        </div>
      </Modal>
    </div>
  );
}

export default function TasksPage() {
  const { isCheckingAuth, session } = useAuthGuard(["admin"]);

  if (isCheckingAuth || !session || session.role !== "admin") {
    return (
      <div className="flex min-h-[40vh] items-center justify-center">
        <LoaderCircle className="size-5 animate-spin text-stone-400" />
      </div>
    );
  }

  return <TasksContent />;
}
