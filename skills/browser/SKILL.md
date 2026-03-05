---
name: browser
description: Browser automation for dynamic web extraction with Playwright scripts. Use when static fetch is insufficient and page-rendered content must be read from the DOM.
---

# Browser Automation

## Goal

Extract structured data from dynamic websites with deterministic script-driven scraping.

## Hard Constraints

- **Always start from the homepage** — never guess deep URLs directly
- **Use `scrape.cjs` for all data extraction** — it runs nav + eval in one process, no context errors
- **Never use `screenshot.cjs` to read data** — only for visual debugging when JS returns nothing
- **Never chain `nav.cjs` + `eval.cjs` for simple scraping** — use `scrape.cjs` instead
- **If result is empty `[]`** — the selector is wrong, adjust JS; do NOT take a screenshot
- **Always show the source URL to the user** — every scrape.cjs result includes `url` and `title`; always include them in your reply so the user can verify the data source

## Workflow

1. Start from homepage and discover links.
2. Navigate to the target section with additional link extraction if needed.
3. Extract structured fields using targeted selectors.
4. If selectors fail repeatedly, fall back once to `document.body.innerText`.
5. Always report source `url` and `title` with extracted results.

Primary command:

```bash
node ~/.aevitas/workspace/.claude/skills/browser/scripts/scrape.cjs \
  <url> \
  '<javascript>'
```

Homepage discovery example:

```bash
node ~/.aevitas/workspace/.claude/skills/browser/scripts/scrape.cjs \
  'https://www.example.com' \
  'Array.from(document.querySelectorAll("a")).filter(a=>a.textContent.trim()).map(a=>({text:a.textContent.trim(),url:a.href})).slice(0,30)'
```

Data extraction example:

```bash
node ~/.aevitas/workspace/.claude/skills/browser/scripts/scrape.cjs \
  '<target-url>' \
  'Array.from(document.querySelectorAll(".item-class")).slice(0,10).map((el,i)=>({rank:i+1,title:el.querySelector(".title-class")?.textContent?.trim(),value:el.querySelector(".value-class")?.textContent?.trim()}))'
```

Fallback example:

```bash
node ~/.aevitas/workspace/.claude/skills/browser/scripts/scrape.cjs \
  '<target-url>' \
  'document.body.innerText.slice(0, 3000)'
```

Multi-step scripts (only when interaction is required):

```bash
node ~/.aevitas/workspace/.claude/skills/browser/scripts/start.cjs
node ~/.aevitas/workspace/.claude/skills/browser/scripts/nav.cjs <url>
node ~/.aevitas/workspace/.claude/skills/browser/scripts/eval.cjs '<js>'
node ~/.aevitas/workspace/.claude/skills/browser/scripts/stop.cjs
```

Visual debug only:

```bash
node ~/.aevitas/workspace/.claude/skills/browser/scripts/screenshot.cjs
```

## Output Template

```markdown
Data Source: [<title>](<url>)

Extracted Data:
- <item 1>
- <item 2>
- <item 3>

Notes:
- <selector confidence / fallback used>
```

## When NOT to use this skill

- Static content is already available via regular fetch tools.
- User asks for non-web local file operations.
- Task is unrelated to webpage content extraction.
