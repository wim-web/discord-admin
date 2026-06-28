# discord-admin

A small Go CLI for managing Discord server channel organization through the
Discord REST API.

This repository is intentionally dependency-light and safety-focused. The v1
command surface is limited to channel organization workflows plus guarded
category-level role permission overwrites that are expressed in reviewed plans.

## Module

```text
github.com/wim-web/discord-admin
```

## Setup

Use Go 1.26 or newer, then verify the CLI from the repository root:

```bash
go test ./...
go run ./cmd/discord-admin --help
```

Optional local build output should stay out of git:

```bash
go build -o bin/discord-admin ./cmd/discord-admin
```

## Authentication

The CLI reads a Discord bot token from `DISCORD_BOT_TOKEN`. You can export it in
the shell:

```bash
export DISCORD_BOT_TOKEN='replace-with-bot-token'
```

Or load it from a dotenv-style file with the global `--env-file` option:

```bash
go run ./cmd/discord-admin --env-file .env list --guild-id <guild-id>
```

The env file may contain `DISCORD_BOT_TOKEN=...` and, for read-only commands,
optional `DISCORD_GUILD_ID=...`. Do not commit or print token files.

## Usage

All examples below use `go run ./cmd/discord-admin`; replace that prefix with a
built binary such as `bin/discord-admin` if you build one.

### List channels

Print a readable tree of guild categories and channels:

```bash
go run ./cmd/discord-admin list --guild-id <guild-id>
```

If `DISCORD_GUILD_ID` is configured in the environment or env file, `--guild-id`
can be omitted for read-only discovery commands.

### Snapshot current channel state

Write the current guild channel state to JSON:

```bash
mkdir -p snapshots
go run ./cmd/discord-admin snapshot --guild-id <guild-id> --out snapshots/guild.snapshot.json
```

Snapshots may contain private server structure and are ignored by git by
default.

### Create a sample plan

Create a sample safe channel-organization plan:

```bash
mkdir -p plans/local
go run ./cmd/discord-admin plan init > plans/local/sample-plan.json
```

The plan schema currently supports these operations:

- `create_category`
- `create_text_channel`
- `rename_channel`
- `move_channel`
- `set_position`
- `set_permission_overwrites`

`create_category` and `create_text_channel` may include
`permission_overwrites` for role targets only, using `role_name` (resolved at
apply time) or `role_id`. `create_*` operations may also set an `alias`; later
`move_channel` operations can use `parent_alias` to refer to a category created
earlier in the same plan. `move_channel` supports `sync_permissions: true`,
which sends Discord's `lock_permissions` flag so the moved channel syncs to the
target category's permission overwrites.

`set_permission_overwrites` replaces one existing channel or category's full
`permission_overwrites` array with reviewed exact role-only overwrites. It does
not support member overwrites, role creation/changes, or partial add/remove
permission operations. For an additional guard, include
`expected_permission_overwrites` (including `[]` when the current channel should
have no overwrites); `apply --yes` verifies the pre-apply snapshot matches that
expectation before making the PATCH write.

### Dry-run a plan

Validate a plan and print the proposed changes without making Discord API write
calls:

```bash
go run ./cmd/discord-admin apply plans/local/sample-plan.json --dry-run
```

`apply` defaults to dry-run behavior unless `--yes` is provided, so omitting
`--dry-run` is still non-writing.

### Guarded apply

Only apply a reviewed plan after a dry run looks correct:

```bash
go run ./cmd/discord-admin --env-file .env apply plans/local/reviewed-plan.json --yes
```

Guarded apply validates the plan, prints a concise diff, writes a pre-apply
snapshot next to the plan as `<plan-name>.pre-apply-snapshot.json`, and then
performs the supported Discord write operations. Do not use `--yes` from
automation unless the exact plan has already been reviewed.

## v1 safety scope

v1 intentionally excludes:

- Channel deletes.
- Partial/direct per-channel permission mutation outside reviewed exact
  overwrite-replacement and create/sync plans.
- Role creation or role changes.
- Message or member moderation.

Unsupported, destructive, partial/direct permission mutation, role, and unknown
operations are rejected during plan validation before the write path.

## GitHub repository management

The intended repository is `github.com/wim-web/discord-admin` with SSH remote:

```text
git@github.com:wim-web/discord-admin.git
```

Safe local preparation is limited to initializing git and configuring a remote.
Do not commit, push, or create the GitHub repository without explicit approval.

If the GitHub repository does not exist yet, check `gh auth status`. When `gh` is
authenticated as an account with access to `wim-web`, the repository can be
created after approval with `gh repo create`; otherwise create it manually on
GitHub and then add the SSH remote locally.
