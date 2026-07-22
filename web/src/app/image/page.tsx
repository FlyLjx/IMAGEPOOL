"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button, Card, Empty, Image, Input, Modal, Progress, Select, Tag, Tooltip, Typography } from "antd";
import {
  CheckCircle2,
  CircleAlert,
  Clock3,
  Copy,
  FileImage,
  ImagePlus,
  LoaderCircle,
  RefreshCw,
  Sparkles,
  X,
} from "lucide-react";
import { toast } from "sonner";

import {
  createImageEditTask,
  createImageGenerationTask,
  fetchImageTaskStatus,
  fetchImageTasks,
  type ImageTask,
  type ImageTaskStatusLog,
} from "@/lib/api";
import { formatShanghaiDateTime } from "@/lib/datetime";
import { useAuthGuard } from "@/lib/use-auth-guard";

const MODEL_OPTIONS = ["gpt-image-2", "codex-gpt-image-2", "plus-codex-gpt-image-2"];
const SIZE_OPTIONS = ["1024x1024", "1536x1024", "1024x1536"];
const QUALITY_OPTIONS = ["auto", "low", "medium", "high"];
const MAX_REFERENCE_FILES = 4;

function newClientTaskID() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return `web-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

function imageSource(image: { url?: string; b64_json?: string; mime_type?: string; format?: string }) {
  if (image.url) return image.url;
  if (image.b64_json) return `data:${imageMimeType(image)};base64,${image.b64_json}`;
  return "";
}

function imageMimeType(image: { b64_json?: string; mime_type?: string; format?: string }) {
  const explicit = image.mime_type?.trim().toLowerCase();
  if (explicit?.startsWith("image/")) return explicit;
  switch (image.format?.trim().toLowerCase()) {
    case "jpg":
    case "jpeg":
      return "image/jpeg";
    case "webp":
      return "image/webp";
    case "gif":
      return "image/gif";
    case "png":
      return "image/png";
    default:
      return detectImageMimeTypeFromBase64(image.b64_json) || "image/png";
  }
}

function detectImageMimeTypeFromBase64(value?: string) {
  if (!value) return "";
  const head = value.slice(0, 32);
  if (head.startsWith("iVBORw0KGgo")) return "image/png";
  if (head.startsWith("/9j/")) return "image/jpeg";
  if (head.startsWith("UklGR")) return "image/webp";
  if (head.startsWith("R0lGOD")) return "image/gif";
  return "";
}

function statusTone(status: ImageTask["status"]) {
  if (status === "success") return "success";
  if (status === "error") return "error";
  if (status === "running") return "processing";
  return "default";
}

function statusLabel(status: ImageTask["status"]) {
  return { queued: "等待处理", running: "处理中", success: "已完成", error: "生成失败" }[status] || status;
}

function elapsedLabel(task: ImageTask) {
  const seconds = task.duration_ms ? task.duration_ms / 1000 : task.elapsed_secs;
  if (!seconds || !Number.isFinite(seconds)) return "刚刚提交";
  return `${seconds.toFixed(seconds >= 10 ? 0 : 1)} 秒`;
}

function TaskStatusModal({ task, onClose }: { task: ImageTask | null; onClose: () => void }) {
  const logs = task?.status_logs || [];
  const renderDetails = (log: ImageTaskStatusLog) => {
    if (!log.details || Object.keys(log.details).length === 0) return null;
    return (
      <div className="mt-2 grid gap-1 text-xs sm:grid-cols-2">
        {Object.entries(log.details).map(([key, value]) => (
          <div key={key} className="rounded bg-white px-2 py-1 text-slate-500">
            <span>{key}: </span>
            <span className="break-all text-slate-700">{typeof value === "object" ? JSON.stringify(value) : String(value)}</span>
          </div>
        ))}
      </div>
    );
  };

  return (
    <Modal title="实时处理日志" open={Boolean(task)} onCancel={onClose} footer={<Button type="primary" onClick={onClose}>关闭</Button>} width={780}>
      {!task ? null : (
        <div className="space-y-4">
          <div className="rounded-lg border border-slate-200 bg-slate-50 px-4 py-3 text-sm">
            <div className="flex flex-wrap items-center gap-2">
              <Tag color={statusTone(task.status)}>{statusLabel(task.status)}</Tag>
              <Typography.Text copyable={{ icon: <Copy className="size-3.5" /> }}>{task.id}</Typography.Text>
            </div>
            <div className="mt-3 grid gap-2 sm:grid-cols-2">
              <span>当前状态：{task.realtime_status || task.progress || "-"}</span>
              <span>模型：{task.model || "gpt-image-2"}</span>
              <span>耗时：{elapsedLabel(task)}</span>
            </div>
          </div>
          {logs.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无处理日志" /> : (
            <div className="max-h-[52vh] space-y-2 overflow-y-auto pr-1">
              {logs.map((log, index) => (
                <div key={`${log.time}-${index}`} className="rounded-lg border border-slate-200 bg-slate-50 px-3 py-2">
                  <div className="flex flex-col gap-1 sm:flex-row sm:items-start sm:justify-between">
                    <span className="font-medium text-slate-800">{log.message || log.progress || "状态更新"}</span>
                    <span className="shrink-0 text-xs text-slate-400">{formatShanghaiDateTime(log.time)}</span>
                  </div>
                  {renderDetails(log)}
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </Modal>
  );
}

function ImageWorkspace() {
  const [prompt, setPrompt] = useState("");
  const [model, setModel] = useState(MODEL_OPTIONS[0]);
  const [size, setSize] = useState(SIZE_OPTIONS[0]);
  const [quality, setQuality] = useState("auto");
  const [references, setReferences] = useState<File[]>([]);
  const [tasks, setTasks] = useState<ImageTask[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [statusTask, setStatusTask] = useState<ImageTask | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);

  const loadTaskStatus = useCallback(async (taskId: string, silent = false) => {
    try {
      const task = await fetchImageTaskStatus(taskId);
      setStatusTask((current) => current?.id === taskId ? task : current);
    } catch (error) {
      if (!silent) {
        toast.error(error instanceof Error ? error.message : "加载任务日志失败");
      }
    }
  }, []);

  const loadTasks = useCallback(async (silent = false) => {
    if (!silent) setIsLoading(true);
    try {
      const result = await fetchImageTasks([]);
      setTasks(result.items || []);
      setStatusTask((current) => {
        if (!current) return null;
        const latest = (result.items || []).find((item) => item.id === current.id);
        return latest ? { ...latest, status_logs: current.status_logs } : current;
      });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载图片任务失败");
    } finally {
      if (!silent) setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadTasks();
  }, [loadTasks]);

  const hasActiveTask = tasks.some((task) => task.status === "queued" || task.status === "running");
  useEffect(() => {
    if (!hasActiveTask) return;
    const timer = window.setInterval(() => void loadTasks(true), 1800);
    return () => window.clearInterval(timer);
  }, [hasActiveTask, loadTasks]);

  useEffect(() => {
    if (!statusTask || (statusTask.status !== "queued" && statusTask.status !== "running")) return;
    const timer = window.setInterval(() => void loadTaskStatus(statusTask.id, true), 1500);
    return () => window.clearInterval(timer);
  }, [loadTaskStatus, statusTask?.id, statusTask?.status]);

  const openTaskStatus = useCallback((task: ImageTask) => {
    setStatusTask(task);
    void loadTaskStatus(task.id);
  }, [loadTaskStatus]);

  const completedTasks = useMemo(() => tasks.filter((task) => task.status === "success"), [tasks]);
  const activeTasks = useMemo(() => tasks.filter((task) => task.status === "queued" || task.status === "running"), [tasks]);
  const failedTasks = useMemo(() => tasks.filter((task) => task.status === "error").slice(0, 5), [tasks]);

  const addReferences = (files: FileList | null) => {
    if (!files) return;
    const selected = Array.from(files).filter((file) => file.type.startsWith("image/"));
    if (selected.length !== files.length) toast.error("只能添加图片文件");
    setReferences((current) => {
      const next = [...current, ...selected].slice(0, MAX_REFERENCE_FILES);
      if (current.length + selected.length > MAX_REFERENCE_FILES) toast.error(`最多添加 ${MAX_REFERENCE_FILES} 张参考图`);
      return next;
    });
    if (fileInput.current) fileInput.current.value = "";
  };

  const submit = async () => {
    const cleanPrompt = prompt.trim();
    if (!cleanPrompt) {
      toast.error("请输入图片描述");
      return;
    }
    setIsSubmitting(true);
    try {
      const clientTaskID = newClientTaskID();
      const task = references.length > 0
        ? await createImageEditTask(clientTaskID, references, cleanPrompt, model, size, quality)
        : await createImageGenerationTask(clientTaskID, cleanPrompt, model, size, quality);
      setTasks((current) => [task, ...current.filter((item) => item.id !== task.id)]);
      toast.success(references.length > 0 ? "参考图任务已提交" : "生图任务已提交");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "提交图片任务失败");
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <div className="dashboard-console">
      <section className="flex flex-col gap-4 rounded-lg border border-slate-200 bg-white px-5 py-5 shadow-sm lg:flex-row lg:items-center lg:justify-between">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-slate-900"><Sparkles className="size-5 text-sky-600" /><Typography.Title level={2} className="!mb-0 !text-2xl">图片工作台</Typography.Title></div>
          <Typography.Text type="secondary">提交后在当前页面查看任务进度和生成结果。</Typography.Text>
        </div>
        <Button icon={<RefreshCw className="size-4" />} loading={isLoading} onClick={() => void loadTasks(true)}>刷新任务</Button>
      </section>

      <section className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(360px,0.75fr)]">
        <Card className="!rounded-lg" title="创建图片">
          <div className="space-y-5">
            <div>
              <label className="mb-2 block text-sm font-medium text-slate-700">图片描述</label>
              <Input.TextArea value={prompt} onChange={(event) => setPrompt(event.target.value)} placeholder="描述你想生成或修改的图片" autoSize={{ minRows: 6, maxRows: 12 }} maxLength={4000} showCount />
            </div>
            <div className="grid gap-3 sm:grid-cols-3">
              <label className="text-sm font-medium text-slate-700">模型<Select className="mt-1.5 w-full" value={model} options={MODEL_OPTIONS.map((value) => ({ value, label: value }))} onChange={setModel} /></label>
              <label className="text-sm font-medium text-slate-700">尺寸<Select className="mt-1.5 w-full" value={size} options={SIZE_OPTIONS.map((value) => ({ value, label: value }))} onChange={setSize} /></label>
              <label className="text-sm font-medium text-slate-700">质量<Select className="mt-1.5 w-full" value={quality} options={QUALITY_OPTIONS.map((value) => ({ value, label: value }))} onChange={setQuality} /></label>
            </div>
            <div>
              <div className="mb-2 flex items-center justify-between gap-3"><label className="text-sm font-medium text-slate-700">参考图</label><span className="text-xs text-slate-400">可选，添加后将以图生图方式处理</span></div>
              <input ref={fileInput} className="hidden" type="file" accept="image/*" multiple onChange={(event) => addReferences(event.target.files)} />
              <div className="flex flex-wrap gap-2">
                {references.map((file, index) => (
                  <Tag key={`${file.name}-${file.lastModified}-${index}`} closable onClose={() => setReferences((current) => current.filter((_, itemIndex) => itemIndex !== index))} icon={<FileImage className="size-3.5" />} className="!m-0 !flex h-8 items-center !rounded-md !px-2">{file.name}</Tag>
                ))}
                {references.length < MAX_REFERENCE_FILES ? <Button icon={<ImagePlus className="size-4" />} onClick={() => fileInput.current?.click()}>添加参考图</Button> : null}
              </div>
            </div>
            <div className="flex justify-end border-t border-slate-100 pt-4">
              <Button type="primary" size="large" icon={isSubmitting ? <LoaderCircle className="size-4 animate-spin" /> : <Sparkles className="size-4" />} loading={isSubmitting} onClick={() => void submit()}>
                {references.length > 0 ? "开始参考图生成" : "开始生成"}
              </Button>
            </div>
          </div>
        </Card>

        <Card className="!rounded-lg" title="任务状态">
          {activeTasks.length === 0 && failedTasks.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="当前没有处理中的任务" /> : (
            <div className="space-y-3">
              {activeTasks.map((task) => <TaskCard key={task.id} task={task} onShowStatus={() => openTaskStatus(task)} />)}
              {failedTasks.length > 0 ? <div className="pt-1 text-xs font-medium text-slate-500">最近失败</div> : null}
              {failedTasks.map((task) => <TaskCard key={task.id} task={task} onShowStatus={() => openTaskStatus(task)} />)}
            </div>
          )}
        </Card>
      </section>

      <Card className="!rounded-lg" title="生成结果" extra={<span className="text-sm text-slate-400">{completedTasks.length} 个已完成任务</span>}>
        {completedTasks.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="完成的图片会显示在这里" /> : (
          <Image.PreviewGroup>
            <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
              {completedTasks.flatMap((task) => (task.data || []).map((item, index) => {
                const src = imageSource(item);
                if (!src) return null;
                return <div key={`${task.id}-${index}`} className="overflow-hidden rounded-lg border border-slate-200 bg-slate-50"><Image src={src} alt={task.prompt || "生成图片"} className="block aspect-square !w-full object-cover" preview /><div className="flex items-center justify-between gap-2 border-t border-slate-200 bg-white px-3 py-2"><span className="truncate text-xs text-slate-500">{task.model || "gpt-image-2"}</span><Tooltip title="查看处理日志"><Button type="text" size="small" icon={<Clock3 className="size-4" />} onClick={() => openTaskStatus(task)} /></Tooltip></div></div>;
              }))}
            </div>
          </Image.PreviewGroup>
        )}
      </Card>

      <TaskStatusModal task={statusTask} onClose={() => setStatusTask(null)} />
    </div>
  );
}

function TaskCard({ task, onShowStatus }: { task: ImageTask; onShowStatus: () => void }) {
  const percent = typeof task.progress_percent === "number" ? task.progress_percent : task.status === "success" ? 100 : 0;
  const icon = task.status === "error" ? <CircleAlert className="size-4 text-rose-500" /> : task.status === "success" ? <CheckCircle2 className="size-4 text-emerald-500" /> : <LoaderCircle className="size-4 animate-spin text-sky-500" />;
  return (
    <div className="rounded-lg border border-slate-200 bg-slate-50 px-3 py-3">
      <div className="flex items-start justify-between gap-3"><div className="flex min-w-0 items-center gap-2">{icon}<span className="truncate text-sm font-medium text-slate-800">{task.prompt || "图片任务"}</span></div><Tag color={statusTone(task.status)} className="!m-0 shrink-0">{statusLabel(task.status)}</Tag></div>
      <Progress className="!mt-3" percent={percent} size="small" status={task.status === "error" ? "exception" : "active"} />
      <div className="mt-2 flex items-center justify-between gap-3"><span className="truncate text-xs text-slate-500">{task.realtime_status || task.progress || "等待上游响应"}</span><Button type="link" size="small" className="!px-0" onClick={onShowStatus}>日志</Button></div>
    </div>
  );
}

export default function ImagePage() {
  const { isCheckingAuth, session } = useAuthGuard(["user"]);
  if (isCheckingAuth || !session || session.role !== "user") {
    return <div className="flex min-h-[40vh] items-center justify-center"><LoaderCircle className="size-5 animate-spin text-slate-400" /></div>;
  }
  return <ImageWorkspace />;
}
