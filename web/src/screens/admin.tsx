import { useEffect, useState, type FormEvent } from "react";
import { Database, Download, RefreshCw, Save, Send, ShieldCheck } from "lucide-react";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { useApp, useLoader } from "../state";
import type { SecuritySettings, SmtpSettings } from "../types";
import {
  Badge,
  Button,
  ErrorState,
  Field,
  InlineError,
  Input,
  Loading,
  PageHead,
  Switch,
  useToast,
} from "../components/ui";

export function AdminScreen() {
  const { t } = useI18n();
  const { user, setUser } = useApp();
  const { notify } = useToast();
  const state = useLoader(() => api<SmtpSettings>("/api/admin/smtp"), []);

  const [host, setHost] = useState("");
  const [port, setPort] = useState("587");
  const [smtpUser, setSmtpUser] = useState("");
  const [password, setPassword] = useState("");
  const [from, setFrom] = useState("");
  const [tls, setTls] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [testBusy, setTestBusy] = useState(false);
  const [backupBusy, setBackupBusy] = useState(false);
  const [rebuildBusy, setRebuildBusy] = useState(false);
  const [security, setSecurity] = useState<SecuritySettings | null>(null);
  const [securityError, setSecurityError] = useState("");
  const [securityBusy, setSecurityBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    api<SecuritySettings>("/api/admin/security")
      .then((body) => {
        if (!cancelled) {
          setSecurity(body);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setSecurityError(errorMessage(err));
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!state.data) {
      return;
    }
    setHost(state.data.host);
    setPort(state.data.port ? String(state.data.port) : "587");
    setSmtpUser(state.data.user);
    setFrom(state.data.from);
    setTls(state.data.tls);
  }, [state.data]);

  async function save(event: FormEvent) {
    event.preventDefault();
    setError("");
    setBusy(true);
    try {
      const body = await api<SmtpSettings>("/api/admin/smtp", {
        method: "PUT",
        body: {
          host: host.trim(),
          port: Number(port) || 0,
          user: smtpUser.trim(),
          password: password === "" ? undefined : password,
          from: from.trim(),
          tls,
        },
      });
      setPassword("");
      setUser({ ...user, smtp_configured: body.configured });
      notify(t("SMTP settings saved."), "good");
      state.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function sendTest() {
    setTestBusy(true);
    try {
      await api("/api/admin/smtp/test", { method: "POST" });
      notify(t("Test email sent."), "good");
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setTestBusy(false);
    }
  }

  async function downloadBackup() {
    setBackupBusy(true);
    try {
      const response = await fetch("/api/admin/backup", { credentials: "same-origin" });
      if (!response.ok) {
        let message = response.statusText || "Request failed";
        try {
          const body = (await response.json()) as { error?: string };
          if (body.error) {
            message = body.error;
          }
        } catch {
          // A non-JSON error body keeps the status text.
        }
        throw new Error(message);
      }
      const blob = await response.blob();
      const disposition = response.headers.get("Content-Disposition") ?? "";
      const match = /filename="([^"]+)"/.exec(disposition);
      const filename = match?.[1] ?? "together-backup.db";
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = filename;
      document.body.appendChild(link);
      link.click();
      link.remove();
      // Revoking immediately can abort the download in some browsers.
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
      notify(t("Backup downloaded."), "good");
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setBackupBusy(false);
    }
  }

  async function rebuildSearch() {
    setRebuildBusy(true);
    try {
      const body = await api<{ indexed: number }>("/api/admin/search/rebuild", { method: "POST" });
      notify(t("Search index rebuilt: {count} rows.", { count: body.indexed }), "good");
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setRebuildBusy(false);
    }
  }

  async function saveSecurity() {
    if (!security) {
      return;
    }
    setSecurityBusy(true);
    setSecurityError("");
    try {
      const body = await api<SecuritySettings>("/api/admin/security", {
        method: "PUT",
        body: { require_2fa: security.require_2fa },
      });
      setSecurity(body);
      notify(
        body.require_2fa
          ? t("Two-factor authentication is now required for non-guest accounts.")
          : t("Two-factor authentication is no longer required."),
        "good",
      );
    } catch (err) {
      setSecurityError(errorMessage(err));
    } finally {
      setSecurityBusy(false);
    }
  }

  return (
    <>
      <PageHead title={t("Mail server")} lede={t("Mail delivery and operational notes.")} />

      {state.loading ? <Loading /> : null}
      {state.error ? <ErrorState message={state.error} onRetry={state.reload} /> : null}

      {state.data ? (
        <div className="admin-grid">
          <form className="card card-pad form" onSubmit={save}>
            <div className="spread">
              <h2 className="card-title">{t("SMTP settings")}</h2>
              <Badge tone={state.data.configured ? "good" : "warn"}>
                {state.data.configured ? t("Configured") : t("Not configured")}
              </Badge>
            </div>
            <p className="small muted">
              {t("When SMTP is configured, the installation can send invitations and notification email.")}
            </p>
            {error ? <InlineError message={error} /> : null}

            <div className="form-grid">
              <Field label={t("Host")} htmlFor="smtp-host">
                <Input id="smtp-host" value={host} onChange={(event) => setHost(event.target.value)} />
              </Field>
              <Field label={t("Port")} htmlFor="smtp-port">
                <Input
                  id="smtp-port"
                  type="number"
                  min={1}
                  max={65535}
                  value={port}
                  onChange={(event) => setPort(event.target.value)}
                />
              </Field>
            </div>

            <div className="form-grid">
              <Field label={t("Username")} htmlFor="smtp-user">
                <Input id="smtp-user" value={smtpUser} onChange={(event) => setSmtpUser(event.target.value)} />
              </Field>
              <Field
                label={t("Password")}
                hint={t("Leave blank to keep the current password.")}
                htmlFor="smtp-password"
              >
                <Input
                  id="smtp-password"
                  type="password"
                  value={password}
                  autoComplete="new-password"
                  onChange={(event) => setPassword(event.target.value)}
                />
              </Field>
            </div>

            <Field label={t("Sender address")} htmlFor="smtp-from">
              <Input id="smtp-from" type="email" value={from} onChange={(event) => setFrom(event.target.value)} />
            </Field>

            <Switch checked={tls} onChange={setTls} label={t("Use TLS")} />

            <div className="cluster">
              <Button type="submit" variant="primary" icon={<Save />} disabled={busy}>
                {t("Save SMTP")}
              </Button>
              <Button
                type="button"
                variant="secondary"
                icon={<Send />}
                disabled={testBusy || !state.data.configured}
                onClick={sendTest}
              >
                {t("Send test email")}
              </Button>
            </div>

            <div className="cluster">
              <Badge tone={state.data.has_password ? "good" : "mono"}>
                {state.data.has_password ? t("Password stored") : t("No password stored")}
              </Badge>
              <Badge tone={state.data.configured ? "good" : "warn"}>
                {state.data.configured ? t("Configured") : t("Not configured")}
              </Badge>
            </div>
          </form>

          <div className="card">
            <div className="card-head">
              <div>
                <h2 className="card-title">{t("Backup")}</h2>
                <p className="card-sub">{t("Manual backup only.")}</p>
              </div>
              <Database />
            </div>
            <div className="card-body stack-sm">
              <p className="small muted">
                {t(
                  "The download is a consistent snapshot of the SQLite database with WAL content included.",
                )}
              </p>
              <div>
                <Button
                  type="button"
                  variant="secondary"
                  icon={<Download />}
                  disabled={backupBusy}
                  onClick={downloadBackup}
                >
                  {backupBusy ? t("Preparing backup…") : t("Download backup")}
                </Button>
              </div>
              <p className="tiny muted">
                {t(
                  "Stop heavy writes before copying the file by hand from disk. The database lives in the configured data directory as app.db.",
                )}
              </p>
              <p className="tiny muted">{t("There are no attachments.")}</p>
            </div>
          </div>

          <div className="card">
            <div className="card-head">
              <div>
                <h2 className="card-title">{t("Search index")}</h2>
                <p className="card-sub">{t("Full-text index for titles, descriptions and comments.")}</p>
              </div>
              <RefreshCw />
            </div>
            <div className="card-body stack-sm">
              <p className="small muted">
                {t("Normal edits update the index automatically. Rebuild it after restoring a database by hand or if search results look stale.")}
              </p>
              <div>
                <Button
                  type="button"
                  variant="secondary"
                  icon={<RefreshCw />}
                  disabled={rebuildBusy}
                  onClick={rebuildSearch}
                >
                  {rebuildBusy ? t("Rebuilding…") : t("Rebuild search index")}
                </Button>
              </div>
            </div>
          </div>

          <div className="card">
            <div className="card-head">
              <div>
                <h2 className="card-title">{t("Two-factor authentication")}</h2>
                <p className="card-sub">{t("Enrollment policy for non-guest accounts.")}</p>
              </div>
              <ShieldCheck />
            </div>
            <div className="card-body stack-sm">
              {securityError ? <InlineError message={securityError} /> : null}
              {security ? (
                <>
                  <Switch
                    checked={security.require_2fa}
                    onChange={(checked) => setSecurity({ ...security, require_2fa: checked })}
                    label={t("Require two-factor authentication")}
                  />
                  <p className="small muted">
                    {t(
                      "Every non-guest account without a confirmed second factor must enroll at its next sign-in. Sessions already open stay valid until they expire. Turning the switch off does not remove existing enrollments.",
                    )}
                  </p>
                  <div>
                    <Button
                      type="button"
                      variant="primary"
                      icon={<Save />}
                      disabled={securityBusy}
                      onClick={saveSecurity}
                    >
                      {securityBusy ? t("Saving…") : t("Save security policy")}
                    </Button>
                  </div>
                </>
              ) : (
                <Loading />
              )}
            </div>
          </div>
        </div>
      ) : null}
    </>
  );
}
