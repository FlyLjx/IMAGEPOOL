"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import {
  Bug,
  Images,
  LayoutDashboard,
  LogOut,
  Menu,
  Settings,
  TimerReset,
  UsersRound,
} from "lucide-react";
import { Button, Drawer, Layout, Menu as AntMenu } from "antd";
import type { MenuProps } from "antd";
import { usePathname, useRouter } from "next/navigation";

import { HeaderActions } from "@/components/header-actions";
import { getStoredAuthSessionFast, getValidatedAuthSession } from "@/lib/auth-session";
import { clearStoredAuthSession, type StoredAuthSession } from "@/store/auth";

const adminNavItems = [
  { href: "/dashboard", label: "系统总览", icon: LayoutDashboard },
  { href: "/accounts", label: "号池管理", icon: UsersRound },
  { href: "/image-manager", label: "图片管理", icon: Images },
  { href: "/tasks", label: "任务队列", icon: TimerReset },
  { href: "/debug", label: "调试", icon: Bug },
  { href: "/settings", label: "设置", icon: Settings },
];

const userNavItems = [{ href: "/image", label: "图片工作台", icon: Images }];

type NavItem = (typeof adminNavItems)[number];

function SidebarBrand() {
  return (
    <Link href="/dashboard" className="flex items-center gap-3 px-3 py-2">
      <span className="flex size-9 items-center justify-center rounded-xl bg-blue-600 text-white shadow-sm">
        <LayoutDashboard className="size-4" />
      </span>
      <span className="min-w-0">
        <span className="block truncate text-sm font-semibold text-slate-950 dark:text-white">IMAGE POOL</span>
        <span className="block truncate text-xs text-slate-400">Image Operations</span>
      </span>
    </Link>
  );
}

function buildMenuItems(navItems: NavItem[]): MenuProps["items"] {
  return navItems.map((item) => {
    const Icon = item.icon;
    return {
      key: item.href,
      icon: <Icon className="size-4" />,
      label: item.label,
    };
  });
}

function matchNavItem(pathname: string, navItems: NavItem[]) {
  const normalizedPathname = pathname.replace(/\/$/, "") || "/";
  return [...navItems]
    .sort((left, right) => right.href.length - left.href.length)
    .find((item) => normalizedPathname === item.href || normalizedPathname.startsWith(`${item.href}/`));
}

function pathTitle(pathname: string, navItems: NavItem[]) {
  return matchNavItem(pathname, navItems)?.label || "控制台";
}

export function TopNav({ children }: { children?: React.ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const [session, setSession] = useState<StoredAuthSession | null | undefined>(undefined);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [activeKey, setActiveKey] = useState("");
  const navItems = session?.role === "admin" ? adminNavItems : userNavItems;

  useEffect(() => {
    let active = true;

    const load = async () => {
      if (pathname === "/login") {
        if (active) {
          setSession(null);
        }
        return;
      }

      const cachedSession = await getStoredAuthSessionFast();
      if (active && cachedSession) {
        setSession(cachedSession);
      }

      const storedSession = await getValidatedAuthSession();
      if (active) {
        setSession(storedSession);
      }
    };

    void load();
    return () => {
      active = false;
    };
  }, [pathname]);

  useEffect(() => {
    if (pathname === "/login" || !session) {
      return;
    }
    setActiveKey(matchNavItem(pathname, navItems)?.href || navItems[0]?.href || "");
  }, [pathname, session, navItems]);

  const handleLogout = async () => {
    await clearStoredAuthSession();
    router.replace("/login");
  };

  if (pathname === "/login" || session === undefined || !session) {
    return children ?? null;
  }

  const menuItems = buildMenuItems(navItems);
  const selectedKey = activeKey || matchNavItem(pathname, navItems)?.href || navItems[0]?.href;
  const sidebar = (
    <div className="flex h-full flex-col border-r border-slate-200 bg-white">
      <div className="border-b border-slate-100 px-3 py-4">
        <SidebarBrand />
      </div>
      <AntMenu
        mode="inline"
        selectedKeys={selectedKey ? [selectedKey] : []}
        items={menuItems}
        className="flex-1 border-0"
        onClick={({ key }) => {
          setActiveKey(String(key));
          setDrawerOpen(false);
          router.push(String(key));
        }}
      />
      <div className="border-t border-slate-100 p-3">
        <Button block icon={<LogOut className="size-4" />} onClick={() => void handleLogout()}>
          退出登录
        </Button>
      </div>
    </div>
  );

  return (
    <Layout className="min-h-screen bg-[#f5f7fb]">
      <Layout.Sider
        width={248}
        className="!fixed !inset-y-0 !left-0 z-30 hidden overflow-hidden !bg-white shadow-sm lg:block"
      >
        {sidebar}
      </Layout.Sider>
      <Drawer
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        placement="left"
        size={280}
        styles={{ body: { padding: 0 } }}
      >
        {sidebar}
      </Drawer>
      <Layout className="min-h-screen !bg-transparent lg:pl-[248px]">
        <Layout.Header
          className="sticky top-0 z-20 h-14 border-b border-slate-200 !px-0"
          style={{ background: "#fff" }}
        >
          <div className="mx-auto flex h-full w-full max-w-[1600px] items-center justify-between px-3 sm:px-5">
            <div className="flex min-w-0 items-center gap-2 sm:gap-3">
              <Button className="lg:hidden" icon={<Menu className="size-4" />} onClick={() => setDrawerOpen(true)} />
              <div className="min-w-0">
                <div className="truncate text-base font-semibold text-slate-900">{pathTitle(pathname, navItems)}</div>
                <div className="hidden text-xs text-slate-400 sm:block">当前模块</div>
              </div>
            </div>
            <HeaderActions className="shrink-0" />
          </div>
        </Layout.Header>
        <Layout.Content className="min-h-[calc(100vh-3.5rem)] p-3 sm:p-5">
          <div className="mx-auto w-full max-w-[1600px]">{children}</div>
        </Layout.Content>
      </Layout>
    </Layout>
  );
}
