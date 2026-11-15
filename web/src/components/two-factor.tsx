import { useCallback, useEffect, useState, type FormEvent } from "react";
import QRCode from "qrcode";
import { Check, Copy, Download, KeyRound, Loader2, ShieldCheck } from "lucide-react";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { useApp } from "../state";
import type { EnrollResult, LoginChallenge, TwoFactorSetup, TwoFactorStatus, User } from "../types";
import {
  Badge,
  Button,
  CopyField,
  ErrorState,
  Field,
  InlineError,
  Input,
  Loading,
  useToast,
} from "./ui";

export function QrCode({ value }: { value: string }) {
  const { t } = useI18n();
  const [source, setSource] = useState("");

  useEffect(() => {
    let cancelled = false;
    QRCode.toDataURL(value, { margin: 1, width: 240, errorCorrectionLevel: "M" })
      .then((url) => {
        if (!cancelled) {
          setSource(url);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setSource("");
        }
      });
    return () => {
      cancelled = true;
    };
  }, [value]);

  if (!source) {
    return <Loading label={t("Building the QR code…")} />;
  }
  return (
    <img
      className="qr-code"
      src={source}
      width={240}
      height={240}
      alt={t("QR code for your authenticator app")}
    />
  );
}

function downloadRecoveryCodes(codes: string[]) {
  const text = `${codes.join("\n")}\n`;
  const blob = new Blob([text], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = "together-recovery-codes.txt";
  document.body.appendChild(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export function RecoveryCodesPanel({
  codes,
  onContinue,
  continueLabel,
}: {
  codes: string[];
  onContinue?: () => void;
  continueLabel?: string;
}) {
  const { t } = useI18n();
  const { notify } = useToast();

  async function copyAll() {
    try {
      await navigator.clipboard.writeText(codes.join("\n"));
      notify(t("Recovery codes copied."), "good");
    } catch {
      notify(t("Copy failed. Select the codes and copy them manually."), "bad");
    }
  }

  return (
    <div className="stack-sm">
      <div className="banner banner--warn" role="alert">
        <KeyRound />
        <span>{t("These recovery codes are shown once. Store them somewhere safe: each one works a single time.")}</span>
      </div>
      <ul className="recovery-codes" aria-label={t("Recovery codes")}>
        {codes.map((code) => (
          <li key={code}>
            <code>{code}</code>
          </li>
        ))}
      </ul>
      <div className="cluster">
        <Button variant="secondary" icon={<Copy />} onClick={copyAll}>
          {t("Copy all")}
        </Button>
        <Button variant="secondary" icon={<Download />} onClick={() => downloadRecoveryCodes(codes)}>
          {t("Download .txt")}
        </Button>
      </div>
      {onContinue ? (
        <Button variant="primary" icon={<Check />} onClick={onContinue}>
          {continueLabel ?? t("I saved the codes")}
        </Button>
      ) : null}
    </div>
  );
}

/**
 * Shared second-step enrollment used by sign-in and invitation acceptance. The
 * challenge token, not a session, authorizes start and confirm.
 */
export function TwoFactorEnrollment({
  challenge,
  onDone,
  onCancel,
}: {
  challenge: LoginChallenge;
  onDone: (user: User) => void;
  onCancel?: () => void;
}) {
  const { t } = useI18n();
  const [setup, setSetup] = useState<TwoFactorSetup | null>(null);
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<EnrollResult | null>(null);

  useEffect(() => {
    let cancelled = false;
    api<TwoFactorSetup>("/api/login/2fa/start", {
      method: "POST",
      body: { challenge_token: challenge.challenge_token },
    })
      .then((body) => {
        if (!cancelled) {
          setSetup(body);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(errorMessage(err));
        }
      })
      .finally(() => {
        if (!cancelled) {
          setLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [challenge.challenge_token]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (!/^\d{6}$/.test(code.trim())) {
      setError(t("Enter the six-digit code from your authenticator app."));
      return;
    }
    setBusy(true);
    try {
      const body = await api<EnrollResult>("/api/login/2fa/confirm", {
        method: "POST",
        body: { challenge_token: challenge.challenge_token, code: code.trim() },
      });
      setResult(body);
    } catch (err) {
      setError(t("Invalid code or challenge. Try again."));
    } finally {
      setBusy(false);
    }
  }

  if (result) {
    return (
      <div className="stack-sm">
        <h1>{t("Save your recovery codes")}</h1>
        <p className="small muted">
          {t("If you lose your authenticator app, a recovery code is the only way back in.")}
        </p>
        <RecoveryCodesPanel codes={result.recovery_codes} onContinue={() => onDone(result.user)} />
      </div>
    );
  }

  return (
    <form className="form" onSubmit={submit}>
      <h1>{t("Set up two-factor authentication")}</h1>
      <p className="small muted">
        {t("Scan the QR code with an authenticator app, then enter the six-digit code it shows.")}
      </p>
      {error ? <InlineError message={error} /> : null}
      {loading ? <Loading /> : null}
      {setup ? (
        <>
          <div className="enroll-qr">
            <QrCode value={setup.otpauth_uri} />
          </div>
          <Field label={t("Setup secret")} hint={t("You can type this into the app instead of scanning.")} htmlFor="enroll-secret">
            <CopyField value={setup.secret} label={t("Setup secret")} />
          </Field>
          <Field label={t("Six-digit code")} htmlFor="enroll-code" required>
            <Input
              id="enroll-code"
              value={code}
              inputMode="numeric"
              autoComplete="one-time-code"
              pattern="[0-9]*"
              maxLength={6}
              autoFocus
              onChange={(event) => setCode(event.target.value.replace(/\s/g, ""))}
            />
          </Field>
          <Button type="submit" variant="primary" block disabled={busy} icon={busy ? <Loader2 className="spinner" /> : <ShieldCheck />}>
            {busy ? t("Verifying…") : t("Verify and enable")}
          </Button>
        </>
      ) : null}
      {onCancel ? (
        <Button variant="ghost" onClick={onCancel}>
          {t("Use another account")}
        </Button>
      ) : null}
    </form>
  );
}

/** Profile card: enroll, rotate recovery codes and disable the second factor. */
export function TwoFactorSecurityCard() {
  const { t } = useI18n();
  const { user, setUser } = useApp();
  const { notify } = useToast();
  const [status, setStatus] = useState<TwoFactorStatus | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [setup, setSetup] = useState<TwoFactorSetup | null>(null);
  const [mode, setMode] = useState<"none" | "enable" | "disable" | "rotate">("none");
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [freshCodes, setFreshCodes] = useState<string[] | null>(null);

  const loadStatus = useCallback(async () => {
    try {
      const body = await api<TwoFactorStatus>("/api/me/2fa");
      setStatus(body);
      setError("");
    } catch (err) {
      setError(errorMessage(err));
    }
  }, []);

  useEffect(() => {
    void loadStatus();
  }, [loadStatus]);

  async function startSetup() {
    setBusy(true);
    setError("");
    try {
      const body = await api<TwoFactorSetup>("/api/me/2fa/start", { method: "POST" });
      setSetup(body);
      setFreshCodes(null);
      setMode("enable");
      setCode("");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function confirm(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (!/^\d{6}$/.test(code.trim())) {
      setError(t("Enter the six-digit code from your authenticator app."));
      return;
    }
    setBusy(true);
    try {
      const body = await api<{ recovery_codes: string[] }>("/api/me/2fa/confirm", {
        method: "POST",
        body: { code: code.trim() },
      });
      setFreshCodes(body.recovery_codes);
      setSetup(null);
      setMode("none");
      setCode("");
      setUser({ ...user, two_factor_enabled: true });
      await loadStatus();
      notify(t("Two-factor authentication enabled."), "good");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function disable(event: FormEvent) {
    event.preventDefault();
    setError("");
    setBusy(true);
    try {
      await api("/api/me/2fa/disable", {
        method: "POST",
        body: { password, code: code.trim() },
      });
      setPassword("");
      setCode("");
      setMode("none");
      setUser({ ...user, two_factor_enabled: false });
      await loadStatus();
      notify(t("Two-factor authentication disabled."), "good");
    } catch {
      setError(t("Wrong password or code. Nothing was changed."));
    } finally {
      setBusy(false);
    }
  }

  async function rotate(event: FormEvent) {
    event.preventDefault();
    setError("");
    setBusy(true);
    try {
      const body = await api<{ recovery_codes: string[] }>("/api/me/2fa/recovery-codes", {
        method: "POST",
        body: { password, code: code.trim() },
      });
      setFreshCodes(body.recovery_codes);
      setPassword("");
      setCode("");
      setMode("none");
      await loadStatus();
      notify(t("New recovery codes generated."), "good");
    } catch {
      setError(t("Wrong password or code. Nothing was changed."));
    } finally {
      setBusy(false);
    }
  }

  const enabled = Boolean(status?.enabled);
  const available = status?.available !== false;

  return (
    <div className="card">
      <div className="card-head">
        <div>
          <h2 className="card-title">{t("Security")}</h2>
          <p className="card-sub">{t("A second step protects interactive sign-in.")}</p>
        </div>
        <Badge tone={!available ? "mono" : enabled ? "good" : "warn"}>
          {!available ? t("Not available") : enabled ? t("Enabled") : t("Not enabled")}
        </Badge>
      </div>
      <div className="card-body stack-sm">
        {error ? <InlineError message={error} /> : null}
        {status === null && !error ? <Loading /> : null}
        {status === null && error ? <ErrorState message={error} onRetry={() => void loadStatus()} /> : null}

        {status ? (
          <>
            <p className="small muted">
              {available
                ? t(
                    "Use an authenticator app. Recovery codes are the fallback if the app is lost; an administrator can also reset the second factor.",
                  )
                : t("Two-factor authentication is not available for guest accounts.")}
            </p>
            <p className="tiny muted">
              {t("API tokens stay single-factor: they are long-lived secrets, not interactive sign-in.")}
            </p>

            {freshCodes ? (
              <RecoveryCodesPanel
                codes={freshCodes}
                onContinue={() => {
                  setFreshCodes(null);
                  void loadStatus();
                }}
              />
            ) : null}

            {mode === "enable" && setup ? (
              <form className="form" onSubmit={confirm}>
                <div className="enroll-qr">
                  <QrCode value={setup.otpauth_uri} />
                </div>
                <Field label={t("Setup secret")} hint={t("You can type this into the app instead of scanning.")} htmlFor="security-secret">
                  <CopyField value={setup.secret} label={t("Setup secret")} />
                </Field>
                <Field label={t("Six-digit code")} htmlFor="security-code" required>
                  <Input
                    id="security-code"
                    value={code}
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    pattern="[0-9]*"
                    maxLength={6}
                    autoFocus
                    onChange={(event) => setCode(event.target.value.replace(/\s/g, ""))}
                  />
                </Field>
                <div className="cluster">
                  <Button type="submit" variant="primary" icon={<ShieldCheck />} disabled={busy}>
                    {busy ? t("Verifying…") : t("Verify and enable")}
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={() => {
                      setSetup(null);
                      setMode("none");
                      setCode("");
                    }}
                  >
                    {t("Cancel")}
                  </Button>
                </div>
              </form>
            ) : null}

            {mode === "disable" || mode === "rotate" ? (
              <form className="form" onSubmit={mode === "disable" ? disable : rotate}>
                <p className="small">
                  {mode === "disable"
                    ? t("Confirm with your password and a current code to turn off two-factor authentication.")
                    : t("Confirm with your password and a current code to replace every recovery code.")}
                </p>
                <Field label={t("Password")} htmlFor="security-password" required>
                  <Input
                    id="security-password"
                    type="password"
                    value={password}
                    autoComplete="current-password"
                    onChange={(event) => setPassword(event.target.value)}
                  />
                </Field>
                <Field label={t("Code or recovery code")} htmlFor="security-confirm-code" required>
                  <Input
                    id="security-confirm-code"
                    value={code}
                    autoComplete="one-time-code"
                    onChange={(event) => setCode(event.target.value.trim())}
                  />
                </Field>
                <div className="cluster">
                  <Button type="submit" variant={mode === "disable" ? "danger" : "primary"} disabled={busy}>
                    {mode === "disable" ? t("Disable two-factor authentication") : t("Generate new codes")}
                  </Button>
                  <Button
                    variant="ghost"
                    onClick={() => {
                      setMode("none");
                      setPassword("");
                      setCode("");
                    }}
                  >
                    {t("Cancel")}
                  </Button>
                </div>
              </form>
            ) : null}

            {available && !freshCodes && mode === "none" ? (
              <div className="cluster">
                {enabled ? (
                  <>
                    <Button variant="secondary" icon={<KeyRound />} disabled={busy} onClick={() => setMode("rotate")}>
                      {t("Regenerate recovery codes")}
                    </Button>
                    <Button variant="danger-ghost" disabled={busy} onClick={() => setMode("disable")}>
                      {t("Disable two-factor authentication")}
                    </Button>
                  </>
                ) : (
                  <Button variant="primary" icon={<ShieldCheck />} disabled={busy} onClick={startSetup}>
                    {busy ? t("Preparing…") : t("Enable two-factor authentication")}
                  </Button>
                )}
              </div>
            ) : null}

            {enabled ? (
              <p className="tiny muted">
                {t("{count} recovery codes remaining.", { count: status.recovery_codes_remaining })}
              </p>
            ) : null}
          </>
        ) : null}
      </div>
    </div>
  );
}
