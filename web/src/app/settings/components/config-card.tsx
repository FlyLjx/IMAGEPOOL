"use client";

import { useState } from "react";
import {
  Alert,
  Button,
  Card,
  Col,
  Divider,
  Form,
  Input,
  Row,
  Space,
  Switch,
  Typography,
} from "antd";
import { LoaderCircle, PlugZap, Save, ShieldCheck } from "lucide-react";
import { toast } from "sonner";

import { testProxy, type ProxyTestResult } from "@/lib/api";

import { useSettingsStore } from "../store";

function SectionTitle({ title, description }: { title: string; description: string }) {
  return (
    <div className="mb-4">
      <Typography.Title level={5} className="!mb-1">
        {title}
      </Typography.Title>
      <Typography.Text type="secondary">{description}</Typography.Text>
    </div>
  );
}

function NumberInput({
  label,
  value,
  onChange,
  placeholder,
  help,
  disabled,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  help?: string;
  disabled?: boolean;
}) {
  return (
    <Form.Item label={label} extra={help}>
      <Input value={value} onChange={(event) => onChange(event.target.value)} placeholder={placeholder} disabled={disabled} />
    </Form.Item>
  );
}

function formatProxyExit(result: ProxyTestResult) {
  const exit = result.exit_ip;
  if (!exit?.ip) {
    return "出口信息未返回";
  }
  const location = [exit.city, exit.region, exit.country].filter(Boolean).join(" / ");
  const parts = [`出口 IP: ${exit.ip}`];
  if (location) parts.push(`地区: ${location}`);
  if (exit.org) parts.push(`运营商: ${exit.org}`);
  if (exit.timezone) parts.push(`时区: ${exit.timezone}`);
  return parts.join("，");
}

function formatProxyCheck(label: string, check?: ProxyTestResult["chatgpt"]) {
  if (!check) {
    return `${label}: 未测试`;
  }
  const status = check.status ? `HTTP ${check.status}` : "无响应";
  return check.ok
    ? `${label}: 可连接，${status}，${check.latency_ms} ms`
    : `${label}: 失败（${check.error || status}），${check.latency_ms} ms`;
}

function formatProxyTestDescription(result: ProxyTestResult) {
  return [
    formatProxyCheck("ChatGPT 连接", result.chatgpt),
    formatProxyCheck("Codex/urllib 路径", result.urllib_chatgpt),
    formatProxyExit(result),
  ].join("；");
}

export function ConfigCard() {
  const [isTestingProxy, setIsTestingProxy] = useState(false);
  const [proxyTestResult, setProxyTestResult] = useState<ProxyTestResult | null>(null);
  const config = useSettingsStore((state) => state.config);
  const isLoadingConfig = useSettingsStore((state) => state.isLoadingConfig);
  const isSavingConfig = useSettingsStore((state) => state.isSavingConfig);
  const setRefreshAccountIntervalMinute = useSettingsStore((state) => state.setRefreshAccountIntervalMinute);
  const setRefreshAccountConcurrency = useSettingsStore((state) => state.setRefreshAccountConcurrency);
  const setImageRetentionDays = useSettingsStore((state) => state.setImageRetentionDays);
  const setImagePollTimeoutSecs = useSettingsStore((state) => state.setImagePollTimeoutSecs);
  const setImageCapacityBurstParallel = useSettingsStore((state) => state.setImageCapacityBurstParallel);
  const setImagePoolAutoRegisterEnabled = useSettingsStore((state) => state.setImagePoolAutoRegisterEnabled);
  const setImagePoolMinUsableAccounts = useSettingsStore((state) => state.setImagePoolMinUsableAccounts);
  const setImagePoolIdleFloorAccounts = useSettingsStore((state) => state.setImagePoolIdleFloorAccounts);
  const setImagePoolMaxUsableAccounts = useSettingsStore((state) => state.setImagePoolMaxUsableAccounts);
  const setImagePoolQuietAfterMinutes = useSettingsStore((state) => state.setImagePoolQuietAfterMinutes);
  const setImagePoolRegisterCooldownMinutes = useSettingsStore((state) => state.setImagePoolRegisterCooldownMinutes);
  const setImagePoolMaxRegisterPerCycle = useSettingsStore((state) => state.setImagePoolMaxRegisterPerCycle);
  const setImagePoolAutoRegisterIntervalSecs = useSettingsStore((state) => state.setImagePoolAutoRegisterIntervalSecs);
  const setImageGlobalMaxInflight = useSettingsStore((state) => state.setImageGlobalMaxInflight);
  const setImagePrepareParallel = useSettingsStore((state) => state.setImagePrepareParallel);
  const setImageSubmitParallel = useSettingsStore((state) => state.setImageSubmitParallel);
  const setImagePollParallel = useSettingsStore((state) => state.setImagePollParallel);
  const setImageDownloadParallel = useSettingsStore((state) => state.setImageDownloadParallel);
  const setImageUploadParallel = useSettingsStore((state) => state.setImageUploadParallel);
  const setImageAccountMaxInflightPerAccount = useSettingsStore((state) => state.setImageAccountMaxInflightPerAccount);
  const setImageAccountDynamicSlots = useSettingsStore((state) => state.setImageAccountDynamicSlots);
  const setImageStallTimeoutSecs = useSettingsStore((state) => state.setImageStallTimeoutSecs);
  const setImageMaxSwitchesPerTask = useSettingsStore((state) => state.setImageMaxSwitchesPerTask);
  const setImageWebModelSlug = useSettingsStore((state) => state.setImageWebModelSlug);
  const setProxy = useSettingsStore((state) => state.setProxy);
  const setBaseUrl = useSettingsStore((state) => state.setBaseUrl);
  const setTimezone = useSettingsStore((state) => state.setTimezone);
  const saveConfig = useSettingsStore((state) => state.saveConfig);

  const handleTestProxy = async () => {
    const candidate = String(config?.proxy || "").trim();
    if (!candidate) {
      toast.error("请先填写代理地址");
      return;
    }
    setIsTestingProxy(true);
    setProxyTestResult(null);
    try {
      const data = await testProxy(candidate);
      setProxyTestResult(data.result);
      if (data.result.ok) {
        toast.success(`代理可连接 chatgpt.com，${data.result.latency_ms} ms`);
      } else {
        toast.error(`代理无法完整连接 chatgpt.com，${data.result.error ?? "未知错误"}`);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "测试代理失败");
    } finally {
      setIsTestingProxy(false);
    }
  };

  if (isLoadingConfig) {
    return (
      <Card>
        <div className="flex items-center justify-center py-12">
          <LoaderCircle className="size-5 animate-spin text-slate-400" />
        </div>
      </Card>
    );
  }

  if (!config) {
    return null;
  }


  return (
    <Card
      title={
        <Space>
          <ShieldCheck className="size-4 text-blue-500" />
          <span>系统配置</span>
        </Space>
      }
      extra={
        <Button
          type="primary"
          icon={isSavingConfig ? <LoaderCircle className="size-4 animate-spin" /> : <Save className="size-4" />}
          onClick={() => void saveConfig()}
          disabled={isSavingConfig}
        >
          保存配置
        </Button>
      }
    >
      <Form layout="vertical" requiredMark={false}>
        <Alert
          type="info"
          showIcon
          className="mb-5"
          message="常用配置保持简单；管理员密钥和用户 Key 在右侧单独管理。"
        />

        <SectionTitle title="生图调度" description="通常只需要设置这两项；单号槽位可按账号自动升降，也可固定使用上限。" />
        <Row gutter={[16, 16]}>
          <Col xs={24} md={12}>
            <NumberInput
              label="全局生图并发"
              value={String(config.image_global_max_inflight || "")}
              onChange={setImageGlobalMaxInflight}
              placeholder="120"
              help="所有账号合计的固定总上限。"
            />
          </Col>
          <Col xs={24} md={12}>
            <div className="flex flex-wrap items-start gap-x-4">
              <div className="min-w-44 flex-1">
                <NumberInput
                  label="单号最大并发"
                  value={String(config.image_account_max_inflight_per_account || "")}
                  onChange={setImageAccountMaxInflightPerAccount}
                  placeholder="1"
                  help={config.image_account_dynamic_slots !== false ? "动态模式的每个账号槽位最高值。" : "静态模式下每个账号直接使用这个上限。"}
                />
              </div>
              <Form.Item label="槽位模式" extra="动态 / 静态">
                <Switch
                  checked={config.image_account_dynamic_slots !== false}
                  checkedChildren="动态"
                  unCheckedChildren="静态"
                  disabled={isSavingConfig}
                  onChange={setImageAccountDynamicSlots}
                />
              </Form.Item>
            </div>
          </Col>
        </Row>

        <details className="mt-5 rounded-lg border border-slate-200 bg-slate-50/50">
          <summary className="cursor-pointer px-4 py-3 text-sm font-medium text-slate-700">更多运行设置（按需修改）</summary>
          <div className="border-t border-slate-200 px-4 py-4">
            <SectionTitle title="基础运行" description="账号刷新、图片清理和轮询等待时间。" />
            <Row gutter={[16, 16]}>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="账号刷新间隔"
                  value={String(config.refresh_account_interval_minute || "")}
                  onChange={setRefreshAccountIntervalMinute}
                  placeholder="60"
                  help="单位分钟；保存后会重置下一轮自动刷新倒计时。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="账号刷新并发"
                  value={String(config.refresh_account_concurrency || "")}
                  onChange={setRefreshAccountConcurrency}
                  placeholder="20"
                  help="同时检测账号信息和额度的线程数，最高 100。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="图片自动清理"
                  value={String(config.image_retention_days || "")}
                  onChange={setImageRetentionDays}
                  placeholder="30"
                  help="自动删除多少天前的本地图片。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="图片轮询上限"
                  value={String(config.image_poll_timeout_secs || "")}
                  onChange={setImagePollTimeoutSecs}
                  placeholder="600"
                  help="单位秒，最高 600 秒（10 分钟）；超时后记录诊断并结束当前尝试。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="生图总预算"
                  value="630"
                  onChange={() => {}}
                  placeholder="630"
                  help="固定 630 秒，旧配置会自动迁移。"
                  disabled
                />
              </Col>
            </Row>

            <Divider />
            <SectionTitle title="调度高级参数" description="阶段并发和卡住切号阈值；账号槽位模式按上方选择执行。" />
            <Row gutter={[16, 16]}>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="准备阶段并发"
                  value={String(config.image_prepare_parallel || "")}
                  onChange={setImagePrepareParallel}
                  placeholder="20"
                  help="浏览器身份和生图准备阶段的并发上限。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="提交阶段并发"
                  value={String(config.image_submit_parallel || "")}
                  onChange={setImageSubmitParallel}
                  placeholder="20"
                  help="创建生图会话、提交提示词的并发上限。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="轮询阶段并发"
                  value={String(config.image_poll_parallel || "")}
                  onChange={setImagePollParallel}
                  placeholder="80"
                  help="同时发出的会话状态轮询请求上限。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="下载阶段并发"
                  value={String(config.image_download_parallel || "")}
                  onChange={setImageDownloadParallel}
                  placeholder="20"
                  help="生成结果解析和下载请求的并发上限。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="参考图上传并发"
                  value={String(config.image_upload_parallel || "")}
                  onChange={setImageUploadParallel}
                  placeholder="12"
                  help="参考图上传请求的并发上限。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="无图切号阈值"
                  value={String(config.image_stall_timeout_secs || "")}
                  onChange={setImageStallTimeoutSecs}
                  placeholder="150"
                  help="连续无图片引用达到该秒数后冷却当前账号并切换新会话。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="单任务最大切号"
                  value={String(config.image_max_switches_per_task || "")}
                  onChange={setImageMaxSwitchesPerTask}
                  placeholder="2"
                  help="卡住生图最多切换账号的次数，最高 5 次。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="突发并发保障"
                  value={String(config.image_capacity_burst_parallel || "")}
                  onChange={setImageCapacityBurstParallel}
                  placeholder="50"
                  help="容量预测为短时突发任务预留的并发槽位。"
                />
              </Col>
            </Row>

            <Divider />
            <SectionTitle title="访问与上游" description="代理、结果访问地址、时区和 ChatGPT Web 模型。" />
            <Row gutter={[16, 16]}>
              <Col xs={24} lg={12}>
                <Form.Item label="全局出站代理" extra="保存后用于所有 ChatGPT 请求；留空则直连。">
                  <Space.Compact className="w-full">
                    <Input
                      value={String(config.proxy || "")}
                      onChange={(event) => {
                        setProxy(event.target.value);
                        setProxyTestResult(null);
                      }}
                      placeholder="http://127.0.0.1:7890"
                    />
                    <Button
                      icon={isTestingProxy ? <LoaderCircle className="size-4 animate-spin" /> : <PlugZap className="size-4" />}
                      onClick={() => void handleTestProxy()}
                      disabled={isTestingProxy}
                    >
                      测试
                    </Button>
                  </Space.Compact>
                </Form.Item>
                {proxyTestResult ? (
                  <Alert
                    type={proxyTestResult.ok ? "success" : "error"}
                    showIcon
                    message={
                      proxyTestResult.ok
                        ? `代理可连接 chatgpt.com，用时 ${proxyTestResult.latency_ms} ms`
                        : `代理无法完整连接 chatgpt.com，${proxyTestResult.error ?? "未知错误"}`
                    }
                    description={formatProxyTestDescription(proxyTestResult)}
                  />
                ) : null}
              </Col>
              <Col xs={24} lg={12}>
                <Form.Item label="图片访问地址" extra="用于生成图片结果的访问前缀地址。">
                  <Input value={String(config.base_url || "")} onChange={(event) => setBaseUrl(event.target.value)} placeholder="https://example.com" />
                </Form.Item>
              </Col>
              <Col xs={24} md={12} xl={6}>
                <Form.Item label="运行时区" extra="影响后台日志、任务时间和本地文件日期。">
                  <Input value={String(config.timezone || "Asia/Shanghai")} onChange={(event) => setTimezone(event.target.value)} placeholder="Asia/Shanghai" />
                </Form.Item>
              </Col>
              <Col xs={24} md={12} xl={6}>
                <Form.Item label="ChatGPT Web 生图模型" extra="普通 gpt-image-2 线路的底层 model slug，保存后新任务生效。">
                  <Input
                    value={String(config.image_web_model_slug || "gpt-5-5")}
                    onChange={(event) => setImageWebModelSlug(event.target.value)}
                    placeholder="gpt-5-5"
                  />
                </Form.Item>
              </Col>
            </Row>

            <Divider />
            <SectionTitle title="号池自动补号" description="按当前排队和运行任务估算所需有效账号；无任务时自动停顿，避免空闲时持续注册。默认关闭。" />
            <Row gutter={[16, 16]}>
              <Col xs={24} md={12} xl={6}>
                <Form.Item label="启用自动补号" extra="开启后由任务压力控制注册机，手动注册运行时自动控制会让路。">
                  <Switch checked={Boolean(config.image_pool_auto_register_enabled)} onChange={setImagePoolAutoRegisterEnabled} disabled={isSavingConfig} />
                </Form.Item>
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="活跃任务最低账号"
                  value={String(config.image_pool_min_usable_accounts ?? "")}
                  onChange={setImagePoolMinUsableAccounts}
                  placeholder="0"
                  help="有任务时至少保留的有效账号数，0 表示完全按任务数计算。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="空闲保底账号"
                  value={String(config.image_pool_idle_floor_accounts ?? "")}
                  onChange={setImagePoolIdleFloorAccounts}
                  placeholder="0"
                  help="无任务时最多在静默宽限期内补到的账号数，建议 0。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="有效账号上限"
                  value={String(config.image_pool_max_usable_accounts ?? "")}
                  onChange={setImagePoolMaxUsableAccounts}
                  placeholder="200"
                  help="自动补号不会超过此有效账号数，上限 10000。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="空闲静默分钟"
                  value={String(config.image_pool_quiet_after_minutes ?? "")}
                  onChange={setImagePoolQuietAfterMinutes}
                  placeholder="15"
                  help="没有任务多久后进入静默模式。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="批次冷却分钟"
                  value={String(config.image_pool_register_cooldown_minutes ?? "")}
                  onChange={setImagePoolRegisterCooldownMinutes}
                  placeholder="1"
                  help="注册批次之间的最短间隔，防止短时间反复启动。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="单批最大尝试"
                  value={String(config.image_pool_max_register_per_cycle ?? "")}
                  onChange={setImagePoolMaxRegisterPerCycle}
                  placeholder="10"
                  help="每个自动批次最多尝试注册多少个账号。"
                />
              </Col>
              <Col xs={24} md={12} xl={6}>
                <NumberInput
                  label="预测检查间隔秒"
                  value={String(config.image_pool_auto_register_interval_secs ?? "")}
                  onChange={setImagePoolAutoRegisterIntervalSecs}
                  placeholder="30"
                  help="后台重新评估任务压力和有效账号数的间隔。"
                />
              </Col>
            </Row>
          </div>
        </details>

      </Form>
    </Card>
  );
}
