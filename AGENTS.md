# AGENTS.md

Working guide for AI agents and contributors. It covers how the project is structured, the rules that must not be broken, and the recipes for common changes.

`README.md` is the product, administrator and operator manual. `CHANGELOG.md` holds release notes. Those three files are the only documentation in the repository: do not add other markdown files, and keep transient notes out of the tree.

## At a glance

| | |
|---|---|
| Product name | **Together** (self-hosted work management) |
| Go module | `jiralike` (legacy internal name; do not rename casually) |
| Backend | Go 1.26, standard library + `modernc.org/sqlite` + `golang.org/x/crypto` |
| Frontend | React + TypeScript, Vite, lucide-react, fontsource; no runtime external requests |
| Database | SQLite, WAL, embedded migrations, no ORM |
| Binary | `cmd/server`, one static binary with the SPA embedded |
| Current version | 3.0.0 (2026-11-15) |
| Languages | English (source), Italian, Spanish |
| Themes | light and dark, CSS tokens |

## Ground rules

1. **One binary, no new services.** Do not introduce a database server, cache, queue, worker process or cloud API. SMTP is the only optional integration and the app must start without it.
2. **The server is the authority.** Hiding a control in the UI is a courtesy; enforce every rule in `internal/httpapi` or the domain package it calls.
3. **Never hard-delete user data.** Projects, teams and items are archived with `archived_at`; history is append-only. "Delete" endpoints archive.
4. **Schema changes only through a new migration.** Never edit an applied migration. Add the next numbered file in `internal/db/migrations`, test the upgrade path, and update the README migration table.
5. **SQLite cannot alter a `CHECK` constraint or rebuild a referenced table in place.** The migration runner disables foreign keys while pending migrations run and checks them afterwards; keep that contract.
6. **All user-visible text goes through `t()`.** English is the source: the key itself is the English string. Italian and Spanish dictionaries must keep exactly the same key set.
7. **Never log secrets.** No passwords, session cookies, bearer tokens, invite tokens, TOTP secrets or recovery codes. Use the existing redaction in the request logger.
8. **Keep the API contract and `web/src/types.ts` in sync.** The API is the product contract; the UI consumes it.
9. **Test before finishing.** `gofmt`, `go vet`, `go test ./...`, `npm run typecheck` and `npm run build` must pass. Add or extend tests for behavior changes.
10. **Update documentation only when behavior, configuration or conventions change.** Release notes go to `CHANGELOG.md` when preparing a release, not for every edit.

## Commands

```powershell
# Dependencies and development
npm install
go run ./cmd/server            # http://127.0.0.1:8080, data/ created on first run
npm run dev                    # Vite on :5173, proxies /api to :8080

# Verification
npm run typecheck
npm run build                  # tsc -b + vite build into internal/web/dist
go vet ./...
go test ./... -count=1

# Release
powershell -File scripts/build.ps1
powershell -File scripts/build.ps1 -Targets linux/amd64
powershell -File scripts/smoke.ps1
powershell -File scripts/smoke.ps1 -Binary dist/together-windows-amd64.exe

# Branding
powershell -File scripts/make-logo.ps1
```

`go run` uses `config.ini` in the working directory; environment variables override it (`ADDR`, `DATA_DIR`, `DB_PATH`, `LOCALE`, `SECURE_COOKIES`, `SMTP_*`). For an isolated experiment, point `DATA_DIR` at a temporary directory.

## Repository map

```
cmd/server/            main.go: config -> db.Open -> stores -> httpapi -> serve
internal/auth/         users, sessions, API tokens, invites, password hashing
internal/automations/  rule storage/execution, due_passed evaluation
internal/config/       config.ini + env, validation, atomic write-back
internal/db/           Open, migrate, seed workflow, VACUUM INTO backup
internal/db/migrations/NNNN_name.sql (never edit an applied file)
internal/email/        SMTP with optional STARTTLS
internal/httpapi/      routes, middleware, handlers, JSON, SSE, SPA fallback
internal/items/        items, filters, search, comments, history, transitions
internal/notifications/inbox, dedup, due summary
internal/projects/     visibility, grants, archive rules
internal/realtime/     in-process SSE fan-out hub
internal/secrets/      data/secret.key, AES-256-GCM sealing for recoverable values
internal/teams/        groups and membership
internal/tlsconf/      self-signed certificate generation for tls_auto
internal/totp/         RFC 6238 + recovery code helpers
internal/web/          embeds dist/ (generated; never edit by hand)
internal/workflow/     states, transitions, conditions, initial state
web/src/               React source
web/src/components/    shell, UI primitives, item widgets, 2FA screens
web/src/screens/       one file per screen
web/public/            served assets (logo-256.png, logo.ico)
web/assets/            master artwork, not served
scripts/               build.ps1, smoke.ps1, make-logo.ps1
dist/                  release binaries (build output)
```

Key files:

- `internal/httpapi/httpapi.go` — route table, auth/role/CSRF middleware, request helpers, SPA fallback.
- `internal/httpapi/notifications.go` — inbox, SSE handler, due digest scheduler.
- `internal/db/db.go` — `Open`, `migrate`, `seedWorkflow`, foreign-key handling.
- `internal/auth/auth.go` — `Setup`, `Login`, sessions, hashed tokens, invites, `SealLegacy` backfill.
- `internal/secrets/secrets.go` — installation key and AES-256-GCM `Seal`/`Open`.
- `internal/tlsconf/tlsconf.go` — self-signed certificate generation and rotation.
- `internal/projects/projects.go` — `CanView`, `CanSee`, `CanManage`, `CanWrite`, membership.
- `web/src/app.tsx` — providers, boot gate, route table.
- `web/src/state.tsx` — current user, project cache, unread count, SSE lifecycle.
- `web/src/i18n.tsx` — dictionaries and date/relative formatting.
- `web/src/preferences.tsx` — theme/locale detection, persistence and providers.
- `web/src/tokens.css` — light and dark design tokens.

Generated or ignored: `node_modules/`, `internal/web/dist/`, `dist/`, `data/`, `*.db*`, `tsconfig.tsbuildinfo`. Regenerate them; never hand-edit.

## Architecture

### Startup and shutdown

`cmd/server/main.go` loads config, opens SQLite (`db.Open` applies migrations and seeds the default workflow when empty), loads the installation key (`secrets.LoadOrCreate`), resolves the TLS certificate pair, constructs the domain stores and `httpapi.NewWithOptions`, backfills legacy clear-text secrets (`SealLegacy`), starts the due-digest scheduler and listens. With `tls_auto` or `tls_cert`/`tls_key` it serves HTTPS through `ServeTLS`; the optional `tls_redirect` listener answers plain HTTP with a permanent redirect. On `SIGINT`/`SIGTERM` it closes notification streams first, then drains HTTP for up to 15 seconds, stops the scheduler and closes the database. The same `serve` path is covered by tests.

### Request lifecycle

1. `httpapi.Handler()` wraps the mux with security headers, body limit, request logging and panic recovery.
2. Public routes: health, readiness, setup status/create, login, 2FA login steps, invite preview/accept.
3. Authenticated routes use `s.requireUser`; role routes use `s.requireRole([...])`.
4. Every mutating cookie route is wrapped in `s.requireCSRF`, which checks the `Origin`/Fetch Metadata against the request origin.
5. Handlers decode strict JSON, validate, call a domain store, and write `writeJSON`/`writeError`.
6. Visibility is always resolved through `projects.Store` (`CanSee`, `CanWrite`, `CanManage`, `AssignableUsers`); never re-implement it in a handler.

API tokens (`Authorization: Bearer tgr_...`) authenticate the same routes as cookies, but cannot open the SSE stream.

### Notification stream

`GET /api/notifications/stream` is served by `internal/realtime` plus the handler in `notifications.go`. One connection per tab, heartbeat every 25 s, at most 5 per user and 500 per process; slow consumers receive `resync` and are dropped. Frames carry counts/ids only; the client reloads through normal JSON endpoints, so visibility is applied. The browser falls back to polling after three consecutive failures. `StartDueDigest` ticks hourly and writes one due summary per user per UTC day.

## Data invariants

- **Identity**: email unique and `COLLATE NOCASE`; `users.locale` is `en`, `it` or `es`.
- **Secrets**: passwords are bcrypt hashes; session tokens, API tokens (bearer) and invitation links are stored only as SHA-256 digests (`*_token_hash`), with `api_tokens.token_prefix` kept purely for display. A bearer value appears in clear text exactly once, in the response that creates it; it is never readable afterwards. Values that must be decrypted (TOTP secret, SMTP password) are sealed with `secrets.Box` (AES-256-GCM under `data/secret.key`); `Seal` refuses with `ErrNoKey` instead of writing plain text.
- **At least one token**: creating and revoking tokens enforces one active token per account; the last one cannot be revoked.
- **Soft deletion**: `archived_at` on projects, teams, items. Archived projects are invisible except to admins/managers who manage them; archived items are hidden from views but kept.
- **Item hierarchy**: Observer has no parent; Action/Bug may parent to an Observer in the same project; Micro must parent to an Action in the same project; archiving an Action archives its Micros in the same transaction.
- **History**: `item_events` is append-only; assignee changes store ordered ids; history is never edited or deleted.
- **Workflow**: one initial state in `settings`; at least one open and one done state; transitions unique from/to; conditions are OR within a kind and AND across kinds; guests never transition.
- **Automations**: a rule with no project rows is global; actions run in `position` order in the triggering transaction and never re-enter the engine; `due_passed` uses an idempotent run marker.
- **Notifications**: deduplicated by partial unique indexes; `emailed_at` is set only after SMTP accepts the message; badge counts ignore unreadable items.
- **Search**: `items_fts` is kept current by triggers; migrations backfill; queries are parsed, never passed through as FTS syntax; visibility is joined in SQL.
- **2FA**: `settings.require_2fa` forces enrollment for non-guests; challenges are single-use, 5 minutes, 5 attempts; recovery codes are hashed and single-use; reset deletes the secret, codes, challenges and revokes sessions.

## Backend conventions

- Domain packages expose a `Store` backed by `*sql.DB`; write multi-table changes in an explicit transaction.
- Use wrapped sentinel errors (`ErrNotFound`, `ErrEmailTaken`, …) and `errors.Is` at the HTTP boundary. Handlers translate them to the documented statuses; unexpected errors are logged and answered with a generic message.
- JSON field names are `snake_case`. Absent values are `""` or `null` consistently with the existing structs; the UI tolerates both.
- Time: instants are `time.RFC3339` UTC; due dates are `YYYY-MM-DD` strings with no zone. Use the package helpers, not ad-hoc formats.
- IDs are `int64`.
- Keep handlers thin: validation and status codes there, rules in the store.
- New routes go in `internal/httpapi/httpapi.go` with the narrowest middleware (`requireUser`, `requireRole`, `requireCSRF`).
- Log only non-sensitive metadata. If a new path can contain a secret, extend `redactPath`.
- **Secrets go through `secrets.Box`.** Anything the server must read back (a shared key, an SMTP credential) is sealed, and a missing key is an error, never a reason to write plain text. Anything that is only verified (tokens, invite links, session ids, recovery codes) is stored as `hashSecret(raw)`. Return the raw value in the creating response only, and list a short prefix afterwards.
- **Serve JSON through `writeJSON`/`writeError`.** They set `Cache-Control: no-store`, which API responses require; a bare `json.NewEncoder(w)` skips it.
- Tests use `httptest` with `testServer(t)`, temp directories and real cookies where possible. HTTP end-to-end flows live in `cmd/server/e2e_test.go`; store rules live beside their package.

## Frontend conventions

- Strict TypeScript (`noUnusedLocals`, `noUnusedParameters`); no `any` unless unavoidable.
- Screens are function components in `web/src/screens`, shared widgets in `web/src/components`, API calls through `api<T>()`.
- Every request needs loading, empty and error states. Use `errorMessage()` for display and the toast provider for confirmations.
- Routing is the small history router in `web/src/router.tsx`. Add a route in `app.tsx`; deep links work because the Go server falls back to `index.html` for non-API paths.
- Shared state is deliberately small: current user, project cache, unread count. Do not add a global store for screen-local data.
- **i18n**: call `t("English text")`; placeholders look like `{count}` and are passed as the second argument. Add the same key to the Italian and Spanish dictionaries with identical spelling; duplicate English keys are bugs. Format dates with `formatDate`, `formatDateTime` and `formatRelative`, never with raw `toLocaleString`.
- **Theme**: use CSS variables from `tokens.css`; if a rule uses a literal colour, add a `[data-theme="dark"]` override in `styles.css`. Verify both themes.
- Prefer classes and design tokens. React `style` props are used for one-off layout and work under the CSP because React writes through CSSOM; raw inline `style="..."` attributes in static HTML are blocked by `default-src 'self'` and must not be added. External fonts, scripts, images and CDNs are blocked by design.
- Icons come from `lucide-react`; keep `aria-label`s and visible focus states. The shell, dialogs and menus already implement keyboard/escape behaviour; reuse the components in `components/ui.tsx`.
- Never use `dangerouslySetInnerHTML` for user content; search snippets are plain text with `«»` markers.

## Recipes

### Add or change an API endpoint

1. Add the route in `internal/httpapi/httpapi.go` with `requireUser`/`requireRole` and `requireCSRF` for mutations.
2. Implement the handler in the relevant `internal/httpapi/<area>.go`; keep validation there.
3. Add or extend a store method in the owning package; wrap multi-table writes in a transaction.
4. Extend `web/src/types.ts` and the calling screen, then run the frontend checks.
5. Add an `httptest` case (role allowed, role denied, invalid input) and, for a user-visible flow, an e2e step.
6. If the endpoint changes the public contract, update the README endpoint table and the changelog at release time.

### Add a migration

1. Create `internal/db/migrations/NNNN_short_name.sql` with the next number. Migrations run once, in order, inside a transaction; they must be idempotent only in the sense that `schema_migrations` prevents a rerun.
2. Never modify an existing migration. A table rebuild must copy all columns and constraints; the runner disables foreign keys for pending migrations and verifies them afterwards.
3. Add an upgrade test in `internal/db/migrate_test.go` starting from the previous schema with representative rows.
4. Update the migration table in `README.md`; mention the change in `CHANGELOG.md` when releasing.

Column renames that change meaning (`token` → `token_hash`, `secret` → `secret_enc`, as in `0008_hashed_secrets`) must come with a Go backfill step that converts existing rows on start and is safe to run repeatedly; the migration alone cannot hash or encrypt data. `0008` is the reference: `auth.SealLegacy` rewrites rows until none match, and `cmd/server` calls it before `net.Listen`, so no request is served while plain text remains.

### Add a language

1. Extend `Locale` in `web/src/types.ts`, `LOCALES`/`LOCALE_NAMES` and `dateLocales` in `web/src/preferences.tsx`/`i18n.tsx`.
2. Add the full dictionary in `i18n.tsx` with the same key set as English, then wire the picker (it iterates `LOCALES`).
3. Add the locale to `internal/config` validation, `validLocale`/`normalizeLocale`, the profile handler, and the setup/invite handlers in `internal/httpapi`.
4. Add the language to email bodies (`notifications.go`, `smtp.go`), the SQLite `locale` `CHECK` (new migration rebuilding `users`), and tests.
5. Update the README language section and the changelog.

### Add a UI string or screen

1. Use `t("...")` for text and add the Italian/Spanish keys.
2. Add the screen under `web/src/screens`, register the route in `app.tsx`, and add it to the shell navigation only if it is a primary destination.
3. Check light/dark, keyboard navigation and the mobile breakpoints (1100/900/760/560 px).

### Change notification or email behavior

1. Keep the dedup indexes in sync with the new kind, or add a migration.
2. Respect the recipient's `email_notifications` and the configured SMTP sender; never send email during an unauthenticated request unless it is an invitation.
3. Add the email translation for all supported locales.
4. Test with `testServerWithMail` / the existing mail recorder.

## Pitfalls

- **Locale constraint**: `users.locale` is enforced by SQLite (`0007_locale_es`). A new language needs a new table-rebuild migration, not just a TypeScript change.
- **CSRF**: mutating routes without `requireCSRF` are a security bug. Tests must send the expected origin or assert the rejection.
- **SSE and tokens**: `EventSource` cannot send an `Authorization` header; the stream is cookie-only. Do not "fix" this by putting tokens in query strings (they would leak into logs).
- **Logging**: `redactPath` hides invite tokens and API paths; extend it rather than logging raw URLs.
- **CSP**: no inline `<script>`, no external assets, no `eval`. React `style` props are fine; static inline style attributes are not.
- **The last token**: revocation enforces at least one active token; do not relax that in tests or handlers.
- **`data/secret.key` is not disposable**: deleting it silently invalidates every sealed value, so TOTP enrollments stop verifying and the stored SMTP password no longer opens. It must be backed up with the database, and tests that exercise 2FA need a key (`testServer` and the e2e helpers create one).
- **Plain HTTP leaks the password**: hashing at rest does not encrypt the transport. The only real fix is TLS (`tls_auto`, `tls_cert`/`tls_key`, or a terminating proxy); do not try to "solve" it by hashing credentials in the browser, because the hash becomes the password.
- **TLS is opt-in**: configuration must reject half a certificate pair, `tls_auto` combined with `tls_cert`, and a redirect without TLS. With TLS on, the session cookie is `Secure` unconditionally.
- **Guests**: read-only, never transition, never enroll in 2FA, and expire by date; expiry is computed from `guest_expires_on`, not `disabled_at`.
- **2FA policy**: `require_2fa` affects the next sign-in, not open sessions, and never applies to guests.
- **Soft delete**: archived rows still exist and can still be referenced; queries must filter `archived_at IS NULL` unless intentionally listing archived records.
- **Module name**: the Go module is still `jiralike`; product strings, binary names, token prefix (`tgr_`), TOTP issuer (`Together`) and backup filenames (`together-backup-*`) use Together.
- **Generated files**: rebuilding the frontend changes hashed asset names in `internal/web/dist`; never edit those files. `dist/` is release output and is ignored by git.
- **Windows scripts**: the release scripts are PowerShell; keep them cross-platform and avoid hard-coded Windows paths.
- **Time zones**: due dates have no zone; instants are UTC. Mixing the two produces off-by-one-day bugs at month boundaries.

## Definition of done

- Behavior covered by tests at the lowest useful level, plus an e2e step for a user-visible flow.
- New secrets are hashed or sealed, and the `cmd/server` disk scan still passes.
- `gofmt -l` clean, `go vet ./...` clean, `go test ./... -count=1` green.
- `npm run typecheck` and `npm run build` green; both themes and the three languages checked for new UI.
- README updated if configuration, API, data model or operator behavior changed.
- `CHANGELOG.md` updated only when preparing a release.
- Release build and smoke test green before publishing `dist/together-*`.
