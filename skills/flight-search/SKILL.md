---
name: flight-search
description: Search flight options and prices from ly.com for outbound/day, fixed-outbound return watch, or locked roundtrip views. Use for fetch/display only; monitoring belongs to flight-monitor.
---

# Flight Search

## Goal

Fetch candidate flight options and present script-provided tables without manual price recomputation.

## Hard Constraints

- Always run the script; never guess prices/times.
- Do not use `WebSearch` or generic browser scraping for this task.
- Use script output as source of truth; do not recompute roundtrip totals manually.
- Show `view.table` directly when available.
- Present full returned options; do not silently truncate to top-N.

Price semantics:
- `flights[i].price.amount`: numeric value for comparison
- `flights[i].price.text`: display text from source
- `outbound_day`: `price` is outbound fare
- `return_after_outbound` and `roundtrip_locked`: `price` is roundtrip total
- `return_after_outbound`: `extra = price.amount - outboundPrice`

## Workflow

1. Collect route and dates, then map to IATA codes.
2. Run outbound discovery:
   - `node skills/flight-search/scripts/search.cjs outbound_day <DEPART> <ARRIVE> <DEPART_DATE> oneway`
3. Show `view.table`, ask user to pick outbound option.
4. Run return search with selected outbound:
   - `node skills/flight-search/scripts/search.cjs return_after_outbound <DEPART> <ARRIVE> <DEPART_DATE> <RETURN_DATE> <OUTBOUND_FLIGHT> <OUTBOUND_PRICE>`
5. Show `view.table`, then summarize selected plan.
6. For fixed pairing checks, use `roundtrip_locked` mode directly.

Command reference:

```bash
node skills/flight-search/scripts/search.cjs outbound_day <DEPART> <ARRIVE> <DEPART_DATE> [oneway|roundtrip_context] [RETURN_DATE]
node skills/flight-search/scripts/search.cjs return_after_outbound <DEPART> <ARRIVE> <DEPART_DATE> <RETURN_DATE> <OUTBOUND_FLIGHT> <OUTBOUND_PRICE>
node skills/flight-search/scripts/search.cjs roundtrip_locked <DEPART> <ARRIVE> <DEPART_DATE> <RETURN_DATE> <OUTBOUND_FLIGHT>
```

## Output Template

```markdown
Search Result:
- Mode: <outbound_day|return_after_outbound|roundtrip_locked>
- Route: <DEPART>-<ARRIVE>
- Dates: <depart / return>

Options:
<paste view.table directly>

Selection Guidance:
- <use view.hint or explicit next step>
```

## When NOT to use this skill

- User asks to create recurring monitor/scheduler (use `flight-monitor`).
- User asks for general destination advice without route/date constraints.
- User asks for non-flight travel products (hotel/train).
