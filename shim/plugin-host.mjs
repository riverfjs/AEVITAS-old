#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import os from "node:os";
import { pathToFileURL } from "node:url";
import http from "node:http";
import { createToolBridge } from "./tool-bridge.mjs";

const HOME = process.env.HOME || os.homedir();
const AEVITAS_CONFIG = process.env.AEVITAS_CONFIG || path.join(HOME, ".aevitas", "config.json");

function readJSON(p) {
  return JSON.parse(fs.readFileSync(p, "utf8"));
}

function parseArgs(argv) {
  const out = {};
  for (let i = 2; i < argv.length; i += 1) {
    const token = argv[i];
    if (!token.startsWith("--")) continue;
    const key = token.slice(2);
    const next = argv[i + 1];
    if (!next || next.startsWith("--")) {
      out[key] = "true";
      continue;
    }
    out[key] = next;
    i += 1;
  }
  return out;
}

function pluginInstallPathFromRegistry(pluginID) {
  const regPath = path.join(HOME, ".aevitas", "plugins", "registry.json");
  const reg = readJSON(regPath);
  const entry = reg.plugins?.[pluginID];
  if (!entry?.installPath) {
    throw new Error(`plugin ${pluginID} not installed`);
  }
  return entry.installPath;
}

function ensureOpenclawCompatModule(pluginPackageDir) {
  const compatSrc = path.resolve(path.dirname(new URL(import.meta.url).pathname), "openclaw-compat", "plugin-sdk.js");
  const openclawDir = path.join(pluginPackageDir, "node_modules", "openclaw");
  fs.mkdirSync(openclawDir, { recursive: true });
  fs.writeFileSync(path.join(openclawDir, "plugin-sdk.js"), fs.readFileSync(compatSrc, "utf8"));
  fs.writeFileSync(
    path.join(openclawDir, "package.json"),
    JSON.stringify(
      {
        name: "openclaw",
        private: true,
        type: "module",
        exports: {
          "./plugin-sdk": "./plugin-sdk.js",
        },
      },
      null,
      2,
    ),
  );
}

function resolvePluginConfig(cfg, pluginID, platform = "") {
  const candidates = [
    { key: `channels.${platform}`, value: platform ? cfg?.channels?.[platform] : null, channelKey: platform || pluginID },
    { key: `channels.${pluginID}`, value: cfg?.channels?.[pluginID], channelKey: pluginID },
  ];
  const hit = candidates.find((c) => c.value && typeof c.value === "object");
  if (!hit) {
    throw new Error(`missing plugin config, checked: ${candidates.map((c) => c.key).join(", ")}`);
  }
  return {
    pluginCfg: hit.value,
    channelKey: hit.channelKey,
  };
}

function buildPluginConfig(cfg, pluginID, platform = "") {
  const { pluginCfg, channelKey } = resolvePluginConfig(cfg, pluginID, platform);
  return {
    channels: { [channelKey]: pluginCfg },
    plugins: { [pluginID]: pluginCfg },
    workspace: { root: cfg?.agent?.workspace || "" },
    messages: cfg?.messages || {},
  };
}

async function postJSON(url, payload, authToken) {
  const res = await fetch(url, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      ...(authToken ? { authorization: `Bearer ${authToken}` } : {}),
    },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`http ${res.status}: ${body}`);
  }
}

function stripPrefix(v) {
  const s = String(v || "");
  const i = s.indexOf(":");
  return i >= 0 ? s.slice(i + 1) : s;
}

async function loadAdapter(pluginID) {
  const shimDir = path.dirname(new URL(import.meta.url).pathname);
  const adapterPath = path.join(shimDir, "adapters", `${pluginID}.mjs`);
  if (!fs.existsSync(adapterPath)) {
    return null;
  }
  const mod = await import(pathToFileURL(adapterPath).href);
  if (typeof mod?.createAdapter !== "function") {
    throw new Error(`invalid adapter module: ${adapterPath}`);
  }
  return mod.createAdapter;
}

async function loadWithTrace(pluginPackageDir) {
  const tracePath = path.join(pluginPackageDir, "src", "core", "trace.js");
  if (!fs.existsSync(tracePath)) {
    return null;
  }
  try {
    const mod = await import(pathToFileURL(tracePath).href);
    return typeof mod?.withTrace === "function" ? mod.withTrace : null;
  } catch {
    return null;
  }
}

function buildCoreRuntime(outboundURL, emitInbound) {
  return {
    channel: {
      text: {
        chunkMarkdownText: (t) => [String(t ?? "")],
        resolveTextChunkLimit: (_c, _ch, _aid, opts) => opts?.fallbackLimit ?? 4000,
        resolveChunkMode: () => "length",
        resolveMarkdownTableMode: () => "compact",
        convertMarkdownTables: (t) => String(t ?? ""),
        chunkTextWithMode: (t) => [String(t ?? "")],
      },
      routing: {
        resolveAgentRoute: ({ peer, accountId }) => ({
          sessionKey: `plugin:${peer?.id || "unknown"}`,
          accountId: accountId || "default",
          agentId: "default",
        }),
      },
      commands: {
        shouldComputeCommandAuthorized: () => true,
        resolveCommandAuthorizedFromAuthorizers: () => true,
        isControlCommandMessage: (text) => /^\/\w+/.test(String(text || "").trim()),
      },
      reply: {
        resolveEnvelopeFormatOptions: () => ({}),
        formatAgentEnvelope: ({ body }) => String(body ?? ""),
        finalizeInboundContext: (ctx) => ({ ...ctx }),
        resolveHumanDelayConfig: () => ({}),
        createReplyDispatcherWithTyping: () => ({
          dispatcher: { waitForIdle: async () => {} },
          replyOptions: {},
          markDispatchIdle: () => {},
        }),
        dispatchReplyFromConfig: async ({ ctx }) => {
          await emitInbound({
            sender_id: stripPrefix(ctx.From),
            chat_id: stripPrefix(ctx.OriginatingTo || ctx.To),
            thread_id: ctx.MessageThreadId || "",
            message_id: ctx.MessageSid || "",
            content: ctx.RawBody || ctx.CommandBody || ctx.BodyForAgent || ctx.Body || "",
            metadata: { outbound_url: outboundURL },
          });
          return { queuedFinal: true, counts: { final: 1 } };
        },
        dispatchReplyWithBufferedBlockDispatcher: async ({ ctx }) => {
          await emitInbound({
            sender_id: stripPrefix(ctx.From),
            chat_id: stripPrefix(ctx.OriginatingTo || ctx.To),
            thread_id: ctx.MessageThreadId || "",
            message_id: ctx.MessageSid || "",
            content: ctx.RawBody || ctx.CommandBody || ctx.BodyForAgent || ctx.Body || "",
            metadata: { outbound_url: outboundURL },
          });
        },
      },
    },
    system: { enqueueSystemEvent: () => {} },
  };
}


async function main() {
  const args = parseArgs(process.argv);
  const pluginID = String(args["plugin-id"] || "").trim();
  if (!pluginID) {
    throw new Error("missing required --plugin-id");
  }
  const platform = args.platform || "";
  const compat = args.compat || "openclaw";
  const entryOverride = args.entry || "";
  const cfg = readJSON(AEVITAS_CONFIG);
  const interactionCfg = cfg?.channels?.interaction || {};
  const inboundURL = process.env.AEVITAS_INTERACTION_INBOUND || `http://${interactionCfg.listenAddr || "127.0.0.1:18901"}${interactionCfg.inboundPath || "/interaction/inbound"}`;
  const outboundListen = args.listen || process.env.AEVITAS_SHIM_LISTEN || "127.0.0.1:18902";
  const outboundPath = args["outbound-path"] || process.env.AEVITAS_SHIM_OUTBOUND_PATH || "/interaction/outbound";
  const outboundURL = `http://${outboundListen}${outboundPath}`;
  const authToken = interactionCfg.authToken || "";

  const installPath = pluginInstallPathFromRegistry(pluginID);
  const pluginPackageDir = path.join(installPath, "package");
  if (compat === "openclaw") {
    ensureOpenclawCompatModule(pluginPackageDir);
  } else {
    throw new Error(`unsupported compat runtime: ${compat}`);
  }

  const entryPath = entryOverride ? path.resolve(entryOverride) : path.join(pluginPackageDir, "index.js");
  const pluginURL = pathToFileURL(entryPath).href;
  const pluginMod = await import(pluginURL);
  const withTrace = await loadWithTrace(pluginPackageDir);
  const plugin = pluginMod.default;
  if (!plugin?.register) {
    throw new Error("invalid plugin export");
  }

  const channels = [];
  const toolBridge = createToolBridge();
  let runtimePlatform = platform || "generic";
  const createAdapter = await loadAdapter(pluginID);
  const adapter =
    typeof createAdapter === "function"
      ? createAdapter({
          pluginID,
          pluginMod,
          cfg,
          buildPluginConfig,
          getRuntimePlatform: () => runtimePlatform || platform,
        })
      : null;
  const emitInbound = async (data) => {
    if (adapter && typeof adapter.onInbound === "function") {
      await adapter.onInbound({ data });
    }
    await postJSON(
      inboundURL,
      {
        plugin_id: pluginID,
        platform: runtimePlatform,
        channel: "interaction",
        ...data,
        timestamp: new Date().toISOString(),
      },
      authToken,
    );
  };
  const emitOutboundResult = async ({ action, result }) => {
    const eventType = String(action?.metadata?.event_type || "").trim().toLowerCase();
    const requestId = String(action?.request_id || "").trim();
    const messageId = String(
      result?.messageId || result?.message_id || result?.id || result?.message?.id || ""
    ).trim();
    if (!messageId) {
      return;
    }
    if (!requestId) {
      console.warn("[plugin-host] outbound_result skipped: missing request_id", action);
      return;
    }
    await emitInbound({
      plugin_id: pluginID,
      platform: runtimePlatform,
      channel: runtimePlatform,
      sender_id: "",
      chat_id: action?.chat_id || action?.chatId || "",
      thread_id: action?.thread_id || action?.threadId || "",
      message_id: "",
      content: "",
      metadata: {
        event_type: "outbound_result",
        source_event_type: eventType,
        request_id: requestId,
        message_id: messageId,
        session_chat_id: action?.metadata?.session_chat_id || "",
      },
    });
  };
  const coreRuntime = buildCoreRuntime(outboundURL, emitInbound);
  const apiLogger = {
    info: (m) => console.log(`[plugin-host] ${m}`),
    warn: (m) => console.warn(`[plugin-host] ${m}`),
    error: (m) => console.error(`[plugin-host] ${m}`),
  };
  // Plugins expect api.config for tool registration (e.g. if (!api.config) return;). Without it, no tools are registered and tool_catalog is empty.
  const pluginCfgForRegister = buildPluginConfig(cfg, pluginID, platform);
  const api = {
    config: pluginCfgForRegister,
    runtime: coreRuntime,
    logger: apiLogger,
    registerChannel: ({ plugin: ch }) => channels.push(ch),
    registerCommand: () => {},
    registerTool: (toolEntry) => {
      const normalized = adapter && typeof adapter.normalizeToolEntry === "function"
        ? adapter.normalizeToolEntry(toolEntry)
        : toolEntry;
      toolBridge.registerTool(normalized);
    },
    registerCli: () => {},
    on: () => {},
  };
  plugin.register(api);

  const channel = channels.find((c) => c?.id === platform) || channels[0];
  if (!channel) throw new Error(`channel not registered for platform=${platform}`);
  if (channel?.id) {
    runtimePlatform = String(channel.id);
  }

  const server = http.createServer(async (req, res) => {
    if (req.method !== "POST" || req.url !== outboundPath) {
      res.statusCode = 404;
      res.end("not found");
      return;
    }
    let raw = "";
    req.on("data", (c) => (raw += c.toString("utf8")));
    req.on("end", async () => {
      try {
        const action = JSON.parse(raw || "{}");
        if (String(action?.action || "").trim() === "invoke_tool") {
          const toolName = String(action?.metadata?.tool_name || "").trim();
          const toolParams = action?.metadata?.tool_params;
          if (!toolName) {
            throw new Error("invoke_tool requires metadata.tool_name");
          }
          try {
            const execTool = () =>
              toolBridge.executeTool(toolName, toolParams && typeof toolParams === "object" ? toolParams : {});
            const traceCtx =
              adapter && typeof adapter.buildInvokeTraceContext === "function"
                ? adapter.buildInvokeTraceContext(action)
                : null;
            const result =
              typeof withTrace === "function" && traceCtx && typeof traceCtx === "object"
                ? await withTrace(traceCtx, execTool)
                : await execTool();
            let output = "";
            if (typeof result === "string") {
              output = result;
            } else if (result != null) {
              try {
                output = JSON.stringify(result);
              } catch {
                output = String(result);
              }
            }
            res.statusCode = 200;
            res.setHeader("content-type", "application/json");
            res.end(JSON.stringify({ accepted: true, success: true, output }));
          } catch (err) {
            res.statusCode = 200;
            res.setHeader("content-type", "application/json");
            res.end(JSON.stringify({ accepted: true, success: false, error: String(err?.stack || err) }));
          }
          return;
        }
        if (adapter && typeof adapter.handleOutbound === "function") {
          const handled = await adapter.handleOutbound({ action });
          if (handled) {
            if (typeof handled === "object") {
              await emitOutboundResult({ action, result: handled });
            }
            res.statusCode = 200;
            res.setHeader("content-type", "application/json");
            res.end(JSON.stringify({ accepted: true }));
            return;
          }
        }
        throw new Error(`no outbound adapter handled plugin=${pluginID}`);
      } catch (err) {
        res.statusCode = 500;
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify({ accepted: false, error: String(err) }));
      }
    });
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(Number(outboundListen.split(":").pop()), outboundListen.split(":")[0], resolve);
  });
  console.log(`[plugin-host] plugin=${pluginID} listening ${outboundURL}`);
  await emitInbound({
    sender_id: "",
    chat_id: "",
    thread_id: "",
    message_id: "",
    content: "",
    metadata: {
      event_type: "host_ready",
      plugin_id: pluginID,
      platform: runtimePlatform,
    },
  });
  const toolList = toolBridge.listTools();
  if (adapter && typeof adapter.onToolsCatalogReady === "function") {
    adapter.onToolsCatalogReady();
  }
  const toolNames = toolList.map((t) => t?.name).filter(Boolean);
  const bitableTool = toolList.find((t) => String(t?.name || "").trim() === "feishu_bitable_app");
  void bitableTool;
  await emitInbound({
    sender_id: "",
    chat_id: "",
    thread_id: "",
    message_id: "",
    content: "",
    metadata: {
      event_type: "tool_catalog",
      plugin_id: pluginID,
      platform: runtimePlatform,
      tools: toolList,
    },
  });

  const pluginCfg = buildPluginConfig(cfg, pluginID, runtimePlatform || platform);
  const abortController = new AbortController();
  const runtimeLog = (...args) => console.log(`[${pluginID}]`, ...args);
  runtimeLog.info = (...args) => console.log(`[${pluginID}]`, ...args);
  runtimeLog.warn = (...args) => console.warn(`[${pluginID}]`, ...args);
  runtimeLog.error = (...args) => console.error(`[${pluginID}]`, ...args);
  await channel.gateway.startAccount({
    cfg: pluginCfg,
    accountId: "default",
    runtime: {
      ...coreRuntime,
      log: runtimeLog,
      error: (m) => console.error(`[${pluginID}] ${m}`),
      exit: (code) => process.exit(code),
    },
    abortSignal: abortController.signal,
    setStatus: () => {},
    log: { info: (m) => console.log(`[plugin-host] ${m}`) },
  });
}

main().catch((err) => {
  console.error(`[plugin-host] fatal: ${err?.stack || String(err)}`);
  process.exit(1);
});
