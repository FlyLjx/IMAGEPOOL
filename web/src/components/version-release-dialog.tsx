"use client";

import { ArrowRight, Check, CircleDotDashed, Download, ExternalLink, GitCommitHorizontal, RefreshCw, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import webConfig from "@/constants/common-env";
import { useVersionCheck } from "@/hooks/use-version-check";
import { githubRepositoryURL } from "@/lib/release";
import { cn } from "@/lib/utils";

function releaseTypeClass(type: string) {
  if (type === "新增") return "bg-emerald-50 text-emerald-700 ring-emerald-200";
  if (type === "修复") return "bg-rose-50 text-rose-700 ring-rose-200";
  if (type === "调整") return "bg-blue-50 text-blue-700 ring-blue-200";
  if (type === "文档") return "bg-slate-100 text-slate-600 ring-slate-200";
  return "bg-slate-100 text-slate-600 ring-slate-200";
}

export function VersionReleaseDialog({ className, canUpdate = false }: { className?: string; canUpdate?: boolean }) {
  const {
    open,
    setOpen,
    openReleaseModal,
    latestVersion,
    releases,
    checking,
    hasNewVersion,
    checkLatestRelease,
    updateStatus,
    startingUpdate,
    startUpdate,
  } = useVersionCheck(canUpdate);
  const updateInProgress = startingUpdate || Boolean(updateStatus?.updating);
  const releaseState = hasNewVersion ? "available" : checking ? "checking" : "current";
  const statusText = updateStatus?.last_error
    ? updateStatus.last_error
    : releaseState === "available"
      ? "发现可用更新"
      : releaseState === "checking"
        ? "正在同步发布信息"
        : "已是最新版本";

  return (
    <>
      <button
        type="button"
        className={cn(
          "relative px-1 py-1 text-[11px] font-medium text-stone-500 transition hover:text-stone-900 dark:text-stone-300 dark:hover:text-white",
          className,
        )}
        onClick={openReleaseModal}
        title="查看版本更新"
      >
        v{webConfig.appVersion}
        {hasNewVersion ? (
          <span className="absolute -top-1 -right-1 size-2 rounded-full bg-emerald-500" />
        ) : null}
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent
          showCloseButton={false}
          className="w-[min(calc(100vw-2rem),760px)] max-h-[calc(100vh-2rem)] gap-0 overflow-hidden rounded-lg border-slate-200 bg-white p-0 shadow-[0_24px_72px_-28px_rgba(15,23,42,0.42)]"
        >
          <DialogHeader className="flex-row items-center justify-between gap-4 border-b border-slate-200 px-5 py-4 sm:px-6">
            <div className="flex min-w-0 items-center gap-3">
              <span className="flex size-9 shrink-0 items-center justify-center rounded-md bg-blue-600 text-white shadow-sm">
                <GitCommitHorizontal className="size-5" />
              </span>
              <div className="min-w-0">
                <DialogTitle className="text-base font-semibold text-slate-950">版本更新</DialogTitle>
                <div className="mt-0.5 flex items-center gap-1.5 text-xs text-slate-500">
                  <CircleDotDashed className={cn("size-3.5", releaseState === "available" ? "text-blue-600" : "text-emerald-600")} />
                  <span className="truncate">{statusText}</span>
                </div>
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-1.5">
              <Button
                type="button"
                variant="outline"
                size="icon"
                title="检查更新"
                aria-label="检查更新"
                disabled={checking}
                onClick={() => void checkLatestRelease(true)}
              >
                <RefreshCw className={cn("size-4", checking && "animate-spin")} />
              </Button>
              <DialogClose className="flex size-9 items-center justify-center rounded-md border border-slate-200 text-slate-500 transition-colors hover:bg-slate-50 hover:text-slate-900">
                <X className="size-4" />
                <span className="sr-only">关闭</span>
              </DialogClose>
            </div>
          </DialogHeader>
          <div className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center border-b border-slate-200 bg-slate-50/80 px-5 py-4 sm:px-6">
            <VersionMetric label="当前版本" value={webConfig.appVersion} tone="muted" />
            <ArrowRight className={cn("mx-3 size-4", hasNewVersion ? "text-blue-500" : "text-slate-300")} />
            <VersionMetric label="已发布版本" value={latestVersion} tone={hasNewVersion ? "active" : "muted"} />
          </div>
          <section className="min-h-0 flex-1 overflow-hidden">
            <div className="flex items-center justify-between border-b border-slate-100 px-5 py-3 sm:px-6">
              <span className="text-xs font-semibold text-slate-700">发布记录</span>
              <span className="font-mono text-[11px] text-slate-400">{releases.length} RELEASES</span>
            </div>
            <div className="max-h-[min(48vh,440px)] overflow-y-auto">
              {releases.map((release) => {
                const isLatest = release.version === latestVersion;
                const isCurrent = release.version === webConfig.appVersion;
                return (
                  <article key={release.version} className="border-b border-slate-100 px-5 py-4 last:border-b-0 sm:px-6">
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                      <div className="flex items-center gap-2">
                        <span className={cn("size-2 rounded-full", isLatest ? "bg-blue-500" : isCurrent ? "bg-emerald-500" : "bg-slate-300")} />
                        <span className="font-mono text-sm font-semibold text-slate-900">
                          {release.version === "Unreleased" ? "未发布" : `v${release.version}`}
                        </span>
                      </div>
                      {release.date ? <span className="font-mono text-[11px] text-slate-400">{release.date}</span> : null}
                      {isLatest ? <span className="rounded-sm bg-blue-50 px-1.5 py-0.5 text-[10px] font-medium text-blue-700">最新</span> : null}
                      {isCurrent ? <span className="rounded-sm bg-emerald-50 px-1.5 py-0.5 text-[10px] font-medium text-emerald-700">当前</span> : null}
                    </div>
                    <ul className="mt-3 space-y-2">
                      {release.items.map((item, index) => (
                        <li key={index} className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-2.5 text-sm leading-5 text-slate-600">
                          <span className={cn("mt-0.5 rounded-sm px-1.5 py-0.5 text-[10px] font-medium ring-1", releaseTypeClass(item.type))}>
                            {item.type}
                          </span>
                          <span className="min-w-0">{item.content}</span>
                        </li>
                      ))}
                    </ul>
                  </article>
                );
              })}
            </div>
          </section>
          <div className="flex flex-col-reverse gap-2 border-t border-slate-200 bg-white px-5 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-6">
            <Button variant="outline" size="sm" asChild>
              <a href={githubRepositoryURL} target="_blank" rel="noreferrer">
                <ExternalLink className="size-4" />
                GitHub
              </a>
            </Button>
            {canUpdate && hasNewVersion ? (
              <Button
                size="sm"
                className="bg-blue-600 hover:bg-blue-700"
                disabled={!updateStatus?.enabled || updateInProgress}
                onClick={() => void startUpdate()}
              >
                {updateInProgress ? <RefreshCw className="size-4 animate-spin" /> : <Download className="size-4" />}
                {updateInProgress ? "升级中" : "立即升级"}
              </Button>
            ) : (
              <span className="flex items-center gap-1.5 text-xs text-emerald-600">
                <Check className="size-3.5" />
                {hasNewVersion ? "可在 GitHub 获取更新" : "版本已同步"}
              </span>
            )}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}

function VersionMetric({
  label,
  value,
  tone,
}: {
  label: string;
  value: string;
  tone: "active" | "muted";
}) {
  return (
    <div className="min-w-0">
      <div className="text-[11px] font-medium text-slate-500">{label}</div>
      <div className={cn("mt-1 truncate font-mono text-base font-semibold", tone === "active" ? "text-blue-700" : "text-slate-900")}>
        v{value}
      </div>
    </div>
  );
}
