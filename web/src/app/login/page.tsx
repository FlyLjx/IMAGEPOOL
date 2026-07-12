"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { Button, Card, Input, Spin, Typography } from "antd";
import { LoaderCircle, LockKeyhole } from "lucide-react";
import { toast } from "sonner";

import { HeaderActions } from "@/components/header-actions";
import { login } from "@/lib/api";
import { useRedirectIfAuthenticated } from "@/lib/use-auth-guard";
import { getDefaultRouteForRole, setStoredAuthSession } from "@/store/auth";

export default function LoginPage() {
  const router = useRouter();
  const [authKey, setAuthKey] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const { isCheckingAuth } = useRedirectIfAuthenticated();

  const handleLogin = async () => {
    const normalizedAuthKey = authKey.trim();
    if (!normalizedAuthKey) {
      toast.error("请输入 密钥");
      return;
    }

    setIsSubmitting(true);
    try {
      const data = await login(normalizedAuthKey);
      await setStoredAuthSession({
        key: normalizedAuthKey,
        role: data.role,
        subjectId: data.subject_id,
        name: data.name,
      });
      router.replace(getDefaultRouteForRole(data.role));
    } catch (error) {
      const message = error instanceof Error ? error.message : "登录失败";
      toast.error(message);
    } finally {
      setIsSubmitting(false);
    }
  };

  if (isCheckingAuth) {
    return (
      <div className="grid min-h-[calc(100vh-1rem)] w-full place-items-center px-4 py-6">
        <Spin />
      </div>
    );
  }

  return (
    <div className="grid min-h-[calc(100vh-1rem)] w-full place-items-center px-4 py-6">
      <HeaderActions className="fixed top-4 right-4 z-10" />
      <Card className="w-full max-w-[505px] shadow-sm">
        <div className="space-y-7 p-2 sm:p-4">
          <div className="space-y-4 text-center">
            <div className="mx-auto inline-flex size-14 items-center justify-center rounded-[18px] bg-blue-600 text-white shadow-sm">
              <LockKeyhole className="size-5" />
            </div>
            <div className="space-y-2">
              <Typography.Title level={2} className="!mb-0">欢迎回来</Typography.Title>
              <Typography.Text type="secondary">输入密钥后继续使用账号管理和系统配置功能。</Typography.Text>
            </div>
          </div>

          <div className="space-y-3">
            <label htmlFor="auth-key" className="block text-sm font-medium text-stone-700">
              密钥
            </label>
            <Input
              id="auth-key"
              type="password"
              value={authKey}
              onChange={(event) => setAuthKey(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  void handleLogin();
                }
              }}
              placeholder="请输入密钥"
              size="large"
            />
          </div>

          <Button
            type="primary"
            size="large"
            block
            onClick={() => void handleLogin()}
            disabled={isSubmitting}
          >
            {isSubmitting ? <LoaderCircle className="size-4 animate-spin" /> : null}
            登录
          </Button>
        </div>
      </Card>
    </div>
  );
}
