---
name: flight-monitor
description: Manage recurring flight price monitoring after flight selection. Use for monitor lifecycle, schedule setup, and alert checks; use flight-search for data retrieval and tables.
---

# Flight Monitor

## Goal

Create and manage recurring flight monitors with explicit mode selection and scheduler integration.

## Hard Constraints

- `flight-search` owns flight data fetch and `view.table` formatting; do not duplicate those inside this skill.
- Ask user first whether outbound and return are fixed; never force a mode.
- Do not hardcode check interval; ask user to choose (6h/12h/24h/etc.).
- Use current mode schema only: `roundtrip_locked`, `outbound_day`, `return_after_outbound`.
- Keep monitors if a check temporarily finds no matching flight; retry in next run.
- Scheduled run output is delivered by gateway; do not add extra notification channels ad-hoc.

## Workflow

1. Determine monitor mode:
   - Both outbound and return fixed -> `roundtrip_locked`
   - Outbound not fixed -> `outbound_day`
   - Outbound fixed, return not fixed -> `return_after_outbound`
2. Use `flight-search` first if user has not finalized candidate flights/prices.
3. Create monitor:
   - `add-roundtrip` for fixed roundtrip
   - `add-outbound-day` for outbound day-level watch
   - `add-return-watch` for fixed outbound + floating return
4. Validate with `list`.
5. If user wants automatic checks, create cron job via `todoist cron-add`.
6. On deletion, remove both monitor record and associated cron job.

Create commands:

```bash
bash ~/.aevitas/workspace/.claude/skills/flight-monitor/scripts/monitor.sh add-roundtrip <FROM> <TO> <DEPART_DATE> <RETURN_DATE> <OUTBOUND_FLIGHT> <RETURN_FLIGHT> <BASELINE_TOTAL>
bash ~/.aevitas/workspace/.claude/skills/flight-monitor/scripts/monitor.sh add-outbound-day <FROM> <TO> <DEPART_DATE> [RETURN_DATE]
bash ~/.aevitas/workspace/.claude/skills/flight-monitor/scripts/monitor.sh add-return-watch <FROM> <TO> <DEPART_DATE> <RETURN_DATE> <OUTBOUND_FLIGHT> <OUTBOUND_PRICE>
```

Manage commands:

```bash
bash ~/.aevitas/workspace/.claude/skills/flight-monitor/scripts/monitor.sh list
node ~/.aevitas/workspace/.claude/skills/flight-monitor/scripts/check.cjs
bash ~/.aevitas/workspace/.claude/skills/flight-monitor/scripts/monitor.sh delete <id>
```

Schedule commands:

~/.aevitas/workspace/.claude/skills/todoist/bin/todoist cron-add \
  "flight-monitor-<id>" \
  "node ~/.aevitas/workspace/.claude/skills/flight-monitor/scripts/check.cjs" \
  21600000
~/.aevitas/workspace/.claude/skills/todoist/bin/todoist cron-list
~/.aevitas/workspace/.claude/skills/todoist/bin/todoist cron-delete <job-id>
```

## Output Template

```markdown
Monitor Created:
- ID: <id>
- Mode: <roundtrip_locked|outbound_day|return_after_outbound>
- Route: <FROM>-<TO>
- Dates: <depart / return or N/A>
- Baseline: <price context>

Schedule:
- Enabled: <yes/no>
- Interval: <ms or human-readable>
- Cron Job ID: <id if created>

Next Action:
- <what user can ask next>
```

## When NOT to use this skill

- User is still exploring flight options (use `flight-search` first).
- User asks only for one-time flight lookup with no monitoring.
- User asks for generic travel advice unrelated to monitor lifecycle.
