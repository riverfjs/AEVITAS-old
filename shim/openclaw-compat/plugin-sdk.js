export const DEFAULT_ACCOUNT_ID = "default";
export const DEFAULT_GROUP_HISTORY_LIMIT = 20;
export const PAIRING_APPROVED_MESSAGE = "Pairing approved.";
export const SILENT_REPLY_TOKEN = "__SILENT_REPLY__";

export function emptyPluginConfigSchema() {
  return { schema: { type: "object", additionalProperties: true } };
}

export function normalizeAccountId(v) {
  const s = String(v ?? "").trim();
  return s === "" ? DEFAULT_ACCOUNT_ID : s;
}

export function addWildcardAllowFrom(entries) {
  const list = Array.isArray(entries) ? [...entries] : [];
  if (!list.includes("*")) list.push("*");
  return list;
}

export function formatDocsLink(path) {
  const p = String(path ?? "").trim();
  if (p === "") return "";
  if (p.startsWith("http://") || p.startsWith("https://")) return p;
  return `https://openclaw.example${p.startsWith("/") ? "" : "/"}${p}`;
}

export function buildRandomTempFilePath(ext = "") {
  const tail = Math.random().toString(36).slice(2, 10);
  const suffix = String(ext).trim();
  return `/tmp/openclaw/${Date.now()}-${tail}${suffix}`;
}

export function readStringParam(params, key, fallback = "") {
  if (!params || typeof params !== "object") return fallback;
  const v = params[key];
  if (v == null) return fallback;
  return String(v);
}

export function readReactionParams(params) {
  const messageId = readStringParam(params, "message_id");
  const emoji = readStringParam(params, "emoji");
  return { messageId, emoji };
}

export function jsonResult(data) {
  return { ok: true, data };
}

export function extractToolSend(result) {
  if (!result || typeof result !== "object") return null;
  const text = typeof result.text === "string" ? result.text : "";
  const mediaUrl = typeof result.mediaUrl === "string" ? result.mediaUrl : "";
  if (!text && !mediaUrl) return null;
  return { text, mediaUrl };
}

export function recordPendingHistoryEntryIfEnabled({ historyMap, historyKey, limit, entry }) {
  if (!historyMap || !historyKey || !entry) return;
  const list = historyMap.get(historyKey) ?? [];
  list.push(entry);
  const max = Math.max(0, Number(limit ?? DEFAULT_GROUP_HISTORY_LIMIT));
  if (max > 0 && list.length > max) {
    list.splice(0, list.length - max);
  }
  historyMap.set(historyKey, list);
}

export function clearHistoryEntriesIfEnabled({ historyMap, historyKey, limit }) {
  if (!historyMap || !historyKey) return;
  if (Number(limit ?? 0) <= 0) return;
  historyMap.delete(historyKey);
}

export function buildPendingHistoryContextFromMap({ historyMap, historyKey, currentMessage, formatEntry }) {
  if (!historyMap || !historyKey) return currentMessage;
  const list = historyMap.get(historyKey) ?? [];
  if (!Array.isArray(list) || list.length === 0) return currentMessage;
  const rendered = list.map((entry) => {
    if (typeof formatEntry === "function") return formatEntry(entry);
    return `${entry.sender ?? "user"}: ${entry.body ?? ""}`;
  });
  return `${rendered.join("\n")}\n${currentMessage}`;
}

export function resolveThreadSessionKeys({ baseSessionKey, threadId, parentSessionKey, normalizeThreadId }) {
  const t = typeof normalizeThreadId === "function" ? normalizeThreadId(String(threadId ?? "")) : String(threadId ?? "");
  const parent = String(parentSessionKey ?? baseSessionKey ?? "");
  const sessionKey = t ? `${parent}#thread:${t}` : parent;
  return { sessionKey, parentSessionKey: parent };
}

export function createReplyPrefixContext({ cfg, agentId }) {
  return { cfg, agentId };
}

export function createTypingCallbacks({ start, stop, onStartError, onStopError }) {
  return {
    async start() {
      try { await start?.(); } catch (e) { onStartError?.(e); }
    },
    async stop() {
      try { await stop?.(); } catch (e) { onStopError?.(e); }
    },
  };
}

export function logTypingFailure({ log, channel, action, error }) {
  if (typeof log === "function") {
    log(`[${channel}] typing ${action} failed: ${String(error)}`);
  }
}

export function isNormalizedSenderAllowed({ senderId, allowFrom }) {
  const sid = String(senderId ?? "").trim().toLowerCase();
  const list = Array.isArray(allowFrom) ? allowFrom.map((v) => String(v).trim().toLowerCase()) : [];
  return sid !== "" && (list.includes("*") || list.includes(sid));
}

export async function resolveSenderCommandAuthorization() {
  return { commandAuthorized: true };
}
