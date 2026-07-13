"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { toast } from "sonner";

import webConfig from "@/constants/common-env";
import { fetchSystemUpdateStatus, startSystemUpdate, type SystemUpdateStatus } from "@/lib/api";
import {
  githubLatestReleaseURL,
  parseChangelog,
  releaseChangelogURL,
  releaseVersionResolverURL,
  type ReleaseInfo,
} from "@/lib/release";

function readLocalReleases(): ReleaseInfo[] {
  return JSON.parse(process.env.NEXT_PUBLIC_APP_RELEASES || "[]");
}

function toVersionParts(version: string) {
  const match = version.trim().match(/^v?(\d+)\.(\d+)\.(\d+)/);
  return match ? match.slice(1).map(Number) : null;
}

function isNewerVersion(latestVersion: string, currentVersion: string) {
  const latest = toVersionParts(latestVersion);
  const current = toVersionParts(currentVersion);
  if (!latest || !current) return false;
  return latest.some(
    (value, index) =>
      value > current[index] &&
      latest.slice(0, index).every((part, prevIndex) => part === current[prevIndex]),
  );
}

export function useVersionCheck(canUpdate = false) {
  const currentVersion = webConfig.appVersion;
  const localReleases = useMemo(readLocalReleases, []);
  const [latestVersion, setLatestVersion] = useState(currentVersion);
  const [releases, setReleases] = useState<ReleaseInfo[]>(localReleases);
  const [checking, setChecking] = useState(false);
  const [open, setOpen] = useState(false);
  const [updateStatus, setUpdateStatus] = useState<SystemUpdateStatus | null>(null);
  const [startingUpdate, setStartingUpdate] = useState(false);
  const hasNewVersion = isNewerVersion(latestVersion, currentVersion);

  const checkLatestRelease = useCallback(
    async (showMessage = false) => {
      setChecking(true);
      try {
        const version = await fetchLatestPublishedVersion();
        if (!version) {
          setLatestVersion(currentVersion);
          setReleases(localReleases);
          if (showMessage) toast.info("暂无已完成发布的版本");
          return;
        }
        setLatestVersion(version);
        try {
          const changelogResponse = await fetch(releaseChangelogURL(version), { cache: "no-store" });
          if (changelogResponse.ok) {
            const changelog = await changelogResponse.text();
            if (changelog.trim()) setReleases(parseChangelog(changelog));
          }
        } catch {
          // Release metadata remains sufficient to notify about an upgrade.
        }
        if (showMessage) toast.success("已获取最新版本信息");
      } catch {
        setLatestVersion(currentVersion);
        setReleases(localReleases);
        if (showMessage) toast.error("获取最新版本信息失败");
      } finally {
        setChecking(false);
      }
    },
    [currentVersion, localReleases],
  );

  const openReleaseModal = () => {
    setOpen(true);
    void checkLatestRelease();
  };

  const refreshUpdateStatus = useCallback(async () => {
    if (!canUpdate) {
      setUpdateStatus(null);
      return;
    }
    try {
      setUpdateStatus(await fetchSystemUpdateStatus());
    } catch {
      setUpdateStatus(null);
    }
  }, [canUpdate]);

  const startUpdate = useCallback(async () => {
    if (!canUpdate || !hasNewVersion || startingUpdate) {
      return;
    }
    setStartingUpdate(true);
    try {
      const result = await startSystemUpdate(latestVersion);
      setUpdateStatus(result.update);
      toast.success(result.started ? "已开始拉取镜像并重启服务" : "升级任务正在执行中");
      window.setTimeout(() => window.location.reload(), 8000);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "启动升级失败");
      setStartingUpdate(false);
      void refreshUpdateStatus();
    }
  }, [canUpdate, hasNewVersion, latestVersion, refreshUpdateStatus, startingUpdate]);

  useEffect(() => {
    void checkLatestRelease();
    void refreshUpdateStatus();
  }, [checkLatestRelease, refreshUpdateStatus]);

  return {
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
    refreshUpdateStatus,
  };
}

async function fetchLatestPublishedVersion() {
  const lookups = await Promise.allSettled([
    fetchResolvedReleaseVersion(),
    fetchGitHubReleaseVersion(),
  ]);
  const versions = lookups
    .filter((result): result is PromiseFulfilledResult<string> => result.status === "fulfilled")
    .map((result) => result.value)
    .filter((version) => Boolean(toVersionParts(version)));
  if (!versions.length) {
    throw new Error("all release lookups failed");
  }
  return versions.reduce((latest, version) => (isNewerVersion(version, latest) ? version : latest));
}

async function fetchResolvedReleaseVersion() {
  const resolverURL = `${releaseVersionResolverURL}&_t=${Date.now()}`;
  const response = await fetch(resolverURL, { cache: "no-store" });
  if (!response.ok) throw new Error(`jsDelivr release lookup failed: HTTP ${response.status}`);
  const payload = await (response.json() as Promise<{ version?: string }>);
  return String(payload.version || "").trim().replace(/^v/, "");
}

async function fetchGitHubReleaseVersion() {
  const response = await fetch(`${githubLatestReleaseURL}?_t=${Date.now()}`, { cache: "no-store" });
  if (response.status === 404) return "";
  if (!response.ok) throw new Error(`GitHub release lookup failed: HTTP ${response.status}`);
  const release = await (response.json() as Promise<{ tag_name?: string }>);
  return String(release.tag_name || "").trim().replace(/^v/, "");
}
