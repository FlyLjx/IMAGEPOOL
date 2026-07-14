import { httpRequest, request } from "@/lib/request";

export type AccountType = string;
export type AccountStatus = "正常" | "限流" | "异常" | "失效" | "恢复中" | "禁用";
export type ImageModel = string;
export type AuthRole = "admin" | "user";
export type ProxyRuntimeEgressMode = "direct" | "single_proxy";
export type ProxyRuntimeClearanceMode = "none" | "manual" | "flaresolverr";

export type BarkNotificationSettings = {
  enabled: boolean;
  server_url: string;
  device_key: string;
  title_prefix: string;
  group: string;
  level: "active" | "timeSensitive" | "passive" | "critical" | string;
  timeout_secs: number | string;
  min_interval_seconds: number | string;
  notify_failed_calls: boolean;
  notify_register: boolean;
  notify_register_errors_only: boolean;
  notify_auto_refill: boolean;
};

export type NotificationSettings = {
  bark: BarkNotificationSettings;
};

export type ProxyRuntimeSettings = {
  enabled: boolean;
  egress_mode: ProxyRuntimeEgressMode;
  proxy_url: string;
  resource_proxy_url: string;
  skip_ssl_verify: boolean;
  reset_session_status_codes: number[];
  clearance: {
    enabled: boolean;
    mode: ProxyRuntimeClearanceMode;
    cf_cookies: string;
    cf_clearance: string;
    has_cf_cookies?: boolean;
    has_cf_clearance?: boolean;
    user_agent: string;
    browser: string;
    flaresolverr_url: string;
    timeout_sec: number | string;
    refresh_interval: number | string;
    warm_up_on_start: boolean;
  };
};

export type ProxyRuntimeStatus = {
  enabled: boolean;
  egress_mode: string;
  proxy_source: string;
  has_proxy: boolean;
  clearance_enabled: boolean;
  clearance_mode: string;
  has_clearance_bundle: boolean;
  cached_clearance_hosts: string[];
};

export type ClearanceTestResult = {
  ok: boolean;
  status: string;
  latency_ms: number;
  has_cookies: boolean;
  user_agent: string;
  error: string | null;
  runtime: ProxyRuntimeStatus;
};

export type Account = {
  access_token: string;
  type: AccountType;
  source_type?: string | null;
  status: AccountStatus;
  quota: number;
  image_quota_total?: number;
  usage?: unknown;
  image_quota_unknown?: boolean;
  email?: string | null;
  has_password?: boolean;
  user_id?: string | null;
  limits_progress?: Array<{
    feature_name?: string;
    remaining?: number;
    reset_after?: string;
  }>;
  default_model_slug?: string | null;
  restore_at?: string | null;
  success: number;
  fail: number;
  last_used_at?: string | null;
  proxy?: string | null;
  consecutive_failures?: number;
  last_error_type?: string | null;
  cooldown_until?: string | null;
  last_success_at?: string | null;
  last_failure_at?: string | null;
  dispatch_score?: number;
  health_score?: number;
  health_label?: string;
  health_reasons?: string[];
  cooldown_active?: boolean;
  proxy_cooldown_active?: boolean;
  recent_success?: number;
  recent_total?: number;
  recent_success_rate?: number | null;
  proxy_stats?: {
    success?: number;
    fail?: number;
    last_error_type?: string | null;
    cooldown_until?: string | null;
    last_success_at?: string | null;
    last_failure_at?: string | null;
  };
  recent_results?: Array<{
    time?: string;
    ok?: boolean;
    kind?: string;
    error_type?: string;
  }>;
};

export type AccountImportPayload = {
  access_token: string;
  accessToken?: string;
  type?: string;
  export_type?: string;
  source_type?: string;
  [key: string]: unknown;
};

export type Model = {
  id: string;
  object: string;
  created: number;
  owned_by: string;
  permission: unknown[];
  root: string;
  parent: string | null;
};

export type ChatGPTWebDebugPayload = {
  method: string;
  path: string;
  access_token?: string;
  headers?: Record<string, string>;
  body?: unknown;
  timeout_seconds?: number;
  bootstrap?: boolean;
};

export type ChatGPTWebDebugResponse = {
  ok: boolean;
  status: number;
  elapsed_ms: number;
  method: string;
  url: string;
  request_headers?: Record<string, unknown>;
  response_headers?: Record<string, string>;
  body: unknown;
};

type AccountListResponse = {
  items: Account[];
};

type ModelListResponse = {
  object: string;
  data: Model[];
};

type AccountMutationResponse = {
  items: Account[];
  added?: number;
  skipped?: number;
  removed?: number;
  refreshed?: number;
  relogined?: number;
  errors?: Array<{ access_token: string; error: string }>;
};

export type AccountRefreshResponse = {
  items: Account[];
  refreshed: number;
  relogined?: number;
  errors: Array<{ access_token: string; error: string }>;
};

export type RefreshProgressResponse = {
  total: number;
  processed: number;
  done: boolean;
  error: string | null;
  status_counts?: Record<string, number>;
  total_quota?: number;
  result?: AccountRefreshResponse | null;
  results?: Array<{ token: string; email?: string; status: string; quota?: number; error?: string | null }>;
};

type AccountUpdateResponse = {
  item: Account;
  items: Account[];
};

type AccountImageTestResponse = {
  ok: boolean;
  created?: number;
  image_count?: number;
  task_id?: string;
  error?: string;
  items: Account[];
};

export type SettingsConfig = {
  api_keys?: string[];
  proxy: string;
  base_url?: string;
  timezone?: string;
  global_system_prompt?: string;
  sensitive_words?: string[];
  ai_review?: {
    enabled?: boolean;
    base_url?: string;
    api_key?: string;
    model?: string;
    prompt?: string;
  };
  refresh_account_interval_minute?: number | string;
  refresh_account_concurrency?: number | string;
  image_retention_days?: number | string;
  image_poll_timeout_secs?: number | string;
  image_task_timeout_secs?: number | string;
  image_web_model_slug?: string;
  image_account_concurrency?: number | string;
  image_account_max_inflight_per_account?: number | string;
  image_account_precheck_interval_minutes?: number | string;
  image_account_precheck_concurrency?: number | string;
  image_account_precheck_timeout_secs?: number | string;
  image_parallel_generation?: boolean;
  image_settle_enabled?: boolean;
  image_check_before_hit_enabled?: boolean;
  image_settle_secs?: number | string;
  image_timeout_retry_secs?: number | string;
  auto_remove_invalid_accounts?: boolean;
  auto_remove_rate_limited_accounts?: boolean;
  auto_relogin_after_refresh?: boolean;
  auto_refill_enabled?: boolean;
  auto_refill_threshold_percent?: number | string;
  auto_refill_target_available?: number | string;
  auto_refill_interval_minutes?: number | string;
  log_levels?: string[];
  notifications?: NotificationSettings;
  proxy_runtime?: ProxyRuntimeSettings;
  [key: string]: unknown;
};

export type ManagedImage = {
  rel: string;
  path?: string;
  name: string;
  date: string;
  size: number;
  url: string;
  thumbnail_url?: string;
  created_at: string;
  width?: number;
  height?: number;
  tags?: string[];
};

export type SystemLog = {
  id: string;
  time: string;
  type: "call" | "account" | string;
  summary?: string;
  detail?: Record<string, unknown>;
  [key: string]: unknown;
};

export type ImageResponse = {
  created: number;
  data: Array<{ b64_json?: string; url?: string; revised_prompt?: string }>;
};

export type SystemUpdateStatus = {
  enabled: boolean;
  updating: boolean;
  target_version?: string;
  last_error?: string;
};

export type CredentialRecoveryLog = {
  id: string;
  time: string;
  level: "processing" | "success" | "warning" | "error" | string;
  event: string;
  account_email?: string;
  attempt?: number;
  message: string;
  error?: string;
};

export type ImageTaskStatusLog = {
  time: string;
  level?: "info" | "processing" | "success" | "warning" | "error" | string;
  event?: string;
  progress?: string;
  message: string;
  details?: Record<string, unknown>;
};

export type ImageTask = {
  id: string;
  status: "queued" | "running" | "success" | "error";
  mode: "generate" | "edit";
  prompt?: string;
  model?: ImageModel;
  size?: string;
  quality?: string;
  created_at: string;
  updated_at: string;
  conversation_id?: string;
  data?: Array<{ b64_json?: string; url?: string; revised_prompt?: string }>;
  error?: string;
  progress?: string;
  progress_percent?: number;
  realtime_status?: string;
  status_log_count?: number;
  status_logs?: ImageTaskStatusLog[];
  image_route_attempt_count?: number;
  used_account_count?: number;
  failed_account_count?: number;
  client_retry_count?: number;
  run_count?: number;
  elapsed_secs?: number;
  duration_ms?: number;
};

type ImageTaskListResponse = {
  items: ImageTask[];
  missing_ids: string[];
};

export type LoginResponse = {
  ok: boolean;
  version: string;
  role: AuthRole;
  subject_id: string;
  name: string;
};

export type UserKey = {
  id: string;
  name: string;
  role: "user";
  enabled: boolean;
  created_at: string | null;
  last_used_at: string | null;
  limits: {
    daily_requests: number;
    daily_images: number;
    allowed_models: string[];
    allowed_endpoints: string[];
  };
  usage: {
    date: string;
    requests: number;
    images: number;
  };
};

export type RegisterConfig = {
  enabled: boolean;
  mail: {
    request_timeout: number;
    wait_timeout: number;
    wait_interval: number;
    providers: Array<Record<string, unknown>>;
  };
  proxy: string;
  flaresolverr?: {
    enabled?: boolean;
    url?: string;
    max_timeout_ms?: number;
    preload?: boolean;
  };
  total: number;
  threads: number;
  mode: "total" | "quota" | "available";
  target_quota: number;
  target_available: number;
  check_interval: number;
  stats: {
    job_id?: string;
    success: number;
    fail: number;
    done: number;
    running: number;
    threads: number;
    elapsed_seconds?: number;
    avg_seconds?: number;
    success_rate?: number;
    current_quota?: number;
    current_available?: number;
    started_at?: string;
    updated_at?: string;
    finished_at?: string;
  };
  logs?: Array<{
    time: string;
    text: string;
    level: string;
  }>;
};

export type SystemLoad = {
  sampled_at: string;
  cpu: {
    usage_percent: number;
    cores: number;
    load_1: number;
    load_5: number;
    load_15: number;
  };
  memory: {
    total_bytes: number;
    used_bytes: number;
    available_bytes: number;
    usage_percent: number;
  };
  disk: {
    path: string;
    total_bytes: number;
    used_bytes: number;
    available_bytes: number;
    usage_percent: number;
  };
  network: {
    received_bytes: number;
    sent_bytes: number;
    receive_bytes_per_second: number;
    send_bytes_per_second: number;
  };
};

export type DashboardSummary = {
  version: string;
  generated_at: string;
  system: SystemLoad;
  storage: {
    backend: {
      type?: string;
      db_type?: string;
      description?: string;
      database_url?: string;
      [key: string]: unknown;
    };
    health: {
      status?: string;
      backend?: string;
      account_count?: number;
      auth_key_count?: number;
      error?: string;
      [key: string]: unknown;
    };
  };
  accounts: {
    total: number;
    cumulative_total?: number;
    active: number;
    limited: number;
    abnormal: number;
    disabled: number;
    cooling: number;
    total_quota: number;
    unlimited_quota_count: number;
    total_success: number;
    total_fail: number;
    recent_success_rate?: number | null;
    by_type: Record<string, number>;
    by_error_type: Record<string, number>;
    proxy_stats: {
      accounts: number;
      success: number;
      fail: number;
      cooling: number;
      by_error_type: Record<string, number>;
    };
  };
  auth_keys: {
    users: number;
    enabled_users: number;
  };
  calls: {
    date: string;
    total: number;
    by_status: Record<string, number>;
    by_endpoint: Record<string, number>;
    by_model: Record<string, number>;
    runtime?: {
      window_minutes: number;
      bucket_minutes: number;
      start_time: string;
      end_time: string;
      total: number;
      success_rate: number;
      error_rate: number;
      totals: {
        success: number;
        failed: number;
        running: number;
        other: number;
      };
      series: Array<{
        time: string;
        label: string;
        success: number;
        failed: number;
      }>;
      status_pie: Array<{
        label: string;
        value: number;
        status: "success" | "failed" | "running" | "other" | string;
      }>;
      error_reasons: Array<{
        label: string;
        value: number;
      }>;
    };
    recent_failed: Array<{
      id?: string;
      time?: string;
      summary?: string;
      endpoint?: string;
      model?: string;
      error?: string;
      account_email?: string;
    }>;
  };
  tasks: {
    total: number;
    by_status: Record<string, number>;
    by_mode: Record<string, number>;
    recent: ImageTask[];
  };
};

export async function login(authKey: string) {
  const normalizedAuthKey = String(authKey || "").trim();
  return httpRequest<LoginResponse>("/auth/login", {
    method: "POST",
    body: {},
    headers: {
      Authorization: `Bearer ${normalizedAuthKey}`,
    },
    redirectOnUnauthorized: false,
  });
}

export async function fetchAccounts() {
  return httpRequest<AccountListResponse>("/api/accounts");
}

export async function fetchCredentialRecoveryLogs(filters: { email?: string; limit?: number } = {}) {
  const params = new URLSearchParams();
  if (filters.email) params.set("email", filters.email);
  if (filters.limit) params.set("limit", String(filters.limit));
  return httpRequest<{ items: CredentialRecoveryLog[] }>(`/api/accounts/recovery-logs${params.toString() ? `?${params.toString()}` : ""}`);
}

export async function fetchModels() {
  return httpRequest<ModelListResponse>("/v1/models");
}

export async function fetchDashboard(runtimeWindowMinutes = 60) {
  return httpRequest<DashboardSummary>(`/api/dashboard?runtime_window_minutes=${encodeURIComponent(String(runtimeWindowMinutes))}`);
}

export async function fetchSystemLoad() {
  return httpRequest<SystemLoad>("/api/system/load");
}

export async function debugChatGPTWeb(payload: ChatGPTWebDebugPayload) {
  return httpRequest<ChatGPTWebDebugResponse>("/api/debug/chatgpt-web", {
    method: "POST",
    body: payload,
  });
}

export async function createAccounts(tokens: string[], accounts: AccountImportPayload[] = []) {
  return httpRequest<AccountMutationResponse>("/api/accounts", {
    method: "POST",
    body: { tokens, accounts },
  });
}

export type OAuthLoginStartResponse = {
  session_id: string;
  authorize_url: string;
  expires_in: string;
  redirect_uri_prefix: string;
};

export async function startOAuthLogin(emailHint?: string) {
  return httpRequest<OAuthLoginStartResponse>("/api/accounts/oauth/start", {
    method: "POST",
    body: { email_hint: emailHint ?? "" },
  });
}

export async function finishOAuthLogin(sessionId: string, callback: string) {
  return httpRequest<AccountMutationResponse>("/api/accounts/oauth/finish", {
    method: "POST",
    body: { session_id: sessionId, callback },
  });
}

export async function deleteAccounts(tokens: string[]) {
  return httpRequest<AccountMutationResponse>("/api/accounts", {
    method: "DELETE",
    body: { tokens },
  });
}

export async function refreshAccounts(accessTokens: string[]) {
  return httpRequest<{ progress_id: string }>("/api/accounts/refresh", {
    method: "POST",
    body: { access_tokens: accessTokens },
  });
}

export async function fetchRefreshProgress(progressId: string) {
  return httpRequest<RefreshProgressResponse>(`/api/accounts/refresh/progress/${progressId}`);
}

export async function reLoginAccounts(accessTokens: string[]) {
  return httpRequest<{ progress_id: string }>("/api/accounts/re-login", {
    method: "POST",
    body: { access_tokens: accessTokens },
  });
}

export async function fetchReLoginProgress(progressId: string) {
  return httpRequest<RefreshProgressResponse>(`/api/accounts/re-login/progress/${progressId}`);
}

export async function updateAccount(
  accessToken: string,
  updates: {
    type?: AccountType;
    status?: AccountStatus;
    quota?: number;
    proxy?: string;
    email?: string;
    password?: string;
  },
) {
  return httpRequest<AccountUpdateResponse>("/api/accounts/update", {
    method: "POST",
    body: {
      access_token: accessToken,
      ...updates,
    },
  });
}

export async function testAccountImage(accessToken: string) {
  return httpRequest<AccountImageTestResponse>("/api/accounts/test-image", {
    method: "POST",
    body: { access_token: accessToken },
  });
}

export async function generateImage(prompt: string, model?: ImageModel, size?: string, quality = "auto") {
  return httpRequest<ImageResponse>(
    "/v1/images/generations",
    {
      method: "POST",
      body: {
        prompt,
        ...(model ? { model } : {}),
        ...(size ? { size } : {}),
        quality,
        n: 1,
        response_format: "b64_json",
      },
    },
  );
}

export async function editImage(files: File | File[], prompt: string, model?: ImageModel, size?: string, quality = "auto") {
  const formData = new FormData();
  const uploadFiles = Array.isArray(files) ? files : [files];

  uploadFiles.forEach((file) => {
    formData.append("image", file);
  });
  formData.append("prompt", prompt);
  if (model) {
    formData.append("model", model);
  }
  if (size) {
    formData.append("size", size);
  }
  formData.append("quality", quality);
  formData.append("n", "1");

  return httpRequest<ImageResponse>(
    "/v1/images/edits",
    {
      method: "POST",
      body: formData,
    },
  );
}

export async function createImageGenerationTask(clientTaskId: string, prompt: string, model?: ImageModel, size?: string, quality = "auto") {
  return httpRequest<ImageTask>("/api/image-tasks/generations", {
    method: "POST",
    body: {
      client_task_id: clientTaskId,
      prompt,
      ...(model ? { model } : {}),
      ...(size ? { size } : {}),
      quality,
    },
  });
}

export async function createImageEditTask(
  clientTaskId: string,
  files: File | File[],
  prompt: string,
  model?: ImageModel,
  size?: string,
  quality = "auto",
) {
  const formData = new FormData();
  const uploadFiles = Array.isArray(files) ? files : [files];

  uploadFiles.forEach((file) => {
    formData.append("image", file);
  });
  formData.append("client_task_id", clientTaskId);
  formData.append("prompt", prompt);
  if (model) {
    formData.append("model", model);
  }
  if (size) {
    formData.append("size", size);
  }
  formData.append("quality", quality);

  return httpRequest<ImageTask>("/api/image-tasks/edits", {
    method: "POST",
    body: formData,
  });
}

export async function fetchImageTasks(ids: string[]) {
  const params = new URLSearchParams();
  if (ids.length > 0) {
    params.set("ids", ids.join(","));
  }
  params.set("compact", "1");
  params.set("_t", String(Date.now()));
  return httpRequest<ImageTaskListResponse>(`/api/image-tasks?${params.toString()}`);
}

export async function fetchImageTaskStatus(taskId: string) {
  const params = new URLSearchParams({ _t: String(Date.now()) });
  return httpRequest<ImageTask>(`/api/image-tasks/${encodeURIComponent(taskId)}/status?${params.toString()}`);
}

export async function resumeImagePoll(taskId: string, extraTimeoutSecs = 30) {
  return httpRequest<ImageTask>(`/api/image-tasks/${encodeURIComponent(taskId)}/resume-poll`, {
    method: "POST",
    body: { extra_timeout_secs: extraTimeoutSecs },
  });
}

export async function cancelImageTask(taskId: string) {
  return httpRequest<ImageTask>(`/api/image-tasks/${encodeURIComponent(taskId)}/cancel`, {
    method: "POST",
    body: {},
  });
}

export async function fetchSettingsConfig() {
  return httpRequest<{ config: SettingsConfig }>("/api/settings");
}

export async function fetchSystemUpdateStatus() {
  return httpRequest<SystemUpdateStatus>("/api/system/update");
}

export async function startSystemUpdate(version: string) {
  return httpRequest<{ started: boolean; update: SystemUpdateStatus }>("/api/system/update", {
    method: "POST",
    body: { version },
  });
}

export async function updateSettingsConfig(settings: Partial<SettingsConfig>) {
  return httpRequest<{ config: SettingsConfig }>("/api/settings", {
    method: "POST",
    body: settings,
  });
}

export async function testBarkNotification() {
  return httpRequest<{ result: { ok: boolean; status: number; latency_ms?: number; error?: string } }>("/api/notifications/bark/test", {
    method: "POST",
    body: {},
  });
}

export async function fetchManagedImages(filters: { start_date?: string; end_date?: string }) {
  const params = new URLSearchParams();
  if (filters.start_date) params.set("start_date", filters.start_date);
  if (filters.end_date) params.set("end_date", filters.end_date);
  return httpRequest<{ items: ManagedImage[]; groups: Array<{ date: string; items: ManagedImage[] }> }>(
    `/api/images${params.toString() ? `?${params.toString()}` : ""}`,
  );
}

export async function deleteManagedImages(body: { paths?: string[]; start_date?: string; end_date?: string; all_matching?: boolean }) {
  return httpRequest<{ removed: number }>("/api/images/delete", { method: "POST", body });
}

export async function downloadImages(paths: string[]) {
  const response = await request.post("/api/images/download", { paths }, { responseType: "blob" });
  const blob = response.data as Blob;
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = "images.zip";
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

export async function downloadSingleImage(path: string) {
  const response = await request.get(`/api/images/download/${path}`, { responseType: "blob" });
  const blob = response.data as Blob;
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = path.split("/").pop() || "image.png";
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

export async function fetchImageTags() {
  return httpRequest<{ tags: string[] }>("/api/images/tags");
}

export async function setImageTags(path: string, tags: string[]) {
  return httpRequest<{ ok: boolean; tags: string[] }>("/api/images/tags", {
    method: "POST",
    body: { path, tags },
  });
}

export async function deleteImageTag(tag: string) {
  return httpRequest<{ ok: boolean; removed_from: number }>(`/api/images/tags/${encodeURIComponent(tag)}`, {
    method: "DELETE",
  });
}

export type ImageStorageStats = {
  disk_total_mb: number; disk_used_mb: number; disk_free_mb: number;
  image_count: number; image_size_mb: number; image_size_bytes: number;
};

export async function fetchImageStorage() {
  return httpRequest<ImageStorageStats>("/api/images/storage");
}

export async function compressAllImages() {
  return httpRequest<{ compressed: number; saved_bytes: number; saved_mb: number }>("/api/images/storage/compress", { method: "POST" });
}

export async function deleteToTarget(targetFreeMb: number) {
  return httpRequest<{ removed: number; freed_mb: number; done: boolean }>(
    `/api/images/storage/cleanup-to-target?target_free_mb=${targetFreeMb}&dry_run=false`,
    { method: "POST" },
  );
}

export async function fetchSystemLogs(filters: { type?: string; start_date?: string; end_date?: string }) {
  const params = new URLSearchParams();
  if (filters.type) params.set("type", filters.type);
  if (filters.start_date) params.set("start_date", filters.start_date);
  if (filters.end_date) params.set("end_date", filters.end_date);
  return httpRequest<{ items: SystemLog[] }>(`/api/logs${params.toString() ? `?${params.toString()}` : ""}`);
}

export async function deleteSystemLogs(ids: string[]) {
  return httpRequest<{ removed: number }>("/api/logs/delete", {
    method: "POST",
    body: { ids },
  });
}

export async function fetchUserKeys() {
  return httpRequest<{ items: UserKey[] }>("/api/auth/users");
}

export async function createUserKey(name: string) {
  return httpRequest<{ item: UserKey; key: string; items: UserKey[] }>("/api/auth/users", {
    method: "POST",
    body: { name },
  });
}

export async function updateUserKey(
  keyId: string,
  updates: {
    enabled?: boolean;
    name?: string;
    key?: string;
    limits?: UserKey["limits"];
  },
) {
  return httpRequest<{ item: UserKey; items: UserKey[] }>(`/api/auth/users/${keyId}`, {
    method: "POST",
    body: updates,
  });
}

export async function deleteUserKey(keyId: string) {
  return httpRequest<{ items: UserKey[] }>(`/api/auth/users/${keyId}`, {
    method: "DELETE",
  });
}

export async function fetchRegisterConfig() {
  return httpRequest<{ register: RegisterConfig }>("/api/register");
}

export async function updateRegisterConfig(updates: Partial<RegisterConfig>) {
  return httpRequest<{ register: RegisterConfig }>("/api/register", {
    method: "POST",
    body: updates,
  });
}

export async function startRegister() {
  return httpRequest<{ register: RegisterConfig }>("/api/register/start", { method: "POST" });
}

export async function stopRegister() {
  return httpRequest<{ register: RegisterConfig }>("/api/register/stop", { method: "POST" });
}

export async function resetRegister() {
  return httpRequest<{ register: RegisterConfig }>("/api/register/reset", { method: "POST" });
}

export type ProxyTestResult = {
  ok: boolean;
  status: number;
  latency_ms: number;
  proxy_source?: string;
  has_proxy?: boolean;
  exit_ip?: {
    ip?: string;
    country?: string;
    region?: string;
    city?: string;
    org?: string;
    timezone?: string;
  };
  chatgpt?: {
    ok: boolean;
    status: number;
    latency_ms: number;
    url?: string;
    error?: string | null;
  };
  urllib_chatgpt?: {
    ok: boolean;
    status: number;
    latency_ms: number;
    url?: string;
    error?: string | null;
  };
  error: string | null;
};

export async function testProxy(url?: string) {
  return httpRequest<{ result: ProxyTestResult }>("/api/proxy/test", {
    method: "POST",
    body: { url: url ?? "" },
  });
}

export async function fetchProxyRuntime() {
  return httpRequest<{ runtime: ProxyRuntimeSettings; status: ProxyRuntimeStatus }>("/api/proxy/runtime");
}

export async function updateProxyRuntime(runtime: ProxyRuntimeSettings) {
  return httpRequest<{ runtime: ProxyRuntimeSettings; status: ProxyRuntimeStatus }>("/api/proxy/runtime", {
    method: "POST",
    body: runtime,
  });
}

export async function testProxyClearance(targetUrl = "https://chatgpt.com") {
  return httpRequest<{ result: ClearanceTestResult }>("/api/proxy/clearance/test", {
    method: "POST",
    body: { target_url: targetUrl },
  });
}

export async function resetOutlookPool(scope: "all" | "failed" | "unused" = "all") {
  return httpRequest<{ register: RegisterConfig }>("/api/register/outlook-pool/reset", {
    method: "POST",
    body: { scope },
  });
}
