# discord-admin CLI Design

## Goal

Build a small Go CLI, `discord-admin`, for managing this Discord server's channel structure through the Discord REST API. The first version is intentionally limited to non-permission channel organization tasks so it is safe to use from Hermes, terminal sessions, and future automation.

## Repository and ownership

- Local project path: `/home/ubuntu/discord-admin`
- Intended GitHub repository: `github.com/wim-web/discord-admin`
- Language: Go
- Primary user-facing binary: `discord-admin`
- Secrets: never committed; read from environment variables or an explicitly supplied env file.

## Authentication

The CLI reads `DISCORD_BOT_TOKEN` from the environment. For local use on this server, it may also support `--env-file /home/ubuntu/.hermes/profiles/mio/.env` to reuse the existing Hermes Discord bot token without printing it.

The current token has already been verified for non-destructive Discord API reads against the target guild. The CLI must still treat missing, invalid, or insufficient permissions as normal errors and report the failing endpoint/action without leaking the token.

## Initial command surface

Initial commands:

- `discord-admin list` — list guild categories and channels in a readable tree.
- `discord-admin snapshot` — write the current guild channel state to JSON.
- `discord-admin plan init` — create a sample plan JSON for safe channel organization changes.
- `discord-admin apply <plan.json> --dry-run` — validate and print changes without modifying Discord.
- `discord-admin apply <plan.json> --yes` — apply an already-reviewed plan.

Supported first-version operations:

- Create category.
- Create text channel.
- Rename channel.
- Move channel to category.
- Update channel position/order.

Explicitly out of scope for the first version:

- Channel deletion.
- Permission overwrites.
- Role creation or role modification.
- Message moderation.
- Member moderation.

## Safety model

- `apply` defaults to dry-run behavior unless `--yes` is provided.
- The CLI must validate a plan before any write call.
- The CLI must print a concise diff before applying writes.
- The CLI should create a fresh snapshot before any real apply.
- Destructive operations are not implemented in v1.
- Permission and role operations are not implemented in v1.

## Project structure

```text
/home/ubuntu/discord-admin/
  go.mod
  cmd/discord-admin/main.go
  internal/discordapi/
  internal/plan/
  internal/output/
  docs/superpowers/specs/2026-06-28-discord-admin-cli-design.md
  README.md
  .gitignore
```

## Implementation notes

Use only Go standard library HTTP/JSON support unless a small CLI parser dependency is clearly justified. Keeping v1 dependency-light makes it easier to audit and run on this server.

The API client should be separated from CLI command parsing so read-only operations can be unit tested with `httptest` and plan validation can be tested without network access.

## Verification

Minimum verification for v1:

- `go test ./...` passes.
- `go run ./cmd/discord-admin --help` works.
- `discord-admin list --env-file /home/ubuntu/.hermes/profiles/mio/.env` can read the current guild without printing secrets.
- `discord-admin snapshot --env-file /home/ubuntu/.hermes/profiles/mio/.env --out <file>` writes valid JSON.
- `discord-admin apply <sample-plan> --dry-run` validates and prints planned changes without calling write endpoints.

## Kanban management

Track work on a dedicated Hermes Kanban board named `discord-admin`. Implementation cards should be small, independently verifiable, and assigned to the coding worker profile where appropriate. Cards that require system package installation, GitHub repository creation, or real Discord writes must state the approval requirement explicitly.
