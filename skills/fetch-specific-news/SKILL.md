---
name: fetch-specific-news
description: Fetch and verify one specific news event with minimal tool calls. Use when the user asks about a single event/topic (e.g. "Apple just announced a new Mac. Please summarize the news."), wants the latest update for one claim, or asks for fact confirmation on one specific news item.
---

# Fetch Specific News

## Goal

Get the latest status of one specific news event with strict search/fetch limits.

## Hard Constraints

- For a single specific event/topic, use this skill instead of `news-summary`.
- Do not run broad multi-round searching by default.
- Default budget: `WebSearch` <= 1 call, `WebFetch` <= 2 calls.
- If evidence is already sufficient, stop and answer.
- Only exceed budget when sources are clearly conflicting and user asks for deeper verification.
- Absolute cap: `WebSearch` <= 2, `WebFetch` <= 3.

## Workflow

1. Extract a single target claim:
   - subject (who/what)
   - key event/action
   - time scope (if provided)
   - location/org (if provided)
2. Build one high-quality query first (not multiple variants).
3. Run one `WebSearch` with focused terms and relevant source hints.
4. Pick top high-confidence sources, then use `WebFetch` for details.
5. Decide result status:
   - `Confirmed`
   - `Likely true`
   - `Uncertain`
   - `Likely false`
   - `False`
6. If confidence is low and sources conflict, run one final narrow `WebSearch` to break tie.
7. Stop when either condition is met:
   - official source + 1 reputable media source agree, or
   - 2 reputable media sources agree and no strong contradiction found.

Source priority:

1. Official sources (government, company newsroom, regulator filings)
2. Tier-1 wires/outlets
3. Reputable domain-specific media
4. Social posts only as weak evidence (never sole basis)

## Output Template

Use this structure:

```markdown
结论: <Confirmed | Likely true | Uncertain | Likely false | False>

事件: <one-line normalized claim>
最新进展: <2-4 bullets>

证据:
- <source 1 + link + key fact>
- <source 2 + link + key fact>
- <source 3 + link + key fact, optional>

不确定点:
- <what is still unknown, if any>
```

## When NOT to use this skill

- User asks for broad daily/weekly news roundup -> use `news-summary`.
- User asks for many unrelated topics in one request.
- User asks for industry-wide digest (e.g. "today's tech roundup", "weekly business summary") -> use `news-summary`.
