"use client";

import { Alert, Button, Card, Checkbox, Col, Divider, Empty, Input, Row, Select, Space, Spin, Tag, Typography } from "antd";
import { LoaderCircle, Plus, Play, RotateCcw, Save, Square, Trash2, UserPlus } from "lucide-react";

import { formatShanghaiTime } from "@/lib/datetime";

import { useSettingsStore } from "../../settings/store";

const providerOptions = [
  { value: "cloudmail_gen", label: "cloudmail_gen" },
  { value: "cloudflare_temp_email", label: "cloudflare_temp_email" },
  { value: "tempmail_lol", label: "tempmail_lol" },
  { value: "moemail", label: "moemail" },
  { value: "inbucket", label: "inbucket_mail" },
  { value: "duckmail", label: "duckmail" },
  { value: "gptmail", label: "gptmail(未测试)" },
  { value: "yyds_mail", label: "yyds_mail" },
  { value: "ddg_mail", label: "ddg_mail (DDG邮箱+CF中转)" },
  { value: "outlook_token", label: "outlook_token (Outlook/Hotmail邮箱池)" },
];

function textList(value: unknown) {
  return Array.isArray(value) ? value.map(String).join("\n") : "";
}

function splitList(value: string) {
  return value.split(/[\n,]/).map((item) => item.trim());
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Typography.Text type="secondary" className="text-sm">{label}</Typography.Text>
      {children}
    </div>
  );
}

export function RegisterCard() {
  const config = useSettingsStore((state) => state.registerConfig);
  const isLoading = useSettingsStore((state) => state.isLoadingRegister);
  const isSaving = useSettingsStore((state) => state.isSavingRegister);
  const setProxy = useSettingsStore((state) => state.setRegisterProxy);
  const setTotal = useSettingsStore((state) => state.setRegisterTotal);
  const setThreads = useSettingsStore((state) => state.setRegisterThreads);
  const setMode = useSettingsStore((state) => state.setRegisterMode);
  const setTargetQuota = useSettingsStore((state) => state.setRegisterTargetQuota);
  const setTargetAvailable = useSettingsStore((state) => state.setRegisterTargetAvailable);
  const setCheckInterval = useSettingsStore((state) => state.setRegisterCheckInterval);
  const setMailField = useSettingsStore((state) => state.setRegisterMailField);
  const addProvider = useSettingsStore((state) => state.addRegisterProvider);
  const updateProvider = useSettingsStore((state) => state.updateRegisterProvider);
  const deleteProvider = useSettingsStore((state) => state.deleteRegisterProvider);
  const save = useSettingsStore((state) => state.saveRegister);
  const toggle = useSettingsStore((state) => state.toggleRegister);
  const reset = useSettingsStore((state) => state.resetRegister);
  const resetOutlookPool = useSettingsStore((state) => state.resetOutlookPool);

  if (isLoading) {
    return (
      <Card>
        <div className="flex min-h-48 items-center justify-center">
          <Spin />
        </div>
      </Card>
    );
  }

  if (!config) return null;

  const stats = config.stats || { success: 0, fail: 0, done: 0, running: 0, threads: config.threads };
  const providers = config.mail.providers || [];
  const logs = config.logs || [];
  const updateProviderType = (index: number, type: string) => {
    updateProvider(index, {
      type,
      enable: true,
      ...(type === "cloudmail_gen" ? { api_base: "", admin_email: "", admin_password: "", domain: [], subdomain: [], email_prefix: "" } : {}),
      ...(type === "cloudflare_temp_email" ? { api_base: "", admin_password: "", domain: [] } : {}),
      ...(type === "tempmail_lol" ? { api_key: "", domain: [] } : {}),
      ...(type === "moemail" ? { api_base: "", api_key: "", domain: [] } : {}),
      ...(type === "inbucket" ? { api_base: "", domain: [], random_subdomain: true } : {}),
      ...(type === "duckmail" ? { api_key: "", default_domain: "duckmail.sbs" } : {}),
      ...(type === "gptmail" ? { api_key: "", default_domain: "" } : {}),
      ...(type === "yyds_mail" ? { api_base: "https://maliapi.215.im/v1", api_key: "", domain: [], subdomain: "", wildcard: false } : {}),
      ...(type === "ddg_mail" ? { ddg_token: "", cf_inbox_jwt: "", cf_domain: [], admin_password: "" } : {}),
      ...(type === "outlook_token" ? { mailboxes: "", mode: "graph", imap_host: "outlook.office365.com", message_limit: 10 } : {}),
    });
  };

  return (
    <Row gutter={[24, 24]} align="top" className="register-console mt-6">
      <Col xs={24} xl={15}>
        <Card
          title={
            <Space>
              <UserPlus className="size-5 text-slate-500" />
              <span>注册配置</span>
            </Space>
          }
          extra={
            <Button type="primary" icon={isSaving ? <LoaderCircle className="size-4 animate-spin" /> : <Save className="size-4" />} onClick={() => void save()} disabled={isSaving || config.enabled}>
              保存配置
            </Button>
          }
        >
          <Row gutter={[16, 16]}>
            <Col xs={24} md={8}>
              <Field label="注册模式">
                <Select
                  className="w-full"
                  value={config.mode || "total"}
                  onChange={(value) => setMode(value as "total" | "quota" | "available")}
                  disabled={config.enabled}
                  options={[
                    { value: "total", label: "注册总数" },
                    { value: "quota", label: "号池剩余额度" },
                    { value: "available", label: "可用账号数量" },
                  ]}
                />
              </Field>
            </Col>
            <Col xs={24} md={8}>
              <Field label="注册总数">
                <Input value={String(config.total)} onChange={(event) => setTotal(event.target.value)} disabled={config.enabled || config.mode !== "total"} />
              </Field>
            </Col>
            <Col xs={24} md={8}>
              <Field label="线程数">
                <Input value={String(config.threads)} onChange={(event) => setThreads(event.target.value)} disabled={config.enabled} />
              </Field>
            </Col>
            <Col xs={24} md={8}>
              <Field label="注册代理">
                <Input
                  value={config.proxy}
                  onChange={(event) => setProxy(event.target.value)}
                  placeholder="http://127.0.0.1:7890"
                  disabled={config.enabled}
                />
              </Field>
            </Col>
            <Col xs={24} md={8}>
              <Field label="目标剩余额度">
                <Input value={String(config.target_quota || "")} onChange={(event) => setTargetQuota(event.target.value)} disabled={config.enabled || config.mode !== "quota"} />
              </Field>
            </Col>
            <Col xs={24} md={8}>
              <Field label="目标可用账号">
                <Input value={String(config.target_available || "")} onChange={(event) => setTargetAvailable(event.target.value)} disabled={config.enabled || config.mode !== "available"} />
              </Field>
            </Col>
            <Col xs={24} md={8}>
              <Field label="检查间隔（秒）">
                <Input value={String(config.check_interval || "")} onChange={(event) => setCheckInterval(event.target.value)} disabled={config.enabled || config.mode === "total"} />
              </Field>
            </Col>
          </Row>

          <Alert
            className="mt-4"
            type="info"
            showIcon
            message="注册默认启用 FlareSolverr"
            description={`当前${config.flaresolverr?.enabled === false ? "未开启" : "已开启"} FlareSolverr 预热；注册地址优先走你填写的“注册代理”，未填写时按全局代理或直连处理。`}
          />

          <Divider className="!my-5" />

          <div className="mb-4 flex items-center justify-between gap-3">
            <div>
              <Typography.Title level={5} className="!mb-0">邮箱配置</Typography.Title>
              <Typography.Text type="secondary">可配置多个 provider，按启用顺序轮换。</Typography.Text>
            </div>
            <Button icon={<Plus className="size-4" />} onClick={addProvider} disabled={config.enabled}>添加</Button>
          </div>

          <Row gutter={[16, 16]}>
            <Col xs={24} md={8}>
              <Field label="请求超时">
                <Input value={String(config.mail.request_timeout || "")} onChange={(event) => setMailField("request_timeout", event.target.value)} disabled={config.enabled} />
              </Field>
            </Col>
            <Col xs={24} md={8}>
              <Field label="等待验证码超时">
                <Input value={String(config.mail.wait_timeout || "")} onChange={(event) => setMailField("wait_timeout", event.target.value)} disabled={config.enabled} />
              </Field>
            </Col>
            <Col xs={24} md={8}>
              <Field label="轮询间隔">
                <Input value={String(config.mail.wait_interval || "")} onChange={(event) => setMailField("wait_interval", event.target.value)} disabled={config.enabled} />
              </Field>
            </Col>
          </Row>

          <div className="mt-5 space-y-4">
            {providers.map((provider, index) => {
              const type = String(provider.type || "tempmail_lol");
              const domains = textList(provider.domain);
              const subdomains = textList(provider.subdomain);
              return (
                <Card
                  key={index}
                  size="small"
                  title={
                    <Space>
                      <Checkbox checked={Boolean(provider.enable)} onChange={(event) => updateProvider(index, { enable: event.target.checked })} disabled={config.enabled} />
                      <span>Provider #{index + 1}</span>
                      <Tag>{type}</Tag>
                    </Space>
                  }
                  extra={
                    <Button
                      danger
                      type="text"
                      icon={<Trash2 className="size-4" />}
                      onClick={() => deleteProvider(index)}
                      disabled={config.enabled || providers.length <= 1}
                    />
                  }
                >
                  <Row gutter={[16, 16]}>
                    <Col xs={24} md={12}>
                      <Field label="类型">
                        <Select className="w-full" value={type} onChange={(value) => updateProviderType(index, value)} disabled={config.enabled} options={providerOptions} />
                      </Field>
                    </Col>
                    {type === "cloudmail_gen" || type === "cloudflare_temp_email" || type === "moemail" || type === "inbucket" || type === "yyds_mail" || type === "ddg_mail" ? (
                      <Col xs={24} md={12}>
                        <Field label={type === "cloudmail_gen" ? "CloudMail URL" : "API Base"}>
                          <Input value={String(provider.api_base || "")} onChange={(event) => updateProvider(index, { api_base: event.target.value })} disabled={config.enabled} />
                        </Field>
                      </Col>
                    ) : null}
                    {type === "cloudmail_gen" ? (
                      <>
                        <Col xs={24} md={12}>
                          <Field label="管理员邮箱">
                            <Input value={String(provider.admin_email || "")} onChange={(event) => updateProvider(index, { admin_email: event.target.value })} disabled={config.enabled} />
                          </Field>
                        </Col>
                        <Col xs={24} md={12}>
                          <Field label="管理员密码">
                            <Input value={String(provider.admin_password || "")} onChange={(event) => updateProvider(index, { admin_password: event.target.value })} disabled={config.enabled} />
                          </Field>
                        </Col>
                      </>
                    ) : null}
                    {type === "cloudflare_temp_email" || type === "ddg_mail" ? (
                      <Col xs={24} md={12}>
                        <Field label="Admin Password">
                          <Input value={String(provider.admin_password || "")} onChange={(event) => updateProvider(index, { admin_password: event.target.value })} disabled={config.enabled} />
                        </Field>
                      </Col>
                    ) : null}
                    {type === "ddg_mail" ? (
                      <>
                        <Col xs={24} md={12}>
                          <Field label="DDG Token">
                            <Input value={String(provider.ddg_token || "")} onChange={(event) => updateProvider(index, { ddg_token: event.target.value })} disabled={config.enabled} placeholder="DuckDuckGo Email Protection 的 Bearer Token" />
                          </Field>
                        </Col>
                        <Col xs={24} md={12}>
                          <Field label="CF Inbox JWT">
                            <Input value={String(provider.cf_inbox_jwt || "")} onChange={(event) => updateProvider(index, { cf_inbox_jwt: event.target.value })} disabled={config.enabled} placeholder="CF 临时邮箱后端固定收件箱 JWT" />
                          </Field>
                        </Col>
                        <Col xs={24}>
                          <Alert
                            type="warning"
                            showIcon
                            title="DDG 邮箱使用说明"
                            description="先在 DuckDuckGo Email Protection 登录并设置转发目标为 CF 收件箱地址；DDG Token 从 quack.duckduckgo.com 请求 Authorization 获取；CF Inbox JWT 从固定收件箱获取。"
                          />
                        </Col>
                      </>
                    ) : null}
                    {type === "inbucket" ? (
                      <Col xs={24} md={12}>
                        <Checkbox checked={Boolean(provider.random_subdomain ?? true)} onChange={(event) => updateProvider(index, { random_subdomain: event.target.checked })} disabled={config.enabled}>
                          启用随机子域名
                        </Checkbox>
                      </Col>
                    ) : null}
                    {type === "tempmail_lol" || type === "moemail" || type === "duckmail" || type === "gptmail" || type === "yyds_mail" ? (
                      <Col xs={24} md={12}>
                        <Field label="API Key">
                          <Input value={String(provider.api_key || "")} onChange={(event) => updateProvider(index, { api_key: event.target.value })} disabled={config.enabled} />
                        </Field>
                      </Col>
                    ) : null}
                    {type === "duckmail" || type === "gptmail" ? (
                      <Col xs={24} md={12}>
                        <Field label="Default Domain">
                          <Input value={String(provider.default_domain || "")} onChange={(event) => updateProvider(index, { default_domain: event.target.value })} placeholder={type === "duckmail" ? "duckmail.sbs" : ""} disabled={config.enabled} />
                        </Field>
                      </Col>
                    ) : null}
                    {type === "yyds_mail" ? (
                      <>
                        <Col xs={24} md={12}>
                          <Field label="Subdomain">
                            <Input value={String(provider.subdomain || "")} onChange={(event) => updateProvider(index, { subdomain: event.target.value })} disabled={config.enabled} />
                          </Field>
                        </Col>
                        <Col xs={24} md={12}>
                          <Checkbox checked={Boolean(provider.wildcard)} onChange={(event) => updateProvider(index, { wildcard: event.target.checked })} disabled={config.enabled}>
                            Wildcard
                          </Checkbox>
                        </Col>
                      </>
                    ) : null}
                    {type === "outlook_token" ? (
                      <>
                        <Col xs={24} md={12}>
                          <Field label="读取方式">
                            <Select
                              className="w-full"
                              value={String(provider.mode || "graph")}
                              onChange={(value) => updateProvider(index, { mode: value })}
                              disabled={config.enabled}
                              options={[
                                { value: "graph", label: "Graph API" },
                                { value: "imap", label: "IMAP (XOAUTH2)" },
                                { value: "auto", label: "自动 (Graph→IMAP)" },
                              ]}
                            />
                          </Field>
                        </Col>
                        {String(provider.mode || "graph") !== "graph" ? (
                          <Col xs={24} md={12}>
                            <Field label="IMAP Host">
                              <Input value={String(provider.imap_host || "outlook.office365.com")} onChange={(event) => updateProvider(index, { imap_host: event.target.value })} disabled={config.enabled} />
                            </Field>
                          </Col>
                        ) : null}
                        <Col xs={24} md={12}>
                          <Field label="读取邮件数量">
                            <Input value={String(provider.message_limit || 10)} onChange={(event) => updateProvider(index, { message_limit: Number(event.target.value) || 10 })} disabled={config.enabled} />
                          </Field>
                        </Col>
                        <Col xs={24}>
                          <Field label="邮箱池导入">
                            <Input.TextArea
                              value={String(provider.mailboxes || "")}
                              onChange={(event) => updateProvider(index, { mailboxes: event.target.value })}
                              placeholder={"每行一个邮箱，格式：\nemail----password----client_id----refresh_token\n已保存的密码/refresh_token 不会回显；这里用于新增或覆盖同名邮箱。"}
                              autoSize={{ minRows: 5, maxRows: 10 }}
                              disabled={config.enabled}
                            />
                          </Field>
                          <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
                            <Tag>已保存 {Number(provider.mailboxes_count || 0)}</Tag>
                            <Tag color="default">未使用 {Number((provider.mailboxes_stats as Record<string, number> | undefined)?.unused || 0)}</Tag>
                            <Tag color="processing">占用中 {Number((provider.mailboxes_stats as Record<string, number> | undefined)?.in_use || 0)}</Tag>
                            <Tag color="success">已用 {Number((provider.mailboxes_stats as Record<string, number> | undefined)?.used || 0)}</Tag>
                            <Tag color="warning">token失效 {Number((provider.mailboxes_stats as Record<string, number> | undefined)?.token_invalid || 0)}</Tag>
                            <Tag color="error">失败 {Number((provider.mailboxes_stats as Record<string, number> | undefined)?.failed || 0)}</Tag>
                          </div>
                          {Array.isArray(provider.mailboxes_preview) && provider.mailboxes_preview.length > 0 ? (
                            <Typography.Text type="secondary" className="mt-2 block text-xs">
                              已保存邮箱（脱敏）：{provider.mailboxes_preview.slice(0, 8).join("、")}{provider.mailboxes_preview.length > 8 ? ` 等 ${provider.mailboxes_preview.length} 个` : ""}
                            </Typography.Text>
                          ) : null}
                          <Space wrap className="mt-3">
                            <Button size="small" onClick={() => void resetOutlookPool("failed")} disabled={config.enabled}>
                              清除失败/占用状态
                            </Button>
                            <Button size="small" danger onClick={() => {
                              if (window.confirm("确定要重置整个 Outlook 邮箱池状态吗？所有邮箱会被标记为可重新使用。")) void resetOutlookPool("all");
                            }} disabled={config.enabled}>
                              重置全部状态
                            </Button>
                            <Button size="small" danger onClick={() => {
                              if (window.confirm("确定要从 Outlook 邮箱池中删除所有未使用邮箱吗？此操作会移除这些已保存凭据。")) void resetOutlookPool("unused");
                            }} disabled={config.enabled}>
                              清空未使用
                            </Button>
                          </Space>
                          <Alert className="mt-3" type="info" showIcon message="Outlook 邮箱池格式为 email----password----client_id----refresh_token；成功注册后邮箱会标记 used，不会重复使用。" />
                        </Col>
                      </>
                    ) : null}
                    {type === "cloudmail_gen" || type === "tempmail_lol" || type === "cloudflare_temp_email" || type === "moemail" || type === "inbucket" || type === "yyds_mail" || type === "ddg_mail" ? (
                      <Col xs={24}>
                        <Field label={type === "cloudmail_gen" ? "邮箱域名" : type === "inbucket" ? "基础域名列表" : "Domain"}>
                          <Input.TextArea
                            value={domains}
                            onChange={(event) => updateProvider(index, { domain: splitList(event.target.value) })}
                            placeholder={type === "cloudmail_gen" ? "每行一个域名，留空则使用服务默认域名" : type === "inbucket" ? "每行一个基础域名，系统会自动生成随机子域名" : "每行一个域名，留空则使用服务默认域名"}
                            autoSize={{ minRows: 3, maxRows: 8 }}
                            disabled={config.enabled}
                          />
                        </Field>
                      </Col>
                    ) : null}
                    {type === "cloudmail_gen" ? (
                      <Col xs={24}>
                        <Field label="子域名（支持多个）">
                          <Input.TextArea
                            value={subdomains}
                            onChange={(event) => updateProvider(index, { subdomain: splitList(event.target.value) })}
                            placeholder="每行一个子域名前缀，留空则直接使用主域名"
                            autoSize={{ minRows: 3, maxRows: 8 }}
                            disabled={config.enabled}
                          />
                        </Field>
                      </Col>
                    ) : null}
                  </Row>
                </Card>
              );
            })}
          </div>
        </Card>
      </Col>

      <Col xs={24} xl={9}>
        <Card
          title="运行结果"
          extra={<Tag color={config.enabled ? "success" : "default"}>{config.enabled ? "运行中" : "已停止"}</Tag>}
        >
          <Typography.Text type="secondary">SSE 实时推送当前状态。</Typography.Text>
          <Alert className="mt-4" type="warning" showIcon title="启动之前注意先保存配置。" />
          <div className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-4">
            {[
              ["成功 / 成功率", `${stats.success} / ${stats.success_rate || 0}%`],
              ["失败", stats.fail],
              ["完成", stats.done],
              ["运行 / 线程", `${stats.running} / ${stats.threads}`],
              ["运行时间", `${stats.elapsed_seconds || 0}s`],
              ["平均注册单个", `${stats.avg_seconds || 0}s`],
              ["当前额度", stats.current_quota || 0],
              ["正常账号", stats.current_available || 0],
            ].map(([label, value]) => (
              <div key={label} className="min-w-0 rounded-lg border border-slate-100 bg-slate-50/70 px-3 py-2">
                <div className="truncate text-xs text-slate-500">{label}</div>
                <div className="mt-1 text-sm font-semibold leading-none text-slate-900">{value}</div>
              </div>
            ))}
          </div>

          <Space className="mt-5 w-full" wrap>
            <Button type="primary" icon={isSaving ? <LoaderCircle className="size-4 animate-spin" /> : config.enabled ? <Square className="size-4" /> : <Play className="size-4" />} onClick={() => void toggle()} disabled={isSaving}>
              {config.enabled ? "停止" : "启动"}
            </Button>
            <Button icon={<RotateCcw className="size-4" />} onClick={() => void reset()} disabled={isSaving || config.enabled}>重置</Button>
            <Button icon={<Save className="size-4" />} onClick={() => void save()} disabled={isSaving || config.enabled}>保存</Button>
          </Space>

          <Divider className="!my-6" />

          <div className="mb-3 flex items-center justify-between">
            <div>
              <Typography.Title level={5} className="!mb-0">实时日志</Typography.Title>
              <Typography.Text type="warning" className="text-xs">遇到 HTTP 400 等错误，通常是邮箱滥用被封，需要更换新的域名邮箱。</Typography.Text>
            </div>
            <Tag>{logs.length}</Tag>
          </div>
          <div className="h-[360px] overflow-y-auto rounded-lg border border-slate-200 bg-slate-950 p-3 font-mono text-xs leading-6 text-slate-100">
            {logs.length === 0 ? (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={<span className="text-slate-400">暂无日志</span>} />
            ) : (
              logs.slice().reverse().map((item, index) => (
                <div key={`${item.time}-${index}`} className={item.level === "red" ? "text-rose-300" : item.level === "green" ? "text-emerald-300" : item.level === "yellow" ? "text-amber-300" : "text-slate-200"}>
                  <span className="text-slate-500">{formatShanghaiTime(item.time)}</span>
                  <span className="pl-2">{item.text}</span>
                </div>
              ))
            )}
          </div>
        </Card>
      </Col>
    </Row>
  );
}
