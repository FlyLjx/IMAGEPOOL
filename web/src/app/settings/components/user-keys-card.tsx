"use client";

import { useEffect, useRef, useState } from "react";
import { Alert, Button, Card, Empty, Form, Input, InputNumber, List, Modal, Select, Space, Spin, Tag, Typography } from "antd";
import { Ban, CheckCircle2, Copy, KeyRound, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { createUserKey, deleteUserKey, fetchUserKeys, updateUserKey, type UserKey } from "@/lib/api";
import { formatShanghaiDateTime } from "@/lib/datetime";

const endpointOptions = [
  { label: "图片生成", value: "/v1/images/generations" },
  { label: "图片编辑", value: "/v1/images/edits" },
  { label: "异步图片生成", value: "/api/image-tasks/generations" },
  { label: "异步图片编辑", value: "/api/image-tasks/edits" },
  { label: "Chat Completions", value: "/v1/chat/completions" },
  { label: "Responses", value: "/v1/responses" },
  { label: "Messages", value: "/v1/messages" },
  { label: "搜索", value: "/v1/search" },
];

const modelSuggestions = [
  "gpt-image-2",
  "codex-gpt-image-2",
  "auto",
  "gpt-5",
  "gpt-5-mini",
  "gpt-5-5-thinking",
];

function emptyLimits(): UserKey["limits"] {
  return {
    daily_requests: 0,
    daily_images: 0,
    allowed_models: [],
    allowed_endpoints: [],
  };
}

function normalizeLimits(value?: UserKey["limits"] | null): UserKey["limits"] {
  return {
    daily_requests: Number(value?.daily_requests || 0),
    daily_images: Number(value?.daily_images || 0),
    allowed_models: Array.isArray(value?.allowed_models) ? value.allowed_models : [],
    allowed_endpoints: Array.isArray(value?.allowed_endpoints) ? value.allowed_endpoints : [],
  };
}

function quotaText(item: UserKey) {
  const limits = normalizeLimits(item.limits);
  const usage = item.usage || { requests: 0, images: 0 };
  const requestText = limits.daily_requests > 0 ? `${usage.requests}/${limits.daily_requests} 次` : `${usage.requests} 次`;
  const imageText = limits.daily_images > 0 ? `${usage.images}/${limits.daily_images} 图` : `${usage.images} 图`;
  return `今日 ${requestText} / ${imageText}`;
}

export function UserKeysCard() {
  const didLoadRef = useRef(false);
  const [items, setItems] = useState<UserKey[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [isDialogOpen, setIsDialogOpen] = useState(false);
  const [name, setName] = useState("");
  const [isCreating, setIsCreating] = useState(false);
  const [pendingIds, setPendingIds] = useState<Set<string>>(() => new Set());
  const [revealedKey, setRevealedKey] = useState("");
  const [editingItem, setEditingItem] = useState<UserKey | null>(null);
  const [editName, setEditName] = useState("");
  const [editKey, setEditKey] = useState("");
  const [editLimits, setEditLimits] = useState<UserKey["limits"]>(emptyLimits);

  const load = async () => {
    setIsLoading(true);
    try {
      const data = await fetchUserKeys();
      setItems(data.items);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "加载用户密钥失败");
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    if (didLoadRef.current) {
      return;
    }
    didLoadRef.current = true;
    void load();
  }, []);

  const handleCreate = async () => {
    setIsCreating(true);
    try {
      const data = await createUserKey(name.trim());
      setItems(data.items);
      setRevealedKey(data.key);
      setName("");
      setIsDialogOpen(false);
      toast.success("用户密钥已创建");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "创建用户密钥失败");
    } finally {
      setIsCreating(false);
    }
  };

  const setItemPending = (id: string, isPending: boolean) => {
    setPendingIds((current) => {
      const next = new Set(current);
      if (isPending) {
        next.add(id);
      } else {
        next.delete(id);
      }
      return next;
    });
  };

  const handleToggle = async (item: UserKey) => {
    setItemPending(item.id, true);
    try {
      const data = await updateUserKey(item.id, { enabled: !item.enabled });
      setItems(data.items);
      toast.success(item.enabled ? "用户密钥已禁用" : "用户密钥已启用");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "更新用户密钥失败");
    } finally {
      setItemPending(item.id, false);
    }
  };

  const handleDelete = async (item: UserKey) => {
    setItemPending(item.id, true);
    try {
      const data = await deleteUserKey(item.id);
      setItems(data.items);
      toast.success("用户密钥已删除");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "删除用户密钥失败");
    } finally {
      setItemPending(item.id, false);
    }
  };

  const openEditDialog = (item: UserKey) => {
    setEditingItem(item);
    setEditName(item.name);
    setEditKey("");
    setEditLimits(normalizeLimits(item.limits));
  };

  const handleEdit = async () => {
    if (!editingItem) {
      return;
    }
    const item = editingItem;
    const trimmedName = editName.trim();
    const trimmedKey = editKey.trim();
    const nextLimits = normalizeLimits(editLimits);
    if (trimmedName === item.name && !trimmedKey && JSON.stringify(nextLimits) === JSON.stringify(normalizeLimits(item.limits))) {
      setEditingItem(null);
      return;
    }
    setItemPending(item.id, true);
    try {
      const data = await updateUserKey(item.id, {
        ...(trimmedName !== item.name ? { name: trimmedName } : {}),
        ...(trimmedKey ? { key: trimmedKey } : {}),
        limits: nextLimits,
      });
      setItems(data.items);
      setEditingItem(null);
      setEditKey("");
      toast.success("用户密钥已更新");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "更新用户密钥失败");
    } finally {
      setItemPending(item.id, false);
    }
  };

  const handleCopy = async (value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      toast.success("已复制到剪贴板");
    } catch {
      toast.error("复制失败，请手动复制");
    }
  };

  const confirmDelete = (item: UserKey) => {
    Modal.confirm({
      title: "删除用户密钥",
      content: `确认删除用户密钥「${item.name}」吗？删除后该密钥将无法继续调用接口。`,
      okText: "删除",
      cancelText: "取消",
      okButtonProps: { danger: true },
      onOk: () => handleDelete(item),
    });
  };

  return (
    <>
      <Card
        title={
          <Space>
            <KeyRound className="size-4 text-blue-500" />
            <span>用户密钥</span>
          </Space>
        }
        extra={
          <Button type="primary" icon={<Plus className="size-4" />} onClick={() => setIsDialogOpen(true)}>
            创建
          </Button>
        }
      >
        <Space direction="vertical" size={16} className="w-full">
          <Typography.Text type="secondary">
            为普通用户创建专用密钥；普通用户只能进入画图页，不能查看设置和号池。
          </Typography.Text>

          {revealedKey ? (
            <Alert
              type="success"
              showIcon
              title="新密钥仅展示一次，请立即保存"
              description={
                <Space direction="vertical" size={10} className="w-full">
                  <Typography.Text code copyable={{ text: revealedKey }} className="break-all">
                    {revealedKey}
                  </Typography.Text>
                  <Button size="small" icon={<Copy className="size-3.5" />} onClick={() => void handleCopy(revealedKey)}>
                    复制密钥
                  </Button>
                </Space>
              }
            />
          ) : null}

          <Spin spinning={isLoading}>
            {items.length === 0 && !isLoading ? (
              <Empty description="暂无普通用户密钥" image={Empty.PRESENTED_IMAGE_SIMPLE} />
            ) : (
              <List
                itemLayout="vertical"
                dataSource={items}
                split
                renderItem={(item) => {
                  const isPending = pendingIds.has(item.id);
                  return (
                    <List.Item
                      actions={[
                        <Button key="edit" size="small" icon={<Pencil className="size-3.5" />} disabled={isPending} onClick={() => openEditDialog(item)}>
                          编辑
                        </Button>,
                        <Button key="toggle" size="small" icon={item.enabled ? <Ban className="size-3.5" /> : <CheckCircle2 className="size-3.5" />} loading={isPending} onClick={() => void handleToggle(item)}>
                          {item.enabled ? "禁用" : "启用"}
                        </Button>,
                        <Button key="delete" size="small" danger icon={<Trash2 className="size-3.5" />} disabled={isPending} onClick={() => confirmDelete(item)}>
                          删除
                        </Button>,
                      ]}
                    >
                      <List.Item.Meta
                        title={
                          <Space size={8} wrap>
                            <Typography.Text strong>{item.name}</Typography.Text>
                            <Tag color={item.enabled ? "green" : "default"}>{item.enabled ? "已启用" : "已禁用"}</Tag>
                          </Space>
                        }
                        description={
                          <Space direction="vertical" size={2}>
                            <Typography.Text type="secondary">创建时间 {formatShanghaiDateTime(item.created_at)}</Typography.Text>
                            <Typography.Text type="secondary">最近使用 {formatShanghaiDateTime(item.last_used_at)}</Typography.Text>
                            <Typography.Text type="secondary">{quotaText(item)}</Typography.Text>
                            {normalizeLimits(item.limits).allowed_endpoints.length ? (
                              <Typography.Text type="secondary">
                                接口权限 {normalizeLimits(item.limits).allowed_endpoints.length} 项
                              </Typography.Text>
                            ) : null}
                          </Space>
                        }
                      />
                    </List.Item>
                  );
                }}
              />
            )}
          </Spin>
        </Space>
      </Card>

      <Modal
        title="创建用户密钥"
        open={isDialogOpen}
        onCancel={() => setIsDialogOpen(false)}
        onOk={() => void handleCreate()}
        okText="创建"
        cancelText="取消"
        confirmLoading={isCreating}
      >
        <Form layout="vertical" className="mt-4">
          <Form.Item label="名称（可选）" extra="方便区分不同使用者；创建后会生成一条只能查看一次的原始密钥。">
            <Input value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：设计同学 A、运营临时账号" />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title="编辑用户密钥"
        open={Boolean(editingItem)}
        onCancel={() => {
          setEditingItem(null);
          setEditKey("");
        }}
        onOk={() => void handleEdit()}
        okText="保存"
        cancelText="取消"
        confirmLoading={editingItem ? pendingIds.has(editingItem.id) : false}
      >
        <Form layout="vertical" className="mt-4">
          <Form.Item label="名称">
            <Input value={editName} onChange={(event) => setEditName(event.target.value)} placeholder="例如：设计同学 A、运营临时账号" />
          </Form.Item>
          <Form.Item label="新的专用密钥（可选）" extra="留空则保持当前密钥不变。保存后旧密钥会立即失效。">
            <Input value={editKey} onChange={(event) => setEditKey(event.target.value)} placeholder="例如：sk-your-custom-user-key" />
          </Form.Item>
          <Form.Item label="每日调用上限" extra="0 表示不限制。">
            <InputNumber
              min={0}
              className="w-full"
              value={editLimits.daily_requests}
              onChange={(value) => setEditLimits((current) => ({ ...current, daily_requests: Number(value || 0) }))}
            />
          </Form.Item>
          <Form.Item label="每日图片上限" extra="会统计同步和异步图片接口；0 表示不限制。">
            <InputNumber
              min={0}
              className="w-full"
              value={editLimits.daily_images}
              onChange={(value) => setEditLimits((current) => ({ ...current, daily_images: Number(value || 0) }))}
            />
          </Form.Item>
          <Form.Item label="允许接口" extra="留空表示允许所有接口。">
            <Select
              mode="multiple"
              allowClear
              options={endpointOptions}
              value={editLimits.allowed_endpoints}
              onChange={(value) => setEditLimits((current) => ({ ...current, allowed_endpoints: value }))}
              placeholder="选择可访问的接口"
            />
          </Form.Item>
          <Form.Item label="允许模型" extra="留空表示允许所有模型；可手动输入新模型名。">
            <Select
              mode="tags"
              allowClear
              options={modelSuggestions.map((value) => ({ label: value, value }))}
              value={editLimits.allowed_models}
              onChange={(value) => setEditLimits((current) => ({ ...current, allowed_models: value }))}
              placeholder="例如 gpt-image-2"
            />
          </Form.Item>
        </Form>
      </Modal>
    </>
  );
}
