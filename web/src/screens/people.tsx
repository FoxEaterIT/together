import { useState, type FormEvent } from "react";
import { Check, Copy, Mail, Send, ShieldCheck, UserPlus, Users, X } from "lucide-react";
import { api, errorMessage } from "../api";
import { roleLabel, useI18n } from "../i18n";
import { Link } from "../router";
import { useApp, useLoader } from "../state";
import type { Invite, User } from "../types";
import {
  Badge,
  Button,
  ConfirmDialog,
  CopyField,
  EmptyState,
  ErrorState,
  Field,
  InlineError,
  Input,
  Loading,
  PageHead,
  Select,
  useToast,
} from "../components/ui";

export function PeopleScreen() {
  const { t, formatDate } = useI18n();
  const { user } = useApp();
  const { notify } = useToast();
  const state = useLoader(() => api<{ users: User[] }>("/api/users"), []);
  const [busyID, setBusyID] = useState<number | null>(null);
  const [guestDays, setGuestDays] = useState<Record<number, number>>({});
  const [resetTarget, setResetTarget] = useState<User | null>(null);
  const [resetBusy, setResetBusy] = useState(false);

  const users = state.data?.users ?? [];

  async function update(target: User, body: Record<string, unknown>, success: string) {
    setBusyID(target.id);
    try {
      await api(`/api/users/${target.id}`, { method: "PUT", body });
      notify(success, "good");
      state.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setBusyID(null);
    }
  }

  async function resetTwoFactor() {
    if (!resetTarget) {
      return;
    }
    setResetBusy(true);
    try {
      await api(`/api/users/${resetTarget.id}/2fa/reset`, { method: "POST" });
      notify(t("Two-factor authentication reset."), "good");
      setResetTarget(null);
      state.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setResetBusy(false);
    }
  }

  return (
    <>
      <PageHead
        title={t("People")}
        lede={t("Manage accounts, roles and guest access.")}
        actions={
          <Link to="/people/invites" className="btn btn--primary">
            <UserPlus /> {t("Invite person")}
          </Link>
        }
      />

      {state.loading ? <Loading /> : null}
      {state.error ? <ErrorState message={state.error} onRetry={state.reload} /> : null}

      {!state.loading && !state.error ? (
        users.length === 0 ? (
          <div className="card">
            <EmptyState icon={<Users />} title={t("No people found.")} />
          </div>
        ) : (
          <div className="table-wrap">
            <table className="data-table">
              <thead>
                <tr>
                  <th>{t("Person")}</th>
                  <th>{t("Role")}</th>
                  <th>{t("Guest until")}</th>
                  <th>{t("Account")}</th>
                  <th>{t("2FA")}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {users.map((person) => {
                  const self = person.id === user.id;
                  const disabled = person.disabled;
                  return (
                    <tr key={person.id}>
                      <td className="cell-primary">
                        <div className="list-main">
                          <div className="list-name">
                            {person.name} {self ? <Badge tone="accent">{t("You")}</Badge> : null}
                          </div>
                          <div className="list-sub">{person.email}</div>
                        </div>
                      </td>
                      <td data-label={t("Role")}>
                        {self ? (
                          <Badge tone="mono">{roleLabel(person.role, t)}</Badge>
                        ) : (
                          <Select
                            value={person.role}
                            disabled={busyID === person.id}
                            onChange={(event) => {
                              const role = event.target.value;
                              const body: Record<string, unknown> = { role };
                              if (role === "guest") {
                                body.guest_days = 30;
                              }
                              void update(person, body, t("Role updated."));
                            }}
                          >
                            <option value="admin">{t("Admin role")}</option>
                            <option value="manager">{t("Manager")}</option>
                            <option value="member">{t("Member")}</option>
                            <option value="guest">{t("Guest")}</option>
                          </Select>
                        )}
                      </td>
                      <td data-label={t("Guest until")}>
                        {person.role === "guest" && person.guest_expires_on ? (
                          <span className={disabled ? "small muted" : "small"}>{formatDate(person.guest_expires_on)}</span>
                        ) : (
                          <span className="muted">—</span>
                        )}
                      </td>
                      <td data-label={t("Account")}>
                        <Badge tone={disabled ? "bad" : "good"}>{disabled ? t("Disabled") : t("Active")}</Badge>
                      </td>
                      <td data-label={t("2FA")}>
                        <Badge tone={person.two_factor_enabled ? "good" : "mono"}>
                          {person.two_factor_enabled ? t("Enabled") : t("Not enabled")}
                        </Badge>
                      </td>
                      <td data-label={t("Actions")}>
                        {self ? (
                          <span className="tiny muted">{t("You cannot change your own account here. Use your profile.")}</span>
                        ) : (
                          <div className="list-actions">
                            {person.role === "guest" ? (
                              <>
                                <Select
                                  value={guestDays[person.id] ?? 30}
                                  disabled={busyID === person.id}
                                  onChange={(event) =>
                                    setGuestDays((current) => ({ ...current, [person.id]: Number(event.target.value) }))
                                  }
                                >
                                  <option value={7}>{t("7 days")}</option>
                                  <option value={30}>{t("30 days")}</option>
                                  <option value={90}>{t("90 days")}</option>
                                </Select>
                                <Button
                                  size="sm"
                                  variant="secondary"
                                  disabled={busyID === person.id}
                                  onClick={() =>
                                    void update(
                                      person,
                                      { guest_days: guestDays[person.id] ?? 30 },
                                      t("Guest access extended."),
                                    )
                                  }
                                >
                                  {t("Extend guest access")}
                                </Button>
                              </>
                            ) : null}
                            <Button
                              size="sm"
                              variant={disabled ? "secondary" : "danger-ghost"}
                              disabled={busyID === person.id}
                              onClick={() =>
                                void update(
                                  person,
                                  { disabled: !disabled },
                                  disabled ? t("Account enabled.") : t("Account disabled."),
                                )
                              }
                            >
                              {disabled ? t("Enable") : t("Disable")}
                            </Button>
                            {person.two_factor_enabled ? (
                              <Button
                                size="sm"
                                variant="ghost"
                                icon={<ShieldCheck />}
                                disabled={busyID === person.id}
                                onClick={() => setResetTarget(person)}
                              >
                                {t("Reset 2FA")}
                              </Button>
                            ) : null}
                          </div>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )
      ) : null}

      <p className="tiny muted" style={{ marginTop: 12 }}>
        {t("The last active admin cannot be demoted or disabled.")}
      </p>

      <ConfirmDialog
        open={resetTarget !== null}
        title={t("Reset two-factor authentication?")}
        body={t(
          "This removes the secret and every recovery code for {name} and signs them out everywhere. They can enroll again at the next sign-in.",
          { name: resetTarget?.name ?? "" },
        )}
        confirmLabel={t("Reset 2FA")}
        busy={resetBusy}
        onConfirm={() => void resetTwoFactor()}
        onClose={() => setResetTarget(null)}
      />
    </>
  );
}

export function InvitationsScreen() {
  const { t, formatDate } = useI18n();
  const { user } = useApp();
  const { notify } = useToast();
  const state = useLoader(() => api<{ invites: Invite[] }>("/api/invites"), []);

  const [email, setEmail] = useState("");
  const [role, setRole] = useState("member");
  const [guestDays, setGuestDays] = useState(30);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [createdLink, setCreatedLink] = useState("");
  const [emailed, setEmailed] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (!email.includes("@")) {
      setError(t("The email address is not valid."));
      return;
    }
    setBusy(true);
    try {
      const body = await api<{ token: string; accept_url: string }>("/api/invites", {
        method: "POST",
        body: { email: email.trim(), role, guest_days: role === "guest" ? guestDays : 0 },
      });
      setCreatedLink(body.accept_url);
      setEmailed(Boolean(user.smtp_configured));
      setEmail("");
      notify(t("Invitation created."), "good");
      state.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function revoke(invite: Invite) {
    try {
      await api(`/api/invites/${invite.id}`, { method: "DELETE" });
      notify(t("Invitation revoked."), "good");
      state.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    }
  }

  const invites = state.data?.invites ?? [];

  return (
    <>
      <PageHead
        title={t("Invitations")}
        lede={t("Access is invitation-only. A link is always available to copy.")}
        actions={
          <Link to="/people" className="btn btn--ghost">
            {t("People")}
          </Link>
        }
      />

      <div className="admin-grid">
        <form className="card card-pad form" onSubmit={submit}>
          <h2 className="card-title">{t("Invite someone")}</h2>
          {error ? <InlineError message={error} /> : null}
          <Field label={t("Invite email")} required htmlFor="invite-email">
            <Input
              id="invite-email"
              type="email"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
            />
          </Field>
          <div className="form-grid">
            <Field label={t("Role")} htmlFor="invite-role">
              <Select id="invite-role" value={role} onChange={(event) => setRole(event.target.value)}>
                <option value="member">{t("Member")}</option>
                <option value="manager">{t("Manager")}</option>
                <option value="admin">{t("Admin role")}</option>
                <option value="guest">{t("Guest")}</option>
              </Select>
            </Field>
            {role === "guest" ? (
              <Field label={t("Guest duration")} htmlFor="invite-days">
                <Select id="invite-days" value={guestDays} onChange={(event) => setGuestDays(Number(event.target.value))}>
                  <option value={7}>{t("7 days")}</option>
                  <option value={30}>{t("30 days")}</option>
                  <option value={90}>{t("90 days")}</option>
                </Select>
              </Field>
            ) : null}
          </div>
          <Button type="submit" variant="primary" icon={<Send />} disabled={busy}>
            {t("Send invitation")}
          </Button>
        </form>

        <div className="card">
          <div className="card-head">
            <div>
              <h2 className="card-title">{t("Pending invitations")}</h2>
              <p className="card-sub">{t("Copy the link now: it is not shown again after another invitation is created.")}</p>
            </div>
            <Badge tone="mono">{invites.length}</Badge>
          </div>
          <div className="card-body stack-sm">
            {state.loading ? <Loading /> : null}
            {state.error ? <ErrorState message={state.error} onRetry={state.reload} /> : null}
            {!state.loading && invites.length === 0 ? (
              <EmptyState icon={<Mail />} title={t("No pending invitations.")} />
            ) : (
              <div className="list-rows">
                {invites.map((invite) => (
                  <div key={invite.id} className="list-row">
                    <div className="list-main">
                      <div className="list-name">{invite.email}</div>
                      <div className="list-sub">
                        {roleLabel(invite.role, t)} · {t("Expires {date}", { date: formatDate(invite.expires_at) })}
                        {invite.guest_days ? ` · ${invite.guest_days} ${t("days")}` : ""}
                      </div>
                    </div>
                    <Button size="sm" variant="ghost" icon={<X />} onClick={() => revoke(invite)}>
                      {t("Revoke")}
                    </Button>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>

      {createdLink ? (
        <section className="section">
          <div className="card card-pad stack-sm">
            <div className="spread">
              <h2 className="card-title">{t("Invite link")}</h2>
              <Badge tone={emailed ? "good" : "warn"}>
                {emailed ? t("The invitation email was sent.") : t("SMTP is not configured. Copy the link and send it yourself.")}
              </Badge>
            </div>
            <CopyField value={createdLink} label={t("Invite link")} />
            <div className="cluster">
              <Button
                variant="secondary"
                size="sm"
                icon={<Copy />}
                onClick={async () => {
                  try {
                    await navigator.clipboard.writeText(createdLink);
                    notify(t("Link copied."), "good");
                  } catch {
                    notify(t("Copied"), "good");
                  }
                }}
              >
                {t("Copy link")}
              </Button>
              <Button variant="ghost" size="sm" icon={<Check />} onClick={() => setCreatedLink("")}>
                {t("Close")}
              </Button>
            </div>
          </div>
        </section>
      ) : null}
    </>
  );
}
