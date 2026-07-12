"use client";

import { useRef, useState, type ChangeEvent } from "react";
import {
  Button as AntButton,
  Card as AntCard,
  Input as AntInput,
  Modal,
  Tag,
} from "antd";
import {
  ArrowLeft,
  Copy,
  ExternalLink,
  FileJson,
  FileText,
  KeyRound,
  LogIn,
  Upload,
  type LucideIcon,
} from "lucide-react";
import { toast } from "sonner";

import {
  createAccounts,
  finishOAuthLogin,
  startOAuthLogin,
  type Account,
  type AccountImportPayload,
  type OAuthLoginStartResponse,
} from "@/lib/api";

type ImportMethod = "menu" | "token" | "session" | "codex-auth" | "oauth";

type AccountImportDialogProps = {
  disabled?: boolean;
  onImported: (items: Account[]) => void;
};

const sessionUrl = "https://chatgpt.com/api/auth/session";

function splitTokens(value: string) {
  return value
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function getSessionAccessToken(value: unknown) {
  const token = (value as { accessToken?: unknown })?.accessToken;
  return typeof token === "string" ? token.trim() : "";
}

function getCodexAuthAccount(value: unknown): AccountImportPayload | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return null;
  }
  const raw = value as Record<string, unknown>;
  const tokenValue = raw.access_token ?? raw.accessToken;
  const token = typeof tokenValue === "string" ? tokenValue.trim() : "";
  if (!token) {
    return null;
  }

  const payload: AccountImportPayload = {
    ...raw,
    access_token: token,
    export_type: "codex",
    source_type: "codex",
  };
  delete payload.accessToken;
  if (payload.type === "codex") {
    delete payload.type;
  }
  return payload;
}

function readFileAsText(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(typeof reader.result === "string" ? reader.result : "");
    reader.onerror = () => reject(reader.error ?? new Error(`读取文件失败: ${file.name}`));
    reader.readAsText(file);
  });
}

function MethodCard({
  title,
  description,
  icon: Icon,
  onClick,
  featured = false,
  tag,
}: {
  title: string;
  description: string;
  icon: LucideIcon;
  onClick: () => void;
  featured?: boolean;
  tag?: string;
}) {
  return (
    <AntCard
      hoverable
      size="small"
      onClick={onClick}
      className={`cursor-pointer transition ${featured ? "border-blue-200 bg-blue-50/50" : "bg-white"}`}
      styles={{ body: { padding: featured ? 20 : 18 } }}
    >
      <div className={`flex items-start ${featured ? "gap-4" : "gap-3"}`}>
        <div className={`shrink-0 rounded-xl ${featured ? "bg-blue-600 p-3 text-white" : "bg-blue-50 p-2.5 text-blue-600"}`}>
          <Icon className={featured ? "size-5" : "size-4"} />
        </div>
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex items-center gap-2">
            <div className={`${featured ? "text-base" : "text-sm"} font-semibold text-slate-900`}>{title}</div>
            {tag ? <Tag color="blue" className="m-0">{tag}</Tag> : null}
          </div>
          <div className={`${featured ? "text-sm leading-6" : "text-xs leading-5"} text-slate-500`}>{description}</div>
        </div>
      </div>
    </AntCard>
  );
}

export function AccountImportDialog({ disabled, onImported }: AccountImportDialogProps) {
  const [open, setOpen] = useState(false);
  const [method, setMethod] = useState<ImportMethod>("menu");
  const [tokenInput, setTokenInput] = useState("");
  const [sessionInput, setSessionInput] = useState("");
  const [codexAuthInput, setCodexAuthInput] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [oauthEmailHint, setOauthEmailHint] = useState("");
  const [oauthSession, setOauthSession] = useState<OAuthLoginStartResponse | null>(null);
  const [oauthCallbackInput, setOauthCallbackInput] = useState("");
  const [oauthStarting, setOauthStarting] = useState(false);

  const txtInputRef = useRef<HTMLInputElement | null>(null);

  const resetState = () => {
    setMethod("menu");
    setTokenInput("");
    setSessionInput("");
    setCodexAuthInput("");
    setOauthEmailHint("");
    setOauthSession(null);
    setOauthCallbackInput("");
    setOauthStarting(false);
  };

  const handleOpenChange = (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (!nextOpen) {
      resetState();
    }
  };

  const submitTokens = async (tokens: string[], successText?: string, accountPayloads: AccountImportPayload[] = []) => {
    const normalizedTokens = tokens.map((item) => item.trim()).filter(Boolean);

    if (normalizedTokens.length === 0) {
      toast.error("请先提供至少一个可用 Token");
      return;
    }

    setIsSubmitting(true);
    try {
      const data = await createAccounts(normalizedTokens, accountPayloads);
      onImported(data.items);
      setOpen(false);
      resetState();

      if ((data.errors?.length ?? 0) > 0) {
        const firstError = data.errors?.[0]?.error;
        toast.error(
          `${successText ?? "导入完成"}，新增 ${data.added ?? 0} 个，已刷新 ${data.refreshed ?? 0} 个，失败 ${data.errors?.length ?? 0} 个${firstError ? `，首个错误：${firstError}` : ""}`,
        );
      } else {
        toast.success(
          `${successText ?? "导入完成"}，新增 ${data.added ?? 0} 个，跳过 ${data.skipped ?? 0} 个重复项，已自动刷新账号信息`,
        );
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "导入账户失败";
      toast.error(message);
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleImportTokenText = async () => {
    await submitTokens(splitTokens(tokenInput), "Access Token 导入完成");
  };

  // 起授权：拿 authorize URL，立刻在新窗口打开，方便用户登录
  const handleStartOAuth = async () => {
    setOauthStarting(true);
    try {
      const data = await startOAuthLogin(oauthEmailHint.trim());
      setOauthSession(data);
      setOauthCallbackInput("");
      if (typeof window !== "undefined") {
        window.open(data.authorize_url, "_blank", "noopener,noreferrer");
      }
      toast.success("已打开 OpenAI 授权页面，请在登录后复制 callback URL 回来");
    } catch (error) {
      const message = error instanceof Error ? error.message : "OAuth 起始失败";
      toast.error(message);
    } finally {
      setOauthStarting(false);
    }
  };

  // 用粘贴回来的 callback URL 完成换 token + 落盘
  const handleFinishOAuth = async () => {
    if (!oauthSession) {
      toast.error("请先点击\"打开授权页面\"获取 session");
      return;
    }
    const trimmed = oauthCallbackInput.trim();
    if (!trimmed) {
      toast.error("请粘贴 callback URL 或 code");
      return;
    }

    setIsSubmitting(true);
    try {
      const data = await finishOAuthLogin(oauthSession.session_id, trimmed);
      onImported(data.items);
      setOpen(false);
      resetState();

      if ((data.errors?.length ?? 0) > 0) {
        const firstError = data.errors?.[0]?.error;
        toast.error(
          `OAuth 登录完成，新增 ${data.added ?? 0} 个，已刷新 ${data.refreshed ?? 0} 个，失败 ${data.errors?.length ?? 0} 个${firstError ? `，首个错误：${firstError}` : ""}`,
        );
      } else {
        toast.success(
          `OAuth 登录完成，新增 ${data.added ?? 0} 个，跳过 ${data.skipped ?? 0} 个重复项，已自动刷新账号信息`,
        );
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "OAuth 换 token 失败";
      toast.error(message);
    } finally {
      setIsSubmitting(false);
    }
  };

  // 复制 authorize URL 到剪贴板（适配浏览器和 fallback）
  const handleCopyAuthorizeUrl = async () => {
    if (!oauthSession) {
      return;
    }
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(oauthSession.authorize_url);
        toast.success("授权 URL 已复制到剪贴板");
      } else {
        toast.error("当前环境不支持自动复制，请手动选择并复制");
      }
    } catch {
      toast.error("复制失败，请手动选择并复制");
    }
  };

  const handleTxtSelected = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";

    if (!file) {
      return;
    }

    try {
      const content = await readFileAsText(file);
      const tokens = splitTokens(content);

      if (tokens.length === 0) {
        toast.error("TXT 文件里没有读取到有效 Token");
        return;
      }

      setTokenInput((prev) => {
        const next = [...splitTokens(prev), ...tokens];
        return next.join("\n");
      });
      toast.success(`已从 ${file.name} 读取 ${tokens.length} 个 Token`);
    } catch (error) {
      const message = error instanceof Error ? error.message : "读取 TXT 文件失败";
      toast.error(message);
    }
  };

  const handleImportSessionJson = async () => {
    if (!sessionInput.trim()) {
      toast.error("请先粘贴完整 Session JSON");
      return;
    }

    try {
      const payload = JSON.parse(sessionInput) as unknown;
      const token = getSessionAccessToken(payload);

      if (!token) {
        toast.error("未从 Session JSON 中提取到 accessToken");
        return;
      }

      await submitTokens([token], "Session JSON 导入完成");
    } catch (error) {
      const message = error instanceof Error ? error.message : "Session JSON 解析失败";
      toast.error(message);
    }
  };

  const handleImportCodexAuthJson = async () => {
    if (!codexAuthInput.trim()) {
      toast.error("请先粘贴 Codex 认证 JSON");
      return;
    }

    try {
      const payload = JSON.parse(codexAuthInput) as unknown;
      const account = getCodexAuthAccount(payload);

      if (!account) {
        toast.error("未从 Codex 认证 JSON 中提取到 access_token");
        return;
      }

      await submitTokens([account.access_token], "Codex 认证 JSON 导入完成", [account]);
    } catch (error) {
      const message = error instanceof Error ? error.message : "Codex 认证 JSON 解析失败";
      toast.error(message);
    }
  };

  const renderMethodBody = () => {
    if (method === "token") {
      const tokenCount = splitTokens(tokenInput).length;

      return (
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <button
              type="button"
              onClick={() => setMethod("menu")}
              className="inline-flex items-center gap-1 text-sm text-stone-500 transition hover:text-stone-800"
            >
              <ArrowLeft className="size-4" />
              返回导入方式
            </button>
            <span className="text-xs text-stone-400">当前识别 {tokenCount} 个 Token</span>
          </div>
          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">Access Token 列表</label>
            <AntInput.TextArea
              placeholder="每行一个 Access Token..."
              value={tokenInput}
              onChange={(event) => setTokenInput(event.target.value)}
              rows={9}
            />
          </div>
          <div className="rounded-2xl border border-dashed border-stone-200 bg-stone-50 p-4">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="space-y-1">
                <div className="text-sm font-medium text-stone-800">从 TXT 文件导入</div>
                <div className="text-sm leading-6 text-stone-500">支持 `.txt`，文件内容也是一行一个 Token。</div>
              </div>
              <AntButton
                onClick={() => txtInputRef.current?.click()}
                disabled={isSubmitting}
                icon={<FileText className="size-4" />}
              >
                选择 TXT
              </AntButton>
            </div>
          </div>
          <input
            ref={txtInputRef}
            type="file"
            accept=".txt,text/plain"
            className="hidden"
            onChange={(event) => void handleTxtSelected(event)}
          />
        </div>
      );
    }

    if (method === "session") {
      return (
        <div className="space-y-4">
          <button
            type="button"
            onClick={() => setMethod("menu")}
            className="inline-flex items-center gap-1 text-sm text-stone-500 transition hover:text-stone-800"
          >
            <ArrowLeft className="size-4" />
            返回导入方式
          </button>
          <div className="rounded-2xl border border-stone-200 bg-stone-50 p-4 text-sm leading-6 text-stone-600">
            打开
            {" "}
            <a
              href={sessionUrl}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 font-medium text-stone-900 underline underline-offset-4"
            >
              {sessionUrl}
              <ExternalLink className="size-3.5" />
            </a>
            ，复制页面返回的完整 JSON，系统会自动提取其中的 `accessToken` 导入。
          </div>
          <div className="rounded-2xl border border-amber-200 bg-amber-50 p-4 text-sm leading-6 text-amber-900">
            <div className="font-medium">风险提示</div>
            <div>
              不要使用自己的大号，尽量使用不常用的小号进行导入，避免出现封号风险。本项目不承担任何封号风险责任。
            </div>
          </div>
          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">Session JSON</label>
            <AntInput.TextArea
              placeholder='粘贴完整 JSON，例如包含 "accessToken" 的对象...'
              value={sessionInput}
              onChange={(event) => setSessionInput(event.target.value)}
              rows={9}
              className="font-mono text-xs"
            />
          </div>
        </div>
      );
    }

    if (method === "oauth") {
      return (
        <div className="space-y-4">
          <button
            type="button"
            onClick={() => setMethod("menu")}
            className="inline-flex items-center gap-1 text-sm text-stone-500 transition hover:text-stone-800"
          >
            <ArrowLeft className="size-4" />
            返回导入方式
          </button>
          <div className="rounded-2xl border border-stone-200 bg-stone-50 p-4 text-sm leading-6 text-stone-600 space-y-2">
            <div className="font-medium text-stone-800">操作步骤</div>
            <ol className="list-decimal pl-5 space-y-1">
              <li>（可选）填写你 ChatGPT 账号的邮箱，登录页会预填。</li>
              <li>点击下方"打开授权页面"，在新标签里登录自己的 ChatGPT 账号。</li>
              <li>登录完成后浏览器会跳到 <code className="rounded bg-stone-200 px-1">platform.openai.com/auth/callback?code=...</code>。立刻从地址栏复制整段 URL（或开 F12 在 Network 里抓到 callback 那一行，右键 Copy → Copy URL）。</li>
              <li>把 callback URL 粘到下面输入框，点"完成导入"。</li>
            </ol>
          </div>
          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">邮箱（可选预填）</label>
            <AntInput
              type="email"
              placeholder="you@example.com"
              value={oauthEmailHint}
              onChange={(event) => setOauthEmailHint(event.target.value)}
              disabled={Boolean(oauthSession) || oauthStarting}
            />
          </div>
          {!oauthSession ? (
            <AntButton
              type="primary"
              onClick={() => void handleStartOAuth()}
              loading={oauthStarting}
              icon={!oauthStarting ? <ExternalLink className="size-4" /> : undefined}
            >
              打开授权页面
            </AntButton>
          ) : (
            <div className="space-y-3">
              <div className="rounded-2xl border border-stone-200 bg-white p-3 text-xs leading-6 text-stone-600 break-all font-mono">
                {oauthSession.authorize_url}
              </div>
              <div className="flex flex-wrap gap-2">
                <AntButton
                  onClick={() => void handleCopyAuthorizeUrl()}
                  icon={<Copy className="size-4" />}
                >
                  复制授权 URL
                </AntButton>
                <AntButton
                  onClick={() => window.open(oauthSession.authorize_url, "_blank", "noopener,noreferrer")}
                  icon={<ExternalLink className="size-4" />}
                >
                  再次打开
                </AntButton>
                <AntButton
                  onClick={() => {
                    setOauthSession(null);
                    setOauthCallbackInput("");
                  }}
                >
                  重新生成
                </AntButton>
              </div>
              <div className="space-y-2">
                <label className="text-sm font-medium text-stone-700">粘贴 callback URL（或仅 code）</label>
                <AntInput.TextArea
                  placeholder={"https://platform.openai.com/auth/callback?code=...&state=..."}
                  value={oauthCallbackInput}
                  onChange={(event) => setOauthCallbackInput(event.target.value)}
                  rows={4}
                  className="font-mono text-xs"
                />
              </div>
            </div>
          )}
          <div className="rounded-2xl border border-amber-200 bg-amber-50 p-4 text-sm leading-6 text-amber-900">
            <div className="font-medium">注意</div>
            <div>
              授权码（code）只能使用一次。如果浏览器的 callback 页加载完成、显示了 OpenAI 的错误页，那 code 大概率已经被消耗，
              请点击"重新生成"再走一次。整个流程在 10 分钟内完成即可。
            </div>
          </div>
        </div>
      );
    }

    if (method === "codex-auth") {
      return (
        <div className="space-y-4">
          <button
            type="button"
            onClick={() => setMethod("menu")}
            className="inline-flex items-center gap-1 text-sm text-stone-500 transition hover:text-stone-800"
          >
            <ArrowLeft className="size-4" />
            返回导入方式
          </button>
          <div className="space-y-2">
            <label className="text-sm font-medium text-stone-700">Codex 认证 JSON</label>
            <AntInput.TextArea
              placeholder='粘贴包含 "access_token"、"refresh_token"、"id_token" 的 Codex 认证 JSON...'
              value={codexAuthInput}
              onChange={(event) => setCodexAuthInput(event.target.value)}
              rows={10}
              className="font-mono text-xs"
            />
          </div>
        </div>
      );
    }

    return (
      <div>
        <div className="mb-6">
          <MethodCard
            title="OAuth 登录已有账号（带自动刷新）"
            description="用浏览器登录自己的 ChatGPT 账号，回填 callback URL 即可拿到 refresh_token，后台会自动续期。"
            icon={LogIn}
            onClick={() => setMethod("oauth")}
            featured
            tag="推荐"
          />
        </div>
        <div className="grid gap-x-5 gap-y-5 md:grid-cols-2">
          <MethodCard
            title="导入 Access Token"
            description="粘贴或读取 TXT，一行一个。"
            icon={KeyRound}
            onClick={() => setMethod("token")}
          />
          <MethodCard
            title="导入 Session JSON"
            description="从 session 接口复制 JSON，自动提取 accessToken。"
            icon={FileJson}
            onClick={() => setMethod("session")}
          />
          <MethodCard
            title="导入 Codex 认证 JSON"
            description="导入后账号来源标记为 codex。"
            icon={FileJson}
            onClick={() => setMethod("codex-auth")}
          />
        </div>
      </div>
    );
  };

  const footerDisabled = disabled || isSubmitting;

  return (
    <>
      <AntButton
          type="primary"
          icon={<Upload className="size-4" />}
          onClick={() => setOpen(true)}
          disabled={disabled}
        >
          导入
      </AntButton>
      <Modal
        title={
          method === "menu"
            ? "导入账户"
            : method === "token"
              ? "导入 Access Token"
              : method === "session"
                ? "导入 Session JSON"
                : method === "codex-auth"
                  ? "导入 Codex 认证 JSON"
                : method === "oauth"
                  ? "OAuth 登录已有账号"
                  : "导入账户"
        }
        open={open}
        onCancel={() => handleOpenChange(false)}
        width={760}
        styles={{ body: { paddingTop: 12 } }}
        footer={[
          <AntButton key="cancel" onClick={() => setOpen(false)} disabled={footerDisabled}>
            取消
          </AntButton>,
          method === "token" ? (
            <AntButton key="token" type="primary" onClick={() => void handleImportTokenText()} disabled={footerDisabled} loading={isSubmitting}>
              导入 Token
            </AntButton>
          ) : null,
          method === "session" ? (
            <AntButton key="session" type="primary" onClick={() => void handleImportSessionJson()} disabled={footerDisabled} loading={isSubmitting}>
              导入 JSON
            </AntButton>
          ) : null,
          method === "codex-auth" ? (
            <AntButton key="codex" type="primary" onClick={() => void handleImportCodexAuthJson()} disabled={footerDisabled} loading={isSubmitting}>
              导入 JSON
            </AntButton>
          ) : null,
          method === "oauth" && oauthSession ? (
            <AntButton key="oauth" type="primary" onClick={() => void handleFinishOAuth()} disabled={footerDisabled || !oauthCallbackInput.trim()} loading={isSubmitting}>
              完成导入
            </AntButton>
          ) : null,
        ].filter(Boolean)}
      >
        <div className="mb-5 rounded-xl border border-slate-100 bg-slate-50 px-4 py-3 text-sm leading-6 text-slate-500">
          {method === "menu"
            ? "选择一种导入方式。导入成功后会自动拉取邮箱、类型和额度。"
            : method === "token"
              ? "支持手动粘贴或从 TXT 文件导入，一行一个 Token。"
            : method === "session"
              ? "粘贴完整 Session JSON，系统会自动提取 accessToken。"
              : method === "codex-auth"
                ? "粘贴 Codex 认证 JSON，系统会按 codex 来源导入。"
                : method === "oauth"
                  ? "用浏览器跑一遍 OpenAI 标准 OAuth，拿回 refresh_token 后系统会自动续期。"
                  : "选择一种导入方式。"}
        </div>

        {renderMethodBody()}
      </Modal>
    </>
  );
}
