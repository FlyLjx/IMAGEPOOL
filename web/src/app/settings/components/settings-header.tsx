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
          管理运行配置和用户 Key。
        </Typography.Text>
      </div>
    </Card>
  );
}
