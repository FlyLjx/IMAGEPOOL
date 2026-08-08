"use client";

import { Card, Typography } from "antd";

export function SettingsHeader() {
  return (
    <Card>
      <div className="py-1">
        <Typography.Text type="secondary" className="text-xs font-semibold uppercase tracking-[0.18em]">
          Settings
        </Typography.Text>
        <Typography.Title level={3} className="!mb-0 !mt-1">
          设置
        </Typography.Title>
        <Typography.Text type="secondary" className="block !mt-2">
          常用配置保持简单；账号槽位模式、切号和补号由调度器统一处理。
        </Typography.Text>
      </div>
    </Card>
  );
}
