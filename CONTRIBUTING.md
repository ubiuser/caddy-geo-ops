# Contributing

Thanks for contributing to **caddy-geo-ops**! This page covers the workflow. For the
codebase's internal conventions — architecture, logging, privacy, lint rules, the test
layout — see [CLAUDE.md](CLAUDE.md).

## Getting started

```sh
git clone --recursive https://github.com/ubiuser/caddy-geo-ops
# already cloned without --recursive? fetch the fixtures submodule:
make submodule
```

The repo has **two Go modules** — the plugin (root) and `e2e/` — plus the **MaxMind-DB git
submodule** that provides the test fixtures the `internal/ops` tests read. The `Makefile`
drives both modules; run `make` with no arguments to list targets.

## Before you push

Run the same checks CI gates on:

```sh
make check        # lint + unit tests (-race) + e2e tests, across both modules
```

Or individually: `make lint`, `make test`, `make e2e`, `make fmt`, `make tidy`.

- **Lint is a hard gate.** `golangci-lint` must be clean (config: [`.golangci.yml`](.golangci.yml),
  `default: all`). Any `//nolint` directive must name the linter and carry a reason.
- Follow the **logging** and **privacy / PII** principles documented in
  [CLAUDE.md](CLAUDE.md) — e.g. no silent control flow, correct log levels, log fields via
  `internal/logfields`, never log a client IP as free text.
- Keep both modules' dependencies tidy (`make tidy`); update them with `make update-deps`.

## Pull requests

Changes are merged through **pull requests** (squash merge). The PR **title becomes the
squash commit subject**, so use [Conventional Commits](https://www.conventionalcommits.org/)
— `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `ci:`, `chore:`, optionally scoped
(`fix(matcher): …`). A CI check (`PR Title`) enforces this.

PRs also drive the auto-generated release notes, grouped by **label**
(see [`.github/release.yml`](.github/release.yml)), so please label yours:

| Label | Release-notes section | Applied |
|-------|-----------------------|---------|
| `breaking-change` | ⚠️ Breaking Changes | by you |
| `enhancement` / `feature` | 🎉 Features | by you |
| `bug` / `fix` | 🐛 Bug Fixes | by you |
| `documentation` | 📚 Documentation | auto (docs/`*.md` changes) |
| `dependencies` | ⬆️ Dependencies | auto (Dependabot, or `go.mod`/submodule changes) |
| `skip-changelog` | omitted from the notes | by you |

The `Labeler` workflow auto-applies `documentation`, `dependencies`, `ci`, and `tests` from
the files a PR touches; the type labels above (`bug`, `feature`, `breaking-change`) aren't
path-derivable, so add them yourself. Unlabelled PRs fall under **Other Changes**. Use
`breaking-change` whenever a change forces users to adjust their config — it flags the semver
**major** bump.

> **Maintainers:** the custom labels (`breaking-change`, `feature`, `fix`, `skip-changelog`,
> `ci`, `tests`) must exist in the repo — `actions/labeler` applies but does not create them.

Keep PRs focused, include tests for behaviour changes, and update the README / CLAUDE.md when
you change user-facing behaviour or conventions.

## Versioning & releases

Releases are git tags (`vX.Y.Z`, [semver](https://semver.org/)). Pushing a tag publishes a
GitHub Release with generated notes. The **tag is the release** — users pin it via
`xcaddy build --with github.com/ubiuser/caddy-geo-ops@vX.Y.Z`; no binaries are attached,
since a plugin is composed into Caddy by the user via `xcaddy`.

## License

By contributing, you agree that your contributions are licensed under the project's
[MIT License](LICENSE).
