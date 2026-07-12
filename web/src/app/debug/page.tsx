"use client";

import { useEffect, useMemo, useState } from "react";
import { Button, Card, Input, Select, Space, Spin, Switch, Tag, Typography } from "antd";
import { Play, RotateCcw } from "lucide-react";
import { toast } from "sonner";

import { debugChatGPTWeb, fetchAccounts, type Account, type ChatGPTWebDebugResponse } from "@/lib/api";
import { useAuthGuard } from "@/lib/use-auth-guard";
import { cn } from "@/lib/utils";

const presets = [
  {
    label: "匿名模型列表",
    group: "模型",
    method: "GET",
    path: "/backend-anon/models?iim=false&is_gizmo=false",
    body: "",
  },
  {
    label: "登录模型列表",
    group: "模型",
    method: "GET",
    path: "/backend-api/models?history_and_training_disabled=false",
    body: "",
  },
  {
    label: "用户信息",
    group: "账号",
    method: "GET",
    path: "/backend-api/me",
    body: "",
  },
  {
    label: "账号套餐/额度检查",
    group: "账号",
    method: "GET",
    path: "/backend-api/accounts/check/v4-2023-04-27?timezone_offset_min=-480",
    body: "",
  },
  {
    label: "登录 Sentinel",
    group: "Sentinel",
    method: "POST",
    path: "/backend-api/sentinel/chat-requirements",
    body: JSON.stringify({ p: "需要由客户端 pow 生成，普通调试可先观察错误结构" }, null, 2),
  },
  {
    label: "匿名 Sentinel",
    group: "Sentinel",
    method: "POST",
    path: "/backend-anon/sentinel/chat-requirements",
    body: JSON.stringify({ p: "需要由客户端 pow 生成，普通调试可先观察错误结构" }, null, 2),
  },
  {
    label: "会话初始化",
    group: "会话",
    method: "POST",
    path: "/backend-api/conversation/init",
    body: JSON.stringify({ gizmo_id: null, requested_default_model: null }, null, 2),
  },
  {
    label: "最近会话列表",
    group: "会话",
    method: "GET",
    path: "/backend-api/conversations?offset=0&limit=10&order=updated&conversation_filter=all",
    body: "",
  },
  {
    label: "单个会话详情",
    group: "会话",
    method: "GET",
    path: "/backend-api/conversation/{conversation_id}",
    body: "",
  },
  {
    label: "异步任务",
    group: "生图任务",
    method: "GET",
    path: "/backend-api/tasks",
    body: "",
  },
  {
    label: "图片会话准备",
    group: "生图任务",
    method: "POST",
    path: "/backend-api/f/conversation/prepare",
    body: JSON.stringify({
      action: "next",
      fork_from_shared_post: false,
      parent_message_id: "{uuid}",
      model: "gpt-5",
      client_prepare_state: "success",
      timezone_offset_min: -480,
      timezone: "Asia/Shanghai",
      conversation_mode: { kind: "primary_assistant" },
      system_hints: ["picture_v2"],
      partial_query: {
        id: "{uuid}",
        author: { role: "user" },
        content: { content_type: "text", parts: ["测试提示词"] },
      },
      supports_buffering: true,
      supported_encodings: ["v1"],
      client_contextual_info: { app_name: "chatgpt.com" },
    }, null, 2),
  },
  {
    label: "文件上传创建",
    group: "文件",
    method: "POST",
    path: "/backend-api/files",
    body: JSON.stringify({ file_name: "debug.txt", file_size: 12, use_case: "multimodal" }, null, 2),
  },
  {
    label: "文件上传完成",
    group: "文件",
    method: "POST",
    path: "/backend-api/files/{file_id}/uploaded",
    body: JSON.stringify({ file_id: "{file_id}", upload_id: "{upload_id}" }, null, 2),
  },
  {
    label: "文件下载",
    group: "文件",
    method: "GET",
    path: "/backend-api/files/{file_id}/download",
    body: "",
  },
  {
    label: "Codex Responses",
    group: "Codex",
    method: "POST",
    path: "/backend-api/codex/responses",
    body: JSON.stringify({
      model: "gpt-5.5",
      input: "hello",
      stream: false,
    }, null, 2),
  },
] as const;

function pretty(value: unknown) {
  if (typeof value === "string") {
    return value;
  }
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function parseJsonObject(text: string, label: string) {
  const raw = text.trim();
  if (!raw) {
    return {};
  }
  const value = JSON.parse(raw);
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${label} 必须是 JSON 对象`);
  }
  return value as Record<string, unknown>;
}

function parseJsonBody(text: string) {
  const raw = text.trim();
  if (!raw) {
    return undefined;
  }
  return JSON.parse(raw);
}

function maskToken(token?: string) {
  if (!token) {
    return "匿名";
  }
  if (token.length <= 16) {
    return token;
  }
  return `${token.slice(0, 10)}...${token.slice(-6)}`;
}

function accountLabel(account: Account) {
  return account.email || `${account.type || "account"} · ${maskToken(account.access_token)}`;
}

function presetOptions() {
  const groups = Array.from(new Set(presets.map((item) => item.group)));
  return groups.map((group) => ({
    label: group,
    options: presets
      .filter((item) => item.group === group)
      .map((item) => ({ label: item.label, value: item.label })),
  }));
}

export default function DebugPage() {
  const { isCheckingAuth, session } = useAuthGuard(["admin"]);
  const [method, setMethod] = useState("GET");
  const [path, setPath] = useState("/backend-anon/models?iim=false&is_gizmo=false");
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [selectedToken, setSelectedToken] = useState("");
  const [isLoadingAccounts, setIsLoadingAccounts] = useState(false);
  const [headersText, setHeadersText] = useState("{}");
  const [bodyText, setBodyText] = useState("");
  const [timeoutSeconds, setTimeoutSeconds] = useState(30);
  const [bootstrap, setBootstrap] = useState(true);
  const [isRunning, setIsRunning] = useState(false);
  const [result, setResult] = useState<ChatGPTWebDebugResponse | null>(null);

  const selectedPreset = useMemo(
    () => presets.find((item) => item.method === method && item.path === path),
    [method, path],
  );

  const selectedAccount = useMemo(
    () => accounts.find((account) => account.access_token === selectedToken) || null,
    [accounts, selectedToken],
  );

  useEffect(() => {
    if (!session || session.role !== "admin") {
      return;
    }
    let cancelled = false;
    setIsLoadingAccounts(true);
    fetchAccounts()
      .then((data) => {
        if (!cancelled) {
          setAccounts(data.items || []);
        }
      })
      .catch((error) => {
        if (!cancelled) {
          toast.error(error instanceof Error ? error.message : "加载账号列表失败");
        }
      })
      .finally(() => {
        if (!cancelled) {
          setIsLoadingAccounts(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [session]);

  if (isCheckingAuth || !session || session.role !== "admin") {
    return (
      <div className="flex min-h-[calc(100vh-49px)] items-center justify-center">
        <Spin />
      </div>
    );
  }

  const applyPreset = (label: string) => {
    const preset = presets.find((item) => item.label === label);
    if (!preset) {
      return;
    }
    setMethod(preset.method);
    setPath(preset.path);
    setBodyText(preset.body);
    setResult(null);
  };

  const runRequest = async () => {
    setIsRunning(true);
    try {
      const headers = parseJsonObject(headersText, "Headers");
      const body = parseJsonBody(bodyText);
      const data = await debugChatGPTWeb({
        method,
        path,
        access_token: selectedToken,
        headers: headers as Record<string, string>,
        body,
        timeout_seconds: timeoutSeconds,
        bootstrap,
      });
      setResult(data);
      toast.success(`HTTP ${data.status} · ${data.elapsed_ms}ms`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "调试请求失败");
    } finally {
      setIsRunning(false);
    }
  };

  return (
    <section className="space-y-4">
      <Card>
        <Typography.Text type="secondary" className="text-xs font-semibold uppercase tracking-[0.18em]">
          ChatGPT Web Backend
        </Typography.Text>
        <Typography.Title level={3} className="!mb-0 !mt-1">
          Web 后端接口调试
        </Typography.Title>
        <Typography.Text type="secondary">
          通过服务端指纹、代理和 ChatGPT Web headers 调试 /backend-api 与 /backend-anon 接口。
        </Typography.Text>
      </Card>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,520px)_minmax(0,1fr)]">
        <Card title="请求">
          <div className="space-y-4">
            <div className="space-y-2">
              <label className="text-sm font-medium text-slate-700">快捷接口</label>
              <Select
                className="w-full"
                value={selectedPreset?.label}
                placeholder="选择常用 ChatGPT Web 接口"
                onChange={applyPreset}
                options={presetOptions()}
              />
            </div>

            <div className="grid gap-3 sm:grid-cols-[120px_minmax(0,1fr)]">
              <div className="space-y-2">
                <label className="text-sm font-medium text-slate-700">方法</label>
                <Select
                  className="w-full"
                  value={method}
                  onChange={setMethod}
                  options={["GET", "POST", "PUT", "PATCH", "DELETE"].map((item) => ({ label: item, value: item }))}
                />
              </div>
              <div className="space-y-2">
                <label className="text-sm font-medium text-slate-700">路径</label>
                <Input
                  value={path}
                  onChange={(event) => setPath(event.target.value)}
                  placeholder="/backend-api/me"
                />
                {path.includes("{") ? (
                  <div className="text-xs text-amber-600">路径包含占位符，请先替换后再发送。</div>
                ) : null}
              </div>
            </div>

            <div className="space-y-2">
              <label className="text-sm font-medium text-slate-700">账号</label>
              <Select
                showSearch
                className="w-full"
                value={selectedToken}
                loading={isLoadingAccounts}
                placeholder="选择当前号池账号，或使用匿名链路"
                optionFilterProp="label"
                onChange={setSelectedToken}
                options={[
                  { label: "匿名链路", value: "" },
                  ...accounts.map((account) => ({
                    label: `${accountLabel(account)} · ${account.type || "free"} · ${account.status}`,
                    value: account.access_token,
                  })),
                ]}
              />
              <div className="text-xs text-slate-400">
                {selectedAccount
                  ? `${selectedAccount.status} · ${selectedAccount.type || "free"} · ${maskToken(selectedAccount.access_token)}`
                  : "匿名链路会走 /backend-anon；/backend-api 通常需要选择账号。"}
              </div>
            </div>

            <div className="grid gap-3 sm:grid-cols-2">
              <div className="space-y-2">
                <label className="text-sm font-medium text-slate-700">超时秒数</label>
                <Input
                  type="number"
                  min={1}
                  max={120}
                  value={timeoutSeconds}
                  onChange={(event) => setTimeoutSeconds(Math.max(1, Math.min(120, Number(event.target.value) || 30)))}
                />
              </div>
              <div className="flex items-end justify-between rounded-md border border-slate-200 px-3 py-2">
                <span className="text-sm font-medium text-slate-700">先加载首页指纹</span>
                <Switch checked={bootstrap} onChange={setBootstrap} />
              </div>
            </div>

            <div className="space-y-2">
              <label className="text-sm font-medium text-slate-700">额外 Headers JSON</label>
              <Input.TextArea
                value={headersText}
                onChange={(event) => setHeadersText(event.target.value)}
                autoSize={{ minRows: 4, maxRows: 8 }}
                className="font-mono text-xs"
              />
            </div>

            <div className="space-y-2">
              <label className="text-sm font-medium text-slate-700">Body JSON</label>
              <Input.TextArea
                value={bodyText}
                onChange={(event) => setBodyText(event.target.value)}
                autoSize={{ minRows: 8, maxRows: 16 }}
                className="font-mono text-xs"
                placeholder="{ }"
              />
            </div>

            <Space wrap>
              <Button type="primary" icon={<Play className="size-4" />} loading={isRunning} onClick={() => void runRequest()}>
                发送请求
              </Button>
              <Button
                icon={<RotateCcw className="size-4" />}
                onClick={() => {
                  setHeadersText("{}");
                  setBodyText("");
                  setResult(null);
                }}
              >
                清空
              </Button>
            </Space>
          </div>
        </Card>

        <Card
          title="响应"
          extra={
            result ? (
              <Space>
                <Tag color={result.ok ? "success" : "error"}>HTTP {result.status}</Tag>
                <Tag>{result.elapsed_ms}ms</Tag>
              </Space>
            ) : null
          }
        >
          {result ? (
            <div className="space-y-3">
              <div className="rounded-md border border-slate-100 bg-slate-50 px-3 py-2 text-xs text-slate-500">
                <div className="font-mono text-slate-700">{result.method} {result.url}</div>
              </div>
              <div className="grid gap-3 lg:grid-cols-2">
                <pre className="max-h-56 overflow-auto rounded-md bg-slate-950 p-3 text-xs text-slate-100">
                  {pretty(result.request_headers)}
                </pre>
                <pre className="max-h-56 overflow-auto rounded-md bg-slate-950 p-3 text-xs text-slate-100">
                  {pretty(result.response_headers)}
                </pre>
              </div>
              <pre className={cn("min-h-[420px] overflow-auto rounded-md p-4 text-xs", result.ok ? "bg-slate-950 text-slate-100" : "bg-rose-950 text-rose-50")}>
                {pretty(result.body)}
              </pre>
            </div>
          ) : (
            <div className="flex min-h-[520px] items-center justify-center rounded-md border border-dashed border-slate-200 text-sm text-slate-400">
              发送请求后在这里查看 ChatGPT Web 后端响应。
            </div>
          )}
        </Card>
      </div>
    </section>
  );
}
