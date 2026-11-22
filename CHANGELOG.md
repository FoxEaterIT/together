# Changelog

All notable changes to this project. The format follows Keep a Changelog and the project uses semantic versioning.

## [4.1.0] - 2026-11-22

Project workflows are now configurable independently, with reusable task-type bindings, a standard kit catalog and transactional state mapping when a workflow changes. This release also improves workflow-aware item creation, search, reports, automation and project views.

### Added

- Project workflow catalogs with multiple local flows, duplication, rename, and standard kits for Project Management, Development, Design, Customer Service and Sales teams, including lightweight variants.
- Per-task-type workflow assignment. Reassigning a type with existing items requires complete state mapping; affected items, history events and automation effects are handled transactionally.
- Workflow editor in project settings, with state and transition management, conditions, an initial state per flow, and a visual transition graph.
- Project-wide workflow state listing for filters, search and automation configuration across separate project workflows.

### Changed

- New projects receive a local Default flow, initially bound to Observer, Action, Micro and Bug.
- Item creation, state transitions, search, reports, project views and automation triggers resolve states in the relevant project's workflow.
- Automation state triggers also resolve by state name so project-local copies do not invalidate existing rules.
- The workflow API is project-scoped. Existing single-workflow installations are converted automatically by migration `0009_project_workflows`.

### Breaking

- Workflow API operations now use project-nested `/api/projects/{id}/workflows/...` routes. `GET /api/workflow/presets` and `GET /api/workflow/states` provide the kit catalog and cross-project state listing.
- `settings.initial_state_id` has been removed; initial states are configured per workflow.

### Added

- Project workflow catalogs with multiple local flows, duplication, rename, and a standard kit catalog for Project Management, Development, Design, Customer Service and Sales teams.
- Per-task-type flow assignment. Reassigning a type with existing items requires complete state mapping; item state changes and history events are written transactionally.
- Workflow editor in project settings; project-wide state listing supports filters, search and automation configuration across separate project workflows.

### Changed

- Creating a project seeds a local Default flow and binds it to Observer, Action, Micro and Bug.
- Automations resolve state triggers by name as well as identifier so project-local copies do not invalidate existing rules.

### Breaking

- Workflow API operations now use project-nested `/api/projects/{id}/workflows/...` routes. `GET /api/workflow/presets` and `GET /api/workflow/states` provide the kit catalog and cross-project state listing.
- `settings.initial_state_id` has been removed; initial states are configured per workflow.

## [3.0.0] - 2026-11-15

Security release: secrets are no longer stored or served in clear text, and the binary can terminate HTTPS itself. The one breaking change is that a personal API token is no longer readable after creation; existing tokens keep working.

### Added

- Built-in TLS: `tls_auto` generates a self-signed certificate in the data directory, `tls_cert` and `tls_key` use a real pair, `tls_redirect` adds an optional plain-HTTP listener that permanently redirects to HTTPS. HTTPS is served with HTTP/2 and TLS 1.2 as the floor.
- `Strict-Transport-Security` on responses served over TLS; the session cookie is `Secure` whenever TLS is on, regardless of `secure_cookies`.
- Installation key at `data/secret.key` (32 bytes, owner-only) and AES-256-GCM sealing for every value the server must read back.
- `internal/secrets` (key handling, `Seal`/`Open`, refusal without a key) and `internal/tlsconf` (self-signed generation, reuse and rotation 30 days before expiry).
- Startup backfill that converts clear-text tokens, invitation links and TOTP secrets written by earlier versions; it is idempotent, logs how many rows it rewrote, and runs before the listener opens.
- `Cache-Control: no-store` on every API response, because a create response can carry a token or an invitation link.
- Tests: HTTPS with a generated certificate, a byte-level scan of `app.db*` and `config.ini` proving no password, token, TOTP secret, recovery code or SMTP password reaches disk, legacy conversion, and certificate/key handling.

### Changed

- Session tokens, personal API tokens and invitation links are stored only as SHA-256 digests (`*_token_hash`). A new token is shown once, in the response that creates it; the profile lists name, prefix, date and revoke.
- The SMTP password is written to `config.ini` as `smtp_pass_enc`, sealed with the installation key. An existing clear-text `smtp_pass` is sealed on the next start and the key is removed; `SMTP_PASS` from the environment stays in memory only.
- TOTP secrets are sealed in `user_totp.secret_enc`; enrollment is refused rather than stored in clear text when the key is unavailable.
- `config.Save` treats an empty value as "remove this key", which is how the clear-text SMTP password is deleted from the file.
- Migration `0008_hashed_secrets` renames `api_tokens.token`, `invites.token` and `user_totp.secret`, and adds `api_tokens.token_prefix`.

### Breaking

- `GET /api/tokens` no longer returns `token`; it returns `prefix` instead. Scripts that read token values from the API must capture the value from the `POST /api/tokens` response at creation time. Existing tokens are unaffected.
- Upgrading rewrites the token and invitation rows on first start. Back up `data/secret.key` with the database: without it, sealed TOTP secrets and the stored SMTP password cannot be recovered and the affected accounts need a second-factor reset.

## [2.1.0] - 2026-11-14

Branding and personalization release. The product is now called Together, the interface speaks Spanish in addition to English and Italian, and each person can choose language and light or dark theme at any time.

### Added

- Spanish interface (`es`) alongside English and Italian, with the same key set; dates and relative times use `es-ES`.
- Language picker in the top bar, on the setup, sign-in and invitation screens, and in the profile. The choice is stored on the account when signed in and in the browser otherwise.
- First-visit language detection from `navigator.languages`, falling back to English for unsupported languages. New accounts created by setup or invitation store the language chosen on that screen.
- Light and dark themes driven by CSS tokens, starting from `prefers-color-scheme`, remembered in `localStorage`, applied before the first paint, and switchable from the top bar and the profile.
- Profile and boot screens can change language and theme too, including the offline screen.
- Config rejects unknown `locale` values and accepts `es`; the profile API accepts `es` as well.
- Migration 0007 rebuilds the users table so the stored `locale` check accepts `es`, keeping every account and reference intact.

### Changed

- Renamed the product from Work to Together across the UI, page title, invitation and test emails, TOTP issuer (`otpauth://totp/Together:...`), backup filenames, API token prefix (`tgr_`) and release binaries (`dist/together-*`).
- `README.md` documents the language, theme and renamed artifact paths; `scripts/build.ps1` and `scripts/smoke.ps1` use the new binary names.
- Documentation consolidated: `README.md` is the single product and operator manual, `AGENTS.md` covers contributor and agent workflows, and the former research notes were removed.

## [2.0.0] - 2026-11-13

Second major release. Real-time notifications, full-text search and two-factor authentication arrive without new services: one Go binary, embedded React and SQLite, upgrading in place from 1.0.0.

### Added

- Real-time notification stream at `GET /api/notifications/stream` using Server-Sent Events: initial `ready` count, `unread` and `notification` frames, `resync` for slow consumers, a 25-second heartbeat and automatic browser reconnection with polling fallback.
- In-memory fan-out with at most 5 streams per user and 500 per process; the hub is closed before the HTTP drain so shutdown stays bounded.
- Full-text search through SQLite FTS5 over item titles, descriptions and comments, with diacritic folding, `bm25` ranking, plain-text `«…»` snippets and a safe query parser for phrases, prefixes and `-exclusions`.
- New item filters `state_category`, `created_from/to`, `updated_from/to`, `has_assignee`, `parent_id`, `sort` and `total`, with visibility still enforced in SQL.
- Administrator-only search index rebuild at `POST /api/admin/search/rebuild`, plus a documented `LIKE` fallback when FTS5 is unavailable.
- Optional TOTP two-factor authentication for Admin, Manager and Member accounts: RFC 6238 SHA-1, six digits, 30-second steps, ±1 window, replay guard and QR enrollment generated in the browser.
- Ten single-use recovery codes per account, stored as SHA-256 hashes and shown once, with a password-plus-code rotation flow.
- Two-step sign-in with a 5-minute, 5-attempt, single-use challenge; no session cookie exists until the second factor succeeds.
- Administrator policy `require_2fa` that forces enrollment for non-guest accounts at the next sign-in and at invite acceptance, while existing sessions stay valid until expiry.
- Administrator reset of another account's second factor at `POST /api/users/{id}/2fa/reset`; the reset is refused on your own account and revokes the target's sessions.
- Guest accounts are always exempt from the challenge, and demotion keeps an existing enrollment without enforcing it.
- Search screen filters, sorting, totals, highlighted snippets, comment-match labels and recent searches; profile Security card, forced enrollment screen, Admin policy card and People reset action in the React UI.
- `scripts/smoke.ps1` starts a release binary on a temporary database and checks readiness, health, setup and the embedded SPA.
- Custom brand artwork: `logo.ico` as the browser favicon and `assets/logo-256.png` in the sidebar and sign-in panel; `scripts/make-logo.ps1` regenerates the served PNG from the master file in `web/assets/logo.png`.

### Security

- Recovery codes are single use and hashed; TOTP and recovery secrets never appear in `/api/me`, user lists, logs or normal responses.
- Login failures are counted per remote IP and per challenge; a tampered, expired, consumed or scope-mismatched challenge fails generically.
- Personal API tokens stay single-factor, cannot open the notification stream and are documented as long-lived automation secrets.
- The notification stream authenticates with the session cookie only and checks notification visibility before pushing anything.
- Search snippets are plain text; raw FTS operators, parentheses and overlong queries are rejected instead of reaching the index.

### Changed

- Schema version 6 adds `user_totp`, `user_recovery_codes`, `login_challenges` and `settings.require_2fa`; migrations from 1.0.0 apply automatically.
- Documentation covers the stream, reverse-proxy settings, search syntax, 2FA enrollment/recovery/reset and the accepted residual risks.

### Known limits in v2

- The TOTP secret is stored in clear text inside SQLite, like personal API tokens: the database file is the trust boundary.
- Login rate limiting is in process memory and is not shared across instances.
- SMTP uses STARTTLS when enabled and does not support implicit TLS on port 465.
- Backup remains manual, with no scheduler or retention policy.
- No attachments, marketplace, plugins, desktop app, wiki or collaborative documents.

## [1.0.0] - 2026-11-12

First self-hosted release. One Go binary serves the API and the embedded React interface; SQLite is created and migrated on first start.

### Added

- First-run setup that creates the single administrator, a session and the default personal API token.
- Invite-only accounts with one-use links, 7/30/90-day guest windows, optional SMTP delivery and admin user management.
- Roles Admin, Manager, Member and Guest, enforced per request on the server.
- Teams as reusable groups, plus direct project membership; open and private projects with archive.
- Work items Observer, Action, Micro and Bug with hierarchy validation, description, priority, multiple assignees, due date, comments with mentions, append-only history and archive cascade.
- One global workflow with admin-only states, transitions, a configurable initial state and role/assignee/priority/type conditions.
- In-app notifications for assignment, mention and the daily due summary, with deduplication; optional email for the same three events.
- Automation rules on `state_changed`, `assigned`, `comment_added` and `due_passed`, with ordered `set_state`, `assign_user`, `add_comment` and `notify` actions in one transaction.
- Server-aggregated reports by state and open workload per person, with visibility applied in SQL.
- Admin SQLite backup download using `VACUUM INTO`, producing one consistent file with WAL content and no sidecars.
- Personal API tokens usable with `Authorization: Bearer`, never expiring, with the last active token protected from revocation.
- English and Italian interface, with pre-authentication screens following the browser language.
- Health (`/api/health`) and readiness (`/api/ready`) probes, redacted request logs and graceful shutdown on `SIGINT`/`SIGTERM`.
- Reproducible cross-builds for Windows amd64/arm64 and Linux amd64/arm64 through `scripts/build.ps1`.

### Security

- Session cookies are HttpOnly, SameSite=Lax and optionally Secure behind HTTPS.
- Cookie mutations check `Origin` and Fetch Metadata; login attempts are rate-limited per remote IP.
- Request bodies are capped at 1 MiB, JSON decoding is strict and security headers include a self-only CSP.
- Private project access answers `404` instead of confirming existence; visibility filters run in SQL for lists, search, calendar, reports and notifications.
- Invite tokens and personal tokens are never written to the request log, which records only method, redacted path, status, duration and remote IP.
- The SMTP password is never returned by the API; an empty update keeps the stored password.

### Fixed during release validation

- `GET /api/me` now accepts personal API tokens as well as the session cookie, so token-based clients can identify themselves like any other authenticated endpoint.
- Opening a database written by a newer binary is now refused with a clear error instead of being silently accepted.

### Known limits in v1

- Backup is manual; there is no scheduler or retention policy.
- SMTP is optional: without it, invitations still work through the copyable link and notifications stay in-app.
- No attachments, marketplace, plugins, desktop app, wiki or collaborative documents.
- Declared binary targets are Windows and Linux on amd64 and arm64; other Go targets are not part of the release contract.
