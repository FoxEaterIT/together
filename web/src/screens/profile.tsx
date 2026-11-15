import { useEffect, useState, type FormEvent } from "react";
import { Copy, Plus, Save, Trash2 } from "lucide-react";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { LOCALES, LOCALE_NAMES, useTheme, type Theme } from "../preferences";
import { useApp } from "../state";
import type { Locale, Token, User } from "../types";
import { TwoFactorSecurityCard } from "../components/two-factor";
import {
  Badge,
  Button,
  ErrorState,
  Field,
  InlineError,
  Input,
  Loading,
  PageHead,
  Select,
  Switch,
  useToast,
} from "../components/ui";

export function ProfileScreen() {
  const { t, formatDate } = useI18n();
  const { theme, setTheme } = useTheme();
  const { user, setUser } = useApp();
  const { notify } = useToast();

  const [name, setName] = useState(user.name);
  const [locale, setLocale] = useState<Locale>(user.locale);
  const [emailNotifications, setEmailNotifications] = useState(user.email_notifications);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const [tokens, setTokens] = useState<Token[] | null>(null);
  const [tokenError, setTokenError] = useState("");
  const [tokenName, setTokenName] = useState("");
  const [tokenBusy, setTokenBusy] = useState(false);
  // The API returns a personal token exactly once; this keeps it on screen until
  // the person copies it or dismisses the panel.
  const [freshToken, setFreshToken] = useState("");

  useEffect(() => {
    setName(user.name);
    setLocale(user.locale);
    setEmailNotifications(user.email_notifications);
  }, [user]);

  useEffect(() => {
    let cancelled = false;
    api<{ tokens: Token[] }>("/api/tokens")
      .then((body) => {
        if (!cancelled) {
          setTokens(body.tokens);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setTokenError(errorMessage(err));
          setTokens([]);
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function save(event: FormEvent) {
    event.preventDefault();
    setError("");
    setBusy(true);
    try {
      const updated = await api<User>("/api/me", {
        method: "PUT",
        body: {
          name: name.trim(),
          locale,
          email_notifications: emailNotifications ? 1 : 0,
        },
      });
      setUser({ ...user, ...updated });
      notify(t("Profile saved."), "good");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function createToken(event: FormEvent) {
    event.preventDefault();
    setTokenError("");
    if (tokenName.trim() === "") {
      setTokenError(t("Please fill in the required fields."));
      return;
    }
    setTokenBusy(true);
    try {
      const token = await api<Token>("/api/tokens", {
        method: "POST",
        body: { name: tokenName.trim() },
      });
      setTokens((current) => [...(current ?? []), token]);
      setFreshToken(token.token ?? "");
      setTokenName("");
      notify(t("Token created."), "good");
    } catch (err) {
      setTokenError(errorMessage(err));
    } finally {
      setTokenBusy(false);
    }
  }

  async function copyToken(value: string) {
    try {
      await navigator.clipboard.writeText(value);
      notify(t("Copied"), "good");
    } catch {
      // Clipboard access can be blocked; the value stays on screen for a manual copy.
      notify(t("Copy the value manually."), "bad");
    }
  }

  async function revoke(token: Token) {
    setTokenBusy(true);
    try {
      await api(`/api/tokens/${token.id}`, { method: "DELETE" });
      setTokens((current) => (current ?? []).filter((entry) => entry.id !== token.id));
      notify(t("Token revoked."), "good");
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setTokenBusy(false);
    }
  }

  return (
    <>
      <PageHead title={t("Profile")} lede={t("Your name, language and email preference.")} />

      <div className="admin-grid">
        <form className="card card-pad form" onSubmit={save}>
          <h2 className="card-title">{t("Profile")}</h2>
          {error ? <InlineError message={error} /> : null}
          <Field label={t("Display name")} required htmlFor="profile-name">
            <Input
              id="profile-name"
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          <Field label={t("Email")} htmlFor="profile-email">
            <Input id="profile-email" value={user.email} disabled readOnly />
          </Field>
          <Field label={t("Language")} htmlFor="profile-locale">
            <Select
              id="profile-locale"
              value={locale}
              onChange={(event) => setLocale(event.target.value as Locale)}
            >
              {LOCALES.map((value) => (
                <option key={value} value={value}>
                  {LOCALE_NAMES[value]}
                </option>
              ))}
            </Select>
          </Field>
          <Field label={t("Theme")} htmlFor="profile-theme">
            <Select
              id="profile-theme"
              value={theme}
              onChange={(event) => setTheme(event.target.value as Theme)}
            >
              <option value="light">{t("Light")}</option>
              <option value="dark">{t("Dark")}</option>
            </Select>
          </Field>
          <div className="field">
            <span className="field-label">{t("Email notifications")}</span>
            <Switch
              checked={emailNotifications}
              disabled={!user.smtp_configured}
              onChange={setEmailNotifications}
              label={t("Send me email for assignments, mentions and due summaries.")}
            />
            {!user.smtp_configured ? (
              <span className="field-hint">{t("Email is unavailable because SMTP is not configured.")}</span>
            ) : null}
          </div>
          <div>
            <Button type="submit" variant="primary" icon={<Save />} disabled={busy}>
              {t("Save changes")}
            </Button>
          </div>
        </form>

        <div className="card">
          <div className="card-head">
            <div>
              <h2 className="card-title">{t("API tokens")}</h2>
              <p className="card-sub">{t("Tokens do not expire. Keep at least one active token.")}</p>
            </div>
            <Badge tone="mono">{tokens?.length ?? 0}</Badge>
          </div>
          <div className="card-body stack-sm">
            {tokenError ? <InlineError message={tokenError} /> : null}
            <form className="cluster-lg" style={{ alignItems: "flex-end" }} onSubmit={createToken}>
              <Field label={t("Token name")} className="grow" htmlFor="token-name">
                <Input
                  id="token-name"
                  value={tokenName}
                  placeholder={t("New token")}
                  onChange={(event) => setTokenName(event.target.value)}
                />
              </Field>
              <Button type="submit" variant="secondary" icon={<Plus />} disabled={tokenBusy}>
                {t("Create token")}
              </Button>
            </form>

            {tokens === null ? <Loading /> : null}
            {tokenError && tokens === null ? <ErrorState message={tokenError} /> : null}
            {tokens !== null && tokens.length === 0 ? <p className="small muted">{t("No tokens yet.")}</p> : null}

            {freshToken ? (
              <div className="token-fresh">
                <div className="token-fresh-head">
                  <strong>{t("Copy this token now")}</strong>
                  <Button size="sm" variant="ghost" onClick={() => setFreshToken("")}>
                    {t("Dismiss")}
                  </Button>
                </div>
                <code className="token-value">{freshToken}</code>
                <div className="cluster" style={{ gap: 8, alignItems: "center" }}>
                  <Button size="sm" variant="secondary" icon={<Copy />} onClick={() => copyToken(freshToken)}>
                    {t("Copy token")}
                  </Button>
                  <span className="tiny muted">{t("It is stored hashed and will not be shown again.")}</span>
                </div>
              </div>
            ) : null}

            {(tokens ?? []).map((token) => (
              <div key={token.id} className="token-row">
                <div className="list-main">
                  <div className="list-name">{token.name}</div>
                  <code className="token-value">{token.prefix}…</code>
                  <div className="tiny muted">{t("Created {date}", { date: formatDate(token.created_at) })}</div>
                </div>
                <div className="list-actions">
                  <Button
                    size="sm"
                    variant="danger-ghost"
                    icon={<Trash2 />}
                    disabled={tokenBusy || (tokens?.length ?? 0) < 2}
                    title={(tokens?.length ?? 0) < 2 ? t("You need at least one active token. Create another before revoking this one.") : t("Revoke token")}
                    onClick={() => revoke(token)}
                  >
                    {t("Revoke token")}
                  </Button>
                </div>
              </div>
            ))}

            {(tokens?.length ?? 0) < 2 ? (
              <p className="tiny muted">{t("You need at least one active token. Create another before revoking this one.")}</p>
            ) : null}
            <p className="tiny muted">{t("Only a prefix is kept so you can recognise a token; the value itself is not stored.")}</p>
          </div>
        </div>

        <TwoFactorSecurityCard />
      </div>
    </>
  );
}
