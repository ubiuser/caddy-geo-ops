<!-- Use a Conventional Commit PR title, e.g. "fix(matcher): ..." — it becomes the
     squash-merge commit subject and feeds the release notes. See CONTRIBUTING.md. -->

## What & why

<!-- What does this change do, and why? Link any related issue (e.g. "Closes #123"). -->

## Checklist

- [ ] `make check` passes (lint + unit + e2e, both modules)
- [ ] Tests added/updated for behaviour changes
- [ ] README / CLAUDE.md updated if user-facing behaviour or conventions changed
- [ ] PR labelled for the release notes (`enhancement`/`feature`, `bug`/`fix`,
      `breaking-change`, `documentation`, …) — `documentation`/`dependencies` are
      auto-applied; add the rest. See CONTRIBUTING.md
- [ ] No client IPs or other personal data logged as free text (use `internal/logfields`)
