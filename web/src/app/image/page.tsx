"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Card, Empty, Image, Input, Select, Tag, Typography } from "antd";
import {
  FileImage,
  ImagePlus,
  LoaderCircle,
  Sparkles,
} from "lucide-react";
import { toast } from "sonner";

import { editImage, fetchModels, generateImage, type ImageResponse } from "@/lib/api";
import { useAuthGuard } from "@/lib/use-auth-guard";

const DEFAULT_MODEL_OPTIONS = ["gpt-image-2"];
const SIZE_OPTIONS = ["1024x1024", "1536x1024", "1024x1536"];
const QUALITY_OPTIONS = ["auto", "low", "medium", "high"];
const MAX_REFERENCE_FILES = 10;

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

function uniqueModelIDs(ids: string[]) {
  return Array.from(new Set(ids.map((item) => item.trim()).filter(Boolean)));
}

type WorkspaceResult = ImageResponse & { id: string; prompt: string; model: string };

function ImageWorkspace() {
  const [prompt, setPrompt] = useState("");
  const [model, setModel] = useState(DEFAULT_MODEL_OPTIONS[0]);
  const [modelOptions, setModelOptions] = useState(DEFAULT_MODEL_OPTIONS);
  const [isLoadingModels, setIsLoadingModels] = useState(false);
  const [size, setSize] = useState(SIZE_OPTIONS[0]);
  const [quality, setQuality] = useState("auto");
  const [references, setReferences] = useState<File[]>([]);
  const [results, setResults] = useState<WorkspaceResult[]>([]);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    let active = true;
    const loadModels = async () => {
      setIsLoadingModels(true);
      try {
        const result = await fetchModels();
        const ids = uniqueModelIDs((result.data || []).map((item) => item.id));
        if (active && ids.length > 0) {
          setModelOptions(ids);
          setModel((current) => (ids.includes(current) ? current : ids[0]));
        }
      } catch (error) {
        toast.error(error instanceof Error ? error.message : "加载模型列表失败");
      } finally {
        if (active) {
          setIsLoadingModels(false);
        }
      }
    };
    void loadModels();
    return () => {
      active = false;
    };
  }, []);

  const modelSelectOptions = useMemo(() => {
    return [{ label: "GPT号池模型", options: modelOptions.map((value) => ({ value, label: value })) }];
  }, [modelOptions]);

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
      const response = references.length > 0
        ? await editImage(references, cleanPrompt, model, size, quality, undefined, "b64_json")
        : await generateImage(cleanPrompt, model, size, quality, undefined, "b64_json");
      setResults((current) => [{ ...response, id: `${Date.now()}-${Math.random().toString(36).slice(2, 10)}`, prompt: cleanPrompt, model }, ...current]);
      toast.success("图片已生成");
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
          <Typography.Text type="secondary">图片生成完成后直接显示在当前页面。</Typography.Text>
        </div>
      </section>

      <section>
        <Card className="!rounded-lg" title="创建图片">
          <div className="space-y-5">
            <div>
              <label className="mb-2 block text-sm font-medium text-slate-700">图片描述</label>
              <Input.TextArea value={prompt} onChange={(event) => setPrompt(event.target.value)} placeholder="描述你想生成或修改的图片" autoSize={{ minRows: 6, maxRows: 12 }} maxLength={4000} showCount />
            </div>
            <div className="grid gap-3 sm:grid-cols-3">
              <label className="text-sm font-medium text-slate-700">模型<Select className="mt-1.5 w-full" value={model} options={modelSelectOptions} onChange={setModel} loading={isLoadingModels} /></label>
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

      </section>

      <Card className="!rounded-lg" title="生成结果" extra={<span className="text-sm text-slate-400">本次页面 {results.length} 个结果</span>}>
        {results.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="完成的图片会显示在这里" /> : (
          <Image.PreviewGroup>
            <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
              {results.flatMap((result) => (result.data || []).map((item, index) => {
                const src = imageSource(item);
                if (!src) return null;
                return <div key={`${result.id}-${index}`} className="overflow-hidden rounded-lg border border-slate-200 bg-slate-50"><Image src={src} alt={result.prompt || "生成图片"} className="block aspect-square !w-full object-cover" preview /><div className="border-t border-slate-200 bg-white px-3 py-2"><span className="block truncate text-xs text-slate-500">{result.model || "gpt-image-2"}</span></div></div>;
              }))}
            </div>
          </Image.PreviewGroup>
        )}
      </Card>
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
