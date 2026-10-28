## Development

Tools and tasks are pinned in `mise.toml` (tasks with multiple modules live in `.mise/tasks/`). All tasks cover every `go.work` module.

- `mise run bootstrap`: install pinned tools and activate the lefthook git hooks.
- `mise run check`: the complete local gate (format check, lint, build, race tests, Semgrep rule tests). CI runs it too.
- `mise run fmt`: apply formatting; `mise run lint`: run golangci-lint (`.golangci.yml`).
- `mise run vuln`: govulncheck (needs network).
- `mise run semgrep`: project rules in `.semgrep/rules/`. Add each new rule with an annotated test fixture.
- `//nolint` must name the linter and give a reason. Outside `adapters/*/redis`, `depguard` blocks Redis, uuid and miniredis imports so the core module keeps zero third-party dependencies (stdlib + `golang.org/x/sync`).
- Commits follow Conventional Commits (`cog verify` on commit-msg).

## Agent skills

### Issue tracker

Issues live in GitHub Issues for coefficient-engineering/cache, via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary: label string equals role name (`needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one root `GLOSSARY.md` plus `docs/adr/`. See `docs/agents/domain.md`.

### Verification

Follow `docs/agents/verification/README.md` to launch and doctor each real user-facing surface, drive its public path, capture evidence, and clean up.
