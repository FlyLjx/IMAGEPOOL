"use client";

import { useState } from "react";
import { Alert, Button, Card, Checkbox, Col, Form, Input, Row, Select, Space, Tag, Typography } from "antd";
import { Cookie, LoaderCircle, PlugZap, Save, ShieldCheck } from "lucide-react";
import { toast } from "sonner";

import {
  testProxy,
  testProxyClearance,
  type ClearanceTestResult,
  type ProxyRuntimeClearanceMode,
  type ProxyRuntimeEgressMode,
  type ProxyTestResult,
} from "@/lib/api";

import { useSettingsStore } from "../store";

export function ProxyRuntimeCard() {
  const [isTestingProxy, setIsTestingProxy] = useState(false);
  const [isTestingClearance, setIsTestingClearance] = useState(false);
  const [proxyResult, setProxyResult] = useState<ProxyTestResult | null>(null);
  const [clearanceResult, setClearanceResult] = useState<ClearanceTestResult | null>(null);
  const [targetUrl, setTargetUrl] = useState("https://chatgpt.com");
  const config = useSettingsStore((state) => state.config);
  const isLoadingConfig = useSettingsStore((state) => state.isLoadingConfig);
  const isSavingConfig = useSettingsStore((state) => state.isSavingConfig);
  const saveConfig = useSettingsStore((state) => state.saveConfig);
  const setProxyRuntimeField = useSettingsStore((state) => state.setProxyRuntimeField);
  const setProxyRuntimeClearanceField = useSettingsStore((state) => state.setProxyRuntimeClearanceField);
  const setProxyRuntimeStatusCodesText = useSettingsStore((state) => state.setProxyRuntimeStatusCodesText);

  if (isLoadingConfig || !config?.proxy_runtime) {
    return (
      <Card>
        <div className="flex items-center justify-center py-12">
          <LoaderCircle className="size-5 animate-spin text-slate-400" />
        </div>
      </Card>
    );
  }

  const runtime = config.proxy_runtime;
  const clearance = runtime.clearance;
  const runtimeEnabled = Boolean(runtime.enabled);
  const clearanceMode = clearance.mode;
  const hasStoredClearance = Boolean(clearance.has_cf_cookies || clearance.has_cf_clearance);

  const handleTestRuntimeProxy = async () => {
    setIsTestingProxy(true);
    setProxyResult(null);
    try {
      const saved = await saveConfig();
      if (!saved) return;
      const data = await testProxy();
      setProxyResult(data.result);
      if (data.result.ok) {
        toast.success(`清障代理可用（${data.result.latency_ms} ms）`);
      } else {
        toast.error(`清障代理不可用：${data.result.error ?? "未知错误"}`);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "测试清障代理失败");
    } finally {
      setIsTestingProxy(false);
    }
  };

  const handleTestClearance = async () => {
    setIsTestingClearance(true);
    setClearanceResult(null);
    try {
      const saved = await saveConfig();
      if (!saved) return;
      const data = await testProxyClearance(targetUrl.trim() || "https://chatgpt.com");
      setClearanceResult(data.result);
      if (data.result.ok) {
        toast.success(`Clearance 获取成功（${data.result.latency_ms} ms）`);
      } else {
        toast.error(`Clearance 获取失败：${data.result.error ?? data.result.status}`);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "测试 Clearance 失败");
    } finally {
      setIsTestingClearance(false);
    }
  };

  return (
    <Card
      title={
        <Space>
          <PlugZap className="size-4 text-amber-500" />
          <span>FlareSolverr 清障代理</span>
          <Tag color={runtimeEnabled ? "green" : "default"}>{runtimeEnabled ? "已启用" : "未启用"}</Tag>
        </Space>
      }
      extra={
        <Button type="primary" icon={isSavingConfig ? <LoaderCircle className="size-4 animate-spin" /> : <Save className="size-4" />} onClick={() => void saveConfig()} disabled={isSavingConfig}>
          保存配置
        </Button>
      }
    >
      <Alert
        type="info"
        showIcon
        className="mb-5"
        message="用于注册或上游请求遇到 Cloudflare 拦截时自动获取 clearance。Cookie / cf_clearance 对外接口会脱敏，不会明文回显。"
      />

      <Form layout="vertical" requiredMark={false}>
        <Row gutter={[16, 16]}>
          <Col xs={24}>
            <Checkbox checked={runtimeEnabled} onChange={(event) => setProxyRuntimeField("enabled", event.target.checked)}>
              启用清障运行时
            </Checkbox>
          </Col>
          <Col xs={24} md={8}>
            <Form.Item label="出站模式" extra="需要固定代理出口时选择单代理。">
              <Select
                value={runtime.egress_mode}
                onChange={(value) => setProxyRuntimeField("egress_mode", value as ProxyRuntimeEgressMode)}
                disabled={!runtimeEnabled}
                options={[
                  { value: "direct", label: "直连" },
                  { value: "single_proxy", label: "单代理" },
                ]}
              />
            </Form.Item>
          </Col>
          <Col xs={24} md={8}>
            <Form.Item label="清障代理 URL">
              <Input
                value={runtime.proxy_url}
                onChange={(event) => setProxyRuntimeField("proxy_url", event.target.value)}
                placeholder="http://privoxy:8118"
                disabled={!runtimeEnabled || runtime.egress_mode !== "single_proxy"}
              />
            </Form.Item>
          </Col>
          <Col xs={24} md={8}>
            <Form.Item label="资源代理 URL" extra="留空则复用清障代理。">
              <Input
                value={runtime.resource_proxy_url}
                onChange={(event) => setProxyRuntimeField("resource_proxy_url", event.target.value)}
                placeholder="http://privoxy:8118"
                disabled={!runtimeEnabled || runtime.egress_mode !== "single_proxy"}
              />
            </Form.Item>
          </Col>
          <Col xs={24} md={8}>
            <Form.Item label="重置状态码" extra="多个用逗号分隔，默认 403。">
              <Input
                value={runtime.reset_session_status_codes.join(",")}
                onChange={(event) => setProxyRuntimeStatusCodesText(event.target.value)}
                placeholder="403"
                disabled={!runtimeEnabled}
              />
            </Form.Item>
          </Col>
          <Col xs={24} md={8}>
            <Form.Item label="SSL">
              <Checkbox
                checked={Boolean(runtime.skip_ssl_verify)}
                onChange={(event) => setProxyRuntimeField("skip_ssl_verify", event.target.checked)}
                disabled={!runtimeEnabled}
              >
                跳过 SSL 校验
              </Checkbox>
            </Form.Item>
          </Col>
          <Col xs={24} md={8}>
            <Form.Item label="代理连通性">
              <Button icon={isTestingProxy ? <LoaderCircle className="size-4 animate-spin" /> : <PlugZap className="size-4" />} onClick={() => void handleTestRuntimeProxy()} disabled={isTestingProxy || !runtimeEnabled}>
                测试当前清障代理
              </Button>
            </Form.Item>
          </Col>
          {proxyResult ? (
            <Col xs={24}>
              <Alert
                type={proxyResult.ok ? "success" : "error"}
                showIcon
                message={proxyResult.ok ? `代理可用：HTTP ${proxyResult.status}，${proxyResult.latency_ms} ms` : `代理不可用：${proxyResult.error ?? "未知错误"}`}
                description={`来源：${proxyResult.proxy_source ?? "unknown"}，是否有代理：${proxyResult.has_proxy ? "是" : "否"}`}
              />
            </Col>
          ) : null}
        </Row>

        <div className="mt-5 rounded-xl border border-slate-100 bg-slate-50/70 p-4">
          <div className="mb-4 flex items-center gap-2">
            <Cookie className="size-4 text-slate-500" />
            <Typography.Text strong>Cloudflare Clearance</Typography.Text>
            <Tag color={clearance.enabled ? "green" : "default"}>{clearance.enabled ? clearanceMode : "disabled"}</Tag>
          </div>
          <Row gutter={[16, 16]}>
            <Col xs={24} md={8}>
              <Form.Item label="模式">
                <Select
                  value={clearanceMode}
                  onChange={(value) => {
                    const mode = value as ProxyRuntimeClearanceMode;
                    setProxyRuntimeClearanceField("mode", mode);
                    setProxyRuntimeClearanceField("enabled", mode !== "none");
                  }}
                  disabled={!runtimeEnabled}
                  options={[
                    { value: "none", label: "不启用" },
                    { value: "manual", label: "手动 Cookie" },
                    { value: "flaresolverr", label: "FlareSolverr" },
                  ]}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={8}>
              <Form.Item label="FlareSolverr URL">
                <Input
                  value={clearance.flaresolverr_url}
                  onChange={(event) => setProxyRuntimeClearanceField("flaresolverr_url", event.target.value)}
                  placeholder="http://flaresolverr:8191"
                  disabled={!runtimeEnabled || clearanceMode !== "flaresolverr"}
                />
              </Form.Item>
            </Col>
            <Col xs={24} md={4}>
              <Form.Item label="超时秒数">
                <Input value={String(clearance.timeout_sec)} onChange={(event) => setProxyRuntimeClearanceField("timeout_sec", event.target.value)} disabled={!runtimeEnabled || clearanceMode === "none"} />
              </Form.Item>
            </Col>
            <Col xs={24} md={4}>
              <Form.Item label="刷新间隔">
                <Input value={String(clearance.refresh_interval)} onChange={(event) => setProxyRuntimeClearanceField("refresh_interval", event.target.value)} disabled={!runtimeEnabled || clearanceMode === "none"} />
              </Form.Item>
            </Col>
            <Col xs={24}>
              <Form.Item label="User-Agent">
                <Input value={clearance.user_agent} onChange={(event) => setProxyRuntimeClearanceField("user_agent", event.target.value)} disabled={!runtimeEnabled || clearanceMode === "none"} />
              </Form.Item>
            </Col>
            <Col xs={24}>
              <Form.Item label="手动 Cookie" extra={hasStoredClearance ? "服务端已保存过 Cookie/clearance；留空保存不会清空已有值。" : "留空表示不使用手动 Cookie。"}>
                <Input.TextArea value={clearance.cf_cookies} onChange={(event) => setProxyRuntimeClearanceField("cf_cookies", event.target.value)} autoSize={{ minRows: 3, maxRows: 6 }} disabled={!runtimeEnabled || clearanceMode !== "manual"} />
              </Form.Item>
            </Col>
            <Col xs={24} md={12}>
              <Form.Item label="单独 cf_clearance">
                <Input value={clearance.cf_clearance} onChange={(event) => setProxyRuntimeClearanceField("cf_clearance", event.target.value)} disabled={!runtimeEnabled || clearanceMode !== "manual"} />
              </Form.Item>
            </Col>
            <Col xs={24} md={12}>
              <Form.Item label="测试目标 URL">
                <Space.Compact className="w-full">
                  <Input value={targetUrl} onChange={(event) => setTargetUrl(event.target.value)} disabled={!runtimeEnabled || clearanceMode === "none"} />
                  <Button icon={isTestingClearance ? <LoaderCircle className="size-4 animate-spin" /> : <ShieldCheck className="size-4" />} onClick={() => void handleTestClearance()} disabled={isTestingClearance || !runtimeEnabled || clearanceMode === "none"}>
                    测试
                  </Button>
                </Space.Compact>
              </Form.Item>
            </Col>
            {clearanceResult ? (
              <Col xs={24}>
                <Alert
                  type={clearanceResult.ok ? "success" : "error"}
                  showIcon
                  message={clearanceResult.ok ? `Clearance 可用：${clearanceResult.latency_ms} ms` : `Clearance 不可用：${clearanceResult.error ?? clearanceResult.status}`}
                  description={`Cookie：${clearanceResult.has_cookies ? "已获取" : "未获取"}，UA：${clearanceResult.user_agent || "-"}`}
                />
              </Col>
            ) : null}
          </Row>
        </div>
      </Form>
    </Card>
  );
}
