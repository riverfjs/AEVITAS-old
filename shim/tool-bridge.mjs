export function createToolBridge() {
  const tools = [];
  const toolByName = new Map();
  const entryByName = new Map();

  function normalizeName(raw) {
    return String(raw || "").trim();
  }

  function toCatalogItem(entry) {
    const name = normalizeName(entry?.name || entry?.id);
    if (!name) {
      return null;
    }
    return {
      name,
      description: String(entry?.description || "").trim(),
      // OpenClaw tools commonly expose schema as `parameters`; keep backward
      // compatibility with input_schema/inputSchema as well.
      input_schema: entry?.input_schema ?? entry?.inputSchema ?? entry?.parameters ?? null,
    };
  }

  return {
    registerTool(entry) {
      const item = toCatalogItem(entry);
      if (!item) {
        return false;
      }
      entryByName.set(item.name, entry);
      if (toolByName.has(item.name)) {
        toolByName.set(item.name, item);
        const idx = tools.findIndex((t) => t.name === item.name);
        if (idx >= 0) {
          tools[idx] = item;
        }
        return true;
      }
      toolByName.set(item.name, item);
      tools.push(item);
      return true;
    },
    listTools() {
      return tools.map((t) => ({ ...t }));
    },
    async executeTool(name, params) {
      const key = normalizeName(name);
      const entry = entryByName.get(key);
      if (!entry || typeof entry.execute !== "function") {
        throw new Error(`tool not found or not executable: ${key}`);
      }
      return await entry.execute("", params || {});
    },
  };
}
