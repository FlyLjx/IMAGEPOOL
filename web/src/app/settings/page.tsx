"use client";

import { useEffect, useRef } from "react";
import { LoaderCircle } from "lucide-react";

import { useAuthGuard } from "@/lib/use-auth-guard";

import { AdminKeyCard } from "./components/admin-key-card";
import { ConfigCard } from "./components/config-card";
import { SettingsHeader } from "./components/settings-header";
import { UserKeysCard } from "./components/user-keys-card";
import { useSettingsStore } from "./store";

function SettingsDataController() {
  const didLoadRef = useRef(false);
  const initialize = useSettingsStore((state) => state.initialize);

  useEffect(() => {
    if (didLoadRef.current) {
      return;
    }
    didLoadRef.current = true;
    void initialize();
  }, [initialize]);

  return null;
}

function SettingsPageContent() {
  return (
    <section className="settings-console">
      <SettingsDataController />
      <SettingsHeader />
      <section className="settings-layout">
        <div className="settings-main-column">
          <ConfigCard />
        </div>
        <aside className="settings-side-column">
          <AdminKeyCard />
          <UserKeysCard />
        </aside>
      </section>
    </section>
  );
}

export default function SettingsPage() {
  const { isCheckingAuth, session } = useAuthGuard(["admin"]);

  if (isCheckingAuth || !session || session.role !== "admin") {
    return (
      <div className="flex min-h-[40vh] items-center justify-center">
        <LoaderCircle className="size-5 animate-spin text-stone-400" />
      </div>
    );
  }

  return <SettingsPageContent />;
}
