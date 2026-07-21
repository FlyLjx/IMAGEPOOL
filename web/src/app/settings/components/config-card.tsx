"use client";

import { useState } from "react";
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Col,
  Divider,
  Form,
  Input,
  Row,
  Space,
  Switch,
  Tag,
  Typography,
} from "antd";
import { BellRing, LoaderCircle, PlugZap, Save, ShieldCheck } from "lucide-react";
import { toast } from "sonner";

import { testProxy, type ProxyTestResult } from "@/lib/api";

import { useSettingsStore } from "../store";

const logLevelOptions = ["debug", "info", "warning", "error"];

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
  const setImageWebModelSlug = useSettingsStore((state) => state.setImageWebModelSlug);
  const setLogLevel = useSettingsStore((state) => state.setLogLevel);
  const setProxy = useSettingsStore((state) => state.setProxy);
  const setBaseUrl = useSettingsStore((state) => state.setBaseUrl);
  const setTimezone = useSettingsStore((state) => state.setTimezone);
  const setBarkNotificationField = useSettingsStore((state) => state.setBarkNotificationField);
  const testBark = useSettingsStore((state) => state.testBark);
  const isTestingBarkNotification = useSettingsStore((state) => state.isTestingBarkNotification);
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

  const barkEnabled = Boolean(config.notifications?.bark?.enabled);

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
          message="管理员登录密钥可在右侧单独修改。需要分发访问权限时，请在用户密钥管理里创建普通用户密钥。"
        />

        <SectionTitle title="基础运行" description="控制账号刷新、代理、图片访问地址和本地图片保留策略。" />
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
              placeholder="300"
              help="单位秒，最高 300 秒。图片已提交后会等到该时间再返回超时，不切换账号。"
            />
          </Col>
          <Col xs={24} md={12} xl={6}>
            <NumberInput
              label="生图总预算"
              value="330"
              onChange={() => {}}
              placeholder="330"
              help="固定 330 秒；包含准备阶段和已提交后的 300 秒生成等待，旧配置会自动迁移。"
              disabled
            />
          </Col>
          <Col xs={24} md={12} xl={6}>
            <NumberInput
              label="突发并发保障"
              value={String(config.image_capacity_burst_parallel || "")}
              onChange={setImageCapacityBurstParallel}
              placeholder="50"
              help="号池容量评估最低按多少个同时进来的生图请求预留账号。"
            />
          </Col>
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
        <Row gutter={[16, 16]}>
          <Col xs={24} lg={8}>
            <Form.Item label="控制台日志级别" extra="不选时使用默认 info / warning / error。">
              <Checkbox.Group
                value={config.log_levels || []}
                onChange={(values) => {
                  for (const level of logLevelOptions) {
                    setLogLevel(level, values.includes(level));
                  }
                }}
              >
                <Space wrap>
                  {logLevelOptions.map((level) => (
                    <Checkbox key={level} value={level}>
                      <span className="capitalize">{level}</span>
                    </Checkbox>
                  ))}
                </Space>
              </Checkbox.Group>
            </Form.Item>
          </Col>
        </Row>

        <Divider />
        <SectionTitle title="Bark 推送通知" description="把异常调用日志和注册机最终统计推送到手机，方便第一时间排障。" />
        <Card size="small">
          <div className="mb-4 flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <Space>
              <Switch checked={barkEnabled} onChange={(checked) => setBarkNotificationField("enabled", checked)} />
              <Typography.Text strong>启用 Bark 推送</Typography.Text>
              <Tag color={barkEnabled ? "green" : "default"}>{barkEnabled ? "已启用" : "未启用"}</Tag>
            </Space>
            <Button
              icon={isTestingBarkNotification ? <LoaderCircle className="size-4 animate-spin" /> : <BellRing className="size-4" />}
              onClick={() => void testBark()}
              disabled={isTestingBarkNotification || !barkEnabled}
            >
              发送测试
            </Button>
          </div>
          <Row gutter={[16, 16]}>
            <Col xs={24} md={12}>
              <Form.Item label="Bark Server URL" extra="官方 Bark 可用 https://api.day.app，自建服务填你自己的地址。">
                <Input
                  value={String(config.notifications?.bark?.server_url || "")}
                  onChange={(event) => setBarkNotificationField("server_url", event.target.value)}
                  placeholder="https://api.day.app"
                  disabled={!barkEnabled}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={12}>
              <Form.Item label="Device Key">
                <Input.Password
                  value={String(config.notifications?.bark?.device_key || "")}
                  onChange={(event) => setBarkNotificationField("device_key", event.target.value)}
                  placeholder="Bark App 里的 key"
                  disabled={!barkEnabled}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item label="标题前缀">
                <Input
                  value={String(config.notifications?.bark?.title_prefix || "")}
                  onChange={(event) => setBarkNotificationField("title_prefix", event.target.value)}
                  placeholder="IMAGE POOL"
                  disabled={!barkEnabled}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item label="分组">
                <Input
                  value={String(config.notifications?.bark?.group || "")}
                  onChange={(event) => setBarkNotificationField("group", event.target.value)}
                  placeholder="image-pool"
                  disabled={!barkEnabled}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <NumberInput
                label="重复推送冷却"
                value={String(config.notifications?.bark?.min_interval_seconds ?? "")}
                onChange={(value) => setBarkNotificationField("min_interval_seconds", value)}
                placeholder="60"
                help="单位秒。"
                disabled={!barkEnabled}
              />
            </Col>
            <Col xs={24}>
              <Form.Item label="推送范围">
                <Space wrap>
                  <Checkbox
                    checked={Boolean(config.notifications?.bark?.notify_failed_calls !== false)}
                    onChange={(event) => setBarkNotificationField("notify_failed_calls", event.target.checked)}
                    disabled={!barkEnabled}
                  >
                    异常调用日志
                  </Checkbox>
                  <Checkbox
                    checked={Boolean(config.notifications?.bark?.notify_register !== false)}
                    onChange={(event) => setBarkNotificationField("notify_register", event.target.checked)}
                    disabled={!barkEnabled}
                  >
                    注册机最终统计
                  </Checkbox>
                  <Checkbox
                    checked={Boolean(config.notifications?.bark?.notify_register_errors_only)}
                    onChange={(event) => setBarkNotificationField("notify_register_errors_only", event.target.checked)}
                    disabled={!barkEnabled || !config.notifications?.bark?.notify_register}
                  >
                    注册机仅推失败统计
                  </Checkbox>
                </Space>
              </Form.Item>
            </Col>
          </Row>
        </Card>
      </Form>
    </Card>
  );
}
