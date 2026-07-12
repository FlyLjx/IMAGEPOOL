"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button as AntButton,
  Card as AntCard,
  Checkbox as AntCheckbox,
  Col,
  Empty,
  Grid,
  Input as AntInput,
  Modal,
  Pagination as AntPagination,
  Progress as AntProgress,
  Row,
  Select as AntSelect,
  Space,
  Spin,
  Steps,
  Table as AntTable,
  Tag,
  Tooltip,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import {
  Ban,
  CheckCircle2,
  CircleAlert,
  CircleOff,
  Download,
  History,
  Image as ImageIcon,
  LoaderCircle,
  LogIn,
  Pencil,
  RefreshCw,
  Search,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";

import {
  deleteAccounts,
  fetchAccounts,
  fetchCredentialRecoveryLogs,
  fetchModels,
  fetchRefreshProgress,
  fetchReLoginProgress,
  reLoginAccounts,
  refreshAccounts,
  testAccountImage,
  testProxy,
  updateAccount,
  type Account,
  type AccountStatus,
  type CredentialRecoveryLog,
  type Model,
  type RefreshProgressResponse,
} from "@/lib/api";
import { formatShanghaiDateTime, parseDateTime } from "@/lib/datetime";
import { useAuthGuard } from "@/lib/use-auth-guard";
import { cn } from "@/lib/utils";

import { AccountImportDialog } from "./components/account-import-dialog";

const accountStatusOptions: { label: string; value: AccountStatus | "all" }[] = [
  { label: "全部状态", value: "all" },
  { label: "正常", value: "正常" },
  { label: "限流", value: "限流" },
  { label: "异常", value: "异常" },
  { label: "失效", value: "失效" },
  { label: "恢复中", value: "恢复中" },
  { label: "禁用", value: "禁用" },
];

const statusMeta: Record<
  AccountStatus,
  {
    icon: typeof CheckCircle2;
    tagColor: string;
  }
> = {
  正常: { icon: CheckCircle2, tagColor: "success" },
  限流: { icon: CircleAlert, tagColor: "warning" },
  异常: { icon: CircleOff, tagColor: "error" },
  失效: { icon: CircleOff, tagColor: "error" },
  恢复中: { icon: LoaderCircle, tagColor: "processing" },
  禁用: { icon: Ban, tagColor: "default" },
};

function isAbnormalAccountStatus(status: string | null | undefined) {
  return status === "异常" || status === "失效" || status === "恢复中";
}

function accountStatusLabel(status: string | null | undefined) {
  switch (String(status || "").trim().toLowerCase()) {
    case "pending_validation":
    case "validating":
      return "验证中";
    default:
      return status || "未知";
  }
}

function credentialRecoveryEventLabel(event: string) {
  switch (event) {
    case "credential_invalid":
      return "标记失效";
    case "recovery_started":
      return "开始恢复";
    case "token_refreshed":
      return "Token 已刷新";
    case "refresh_token_invalidated":
      return "Refresh Token 已失效";
    case "password_relogin_started":
      return "开始密码重新登录";
    case "password_relogin_succeeded":
      return "密码重新登录成功";
    case "password_relogin_failed":
      return "密码重新登录失败";
    case "recovery_succeeded":
      return "恢复成功";
    case "recovery_failed":
      return "恢复失败";
    case "recovery_deleted":
      return "已自动删除";
    default:
      return event || "恢复事件";
  }
}

function credentialRecoveryLogColor(level: CredentialRecoveryLog["level"]) {
  if (level === "success") return "success";
  if (level === "error") return "error";
  if (level === "warning") return "warning";
  return "processing";
}

const metricCards = [
  { key: "active", label: "正常账号", color: "text-emerald-600", icon: CheckCircle2 },
  { key: "abnormal", label: "异常账号", color: "text-rose-500", icon: CircleOff },
  { key: "cooling", label: "调度冷却", color: "text-amber-600", icon: LoaderCircle },
  { key: "quota", label: "剩余额度", color: "text-blue-500", icon: RefreshCw },
] as const;

const accountGroupMeta = {
  plus: { label: "Plus 账号", description: "ChatGPT Plus 类型账号" },
  free: { label: "Free 账号", description: "免费类型账号" },
  other: { label: "其他账号", description: "非 Plus / Free 类型账号" },
} as const;

type AccountGroupKey = keyof typeof accountGroupMeta;

const accountGroupOrder: AccountGroupKey[] = ["plus", "free", "other"];

function isUnlimitedImageQuotaAccount(account: Account) {
  return account.type === "pro" || account.type === "prolite";
}

function imageQuotaUnknown(account: Account) {
  return Boolean(account.image_quota_unknown);
}

function formatCompact(value: number) {
  if (value >= 1000) {
    return `${(value / 1000).toFixed(1)}k`;
  }
  return String(value);
}

function quotaBarColor(value: number) {
  if (value >= 85) {
    return "#f5222d";
  }
  if (value >= 60) {
    return "#fa8c16";
  }
  return "#52c41a";
}

function numberFrom(value: unknown) {
  const numeric = typeof value === "number" ? value : Number(value);
  return Number.isFinite(numeric) ? numeric : null;
}

function firstNumber(source: Record<string, unknown> | null, keys: string[]) {
  if (!source) {
    return null;
  }
  for (const key of keys) {
    const value = numberFrom(source[key]);
    if (value !== null) {
      return value;
    }
  }
  return null;
}

function imageLimitProgress(account: Account) {
  return (account.limits_progress || []).find((item) => String(item.feature_name || "") === "image_gen") as
    | (Record<string, unknown> & { usage?: unknown })
    | undefined;
}

function quotaProgress(account: Account) {
  if (isUnlimitedImageQuotaAccount(account)) {
    return { label: "不限", used: null, total: null, percent: 100, title: "不限额度" };
  }
  if (imageQuotaUnknown(account)) {
    return { label: "未知", used: null, total: null, percent: 0, title: "额度未知" };
  }

  const remaining = Math.max(0, Number(account.quota || 0));
  const imageProgress = imageLimitProgress(account);
  const usage = account.usage && typeof account.usage === "object" ? account.usage as Record<string, unknown> : null;
  const imageUsage = imageProgress?.usage && typeof imageProgress.usage === "object"
    ? imageProgress.usage as Record<string, unknown>
    : null;
  const upstreamUsed =
    firstNumber(imageUsage, ["used", "count", "current", "consumed", "image_gen_used"]) ??
    firstNumber(usage, ["image_gen_used", "used", "count", "current", "consumed"]);
  const trackedTotal = numberFrom(account.image_quota_total);
  const total =
    trackedTotal ??
    firstNumber(imageUsage, ["total", "limit", "max", "quota", "capacity"]) ??
    firstNumber(imageProgress || null, ["total", "limit", "max", "quota", "capacity"]) ??
    firstNumber(usage, ["image_gen_total", "total", "limit", "max", "quota", "capacity"]);
  const derivedUsed = total !== null ? Math.max(0, total - remaining) : 0;
  const normalizedUsed = Math.max(0, Math.round(trackedTotal !== null ? derivedUsed : (upstreamUsed ?? derivedUsed)));
  const normalizedTotal = Math.max(remaining + normalizedUsed, Math.round(total ?? remaining + normalizedUsed));
  const percent = normalizedTotal > 0 ? Math.min(100, Math.round((normalizedUsed / normalizedTotal) * 100)) : 0;
  return {
    label: `已用 ${formatCompact(normalizedUsed)}/${formatCompact(normalizedTotal)}`,
    used: normalizedUsed,
    total: normalizedTotal,
    percent,
    title: `已使用 ${normalizedUsed} / 总数量 ${normalizedTotal}，剩余 ${remaining}`,
  };
}

function QuotaProgressCell({ account }: { account: Account }) {
  const progress = quotaProgress(account);
  if (progress.used === null || progress.total === null) {
    return (
      <span className="inline-flex rounded-md bg-stone-100 px-2 py-1 font-mono text-[11px] font-medium text-stone-700">
        {progress.label}
      </span>
    );
  }

  return (
    <div className="w-[150px] space-y-1" title={progress.title}>
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-[11px] font-semibold text-slate-700">{progress.label}</span>
        <span className="font-mono text-[10px] text-slate-400">{progress.percent}%</span>
      </div>
      <AntProgress
        percent={progress.percent}
        showInfo={false}
        size={[150, 5]}
        strokeColor={quotaBarColor(progress.percent)}
        railColor="#eef2f7"
      />
    </div>
  );
}

function formatShortRelative(value?: string | null) {
  if (!value) {
    return "";
  }
  const date = parseDateTime(value);
  if (!date) {
    return "";
  }
  const diffMs = date.getTime() - Date.now();
  const absMs = Math.abs(diffMs);
  const minutes = Math.ceil(absMs / (1000 * 60));
  if (minutes < 60) {
    return diffMs >= 0 ? `剩 ${minutes}m` : `${minutes}m前`;
  }
  const hours = Math.ceil(minutes / 60);
  if (hours < 24) {
    return diffMs >= 0 ? `剩 ${hours}h` : `${hours}h前`;
  }
  const days = Math.ceil(hours / 24);
  return diffMs >= 0 ? `剩 ${days}d` : `${days}d前`;
}

function errorTypeLabel(value?: string | null) {
  const key = String(value || "").trim();
  if (!key) {
    return "";
  }
  const labels: Record<string, string> = {
    cloudflare: "Cloudflare",
    rate_limited: "限流",
    token_invalid: "Token 无效",
    timeout: "超时",
    poll_timeout: "轮询超时",
    network: "网络",
    upstream: "上游",
    no_image: "无图",
    generic: "通用",
    content_policy: "内容策略",
  };
  return labels[key] || key;
}

type ReloginResult = NonNullable<RefreshProgressResponse["results"]>[number];

function isReloginVerificationRequired(item: ReloginResult) {
  return item.status === "需验证码" || item.error === "need_verification_code" || String(item.error || "").includes("验证码");
}

function reloginResultSummary(results: NonNullable<RefreshProgressResponse["results"]>) {
  const success = results.filter((item) => item.status === "成功").length;
  const disabled = results.filter((item) => item.status === "禁用").length;
  const skipped = results.filter((item) => item.status === "跳过").length;
  const verification = results.filter(isReloginVerificationRequired).length;
  const failed = results.filter(
    (item) => !isReloginVerificationRequired(item) && (item.status === "异常" || (Boolean(item.error) && item.status !== "跳过" && item.status !== "禁用")),
  ).length;
  return { success, disabled, skipped, verification, failed };
}

function reloginResultTagColor(item: ReloginResult) {
  if (item.status === "success") return "success";
  if (item.status === "removed") return "warning";
  if (item.status === "error") return "error";
  if (item.status === "成功") return "success";
  if (isReloginVerificationRequired(item)) return "warning";
  if (item.status === "跳过") return "default";
  if (item.status === "禁用" || item.status === "异常" || item.error) return "error";
  return statusMeta[(item.status as AccountStatus) || "正常"]?.tagColor || "default";
}

function reloginResultLabel(item: ReloginResult) {
  if (item.status === "success") return "刷新成功";
  if (item.status === "removed") return "已删除";
  if (item.status === "error") return "刷新失败";
  if (item.status === "成功") return "恢复成功";
  if (isReloginVerificationRequired(item)) return "需验证码";
  if (item.status === "异常") return "恢复失败";
  return item.status;
}

function formatProgressError(value?: string | null) {
  const message = String(value || "").replace(/\s+/g, " ").trim();
  if (!message) {
    return "";
  }
  if (message.includes("upstream bootstrap status=403") || message.includes("cf-chl")) {
    return "ChatGPT 上游被 Cloudflare 拦截（HTTP 403）";
  }
  return message.length > 160 ? `${message.slice(0, 160)}...` : message;
}

function healthColor(score?: number) {
  const value = typeof score === "number" ? score : 0;
  if (value >= 80) return "#10b981";
  if (value >= 60) return "#1677ff";
  if (value >= 40) return "#faad14";
  return "#f43f5e";
}

function formatQuotaSummary(accounts: Account[]) {
  const availableAccounts = accounts.filter((account) => account.status === "正常");
  if (availableAccounts.some(isUnlimitedImageQuotaAccount)) {
    return "∞";
  }
  if (availableAccounts.some(imageQuotaUnknown)) {
    return "未知";
  }
  return formatCompact(availableAccounts.reduce((sum, account) => sum + Math.max(0, account.quota), 0));
}

function downloadTokens(accounts: Account[]) {
  const content = `${accounts.map((account) => account.access_token).join("\n")}\n`;
  const blob = new Blob([content], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = `accounts-${Date.now()}.txt`;
  link.click();
  URL.revokeObjectURL(url);
}

function displayAccountType(account: Account) {
  return account.type || "Free";
}

function getAccountGroupKey(account: Account): AccountGroupKey {
  const type = String(account.type || "").trim().toLowerCase();
  if (type === "plus") {
    return "plus";
  }
  if (!type || type === "free") {
    return "free";
  }
  return "other";
}

function accountLastUsedTimestamp(account: Account) {
  const value = String(account.last_used_at || "").trim();
  if (!value) {
    return 0;
  }
  return parseDateTime(value)?.getTime() || 0;
}

function compareAccountsByUsage(a: Account, b: Account) {
  const lastUsedDiff = accountLastUsedTimestamp(b) - accountLastUsedTimestamp(a);
  if (lastUsedDiff !== 0) {
    return lastUsedDiff;
  }

  const groupDiff = accountGroupOrder.indexOf(getAccountGroupKey(a)) - accountGroupOrder.indexOf(getAccountGroupKey(b));
  if (groupDiff !== 0) {
    return groupDiff;
  }

  const typeDiff = displayAccountType(a).localeCompare(displayAccountType(b), "zh-CN");
  if (typeDiff !== 0) {
    return typeDiff;
  }

  return (a.email || a.access_token).localeCompare(b.email || b.access_token, "zh-CN");
}

function AccountsPageContent() {
  const didLoadRef = useRef(false);
  const screens = Grid.useBreakpoint();
  const isCompactTable = !screens.md;
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [availableModels, setAvailableModels] = useState<Model[]>([]);
  const [selectedIds, setSelectedIds] = useState<string[]>([]);
  const [query, setQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("all");
  const [statusFilter, setStatusFilter] = useState<AccountStatus | "all">("all");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState("10");
  const [editingAccount, setEditingAccount] = useState<Account | null>(null);
  const [editStatus, setEditStatus] = useState<AccountStatus>("正常");
  const [editEmail, setEditEmail] = useState("");
  const [editPassword, setEditPassword] = useState("");
  const [editProxy, setEditProxy] = useState("");
  const [isTestingProxy, setIsTestingProxy] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [isLoadingModels, setIsLoadingModels] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [refreshingTokens, setRefreshingTokens] = useState<Set<string>>(new Set());
  const [testingImageTokens, setTestingImageTokens] = useState<Set<string>>(new Set());
  const [isDeleting, setIsDeleting] = useState(false);
  const [isUpdating, setIsUpdating] = useState(false);
  const [isRelogining, setIsRelogining] = useState(false);
  const [progress, setProgress] = useState<{
    visible: boolean;
    current: number;
    total: number;
    message: string;
    email: string;
  }>({
    visible: false,
    current: 0,
    total: 0,
    message: "",
    email: "",
  });
  const progressRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const [refreshSummary, setRefreshSummary] = useState<Record<string, number | string> | null>(null);
  const [progressResults, setProgressResults] = useState<NonNullable<RefreshProgressResponse["results"]>>([]);
  const [credentialRecoveryLogs, setCredentialRecoveryLogs] = useState<CredentialRecoveryLog[]>([]);
  const [credentialRecoveryLogOpen, setCredentialRecoveryLogOpen] = useState(false);
  const [credentialRecoveryLogEmail, setCredentialRecoveryLogEmail] = useState("");
  const [isLoadingCredentialRecoveryLogs, setIsLoadingCredentialRecoveryLogs] = useState(false);

  const resetProgress = () => {
    if (progressRef.current) {
      clearInterval(progressRef.current);
      progressRef.current = null;
    }
    setProgress({ visible: false, current: 0, total: 0, message: "", email: "" });
    setProgressResults([]);
  };

  const animateProgressTo = (target: number) => {
    setProgress((prev) => {
      const safeTarget = Math.min(prev.total || target, Math.max(prev.current, target));
      if (safeTarget <= prev.current) {
        return prev;
      }
      if (progressRef.current) {
        clearInterval(progressRef.current);
      }
      progressRef.current = setInterval(() => {
        setProgress((current) => {
          const nextValue = Math.min(safeTarget, current.current + 1);
          if (nextValue >= safeTarget && progressRef.current) {
            clearInterval(progressRef.current);
            progressRef.current = null;
          }
          return { ...current, current: nextValue };
        });
      }, 80);
      return prev;
    });
  };

  const loadAccounts = async (silent = false) => {
    if (!silent) {
      setIsLoading(true);
    }
    try {
      const data = await fetchAccounts();
      setAccounts(data.items);
      setSelectedIds((prev) => prev.filter((id) => data.items.some((item) => item.access_token === id)));
    } catch (error) {
      const message = error instanceof Error ? error.message : "加载账户失败";
      toast.error(message);
    } finally {
      if (!silent) {
        setIsLoading(false);
      }
    }
  };

  const loadModels = async () => {
    setIsLoadingModels(true);
    try {
      const data = await fetchModels();
      setAvailableModels(Array.isArray(data.data) ? data.data : []);
    } catch (error) {
      const message = error instanceof Error ? error.message : "加载模型列表失败";
      toast.error(message);
    } finally {
      setIsLoadingModels(false);
    }
  };

  const loadCredentialRecoveryLogs = useCallback(async (silent = false) => {
    if (!silent) {
      setIsLoadingCredentialRecoveryLogs(true);
    }
    try {
      const data = await fetchCredentialRecoveryLogs({ email: credentialRecoveryLogEmail, limit: 200 });
      setCredentialRecoveryLogs(data.items);
    } catch (error) {
      if (!silent) {
        toast.error(error instanceof Error ? error.message : "加载凭证恢复日志失败");
      }
    } finally {
      if (!silent) {
        setIsLoadingCredentialRecoveryLogs(false);
      }
    }
  }, [credentialRecoveryLogEmail]);

  useEffect(() => {
    if (didLoadRef.current) {
      return;
    }
    didLoadRef.current = true;
    void Promise.allSettled([loadAccounts(), loadModels()]);

    // 清理进度条定时器
    return () => {
      if (progressRef.current) clearInterval(progressRef.current);
    };
  }, []);

  useEffect(() => {
    if (!credentialRecoveryLogOpen) {
      return;
    }
    void loadCredentialRecoveryLogs();
    const timer = window.setInterval(() => {
      void loadCredentialRecoveryLogs(true);
    }, 5000);
    return () => window.clearInterval(timer);
  }, [credentialRecoveryLogOpen, loadCredentialRecoveryLogs]);

  const filteredAccounts = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return accounts.filter((account) => {
      const searchMatched =
        normalizedQuery.length === 0 || (account.email ?? "").toLowerCase().includes(normalizedQuery);
      const typeMatched = typeFilter === "all" || displayAccountType(account) === typeFilter;
      const statusMatched = statusFilter === "all" || account.status === statusFilter;
      return searchMatched && typeMatched && statusMatched;
    });
  }, [accounts, query, statusFilter, typeFilter]);

  const groupedAccounts = useMemo(() => {
    return [...filteredAccounts].sort(compareAccountsByUsage);
  }, [filteredAccounts]);

  const pageCount = Math.max(1, Math.ceil(groupedAccounts.length / Number(pageSize)));
  const safePage = Math.min(page, pageCount);
  const startIndex = (safePage - 1) * Number(pageSize);
  const currentRows = groupedAccounts.slice(startIndex, startIndex + Number(pageSize));
  const allCurrentSelected =
    currentRows.length > 0 && currentRows.every((row) => selectedIds.includes(row.access_token));

  const summary = useMemo(() => {
    const total = accounts.length;
    const active = accounts.filter((item) => item.status === "正常").length;
    const limited = accounts.filter((item) => item.status === "限流").length;
    const abnormal = accounts.filter((item) => isAbnormalAccountStatus(item.status)).length;
    const disabled = accounts.filter((item) => item.status === "禁用").length;
    const cooling = accounts.filter((item) => item.cooldown_active || item.proxy_cooldown_active).length;
    const proxyCooling = accounts.filter((item) => item.proxy_cooldown_active).length;
    const quota = formatQuotaSummary(accounts);

    return { total, active, limited, abnormal, disabled, cooling, proxyCooling, quota };
  }, [accounts]);

  const accountTypeOptions = useMemo(
    () => [
      { label: "全部类型", value: "all" },
      ...Array.from(new Set(accounts.map(displayAccountType))).map((type) => ({ label: type, value: type })),
    ],
    [accounts],
  );

  const selectedTokens = useMemo(() => {
    const selectedSet = new Set(selectedIds);
    return accounts.filter((item) => selectedSet.has(item.access_token)).map((item) => item.access_token);
  }, [accounts, selectedIds]);

  const abnormalTokens = useMemo(() => {
    return accounts.filter((item) => isAbnormalAccountStatus(item.status)).map((item) => item.access_token);
  }, [accounts]);

  const handleDeleteTokens = async (tokens: string[]) => {
    if (tokens.length === 0) {
      toast.error("请先选择要删除的账户");
      return;
    }

    setIsDeleting(true);
    try {
      const data = await deleteAccounts(tokens);
      setAccounts(data.items);
      setSelectedIds((prev) => prev.filter((id) => data.items.some((item) => item.access_token === id)));
      toast.success(`删除 ${data.removed ?? 0} 个账户`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "删除账户失败";
      toast.error(message);
    } finally {
      setIsDeleting(false);
    }
  };

  const handleRefreshAccounts = async (accessTokens: string[]) => {
    if (accessTokens.length === 0) {
      toast.error("没有需要刷新的账户");
      return;
    }

    if (accessTokens.length === 1) {
      setRefreshingTokens((prev) => new Set([...prev, accessTokens[0]]));
      setProgress({
        visible: true,
        current: 0,
        total: 1,
        message: "正在刷新账号信息...",
        email: accounts.find((item) => item.access_token === accessTokens[0])?.email || "",
      });
      try {
        const { progress_id } = await refreshAccounts(accessTokens);
        // 单账号：轮询等待完成
        await pollRefreshProgress(progress_id, (progress) => {
          setProgress((prev) => ({
            ...prev,
            current: progress.done ? 1 : Math.min(1, progress.processed || 0),
            message: progress.done ? "刷新完成" : "正在刷新账号信息...",
          }));
        });
        await loadAccounts(true);
        setTimeout(resetProgress, 800);
      } catch (error) {
        resetProgress();
        const message = error instanceof Error ? error.message : "刷新账户失败";
        toast.error(message);
      } finally {
        setRefreshingTokens((prev) => {
          const next = new Set(prev);
          next.delete(accessTokens[0]);
          return next;
        });
      }
      return;
    }

    setIsRefreshing(true);

    // 计算非选中账号的基数（统计卡片联动用）
    const selectedTokenSet = new Set(accessTokens);
    const baseAccountsList = accounts.filter((a) => !selectedTokenSet.has(a.access_token));
    const baseActive = baseAccountsList.filter((a) => a.status === "正常").length;
    const baseLimited = baseAccountsList.filter((a) => a.status === "限流").length;
    const baseAbnormal = baseAccountsList.filter((a) => isAbnormalAccountStatus(a.status)).length;
    const baseDisabled = baseAccountsList.filter((a) => a.status === "禁用").length;
    const baseNormalAccounts = baseAccountsList.filter((a) => a.status === "正常");
    const baseHasUnlimited = baseNormalAccounts.some(isUnlimitedImageQuotaAccount);
    const baseHasUnknown = baseNormalAccounts.some(imageQuotaUnknown);
    const baseQuotaNum = baseNormalAccounts.reduce((s, a) => s + Math.max(0, a.quota), 0);

    // 显示进度条（只显示当前任务，不含分类统计）
    const total = accessTokens.length;
    setProgress({
      visible: true,
      current: 0,
      total,
      message: "正在刷新账号信息...",
      email: "",
    });
    setProgressResults([]);

    try {
      const { progress_id } = await refreshAccounts(accessTokens);

      // 刷新进度只包含逐账号结果；完成后统一重新拉取持久化的账号列表。
      const results = await new Promise<NonNullable<RefreshProgressResponse["results"]>>((resolve, reject) => {
        const pollTimer = setInterval(async () => {
          try {
            const p = await fetchRefreshProgress(progress_id);
            if (p.done) {
              clearInterval(pollTimer);
              if (p.error) {
                reject(new Error(p.error));
                return;
              }
              // 更新最终进度显示
              if (progressRef.current) {
                clearInterval(progressRef.current);
                progressRef.current = null;
              }
              setProgress((prev) => ({
                ...prev,
                current: prev.total,
                message: "刷新完成",
                email: "",
              }));
              setProgressResults(p.results ?? []);
              // 清除联动统计
              setRefreshSummary(null);
              resolve(p.results ?? []);
            } else {
              // 实时更新进度
              setProgressResults(p.results ?? []);
              const latest = p.results?.[p.results.length - 1];
              setProgress((prev) => ({
                ...prev,
                message: latest ? `已处理 ${p.processed}/${p.total}` : "正在刷新账号信息...",
                email: latest?.email || latest?.token || "",
              }));
              animateProgressTo(p.processed);
              // 实时更新统计卡片：基数 + 已刷新的累加结果
              const runningActive = baseActive + ((p.status_counts?.success) ?? 0);
              const runningLimited = baseLimited;
              const runningAbnormal = baseAbnormal + ((p.status_counts?.error) ?? 0);
              const runningDisabled = baseDisabled + ((p.status_counts?.["禁用"]) ?? 0);
              let runningQuota: string | number;
              if (baseHasUnlimited) {
                runningQuota = "∞";
              } else if (baseHasUnknown) {
                runningQuota = "未知";
              } else {
                runningQuota = formatCompact(baseQuotaNum + (p.total_quota ?? 0));
              }
              setRefreshSummary({
                total: accounts.length,
                active: runningActive,
                limited: runningLimited,
                abnormal: runningAbnormal,
                disabled: runningDisabled,
                quota: runningQuota,
              });
            }
          } catch (err) {
            clearInterval(pollTimer);
            reject(err);
          }
        }, 300);
      });

      await loadAccounts(true);
      const successCount = results.filter((item) => item.status === "success").length;
      const failedResults = results.filter((item) => item.status === "error");
      const removedCount = results.filter((item) => item.status === "removed").length;
      setProgress({ visible: true, current: total, total, message: "刷新完成", email: "" });
      setTimeout(resetProgress, failedResults.length > 0 ? 8000 : 1000);

      if (failedResults.length > 0) {
        const firstError = formatProgressError(failedResults[0]?.error);
        toast.warning(`刷新完成：成功 ${successCount} 个，失败 ${failedResults.length} 个${removedCount ? `，已移除 ${removedCount} 个失效账号` : ""}${firstError ? `，首个错误：${firstError}` : ""}`);
      } else {
        toast.success(`刷新完成：成功 ${successCount} 个${removedCount ? `，已移除 ${removedCount} 个失效账号` : ""}`);
      }
    } catch (error) {
      resetProgress();
      setRefreshSummary(null);
      const message = error instanceof Error ? error.message : "刷新账户失败";
      toast.error(message);
    } finally {
      setIsRefreshing(false);
    }
  };

  const pollRefreshProgress = async (
    progressId: string,
    onUpdate: (p: RefreshProgressResponse) => void,
  ): Promise<void> => {
    return new Promise<void>((resolve, reject) => {
      const timer = setInterval(async () => {
        try {
          const p = await fetchRefreshProgress(progressId);
          onUpdate(p);
          if (p.done) {
            clearInterval(timer);
            if (p.error) {
              reject(new Error(p.error));
            } else {
              resolve();
            }
          }
        } catch (err) {
          clearInterval(timer);
          reject(err);
        }
      }, 500);
    });
  };

  const handleReLogin = async (accessTokens: string[]) => {
    if (accessTokens.length === 0) {
      toast.error("请先选择要恢复的账户");
      return;
    }

    // 只处理异常账号，过滤非异常账号
    const abnormalTokens = accessTokens.filter((token) => {
      const account = accounts.find((a) => a.access_token === token);
      return isAbnormalAccountStatus(account?.status);
    });

    if (abnormalTokens.length === 0) {
      toast.error("选中账号中没有异常账号");
      return;
    }

    if (abnormalTokens.length < accessTokens.length) {
      toast.info(`已过滤 ${accessTokens.length - abnormalTokens.length} 个非异常账号`);
    }

    setIsRelogining(true);

    // 计算非选中账号的基数（统计卡片联动用）
    const selectedTokenSet = new Set(abnormalTokens);
    const baseAccountsList = accounts.filter((a) => !selectedTokenSet.has(a.access_token));
    const baseActive = baseAccountsList.filter((a) => a.status === "正常").length;
    const baseLimited = baseAccountsList.filter((a) => a.status === "限流").length;
    const baseAbnormal = baseAccountsList.filter((a) => isAbnormalAccountStatus(a.status)).length;
    const baseDisabled = baseAccountsList.filter((a) => a.status === "禁用").length;

    // 显示进度条（真实进度）
    const total = abnormalTokens.length;
    setProgress({ visible: true, current: 0, total, message: "正在尝试恢复异常账号...", email: "" });
    setProgressResults([]);

    try {
      const { progress_id } = await reLoginAccounts(abnormalTokens);
      let finalResults: NonNullable<RefreshProgressResponse["results"]> = [];

      // 轮询进度到完成
      await new Promise<void>((resolve, reject) => {
        const pollTimer = setInterval(async () => {
          try {
            const p = await fetchReLoginProgress(progress_id);
            const results = p.results ?? [];
            finalResults = results;
            setProgressResults(results);

            if (p.done) {
              clearInterval(pollTimer);
              if (p.error) {
                reject(new Error(p.error));
                return;
              }
              const summary = reloginResultSummary(results);
              const message = `恢复完成：成功 ${summary.success}，失败 ${summary.failed}，需验证码 ${summary.verification}，禁用 ${summary.disabled}，跳过 ${summary.skipped}`;
              setProgress((prev) => ({ ...prev, current: prev.total, message, email: "" }));
              setRefreshSummary(null);
              resolve();
            } else {
              // 实时更新进度
              // 找到最新一条有错误的结果
              const lastErrorResult = [...results].reverse().find((r) => r.error);
              const emailHint = lastErrorResult
                ? `${isReloginVerificationRequired(lastErrorResult) ? "需验证码" : "失败"}: ${lastErrorResult.email || lastErrorResult.token} ${lastErrorResult.error ?? ""}`
                : `已处理 ${p.processed}/${p.total}`;
              setProgress((prev) => ({
                ...prev,
                current: p.processed,
                email: emailHint,
                message: "正在尝试恢复异常账号...",
              }));

              // 实时更新统计卡片：基数 + 已处理的恢复结果
              let runningActive = baseActive;
              let runningAbnormal = baseAbnormal;
              let runningDisabled = baseDisabled;
              for (const r of results) {
                if (r.status === "成功") {
                  runningActive += 1;
                  runningAbnormal -= 1;
                } else if (r.status === "禁用") {
                  runningDisabled += 1;
                  runningAbnormal -= 1;
                }
                // "异常"、"需验证码"或"跳过"：保持异常状态不变
              }
              setRefreshSummary({
                total: accounts.length,
                active: runningActive,
                limited: baseLimited,
                abnormal: runningAbnormal,
                disabled: runningDisabled,
                quota: summary.quota,
              });
            }
          } catch (err) {
            clearInterval(pollTimer);
            reject(err);
          }
        }, 300);
      });

      // 等待后台线程完成，再拉取最新数据
      await new Promise<void>((resolve) => setTimeout(resolve, 500));
      try {
        const freshData = await fetchAccounts();
        setAccounts(freshData.items);
        setSelectedIds((prev) => prev.filter((id) => freshData.items.some((item) => item.access_token === id)));
      } catch { /* 静默失败 */ }

      setProgress({
        visible: true,
        current: total,
        total,
        message: (() => {
          const resultSummary = reloginResultSummary(finalResults);
          return `恢复完成：成功 ${resultSummary.success}，失败 ${resultSummary.failed}，需验证码 ${resultSummary.verification}，禁用 ${resultSummary.disabled}，跳过 ${resultSummary.skipped}`;
        })(),
        email: "",
      });
      setProgressResults(finalResults);
      setTimeout(resetProgress, 8000);

      const resultSummary = reloginResultSummary(finalResults);
      if (resultSummary.failed > 0 || resultSummary.verification > 0 || resultSummary.skipped > 0 || resultSummary.disabled > 0) {
        toast.warning(`恢复完成：成功 ${resultSummary.success}，失败 ${resultSummary.failed}，需验证码 ${resultSummary.verification}，禁用 ${resultSummary.disabled}，跳过 ${resultSummary.skipped}`);
      } else {
        toast.success(`恢复成功 ${resultSummary.success} 个账号`);
      }
    } catch (error) {
      resetProgress();
      setRefreshSummary(null);
      const message = error instanceof Error ? error.message : "重新登录失败";
      toast.error(message);
    } finally {
      setIsRelogining(false);
    }
  };

  const openEditDialog = (account: Account) => {
    setEditingAccount(account);
    setEditStatus(account.status);
    setEditEmail(account.email ?? "");
    setEditPassword("");
    setEditProxy(account.proxy ?? "");
  };

  const handleTestAccountProxy = async () => {
    const candidate = editProxy.trim();
    if (!candidate) {
      toast.error("请先填写代理地址");
      return;
    }
    setIsTestingProxy(true);
    try {
      const data = await testProxy(candidate);
      data.result.ok
        ? toast.success(`代理可用（${data.result.latency_ms} ms，HTTP ${data.result.status}）`)
        : toast.error(`代理不可用：${data.result.error ?? "未知错误"}`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "测试代理失败");
    } finally {
      setIsTestingProxy(false);
    }
  };

  const handleTestAccountImage = async (account: Account) => {
    setTestingImageTokens((prev) => new Set([...prev, account.access_token]));
    try {
      const data = await testAccountImage(account.access_token);
      setAccounts(data.items);
      setSelectedIds((prev) => prev.filter((id) => data.items.some((item) => item.access_token === id)));
      if (data.ok) {
        toast.success(`生图测试成功${data.image_count ? `，生成 ${data.image_count} 张` : ""}`);
      } else {
        toast.error(`生图测试失败：${data.error || "未生成图片"}`);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "生图测试失败");
    } finally {
      setTestingImageTokens((prev) => {
        const next = new Set(prev);
        next.delete(account.access_token);
        return next;
      });
    }
  };

  const handleUpdateAccount = async () => {
    if (!editingAccount) {
      return;
    }

    setIsUpdating(true);
    try {
      const updates = {
        status: editStatus,
        email: editEmail.trim(),
        proxy: editProxy.trim(),
        ...(editPassword.trim() ? { password: editPassword } : {}),
      };
      const data = await updateAccount(editingAccount.access_token, {
        ...updates,
      });
      setAccounts(data.items);
      setSelectedIds((prev) => prev.filter((id) => data.items.some((item) => item.access_token === id)));
      setEditingAccount(null);
      toast.success("账号信息已更新");
    } catch (error) {
      const message = error instanceof Error ? error.message : "更新账号失败";
      toast.error(message);
    } finally {
      setIsUpdating(false);
    }
  };

  const toggleSelectAll = (checked: boolean) => {
    if (checked) {
      setSelectedIds((prev) => Array.from(new Set([...prev, ...currentRows.map((item) => item.access_token)])));
      return;
    }
    setSelectedIds((prev) => prev.filter((id) => !currentRows.some((row) => row.access_token === id)));
  };

  const accountColumns: ColumnsType<Account> = [
    {
      title: (
        <div className="flex items-center justify-center">
          <AntCheckbox
            checked={allCurrentSelected}
            indeterminate={currentRows.some((row) => selectedIds.includes(row.access_token)) && !allCurrentSelected}
            onChange={(event) => toggleSelectAll(event.target.checked)}
          />
        </div>
      ),
      dataIndex: "access_token",
      width: 56,
      align: "center",
      fixed: isCompactTable ? undefined : "left",
      render: (_value, account) => (
        <div className="flex items-center justify-center">
          <AntCheckbox
            checked={selectedIds.includes(account.access_token)}
            onChange={(event) => {
              setSelectedIds((prev) =>
                event.target.checked
                  ? Array.from(new Set([...prev, account.access_token]))
                  : prev.filter((item) => item !== account.access_token),
              );
            }}
          />
        </div>
      ),
    },
    {
      title: "邮箱",
      dataIndex: "email",
      width: 240,
      fixed: isCompactTable ? undefined : "left",
      render: (email: string | null | undefined, account) => (
        <div className="min-w-0 space-y-1">
          <div className="truncate text-xs text-slate-600">{email ?? "—"}</div>
          {account.has_password ? (
            <Tag color="green" className="m-0 text-[11px]">已保存密码</Tag>
          ) : (
            <Tag color={isAbnormalAccountStatus(account.status) ? "warning" : "default"} className="m-0 text-[11px]">缺少密码</Tag>
          )}
        </div>
      ),
    },
    {
      title: "分组",
      key: "group",
      width: 112,
      render: (_value, account) => {
        const groupKey = getAccountGroupKey(account);
        const color = groupKey === "plus" ? "gold" : groupKey === "free" ? "blue" : "default";
        return <Tag color={color}>{accountGroupMeta[groupKey].label}</Tag>;
      },
    },
    {
      title: "类型",
      key: "type",
      width: 96,
      render: (_value, account) => <Tag>{displayAccountType(account)}</Tag>,
    },
    {
      title: "健康",
      key: "health",
      width: 132,
      render: (_value, account) => {
        const score = typeof account.health_score === "number" ? account.health_score : account.dispatch_score;
        const reasons = Array.isArray(account.health_reasons) ? account.health_reasons : [];
        return (
          <Tooltip title={reasons.length ? reasons.join(" / ") : "账号近期表现稳定"}>
            <div className="w-[108px] space-y-1">
              <div className="flex items-center justify-between gap-2">
                <span className="text-xs font-medium text-slate-600">{account.health_label || "健康"}</span>
                <span className="font-mono text-[11px] text-slate-400">{typeof score === "number" ? score.toFixed(0) : "-"}</span>
              </div>
              <AntProgress
                percent={typeof score === "number" ? Math.round(score) : 0}
                showInfo={false}
                size={[108, 5]}
                strokeColor={healthColor(score)}
                railColor="#eef2f7"
              />
            </div>
          </Tooltip>
        );
      },
    },
    {
      title: "状态",
      dataIndex: "status",
      width: 92,
      render: (status: string) => {
        const label = accountStatusLabel(status);
        const StatusIcon = label === "验证中" ? LoaderCircle : statusMeta[status as AccountStatus]?.icon ?? CircleAlert;
        const statusClass =
          label === "正常"
            ? "bg-[#f6ffed] text-[#52c41a] ring-[#b7eb8f]"
            : label === "限流"
              ? "bg-amber-50 text-amber-700 ring-amber-200"
              : label === "异常" || label === "失效"
                ? "bg-rose-50 text-rose-700 ring-rose-200"
                : label === "恢复中"
                  ? "bg-amber-50 text-amber-700 ring-amber-200"
                : "bg-slate-100 text-slate-600 ring-slate-200";
        return (
          <span className={cn("inline-flex h-7 min-w-[62px] items-center justify-center gap-1 rounded-md px-2 text-xs font-medium leading-none ring-1 whitespace-nowrap", statusClass)}>
            <StatusIcon className={cn("size-3.5 shrink-0", label === "恢复中" && "animate-spin")} />
            {label}
          </span>
        );
      },
    },
    {
      title: "额度",
      key: "quota",
      width: 178,
      render: (_value, account) => (
        <Tooltip title={quotaProgress(account).title} placement="topLeft">
          <div>
            <QuotaProgressCell account={account} />
          </div>
        </Tooltip>
      ),
    },
    {
      title: "调度",
      key: "dispatch",
      width: 116,
      render: (_value, account) => {
        const cooldown = account.cooldown_active ? formatShortRelative(account.cooldown_until) : "";
        const recentRate = typeof account.recent_success_rate === "number" ? `${account.recent_success_rate}%` : "—";
        const error = errorTypeLabel(account.last_error_type);
        return (
          <Tooltip
            title={`最近成功率 ${recentRate}${account.recent_total ? ` (${account.recent_success || 0}/${account.recent_total})` : ""}${cooldown ? `，冷却${cooldown}` : ""}${error ? `，最近错误 ${error}` : ""}`}
            placement="topLeft"
          >
            <div className="space-y-1 text-xs">
              <Tag color={account.cooldown_active ? "warning" : "green"} className="m-0">
                {account.cooldown_active ? "冷却中" : "可调度"}
              </Tag>
              <div className="text-slate-500">
                连败 {account.consecutive_failures || 0}
              </div>
            </div>
          </Tooltip>
        );
      },
    },
    {
      title: "代理",
      key: "proxy",
      width: 150,
      render: (_value, account) => {
        const proxy = String(account.proxy || "").trim();
        if (!proxy) {
          return <span className="text-xs text-slate-400">—</span>;
        }
        const stats = account.proxy_stats || {};
        const success = Number(stats.success || 0);
        const fail = Number(stats.fail || 0);
        const error = errorTypeLabel(stats.last_error_type);
        return (
          <Tooltip title={`${proxy}${error ? `，最近错误 ${error}` : ""}`} placement="topLeft">
            <div className="space-y-1 text-xs">
              <Tag color={account.proxy_cooldown_active ? "warning" : "blue"} className="m-0 max-w-[128px] truncate">
                {account.proxy_cooldown_active ? "代理冷却" : "代理"}
              </Tag>
              <div className="font-mono text-slate-500">
                {success}/{fail}
                {account.proxy_cooldown_active ? ` · ${formatShortRelative(stats.cooldown_until)}` : ""}
              </div>
            </div>
          </Tooltip>
        );
      },
    },
    {
      title: "创建时间",
      dataIndex: "created_at",
      width: 132,
      render: (raw: unknown) => {
        if (!raw) return "—";
        return formatShanghaiDateTime(typeof raw === "string" || typeof raw === "number" ? raw : null, "—");
      },
    },
    {
      title: "操作",
      key: "actions",
      width: 196,
      fixed: isCompactTable ? undefined : "right",
      render: (_value, account) => (
        <Space size={2}>
          <AntButton
            type="text"
            size="small"
            icon={<Pencil className="size-4" />}
            onClick={() => openEditDialog(account)}
            disabled={isUpdating}
          />
          <Tooltip title="查看凭证恢复日志">
            <AntButton
              type="text"
              size="small"
              icon={<History className="size-4" />}
              onClick={() => {
                setCredentialRecoveryLogEmail(account.email || "");
                setCredentialRecoveryLogOpen(true);
              }}
            />
          </Tooltip>
          <AntButton
            type="text"
            size="small"
            icon={<ImageIcon className={cn("size-4", testingImageTokens.has(account.access_token) ? "animate-pulse" : "")} />}
            onClick={() => void handleTestAccountImage(account)}
            disabled={testingImageTokens.has(account.access_token)}
          />
          <AntButton
            type="text"
            size="small"
            icon={<RefreshCw className={cn("size-4", (isRefreshing || refreshingTokens.has(account.access_token)) ? "animate-spin" : "")} />}
            onClick={() => void handleRefreshAccounts([account.access_token])}
            disabled={isRefreshing || refreshingTokens.has(account.access_token)}
          />
          <AntButton
            danger
            type="text"
            size="small"
            icon={<Trash2 className="size-4" />}
            onClick={() => void handleDeleteTokens([account.access_token])}
            disabled={isDeleting}
          />
        </Space>
      ),
    },
  ];

  return (
    <div className="space-y-5">
      <section className="rounded-lg bg-white p-5 shadow-sm">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div className="space-y-1">
            <div className="text-xs font-semibold tracking-[0.18em] text-slate-400 uppercase">
              Account Pool
            </div>
            <h1 className="text-2xl font-semibold tracking-tight text-slate-950">号池管理</h1>
            <p className="text-sm text-slate-500">管理账号状态、额度、Codex 使用率和批量刷新任务。</p>
          </div>

          <Space wrap>
            <AntButton
              icon={<RefreshCw className={cn("size-4", isLoading ? "animate-spin" : "")} />}
              onClick={() => void loadAccounts()}
              disabled={isLoading || isRefreshing || isDeleting}
            >
              刷新
            </AntButton>
            <AntButton
              icon={<History className="size-4" />}
              onClick={() => {
                setCredentialRecoveryLogEmail("");
                setCredentialRecoveryLogOpen(true);
              }}
            >
              恢复日志
            </AntButton>
            <AntButton
              type="primary"
              icon={<RefreshCw className={cn("size-4", isRefreshing ? "animate-spin" : "")} />}
              onClick={() => void handleRefreshAccounts(accounts.map((item) => item.access_token))}
              disabled={isLoading || isRefreshing || isDeleting || accounts.length === 0}
            >
              一键刷新所有账号信息和额度
            </AntButton>
            <AccountImportDialog
              disabled={isLoading || isRefreshing || isDeleting}
              onImported={(items) => {
                setAccounts(items);
                setSelectedIds([]);
                setPage(1);
              }}
            />
            <AntButton
              icon={<Download className="size-4" />}
              onClick={() => downloadTokens(accounts)}
              disabled={accounts.length === 0}
            >
              导出全部 Token
            </AntButton>
          </Space>
        </div>
      </section>

      {/* 进度条 */}
      {progress.visible && (
        <AntCard className="!mb-5">
          <div className="space-y-4">
            <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
              <div className="min-w-0 flex-1">
                <div className="flex items-center justify-between gap-4 text-sm">
                  <span className="truncate text-slate-600">
                    {progress.message}
                    {progress.email ? <span className="ml-1 font-medium text-slate-800">{progress.email}</span> : null}
                  </span>
                  <span className="shrink-0 font-medium text-slate-700">
                    {progress.current}/{progress.total}
                  </span>
                </div>
                <AntProgress
                  className="mt-2"
                  percent={progress.total > 0 ? Math.round((progress.current / progress.total) * 100) : 0}
                  strokeColor="#1677ff"
                />
              </div>
              <Steps
                className="lg:max-w-md"
                size="small"
                current={progress.total > 0 && progress.current >= progress.total ? 2 : progress.current > 0 ? 1 : 0}
                items={[
                  { title: "启动刷新" },
                  { title: "实时处理" },
                  { title: "完成同步" },
                ]}
              />
            </div>
            {progressResults.length > 0 ? (
              <div className="grid max-h-64 gap-2 overflow-y-auto pr-1 sm:grid-cols-2 lg:grid-cols-3">
                {[...progressResults].reverse().map((item, index) => (
                  <div key={`${item.token}-${index}`} className="flex items-start justify-between gap-3 rounded-lg border border-slate-100 bg-slate-50 px-3 py-2 text-xs">
                    <div className="min-w-0">
                      <div className="truncate font-medium text-slate-700">{item.email || item.token}</div>
                      {item.error ? (
                        <div className={`mt-1 truncate ${isReloginVerificationRequired(item) ? "text-amber-600" : "text-rose-500"}`}>
                          {formatProgressError(item.error)}
                        </div>
                      ) : null}
                    </div>
                    <Tag color={reloginResultTagColor(item)} className="m-0 shrink-0">
                      {reloginResultLabel(item)}
                    </Tag>
                  </div>
                ))}
              </div>
            ) : null}
          </div>
        </AntCard>
      )}

      <Modal
        title="凭证恢复日志"
        open={credentialRecoveryLogOpen}
        onCancel={() => setCredentialRecoveryLogOpen(false)}
        width={820}
        footer={<AntButton onClick={() => setCredentialRecoveryLogOpen(false)}>关闭</AntButton>}
        styles={{ body: { maxHeight: "68vh", overflow: "auto" } }}
      >
        <div className="space-y-3 pt-2">
          <div className="flex flex-col gap-2 sm:flex-row">
            <AntInput
              allowClear
              value={credentialRecoveryLogEmail}
              onChange={(event) => setCredentialRecoveryLogEmail(event.target.value)}
              placeholder="按账号邮箱筛选，留空查看全部"
            />
            <Tooltip title="刷新恢复日志">
              <AntButton
                className="sm:w-10"
                icon={<RefreshCw className={cn("size-4", isLoadingCredentialRecoveryLogs && "animate-spin")} />}
                onClick={() => void loadCredentialRecoveryLogs()}
                loading={isLoadingCredentialRecoveryLogs}
              />
            </Tooltip>
          </div>

          {isLoadingCredentialRecoveryLogs && credentialRecoveryLogs.length === 0 ? (
            <div className="flex min-h-40 items-center justify-center"><Spin /></div>
          ) : credentialRecoveryLogs.length === 0 ? (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无凭证恢复记录" />
          ) : (
            <div className="space-y-2">
              {credentialRecoveryLogs.map((log) => (
                <div key={log.id} className="border border-slate-200 bg-white px-3 py-3">
                  <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
                    <div className="flex min-w-0 flex-wrap items-center gap-2">
                      <span className="max-w-full break-all text-sm font-medium text-slate-700">{log.account_email || "未识别账号"}</span>
                      <Tag color={credentialRecoveryLogColor(log.level)} className="m-0">{credentialRecoveryEventLabel(log.event)}</Tag>
                      {log.attempt ? <span className="text-xs text-slate-400">第 {log.attempt} 次</span> : null}
                    </div>
                    <span className="shrink-0 text-xs text-slate-400">{formatShanghaiDateTime(log.time, "-")}</span>
                  </div>
                  <p className="mt-2 break-words text-sm leading-6 text-slate-600">{log.message}</p>
                  {log.error ? <pre className="mt-2 whitespace-pre-wrap break-words border-l-2 border-rose-300 bg-rose-50 px-3 py-2 text-xs leading-5 text-rose-700">{log.error}</pre> : null}
                </div>
              ))}
            </div>
          )}
        </div>
      </Modal>

      <Modal
        title="编辑账户"
        open={Boolean(editingAccount)}
        onCancel={() => setEditingAccount(null)}
        footer={[
          <AntButton key="cancel" onClick={() => setEditingAccount(null)} disabled={isUpdating}>
            取消
          </AntButton>,
          <AntButton key="submit" type="primary" loading={isUpdating} onClick={() => void handleUpdateAccount()}>
            保存修改
          </AntButton>,
        ]}
      >
        <div className="pt-2">
          <p className="mb-4 text-sm text-slate-500">手动修改账号状态、恢复凭据和专属代理。</p>
          <div className="space-y-4">
            <div className="space-y-2">
              <label className="text-sm font-medium text-slate-700">状态</label>
              <AntSelect
                value={editStatus}
                onChange={(value) => setEditStatus(value as AccountStatus)}
                className="w-full"
                options={accountStatusOptions
                  .filter((option) => option.value !== "all")
                  .map((option) => ({ label: option.label, value: option.value }))}
              />
            </div>
            <div className="rounded-lg border border-slate-100 bg-slate-50 p-3">
              <div className="mb-3 flex items-center justify-between gap-3">
                <div>
                  <div className="text-sm font-medium text-slate-700">恢复凭据</div>
                  <div className="mt-1 text-xs text-slate-500">用于“恢复异常”重新登录。密码不会回显，留空表示不修改已保存密码。</div>
                </div>
                <Tag color={editingAccount?.has_password ? "green" : "warning"} className="m-0 shrink-0">
                  {editingAccount?.has_password ? "已有密码" : "缺少密码"}
                </Tag>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <div className="space-y-2">
                  <label className="text-sm font-medium text-slate-700">登录邮箱</label>
                  <AntInput
                    value={editEmail}
                    onChange={(event) => setEditEmail(event.target.value)}
                    placeholder="you@example.com"
                  />
                </div>
                <div className="space-y-2">
                  <label className="text-sm font-medium text-slate-700">新密码</label>
                  <AntInput.Password
                    value={editPassword}
                    onChange={(event) => setEditPassword(event.target.value)}
                    placeholder={editingAccount?.has_password ? "留空不修改" : "填写后可恢复异常"}
                  />
                </div>
              </div>
            </div>
            <div className="space-y-2">
              <label className="text-sm font-medium text-slate-700">账号代理</label>
              <div className="flex flex-col gap-2 sm:flex-row">
                <AntInput
                  value={editProxy}
                  onChange={(event) => setEditProxy(event.target.value)}
                  placeholder="留空走全局代理，例如 http://127.0.0.1:7890"
                />
                <AntButton
                  className="sm:w-24"
                  onClick={() => void handleTestAccountProxy()}
                  loading={isTestingProxy}
                >
                  测试
                </AntButton>
              </div>
            </div>
          </div>
        </div>
      </Modal>

      <section className="space-y-5">
        <Row gutter={[18, 18]}>
          {metricCards.map((item) => {
            const Icon = item.icon;
            const value = (refreshSummary ?? summary)[item.key];
            return (
              <Col key={item.key} xs={24} sm={12} lg={6}>
              <AntCard size="small" styles={{ body: { minHeight: 92, padding: 16 } }}>
                <div className="mb-3 flex items-center justify-between">
                  <span className="text-xs text-slate-400">{item.label}</span>
                  <Icon className="size-4 text-slate-400" />
                </div>
                <div className={cn("text-2xl font-semibold tracking-tight", item.color)}>
                  {typeof value === "number" ? formatCompact(value) : value}
                </div>
              </AntCard>
              </Col>
            );
          })}
        </Row>
        <AntCard size="small">
          <div className="mb-3 text-sm font-medium text-slate-700">
              系统可用模型
            <span className="ml-1 text-slate-400">({availableModels.length})</span>
          </div>
          <div className="flex flex-wrap gap-2">
            {availableModels.length > 0 ? (
              availableModels.map((model) => (
                <Tag
                  key={model.id}
                  className="cursor-pointer rounded-full px-2.5 py-1"
                  onClick={() => {
                    void navigator.clipboard.writeText(model.id);
                    toast.success("模型名已复制");
                  }}
                >
                  {model.id}
                </Tag>
              ))
            ) : isLoadingModels ? (
              <Spin size="small" />
            ) : (
              <span className="text-sm text-slate-400">当前暂无可用模型</span>
            )}
          </div>
        </AntCard>
      </section>

      <section>

        {isLoading && accounts.length === 0 ? (
          <AntCard>
            <div className="flex flex-col items-center justify-center gap-3 px-6 py-14 text-center">
              <Spin />
              <div className="space-y-1">
                <p className="text-sm font-medium text-slate-700">正在加载账户</p>
                <p className="text-sm text-slate-500">从后端同步账号列表和状态。</p>
              </div>
            </div>
          </AntCard>
        ) : null}

        <AntCard
          className={cn("accounts-table-card", isLoading && accounts.length === 0 ? "hidden" : "")}
          title={
            <Space>
              <span>账户列表</span>
              <Tag color="blue" className="m-0">{filteredAccounts.length}</Tag>
            </Space>
          }
          extra={
            <Space wrap>
              <AntInput
                allowClear
                prefix={<Search className="size-4 text-slate-400" />}
                value={query}
                onChange={(event) => {
                  setQuery(event.target.value);
                  setPage(1);
                }}
                placeholder="搜索邮箱"
                style={{ width: 260 }}
              />
              <AntSelect
                value={typeFilter}
                onChange={(value) => {
                  setTypeFilter(value);
                  setPage(1);
                }}
                style={{ width: 150 }}
                options={accountTypeOptions}
              />
              <AntSelect
                value={statusFilter}
                onChange={(value) => {
                  setStatusFilter(value as AccountStatus | "all");
                  setPage(1);
                }}
                style={{ width: 150 }}
                options={accountStatusOptions}
              />
            </Space>
          }
          styles={{ body: { padding: 0 } }}
        >
            <div className="flex flex-col gap-3 border-b border-slate-100 bg-slate-50/60 px-4 py-3 lg:flex-row lg:items-center lg:justify-between">
              <div className="flex flex-wrap items-center gap-2">
                <span className="mr-1 text-xs font-medium text-slate-400">批量操作</span>
                <AntButton
                  size="small"
                  icon={isRefreshing ? <LoaderCircle className="size-4 animate-spin" /> : <RefreshCw className="size-4" />}
                  onClick={() => void handleRefreshAccounts(selectedTokens)}
                  disabled={selectedTokens.length === 0 || isRefreshing}
                >
                  刷新选中
                </AntButton>
                <AntButton
                  size="small"
                  icon={isRelogining ? <LoaderCircle className="size-4 animate-spin" /> : <LogIn className="size-4" />}
                  onClick={() => void handleReLogin(selectedTokens)}
                  disabled={selectedTokens.length === 0 || isRelogining}
                  title="尝试密码登录恢复账号"
                >
                  恢复异常
                </AntButton>
                <span className="mx-1 h-5 w-px bg-slate-200" />
                <AntButton
                  danger
                  size="small"
                  icon={isDeleting ? <LoaderCircle className="size-4 animate-spin" /> : <Trash2 className="size-4" />}
                  onClick={() => void handleDeleteTokens(abnormalTokens)}
                  disabled={abnormalTokens.length === 0 || isDeleting}
                >
                  移除异常账号
                </AntButton>
                <AntButton
                  danger
                  size="small"
                  icon={isDeleting ? <LoaderCircle className="size-4 animate-spin" /> : <Trash2 className="size-4" />}
                  onClick={() => void handleDeleteTokens(selectedTokens)}
                  disabled={selectedTokens.length === 0 || isDeleting}
                >
                  删除所选
                </AntButton>
                {selectedIds.length > 0 ? (
                  <Tag color="processing" className="m-0 rounded-md">
                    已选择 {selectedIds.length} 项
                  </Tag>
                ) : null}
              </div>
            </div>

            <AntTable
              className="accounts-table"
              rowKey="access_token"
              columns={accountColumns}
              dataSource={currentRows}
              loading={isLoading}
              pagination={false}
              size="small"
              scroll={{ x: isCompactTable ? 980 : 1180 }}
              locale={{
                emptyText: (
                  <Empty
                    image={Empty.PRESENTED_IMAGE_SIMPLE}
                    description="没有匹配的账户，调整筛选条件或搜索关键字后重试。"
                  />
                ),
              }}
            />

            <div className="flex flex-col gap-3 border-t border-slate-100 px-4 py-4 lg:flex-row lg:items-center lg:justify-between">
              <div className="text-sm text-slate-500">
                显示第 {groupedAccounts.length === 0 ? 0 : startIndex + 1} -{" "}
                {Math.min(startIndex + Number(pageSize), groupedAccounts.length)} 条，共{" "}
                {groupedAccounts.length} 条
              </div>
              <AntPagination
                current={safePage}
                pageSize={Number(pageSize)}
                total={groupedAccounts.length}
                showSizeChanger
                pageSizeOptions={[10, 20, 50, 100]}
                showTotal={(total) => `共 ${total} 条`}
                onChange={(nextPage, nextPageSize) => {
                  setPage(nextPage);
                  setPageSize(String(nextPageSize));
                }}
              />
            </div>
        </AntCard>
      </section>
    </div>
  );
}

export default function AccountsPage() {
  const { isCheckingAuth, session } = useAuthGuard(["admin"]);

  if (isCheckingAuth || !session || session.role !== "admin") {
    return (
      <div className="flex min-h-[40vh] items-center justify-center">
        <LoaderCircle className="size-5 animate-spin text-stone-400" />
      </div>
    );
  }

  return <AccountsPageContent />;
}
