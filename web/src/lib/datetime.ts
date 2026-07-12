export const SHANGHAI_TIME_ZONE = "Asia/Shanghai";

type DateTimeValue = string | number | Date | null | undefined;

function normalizeDateString(value: string) {
  const text = value.trim();
  if (!text) {
    return "";
  }

  if (/^\d{4}-\d{2}-\d{2}(?:[T\s]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?)?$/.test(text)) {
    return `${text.replace(" ", "T")}+08:00`;
  }
  return text;
}

export function parseDateTime(value: DateTimeValue): Date | null {
  if (value instanceof Date) {
    return Number.isNaN(value.getTime()) ? null : value;
  }
  if (typeof value === "number") {
    const milliseconds = Math.abs(value) < 100_000_000_000 ? value * 1000 : value;
    const date = new Date(milliseconds);
    return Number.isNaN(date.getTime()) ? null : date;
  }
  if (typeof value !== "string") {
    return null;
  }

  const numeric = Number(value.trim());
  if (value.trim() && Number.isFinite(numeric)) {
    return parseDateTime(numeric);
  }
  const date = new Date(normalizeDateString(value));
  return Number.isNaN(date.getTime()) ? null : date;
}

function parts(value: DateTimeValue) {
  const date = parseDateTime(value);
  if (!date) {
    return null;
  }
  const values = new Intl.DateTimeFormat("zh-CN", {
    timeZone: SHANGHAI_TIME_ZONE,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).formatToParts(date);
  return Object.fromEntries(values.filter((part) => part.type !== "literal").map((part) => [part.type, part.value]));
}

export function formatShanghaiDateTime(value: DateTimeValue, fallback = "-") {
  const valueParts = parts(value);
  if (!valueParts) {
    return fallback;
  }
  return `${valueParts.year}-${valueParts.month}-${valueParts.day} ${valueParts.hour}:${valueParts.minute}:${valueParts.second}`;
}

export function formatShanghaiTime(value: DateTimeValue, fallback = "-") {
  const valueParts = parts(value);
  if (!valueParts) {
    return fallback;
  }
  return `${valueParts.hour}:${valueParts.minute}:${valueParts.second}`;
}

export function formatShanghaiDateTimeParts(value: DateTimeValue) {
  const valueParts = parts(value);
  if (!valueParts) {
    return { date: "-", time: "" };
  }
  return {
    date: `${valueParts.year}-${valueParts.month}-${valueParts.day}`,
    time: `${valueParts.hour}:${valueParts.minute}:${valueParts.second}`,
  };
}
