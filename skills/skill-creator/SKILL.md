---
name: skill-creator
description: Create or update reusable skills for aevitas. Use when the user asks to build a new skill, refactor repeated workflows into a skill, or improve skill structure and SKILL.md quality.
---

# Skill Creator

Create high-quality, reusable skills for the aevitas runtime.

## Before You Start: Gather Requirements

Before creating a skill, gather:

1. Purpose and scope: what workflow should this skill solve?
2. Trigger scenarios: when should the agent automatically use it?
3. Target location: project skill or runtime workspace skill?
4. Domain constraints: required tools, APIs, reliability constraints.
5. Output style: report template, checklist, strict schema, etc.

Use `AskUserQuestion` when requirements are ambiguous and discrete choices are needed.

## Skill File Structure

### Directory layout

```
skill-name/
├── SKILL.md            # required
├── scripts/            # optional utility scripts
├── bin/                # optional compiled binaries
└── reference.md        # optional detailed docs
```

### Storage locations

Project skill:
- `aevitas/skills/<skill-name>/`

Runtime workspace skill:
- `~/.aevitas/workspace/.claude/skills/<skill-name>/`

## SKILL.md Requirements

- Keep SKILL.md concise (prefer under 500 lines).
- Frontmatter fields:
  - `name`: lowercase letters, numbers, hyphens.
  - `description`: include WHAT and WHEN.
- Write SKILL.md in English for consistent triggering.
- Favor deterministic workflows with explicit stop conditions.

## Description Quality Rules

1. Use third-person style.
2. Include trigger phrases users actually say.
3. State both capability and usage moment.
4. Avoid vague wording like "helper" or "utils".

Good:
- "Fetch and verify one specific news event with minimal tool calls. Use when user asks whether a specific claim is true or wants the latest update on one event."

Bad:
- "Helps with news."

## Authoring Principles

1. One file, one concern.
2. Avoid unnecessary prose; keep instructions operational.
3. Prefer a default path with explicit exceptions.
4. Add hard constraints for expensive tools (search, browser, shell).
5. Add stop conditions to prevent tool loops.

## Recommended SKILL.md Sections

1. Goal
2. Hard constraints
3. Workflow
4. Output template
5. When NOT to use this skill

## Creation Workflow

### Phase 1: Discovery

- Clarify user intent and boundaries.
- Capture tool budget and reliability requirements.
- Identify overlap with existing skills.

### Phase 2: Design

- Choose a specific skill name.
- Draft trigger-rich description.
- Decide script/no-script approach.
- Define success criteria and stop criteria.

### Phase 3: Implementation

- Create directory and SKILL.md.
- Add optional scripts in `scripts/` only when needed.
- Keep commands deterministic and reproducible.

### Phase 4: Verification

Checklist:

- [ ] Name and description are specific.
- [ ] Skill can be discovered by trigger terms.
- [ ] Workflow has explicit stop conditions.
- [ ] Tool usage limits are stated when applicable.
- [ ] Terminology is consistent.
- [ ] No platform-specific irrelevant constraints.

## Anti-Patterns

1. Multiple broad search loops without stop conditions.
2. Huge SKILL.md with low-signal explanations.
3. Mixing unrelated workflows in one skill.
4. Missing "When NOT to use" section.
5. Single giant script for many unrelated actions.

## Minimal Template

```markdown
---
name: skill-name
description: Specific capability and trigger scenarios.
---

# Skill Name

## Goal
One clear objective.

## Hard Constraints
- Budget / safety / stop rules.

## Workflow
1. Step one
2. Step two
3. Stop when condition met

## Output Template
Required output structure.

## When NOT to use this skill
- Boundary cases.
```
