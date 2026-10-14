# Contributing

New contributors should start with bug fixes. Pick an available issue labelled
`bug`, comment to claim it, and open a focused PR linked to that issue.
Feature requests need core-team approval before implementation starts.

Bug reports, docs, tests, and help investigating issues are welcome too.

[![Discord](https://img.shields.io/badge/Discord-join%20the%20community-5865F2?style=for-the-badge&logo=discord&logoColor=white&logoSize=auto)](https://discord.com/invite/UZv7JjxbwG)

Use GitHub for reports, proposals, and review. Use Discord for questions,
help, and the weekly public contributor call.

## Ways to contribute

| Type             | Examples                                       |
| ---------------- | ---------------------------------------------- |
| Code             | Bug fixes; feature work after core-team approval |
| Docs             | README, `docs/`, architecture notes            |
| Triage           | Repro bugs, tighten reports, label suggestions |
| Examples / tests | Recipes, edge cases, flaky-test hunts          |

## Quick start

1. Read [AGENTS.md](AGENTS.md) for setup, commands, and PR requirements.
2. Pick an [open bug](https://github.com/OrchestratorInc/agent-orchestrator/issues?q=is%3Aissue%20is%3Aopen%20label%3Abug). Check its assignee, comments, and linked PRs to see whether someone has already claimed it. Issues marked `blocked` or `needs-discussion` need a maintainer's decision first.
3. Comment `I'd like to work on this` before starting. Follow any scope or coordination notes already on the issue.
4. Open one focused PR with `Fixes #123` in its description, using the issue it actually fixes. Describe the change and how you checked it.
5. Address review feedback and get the required checks passing. Maintainers review and merge bug fixes through the normal PR process.

The `good first issue` and `help wanted` labels can help you find a starting
point. Check that the issue is a bug and is still available before claiming it.

Need the product/run overview first? Start with [README.md](README.md),
[docs/architecture.md](docs/architecture.md), and
[docs/development.md](docs/development.md).

Two onboarding notes matter on current `main`:

- On fresh Linux setups, prefer `cd frontend && npm run package` unless you have also installed distro packaging tools such as `rpm`/`rpmbuild` for `npm run make`.
- Mobile companion app docs are still being filled in. Do not assume `packages/mobile/README.md` is a complete headless setup guide on this branch.

### Bugs and features

Use the [GitHub issue forms](https://github.com/OrchestratorInc/agent-orchestrator/issues/new/choose) for bugs and feature requests. A bug describes existing behavior that is broken. A feature request proposes a new capability or a change to how the product works; its label is `enhancement`.

For a feature request, wait for discussion on a contributor call and an
explicit core-team decision on the issue before starting implementation. The team decides
whether to build it, its scope, priority, and who should work on it. A
`needs-discussion` label means that decision is pending. Classification alone
does not approve feature work, and feature PRs opened before approval wait for
that decision before review. Larger changes may also need a design document.

If an issue seems misclassified, explain why in the
[bug-triaging channel on Discord](https://discord.com/channels/1476302178913357958/1491735678156013588)
and link the issue.

For bugs, only a description is required. Write a short, concrete report in your own words: what you did and what went wrong. When available, add what you expected, steps to reproduce, how often it happens, AO version and OS, or screenshots/a recording. You can report an intermittent bug even if you cannot reproduce it reliably.

Search existing issues when possible; add your observations to a matching issue. Submit from your own GitHub account so maintainers can follow up with the person who experienced the problem. Please don't ask AO Bot to file reports on your behalf.

Agent assistance is welcome. We recommend asking your local coding agent to use the [bug-triage skill](.agents/skills/bug-triage/SKILL.md) to ask useful follow-up questions and gather supporting evidence. The issue body should contain only observations you supplied, without invented steps, impact, or root-cause claims. Keep agent-collected logs, relevant database excerpts, and analysis in separate, clearly labeled attachments; remove secrets and unrelated personal data before sharing. Evidence and screenshots are optional, and can be added later.

For example, ask your local agent:

```text
Use .agents/skills/bug-triage/SKILL.md to help with this bug: <what I observed>.
Ask about missing details that would help, draft a concise report using only
what I tell you, and collect relevant evidence separately for me to attach.
```

Review the draft and attachments before submitting. A clear observation is useful on its own; no diagnosis or proposed fix is needed.

### Pull requests

New PRs are prefilled from [`.github/pull_request_template.md`](.github/pull_request_template.md).
Also follow **PR hygiene** in [AGENTS.md](AGENTS.md): branch from `main`, one issue per PR, conventional commits, explain intentional omissions, and keep CI green for the area you touched.

Coding agents should follow this same process. Have your agent read this guide
and AGENTS.md before choosing an issue or writing code. Review its changes and
PR description before submitting.

### Weekly contributor call

The weekly public call is open to everyone. Join to show bug fixes, discuss
PRs waiting for review, bring feedback, or talk through feature requests with
the core team. Rishet shares the time and public call link on Discord.

We cover the past week's issues, feature requests, and PRs, including work that
has merged. Two or three merged bug fixes can lead to a separate conversation
with Prateek about joining the team.

## Code of Conduct

Be respectful, constructive, and assume good intent. Report problems to maintainers via Discord DM.

Thanks for making agent-orchestrator better for the next person who shows up.
