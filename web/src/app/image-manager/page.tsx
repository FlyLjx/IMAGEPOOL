"use client";

import { useEffect, useMemo, useState } from "react";
import {
  Button,
  Card,
  Empty,
  Image,
  Input,
  InputNumber,
  Modal,
  Pagination,
  Select,
  Space,
  Spin,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import type { InputNumberProps } from "antd";
import {
  Archive,
  Download,
  HardDrive,
  Image as ImageIcon,
  LoaderCircle,
  RefreshCw,
  Search,
  Tags,
  Trash2,
  Wrench,
  ZoomIn,
} from "lucide-react";
import { toast } from "sonner";

import { ImageThumbnail } from "@/components/image-thumbnail";
import {
  compressAllImages,
  deleteImageTag,
  deleteManagedImages,
  deleteToTarget,
  downloadImages,
  downloadSingleImage,
  fetchImageStorage,
  fetchImageTags,
  fetchManagedImages,
  setImageTags,
  type ImageStorageStats,
  type ManagedImage,
} from "@/lib/api";
import { formatShanghaiDateTime } from "@/lib/datetime";
import { useAuthGuard } from "@/lib/use-auth-guard";
import { cn } from "@/lib/utils";

type TagEditorState = {
  open: boolean;
  image: ManagedImage | null;
  value: string;
};

type ImageDimensions = {
  width: number;
  height: number;
};

function formatBytes(size: number) {
  if (!Number.isFinite(size) || size <= 0) {
    return "0 B";
  }
  const units = ["B", "KB", "MB", "GB", "TB"];
  let next = size;
  let index = 0;
  while (next >= 1024 && index < units.length - 1) {
    next /= 1024;
    index += 1;
  }
  return `${next.toFixed(next >= 100 || index === 0 ? 0 : 1)} ${units[index]}`;
}

function formatMB(value: number | undefined) {
	const amount = Number(value);
	return `${Number.isFinite(amount) ? amount.toFixed(2) : "0.00"} MB`;
}

function imageKey(item: ManagedImage) {
  return item.path || item.rel || item.url || item.name;
}

function imageDimensionsLabel(item: ManagedImage, dimensions?: ImageDimensions) {
  const width = Number(item.width || dimensions?.width || 0);
  const height = Number(item.height || dimensions?.height || 0);
  return width > 0 && height > 0 ? `${width}x${height}` : "读取中";
}

function dateOptionsFromImages(images: ManagedImage[]) {
  return Array.from(new Set(images.map((item) => item.date).filter(Boolean))).sort((a, b) => b.localeCompare(a));
}

function normalizeTagInput(value: string) {
  return Array.from(
    new Set(
      value
        .split(/[\n,，]+/)
        .map((item) => item.trim())
        .filter(Boolean),
    ),
  );
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
  icon: typeof ImageIcon;
  tone?: "blue" | "green" | "amber" | "rose";
}) {
  const toneClass = {
    blue: "bg-blue-50 text-blue-600",
    green: "bg-emerald-50 text-emerald-600",
    amber: "bg-amber-50 text-amber-600",
    rose: "bg-fuchsia-50 text-fuchsia-600",
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

function ImageManagerContent() {
  const [images, setImages] = useState<ManagedImage[]>([]);
  const [storage, setStorage] = useState<ImageStorageStats | null>(null);
  const [knownTags, setKnownTags] = useState<string[]>([]);
  const [imageDimensions, setImageDimensions] = useState<Record<string, ImageDimensions>>({});
  const [selectedPaths, setSelectedPaths] = useState<string[]>([]);
  const [query, setQuery] = useState("");
  const [dateFilter, setDateFilter] = useState<string>("all");
  const [tagFilter, setTagFilter] = useState<string>("all");
  const [isLoading, setIsLoading] = useState(true);
  const [isMutating, setIsMutating] = useState(false);
  const [previewImage, setPreviewImage] = useState<ManagedImage | null>(null);
  const [tagEditor, setTagEditor] = useState<TagEditorState>({ open: false, image: null, value: "" });
  const [cleanupTargetMb, setCleanupTargetMb] = useState<number>(2048);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(12);

  const load = async (silent = false) => {
    if (!silent) {
      setIsLoading(true);
    }
    try {
      const [imageData, storageData, tagData] = await Promise.all([
        fetchManagedImages({}),
        fetchImageStorage(),
        fetchImageTags(),
      ]);
      setImages(imageData.items || []);
      setStorage(storageData);
      setKnownTags(tagData.tags || []);
      setSelectedPaths((current) =>
        current.filter((path) => (imageData.items || []).some((item) => item.path === path)),
      );
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载图片数据失败");
    } finally {
      if (!silent) {
        setIsLoading(false);
      }
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const dateOptions = useMemo(() => dateOptionsFromImages(images), [images]);

  const filteredImages = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return images.filter((item) => {
      const tags = item.tags || [];
      const matchesDate = dateFilter === "all" || item.date === dateFilter;
      const matchesTag = tagFilter === "all" || tags.includes(tagFilter);
      const searchField = [item.name, item.path, item.rel, ...tags].join(" ").toLowerCase();
      const matchesQuery = normalizedQuery.length === 0 || searchField.includes(normalizedQuery);
      return matchesDate && matchesTag && matchesQuery;
    });
  }, [dateFilter, images, query, tagFilter]);

  const selectedImages = useMemo(() => {
    const selected = new Set(selectedPaths);
    return images.filter((item) => item.path && selected.has(item.path));
  }, [images, selectedPaths]);

  const totalPages = Math.max(1, Math.ceil(filteredImages.length / pageSize));
  const safePage = Math.min(page, totalPages);
  const pagedImages = useMemo(() => {
    const startIndex = (safePage - 1) * pageSize;
    return filteredImages.slice(startIndex, startIndex + pageSize);
  }, [filteredImages, pageSize, safePage]);

  const allVisibleSelected =
    pagedImages.length > 0 &&
    pagedImages.every((item) => item.path && selectedPaths.includes(item.path));

  useEffect(() => {
    setPage(1);
  }, [dateFilter, query, tagFilter]);

  useEffect(() => {
    if (page > totalPages) {
      setPage(totalPages);
    }
  }, [page, totalPages]);

  const usagePercent = useMemo(() => {
    if (!storage || storage.disk_total_mb <= 0) {
      return 0;
    }
    return Math.min(100, Math.round((storage.image_size_mb / storage.disk_total_mb) * 100));
  }, [storage]);

  const refreshAfterMutation = async (message?: string) => {
    await load(true);
    if (message) {
      toast.success(message);
    }
  };

  const handleDelete = async (paths: string[]) => {
    if (paths.length === 0) {
      toast.error("请先选择要删除的图片");
      return;
    }
    Modal.confirm({
      title: "删除图片",
      content: `确认删除 ${paths.length} 张图片吗？这个操作会同时移除缩略图和标签。`,
      okText: "确认删除",
      cancelText: "取消",
      okButtonProps: { danger: true },
      onOk: async () => {
        setIsMutating(true);
        try {
          const result = await deleteManagedImages({ paths });
          setSelectedPaths((current) => current.filter((path) => !paths.includes(path)));
          await refreshAfterMutation(`已删除 ${result.removed} 张图片`);
        } catch (error) {
          toast.error(error instanceof Error ? error.message : "删除图片失败");
        } finally {
          setIsMutating(false);
        }
      },
    });
  };

  const handleBatchDownload = async () => {
    const paths = selectedImages.map((item) => item.path).filter((value): value is string => Boolean(value));
    if (paths.length === 0) {
      toast.error("请先选择要下载的图片");
      return;
    }
    setIsMutating(true);
    try {
      await downloadImages(paths);
      toast.success(`开始下载 ${paths.length} 张图片`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "下载图片失败");
    } finally {
      setIsMutating(false);
    }
  };

  const handleCompress = async () => {
    setIsMutating(true);
    try {
      const result = await compressAllImages();
      await refreshAfterMutation(`已压缩 ${result.compressed} 张图片，节省 ${result.saved_mb} MB`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "压缩图片失败");
    } finally {
      setIsMutating(false);
    }
  };

  const handleCleanup = async () => {
    const target = Number(cleanupTargetMb);
    if (!Number.isFinite(target) || target <= 0) {
      toast.error("请输入有效的磁盘剩余目标");
      return;
    }
    setIsMutating(true);
    try {
      const result = await deleteToTarget(target);
      await refreshAfterMutation(`清理完成，删除 ${result.removed} 张图片，释放 ${result.freed_mb} MB`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "磁盘清理失败");
    } finally {
      setIsMutating(false);
    }
  };

  const openTagEditor = (image: ManagedImage) => {
    setTagEditor({
      open: true,
      image,
      value: (image.tags || []).join(", "),
    });
  };

  const handleSaveTags = async () => {
    if (!tagEditor.image?.path) {
      setTagEditor({ open: false, image: null, value: "" });
      return;
    }
    setIsMutating(true);
    try {
      const tags = normalizeTagInput(tagEditor.value);
      await setImageTags(tagEditor.image.path, tags);
      setTagEditor({ open: false, image: null, value: "" });
      await refreshAfterMutation("图片标签已更新");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存标签失败");
    } finally {
      setIsMutating(false);
    }
  };

  const handleDeleteTag = async (tag: string) => {
    Modal.confirm({
      title: "删除标签",
      content: `确认删除标签 “${tag}” 吗？会从所有图片中移除。`,
      okText: "确认删除",
      cancelText: "取消",
      okButtonProps: { danger: true },
      onOk: async () => {
        setIsMutating(true);
        try {
          const result = await deleteImageTag(tag);
          if (tagFilter === tag) {
            setTagFilter("all");
          }
          await refreshAfterMutation(`标签已移除，影响 ${result.removed_from} 张图片`);
        } catch (error) {
          toast.error(error instanceof Error ? error.message : "删除标签失败");
        } finally {
          setIsMutating(false);
        }
      },
    });
  };

  return (
    <div className="dashboard-console">
      <section className="flex flex-col gap-4 rounded-lg border border-slate-200 bg-white px-5 py-5 shadow-sm lg:flex-row lg:items-center lg:justify-between">
        <div>
          <Typography.Text type="secondary" className="text-xs font-semibold uppercase tracking-[0.18em]">
            Image Manager
          </Typography.Text>
          <Typography.Title level={2} className="!mb-1 !mt-1 !text-2xl">
            图片管理
          </Typography.Title>
          <Typography.Text type="secondary">
            查看本地图片、批量下载删除、管理标签，并做存储压缩与清理。
          </Typography.Text>
        </div>
        <Space wrap>
          <Button
            icon={isLoading ? <LoaderCircle className="size-4 animate-spin" /> : <RefreshCw className="size-4" />}
            onClick={() => void load()}
            disabled={isMutating}
          >
            刷新
          </Button>
          <Button
            icon={<Download className="size-4" />}
            onClick={() => void handleBatchDownload()}
            disabled={selectedImages.length === 0 || isMutating}
          >
            下载所选
          </Button>
          <Button
            danger
            icon={<Trash2 className="size-4" />}
            onClick={() =>
              void handleDelete(
                selectedImages.map((item) => item.path).filter((value): value is string => Boolean(value)),
              )
            }
            disabled={selectedImages.length === 0 || isMutating}
          >
            删除所选
          </Button>
        </Space>
      </section>

      <section className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
        <MetricCard
          title="图片总数"
          value={storage?.image_count ?? 0}
          helper={`筛选结果 ${filteredImages.length} 张`}
          icon={ImageIcon}
          tone="blue"
        />
        <MetricCard
          title="图片占用"
          value={formatBytes(storage?.image_size_bytes ?? 0)}
          helper={`占磁盘约 ${usagePercent}%`}
          icon={HardDrive}
          tone="green"
        />
        <MetricCard
          title="磁盘剩余"
          value={formatMB(storage?.disk_free_mb)}
          helper={`总容量 ${formatMB(storage?.disk_total_mb)}`}
          icon={Archive}
          tone="amber"
        />
        <MetricCard
          title="标签数量"
          value={knownTags.length}
          helper="支持按标签筛选和清理"
          icon={Tags}
          tone="rose"
        />
      </section>

      <section className="grid gap-4 xl:grid-cols-[1fr_340px]">
        <Card
          title={
            <Space>
              <span>图片列表</span>
              <Tag color="blue" className="m-0">{filteredImages.length}</Tag>
            </Space>
          }
          extra={
            <div className="flex flex-col gap-2 lg:flex-row">
              <Input
                allowClear
                prefix={<Search className="size-4 text-slate-400" />}
                value={query}
                onChange={(event) => setQuery(event.target.value)}
                placeholder="搜索文件名、路径、标签"
                style={{ width: 250 }}
              />
              <Select
                value={dateFilter}
                onChange={setDateFilter}
                style={{ width: 150 }}
                options={[
                  { label: "全部日期", value: "all" },
                  ...dateOptions.map((item) => ({ label: item, value: item })),
                ]}
              />
              <Select
                value={tagFilter}
                onChange={setTagFilter}
                style={{ width: 150 }}
                options={[
                  { label: "全部标签", value: "all" },
                  ...knownTags.map((item) => ({ label: item, value: item })),
                ]}
              />
            </div>
          }
        >
          <div className="mb-4 flex flex-col gap-3 rounded-lg border border-slate-100 bg-slate-50/80 px-4 py-3 lg:flex-row lg:items-center lg:justify-between">
            <Space wrap>
              <Button
                size="small"
                onClick={() => {
                  if (allVisibleSelected) {
                    setSelectedPaths((current) =>
                      current.filter((path) => !pagedImages.some((item) => item.path === path)),
                    );
                    return;
                  }
                  const merged = new Set(selectedPaths);
                  pagedImages.forEach((item) => {
                    if (item.path) {
                      merged.add(item.path);
                    }
                  });
                  setSelectedPaths(Array.from(merged));
                }}
                disabled={pagedImages.length === 0}
              >
                {allVisibleSelected ? "取消全选当前页" : "全选当前页"}
              </Button>
              {selectedImages.length > 0 ? (
                <Tag color="processing" className="m-0">
                  已选择 {selectedImages.length} 张
                </Tag>
              ) : (
                <span className="text-sm text-slate-400">当前未选择图片</span>
              )}
            </Space>
            <div className="text-sm text-slate-500">当前页 {pagedImages.length} 张</div>
          </div>

          {isLoading ? (
            <div className="flex min-h-[360px] items-center justify-center">
              <Spin indicator={<LoaderCircle className="size-5 animate-spin text-stone-400" />} />
            </div>
          ) : filteredImages.length === 0 ? (
            <div className="py-16">
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="当前没有图片" />
            </div>
          ) : (
            <>
              <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
                {pagedImages.map((item) => {
                  const key = imageKey(item);
                  const selected = Boolean(item.path && selectedPaths.includes(item.path));
                  const dimensionsText = imageDimensionsLabel(item, imageDimensions[key]);
                  return (
                    <article
                      key={item.path || item.rel}
                      className={cn(
                        "overflow-hidden rounded-lg border bg-white transition",
                        selected
                          ? "border-blue-300 shadow-sm"
                          : "border-slate-200 hover:border-slate-300",
                      )}
                    >
                      <div className="relative">
                          <button
                          type="button"
                          className={cn(
                            "absolute left-3 top-3 z-10 rounded-full px-3 py-1 text-xs font-medium shadow-sm ring-1 ring-inset",
                            selected
                              ? "bg-blue-600 text-white ring-blue-600"
                              : "bg-white/95 text-slate-700 ring-slate-200",
                          )}
                          onClick={() => {
                            if (!item.path) {
                              return;
                            }
                            setSelectedPaths((current) =>
                              current.includes(item.path!)
                                ? current.filter((path) => path !== item.path)
                                : [...current, item.path!],
                            );
                          }}
                        >
                          {selected ? "已选中" : "选择"}
                        </button>
                        <button
                          type="button"
                          className="absolute right-3 top-3 z-10 inline-flex h-8 w-8 items-center justify-center rounded-full bg-slate-900/70 text-white"
                          onClick={() => setPreviewImage(item)}
                        >
                          <ZoomIn className="size-4" />
                        </button>
                        <ImageThumbnail
                          src={item.url}
                          thumbnailSrc={item.thumbnail_url}
                          alt={item.name}
                          className="block aspect-[4/3] bg-slate-100"
                          imageClassName="h-full w-full object-cover"
                          onLoadDimensions={(width, height) => {
                            setImageDimensions((current) => {
                              const previous = current[key];
                              if (previous?.width === width && previous?.height === height) {
                                return current;
                              }
                              return { ...current, [key]: { width, height } };
                            });
                          }}
                        />
                      </div>

                      <div className="space-y-3 p-4">
                        <div>
                          <div className="line-clamp-2 text-sm font-semibold text-slate-900">{item.name}</div>
                          <div className="mt-1 truncate font-mono text-[11px] text-slate-400" title={item.path || item.rel}>
                            {item.path || item.rel}
                          </div>
                        </div>

                        <div className="flex flex-wrap gap-2 text-xs text-slate-500">
                          <span>{formatBytes(item.size)}</span>
                          <span>{dimensionsText}</span>
                          <span>{item.date}</span>
                        </div>

                        <div className="flex min-h-6 flex-wrap gap-2">
                          {(item.tags || []).length > 0 ? (
                            (item.tags || []).map((tag) => (
                              <Tag key={`${item.path}-${tag}`} color="blue" className="m-0">
                                {tag}
                              </Tag>
                            ))
                          ) : (
                            <span className="text-xs text-slate-400">未设置标签</span>
                          )}
                        </div>

                        <div className="flex flex-wrap gap-2">
                          <Tooltip title="编辑标签">
                            <Button size="small" icon={<Tags className="size-3.5" />} onClick={() => openTagEditor(item)} />
                          </Tooltip>
                          <Tooltip title="下载原图">
                            <Button
                              size="small"
                              icon={<Download className="size-3.5" />}
                              onClick={() => {
                                if (!item.path) {
                                  return;
                                }
                                void downloadSingleImage(item.path).catch((error: unknown) => {
                                  toast.error(error instanceof Error ? error.message : "下载图片失败");
                                });
                              }}
                            />
                          </Tooltip>
                          <Tooltip title="预览">
                            <Button size="small" icon={<ZoomIn className="size-3.5" />} onClick={() => setPreviewImage(item)} />
                          </Tooltip>
                          <Tooltip title="删除">
                            <Button
                              size="small"
                              danger
                              icon={<Trash2 className="size-3.5" />}
                              onClick={() => {
                                if (!item.path) {
                                  return;
                                }
                                void handleDelete([item.path]);
                              }}
                            />
                          </Tooltip>
                        </div>
                      </div>
                    </article>
                  );
                })}
              </div>

              <div className="mt-5 flex flex-col gap-3 border-t border-slate-100 pt-4 lg:flex-row lg:items-center lg:justify-between">
                <div className="text-sm text-slate-500">
                  显示第 {filteredImages.length === 0 ? 0 : (safePage - 1) * pageSize + 1} -{" "}
                  {Math.min(safePage * pageSize, filteredImages.length)} 张，共 {filteredImages.length} 张
                </div>
                <Pagination
                  current={safePage}
                  pageSize={pageSize}
                  total={filteredImages.length}
                  showSizeChanger
                  pageSizeOptions={[12, 24, 48, 96]}
                  showTotal={(total) => `共 ${total} 张`}
                  onChange={(nextPage, nextPageSize) => {
                    setPage(nextPage);
                    setPageSize(nextPageSize);
                  }}
                />
              </div>
            </>
          )}
        </Card>

        <div className="space-y-4">
          <Card
            title={
              <Space>
                <Wrench className="size-4 text-slate-500" />
                <span>存储维护</span>
              </Space>
            }
          >
            <div className="space-y-4">
              <div className="rounded-lg border border-slate-100 bg-slate-50/70 p-4">
                <div className="mb-3 text-sm font-medium text-slate-800">一键压缩图片</div>
                <div className="mb-4 text-sm text-slate-500">重新优化本地 PNG，减少磁盘占用。</div>
                <Button block onClick={() => void handleCompress()} disabled={isMutating}>
                  立即压缩
                </Button>
              </div>

              <div className="rounded-lg border border-slate-100 bg-slate-50/70 p-4">
                <div className="mb-3 text-sm font-medium text-slate-800">按目标剩余空间清理</div>
                <div className="mb-4 text-sm text-slate-500">从最旧图片开始删除，直到达到目标剩余空间。</div>
                <Space.Compact block>
                  <InputNumber<number>
                    min={1}
                    value={cleanupTargetMb}
                    onChange={(value: InputNumberProps<number>["value"]) => setCleanupTargetMb(Number(value || 0))}
                    addonAfter="MB"
                    style={{ width: "100%" }}
                  />
                  <Button onClick={() => void handleCleanup()} disabled={isMutating}>
                    清理
                  </Button>
                </Space.Compact>
              </div>
            </div>
          </Card>

          <Card
            title={
              <Space>
                <Tags className="size-4 text-slate-500" />
                <span>标签中心</span>
              </Space>
            }
          >
            <div className="flex flex-wrap gap-2">
              {knownTags.length > 0 ? (
                knownTags.map((tag) => (
                  <Tag
                    key={tag}
                    color={tagFilter === tag ? "processing" : "default"}
                    className="m-0 cursor-pointer"
                    onClick={() => setTagFilter((current) => (current === tag ? "all" : tag))}
                    closable
                    onClose={(event) => {
                      event.preventDefault();
                      void handleDeleteTag(tag);
                    }}
                  >
                    {tag}
                  </Tag>
                ))
              ) : (
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="当前还没有标签" />
              )}
            </div>
          </Card>
        </div>
      </section>

      <Modal
        title="编辑图片标签"
        open={tagEditor.open}
        onCancel={() => setTagEditor({ open: false, image: null, value: "" })}
        onOk={() => void handleSaveTags()}
        confirmLoading={isMutating}
        okText="保存"
        cancelText="取消"
      >
        <div className="space-y-3 pt-2">
          <div className="text-sm text-slate-500">使用逗号或换行分隔多个标签。</div>
          <Input.TextArea
            rows={5}
            value={tagEditor.value}
            onChange={(event) => setTagEditor((current) => ({ ...current, value: event.target.value }))}
            placeholder="例如：营销图, 头像, 客服素材"
          />
        </div>
      </Modal>

      <Modal
        title={previewImage?.name || "图片预览"}
        open={Boolean(previewImage)}
        footer={null}
        onCancel={() => setPreviewImage(null)}
        width={960}
      >
        {previewImage ? (
          <div className="space-y-4 pt-2">
            <div className="overflow-hidden rounded-lg border border-slate-100 bg-slate-50 p-2">
              <Image src={previewImage.url} alt={previewImage.name} className="w-full rounded object-contain" preview={false} />
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="rounded-lg bg-slate-50 p-4 text-sm text-slate-600">
                <div className="font-medium text-slate-900">路径</div>
                <div className="mt-1 break-all font-mono text-xs">{previewImage.path || previewImage.rel}</div>
              </div>
              <div className="rounded-lg bg-slate-50 p-4 text-sm text-slate-600">
                <div className="font-medium text-slate-900">创建时间</div>
                <div className="mt-1">{formatShanghaiDateTime(previewImage.created_at, "未知时间")}</div>
              </div>
            </div>
            <div className="flex flex-wrap gap-2">
              {(previewImage.tags || []).map((tag) => (
                <Tag key={`preview-${tag}`} color="blue" className="m-0">
                  {tag}
                </Tag>
              ))}
              {(previewImage.tags || []).length === 0 ? <span className="text-sm text-slate-400">未设置标签</span> : null}
            </div>
          </div>
        ) : null}
      </Modal>
    </div>
  );
}

export default function ImageManagerPage() {
  const { isCheckingAuth, session } = useAuthGuard(["admin"]);

  if (isCheckingAuth || !session || session.role !== "admin") {
    return (
      <div className="flex min-h-[40vh] items-center justify-center">
        <LoaderCircle className="size-5 animate-spin text-stone-400" />
      </div>
    );
  }

  return <ImageManagerContent />;
}
