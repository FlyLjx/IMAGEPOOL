"use client";

import { login } from "@/lib/api";
import { clearStoredAuthSession, getStoredAuthSession, setStoredAuthSession, type StoredAuthSession } from "@/store/auth";

const AUTH_VALIDATION_TTL_MS = 30_000;

let lastValidatedSession: StoredAuthSession | null = null;
let lastValidatedAt = 0;
let pendingValidation: Promise<StoredAuthSession | null> | null = null;

function sameSession(left: StoredAuthSession | null, right: StoredAuthSession | null) {
  return Boolean(left && right && left.key === right.key && left.role === right.role && left.subjectId === right.subjectId);
}

export async function getValidatedAuthSession(options: { force?: boolean } = {}): Promise<StoredAuthSession | null> {
  const storedSession = await getStoredAuthSession();
  if (!storedSession) {
    lastValidatedSession = null;
    lastValidatedAt = 0;
    return null;
  }

  const now = Date.now();
  if (
    !options.force &&
    sameSession(lastValidatedSession, storedSession) &&
    now - lastValidatedAt < AUTH_VALIDATION_TTL_MS
  ) {
    return storedSession;
  }

  if (pendingValidation) {
    return pendingValidation;
  }

  pendingValidation = validateStoredSession(storedSession);
  try {
    return await pendingValidation;
  } finally {
    pendingValidation = null;
  }
}

export async function getStoredAuthSessionFast(): Promise<StoredAuthSession | null> {
  return getStoredAuthSession();
}

async function validateStoredSession(storedSession: StoredAuthSession): Promise<StoredAuthSession | null> {
  try {
    const data = await login(storedSession.key);
    const nextSession: StoredAuthSession = {
      key: storedSession.key,
      role: data.role,
      subjectId: data.subject_id,
      name: data.name,
    };
    await setStoredAuthSession(nextSession);
    lastValidatedSession = nextSession;
    lastValidatedAt = Date.now();
    return nextSession;
  } catch {
    await clearStoredAuthSession();
    lastValidatedSession = null;
    lastValidatedAt = 0;
    return null;
  }
}
