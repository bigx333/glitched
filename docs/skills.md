# Skills

Glitched gives the agents it starts skills of their own: folders with a
SKILL.md, as Claude Code and Codex read them. An agent you run yourself in a
terminal doesn't see them. Only the agents Glitched starts get them.

## Where to put them

- **Yours** go in `~/.glitched/skills/<name>/SKILL.md`. Every thread on that
  machine gets them.
- **A project's** go in `<project>/.glitched/skills/<name>/SKILL.md`. Every
  thread in that project gets them. Commit them so that everyone on the
  project gets them too.

A project's skill replaces one of yours that has the same name. The Skills
pill in the thread's header marks it **replaces yours**.

A thread in a worktree reads the worktree's `.glitched/skills`. If the
worktree has none, which is what happens when git doesn't track them, it
reads the project's own folder. A scratch thread has no project, so it gets
only yours. Each machine's engine reads its own folders, so a thread on a
paired machine gets that machine's skills.

## What a skill must have

Glitched checks each skill before any agent gets it:

- The folder's name is up to 64 lowercase letters, digits and single
  hyphens, and starts and ends with a letter or a digit.
- SKILL.md starts with frontmatter between `---` lines, with a `name`
  the same as the folder's and a `description` of at most 1024 characters.
  Each key appears once. Other keys are for the agents.
- A link in a project's skills stays inside the project, and no link leads
  into Glitched's own folder (`~/.glitched`).
- A skill holds at most 1000 files, 24 folders deep and 8 MiB in all. Glitched
  reads at most 1000 skills from each folder.

A skill that fails a check is skipped. The pill shows it under **Skipped**,
with the reason, and no agent gets it.

## What each agent sees

| Agent | How it gets them | When a change applies |
| --- | --- | --- |
| Claude Code | As `glitched:<name>`, from a plugin for that session alone, so you call one as `/glitched:<name>` | The next turn |
| Codex | As `glitched:<name>` in its skill list, from a folder Glitched adds for that session | The next turn |
| Antigravity and other ACP agents | Listed in the first message Glitched sends each time it starts the agent, with where each skill's SKILL.md is | The next turn, when Glitched starts the agent again with the new list. A conversation it takes up again still holds the earlier list in its history |

Glitched copies the skills a run gets into `~/.glitched/run-skills`
and gives the agent the copy. A skill you edit during a run doesn't change
under the agent. Glitched deletes the copies once no run uses them. It
never changes your `~/.claude` or `~/.codex`.

## Seeing them

The **Skills N** pill in a thread's header counts the skills that the
thread's agent gets on its next run. Click it to see each skill, where it
comes from, the skills that were skipped and why, and how the agent gets
them. Glitched reads them again each time the thread is shown and each time
the window comes back into focus. Plugins read the same list with
`glitched.skills.list(thread)` (see [plugins](plugins.md)).
