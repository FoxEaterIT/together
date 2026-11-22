import { useEffect, useMemo, useState, type FormEvent } from "react";
import {
  Archive,
  FolderKanban,
  Plus,
  Search,
  Settings,
  ShieldCheck,
  Users,
  X,
} from "lucide-react";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { Link, useParams, useRouter } from "../router";
import { canCreateProjects, useApp, useLoader } from "../state";
import type { Person, Project, ProjectMember, ProjectTeamLink, Team } from "../types";
import { ProjectWorkflowsSection } from "./workflow";
import {
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
  Textarea,
  useToast,
} from "../components/ui";

export function ProjectsScreen() {
  const { t } = useI18n();
  const { user } = useApp();
  const [query, setQuery] = useState("");
  const [visibility, setVisibility] = useState("");
  const [archived, setArchived] = useState(false);
  const canSeeArchived = user.role === "admin" || user.role === "manager";

  const state = useLoader(
    () =>
      api<{ projects: Project[] }>(
        `/api/projects?q=${encodeURIComponent(query)}&visibility=${visibility}${archived ? "&archived=1" : ""}`,
      ),
    [query, visibility, archived],
  );

  const projects = state.data?.projects ?? [];

  return (
    <>
      <PageHead
        title={t("Projects")}
        lede={t("Open and private projects you can read.")}
        actions={
          canCreateProjects(user) ? (
            <Link to="/projects/new" className="btn btn--primary">
              <Plus /> {t("New project")}
            </Link>
          ) : null
        }
      />

      <div className="toolbar">
        <Field label={t("Search")} className="field--grow">
          <div className="search-field">
            <Search />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("Project name")}
              type="search"
            />
          </div>
        </Field>
        <Field label={t("Visibility")}>
          <Select value={visibility} onChange={(event) => setVisibility(event.target.value)}>
            <option value="">{t("All")}</option>
            <option value="open">{t("Open project")}</option>
            <option value="private">{t("Private project")}</option>
          </Select>
        </Field>
        {canSeeArchived ? (
          <label className="checkbox" style={{ paddingBottom: 9 }}>
            <input type="checkbox" checked={archived} onChange={(event) => setArchived(event.target.checked)} />
            <span>{t("Show archived")}</span>
          </label>
        ) : null}
      </div>

      {state.loading ? <Loading /> : null}
      {state.error ? <ErrorState message={state.error} onRetry={state.reload} /> : null}

      {!state.loading && !state.error ? (
        projects.length === 0 ? (
          <div className="card">
            <EmptyState icon={<FolderKanban />} title={t("No projects yet")}>
              {t("Create the first project to start tracking work.")}
            </EmptyState>
            {canCreateProjects(user) ? (
              <div style={{ display: "flex", justifyContent: "center", paddingBottom: 32 }}>
                <Link to="/projects/new" className="btn btn--primary">
                  <Plus /> {t("New project")}
                </Link>
              </div>
            ) : null}
          </div>
        ) : (
          <div className="project-grid">
            {projects.map((project) => (
              <Link key={project.id} to={`/projects/${project.id}`} className="project-card">
                <div className="project-card-top">
                  <span className="project-name">{project.name}</span>
                  <span className="cluster" style={{ gap: 6 }}>
                    {project.archived_at ? <Badge tone="warn">{t("Archived")}</Badge> : null}
                    <Badge tone={project.visibility === "private" ? "violet" : "neutral"}>
                      {project.visibility === "private" ? t("Private project") : t("Open project")}
                    </Badge>
                  </span>
                </div>
                <p className="project-desc">
                  {project.description || t("No description.")}
                </p>
                <div className="project-meta">
                  <span>
                    <Users /> {project.member_count} {t("members")}
                  </span>
                  <span>
                    <ShieldCheck /> {project.team_count} {t("teams")}
                  </span>
                </div>
              </Link>
            ))}
          </div>
        )
      ) : null}
    </>
  );
}

export function NewProjectScreen() {
  const { t } = useI18n();
  const { user, refreshProjects } = useApp();
  const { navigate } = useRouter();
  const { notify } = useToast();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [visibility, setVisibility] = useState<"open" | "private">("open");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  if (!canCreateProjects(user)) {
    return (
      <>
        <PageHead title={t("New project")} />
        <div className="banner banner--warn">
          <ShieldCheck />
          <span>{t("Only admins and managers can create projects.")}</span>
        </div>
      </>
    );
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (name.trim() === "") {
      setError(t("Please fill in the required fields."));
      return;
    }
    setBusy(true);
    try {
      const project = await api<Project>("/api/projects", {
        method: "POST",
        body: { name: name.trim(), description: description.trim(), visibility },
      });
      await refreshProjects();
      notify(t("Project created. Add people and teams next."), "good");
      navigate(`/projects/${project.id}/settings`);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHead
        title={t("New project")}
        lede={t("Name the project and decide who can read it.")}
        actions={
          <Link to="/projects" className="btn btn--ghost">
            {t("Cancel")}
          </Link>
        }
      />
      <form className="form card card-pad" style={{ maxWidth: 720 }} onSubmit={submit}>
        {error ? <InlineError message={error} /> : null}
        <Field label={t("Project name")} required htmlFor="project-name">
          <Input
            id="project-name"
            value={name}
            autoFocus
            maxLength={120}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t("Description")} htmlFor="project-description">
          <Textarea
            id="project-description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
        <div className="field">
          <span className="field-label">{t("Visibility")}</span>
          <label className="radio-card">
            <input
              type="radio"
              name="visibility"
              checked={visibility === "open"}
              onChange={() => setVisibility("open")}
            />
            <span>
              <strong>{t("Open project")}</strong>
              <span>{t("Anyone in the workspace can read this project.")}</span>
            </span>
          </label>
          <label className="radio-card">
            <input
              type="radio"
              name="visibility"
              checked={visibility === "private"}
              onChange={() => setVisibility("private")}
            />
            <span>
              <strong>{t("Private project")}</strong>
              <span>{t("Only admins, direct members and assigned teams can read this project.")}</span>
            </span>
          </label>
        </div>
        <div className="cluster">
          <Button type="submit" variant="primary" disabled={busy} icon={<Plus />}>
            {busy ? t("Loading…") : t("Create project")}
          </Button>
          <Link to="/projects" className="btn btn--ghost">
            {t("Cancel")}
          </Link>
        </div>
      </form>
    </>
  );
}

export function ProjectSettingsScreen() {
  const { t } = useI18n();
  const { id } = useParams();
  const projectID = id;
  const { user, refreshProjects } = useApp();
  const { navigate } = useRouter();
  const { notify } = useToast();

  const projectState = useLoader(() => api<Project>(`/api/projects/${projectID}`), [projectID]);
  const project = projectState.data;

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [visibility, setVisibility] = useState<"open" | "private">("open");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [archiveOpen, setArchiveOpen] = useState(false);

  const [members, setMembers] = useState<ProjectMember[] | null>(null);
  const [teams, setTeams] = useState<ProjectTeamLink[] | null>(null);
  const [accessError, setAccessError] = useState("");
  const [memberPick, setMemberPick] = useState("");
  const [teamPick, setTeamPick] = useState("");
  const [accessBusy, setAccessBusy] = useState(false);

  const peopleState = useLoader(() => api<{ people: Person[] }>("/api/people"), []);
  const allTeamsState = useLoader(() => api<{ teams: Team[] }>("/api/teams"), []);

  useEffect(() => {
    if (!project) {
      return;
    }
    setName(project.name);
    setDescription(project.description);
    setVisibility(project.visibility);
  }, [project]);

  useEffect(() => {
    if (!project || (user.role !== "admin" && user.role !== "manager")) {
      return;
    }
    let cancelled = false;
    setAccessError("");
    Promise.all([
      api<{ members: ProjectMember[] }>(`/api/projects/${projectID}/members`),
      api<{ teams: ProjectTeamLink[] }>(`/api/projects/${projectID}/teams`),
    ])
      .then(([memberBody, teamBody]) => {
        if (!cancelled) {
          setMembers(memberBody.members);
          setTeams(teamBody.teams);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setAccessError(errorMessage(err));
          setMembers([]);
          setTeams([]);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [project, projectID, user.role]);

  const availablePeople = useMemo(() => {
    const existing = new Set((members ?? []).map((member) => member.user_id));
    return (peopleState.data?.people ?? []).filter((person) => !existing.has(person.id) && !person.disabled);
  }, [members, peopleState.data]);

  const availableTeams = useMemo(() => {
    const existing = new Set((teams ?? []).map((team) => team.team_id));
    return (allTeamsState.data?.teams ?? []).filter((team) => !existing.has(team.id));
  }, [teams, allTeamsState.data]);

  if (projectState.loading) {
    return <Loading />;
  }
  if (projectState.error || !project) {
    return (
      <>
        <PageHead title={t("Project settings")} />
        <ErrorState message={projectState.error || t("Project not found")} onRetry={projectState.reload} />
      </>
    );
  }

  const archived = Boolean(project.archived_at);

  async function save(event: FormEvent) {
    event.preventDefault();
    if (!project) {
      return;
    }
    setError("");
    setBusy(true);
    try {
      await api<Project>(`/api/projects/${project.id}`, {
        method: "PUT",
        body: { name: name.trim(), description: description.trim(), visibility },
      });
      await refreshProjects();
      notify(t("Project saved."), "good");
      projectState.reload();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function addMember() {
    if (!project || !memberPick) {
      return;
    }
    setAccessBusy(true);
    try {
      await api(`/api/projects/${project.id}/members`, {
        method: "POST",
        body: { user_id: Number(memberPick) },
      });
      setMemberPick("");
      const body = await api<{ members: ProjectMember[] }>(`/api/projects/${project.id}/members`);
      setMembers(body.members);
      notify(t("Member added."), "good");
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setAccessBusy(false);
    }
  }

  async function removeMember(userID: number) {
    if (!project) {
      return;
    }
    setAccessBusy(true);
    try {
      await api(`/api/projects/${project.id}/members/${userID}`, { method: "DELETE" });
      setMembers((current) => (current ?? []).filter((member) => member.user_id !== userID));
      notify(t("Member removed."), "good");
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setAccessBusy(false);
    }
  }

  async function addTeam() {
    if (!project || !teamPick) {
      return;
    }
    setAccessBusy(true);
    try {
      await api(`/api/projects/${project.id}/teams`, {
        method: "POST",
        body: { team_id: Number(teamPick) },
      });
      setTeamPick("");
      const body = await api<{ teams: ProjectTeamLink[] }>(`/api/projects/${project.id}/teams`);
      setTeams(body.teams);
      notify(t("Member added."), "good");
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setAccessBusy(false);
    }
  }

  async function removeTeam(teamID: number) {
    if (!project) {
      return;
    }
    setAccessBusy(true);
    try {
      await api(`/api/projects/${project.id}/teams/${teamID}`, { method: "DELETE" });
      setTeams((current) => (current ?? []).filter((team) => team.team_id !== teamID));
      notify(t("Member removed."), "good");
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setAccessBusy(false);
    }
  }

  async function archive() {
    if (!project) {
      return;
    }
    setBusy(true);
    try {
      await api(`/api/projects/${project.id}`, { method: "DELETE" });
      await refreshProjects();
      notify(t("Project archived."), "good");
      navigate("/projects");
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
      setArchiveOpen(false);
    }
  }

  return (
    <>
      <PageHead
        title={project.name}
        lede={t("Project settings")}
        actions={
          <>
            <Link to={`/projects/${project.id}`} className="btn btn--secondary">
              {t("Items")}
            </Link>
            <Link to={`/projects/${project.id}/calendar`} className="btn btn--ghost">
              {t("Calendar")}
            </Link>
          </>
        }
      />

      {archived ? (
        <div className="banner banner--warn" style={{ marginBottom: 20 }}>
          <Archive />
          <span>{t("This project is archived. Work items are read-only.")}</span>
        </div>
      ) : null}

      {!archived ? (
        <form className="card card-pad form" style={{ maxWidth: 720 }} onSubmit={save}>
          <h2 className="section-title">{t("Project visibility")}</h2>
          {error ? <InlineError message={error} /> : null}
          <Field label={t("Project name")} required htmlFor="settings-name">
            <Input
              id="settings-name"
              value={name}
              maxLength={120}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          <Field label={t("Description")} htmlFor="settings-description">
            <Textarea
              id="settings-description"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
            />
          </Field>
          <div className="field">
            <span className="field-label">{t("Visibility")}</span>
            <label className="radio-card">
              <input
                type="radio"
                name="settings-visibility"
                checked={visibility === "open"}
                onChange={() => setVisibility("open")}
              />
              <span>
                <strong>{t("Open project")}</strong>
                <span>{t("Anyone in the workspace can read this project.")}</span>
              </span>
            </label>
            <label className="radio-card">
              <input
                type="radio"
                name="settings-visibility"
                checked={visibility === "private"}
                onChange={() => setVisibility("private")}
              />
              <span>
                <strong>{t("Private project")}</strong>
                <span>{t("Only admins, direct members and assigned teams can read this project.")}</span>
              </span>
            </label>
          </div>
          <div className="cluster">
            <Button type="submit" variant="primary" disabled={busy} icon={<Settings />}>
              {t("Save project")}
            </Button>
          </div>
        </form>
      ) : null}

      <section className="section">
        <div className="section-head">
          <div className="grow">
            <h2 className="section-title">{t("Members and teams")}</h2>
            <p className="section-lede">{t("Manage access")}</p>
          </div>
        </div>

        {accessError ? (
          <div className="banner banner--info">
            <ShieldCheck />
            <span>{t("Only project managers can change these settings.")}</span>
          </div>
        ) : members === null || teams === null ? (
          <Loading />
        ) : (
          <div className="admin-grid">
            <div className="card">
              <div className="card-head">
                <div>
                  <h3 className="card-title">{t("Direct members")}</h3>
                  <p className="card-sub">{t("Add a person")}</p>
                </div>
              </div>
              <div className="card-body stack-sm">
                {!archived ? (
                  <div className="cluster-lg" style={{ alignItems: "flex-end" }}>
                    <Field label={t("Add a person")} className="grow">
                      <Select
                        value={memberPick}
                        onChange={(event) => setMemberPick(event.target.value)}
                        disabled={accessBusy || availablePeople.length === 0}
                      >
                        <option value="">{availablePeople.length === 0 ? t("No people available") : t("Choose a person")}</option>
                        {availablePeople.map((person) => (
                          <option key={person.id} value={person.id}>
                            {person.name}
                          </option>
                        ))}
                      </Select>
                    </Field>
                    <Button variant="secondary" icon={<Plus />} onClick={addMember} disabled={!memberPick || accessBusy}>
                      {t("Add")}
                    </Button>
                  </div>
                ) : null}
                <div className="list-rows">
                  {members.length === 0 ? (
                    <p className="small muted">{t("No direct members.")}</p>
                  ) : (
                    members.map((member) => (
                      <div key={member.user_id} className="list-row">
                        <div className="list-main">
                          <div className="list-name">{member.name}</div>
                          <div className="list-sub">{member.email}</div>
                        </div>
                        <Badge tone="mono">{member.role}</Badge>
                        {!archived ? (
                          <IconButton
                            label={t("Remove {name}", { name: member.name })}
                            onClick={() => removeMember(member.user_id)}
                            disabled={accessBusy}
                          >
                            <X />
                          </IconButton>
                        ) : null}
                      </div>
                    ))
                  )}
                </div>
              </div>
            </div>

            <div className="card">
              <div className="card-head">
                <div>
                  <h3 className="card-title">{t("Assigned teams")}</h3>
                  <p className="card-sub">{t("Add a team")}</p>
                </div>
              </div>
              <div className="card-body stack-sm">
                {!archived ? (
                  <div className="cluster-lg" style={{ alignItems: "flex-end" }}>
                    <Field label={t("Add a team")} className="grow">
                      <Select
                        value={teamPick}
                        onChange={(event) => setTeamPick(event.target.value)}
                        disabled={accessBusy || availableTeams.length === 0}
                      >
                        <option value="">{availableTeams.length === 0 ? t("No teams") : t("Choose a person")}</option>
                        {availableTeams.map((team) => (
                          <option key={team.id} value={team.id}>
                            {team.name}
                          </option>
                        ))}
                      </Select>
                    </Field>
                    <Button variant="secondary" icon={<Plus />} onClick={addTeam} disabled={!teamPick || accessBusy}>
                      {t("Add")}
                    </Button>
                  </div>
                ) : null}
                <div className="list-rows">
                  {teams.length === 0 ? (
                    <p className="small muted">{t("No teams assigned.")}</p>
                  ) : (
                    teams.map((team) => (
                      <div key={team.team_id} className="list-row">
                        <div className="list-main">
                          <div className="list-name">{team.name}</div>
                        </div>
                        {!archived ? (
                          <IconButton
                            label={t("Remove {name}", { name: team.name })}
                            onClick={() => removeTeam(team.team_id)}
                            disabled={accessBusy}
                          >
                            <X />
                          </IconButton>
                        ) : null}
                      </div>
                    ))
                  )}
                </div>
              </div>
            </div>
          </div>
        )}
      </section>

      <ProjectWorkflowsSection projectID={project.id} />

      {!archived ? (
        <section className="section">
          <div className="danger-zone">
            <div className="card-head">
              <div>
                <h2 className="card-title">{t("Danger zone")}</h2>
                <p className="card-sub">{t("Archive project")}</p>
              </div>
              <Button variant="danger-ghost" icon={<Archive />} onClick={() => setArchiveOpen(true)}>
                {t("Archive project")}
              </Button>
            </div>
            <div className="card-body">
              <p className="small muted">
                {t("Archiving hides the project and its items from normal views. Nothing is deleted from the database.")}
              </p>
            </div>
          </div>
        </section>
      ) : null}

      <ConfirmDialog
        open={archiveOpen}
        title={t("Archive this project?")}
        body={t("The project and its items will disappear from normal views. You can still find it with Show archived. Nothing is deleted.")}
        confirmLabel={t("Archive project")}
        busy={busy}
        onConfirm={archive}
        onClose={() => setArchiveOpen(false)}
      />
    </>
  );
}
