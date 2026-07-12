"use client";

import localforage from "localforage";

export type AuthRole = "admin" | "user";

export type StoredAuthSession = {
  key: string;
  role: AuthRole;
  subjectId: string;
  name: string;
};

export const AUTH_KEY_STORAGE_KEY = "image_pool_auth_key";
export const AUTH_SESSION_STORAGE_KEY = "image_pool_auth_session";

const authStorage = localforage.createInstance({
  name: "image-pool",
  storeName: "auth",
});

let cachedAuthKey: string | null = null;
let cachedSession: StoredAuthSession | null | undefined;

function normalizeSession(value: unknown, fallbackKey = ""): StoredAuthSession | null {
  if (!value || typeof value !== "object") {
    return null;
  }

  const candidate = value as Partial<StoredAuthSession>;
  const key = String(candidate.key || fallbackKey || "").trim();
  const role = candidate.role === "admin" || candidate.role === "user" ? candidate.role : null;
  if (!key || !role) {
    return null;
  }

  return {
    key,
    role,
    subjectId: String(candidate.subjectId || "").trim(),
    name: String(candidate.name || "").trim(),
  };
}

export function getDefaultRouteForRole(role: AuthRole) {
  return role === "user" ? "/image" : "/dashboard";
}

export async function getStoredAuthKey() {
  if (typeof window === "undefined") {
    return "";
  }
  if (cachedAuthKey !== null) {
    return cachedAuthKey;
  }
  const value = await authStorage.getItem<string>(AUTH_KEY_STORAGE_KEY);
  cachedAuthKey = String(value || "").trim();
  return cachedAuthKey;
}

export async function getStoredAuthSession() {
  if (typeof window === "undefined") {
    return null;
  }
  if (cachedSession !== undefined) {
    return cachedSession;
  }

  const [storedKey, storedSession] = await Promise.all([
    authStorage.getItem<string>(AUTH_KEY_STORAGE_KEY),
    authStorage.getItem<StoredAuthSession>(AUTH_SESSION_STORAGE_KEY),
  ]);

  cachedAuthKey = String(storedKey || "").trim();
  const normalizedSession = normalizeSession(storedSession, String(storedKey || ""));
  if (normalizedSession) {
    if (normalizedSession.key !== String(storedKey || "").trim()) {
      await authStorage.setItem(AUTH_KEY_STORAGE_KEY, normalizedSession.key);
      cachedAuthKey = normalizedSession.key;
    }
    cachedSession = normalizedSession;
    return normalizedSession;
  }

  if (String(storedKey || "").trim()) {
    await clearStoredAuthSession();
  }
  cachedSession = null;
  return null;
}

export async function setStoredAuthSession(session: StoredAuthSession) {
  const normalizedSession = normalizeSession(session);
  if (!normalizedSession) {
    await clearStoredAuthSession();
    return;
  }

  await Promise.all([
    authStorage.setItem(AUTH_KEY_STORAGE_KEY, normalizedSession.key),
    authStorage.setItem(AUTH_SESSION_STORAGE_KEY, normalizedSession),
  ]);
  cachedAuthKey = normalizedSession.key;
  cachedSession = normalizedSession;
}

export async function setStoredAuthKey(authKey: string) {
  const normalizedAuthKey = String(authKey || "").trim();
  if (!normalizedAuthKey) {
    await clearStoredAuthSession();
    return;
  }
  await authStorage.setItem(AUTH_KEY_STORAGE_KEY, normalizedAuthKey);
  cachedAuthKey = normalizedAuthKey;
  cachedSession = null;
}

export async function clearStoredAuthSession() {
  if (typeof window === "undefined") {
    return;
  }
  await Promise.all([
    authStorage.removeItem(AUTH_KEY_STORAGE_KEY),
    authStorage.removeItem(AUTH_SESSION_STORAGE_KEY),
  ]);
  cachedAuthKey = "";
  cachedSession = null;
}

export async function clearStoredAuthKey() {
  await clearStoredAuthSession();
}
