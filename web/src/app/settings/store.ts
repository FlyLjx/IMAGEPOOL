"use client";

import { create } from "zustand";
import { toast } from "sonner";

import {
  fetchRegisterConfig,
  fetchSettingsConfig,
  resetOutlookPool as resetOutlookPoolApi,
  resetRegister as resetRegisterApi,
  startRegister,
  stopRegister,
  testBarkNotification,
  updateRegisterConfig,
  updateSettingsConfig,
  type BarkNotificationSettings,
  type ProxyRuntimeSettings,
  type RegisterConfig,
  type SettingsConfig,
} from "@/lib/api";

function defaultProxyRuntime(): ProxyRuntimeSettings {
  return {
    enabled: false,
    egress_mode: "direct",
    proxy_url: "",
    resource_proxy_url: "",
    skip_ssl_verify: false,
    reset_session_status_codes: [403],
    clearance: {
      enabled: false,
      mode: "none",
      cf_cookies: "",
      cf_clearance: "",
      user_agent:
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
      browser: "chrome",
      flaresolverr_url: "",
      timeout_sec: 60,
      refresh_interval: 3600,
      warm_up_on_start: false,
    },
  };
}

function defaultBark(): BarkNotificationSettings {
  return {
    enabled: false,
    server_url: "https://api.day.app",
    device_key: "",
    title_prefix: "IMAGE POOL",
    group: "image-pool",
    level: "active",
    timeout_secs: 10,
    min_interval_seconds: 60,
    notify_failed_calls: true,
    notify_register: true,
    notify_register_errors_only: false,
    notify_auto_refill: true,
  };
}

function normalizeConfig(config: SettingsConfig): SettingsConfig {
  const proxyRuntime =
    typeof config.proxy_runtime === "object" && config.proxy_runtime
      ? (config.proxy_runtime as ProxyRuntimeSettings)
      : defaultProxyRuntime();
  const bark =
    typeof config.notifications?.bark === "object" && config.notifications.bark
      ? (config.notifications.bark as BarkNotificationSettings)
      : defaultBark();

  return {
    ...config,
    refresh_account_interval_minute: Number(config.refresh_account_interval_minute || 60),
    refresh_account_concurrency: Number(config.refresh_account_concurrency || 20),
    image_retention_days: Number(config.image_retention_days || 30),
    image_poll_timeout_secs: Math.min(180, Number(config.image_poll_timeout_secs) || 180),
    image_task_timeout_secs: 300,
    image_capacity_burst_parallel: Math.max(1, Number(config.image_capacity_burst_parallel) || 50),
    image_web_model_slug: String(config.image_web_model_slug || "gpt-5-5"),
    image_account_concurrency: Number(config.image_account_concurrency || 3),
    image_account_precheck_interval_minutes: Number(config.image_account_precheck_interval_minutes || 10),
    image_account_precheck_concurrency: Number(config.image_account_precheck_concurrency || 6),
    image_account_precheck_timeout_secs: Number(config.image_account_precheck_timeout_secs || 75),
    image_settle_enabled: Boolean(config.image_settle_enabled !== false),
    image_check_before_hit_enabled: Boolean(config.image_check_before_hit_enabled !== false),
    image_settle_secs: Number(config.image_settle_secs || 2.0),
    image_timeout_retry_secs: Number(config.image_timeout_retry_secs || 30),
    auto_remove_invalid_accounts: Boolean(config.auto_remove_invalid_accounts),
    auto_remove_rate_limited_accounts: Boolean(config.auto_remove_rate_limited_accounts),
    auto_relogin_after_refresh: Boolean(config.auto_relogin_after_refresh),
    auto_refill_enabled: Boolean(config.auto_refill_enabled),
    auto_refill_threshold_percent: Number(config.auto_refill_threshold_percent || 30),
    auto_refill_target_available: Number(config.auto_refill_target_available || 10),
    auto_refill_interval_minutes: Number(config.auto_refill_interval_minutes || 5),
    log_levels: Array.isArray(config.log_levels) ? config.log_levels : [],
    notifications: {
      bark: {
        enabled: Boolean(bark.enabled),
        server_url: String(bark.server_url || "https://api.day.app"),
        device_key: String(bark.device_key || ""),
        title_prefix: String(bark.title_prefix || "IMAGE POOL"),
        group: String(bark.group || "image-pool"),
        level: String(bark.level || "active"),
        timeout_secs: Number(bark.timeout_secs || 10),
        min_interval_seconds: Number(bark.min_interval_seconds ?? 60),
        notify_failed_calls: Boolean(bark.notify_failed_calls !== false),
        notify_register: Boolean(bark.notify_register !== false),
        notify_register_errors_only: Boolean(bark.notify_register_errors_only),
        notify_auto_refill: Boolean(bark.notify_auto_refill !== false),
      },
    },
    proxy_runtime: {
      enabled: Boolean(proxyRuntime.enabled),
      egress_mode: proxyRuntime.egress_mode === "single_proxy" ? "single_proxy" : "direct",
      proxy_url: String(proxyRuntime.proxy_url || ""),
      resource_proxy_url: String(proxyRuntime.resource_proxy_url || ""),
      skip_ssl_verify: Boolean(proxyRuntime.skip_ssl_verify),
      reset_session_status_codes: Array.isArray(proxyRuntime.reset_session_status_codes)
        ? proxyRuntime.reset_session_status_codes.map(Number).filter((item) => Number.isFinite(item))
        : [403],
      clearance: {
        enabled: Boolean(proxyRuntime.clearance?.enabled),
        mode: ["none", "manual", "flaresolverr"].includes(String(proxyRuntime.clearance?.mode))
          ? proxyRuntime.clearance.mode
          : "none",
        cf_cookies: String(proxyRuntime.clearance?.cf_cookies || ""),
        cf_clearance: String(proxyRuntime.clearance?.cf_clearance || ""),
        has_cf_cookies: Boolean(proxyRuntime.clearance?.has_cf_cookies),
        has_cf_clearance: Boolean(proxyRuntime.clearance?.has_cf_clearance),
        user_agent: String(proxyRuntime.clearance?.user_agent || ""),
        browser: String(proxyRuntime.clearance?.browser || "chrome"),
        flaresolverr_url: String(proxyRuntime.clearance?.flaresolverr_url || ""),
        timeout_sec: Number(proxyRuntime.clearance?.timeout_sec || 60),
        refresh_interval: Number(proxyRuntime.clearance?.refresh_interval || 3600),
        warm_up_on_start: Boolean(proxyRuntime.clearance?.warm_up_on_start),
      },
    },
    proxy: typeof config.proxy === "string" ? config.proxy : "",
    base_url: typeof config.base_url === "string" ? config.base_url : "",
    timezone: String(config.timezone || "Asia/Shanghai"),
    global_system_prompt: String(config.global_system_prompt || ""),
    sensitive_words: Array.isArray(config.sensitive_words) ? config.sensitive_words : [],
    ai_review: {
      enabled: Boolean(config.ai_review?.enabled),
      base_url: String(config.ai_review?.base_url || ""),
      api_key: String(config.ai_review?.api_key || ""),
      model: String(config.ai_review?.model || ""),
      prompt: String(config.ai_review?.prompt || ""),
    },
  };
}

type SettingsStore = {
  config: SettingsConfig | null;
  isLoadingConfig: boolean;
  isSavingConfig: boolean;
  isTestingBarkNotification: boolean;
  registerConfig: RegisterConfig | null;
  isLoadingRegister: boolean;
  isSavingRegister: boolean;

  initialize: () => Promise<void>;
  loadConfig: () => Promise<void>;
  setConfig: (config: SettingsConfig) => void;
  saveConfig: () => Promise<boolean>;
  setRefreshAccountIntervalMinute: (value: string) => void;
  setRefreshAccountConcurrency: (value: string) => void;
  setImageRetentionDays: (value: string) => void;
  setImagePollTimeoutSecs: (value: string) => void;
  setImageCapacityBurstParallel: (value: string) => void;
  setImageWebModelSlug: (value: string) => void;
  setAutoRemoveInvalidAccounts: (value: boolean) => void;
  setAutoRemoveRateLimitedAccounts: (value: boolean) => void;
  setAutoReloginAfterRefresh: (value: boolean) => void;
  setAutoRefillEnabled: (value: boolean) => void;
  setAutoRefillThresholdPercent: (value: string) => void;
  setAutoRefillTargetAvailable: (value: string) => void;
  setAutoRefillIntervalMinutes: (value: string) => void;
  setLogLevel: (level: string, enabled: boolean) => void;
  setProxy: (value: string) => void;
  setBaseUrl: (value: string) => void;
  setTimezone: (value: string) => void;
  setBarkNotificationField: (key: keyof BarkNotificationSettings, value: string | boolean) => void;
  testBark: () => Promise<void>;
  setProxyRuntimeField: (key: keyof ProxyRuntimeSettings, value: string | boolean | string[]) => void;
  setProxyRuntimeClearanceField: (key: keyof ProxyRuntimeSettings["clearance"], value: string | boolean) => void;
  setProxyRuntimeStatusCodesText: (value: string) => void;

  loadRegister: (silent?: boolean) => Promise<void>;
  setRegisterConfig: (config: RegisterConfig) => void;
  setRegisterProxy: (value: string) => void;
  setRegisterTotal: (value: string) => void;
  setRegisterThreads: (value: string) => void;
  setRegisterMode: (value: "total" | "quota" | "available") => void;
  setRegisterTargetQuota: (value: string) => void;
  setRegisterTargetAvailable: (value: string) => void;
  setRegisterCheckInterval: (value: string) => void;
  setRegisterMailField: (key: "request_timeout" | "wait_timeout" | "wait_interval", value: string) => void;
  addRegisterProvider: () => void;
  updateRegisterProvider: (index: number, updates: Record<string, unknown>) => void;
  deleteRegisterProvider: (index: number) => void;
  saveRegister: () => Promise<void>;
  toggleRegister: () => Promise<void>;
  resetRegister: () => Promise<void>;
  resetOutlookPool: (scope: "all" | "failed" | "unused") => Promise<void>;
};

export const useSettingsStore = create<SettingsStore>((set, get) => ({
  config: null,
  isLoadingConfig: true,
  isSavingConfig: false,
  isTestingBarkNotification: false,
  registerConfig: null,
  isLoadingRegister: true,
  isSavingRegister: false,

  initialize: async () => {
    await get().loadConfig();
  },

  loadConfig: async () => {
    set({ isLoadingConfig: true });
    try {
      const data = await fetchSettingsConfig();
      set({ config: normalizeConfig(data.config) });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载系统配置失败");
    } finally {
      set({ isLoadingConfig: false });
    }
  },

  setConfig: (config) => {
    set({ config: normalizeConfig(config) });
  },

  saveConfig: async () => {
    const { config } = get();
    if (!config) {
      return false;
    }

    set({ isSavingConfig: true });
    try {
      const barkTimeout = Number(config.notifications?.bark?.timeout_secs);
      const barkMinInterval = Number(config.notifications?.bark?.min_interval_seconds);
      const data = await updateSettingsConfig({
        ...config,
        refresh_account_interval_minute: Math.max(1, Number(config.refresh_account_interval_minute) || 1),
        refresh_account_concurrency: Math.min(100, Math.max(1, Number(config.refresh_account_concurrency) || 20)),
        image_retention_days: Math.max(1, Number(config.image_retention_days) || 30),
        image_poll_timeout_secs: Math.min(180, Math.max(15, Number(config.image_poll_timeout_secs) || 180)),
        image_task_timeout_secs: 300,
        image_capacity_burst_parallel: Math.min(10000, Math.max(1, Number(config.image_capacity_burst_parallel) || 50)),
        image_web_model_slug: String(config.image_web_model_slug || "gpt-5-5").trim() || "gpt-5-5",
        image_account_concurrency: Math.max(1, Number(config.image_account_concurrency) || 3),
        image_account_precheck_interval_minutes: Math.max(1, Number(config.image_account_precheck_interval_minutes) || 10),
        image_account_precheck_concurrency: Math.min(30, Math.max(1, Number(config.image_account_precheck_concurrency) || 6)),
        image_account_precheck_timeout_secs: Math.min(180, Math.max(10, Number(config.image_account_precheck_timeout_secs) || 75)),
        image_settle_enabled: Boolean(config.image_settle_enabled !== false),
        image_check_before_hit_enabled: Boolean(config.image_check_before_hit_enabled !== false),
        image_settle_secs: Math.max(0.5, Number(config.image_settle_secs) || 2.0),
        image_timeout_retry_secs: Math.max(1, Number(config.image_timeout_retry_secs) || 30),
        auto_remove_invalid_accounts: Boolean(config.auto_remove_invalid_accounts),
        auto_remove_rate_limited_accounts: Boolean(config.auto_remove_rate_limited_accounts),
        auto_relogin_after_refresh: Boolean(config.auto_relogin_after_refresh),
        auto_refill_enabled: Boolean(config.auto_refill_enabled),
        auto_refill_threshold_percent: Math.min(100, Math.max(0, Number(config.auto_refill_threshold_percent) || 30)),
        auto_refill_target_available: Math.max(1, Number(config.auto_refill_target_available) || 10),
        auto_refill_interval_minutes: Math.max(1, Number(config.auto_refill_interval_minutes) || 5),
        notifications: {
          bark: {
            enabled: Boolean(config.notifications?.bark?.enabled),
            server_url: String(config.notifications?.bark?.server_url || "https://api.day.app").trim(),
            device_key: String(config.notifications?.bark?.device_key || "").trim(),
            title_prefix: String(config.notifications?.bark?.title_prefix || "IMAGE POOL").trim(),
            group: String(config.notifications?.bark?.group || "image-pool").trim(),
            level: ["active", "timeSensitive", "passive", "critical"].includes(String(config.notifications?.bark?.level))
              ? String(config.notifications?.bark?.level)
              : "active",
            timeout_secs: Math.min(60, Math.max(1, Number.isFinite(barkTimeout) ? barkTimeout : 10)),
            min_interval_seconds: Math.min(3600, Math.max(0, Number.isFinite(barkMinInterval) ? barkMinInterval : 60)),
            notify_failed_calls: Boolean(config.notifications?.bark?.notify_failed_calls !== false),
            notify_register: Boolean(config.notifications?.bark?.notify_register !== false),
            notify_register_errors_only: Boolean(config.notifications?.bark?.notify_register_errors_only),
            notify_auto_refill: Boolean(config.notifications?.bark?.notify_auto_refill !== false),
          },
        },
        proxy_runtime: {
          ...(config.proxy_runtime as ProxyRuntimeSettings),
          enabled: Boolean(config.proxy_runtime?.enabled),
          egress_mode: config.proxy_runtime?.egress_mode === "single_proxy" ? "single_proxy" : "direct",
          proxy_url: String(config.proxy_runtime?.proxy_url || "").trim(),
          resource_proxy_url: String(config.proxy_runtime?.resource_proxy_url || "").trim(),
          skip_ssl_verify: Boolean(config.proxy_runtime?.skip_ssl_verify),
          reset_session_status_codes: Array.isArray(config.proxy_runtime?.reset_session_status_codes)
            ? config.proxy_runtime.reset_session_status_codes.map(Number).filter((item) => Number.isFinite(item))
            : [403],
          clearance: {
            ...(config.proxy_runtime?.clearance || {}),
            enabled: Boolean(config.proxy_runtime?.clearance?.enabled),
            mode: (["none", "manual", "flaresolverr"].includes(String(config.proxy_runtime?.clearance?.mode))
              ? String(config.proxy_runtime?.clearance?.mode)
              : "none") as ProxyRuntimeSettings["clearance"]["mode"],
            cf_cookies: String(config.proxy_runtime?.clearance?.cf_cookies || "").trim(),
            cf_clearance: String(config.proxy_runtime?.clearance?.cf_clearance || "").trim(),
            user_agent: String(config.proxy_runtime?.clearance?.user_agent || "").trim(),
            browser: String(config.proxy_runtime?.clearance?.browser || "chrome").trim(),
            flaresolverr_url: String(config.proxy_runtime?.clearance?.flaresolverr_url || "").trim(),
            timeout_sec: Math.max(1, Number(config.proxy_runtime?.clearance?.timeout_sec) || 60),
            refresh_interval: Math.max(60, Number(config.proxy_runtime?.clearance?.refresh_interval) || 3600),
            warm_up_on_start: Boolean(config.proxy_runtime?.clearance?.warm_up_on_start),
          },
        },
        proxy: String(config.proxy || "").trim(),
        base_url: String(config.base_url || "").trim(),
        timezone: String(config.timezone || "Asia/Shanghai").trim() || "Asia/Shanghai",
        global_system_prompt: String(config.global_system_prompt || "").trim(),
        sensitive_words: (config.sensitive_words || []).map((item) => String(item).trim()).filter(Boolean),
        ai_review: {
          enabled: Boolean(config.ai_review?.enabled),
          base_url: String(config.ai_review?.base_url || "").trim(),
          api_key: String(config.ai_review?.api_key || "").trim(),
          model: String(config.ai_review?.model || "").trim(),
          prompt: String(config.ai_review?.prompt || "").trim(),
        },
      });
      set({ config: normalizeConfig(data.config) });
      toast.success("配置已保存");
      return true;
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存系统配置失败");
      return false;
    } finally {
      set({ isSavingConfig: false });
    }
  },

  setRefreshAccountIntervalMinute: (value) => {
    set((state) => (state.config ? { config: { ...state.config, refresh_account_interval_minute: value } } : {}));
  },

  setRefreshAccountConcurrency: (value) => {
    set((state) => (state.config ? { config: { ...state.config, refresh_account_concurrency: value } } : {}));
  },

  setImageRetentionDays: (value) => {
    set((state) => (state.config ? { config: { ...state.config, image_retention_days: value } } : {}));
  },

  setImagePollTimeoutSecs: (value) => {
    set((state) => (state.config ? { config: { ...state.config, image_poll_timeout_secs: value } } : {}));
  },

  setImageCapacityBurstParallel: (value) => {
    set((state) => (state.config ? { config: { ...state.config, image_capacity_burst_parallel: value } } : {}));
  },

  setImageWebModelSlug: (value) => {
    set((state) => (state.config ? { config: { ...state.config, image_web_model_slug: value } } : {}));
  },

  setAutoRemoveInvalidAccounts: (value) => {
    set((state) => (state.config ? { config: { ...state.config, auto_remove_invalid_accounts: value } } : {}));
  },

  setAutoRemoveRateLimitedAccounts: (value) => {
    set((state) => (state.config ? { config: { ...state.config, auto_remove_rate_limited_accounts: value } } : {}));
  },

  setAutoReloginAfterRefresh: (value) => {
    set((state) => (state.config ? { config: { ...state.config, auto_relogin_after_refresh: value } } : {}));
  },

  setAutoRefillEnabled: (value) => {
    set((state) => (state.config ? { config: { ...state.config, auto_refill_enabled: value } } : {}));
  },

  setAutoRefillThresholdPercent: (value) => {
    set((state) => (state.config ? { config: { ...state.config, auto_refill_threshold_percent: value } } : {}));
  },

  setAutoRefillTargetAvailable: (value) => {
    set((state) => (state.config ? { config: { ...state.config, auto_refill_target_available: value } } : {}));
  },

  setAutoRefillIntervalMinutes: (value) => {
    set((state) => (state.config ? { config: { ...state.config, auto_refill_interval_minutes: value } } : {}));
  },

  setLogLevel: (level, enabled) => {
    set((state) => {
      if (!state.config) return {};
      const levels = new Set(state.config.log_levels || []);
      if (enabled) levels.add(level);
      else levels.delete(level);
      return { config: { ...state.config, log_levels: Array.from(levels) } };
    });
  },

  setProxy: (value) => {
    set((state) => {
      if (!state.config) {
        return {};
      }
      const proxy = value.trim();
      const runtime = state.config.proxy_runtime ?? defaultProxyRuntime();
      return {
        config: {
          ...state.config,
          proxy: value,
          proxy_runtime: {
            ...runtime,
            enabled: Boolean(proxy),
            egress_mode: proxy ? "single_proxy" : "direct",
            proxy_url: proxy,
            resource_proxy_url: proxy,
          },
        },
      };
    });
  },

  setBaseUrl: (value) => {
    set((state) => (state.config ? { config: { ...state.config, base_url: value } } : {}));
  },

  setTimezone: (value) => {
    set((state) => (state.config ? { config: { ...state.config, timezone: value } } : {}));
  },

  setBarkNotificationField: (key, value) => {
    set((state) => {
      if (!state.config?.notifications?.bark) {
        return {};
      }
      return {
        config: {
          ...state.config,
          notifications: {
            ...state.config.notifications,
            bark: {
              ...state.config.notifications.bark,
              [key]: value,
            },
          },
        },
      };
    });
  },

  testBark: async () => {
    set({ isTestingBarkNotification: true });
    try {
      const saved = await get().saveConfig();
      if (!saved) {
        return;
      }
      const data = await testBarkNotification();
      if (data.result.ok) {
        toast.success(`Bark 推送成功（HTTP ${data.result.status}）`);
      } else {
        toast.error(`Bark 推送失败：${data.result.error ?? `HTTP ${data.result.status}`}`);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "测试 Bark 推送失败");
    } finally {
      set({ isTestingBarkNotification: false });
    }
  },

  setProxyRuntimeField: (key, value) => {
    set((state) => {
      if (!state.config?.proxy_runtime) {
        return {};
      }
      return {
        config: {
          ...state.config,
          proxy_runtime: {
            ...state.config.proxy_runtime,
            [key]: value,
          },
        },
      };
    });
  },

  setProxyRuntimeClearanceField: (key, value) => {
    set((state) => {
      if (!state.config?.proxy_runtime) {
        return {};
      }
      return {
        config: {
          ...state.config,
          proxy_runtime: {
            ...state.config.proxy_runtime,
            clearance: {
              ...state.config.proxy_runtime.clearance,
              [key]: value,
            },
          },
        },
      };
    });
  },

  setProxyRuntimeStatusCodesText: (value) => {
    const codes = value
      .split(/[\s,，]+/)
      .map((item) => Number(item.trim()))
      .filter((item) => Number.isInteger(item) && item >= 100 && item <= 599);
    set((state) =>
      state.config?.proxy_runtime
        ? {
            config: {
              ...state.config,
              proxy_runtime: {
                ...state.config.proxy_runtime,
                reset_session_status_codes: codes.length ? Array.from(new Set(codes)) : [403],
              },
            },
          }
        : {},
    );
  },

  loadRegister: async (silent = false) => {
    if (!silent) set({ isLoadingRegister: true });
    try {
      const data = await fetchRegisterConfig();
      set({ registerConfig: data.register });
    } catch (error) {
      if (!silent) toast.error(error instanceof Error ? error.message : "加载注册配置失败");
    } finally {
      if (!silent) set({ isLoadingRegister: false });
    }
  },

  setRegisterConfig: (config) => {
    set({
      registerConfig: {
        ...config,
        flaresolverr: {
          enabled: config.flaresolverr?.enabled !== false,
          url: String(config.flaresolverr?.url || ""),
          max_timeout_ms: Number(config.flaresolverr?.max_timeout_ms || 60000),
          preload: config.flaresolverr?.preload !== false,
        },
      },
      isLoadingRegister: false,
    });
  },

  setRegisterProxy: (value) => {
    set((state) => (state.registerConfig ? { registerConfig: { ...state.registerConfig, proxy: value } } : {}));
  },

  setRegisterTotal: (value) => {
    set((state) => (state.registerConfig ? { registerConfig: { ...state.registerConfig, total: Number(value) || 0 } } : {}));
  },

  setRegisterThreads: (value) => {
    set((state) => (state.registerConfig ? { registerConfig: { ...state.registerConfig, threads: Number(value) || 0 } } : {}));
  },

  setRegisterMode: (value) => {
    set((state) => (state.registerConfig ? { registerConfig: { ...state.registerConfig, mode: value } } : {}));
  },

  setRegisterTargetQuota: (value) => {
    set((state) =>
      state.registerConfig ? { registerConfig: { ...state.registerConfig, target_quota: Number(value) || 0 } } : {},
    );
  },

  setRegisterTargetAvailable: (value) => {
    set((state) =>
      state.registerConfig
        ? { registerConfig: { ...state.registerConfig, target_available: Number(value) || 0 } }
        : {},
    );
  },

  setRegisterCheckInterval: (value) => {
    set((state) =>
      state.registerConfig ? { registerConfig: { ...state.registerConfig, check_interval: Number(value) || 0 } } : {},
    );
  },

  setRegisterMailField: (key, value) => {
    set((state) =>
      state.registerConfig
        ? {
            registerConfig: {
              ...state.registerConfig,
              mail: { ...state.registerConfig.mail, [key]: Number(value) || 0 },
            },
          }
        : {},
    );
  },

  addRegisterProvider: () => {
    set((state) =>
      state.registerConfig
        ? {
            registerConfig: {
              ...state.registerConfig,
              mail: {
                ...state.registerConfig.mail,
                providers: [
                  ...(state.registerConfig.mail.providers || []),
                  {
                    enable: true,
                    type: "cloudmail_gen",
                    api_base: "",
                    admin_email: "",
                    admin_password: "",
                    domain: [],
                    subdomain: [],
                    email_prefix: "",
                  },
                ],
              },
            },
          }
        : {},
    );
  },

  updateRegisterProvider: (index, updates) => {
    set((state) => {
      if (!state.registerConfig) return {};
      const providers = [...(state.registerConfig.mail.providers || [])];
      providers[index] = { ...(providers[index] || {}), ...updates };
      return { registerConfig: { ...state.registerConfig, mail: { ...state.registerConfig.mail, providers } } };
    });
  },

  deleteRegisterProvider: (index) => {
    set((state) =>
      state.registerConfig
        ? {
            registerConfig: {
              ...state.registerConfig,
              mail: {
                ...state.registerConfig.mail,
                providers: (state.registerConfig.mail.providers || []).filter((_, itemIndex) => itemIndex !== index),
              },
            },
          }
        : {},
    );
  },

  saveRegister: async () => {
    const { registerConfig } = get();
    if (!registerConfig) return;
    try {
      set({ isSavingRegister: true });
      const proxy = registerConfig.proxy.trim();
      const mail = { ...registerConfig.mail };
      delete (mail as Record<string, unknown>).proxy;
      const data = await updateRegisterConfig({
        mail,
        proxy,
        flaresolverr: {
          enabled: registerConfig.flaresolverr?.enabled !== false,
          url: String(registerConfig.flaresolverr?.url || "").trim(),
          max_timeout_ms: Math.max(1000, Number(registerConfig.flaresolverr?.max_timeout_ms) || 60000),
          preload: registerConfig.flaresolverr?.preload !== false,
        },
        total: Math.max(1, Number(registerConfig.total) || 1),
        threads: Math.max(1, Number(registerConfig.threads) || 1),
        mode: registerConfig.mode,
        target_quota: Math.max(1, Number(registerConfig.target_quota) || 1),
        target_available: Math.max(1, Number(registerConfig.target_available) || 1),
        check_interval: Math.max(1, Number(registerConfig.check_interval) || 5),
      });
      set({ registerConfig: data.register });
      toast.success("注册配置已保存");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存注册配置失败");
    } finally {
      set({ isSavingRegister: false });
    }
  },

  toggleRegister: async () => {
    const { registerConfig } = get();
    if (!registerConfig) return;
    set({ isSavingRegister: true });
    try {
      if (!registerConfig.enabled) {
        const proxy = registerConfig.proxy.trim();
        const mail = { ...registerConfig.mail };
        delete (mail as Record<string, unknown>).proxy;
        await updateRegisterConfig({
          mail,
          proxy,
          flaresolverr: {
            enabled: registerConfig.flaresolverr?.enabled !== false,
            url: String(registerConfig.flaresolverr?.url || "").trim(),
            max_timeout_ms: Math.max(1000, Number(registerConfig.flaresolverr?.max_timeout_ms) || 60000),
            preload: registerConfig.flaresolverr?.preload !== false,
          },
          total: Math.max(1, Number(registerConfig.total) || 1),
          threads: Math.max(1, Number(registerConfig.threads) || 1),
          mode: registerConfig.mode,
          target_quota: Math.max(1, Number(registerConfig.target_quota) || 1),
          target_available: Math.max(1, Number(registerConfig.target_available) || 1),
          check_interval: Math.max(1, Number(registerConfig.check_interval) || 5),
        });
      }
      const data = registerConfig.enabled ? await stopRegister() : await startRegister();
      set({ registerConfig: data.register });
      toast.success(registerConfig.enabled ? "注册任务已停止" : "注册任务已启动");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "切换注册状态失败");
    } finally {
      set({ isSavingRegister: false });
    }
  },

  resetRegister: async () => {
    set({ isSavingRegister: true });
    try {
      const data = await resetRegisterApi();
      set({ registerConfig: data.register });
      toast.success("注册统计已重置");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "重置注册统计失败");
    } finally {
      set({ isSavingRegister: false });
    }
  },

  resetOutlookPool: async (scope) => {
    set({ isSavingRegister: true });
    try {
      const data = await resetOutlookPoolApi(scope);
      set({ registerConfig: data.register });
      toast.success("Outlook 邮箱池已更新");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "重置 Outlook 邮箱池失败");
    } finally {
      set({ isSavingRegister: false });
    }
  },
}));
