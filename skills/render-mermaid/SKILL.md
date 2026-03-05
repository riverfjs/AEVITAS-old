---
name: render-mermaid
description: Render Mermaid diagram text into an image file. Use when the user asks for flowchart/sequence/state diagrams.
---

# Render Mermaid

## Goal

Render Mermaid text into an image and send it via `SendFile`.

## Hard Constraints

- Do not send Mermaid as `.md` file.
- Source file uses fixed name `diagram.mmd` (overwritten each call).
- Output path is determined by script internally (timestamped `.webp`).
- Exactly 3 tool calls: `Write` → `Bash` → `SendFile`.

## Workflow

1. `Write` Mermaid source (no markdown fences) to:

`$HOME/.aevitas/workspace/var/render-mermaid/diagram.mmd`

2. `Bash`:

```bash
python3 "$HOME/.aevitas/workspace/.claude/skills/render-mermaid/scripts/render_mermaid.py" --input "$HOME/.aevitas/workspace/var/render-mermaid/diagram.mmd"
```

3. Read `image_path` from JSON stdout, call `SendFile` with that path.

## Output Template

```markdown
Diagram rendered successfully.

- File: `<image_path>`
- Live: `<live_url>`
```

## When NOT to use this skill

- User asks for text-only explanation.
- User explicitly asks for raw Mermaid source file.
