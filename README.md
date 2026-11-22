# Together

Together is a self-hosted project and work management app for one team or organization. One Go binary serves the JSON API and the compiled React interface; SQLite is embedded, so there is no database server, no message queue and no cloud dependency. The interface ships in English, Italian and Spanish, with a light and a dark theme that can be switched at any time.

It is deliberately small: no marketplace or plugins. Each project owns independent copies of its workflows, while the installation has one set of people and one source of truth.

## Contents

- [How it is built](#how-it-is-built)
- [Feature tour](#feature-tour)
- [Roles and access](#roles-and-access)
- [Requirements](#requirements)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [First run](#first-run)
- [Language and appearance](#language-and-appearance)
- [Work items](#work-items)
- [Workflow](#workflow)
- [Automations](#automations)
- [Notifications and email](#notifications-and-email)
- [Search](#search)
- [Reports and calendar](#reports-and-calendar)
- [Two-factor authentication](#two-factor-authentication)
- [API](#api)
- [Data model and storage](#data-model-and-storage)
- [Reverse proxy](#reverse-proxy)
- [Operations](#operations)
- [Project layout](#project-layout)
- [Development and testing](#development-and-testing)
- [Release](#release)
- [Out of scope](#out-of-scope)

## How it is built

- **One binary.** `cmd/server` mounts the API, serves the embedded single-page app and runs the schedulers. The React build is embedded through `internal/web/dist`, so deploying means copying one file.
- **One database.** SQLite in WAL mode with foreign keys and a busy timeout. The pure-Go driver means `CGO_ENABLED=0` and no C toolchain.
- **Server is the authority.** The UI hides what a role cannot use as a courtesy; every request is checked again on the server.
- **The API is the product contract.** Screens call `/api/...`; nothing reads the database from the browser.
- **Nothing is hard-deleted.** Projects, teams and items are archived with `archived_at`; history is append-only. A backup is a complete copy of the state.
- **No external requests at runtime.** Fonts, icons and the logo are served by the binary. SMTP is the only optional outbound integration.
- **Project-owned workflows.** Each project can own multiple independent flows and bind one to each task type. Standard kits are copied locally; changes never affect another project.

## Feature tour

- **Setup and sign-in.** First visit creates the only administrator. Everyone else joins through a one-use invitation link; there is no open registration. Optional TOTP second factor and personal API tokens.
- **People, teams, projects.** Reusable teams, direct project members, open or private visibility, logical archiving.
- **Work items.** Observer, Action, Micro and Bug with title, description, priority, multiple assignees, due date, comments with mentions, append-only history and archive cascade from Action to Micro.
- **Workflow.** Configurable states with open/done categories, transitions with role, assignee, priority and item-type conditions, and a draggable editor.
- **Automations.** Rules triggered by state change, assignment, comment or overdue date, running ordered actions in one transaction.
- **Views.** My work, project lists, global search, calendar and server-backed reports.
- **Notifications.** In-app inbox with live updates over Server-Sent Events, a polling fallback, and optional email for assignments, mentions and the daily due summary.
- **Search.** SQLite FTS5 over titles, descriptions and comments, with a small documented query language, ranked results and plain-text snippets.
- **Personalization.** English, Italian and Spanish; light and dark themes; language and theme switchable from the top bar, the auth screens and the profile.
- **Operations.** Admin backup download, SMTP settings, search-index rebuild, 2FA enrollment policy, health and readiness probes, graceful shutdown.

## Roles and access

Roles are installation-wide, not per project.

| Capability | Admin | Manager | Member | Guest |
|---|---|---|---|---|
| Users, invitations, SMTP, backup, 2FA policy | yes | no | no | no |
| Create projects | yes | yes | no | no |
| Manage a project's settings and grants | yes | projects they created or belong to | no | no |
| Teams | yes | yes | no | no |
| Workflow configuration | yes | no | no | no |
| Automations | all rules | rules for projects they manage | no | no |
| Create and edit work items | yes | yes | yes | no |
| Read projects and items | everything | everything readable | everything readable | readable projects, until access expires |

Visibility rules:

- **Open project**: readable by every signed-in user, guests included.
- **Private project**: readable by admins, the creator, direct members and members of a non-archived assigned team.
- **Archived project**: hidden from normal views for everyone except admins and the managers who manage it.
- **Guest**: read-only while access lasts. Invitations choose 7, 30 or 90 days; an expired guest cannot sign in, keeps their data and can be extended by an admin.

## Requirements

- Go 1.26 or newer to build the binary.
- Node 22 or newer to build the embedded frontend.
- PowerShell 5.1 or newer on Windows, or PowerShell 7+ on any OS, for the release scripts.
- No database server and no C toolchain: SQLite is embedded and the driver is pure Go.

## Quick start

### Development loop

```powershell
npm install
go run ./cmd/server          # API and built UI on http://127.0.0.1:8080
npm run dev                  # Vite on http://127.0.0.1:5173, proxies /api to :8080
```

The Vite dev server serves `web/` with hot reload and proxies API calls to the Go process. A first run creates `app.db` in the platform user data directory (or the configured `data_dir`), applies migrations and seeds the default states. Open the app, create the administrator and sign in.

Useful checks while developing:

```powershell
npm run typecheck            # TypeScript, no emit
go vet ./...
go test ./... -count=1
```

### Release build

```powershell
npm install
powershell -File scripts/build.ps1
```

`scripts/build.ps1` compiles the frontend into `internal/web/dist` (which the Go build embeds), then cross-compiles every declared target into `dist/` with `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false` and stripped symbols:

| Target | Output |
|---|---|
| Windows amd64 | `dist/together-windows-amd64.exe` |
| Windows arm64 | `dist/together-windows-arm64.exe` |
| Linux amd64 | `dist/together-linux-amd64` |
| Linux arm64 | `dist/together-linux-arm64` |

A single target on demand:

```powershell
powershell -File scripts/build.ps1 -Targets linux/amd64
```

Or by hand:

```powershell
npm run build
$env:GOOS="linux"; $env:GOARCH="arm64"; $env:CGO_ENABLED="0"
go build -trimpath -buildvcs=false -ldflags "-s -w" -o dist/together-linux-arm64 ./cmd/server
```

A local build without release flags is `go build -o together ./cmd/server`. Other Go targets may compile but are not part of the tested release contract. Rebuilding the same commit with the same Go version produces byte-identical binaries.

`scripts/smoke.ps1` starts a built binary on a free port with a temporary database and checks health, setup state, the SPA shell and the database file:

```powershell
powershell -File scripts/smoke.ps1
powershell -File scripts/smoke.ps1 -Binary dist/together-linux-amd64
```

### Branding

The sidebar and sign-in mark is `web/public/assets/logo-256.png`, served as `/assets/logo-256.png`; the favicon is `web/public/logo.ico`. To swap the artwork, replace `web/assets/logo.png` with a square PNG and regenerate the served file:

```powershell
powershell -File scripts/make-logo.ps1
```

The master artwork stays outside `web/public`, so only the resized copy is embedded in the binary.

## Configuration

Configuration is read from `config.ini` in the working directory. Environment variables override values in the file. `CONFIG_PATH` points at a different ini file. The first-run screen creates the administrator and can optionally save SMTP settings; address/port, TLS and cookie security are intentionally left to the site operator or system administrator.

```ini
addr=:8080
# data_dir defaults to the platform user application-data location
locale=en
secure_cookies=false
# tls_auto=true
# tls_cert=/etc/together/fullchain.pem
# tls_key=/etc/together/privkey.pem
# tls_redirect=:80
# db_path=/path/to/Together/app.db
# smtp_host=mail.example.test
# smtp_port=587
# smtp_user=
# smtp_pass_enc=
# smtp_from=together@example.test
# smtp_tls=true
```

| ini key | environment | default | notes |
|---|---|---|---|
| `addr` | `ADDR` | `:8080` | Listen address. |
| `data_dir` | `DATA_DIR` | `%LOCALAPPDATA%/Together` on Windows; user config directory `/Together` elsewhere | Directory created at startup; holds the database, `secret.key` and, with `tls_auto`, the generated certificate. Operators can override it for managed deployments. |
| `db_path` | `DB_PATH` | `data_dir/app.db` | Explicit database path; normally leave unset so it stays inside `data_dir`. |
| `locale` | `LOCALE` | `en` | Server locale setting (`en`, `it` or `es`). The first-run choice is saved to the administrator profile, not to `config.ini`; the web UI follows browser/user preferences. |
| `secure_cookies` | `SECURE_COOKIES` | `false` | Set `true` when HTTPS terminates at the proxy. Forced on when the binary serves TLS. |
| `tls_auto` | `TLS_AUTO` | `false` | Serve HTTPS with a certificate generated in `data_dir`. The browser warns about a self-signed certificate, but the traffic is encrypted. |
| `tls_cert` | `TLS_CERT` | – | PEM certificate chain. Must be set together with `tls_key`. |
| `tls_key` | `TLS_KEY` | – | PEM private key. Must be set together with `tls_cert`. |
| `tls_redirect` | `TLS_REDIRECT` | – | Optional plain HTTP address that redirects every request to the HTTPS port of `addr`. Requires TLS. |
| `smtp_host` | `SMTP_HOST` | – | Enables email when host, port and sender are set. |
| `smtp_port` | `SMTP_PORT` | `0` | Required when `smtp_host` is set. |
| `smtp_user` | `SMTP_USER` | – | Optional SMTP username. |
| `smtp_pass_enc` | – | – | SMTP password sealed with `secret.key` in `data_dir`. Written by first-run setup or the admin screen; never clear text. |
| `smtp_pass` | `SMTP_PASS` | – | Only read to migrate an old clear-text password from the ini file; it is sealed on the next start and the key is removed. The environment variable keeps the value in memory and is never written. |
| `smtp_from` | `SMTP_FROM` | – | Sender address. |
| `smtp_tls` | `SMTP_TLS` | `false` | Use STARTTLS when true. |

SMTP is optional and is not required to start. During first-run setup, an optional email section can save SMTP settings to the ini file; admins can also edit them later in the admin area. Updates are atomic and an empty password field keeps the stored one. Passwords are sealed with the installation key and never stored in clear text. Environment-provided SMTP values remain authoritative at runtime.

### Transport security

The binary serves plain HTTP unless TLS is configured, so passwords and session cookies would travel readable on the network. Two options:

- `tls_auto=true`: the server creates `tls-cert.pem` and `tls-key.pem` in `data_dir` and serves HTTPS with HTTP/2. Good for a single machine or a trusted LAN; browsers show a self-signed warning.
- `tls_cert` and `tls_key`: a real certificate, for example from Let's Encrypt, either served directly or copied from a proxy that no longer terminates TLS.

A TLS-terminating proxy remains supported: forward to `addr`, keep the original `Host` header, and set `secure_cookies=true`. The binary adds `Strict-Transport-Security` whenever it serves TLS itself, marks the session cookie `Secure` automatically and refuses to start with an inconsistent TLS configuration.

### Secrets at rest

Passwords are bcrypt hashes. Session tokens, personal API tokens and invitation links are stored only as SHA-256 digests; a personal token is shown once, when it is created, and only a short prefix stays visible afterwards. TOTP secrets and the SMTP password must be readable by the server, so they are sealed with AES-256-GCM under `data_dir/secret.key`, a 32-byte key created on first start with owner-only permissions. Back that file up together with the database: losing it makes existing TOTP enrollments and the stored SMTP password unreadable, and they then have to be reset. A database upgraded from an older version is converted on the first start of this release, which is logged once.

## First run

1. Place the binary for the target machine in its own directory, for example `/opt/together/together-linux-amd64`.
2. Create `config.ini` next to it. Set `addr` for the listener; configure TLS and `secure_cookies=true` as required by the deployment. The default data directory is per-user and does not need to be specified.
3. Start the binary as an unprivileged user. A minimal systemd unit:

   ```ini
   [Unit]
   Description=Together
   After=network.target

   [Service]
   User=together
   WorkingDirectory=/opt/together
   ExecStart=/opt/together/together-linux-amd64
   Environment=CONFIG_PATH=/opt/together/config.ini
   Restart=on-failure

   [Install]
   WantedBy=multi-user.target
   ```

   The process handles `SIGTERM`: it stops accepting requests, drains for up to 15 seconds, stops the scheduler and closes the database.
4. Open the public URL. The first visit shows the setup form, which creates the only administrator account in the chosen language.
5. Create projects and invite people. SMTP is optional; invite links are always copyable.
6. Check `GET /api/ready` returns `200` with `{"ok":true,"ready":true}` before sending traffic.
7. Download a first backup from the admin area and store it outside the server.

On first start the binary creates the platform-specific user data directory (unless `data_dir`/`DATA_DIR` overrides it), opens `app.db` inside it unless `db_path`/`DB_PATH` is explicitly set, applies migrations in order and seeds the default workflow states To do, In progress and Done. Each new project receives its own Default workflow and initial state, bound to Observer, Action, Micro and Bug; administrators can add transitions or create a local flow from a kit. The process writes `app.db` and its SQLite WAL sidecars; there are no attachments and no other storage.

## Language and appearance

The interface is available in English, Italian and Spanish. English is the source language: every user-visible string goes through `t()` in `web/src/i18n.tsx`, and the Italian and Spanish dictionaries contain exactly the same keys.

- **First visit**: the language comes from `navigator.languages`, falling back to English when the browser asks for an unsupported language.
- **Signed in**: the choice is stored on the account and follows the person across browsers. Changing it from the top bar, a sign-in screen or Profile → Language updates the account immediately.
- **New accounts**: setup and invitation acceptance store the language chosen on that screen.
- **Dates and numbers**: formatted with `en-GB`, `it-IT` or `es-ES`.

The theme is light or dark. It starts from the operating-system preference (`prefers-color-scheme`) and is remembered per browser in `localStorage`. The toggle sits in the top bar and in Profile → Theme, and the stored theme is applied before the first paint so a reload does not flash the wrong palette. Both preferences are also available on the offline/boot screen.

## Work items

Four fixed types share the same fields:

- **Observer**: optional container for Actions. An item with no parent is valid.
- **Action**: the normal unit of work.
- **Micro**: lives inside an Action, one level only. Archiving the Action archives its Micro items in the same transaction.
- **Bug**: same fields as an Action, no required parent.

Fields: title, description, priority (`none`, `low`, `medium`, `high`, `urgent`), zero or more assignees, due date (a day, no time), comments with mentions, and append-only history. New items receive the configured initial state.

Rules enforced by the server:

- An Observer has no parent. An Action or Bug may point to an Observer in the same non-archived project. A Micro must point to an Action in the same project.
- Moving or re-parenting validates the same hierarchy; detaching clears the parent.
- Changes to title, description, priority, due date, type, parent, assignees and state write history events. Assignee changes store the ordered ids.
- Comments may mention people; mentions are rows, revalidated against project access and rewritten when a comment is edited. Authors and admins may edit comments.
- Archiving is logical: rows disappear from views but are never purged.

## Workflow

Each project owns one or more workflows. A workflow can be reused by several task types within that project; projects cannot share mutable flow records. The kit catalog (Project Manager, Development, Design, Customer Service and Sales, with lightweight variants) creates a local copy that can be edited independently.

- States have a name, a category (`open` or `done`), a position and one initial state. At least one open and one done state must remain.
- Transitions are unique arrows from one state to another, with a label and optional conditions.
- Conditions are simple data, not scripts: `role` (admin, manager, member), `assignee` (yes), `priority`, `item_type`. Conditions of the same kind are ORed; different kinds are ANDed. No conditions means anyone who can write may use the arrow.
- Guests never transition. A transition applies only when the arrow exists from the current state and all conditions hold; state change, history event, transition actions and automation rules happen atomically.
- Administrators configure workflows in project settings. Reassigning a task type that has items requires a complete state mapping; all affected items move transactionally and receive history events. Changes stay within that project and do not affect other projects.
- State selectors spanning projects use each project's state catalog. Automation rules retain state-name matching so existing rules keep working when project-owned copies are introduced.
- Workflow editing is available from project settings. The canvas keeps wide diagrams inside its own scrollable area so a large flow does not widen the entire page, including on narrow screens.

## Automations

Rules react to work without scripts. Triggers: `state_changed` (with an arrival state), `assigned`, `comment_added` and `due_passed`. Actions run in order: `set_state`, `assign_user`, `add_comment` and `notify`.

- Every rule applies to all projects or to an explicit project list.
- Admins manage all rules; managers only rules whose projects they can manage; other roles cannot reach the API.
- Actions run inside one transaction and never re-enter the rule engine, so a rule cannot cascade into itself or another rule in the same round.
- `due_passed` is evaluated once a day by the scheduler for open items whose due date has passed. A per-rule/per-item marker keeps it idempotent across retries and restarts.
- The `notify` action reaches only the current assignees, in-app; email is sent only when the recipient enabled it and SMTP is configured.

## Notifications and email

Notifications cover assignments, comment mentions, one daily due summary per person and automation notices. Marking read is per user; badge counts ignore items or projects the recipient can no longer read.

- Deduplication is enforced by partial unique indexes: one unread assignment per user/item, one mention per user/comment and one due summary per user per UTC day.
- `GET /api/notifications/stream` is a Server-Sent Events connection. Each browser tab keeps one; the server sends a heartbeat every 25 seconds and the browser reconnects automatically. After three consecutive failures the client falls back to polling every 60 seconds.
- The server keeps at most 5 streams per user and 500 per process; a slower client is asked to resync and is disconnected.
- The stream authenticates with the session cookie only, because `EventSource` cannot send headers. Personal API tokens work on JSON endpoints but cannot open the stream.
- The stream carries counts and notification ids, never titles or content: the screen reloads through the normal API, which applies project visibility.
- Email is sent only for assignment, mention and due summary, and only when SMTP is configured and the person enabled email in the profile. `emailed_at` is stored only after the mail server accepts the message.
- Email bodies for notifications and the SMTP test message follow the recipient's language (English, Italian or Spanish); invitation emails are English.

## Search

`GET /api/items?q=...` uses an SQLite FTS5 index over titles, descriptions and comments. The index is a normal database table, so it travels with the backup and needs no external service.

The query language is deliberately small:

| Input | Meaning |
|---|---|
| `release notes` | Both words must be present (AND). |
| `depl` | The last positive term is a prefix match, so it finds "deploy" and "deployment". |
| `"release notes"` | Exact phrase, in that order. |
| `release -draft` | Contains "release", excludes "draft". |

Matching ignores case and diacritics, so `caffe` finds "Caffè". FTS operators (`OR`, `AND`, `NOT`, `NEAR`), field prefixes (`title:`), parentheses, `*` and queries over 256 characters are rejected with `400 invalid search query`; they are not a second language to learn.

Results carry two extra fields: `snippet` is plain text with `«»` around the matched words (never HTML) and `match` says which column matched (`title`, `description` or `comments`). Ranking is `bm25` with title weighted 5, description 2 and comments 1. Visibility is applied in the same SQL `WHERE` clause, so search can never return a row or an excerpt from a private project the caller cannot read.

The list endpoint also accepts `state_category` (`open`/`done`), `created_from`, `created_to`, `updated_from`, `updated_to` (inclusive `YYYY-MM-DD`), `has_assignee=1`, `parent_id`, `sort` (`relevance`, `updated`, `created`, `due`, `priority`) and `total=1`. With `total=1` the response adds `total`, the number of rows the same filters would return without pagination.

Normal writes keep the index current through triggers on items and comments; no action is required. If a database is restored by hand or results look stale, an admin can rebuild it from the admin area or with `POST /api/admin/search/rebuild` (admin only, CSRF protected). The endpoint returns `{"indexed": n}`. On a build without FTS5 the server falls back to a case-insensitive `LIKE` search over the same three fields: results and visibility are unchanged, but there are no snippets and no relevance ordering.

## Reports and calendar

- Reports aggregate over the projects the caller can read: counts per state (including states with no items) and open workload per person. Expired guests still count when work remains assigned. A filtered private project answers `404` instead of leaking counts.
- The workload view is not a performance measure: an item with several assignees counts once per person.
- The calendar lists items with a due date by month and day, scoped to the caller's readable projects or to one project.

## Two-factor authentication

Admin, Manager and Member accounts can add a TOTP second factor from Profile → Security. The screen shows an `otpauth://` QR code generated in the browser plus the Base32 secret for manual entry; the secret is never returned again after confirmation. Each account gets ten single-use recovery codes, shown once, that can be copied or downloaded.

- Codes follow RFC 6238 (SHA-1, six digits, 30-second step, ±1 step window) using only the Go standard library. A step already accepted is refused, so a code cannot be replayed.
- Sign-in becomes two steps only for accounts that enabled it: the password returns `{"two_factor_required":true,"challenge_token":...}` and no cookie, then `POST /api/login/2fa` exchanges the challenge plus a TOTP or recovery code for the normal session.
- A challenge lives 5 minutes, allows 5 attempts and is single use. Recovery codes are single use and stored only as SHA-256 hashes.
- Disabling 2FA or regenerating recovery codes requires the current password and a valid code.
- Guests are never challenged and cannot enroll. An admin can require enrollment for everyone else in Admin → Two-factor authentication (`require_2fa`). A non-guest without a confirmed factor must enroll after the password and before any session exists; invitation acceptance follows the same rule.
- Sessions already open stay valid until they expire; turning the policy off does not remove existing enrollments.
- If someone loses both the app and the recovery codes, an admin can reset their second factor from People → Reset 2FA. The reset is disabled on your own account, deletes the secret and every recovery code, cancels pending challenges and revokes that person's open sessions.

Two residual risks are accepted. First, the installation key lives on the same machine as the database: sealing protects backups, database copies and a leaked `config.ini`, but not a fully compromised host, so filesystem permissions and trustworthy backups remain part of the operating model. Second, personal API tokens are single-factor and keep working on JSON endpoints even when 2FA is enabled; they are long-lived automation secrets, not interactive sign-in, and cannot open the notification stream.

## API

The API uses same-origin session cookies. Mutating cookie requests are checked against `Origin`/Fetch Metadata and must come from the app origin. Requests have a 1 MiB body limit and strict single-value JSON decoding.

Personal API tokens authenticate with a bearer header instead of a cookie:

```bash
curl -H "Authorization: Bearer tgr_..." https://together.example.com/api/me
```

Tokens carry the same permissions as the owner's current role, never expire and are managed in the profile. The value is returned only by the `POST /api/tokens` response that creates it; later reads return a short prefix, so capture it and store it where a password would go. The last active token cannot be revoked. Prefix is `tgr_`; treat a token like a password. Setup creates a first token so the account is never without one, but its value is not displayed: create and copy your own token in the profile, then revoke the unused one.

Endpoint groups:

| Area | Endpoints |
|---|---|
| Boot and auth | `GET /api/health`, `GET /api/ready`, `GET/POST /api/setup`, `POST /api/login`, `/api/login/2fa*`, `POST /api/logout` |
| Self | `GET/PUT /api/me`, `/api/me/2fa*`, `GET/POST/DELETE /api/tokens*` |
| People | `GET /api/people`, `GET /api/users`, `GET/PUT /api/users/{id}`, `POST /api/users/{id}/2fa/reset` |
| Invitations | `GET/POST /api/invites`, `GET /api/invites/{token}`, `DELETE /api/invites/{id}`, `POST /api/invites/accept` |
| Teams | `GET/POST /api/teams`, `DELETE /api/teams/{id}`, `/api/teams/{id}/members*` |
| Projects | `GET/POST /api/projects`, `GET/PUT/DELETE /api/projects/{id}`, `/members*`, `/teams*`, `GET /api/projects/{id}/people` |
| Items | `GET /api/items`, `POST /api/projects/{id}/items`, `GET/PUT/DELETE /api/items/{id}`, `/comments*`, `/events`, `/transitions*` |
| Workflow catalog | `GET /api/workflow/presets`, `GET /api/workflow/states` |
| Project workflows | `GET/POST /api/projects/{id}/workflows`, `PUT/DELETE /api/projects/{id}/workflows/{workflow}`, `POST /api/projects/{id}/workflows/{workflow}/duplicate`, `PUT /api/projects/{id}/item-types/{type}/workflow`; state, initial-state and transition operations are nested under `/api/projects/{id}/workflows/{workflow}` |
| Notifications | `GET /api/notifications`, `/unread-count`, `/stream`, `POST /api/notifications/read-all`, `/api/notifications/{id}/read` |
| Reports | `GET /api/reports` |
| Automations | `GET/POST /api/automations`, `GET/PUT/DELETE /api/automations/{id}` |
| Administration | `GET /api/admin/backup`, `POST /api/admin/search/rebuild`, `GET/PUT/POST /api/admin/smtp*`, `GET/PUT /api/admin/security` |

Errors are JSON objects with a human-readable `error` field. `400` means invalid input, `401` a missing or expired session, `403` a denied role or origin, `404` an invisible or missing record (private projects answer as missing), `409` a conflict such as a duplicate name, and `429` rate limiting on login.

## Data model and storage

SQLite, one file, created at first start. Migrations are numbered, recorded in `schema_migrations` and applied in order at startup; each runs in a transaction. Application code never alters tables outside migrations.

Conventions:

- Integer primary keys, no UUIDs.
- Instant fields are ISO-8601 UTC `TEXT`; due dates are `YYYY-MM-DD`.
- Booleans are `INTEGER` 0 or 1.
- Soft deletion via `archived_at`; views filter `archived_at IS NULL`; nothing is purged.
- Writes touching several tables run in one transaction.

Tables, by area:

| Area | Tables |
|---|---|
| Identity | `users`, `sessions`, `api_tokens`, `invites` |
| Access | `teams`, `team_members`, `project_members`, `project_teams` |
| Work | `projects`, `items`, `item_assignees`, `comments`, `comment_mentions`, `item_events` |
| Workflow | `workflows`, `project_workflows`, `workflow_states`, `workflow_transitions`, `transition_conditions` |
| Automation | `automation_rules`, `automation_rule_projects`, `automation_actions`, `automation_rule_runs` |
| Notifications | `notifications` |
| Search | `items_fts` (FTS5) |
| Second factor | `user_totp`, `user_recovery_codes`, `login_challenges` |
| System | `schema_migrations` |

Important invariants:

- `users.locale` accepts `en`, `it` and `es`; email is unique, case-insensitive.
- Passwords are bcrypt hashes. `sessions.token_hash`, `api_tokens.token_hash`, `invites.token_hash` and `login_challenges.token_hash` hold SHA-256 digests, never the bearer value. `api_tokens.token_prefix` keeps the first characters for display.
- `user_totp.secret_enc` and the SMTP password in `config.ini` are AES-256-GCM ciphertext under `data_dir/secret.key`; recovery codes are hashed.
- Each account keeps at least one active API token; revocation is refused when only one remains.
- `settings` has one row (`id = 1`) with `setup_completed` and `require_2fa`; each workflow stores its own initial state.
- Notification deduplication uses partial unique indexes (one unread assignment per user/item, one mention per user/comment, one due summary per user/day).
- The FTS index is kept current by triggers on items and comments; the migration backfills existing rows.
- Auth acceptance, item creation, Action archive cascade, transitions and comment edits are the writes that span multiple tables in one transaction.

Migrations:

| Version | What it adds |
|---|---|
| `0001_init` | Base schema: users, sessions, tokens, teams, projects, items, comments, history, workflow, automations, notifications, invites. |
| `0002_user_profile` | `users.updated_at`. |
| `0003_notifications_unique` | Deduplication indexes for notifications. |
| `0004_automation_runs` | Automation metadata and per-rule run markers. |
| `0005_search_fts` | FTS5 table, triggers and backfill. |
| `0006_totp` | 2FA tables and `settings.require_2fa`. |
| `0007_locale_es` | Rebuilds `users` so the locale check accepts `es`, preserving rows and references. |
| `0008_hashed_secrets` | Renames the bearer columns to `*_token_hash` (`api_tokens`, `invites`), adds `api_tokens.token_prefix`, and renames the TOTP column to `secret_enc`. Existing values are converted by the startup backfill. |
| `0009_project_workflows` | Converts the installation workflow into project-owned flows, binds a flow to each task type, and moves initial-state configuration into workflow states. |

The migration runner disables foreign-key enforcement only while pending migrations are being applied, because SQLite cannot alter a `CHECK` constraint or rebuild a referenced table otherwise. It re-enables enforcement and runs `PRAGMA foreign_key_check` before returning; a failed check aborts startup.

### Backup and restore

Admins download a consistent snapshot from the admin area. The snapshot is produced with SQLite `VACUUM INTO`, so uncheckpointed WAL content is included and the result is one self-contained `.db` file with no `-wal` or `-shm` sidecar. The temporary copy is removed after the response. Downloads are named `together-backup-YYYYMMDD-HHMMSS.db`.

For a manual copy, stop the service first, then copy `app.db` (and any remaining `app.db-wal`/`app.db-shm` files) from the configured data directory.

Also keep a copy of `data_dir/secret.key`. It is not inside the database snapshot, and it is the key that opens the sealed TOTP secrets and the stored SMTP password: a backup without it restores the data but loses every second factor and the mail password.

To restore, stop the service, move the current database files aside, place the snapshot as `app.db` with no sidecars, restore the matching `secret.key`, and start the service. Migrations run automatically if the snapshot is older than the binary. Rebuild the search index from the admin area if the restored file was copied by hand while the service was running.

### Upgrade

1. Download a backup from the admin area.
2. Stop the service.
3. Replace the binary.
4. Start the service and wait for readiness.

Migrations apply in version order at startup. A 1.0.0 database upgrades in place through every later migration; a 2.0.0 database applies `0007_locale_es`, `0008_hashed_secrets` and `0009_project_workflows`, keeping every account, reference and pending invitation. The first start after the upgrade rewrites the clear-text tokens, invitation links and TOTP secret into digests or ciphertext and logs how many values it converted; personal tokens and invitation links keep working, because they are matched through their digest from then on. A database created by a newer binary is refused; downgrades require restoring the matching backup.

Back up `data_dir/secret.key` together with the database. A restored snapshot without that key cannot decrypt the TOTP secrets or the SMTP password it contains, so affected accounts need their second factor reset.

## Reverse proxy

Terminate HTTPS at the proxy and forward to the binary, or let the binary serve TLS itself with `tls_auto`, `tls_cert` and `tls_key`. With a proxy, keep the original `Host` header so invitation links and redirects use the public origin, and set `secure_cookies=true`.

Caddy:

```
together.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

nginx:

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name together.example.com;

    ssl_certificate     /etc/letsencrypt/live/together.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/together.example.com/privkey.pem;

    # Server-Sent Events: never buffer or time out the notification stream.
    location /api/notifications/stream {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header Connection "";
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 3600s;
    }

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

Caddy needs no extra directive for event streams. If a proxy cannot carry streams, the client polling fallback keeps the badge working.

## Operations

- `GET /api/health` is a liveness probe; `GET /api/ready` is a database-backed readiness probe that returns `503` until the database is usable.
- `SIGINT` and `SIGTERM` stop accepting requests, close notification streams, drain requests for up to 15 seconds, stop the daily scheduler and close the database.
- Request logs contain method, redacted path, status, duration and remote IP. Bodies, cookies, authorization headers and invite tokens are never logged, so passwords and personal API tokens cannot leak into a log file.
- Login attempts are rate-limited per remote IP in process memory.
- Session cookies are HttpOnly/SameSite=Lax and are marked Secure when TLS is served directly or `secure_cookies=true`. Baseline security headers are set, including a strict Content-Security-Policy that allows no external requests, and `Strict-Transport-Security` whenever the binary serves TLS.
- Nothing is stored in clear text: passwords are bcrypt, bearer secrets are SHA-256 digests, TOTP secrets and the SMTP password are AES-256-GCM ciphertext under `data_dir/secret.key`, and API responses carry `Cache-Control: no-store`.
- SQLite runs in WAL mode with foreign keys and a busy timeout enabled.
- The admin area exposes SMTP settings, backup, search-index rebuild and the 2FA policy.

## Project layout

```
cmd/server/            Binary entry point, config, wiring, graceful shutdown
internal/
  auth/                Accounts, sessions, tokens, invitations, passwords
  automations/         Rules, triggers, actions, daily due check
  config/              config.ini + environment, atomic write-back
  db/                  Open, migrations, seeding, backup
  email/               Plain-text SMTP with optional STARTTLS
  httpapi/             Routes, auth/CSRF gates, JSON, SSE stream, SPA fallback
  items/               Items, filters, search, comments, history, transitions
  notifications/       In-app notifications and deduplication
  projects/            Visibility, membership, archive rules
  realtime/            In-process notification fan-out
  secrets/             Installation key, AES-256-GCM sealing
  teams/               Groups and membership
  tlsconf/             Self-signed certificate generation
  totp/                RFC 6238 verifier and recovery codes
  web/                 Embedded frontend (generated dist)
  workflow/            Project-owned flows, presets, states, transitions and conditions
web/                   React + TypeScript source, styles and public assets
  src/components/      Shell, UI primitives, item widgets, 2FA
  src/screens/         One file per screen
scripts/               build.ps1, smoke.ps1, make-logo.ps1
dist/                  Release binaries (build output, not source)
```

`AGENTS.md` describes the contributor workflow, conventions, recipes and pitfalls in detail.

## Development and testing

```powershell
npm install
npm run typecheck
npm run build
go vet ./...
go test ./... -count=1
powershell -File scripts/build.ps1
powershell -File scripts/smoke.ps1
```

The automated suite covers:

- `cmd/server`: HTTP end-to-end flows for v1 and v2 (setup, invite, grants, items, mentions, notifications, transitions, reports, backup, SPA deep links, bearer tokens, logout), search hit/rebuild, TOTP enrollment and two-step login, recovery login, SSE mention frame, admin reset, backup with FTS and 2FA tables, HTTPS with a self-signed certificate, a byte-level scan proving no clear secret reaches the data directory, and the drain/stop shutdown path.
- `internal/db`: migrations on an empty database, upgrades from populated versions 1, 4 and 5, refusal of a newer schema, locale widening to `es`, hashing migration, backup round-trip, and a backup that preserves the FTS index and 2FA rows.
- `internal/secrets` and `internal/tlsconf`: key creation and permissions, seal/open round trip with a wrong key, refusal without a key, generated certificate reuse and replacement.
- `internal/httpapi`: role/project access matrix (guests, expired guests, archived projects), notification stream, search rebuild, profile 2FA, admin reset, security policy, report visibility, token lifecycle, disabled accounts, CSRF/Origin checks, secret redaction, SMTP settings, notifications, search, automations and the full 2FA flow.
- `internal/totp`: RFC 6238 vectors, ±1 window, replay guard, secret normalization and malformed input.
- `internal/items`, `internal/projects`, `internal/workflow`, `internal/notifications`, `internal/automations`: store-level rules, hierarchy, conditions and deduplication; the search parser, FTS query plan and `LIKE` fallback.

## Release

The project uses semantic versioning and keeps a Keep a Changelog style history in `CHANGELOG.md`. Published releases use the changelog entry as release notes.

1. Run the full verification block above.
2. Update `CHANGELOG.md` with a new dated section.
3. Run `powershell -File scripts/build.ps1`.
4. Run `powershell -File scripts/smoke.ps1` against the built binary.
5. Publish `dist/together-*` and the changelog entry.

Current line: **4.2.0** (2026-11-22) — optional SMTP setup with encrypted password storage and platform-specific user data directory defaults, while retaining operator control over listener, TLS and cookie settings. 4.1.0 added project-owned workflows, local copies from a standard kit catalog, per-task-type bindings with transactional state mapping, workflow-aware project and cross-project views, and responsive graph containment. 3.0.0 added hashed bearer secrets, sealed TOTP and SMTP values, optional TLS and `0008_hashed_secrets`; 2.1.0 renamed Work to Together and added the Spanish interface, selectable language and light/dark theme.

## Out of scope

Marketplace, plugins, a desktop app, a wiki, collaborative documents, attachments, kanban boards, custom fields, labels, time tracking and dependencies are deliberately out of scope. The installation stays a single binary and a single database.
