---
name: stock-fundamental
description: Fetch and analyze one stock's fundamental data from Yahoo Finance. Use when the user asks for valuation, earnings, revenue, analyst expectations, or a full fundamental report for a specific symbol.
---

# Stock Fundamental Analysis

## Goal

Return a structured fundamental snapshot for one stock symbol based on Yahoo Finance data.

## Hard Constraints

- Use only this command:
  - `node ~/.aevitas/workspace/.claude/skills/stock-fundamental/scripts/fetch.cjs <SYMBOL> [quote|financials|balance|cashflow|keystats|analysis|all]`
- Default to one-shot `all`.
- Do not use `WebSearch` or `WebFetch` for stock fundamentals.
- Do not call browser helper scripts (`start.cjs`, `nav.cjs`, `eval.cjs`, `screenshot.cjs`) directly.
- If fetch fails, retry the same `all` command once; if still failing, return the error directly.

## Workflow

1. Normalize symbol format:
   - US: `AAPL`, `TSLA`
   - HK: `0700.HK`
   - CN A-share: `600519.SS`, `000858.SZ`
2. Run:
   - `node ~/.aevitas/workspace/.claude/skills/stock-fundamental/scripts/fetch.cjs <SYMBOL> all`
3. Parse returned JSON and build a concise analysis:
   - price and valuation
   - earnings/revenue trend
   - balance and cashflow health
   - analyst expectations and risk points
4. If user asks only one section, run specific mode (`quote`, `analysis`, etc.) instead of `all`.

## Output Template

```markdown
## <SYMBOL> (<Company>) Fundamental Snapshot

Source: Yahoo Finance

### Valuation & Price
- Price / 52W range / market cap
- Key multiples: PB, PS, EV/EBITDA
- Analyst target and implied upside/downside

### Financial Trend
- Revenue and net income direction (recent years)
- EPS trend and estimate revisions

### Balance Sheet & Cashflow
- Debt, cash, net debt
- Operating cashflow and free cashflow trend

### Risks
- 3-5 concrete risk points with short evidence

### Bottom Line
- Bull case (1-2 bullets)
- Bear case (1-2 bullets)
- Key upcoming date (earnings, guidance, etc.)
```

## When NOT to use this skill

- User asks for technical chart analysis only (pure TA).
- User asks for macro news summary across multiple companies.
- User asks for real-time tick streaming.
