"use client";

import { useEffect, useMemo, useState } from "react";
import { Alert, Button, Card, Form, Input, Space, Typography } from "antd";
import { KeyRound, LoaderCircle, Save } from "lucide-react";
import { toast } from "sonner";

import { updateSettingsConfig } from "@/lib/api";
import { getStoredAuthSession, setStoredAuthSession } from "@/store/auth";

import { useSettingsStore } from "../store";

function normalizeKeys(value: unknown): string[] {
  if (!Array.isArray(value)) {
    return [];
  }
  return value.map((item) => String(item || "").trim()).filter(Boolean);
}

export function AdminKeyCard() {
  const config = useSettingsStore((state) => state.config);
  const isLoadingConfig = useSettingsStore((state) => state.isLoadingConfig);
  const setConfig = useSettingsStore((state) => state.setConfig);
  const [key, setKey] = useState("");
  const [isSaving, setIsSaving] = useState(false);
  const configuredKeys = useMemo(() => normalizeKeys(config?.api_keys), [config?.api_keys]);
  const primaryKey = configuredKeys[0] || "";

  useEffect(() => {
    setKey(primaryKey);
  }, [primaryKey]);

  const handleSave = async () => {
    const nextPrimaryKey = key.trim();
    if (!nextPrimaryKey) {
      toast.error("管理员 API Key 不能为空");
      return;
    }
    if (nextPrimaryKey === primaryKey) {
      toast.message("管理员 API Key 未修改");
      return;
    }

    const nextKeys = [nextPrimaryKey, ...configuredKeys.slice(1)];
    setIsSaving(true);
    try {
      const data = await updateSettingsConfig({ api_keys: nextKeys });
      const session = await getStoredAuthSession();
      if (session && !nextKeys.includes(session.key)) {
        await setStoredAuthSession({ ...session, key: nextPrimaryKey });
      }
      setConfig(data.config);
      toast.success("管理员 API Key 已更新");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "更新管理员 API Key 失败");
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <Card
      title={
        <Space>
          <KeyRound className="size-4 text-amber-500" />
          <span>管理员 API Key</span>
        </Space>
      }
      extra={
        <Button
          type="primary"
          size="small"
          icon={isSaving ? <LoaderCircle className="size-3.5 animate-spin" /> : <Save className="size-3.5" />}
          onClick={() => void handleSave()}
          disabled={isLoadingConfig || isSaving}
        >
          保存
        </Button>
      }
    >
      <Space direction="vertical" size={16} className="w-full">
        <Alert
          type="warning"
          showIcon
          message="保存后旧 Key 立即失效"
          description="当前后台会自动切换到新 Key，其他使用旧 Key 的客户端需要同步更新。"
        />
        <Form layout="vertical">
          <Form.Item label="主管理员 Key" extra="默认值为 dev-key；如配置了多个管理员 Key，其余 Key 会保留不变。">
            <Input.Password
              value={key}
              onChange={(event) => setKey(event.target.value)}
              placeholder="例如：admin-api-key-2026"
              autoComplete="new-password"
              disabled={isLoadingConfig || isSaving}
            />
          </Form.Item>
        </Form>
        <Typography.Text type="secondary" className="text-xs">
          当前共 {configuredKeys.length || 1} 个管理员 Key。
        </Typography.Text>
      </Space>
    </Card>
  );
}
