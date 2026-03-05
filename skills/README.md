# Skills

Current skill set for `aevitas`. Each skill directory includes:

- `SKILL.md`: behavior contract and usage rules
- `scripts/`: executable implementation
- optional `bin/`, `data/`, `config.json`

## Installed Skills

### `flight-search`

Fetches flight data from ly.com in three modes:

- `outbound_day`
- `return_after_outbound`
- `roundtrip_locked`

Entry:

```bash
node ~/.aevitas/workspace/.claude/skills/flight-search/scripts/search.cjs <mode> ...
```

Source of truth:

- `view.table` is the display-ready block
- `flights[*].price` uses `{ amount, text }`

### `flight-monitor`

Monitoring orchestrator only (state + checks + notifications).  
`flight-search` owns data fetch/format.

Main commands:

```bash
bash ~/.aevitas/workspace/.claude/skills/flight-monitor/scripts/monitor.sh list
node ~/.aevitas/workspace/.claude/skills/flight-monitor/scripts/check.cjs
bash ~/.aevitas/workspace/.claude/skills/flight-monitor/scripts/monitor.sh delete <id>
```

Modes:

- `roundtrip_locked`
- `outbound_day`
- `return_after_outbound`

### `stock-fundamental`

Yahoo Finance fundamental analysis for US/HK/CN symbols.

Only command:

```bash
node ~/.aevitas/workspace/.claude/skills/stock-fundamental/scripts/fetch.cjs <SYMBOL> [quote|financials|balance|cashflow|keystats|analysis|all]
```

### `todoist`

Task + cron manager over gateway WS RPC (`ws://127.0.0.1:18790`).

Binary:

```bash
~/.aevitas/workspace/.claude/skills/todoist/bin/todoist
```

Core commands:

```bash
TODOIST=~/.aevitas/workspace/.claude/skills/todoist/bin/todoist
$TODOIST list
$TODOIST cron-list
$TODOIST cron-run <job-id>
$TODOIST cron-add "<name>" "<shell cmd>" <ms>
$TODOIST cron-delete <job-id>
```

### `browser`

Playwright browser automation for dynamic pages.

Preferred extraction command:

```bash
node ~/.aevitas/workspace/.claude/skills/browser/scripts/scrape.cjs <url> '<javascript>'
```

### `skill-creator`

Documentation/process skill for creating or updating skills.
It defines file-structure and SKILL.md writing standards.

### `python-conda-workspace`

Standard Python execution skill with fixed Conda env `aevitas-workspace`.
Generated helper scripts should be placed under `~/.aevitas/workspace/var/python-conda-workspace/<workname>/`.

Main commands:

```bash
bash ~/.aevitas/workspace/.claude/skills/python-conda-workspace/scripts/bootstrap.sh
bash ~/.aevitas/workspace/.claude/skills/python-conda-workspace/scripts/init-work.sh work1
bash ~/.aevitas/workspace/.claude/skills/python-conda-workspace/scripts/check.sh
bash ~/.aevitas/workspace/.claude/skills/python-conda-workspace/scripts/list-work.sh
# NOTE: list projects with list-work.sh (not run.sh list)
bash ~/.aevitas/workspace/.claude/skills/python-conda-workspace/scripts/install.sh --work work1 <package...>
bash ~/.aevitas/workspace/.claude/skills/python-conda-workspace/scripts/install.sh --work work1 --conda cairo
bash ~/.aevitas/workspace/.claude/skills/python-conda-workspace/scripts/run.sh --work work1 --file main.py
bash ~/.aevitas/workspace/.claude/skills/python-conda-workspace/scripts/clean.sh work1 --all
# Optional cache cleanup for var artifacts:
/cleanup confirm var
```

## Development Notes

- Keep SKILL.md in English.
- Keep one-file-one-responsibility in `scripts/`.
- Prefer absolute workspace paths with `~/.aevitas/workspace/.claude/skills/...`.
- For behavior details, always read each skill's `SKILL.md`.
