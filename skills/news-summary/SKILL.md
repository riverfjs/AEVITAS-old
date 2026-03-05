---
name: news-summary
description: Summarize broad news updates or one industry/category digest from RSS feeds. Use for daily/weekly roundup or category requests (world, top, business, tech, reuters, npr, aljazeera), not for one specific event claim.
---

# News Summary

## Goal

Provide concise broad news summaries (daily/weekly or category digest) using the bundled RSS script.

## Hard Constraints

- Always use the bundled script; do not use ad-hoc `curl | python -c` pipelines.
- For one specific event/topic claim, switch to `fetch-specific-news`.
- Keep default output concise (5-8 key stories unless user asks for more).
- Include source links when presenting controversial or sensitive claims.

## Workflow

1. Choose mode by user intent:
   - Broad roundup -> use `--group brief` or `--group all`.
   - One category digest -> use `--feeds <category>` or a focused feed list.
2. Run script:
   - `python3 ~/.aevitas/workspace/.claude/skills/news-summary/scripts/fetch_news.py --group brief --limit 5`
3. If structured post-processing is needed, run JSON mode:
   - `python3 ~/.aevitas/workspace/.claude/skills/news-summary/scripts/fetch_news.py --group all --limit 4 --json`
4. Summarize by topic and impact; keep each item to one key point plus source.
5. If user shifts to a single event claim, stop and hand over to `fetch-specific-news`.

Supported groups:
- `brief`: `world`, `business`, `tech`
- `all`: `world`, `top`, `business`, `tech`, `reuters`, `npr`, `aljazeera`

Supported categories/feeds:
- `world`: global affairs
- `top`: broad top headlines
- `business`: business and markets
- `tech`: technology industry
- `reuters`: Reuters world wire
- `npr`: NPR top stories
- `aljazeera`: Al Jazeera general feed

Feed inspection command:
- `python3 ~/.aevitas/workspace/.claude/skills/news-summary/scripts/fetch_news.py --list-feeds`

## Output Template

Use this structure:

```markdown
Scope: <Daily roundup | Weekly roundup | Category digest>

Top Updates:
- <headline 1>: <one-line takeaway> (<source link>)
- <headline 2>: <one-line takeaway> (<source link>)
- <headline 3>: <one-line takeaway> (<source link>)

Trends:
- <1-2 cross-story observations>

Next Watch:
- <what to watch next>
```

## When NOT to use this skill

- User asks about one specific event or one claim verification.
- User asks for "latest update" on a single named incident/company announcement.
- User request is clearly fact-check oriented rather than broad summarization.