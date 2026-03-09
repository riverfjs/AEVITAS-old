function normalizeAttachmentSource(att) {
  const filePath = String(att?.file_path || att?.filePath || "").trim();
  if (filePath) return filePath;
  const fileUrl = String(att?.file_url || att?.fileUrl || att?.url || "").trim();
  if (fileUrl) return fileUrl;
  return "";
}

const ACTION_SEND_MESSAGE = "send_message";
const ACTION_EDIT_MESSAGE = "edit_message";
const ACTION_ADD_REACTION = "add_reaction";
const ACTION_SEND_AUDIO = "send_audio";
const ACTION_SEND_MEDIA = "send_media";

function attachmentFileName(att, fallback = "file") {
  const name = String(att?.file_name || att?.fileName || "").trim();
  if (name) return name;
  const src = normalizeAttachmentSource(att);
  if (!src) return fallback;
  const p = src.split("?")[0].split("#")[0];
  const base = p.split("/").pop();
  return base && base.trim() ? base.trim() : fallback;
}

function buildReplyCard(markdownText) {
  return {
    schema: "2.0",
    config: { wide_screen_mode: true },
    body: {
      elements: [
        {
          tag: "markdown",
          content: String(markdownText || ""),
        },
      ],
    },
  };
}

function parseSessionChatID(sessionChatID) {
  const raw = String(sessionChatID || "").trim();
  const parts = raw.split(":", 3);
  if (parts.length < 3) return { pluginID: "", platform: "", chatID: "" };
  return {
    pluginID: String(parts[0] || "").trim(),
    platform: String(parts[1] || "").trim(),
    chatID: String(parts[2] || "").trim(),
  };
}

function buildInvokeTraceContext(action) {
  const meta = action?.metadata && typeof action.metadata === "object" ? action.metadata : {};
  const parsed = parseSessionChatID(meta.session_chat_id);
  const chatId = String(action?.chat_id || action?.chatId || parsed.chatID || "").trim();
  const threadId = String(action?.thread_id || action?.threadId || "").trim();
  const messageId = String(meta.message_id || action?.reply_to || action?.replyTo || "").trim() || `invoke-${Date.now()}`;
  const accountId = String(meta.account_id || "default").trim() || "default";
  const senderOpenId = String(meta.sender_open_id || chatId || "").trim();
  const httpHeaders = meta.http_headers && typeof meta.http_headers === "object" ? meta.http_headers : undefined;
  return {
    messageId,
    chatId,
    accountId,
    senderOpenId,
    chatType: threadId ? "group" : "p2p",
    threadId: threadId || undefined,
    startTime: Date.now(),
    httpHeaders,
  };
}

function normalizeToolSchemaForAgent(rawSchema) {
  if (!rawSchema || typeof rawSchema !== "object") return rawSchema;
  const anyOf = Array.isArray(rawSchema.anyOf) ? rawSchema.anyOf : null;
  if (!anyOf || anyOf.length === 0) return rawSchema;

  const mergedProps = {};
  let requiredIntersection = null;
  const actionEnums = [];

  for (const variant of anyOf) {
    if (!variant || typeof variant !== "object") continue;
    const props = variant.properties && typeof variant.properties === "object" ? variant.properties : {};
    for (const [k, v] of Object.entries(props)) {
      if (!(k in mergedProps)) mergedProps[k] = v;
    }

    const reqArr = Array.isArray(variant.required)
      ? variant.required.map((x) => String(x || "").trim()).filter(Boolean)
      : [];
    const reqSet = new Set(reqArr);
    if (requiredIntersection == null) {
      requiredIntersection = reqSet;
    } else {
      for (const key of Array.from(requiredIntersection)) {
        if (!reqSet.has(key)) requiredIntersection.delete(key);
      }
    }

    const actionConst = props?.action?.const;
    if (typeof actionConst === "string" && actionConst.trim()) {
      actionEnums.push(actionConst.trim());
    }
  }

  if (mergedProps.action && actionEnums.length > 0) {
    mergedProps.action = {
      ...mergedProps.action,
      type: "string",
      enum: Array.from(new Set(actionEnums)),
    };
    delete mergedProps.action.const;
  }

  return {
    type: "object",
    properties: mergedProps,
    required: requiredIntersection ? Array.from(requiredIntersection) : [],
  };
}

export function createAdapter(ctx) {
  const normalizedToolNames = [];
  const pluginConfig = () =>
    ctx.buildPluginConfig(ctx.cfg, ctx.pluginID, ctx.getRuntimePlatform());
  const sendReply = async ({ cfg, to, text, replyToMessageId, accountId, replyInThread }) => {
    if (typeof ctx.pluginMod.sendCardFeishu !== "function") {
      throw new Error("feishu adapter missing sendCardFeishu");
    }
    return ctx.pluginMod.sendCardFeishu({
      cfg,
      to,
      card: buildReplyCard(text),
      replyToMessageId,
      accountId,
      replyInThread,
    });
  };

  const editReply = async ({ cfg, messageId, text, accountId }) => {
    if (typeof ctx.pluginMod.updateCardFeishu !== "function") {
      throw new Error("feishu adapter missing updateCardFeishu");
    }
    return ctx.pluginMod.updateCardFeishu({
      cfg,
      messageId,
      card: buildReplyCard(text),
      accountId,
    });
  };

  const addOnItReaction = async (messageId, accountId) => {
    if (typeof ctx.pluginMod.addReactionFeishu !== "function") return;
    const mid = String(messageId || "").trim();
    if (!mid) return;
    await ctx.pluginMod.addReactionFeishu({
      cfg: pluginConfig(),
      messageId: mid,
      emojiType: "OnIt",
      accountId: String(accountId || "default"),
    });
  };

  return {
    async onInbound() {},

    buildInvokeTraceContext(action) {
      return buildInvokeTraceContext(action);
    },

    normalizeToolEntry(entry) {
      if (!entry || typeof entry !== "object") return entry;
      const rawSchema = entry.input_schema ?? entry.inputSchema ?? entry.parameters ?? null;
      const normalizedSchema = normalizeToolSchemaForAgent(rawSchema);
      const out = { ...entry, parameters: normalizedSchema };
      const toolName = String(out.name || "").trim();
      if (toolName.startsWith("feishu_")) {
        normalizedToolNames.push(toolName);
      }
      return out;
    },

    onToolsCatalogReady() {
      if (normalizedToolNames.length === 0) return;
      // no-op: keep hook for compatibility; detailed schema debug logs removed.
    },

    async handleOutbound({ action }) {
      const actionType = String(action.action || ACTION_SEND_MESSAGE).trim().toLowerCase();
      const to = action.chat_id || action.chatId;
      const text = action.content || "";
      const cfg = pluginConfig();
      const accountId = action.metadata?.account_id || "default";
      const replyToMessageId = action.reply_to || "";
      const targetMessageId = String(action?.metadata?.message_id || "").trim();
      const replyInThread = !!action.thread_id;
      const voiceDurationMsRaw = Number(action?.metadata?.voice_duration_ms || 0);
      const voiceDurationMs = Number.isFinite(voiceDurationMsRaw) && voiceDurationMsRaw > 0
        ? Math.max(1000, Math.round(voiceDurationMsRaw))
        : 1000;

      if (actionType === ACTION_ADD_REACTION) {
        if (replyToMessageId) {
          await addOnItReaction(replyToMessageId, accountId);
        }
        return { handled: true };
      }
      if (actionType === ACTION_EDIT_MESSAGE) {
        if (!targetMessageId) {
          throw new Error("edit_message requires metadata.message_id");
        }
        const updated = await editReply({
          cfg,
          messageId: targetMessageId,
          text,
          accountId,
        });
        return { handled: true, messageId: updated?.messageId || targetMessageId };
      }
      const outboundAttachments = Array.isArray(action.attachments) ? action.attachments : [];
      const outboundMedia = Array.isArray(action.media) ? action.media : [];
      const mediaCandidates = [];
      for (const att of outboundAttachments) {
        const src = normalizeAttachmentSource(att);
        if (!src) continue;
        mediaCandidates.push({
          source: src,
          name: attachmentFileName(att, "file"),
          type: String(att?.type || "").trim().toLowerCase(),
        });
      }
      for (const m of outboundMedia) {
        const src = String(m || "").trim();
        if (!src) continue;
        mediaCandidates.push({
          source: src,
          name: attachmentFileName({ filePath: src }, "file"),
          type: "",
        });
      }

      if (actionType === ACTION_SEND_AUDIO) {
        const voiceCapable =
          typeof ctx.pluginMod.uploadFileLark === "function" &&
          typeof ctx.pluginMod.sendAudioLark === "function";
        if (!voiceCapable) {
          throw new Error("feishu adapter missing uploadFileLark/sendAudioLark");
        }
        let lastMessageId = "";
        for (const item of mediaCandidates) {
          const uploaded = await ctx.pluginMod.uploadFileLark({
            cfg,
            file: item.source,
            fileName: item.name,
            fileType: "opus",
            duration: voiceDurationMs,
            accountId,
          });
          const sent = await ctx.pluginMod.sendAudioLark({
            cfg,
            to,
            fileKey: uploaded?.fileKey || "",
            replyToMessageId,
            accountId,
            replyInThread,
          });
          lastMessageId = String(sent?.messageId || sent?.message_id || "").trim() || lastMessageId;
        }
        return { handled: true, messageId: lastMessageId };
      }

      if (actionType === ACTION_SEND_MEDIA) {
        if (typeof ctx.pluginMod.uploadAndSendMediaLark !== "function") {
          throw new Error("feishu adapter missing uploadAndSendMediaLark");
        }
        let lastMessageId = "";
        for (const item of mediaCandidates) {
          const sent = await ctx.pluginMod.uploadAndSendMediaLark({
            cfg,
            to,
            mediaUrl: item.source,
            fileName: item.name,
            replyToMessageId,
            accountId,
            replyInThread,
          });
          lastMessageId = String(sent?.messageId || sent?.message_id || "").trim() || lastMessageId;
        }
        return { handled: true, messageId: lastMessageId };
      }

      // send_message: text-only path; media should use explicit actions.
      if (!text) {
        return { handled: true, messageId: targetMessageId };
      }
      const sent = await sendReply({
        cfg,
        to,
        text,
        replyToMessageId,
        accountId,
        replyInThread,
      });
      return { handled: true, messageId: sent?.messageId || "" };
    },
  };
}
