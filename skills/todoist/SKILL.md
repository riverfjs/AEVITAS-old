---
name: todoist
description: Manage tasks, reminders, and cron jobs through the todoist CLI. Use when the user asks to track tasks, set due-date reminders, or create recurring scheduled commands.
---

# Todoist

## Goal

Provide a single command interface for task lifecycle and cron scheduling.

## Hard Constraints

- Always use `todoist` commands for task/cron operations; do not edit storage files directly.
- `todoist cron-list` is the only correct way to list jobs
- `todoist cron-run` accepts **job id only** (from `cron-list`), not job name
- After `cron-run`, do not chain extra checks unless the user explicitly asks
- Gateway must be running for cron commands to work.

## Workflow

1. Ensure bootstrap is done (first use):
   - `bash ~/.aevitas/workspace/.claude/skills/todoist/scripts/bootstrap.sh`
2. Use binary:
   - `TODOIST=~/.aevitas/workspace/.claude/skills/todoist/bin/todoist`
3. Task operations:
   - `$TODOIST add "description" [--due YYYY-MM-DD]`
   - `$TODOIST list`
   - `$TODOIST complete <id>`
   - `$TODOIST delete <id>`
4. Cron operations:
   - `$TODOIST cron-list`
   - `$TODOIST cron-add "<name>" "<shell cmd>" <ms>`
   - `$TODOIST cron-run <job-id>`
   - `$TODOIST cron-delete <job-id>`
5. Explain that `cron-run` is async and result will be delivered by gateway channel.

Common intervals:
- `1h=3600000`
- `6h=21600000`
- `12h=43200000`
- `24h=86400000`

## Output Template

```markdown
Action: <task|cron> <subcommand>
Status: <success/failure>

Details:
- ID: <task-id or job-id>
- Name/Description: <text>
- Schedule/Due: <value if applicable>

Next:
- <suggested next command>
```

## When NOT to use this skill

- User asks for full project planning workflows (not task CRUD/scheduling).
- Task requires external PM platform APIs outside this local todoist CLI.
- Gateway is unavailable and user does not want local-only fallback behavior.
