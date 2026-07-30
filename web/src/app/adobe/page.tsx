"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent } from "react";
import { Button, Empty, Form, Image, Input, InputNumber, Modal, Progress, Segmented, Select, Space, Switch, Table, Tabs, Tag, Timeline, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { CirclePlus, Coins, FileJson, ImageIcon, KeyRound, LoaderCircle, RefreshCw, Trash2, Upload } from "lucide-react";
import { toast } from "sonner";

import {
  createAdobeRoute,
  deleteAdobeAccount,
  deleteAdobeRoute,
  fetchAdobeAccounts,
  fetchAdobeRoutes,
  fetchAdobeTestImageJob,
  fetchAdobeTokenRefreshJob,
  fetchModels,
  importAdobeAccount,
  refreshAdobeAccountCredits,
  setAdobeAccountDisabled,
  setAdobeRouteEnabled,
  startAdobeAccountTestImage,
  startAdobeTokenRefresh,
  testAdobeRoute,
  type AdobeAccount,
  type AdobeRoute,
  type AdobeTestImageJob,
  type AdobeTokenRefreshJob,
  type Model,
} from "@/lib/api";
import { formatShanghaiDateTime } from "@/lib/datetime";
import { useAuthGuard } from "@/lib/use-auth-guard";

function stateTag(state: string) {
  const color: Record<string, string> = {
    ready: "green",
    healthy: "green",
    cooling_down: "orange",
    unknown: "default",
    exhausted: "volcano",
    reauth_required: "red",
    disabled: "default",
    unhealthy: "red",
  };
  return <Tag color={color[state] || "default"}>{state || "unknown"}</Tag>;
}

function adobeImportValidationError(value: unknown) {
  if (!value || typeof value !== "object") return "Adobe 导入内容必须是 JSON 对象或数组";
  if (Array.isArray(value)) return value.length > 0 ? "" : "Adobe 导入数组不能为空";
  const object = value as Record<string, unknown>;
  const supported = ["cookie", "cookies", "tokens", "items", "profiles", "value", "token", "access_token", "endpoint"].some((key) => key in object);
  return supported ? "" : "未识别到 Adobe Token、Cookie 或刷新配置";
}

function accountLabel(account: AdobeAccount) {
  return account.display_name || account.email || account.account_id;
}

function formatCredits(value: number) {
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 2 }).format(value);
}

function AdobeConsole() {
  const [routes, setRoutes] = useState<AdobeRoute[]>([]);
  const [accounts, setAccounts] = useState<AdobeAccount[]>([]);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState("");
  const [selectedAccountIDs, setSelectedAccountIDs] = useState<string[]>([]);
  const [routeModalOpen, setRouteModalOpen] = useState(false);
  const [accountModalOpen, setAccountModalOpen] = useState(false);
  const [accountInput, setAccountInput] = useState("");
  const [tokenRefreshJob, setTokenRefreshJob] = useState<AdobeTokenRefreshJob | null>(null);
  const [tokenRefreshOpen, setTokenRefreshOpen] = useState(false);
  const [testImageOpen, setTestImageOpen] = useState(false);
  const [testImageAccount, setTestImageAccount] = useState<AdobeAccount | null>(null);
  const [testImageJob, setTestImageJob] = useState<AdobeTestImageJob | null>(null);
  const [adobeModels, setAdobeModels] = useState<Model[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const fileRef = useRef<HTMLInputElement | null>(null);
  const mountedRef = useRef(true);
  const [routeForm] = Form.useForm();
  const [testImageForm] = Form.useForm();
  const routeKind = Form.useWatch("kind", routeForm) || "direct";

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [routeData, accountData] = await Promise.all([fetchAdobeRoutes(), fetchAdobeAccounts()]);
      if (!mountedRef.current) return;
      setRoutes(routeData.items || []);
      setAccounts(accountData.items || []);
      setSelectedAccountIDs((current) => current.filter((id) => (accountData.items || []).some((account) => account.account_id === id)));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载 Adobe 号池失败");
    } finally {
      if (mountedRef.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    void load();
    return () => {
      mountedRef.current = false;
    };
  }, [load]);

  const readImportFiles = useCallback(async (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files || []);
    event.target.value = "";
    if (!files.length) return;
    try {
      const parsed = await Promise.all(files.map(async (file) => JSON.parse(await file.text()) as unknown));
      const items = parsed.flatMap((value) => (Array.isArray(value) ? value : [value]));
      setAccountInput(JSON.stringify(items.length === 1 ? items[0] : { items }, null, 2));
    } catch {
      toast.error("所选文件不是有效 JSON");
    }
  }, []);

  const submitAccountImport = useCallback(async () => {
    let payload: unknown;
    try {
      payload = JSON.parse(accountInput);
    } catch {
      toast.error("Adobe 导入内容不是有效 JSON");
      return;
    }
    const validationError = adobeImportValidationError(payload);
    if (validationError) {
      toast.error(validationError);
      return;
    }
    setPending("account-import");
    try {
      const response = await importAdobeAccount(payload);
      if (response.failed_count > 0) {
        const firstFailure = response.failures?.[0]?.error;
        toast.warning(`成功 ${response.imported_count} 个，失败 ${response.failed_count} 个${firstFailure ? `：${firstFailure}` : ""}`);
      }
      else toast.success(`已导入 ${response.imported_count} 个 Adobe 账号`);
      setAccountModalOpen(false);
      setAccountInput("");
      await load(true);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "导入 Adobe 账号失败");
    } finally {
      setPending("");
    }
  }, [accountInput, load]);

  const runTokenRefresh = useCallback(async (accountIDs: string[]) => {
    setPending("token-refresh");
    setTokenRefreshJob(null);
    setTokenRefreshOpen(true);
    try {
      const started = await startAdobeTokenRefresh(accountIDs);
      if (!mountedRef.current) return;
      setTokenRefreshJob(started.item);
      let current = started.item;
      while (current.status === "running" && mountedRef.current) {
        await new Promise((resolve) => window.setTimeout(resolve, 750));
        current = (await fetchAdobeTokenRefreshJob(current.id)).item;
        if (mountedRef.current) setTokenRefreshJob(current);
      }
      if (!mountedRef.current) return;
      await load(true);
      setSelectedAccountIDs([]);
      if (current.status === "succeeded") toast.success("Adobe Token 刷新完成");
      else if (current.status === "partial") toast.warning("部分 Adobe Token 刷新失败");
      else toast.error("Adobe Token 刷新失败");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Adobe Token 刷新失败");
      setTokenRefreshOpen(false);
    } finally {
      if (mountedRef.current) setPending("");
    }
  }, [load]);

  const openTestImage = useCallback(async (account: AdobeAccount) => {
    setTestImageAccount(account);
    setTestImageJob(null);
    setTestImageOpen(true);
    setModelsLoading(true);
    try {
      let models = adobeModels;
      if (!models.length) {
        models = (await fetchModels()).data.filter((model) => model.owned_by === "adobe-firefly");
        setAdobeModels(models);
      }
      testImageForm.setFieldsValue({ prompt: "", model: models[0]?.id, quality: "standard" });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载 Adobe 模型失败");
    } finally {
      setModelsLoading(false);
    }
  }, [adobeModels, testImageForm]);

  const runTestImage = useCallback(async () => {
    if (!testImageAccount) return;
    const values = await testImageForm.validateFields();
    setPending("test-image");
    try {
      const started = await startAdobeAccountTestImage(testImageAccount.account_id, values);
      setTestImageJob(started.item);
      let current = started.item;
      while (current.status === "running" && mountedRef.current) {
        await new Promise((resolve) => window.setTimeout(resolve, 800));
        current = (await fetchAdobeTestImageJob(current.id)).item;
        if (mountedRef.current) setTestImageJob(current);
      }
      await load(true);
      if (current.status === "succeeded") toast.success("Adobe 测试图片生成完成");
      else toast.error(current.error || "Adobe 测试图片生成失败");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "启动 Adobe 测试生图失败");
    } finally {
      if (mountedRef.current) setPending("");
    }
  }, [load, testImageAccount, testImageForm]);

  const runCreditsRefresh = useCallback(async (account: AdobeAccount) => {
    setPending(`credits-${account.account_id}`);
    try {
      await refreshAdobeAccountCredits(account.account_id);
      await load(true);
      toast.success("Adobe 积分已刷新");
    } catch (error) {
      await load(true);
      toast.error(error instanceof Error ? error.message : "Adobe 积分刷新失败");
    } finally {
      if (mountedRef.current) setPending("");
    }
  }, [load]);

  const accountColumns = useMemo<ColumnsType<AdobeAccount>>(() => [
    {
      title: "账号", key: "account", width: 240,
      render: (_, item) => <div><div className="font-medium text-slate-900">{accountLabel(item)}</div><Typography.Text type="secondary" copyable className="text-xs">{item.account_id}</Typography.Text></div>,
    },
    { title: "状态", dataIndex: "state", width: 130, render: stateTag },
    { title: "线路", dataIndex: "route_affinity", width: 190, ellipsis: true },
    {
      title: "积分", key: "credits", width: 150,
      render: (_, item) => {
        if (item.credits_error) return <Typography.Text type="danger" ellipsis={{ tooltip: item.credits_error }}>查询失败</Typography.Text>;
        if (typeof item.credits_available !== "number") return <Typography.Text type="secondary">待查询</Typography.Text>;
        const detail = [
          item.credits_available_until ? `有效期 ${formatShanghaiDateTime(item.credits_available_until)}` : "",
          item.credits_updated_at ? `更新于 ${formatShanghaiDateTime(item.credits_updated_at)}` : "",
        ].filter(Boolean).join(" · ");
        return <div title={detail}><Typography.Text strong>{formatCredits(item.credits_available)}</Typography.Text><Typography.Text type="secondary"> / {typeof item.credits_total === "number" ? formatCredits(item.credits_total) : "-"}</Typography.Text></div>;
      },
    },
    { title: "Token 过期", dataIndex: "token_expires_at", width: 165, render: (value?: string) => value ? formatShanghaiDateTime(value) : "未知" },
    { title: "最近使用", dataIndex: "last_used_at", width: 165, render: (value?: string) => value ? formatShanghaiDateTime(value) : "未使用" },
    {
      title: "失败", key: "failure", width: 210,
      render: (_, item) => item.last_error ? <Typography.Text type="danger" ellipsis={{ tooltip: item.last_error }}>{item.last_error_code || item.last_error}</Typography.Text> : `${item.consecutive_failures || 0} 次`,
    },
    {
      title: "操作", key: "actions", fixed: "right", width: 245,
      render: (_, item) => <Space size={4}>
        <Button size="small" title="测试生图" icon={<ImageIcon className="size-3.5" />} disabled={Boolean(pending) || item.disabled || item.state !== "ready"} onClick={() => void openTestImage(item)} />
        <Button size="small" title="刷新积分" icon={<Coins className="size-3.5" />} loading={pending === `credits-${item.account_id}`} disabled={Boolean(pending) && pending !== `credits-${item.account_id}`} onClick={() => void runCreditsRefresh(item)} />
        <Button size="small" title="刷新 Token" icon={<KeyRound className="size-3.5" />} disabled={Boolean(pending) || item.disabled || !item.refreshable} onClick={() => void runTokenRefresh([item.account_id])} />
        <Button size="small" disabled={Boolean(pending)} onClick={async () => {
          setPending(`account-${item.account_id}`);
          try { await setAdobeAccountDisabled(item.account_id, !item.disabled); await load(true); } catch (error) { toast.error(error instanceof Error ? error.message : "更新账号失败"); } finally { setPending(""); }
        }}>{item.disabled ? "启用" : "禁用"}</Button>
        <Button size="small" danger title="删除账号" icon={<Trash2 className="size-3.5" />} disabled={Boolean(pending)} onClick={() => Modal.confirm({
          title: "删除 Adobe 账号", content: `确认删除 ${accountLabel(item)}？`, okText: "删除", okButtonProps: { danger: true }, cancelText: "取消",
          onOk: async () => { await deleteAdobeAccount(item.account_id); await load(true); },
        })} />
      </Space>,
    },
  ], [load, openTestImage, pending, runCreditsRefresh, runTokenRefresh]);

  const routeColumns = useMemo<ColumnsType<AdobeRoute>>(() => [
    { title: "线路", dataIndex: "name", width: 190 },
    { title: "类型", dataIndex: "kind", width: 90, render: (kind: string) => <Tag>{kind === "direct" ? "直连" : "代理"}</Tag> },
    { title: "地区", dataIndex: "region", width: 100 },
    { title: "优先级", dataIndex: "priority", width: 90 },
    { title: "状态", dataIndex: "health_status", width: 115, render: stateTag },
    { title: "最近检测", dataIndex: "last_checked_at", width: 165, render: (value?: string) => value ? formatShanghaiDateTime(value) : "未检测" },
    { title: "错误", dataIndex: "last_error", ellipsis: true },
    {
      title: "操作", key: "actions", fixed: "right", width: 190,
      render: (_, item) => <Space size={4}>
        <Button size="small" loading={pending === `route-test-${item.id}`} onClick={async () => {
          setPending(`route-test-${item.id}`);
          try { const result = await testAdobeRoute(item.id); result.ok ? toast.success("线路可用") : toast.error("线路检测失败"); await load(true); } catch (error) { toast.error(error instanceof Error ? error.message : "线路检测失败"); } finally { setPending(""); }
        }}>检测</Button>
        <Switch size="small" checked={item.enabled} disabled={Boolean(pending)} onChange={async (enabled) => { setPending(`route-${item.id}`); try { await setAdobeRouteEnabled(item.id, enabled); await load(true); } finally { setPending(""); } }} />
        <Button size="small" danger icon={<Trash2 className="size-3.5" />} disabled={Boolean(pending)} onClick={async () => { try { await deleteAdobeRoute(item.id); await load(true); } catch (error) { toast.error(error instanceof Error ? error.message : "删除线路失败"); } }} />
      </Space>,
    },
  ], [load, pending]);

  return <div className="space-y-4">
    <section className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div><Typography.Title level={2} className="!mb-1 !text-2xl">Adobe 号池</Typography.Title><Typography.Text type="secondary">{accounts.filter((item) => item.state === "ready").length} 个可用账号 · {routes.filter((item) => item.health_status === "healthy").length} 条健康线路</Typography.Text></div>
        <Space wrap>
          <Button icon={<RefreshCw className="size-4" />} loading={loading} onClick={() => void load()}>刷新</Button>
          <Button icon={<CirclePlus className="size-4" />} onClick={() => setRouteModalOpen(true)}>添加线路</Button>
          <Button icon={<KeyRound className="size-4" />} loading={pending === "token-refresh"} disabled={Boolean(pending) && pending !== "token-refresh"} onClick={() => void runTokenRefresh(selectedAccountIDs)}>{selectedAccountIDs.length ? `刷新 Token (${selectedAccountIDs.length})` : "刷新全部 Token"}</Button>
          <Button type="primary" icon={<Upload className="size-4" />} onClick={() => setAccountModalOpen(true)}>导入 Adobe 账号</Button>
        </Space>
      </div>
    </section>

    <section className="rounded-lg border border-slate-200 bg-white px-4 pb-4 shadow-sm">
      <Tabs items={[
        { key: "accounts", label: `Adobe 账号 (${accounts.length})`, children: <Table rowKey="account_id" size="small" loading={loading} columns={accountColumns} dataSource={accounts} pagination={{ pageSize: 20 }} scroll={{ x: 1510 }} locale={{ emptyText: <Empty description="暂无 Adobe 账号" /> }} rowSelection={{ selectedRowKeys: selectedAccountIDs, getCheckboxProps: (item) => ({ disabled: !item.refreshable || item.disabled }), onChange: (keys) => setSelectedAccountIDs(keys.map(String)) }} /> },
        { key: "routes", label: `线路池 (${routes.length})`, children: <Table rowKey="id" size="small" loading={loading} columns={routeColumns} dataSource={routes} pagination={false} scroll={{ x: 1050 }} locale={{ emptyText: <Empty description="暂无 Adobe 线路" /> }} /> },
      ]} />
    </section>

    <Modal title="Adobe Token 刷新" open={tokenRefreshOpen} onCancel={() => tokenRefreshJob?.status !== "running" && setTokenRefreshOpen(false)} closable={tokenRefreshJob?.status !== "running"} maskClosable={false} width={700} footer={<Button disabled={tokenRefreshJob?.status === "running"} onClick={() => setTokenRefreshOpen(false)}>关闭</Button>}>
      <div className="space-y-4 pt-2">
        <div className="flex items-center justify-between gap-3"><Typography.Text strong>{tokenRefreshJob?.message || "正在创建刷新任务"}</Typography.Text><Typography.Text type="secondary">{tokenRefreshJob ? `${tokenRefreshJob.completed}/${tokenRefreshJob.total}` : "0/0"}</Typography.Text></div>
        <Progress percent={tokenRefreshJob?.percent || 0} status={tokenRefreshJob?.status === "failed" ? "exception" : tokenRefreshJob?.status === "succeeded" ? "success" : "active"} />
        <Timeline items={(tokenRefreshJob?.events || []).map((event) => ({ color: event.status === "failed" ? "red" : "green", children: <div><div>{event.account_id || "Adobe"} · {event.message}</div><Typography.Text type="secondary" className="text-xs">{formatShanghaiDateTime(event.at)}</Typography.Text></div> }))} />
      </div>
    </Modal>

    <Modal title="添加 Adobe 线路" open={routeModalOpen} onCancel={() => setRouteModalOpen(false)} onOk={() => routeForm.submit()} okText="添加" cancelText="取消" confirmLoading={pending === "route-create"}>
      <Form form={routeForm} layout="vertical" initialValues={{ kind: "direct", region: "auto", priority: 100 }} onFinish={async (values) => {
        setPending("route-create");
        try { await createAdobeRoute({ ...values, proxy_url: values.kind === "direct" ? "direct://" : values.proxy_url }); toast.success("Adobe 线路已添加"); setRouteModalOpen(false); routeForm.resetFields(); await load(true); } catch (error) { toast.error(error instanceof Error ? error.message : "添加线路失败"); } finally { setPending(""); }
      }}>
        <Form.Item name="name" label="线路名称" rules={[{ required: true }]}><Input placeholder="例如 上海出口 1" /></Form.Item>
        <Form.Item name="kind" label="线路类型"><Segmented block options={[{ label: "本机直连", value: "direct" }, { label: "上游代理", value: "proxy" }]} /></Form.Item>
        {routeKind === "proxy" ? <Form.Item name="proxy_url" label="代理 URL" rules={[{ required: true }]}><Input.Password placeholder="http://user:pass@host:port 或 socks5://host:port" /></Form.Item> : null}
        <div className="grid grid-cols-2 gap-3"><Form.Item name="region" label="地区"><Input /></Form.Item><Form.Item name="priority" label="优先级"><InputNumber className="w-full" min={1} max={10000} /></Form.Item></div>
      </Form>
    </Modal>

    <Modal title="导入 Adobe 账号" open={accountModalOpen} onCancel={() => setAccountModalOpen(false)} onOk={() => void submitAccountImport()} okText="导入" cancelText="取消" okButtonProps={{ disabled: !accountInput.trim() }} confirmLoading={pending === "account-import"} width={760}>
      <div className="mb-3 flex items-center justify-between"><Typography.Text strong>adobe2api JSON</Typography.Text><Button icon={<FileJson className="size-4" />} onClick={() => fileRef.current?.click()}>选择 JSON</Button></div>
      <input ref={fileRef} hidden type="file" accept="application/json,.json" multiple onChange={(event) => void readImportFiles(event)} />
      <Input.TextArea value={accountInput} onChange={(event) => setAccountInput(event.target.value)} autoSize={{ minRows: 12, maxRows: 24 }} placeholder="粘贴 tokens.json、Cookie JSON 或 adobe_refresh_profile" />
    </Modal>

    <Modal title={testImageAccount ? `测试生图 · ${accountLabel(testImageAccount)}` : "测试生图"} open={testImageOpen} onCancel={() => testImageJob?.status !== "running" && setTestImageOpen(false)} width={780} footer={testImageJob ? <Button disabled={testImageJob.status === "running"} onClick={() => setTestImageOpen(false)}>关闭</Button> : <Space><Button onClick={() => setTestImageOpen(false)}>取消</Button><Button type="primary" loading={pending === "test-image"} onClick={() => void runTestImage()}>开始生成</Button></Space>}>
      {testImageJob ? <div className="space-y-4"><Progress percent={testImageJob.percent} status={testImageJob.status === "failed" ? "exception" : testImageJob.status === "succeeded" ? "success" : "active"} /><Typography.Text>{testImageJob.message}</Typography.Text>{testImageJob.image_data_url ? <div className="flex justify-center rounded border border-slate-200 bg-slate-50 p-3"><Image src={testImageJob.image_data_url} alt="Adobe 测试生成结果" className="max-h-[420px] object-contain" /></div> : null}<Timeline items={testImageJob.events.map((event) => ({ children: `${event.message} · ${formatShanghaiDateTime(event.at)}` }))} /></div> : <Form form={testImageForm} layout="vertical"><Form.Item name="model" label="模型" rules={[{ required: true }]}><Select loading={modelsLoading} options={adobeModels.map((model) => ({ label: model.id, value: model.id }))} /></Form.Item><Form.Item name="quality" label="质量"><Select options={[{ label: "标准", value: "standard" }, { label: "高质量", value: "high" }]} /></Form.Item><Form.Item name="prompt" label="提示词" rules={[{ required: true }]}><Input.TextArea autoSize={{ minRows: 5, maxRows: 10 }} maxLength={1200} showCount /></Form.Item></Form>}
    </Modal>
  </div>;
}

export default function AdobePage() {
	const { isCheckingAuth, session } = useAuthGuard(["admin"]);
	if (isCheckingAuth || !session) return <div className="flex min-h-[50vh] items-center justify-center"><LoaderCircle className="size-6 animate-spin text-slate-400" /></div>;
	return <AdobeConsole />;
}
