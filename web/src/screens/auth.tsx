import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { ArrowRight, KeyRound, Loader2, ShieldCheck } from "lucide-react";
import { api, ApiError, errorMessage } from "../api";
import { Link, useRouter } from "../router";
import { useI18n } from "../i18n";
import type { LoginChallenge, User } from "../types";
import { Button, Field, Input, InlineError } from "../components/ui";
import { TwoFactorEnrollment } from "../components/two-factor";
import { PreferenceControls } from "../preferences";

function AuthLayout({ children }: { children: ReactNode }) {
  const { t } = useI18n();
  return (
    <div className="auth-shell">
      <PreferenceControls className="auth-prefs" />
      <section className="auth-brand" aria-hidden="true">
        <div className="auth-brand-inner">
          <div className="auth-wordmark">
            <img className="brand-mark" src="/assets/logo-256.png" alt="" width={34} height={34} />
            Together
          </div>
          <p className="auth-statement">
            {t("The work is the point.")}
            <br />
            <em>{t("Not the tool.")}</em>
          </p>
        </div>
        <p className="auth-caption">
          {t(
            "Projects, items, deadlines and a workflow that belongs to your team. Self-hosted, one binary, no marketplace.",
          )}
        </p>
      </section>
      <section className="auth-panel">
        <div className="auth-form">
          <div className="auth-mobile-brand brand" style={{ marginBottom: 28 }}>
            <img className="brand-mark" src="/assets/logo-256.png" alt="" width={34} height={34} />
            <div>
              <div className="brand-word">Together</div>
              <span className="brand-sub">{t("self-hosted")}</span>
            </div>
          </div>
          {children}
        </div>
      </section>
    </div>
  );
}

export function SetupScreen({ onDone }: { onDone: (user: User) => void }) {
  const { t, locale } = useI18n();
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (name.trim() === "" || !email.includes("@") || password.length < 8) {
      setError(t("Name, email and a password of 8 characters are required."));
      return;
    }
    setBusy(true);
    try {
      const user = await api<User>("/api/setup", {
        method: "POST",
        body: { name: name.trim(), email: email.trim(), password, locale },
      });
      onDone(user);
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        setError(t("Setup is already complete."));
      } else {
        setError(errorMessage(err));
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthLayout>
      <p className="mark tiny muted">{t("First run")}</p>
      <h1>{t("Set up your workspace")}</h1>
      <p className="lede">
        {t(
          "Create the first administrator account. This installation has no open registration: everyone else joins through an invitation.",
        )}
      </p>
      <form className="form" onSubmit={submit}>
        {error ? <InlineError message={error} /> : null}
        <Field label={t("Full name")} htmlFor="setup-name" required>
          <Input
            id="setup-name"
            value={name}
            autoComplete="name"
            autoFocus
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("Email")} htmlFor="setup-email" required>
          <Input
            id="setup-email"
            type="email"
            value={email}
            autoComplete="email"
            onChange={(event) => setEmail(event.target.value)}
          />
        </Field>
        <Field label={t("Password")} htmlFor="setup-password" hint={t("At least 8 characters.")} required>
          <Input
            id="setup-password"
            type="password"
            value={password}
            autoComplete="new-password"
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
        <Button type="submit" variant="primary" block disabled={busy} icon={busy ? <Loader2 className="spinner" /> : <ShieldCheck />}>
          {busy ? t("Creating…") : t("Create administrator")}
        </Button>
      </form>
      <p className="auth-foot">{t("The first account always has the Admin role.")}</p>
    </AuthLayout>
  );
}

export function LoginScreen({ onDone }: { onDone: (user: User) => void }) {
  const { t } = useI18n();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [challenge, setChallenge] = useState<LoginChallenge | null>(null);
  const [code, setCode] = useState("");

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    setBusy(true);
    try {
      const body = await api<User | LoginChallenge>("/api/login", {
        method: "POST",
        body: { email: email.trim(), password },
      });
      if ("challenge_token" in body) {
        setChallenge(body);
        setCode("");
      } else {
        onDone(body);
      }
    } catch (err) {
      if (err instanceof ApiError) {
        switch (err.message) {
          case "wrong email or password":
            setError(t("Wrong email or password."));
            break;
          case "account disabled":
            setError(t("Your account is disabled."));
            break;
          case "too many login attempts":
            setError(t("Too many attempts. Try again in a minute."));
            break;
          default:
            if (err.message.startsWith("guest access ended on ")) {
              setError(
                t("Your guest access ended on {date}.", {
                  date: err.message.replace("guest access ended on ", ""),
                }),
              );
            } else {
              setError(err.message);
            }
        }
      } else {
        setError(errorMessage(err));
      }
    } finally {
      setBusy(false);
    }
  }

  async function verify(event: FormEvent) {
    event.preventDefault();
    if (!challenge) {
      return;
    }
    setError("");
    if (code.trim() === "") {
      setError(t("Enter a code to continue."));
      return;
    }
    setBusy(true);
    try {
      const user = await api<User>("/api/login/2fa", {
        method: "POST",
        body: { challenge_token: challenge.challenge_token, code: code.trim() },
      });
      onDone(user);
    } catch {
      setError(t("Invalid code or challenge. Try again."));
    } finally {
      setBusy(false);
    }
  }

  function reset() {
    setChallenge(null);
    setCode("");
    setError("");
  }

  if (challenge?.two_factor_setup_required) {
    return (
      <AuthLayout>
        <p className="mark tiny muted">{t("Second factor")}</p>
        <TwoFactorEnrollment challenge={challenge} onDone={onDone} onCancel={reset} />
      </AuthLayout>
    );
  }

  if (challenge) {
    return (
      <AuthLayout>
        <p className="mark tiny muted">{t("Second factor")}</p>
        <h1>{t("Confirm it is you")}</h1>
        <p className="lede">
          {t("Enter the six-digit code from your authenticator app, or one of your recovery codes.")}
        </p>
        <form className="form" onSubmit={verify}>
          {error ? <InlineError message={error} /> : null}
          <Field
            label={t("Code or recovery code")}
            hint={t("Six digits, or a recovery code in the form ABCD-EFGH-IJKL-MNOP.")}
            htmlFor="login-code"
            required
          >
            <Input
              id="login-code"
              value={code}
              autoComplete="one-time-code"
              autoFocus
              onChange={(event) => setCode(event.target.value.trim())}
            />
          </Field>
          <Button type="submit" variant="primary" block disabled={busy} icon={busy ? <Loader2 className="spinner" /> : <ShieldCheck />}>
            {busy ? t("Verifying…") : t("Continue")}
          </Button>
        </form>
        <p className="auth-foot">
          <button type="button" className="link-button" onClick={reset}>
            {t("Use another account")}
          </button>
        </p>
      </AuthLayout>
    );
  }

  return (
    <AuthLayout>
      <p className="mark tiny muted">Together</p>
      <h1>{t("Welcome back")}</h1>
      <p className="lede">{t("Sign in to continue to your work.")}</p>
      <form className="form" onSubmit={submit}>
        {error ? <InlineError message={error} /> : null}
        <Field label={t("Email")} htmlFor="login-email" required>
          <Input
            id="login-email"
            type="email"
            value={email}
            autoComplete="email"
            autoFocus
            onChange={(event) => setEmail(event.target.value)}
          />
        </Field>
        <Field label={t("Password")} htmlFor="login-password" required>
          <Input
            id="login-password"
            type="password"
            value={password}
            autoComplete="current-password"
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
        <Button type="submit" variant="primary" block disabled={busy} icon={busy ? <Loader2 className="spinner" /> : <ArrowRight />}>
          {busy ? t("Signing in…") : t("Sign in")}
        </Button>
      </form>
      <p className="auth-foot">{t("No account yet? Access is invitation-only.")}</p>
    </AuthLayout>
  );
}

type InvitePreview = {
  email: string;
  role: string;
  guest_days: number;
  expires_at: string;
};

export function InviteScreen({ token, onDone }: { token: string; onDone: (user: User) => void }) {
  const { t, locale } = useI18n();
  const [preview, setPreview] = useState<InvitePreview | null>(null);
  const [invalid, setInvalid] = useState(false);
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [challenge, setChallenge] = useState<LoginChallenge | null>(null);
  const { navigate } = useRouter();

  useEffect(() => {
    let cancelled = false;
    api<InvitePreview>(`/api/invites/${encodeURIComponent(token)}`)
      .then((body) => {
        if (!cancelled) {
          setPreview(body);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setInvalid(true);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (name.trim() === "" || password.length < 8) {
      setError(t("Your name and a password of at least 8 characters are required."));
      return;
    }
    setBusy(true);
    try {
      const body = await api<User | LoginChallenge>("/api/invites/accept", {
        method: "POST",
        body: { token, name: name.trim(), password, locale },
      });
      if ("challenge_token" in body) {
        setChallenge(body);
      } else {
        onDone(body);
      }
    } catch (err) {
      if (err instanceof ApiError && err.status === 410) {
        setInvalid(true);
      } else if (err instanceof ApiError && err.status === 409) {
        setError(t("An account with this email already exists."));
      } else {
        setError(errorMessage(err));
      }
    } finally {
      setBusy(false);
    }
  }

  if (challenge) {
    return (
      <AuthLayout>
        <p className="mark tiny muted">{t("Second factor")}</p>
        <TwoFactorEnrollment challenge={challenge} onDone={onDone} />
      </AuthLayout>
    );
  }

  if (invalid) {
    return (
      <AuthLayout>
        <p className="mark tiny muted">{t("Invitation")}</p>
        <h1>{t("This invitation is not usable")}</h1>
        <p className="lede">{t("This invitation is invalid, has expired, or has already been accepted.")}</p>
        <Button variant="secondary" onClick={() => navigate("/login")} icon={<KeyRound />}>
          {t("Go to sign in")}
        </Button>
        <p className="auth-foot">
          {t("Ask an administrator for a new invitation.")} <Link to="/login">{t("Sign in")}</Link>
        </p>
      </AuthLayout>
    );
  }

  return (
    <AuthLayout>
      <p className="mark tiny muted">{t("Invitation")}</p>
      <h1>{t("Join the workspace")}</h1>
      <p className="lede">
        {preview
          ? `${preview.email} · ${preview.role}${preview.role === "guest" && preview.guest_days ? ` · ${t("{count} days", { count: preview.guest_days })}` : ""}`
          : t("Checking this invitation…")}
      </p>
      <form className="form" onSubmit={submit}>
        {error ? <InlineError message={error} /> : null}
        <Field label={t("Full name")} htmlFor="invite-name" required>
          <Input
            id="invite-name"
            value={name}
            autoComplete="name"
            autoFocus
            disabled={!preview}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("Password")} htmlFor="invite-password" hint={t("At least 8 characters.")} required>
          <Input
            id="invite-password"
            type="password"
            value={password}
            autoComplete="new-password"
            disabled={!preview}
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
        <Button
          type="submit"
          variant="primary"
          block
          disabled={busy || !preview}
          icon={busy ? <Loader2 className="spinner" /> : <ArrowRight />}
        >
          {busy ? t("Joining…") : t("Accept invitation")}
        </Button>
      </form>
      {preview ? (
        <p className="auth-foot">
          {t("The email address and role were chosen by the person who invited you.")}
        </p>
      ) : null}
    </AuthLayout>
  );
}
