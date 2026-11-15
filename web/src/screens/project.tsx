import { useState } from "react";
import { CalendarDays, FolderKanban, ListChecks, Plus, Search, Settings } from "lucide-react";
import { api, queryString } from "../api";
import { useI18n } from "../i18n";
import { Link, useParams } from "../router";
import { canWrite, useApp, useLoader } from "../state";
import type { Item, Person, Project, Workflow } from "../types";
import { ItemTable } from "../components/items";
import {
  Badge,
  Button,
  EmptyState,
  ErrorState,
  Field,
  Input,
  Loading,
  PageHead,
  Select,
} from "../components/ui";

export function ProjectScreen() {
  const { t } = useI18n();
  const { id } = useParams();
  const projectID = id;
  const { user } = useApp();

  const [query, setQuery] = useState("");
  const [type, setType] = useState("");
  const [stateID, setStateID] = useState("");
  const [priority, setPriority] = useState("");
  const [assigneeID, setAssigneeID] = useState("");

  const projectState = useLoader(() => api<Project>(`/api/projects/${projectID}`), [projectID]);
  const workflowState = useLoader(() => api<Workflow>("/api/workflow"), []);
  const peopleState = useLoader(
    () => api<{ people: Person[] }>(`/api/projects/${projectID}/people`),
    [projectID],
  );

  const filter = queryString({
    project_id: projectID,
    q: query,
    type,
    state_id: stateID,
    priority,
    assignee_id: assigneeID,
    limit: 500,
  });
  const itemsState = useLoader(() => api<{ items: Item[] }>(`/api/items${filter}`), [filter]);

  if (projectState.loading) {
    return <Loading />;
  }
  if (projectState.error || !projectState.data) {
    return (
      <>
        <PageHead title={t("Project not found")} />
        <div className="card">
          <EmptyState icon={<FolderKanban />} title={t("Project not found")}>
            {t("You do not have access to this project.")}
          </EmptyState>
        </div>
        <Link to="/projects" className="btn btn--secondary" style={{ marginTop: 16 }}>
          {t("All projects")}
        </Link>
      </>
    );
  }

  const project = projectState.data;
  const archived = Boolean(project.archived_at);
  const writable = canWrite(user) && !archived;
  const canManage = user.role === "admin" || user.role === "manager";
  const items = itemsState.data?.items ?? [];
  const hasFilters = Boolean(query || type || stateID || priority || assigneeID);

  return (
    <>
      <PageHead
        title={project.name}
        lede={project.description || t("No description.")}
        actions={
          <>
            {writable && canWrite(user) ? (
              <Link to={`/projects/${project.id}/items/new`} className="btn btn--primary">
                <Plus /> {t("New item")}
              </Link>
            ) : null}
            <Link to={`/projects/${project.id}/calendar`} className="btn btn--secondary">
              <CalendarDays /> {t("Calendar")}
            </Link>
            {canManage ? (
              <Link to={`/projects/${project.id}/settings`} className="btn btn--ghost">
                <Settings /> {t("Settings")}
              </Link>
            ) : null}
          </>
        }
      />

      <div className="cluster" style={{ marginBottom: 16 }}>
        <Badge tone={project.visibility === "private" ? "violet" : "neutral"}>
          {project.visibility === "private" ? t("Private project") : t("Open project")}
        </Badge>
        {archived ? <Badge tone="warn">{t("Archived")}</Badge> : null}
        <span className="tiny muted">{t("Created by {name}", { name: project.created_by_name })}</span>
      </div>

      {archived ? (
        <div className="banner banner--warn" style={{ marginBottom: 20 }}>
          <FolderKanban />
          <span>{t("This project is archived. Work items are read-only.")}</span>
        </div>
      ) : null}

      <div className="toolbar">
        <Field label={t("Search this project")} className="field--grow">
          <div className="search-field">
            <Search />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("Search…")}
              type="search"
            />
          </div>
        </Field>
        <Field label={t("Type")}>
          <Select value={type} onChange={(event) => setType(event.target.value)}>
            <option value="">{t("All types")}</option>
            <option value="observer">{t("Observer")}</option>
            <option value="action">{t("Action")}</option>
            <option value="micro">{t("Micro")}</option>
            <option value="bug">{t("Bug")}</option>
          </Select>
        </Field>
        <Field label={t("State")}>
          <Select value={stateID} onChange={(event) => setStateID(event.target.value)}>
            <option value="">{t("All states")}</option>
            {(workflowState.data?.states ?? []).map((state) => (
              <option key={state.id} value={state.id}>
                {state.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("Priority")}>
          <Select value={priority} onChange={(event) => setPriority(event.target.value)}>
            <option value="">{t("All priorities")}</option>
            <option value="urgent">{t("Urgent")}</option>
            <option value="high">{t("High")}</option>
            <option value="medium">{t("Medium")}</option>
            <option value="low">{t("Low")}</option>
            <option value="none">{t("None")}</option>
          </Select>
        </Field>
        <Field label={t("Assignee")}>
          <Select value={assigneeID} onChange={(event) => setAssigneeID(event.target.value)}>
            <option value="">{t("Anyone")}</option>
            {(peopleState.data?.people ?? []).map((person) => (
              <option key={person.id} value={person.id}>
                {person.name}
              </option>
            ))}
          </Select>
        </Field>
        {hasFilters ? (
          <Button
            variant="ghost"
            onClick={() => {
              setQuery("");
              setType("");
              setStateID("");
              setPriority("");
              setAssigneeID("");
            }}
          >
            {t("Clear filters")}
          </Button>
        ) : null}
      </div>

      {itemsState.loading ? <Loading /> : null}
      {itemsState.error ? <ErrorState message={itemsState.error} onRetry={itemsState.reload} /> : null}

      {!itemsState.loading && !itemsState.error ? (
        items.length === 0 ? (
          <div className="card">
            <EmptyState icon={<ListChecks />} title={t("No items in this project")}>
              {t("Create the first item to start tracking work.")}
            </EmptyState>
            {writable ? (
              <div style={{ display: "flex", justifyContent: "center", paddingBottom: 32 }}>
                <Link to={`/projects/${project.id}/items/new`} className="btn btn--primary">
                  <Plus /> {t("New item")}
                </Link>
              </div>
            ) : null}
          </div>
        ) : (
          <ItemTable items={items} />
        )
      ) : null}
    </>
  );
}
