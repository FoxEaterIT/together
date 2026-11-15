import { useMemo, useState, type FormEvent } from "react";
import { Archive, Plus, ShieldCheck, Users, X } from "lucide-react";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { Link, useParams, useRouter } from "../router";
import { useLoader } from "../state";
import type { Person, Team, TeamMember } from "../types";
import {
  Avatar,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  ErrorState,
  Field,
  IconButton,
  InlineError,
  Input,
  Loading,
  PageHead,
  Select,
  useToast,
} from "../components/ui";

export function TeamsScreen() {
  const { t } = useI18n();
  const { notify } = useToast();
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [archiveID, setArchiveID] = useState<number | null>(null);
  const state = useLoader(() => api<{ teams: Team[] }>("/api/teams"), []);

  const teams = state.data?.teams ?? [];

  async function create(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (name.trim() === "") {
      setError(t("Please fill in the required fields."));
      return;
    }
    setBusy(true);
    try {
      await api<Team>("/api/teams", { method: "POST", body: { name: name.trim() } });
      setName("");
      notify(t("Team created."), "good");
      state.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function archive(id: number) {
    setBusy(true);
    try {
      await api(`/api/teams/${id}`, { method: "DELETE" });
      notify(t("Team archived."), "good");
      setArchiveID(null);
      state.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHead title={t("Teams")} lede={t("Reusable groups for project access.")} />

      <div className="admin-grid">
        <form className="card card-pad form" onSubmit={create}>
          <h2 className="card-title">{t("New team")}</h2>
          {error ? <InlineError message={error} /> : null}
          <Field label={t("Team name")} required htmlFor="team-name">
            <Input id="team-name" value={name} onChange={(event) => setName(event.target.value)} />
          </Field>
          <Button type="submit" variant="primary" icon={<Plus />} disabled={busy}>
            {t("Create team")}
          </Button>
        </form>

        <div className="card">
          <div className="card-head">
            <div>
              <h2 className="card-title">{t("Teams")}</h2>
              <p className="card-sub">{t("Archiving a team does not remove people from the installation.")}</p>
            </div>
            <Badge tone="mono">{teams.length}</Badge>
          </div>
          <div className="card-body">
            {state.loading ? <Loading /> : null}
            {state.error ? <ErrorState message={state.error} onRetry={state.reload} /> : null}
            {!state.loading && teams.length === 0 ? (
              <EmptyState icon={<ShieldCheck />} title={t("No teams yet.")} />
            ) : (
              <div className="list-rows">
                {teams.map((team) => (
                  <div key={team.id} className="list-row">
                    <div className="list-main">
                      <Link to={`/teams/${team.id}`} className="list-name">
                        {team.name}
                      </Link>
                      <div className="list-sub">{new Date(team.created_at).toLocaleDateString()}</div>
                    </div>
                    <div className="list-actions">
                      <Link to={`/teams/${team.id}`} className="btn btn--secondary btn--sm">
                        <Users /> {t("Team members")}
                      </Link>
                      <Button size="sm" variant="danger-ghost" icon={<Archive />} onClick={() => setArchiveID(team.id)}>
                        {t("Archive team")}
                      </Button>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </div>

      <ConfirmDialog
        open={archiveID !== null}
        title={t("Archive this team?")}
        body={t("Archiving a team does not remove people from the installation. It stops granting access through this team until it is removed from a project.")}
        confirmLabel={t("Archive team")}
        busy={busy}
        onConfirm={() => archiveID !== null && archive(archiveID)}
        onClose={() => setArchiveID(null)}
      />
    </>
  );
}

export function TeamScreen() {
  const { t } = useI18n();
  const { id } = useParams();
  const teamID = id;
  const { navigate } = useRouter();
  const { notify } = useToast();
  const [pick, setPick] = useState("");
  const [busy, setBusy] = useState(false);
  const [archiveOpen, setArchiveOpen] = useState(false);

  const teamsState = useLoader(() => api<{ teams: Team[] }>("/api/teams"), []);
  const membersState = useLoader(
    () => api<{ members: TeamMember[] }>(`/api/teams/${teamID}/members`),
    [teamID],
  );
  const peopleState = useLoader(() => api<{ people: Person[] }>("/api/people"), []);

  const team = teamsState.data?.teams.find((entry) => String(entry.id) === teamID) ?? null;
  const members = membersState.data?.members ?? [];

  const available = useMemo(() => {
    const existing = new Set(members.map((member) => member.user_id));
    return (peopleState.data?.people ?? []).filter((person) => !existing.has(person.id) && !person.disabled);
  }, [members, peopleState.data]);

  if (teamsState.loading) {
    return <Loading />;
  }
  if (!team) {
    return (
      <>
        <PageHead title={t("Team")} />
        <ErrorState message={t("Not found.")} onRetry={teamsState.reload} />
        <Link to="/teams" className="btn btn--secondary" style={{ marginTop: 16 }}>
          {t("Back to teams")}
        </Link>
      </>
    );
  }

  async function addMember() {
    if (!pick) {
      return;
    }
    setBusy(true);
    try {
      await api(`/api/teams/${teamID}/members`, { method: "POST", body: { user_id: Number(pick) } });
      setPick("");
      notify(t("Member added."), "good");
      membersState.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setBusy(false);
    }
  }

  async function removeMember(userID: number) {
    setBusy(true);
    try {
      await api(`/api/teams/${teamID}/members/${userID}`, { method: "DELETE" });
      notify(t("Member removed."), "good");
      membersState.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setBusy(false);
    }
  }

  async function archive() {
    setBusy(true);
    try {
      await api(`/api/teams/${teamID}`, { method: "DELETE" });
      notify(t("Team archived."), "good");
      navigate("/teams");
    } catch (err) {
      notify(errorMessage(err), "bad");
      setBusy(false);
      setArchiveOpen(false);
    }
  }

  return (
    <>
      <PageHead
        title={team.name}
        lede={t("People in this team")}
        actions={
          <>
            <Link to="/teams" className="btn btn--ghost">
              {t("Back to teams")}
            </Link>
            <Button variant="danger-ghost" icon={<Archive />} onClick={() => setArchiveOpen(true)}>
              {t("Archive team")}
            </Button>
          </>
        }
      />

      <div className="card" style={{ maxWidth: 720 }}>
        <div className="card-head">
          <div>
            <h2 className="card-title">{t("Team members")}</h2>
            <p className="card-sub">{t("Add member")}</p>
          </div>
          <Badge tone="mono">{members.length}</Badge>
        </div>
        <div className="card-body stack-sm">
          <div className="cluster-lg" style={{ alignItems: "flex-end" }}>
            <Field label={t("Add member")} className="grow">
              <Select value={pick} onChange={(event) => setPick(event.target.value)} disabled={available.length === 0 || busy}>
                <option value="">{available.length === 0 ? t("No people available") : t("Choose a person")}</option>
                {available.map((person) => (
                  <option key={person.id} value={person.id}>
                    {person.name}
                  </option>
                ))}
              </Select>
            </Field>
            <Button variant="secondary" icon={<Plus />} onClick={addMember} disabled={!pick || busy}>
              {t("Add")}
            </Button>
          </div>

          {membersState.loading ? <Loading /> : null}
          {membersState.error ? <ErrorState message={membersState.error} onRetry={membersState.reload} /> : null}
          {members.length === 0 ? (
            <p className="small muted">{t("No members in this team.")}</p>
          ) : (
            <div className="list-rows">
              {members.map((member) => (
                <div key={member.user_id} className="list-row">
                  <Avatar name={member.name} size="sm" />
                  <div className="list-main">
                    <div className="list-name">{member.name}</div>
                    <div className="list-sub">{member.email}</div>
                  </div>
                  <IconButton
                    label={t("Remove {name}", { name: member.name })}
                    onClick={() => removeMember(member.user_id)}
                    disabled={busy}
                  >
                    <X />
                  </IconButton>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      <ConfirmDialog
        open={archiveOpen}
        title={t("Archive this team?")}
        body={t("Archiving a team does not remove people from the installation. It stops granting access through this team until it is removed from a project.")}
        confirmLabel={t("Archive team")}
        busy={busy}
        onConfirm={archive}
        onClose={() => setArchiveOpen(false)}
      />
    </>
  );
}
