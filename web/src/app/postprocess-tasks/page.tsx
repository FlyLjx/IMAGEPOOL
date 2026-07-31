"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Button, Card, Empty, Image, Modal, Space, Table, Tag, Tooltip, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { CircleCheck, CircleX, LoaderCircle, RefreshCw, ScanSearch, TimerReset, WandSparkles } from "lucide-react";
import { toast } from "sonner";

import { fetchPostprocessTaskHistory, type PostprocessTask } from "@/lib/api";
import { formatShanghaiDateTime } from "@/lib/datetime";
import { useAuthGuard } from "@/lib/use-auth-guard";

function statusTag(status: PostprocessTask["status"]) {
  const meta = {
    queued: { color: "blue", label: "排队" },
    running: { color: "processing", label: "处理中" },
    success: { color: "green", label: "完成" },
    skipped: { color: "default", label: "跳过" },
    error: { color: "red", label: "失败" },
  }[status];
  return <Tag color={meta?.color || "default"}>{meta?.label || status}</Tag>;
}

function operationTags(item: PostprocessTask) {
  return (
    <Space size={[4, 4]} wrap>
      {item.hd_repair ? <Tag color="cyan">高清修复</Tag> : null}
    </Space>
  );
}

function resultTag(item: PostprocessTask) {
  if (item.status === "error") {
    return <Tag color="red">处理失败</Tag>;
  }
  if (item.restored) {
    return <Tag color="cyan">已修复</Tag>;
  }
  if (item.status === "success") {
    return <Tag color="green">已处理</Tag>;
  }
  if (item.status === "skipped" || item.skipped) {
    return <Tag>无需处理</Tag>;
  }
  return <Tag color="processing">等待结果</Tag>;
}

function formatBytes(value?: number) {
  if (!value || value <= 0) {
    return "-";
  }
  if (value >= 1024 * 1024) {
    return `${(value / 1024 / 1024).toFixed(2)} MB`;
  }
  return `${(value / 1024).toFixed(1)} KB`;
}

function comparisonImageURL(path?: string) {
  return path ? `/images/${encodeURIComponent(path)}` : "";
}

function PostprocessTasksContent() {
  const [items, setItems] = useState<PostprocessTask[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(50);
  const [total, setTotal] = useState(0);
  const [compareTask, setCompareTask] = useState<PostprocessTask | null>(null);

  const load = useCallback(async (silent = false) => {
    if (!silent) {
      setIsLoading(true);
    }
    try {
      const data = await fetchPostprocessTaskHistory({ page, pageSize });
      setItems(data.items);
      setTotal(data.total);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载修复记录失败");
    } finally {
      setIsLoading(false);
    }
  }, [page, pageSize]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!items.some((item) => item.status === "queued" || item.status === "running")) {
      return;
    }
    const timer = window.setInterval(() => void load(true), 1500);
    return () => window.clearInterval(timer);
  }, [items, load]);

  const summary = useMemo(() => ({
    total,
    active: items.filter((item) => item.status === "queued" || item.status === "running").length,
    completed: items.filter((item) => item.status === "success" || item.restored).length,
    failed: items.filter((item) => item.status === "error").length,
  }), [items, total]);

  const columns = useMemo<ColumnsType<PostprocessTask>>(() => [
    {
      title: "高清任务 ID",
      dataIndex: "id",
      width: 230,
      render: (value) => <Typography.Text copyable={{ text: value }} className="font-mono text-xs">{value}</Typography.Text>,
    },
    {
      title: "关联生图任务",
      dataIndex: "parent_task_id",
      width: 230,
      render: (value) => value
        ? <Typography.Text copyable={{ text: value }} className="font-mono text-xs">{value}</Typography.Text>
        : "-",
    },
    { title: "状态", dataIndex: "status", width: 100, render: statusTag },
    { title: "处理类型", width: 180, render: (_, item) => operationTags(item) },
    { title: "处理结果", width: 150, render: (_, item) => resultTag(item) },
    {
      title: "前后对比",
      width: 120,
      render: (_, item) => {
        const available = Boolean(item.input_image_path && item.output_image_path);
        return available ? (
          <Button size="small" icon={<ScanSearch className="size-4" />} onClick={() => setCompareTask(item)}>
            查看对比
          </Button>
        ) : (
          <Tooltip title={item.status === "success" ? "旧任务未保存对比图片" : "任务完成后可查看"}>
            <span><Button size="small" icon={<ScanSearch className="size-4" />} disabled>暂无对比</Button></span>
          </Tooltip>
        );
      },
    },
    { title: "模型", dataIndex: "model", width: 160, ellipsis: true, render: (value) => value || "-" },
    { title: "目标尺寸", dataIndex: "requested_size", width: 120, render: (value) => value || "-" },
    {
      title: "文件大小",
      width: 170,
      render: (_, item) => `${formatBytes(item.input_bytes)} -> ${formatBytes(item.output_bytes)}`,
    },
    { title: "耗时", dataIndex: "duration_ms", width: 100, render: (value) => typeof value === "number" ? `${(value / 1000).toFixed(1)}s` : "-" },
    { title: "创建时间", dataIndex: "created_at", width: 180, render: (value) => formatShanghaiDateTime(value) },
    { title: "完成时间", dataIndex: "finished_at", width: 180, render: (value) => formatShanghaiDateTime(value) },
    {
      title: "错误",
      dataIndex: "error",
      width: 260,
      ellipsis: true,
      render: (value) => value ? <Typography.Text type="danger">{value}</Typography.Text> : "-",
    },
  ], []);

  return (
    <div className="dashboard-console">
      <section className="flex items-center justify-between gap-4 rounded-lg border border-slate-200 bg-white px-5 py-5 shadow-sm">
        <Typography.Title level={2} className="!mb-0 !text-2xl">修复记录</Typography.Title>
        <Button icon={<RefreshCw className="size-4" />} onClick={() => void load(true)}>刷新</Button>
      </section>

      <section className="grid gap-4 md:grid-cols-4">
        <Card><Space><TimerReset className="size-5 text-blue-500" /><span>总记录</span><strong>{summary.total}</strong></Space></Card>
        <Card><Space><LoaderCircle className="size-5 text-blue-500" /><span>本页处理中</span><strong>{summary.active}</strong></Space></Card>
        <Card><Space><CircleCheck className="size-5 text-emerald-500" /><span>本页已处理</span><strong>{summary.completed}</strong></Space></Card>
        <Card><Space><CircleX className="size-5 text-rose-500" /><span>本页失败</span><strong>{summary.failed}</strong></Space></Card>
      </section>

      <Card styles={{ body: { padding: 0 } }}>
        <Table
          className="[&_.ant-table-body]:!min-h-[420px]"
          rowKey="id"
          columns={columns}
          dataSource={items}
          loading={isLoading}
          size="small"
          pagination={{
            current: page,
            pageSize,
            total,
            showSizeChanger: true,
            pageSizeOptions: [20, 50, 100, 200],
            showTotal: (count, range) => `第 ${range[0]}-${range[1]} 条 / 共 ${count} 条`,
            onChange: (nextPage, nextPageSize) => {
              if (nextPageSize !== pageSize) {
                setPage(1);
                setPageSize(nextPageSize);
                return;
              }
              setPage(nextPage);
            },
          }}
          scroll={{ x: 2000, y: 420 }}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无高清处理记录" /> }}
        />
      </Card>

      <Modal
        title="处理前后对比"
        open={Boolean(compareTask)}
        onCancel={() => setCompareTask(null)}
        footer={null}
        width={1040}
        destroyOnHidden
      >
        {compareTask ? (
          <div className="grid gap-4 md:grid-cols-2">
            <section className="overflow-hidden rounded-md border border-slate-200 bg-slate-50">
              <div className="flex items-center justify-between border-b border-slate-200 bg-white px-3 py-2">
                <strong className="text-sm text-slate-800">处理前</strong>
                <span className="font-mono text-xs text-slate-500">{formatBytes(compareTask.input_bytes)}</span>
              </div>
              <div className="flex h-[440px] items-center justify-center p-3">
                <Image
                  src={comparisonImageURL(compareTask.input_image_path)}
                  alt="处理前图片"
                  className="!max-h-[416px] !w-auto !max-w-full object-contain"
                  preview={{ mask: "查看原图" }}
                />
              </div>
            </section>
            <section className="overflow-hidden rounded-md border border-slate-200 bg-slate-50">
              <div className="flex items-center justify-between border-b border-slate-200 bg-white px-3 py-2">
                <strong className="text-sm text-slate-800">处理后</strong>
                <span className="font-mono text-xs text-slate-500">{formatBytes(compareTask.output_bytes)}</span>
              </div>
              <div className="flex h-[440px] items-center justify-center p-3">
                <Image
                  src={comparisonImageURL(compareTask.output_image_path)}
                  alt="处理后图片"
                  className="!max-h-[416px] !w-auto !max-w-full object-contain"
                  preview={{ mask: "查看原图" }}
                />
              </div>
            </section>
          </div>
        ) : null}
      </Modal>
    </div>
  );
}

export default function PostprocessTasksPage() {
  const { isCheckingAuth, session } = useAuthGuard(["admin"]);
  if (isCheckingAuth || !session || session.role !== "admin") {
    return (
      <div className="flex min-h-[40vh] items-center justify-center">
        <WandSparkles className="size-5 animate-pulse text-slate-400" />
      </div>
    );
  }
  return <PostprocessTasksContent />;
}
