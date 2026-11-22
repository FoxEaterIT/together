import { useMemo, useState } from "react";
import { CalendarDays, FolderKanban, ListChecks, Search, X } from "lucide-react";
import { api } from "../api";
import { useI18n } from "../i18n";
import { Link } from "../router";
import { useApp, useLoader } from "../state";
import type { Item, WorkflowStateRef } from "../types";
import { ItemTable } from "../components/items";
import {
  Button,
  EmptyState,
  ErrorState,
  Field,
  Input,
  Loading,
  PageHead,
  Select,
} from "../components/ui";

type DueBucket = "all" | "overdue" | "today" | "week" | "none";

export function MyWorkScreen() {
  const { t } = useI18n();
  const { user, projects } = useApp();
  const [query, setQuery] = useState("");
  const [stateName, setStateName] = useState("all");
  const [priority, setPriority] = useState("all");
  const [projectID, setProjectID] = useState("all");
  const [due, setDue] = useState<DueBucket>("all");

  const itemsState = useLoader(
    () => api<{ items: Item[] }>("/api/items?assignee_id=me&limit=500"),
    [user.id],
  );
  const workflowState = useLoader(() => api<{ states: WorkflowStateRef[] }>("/api/workflow/states"), []);
  const stateNames = useMemo(() => {
    const names = new Set<string>();
    for (const state of workflowState.data?.states ?? []) {
      names.add(state.name);
    }
    return Array.from(names).sort((a, b) => a.localeCompare(b));
  }, [workflowState.data]);

  const all = itemsState.data?.items ?? [];

  const filtered = useMemo(() => {
    const text = query.trim().toLowerCase();
    const today = todayISO();
    const week = addDaysISO(today, 7);
    return all.filter((item) => {
      if (text && !`${item.title} ${item.description} ${item.project_name}`.toLowerCase().includes(text)) {
        return false;
      }
      if (stateName !== "all" && item.state_name !== stateName) {
        return false;
      }
      if (priority !== "all" && item.priority !== priority) {
        return false;
      }
      if (projectID !== "all" && String(item.project_id) !== projectID) {
        return false;
      }
      switch (due) {
        case "overdue":
          return Boolean(item.due_on) && item.due_on < today && item.state_category === "open";
        case "today":
          return item.due_on === today;
        case "week":
          return Boolean(item.due_on) && item.due_on >= today && item.due_on <= week;
        case "none":
          return !item.due_on;
        default:
          return true;
      }
    });
  }, [all, query, stateName, priority, projectID, due]);

  const open = all.filter((item) => item.state_category === "open");
  const overdue = open.filter((item) => item.due_on && item.due_on < todayISO());
  const thisWeek = open.filter(
    (item) => item.due_on && item.due_on >= todayISO() && item.due_on <= addDaysISO(todayISO(), 7),
  );

  const hasFilters = query !== "" || stateName !== "all" || priority !== "all" || projectID !== "all" || due !== "all";

  function clearFilters() {
    setQuery("");
    setStateName("all");
    setPriority("all");
    setProjectID("all");
    setDue("all");
  }

  return (
    <>
      <PageHead
        title={t("My work")}
        lede={t("Everything assigned to you across the projects you can read.")}
        actions={
          <>
            <Link to="/calendar" className="btn btn--secondary">
              <CalendarDays /> {t("Calendar")}
            </Link>
            <Link to="/projects" className="btn btn--primary">
              <FolderKanban /> {t("Projects")}
            </Link>
          </>
        }
      />

      <div className="stat-strip">
        <div className="stat">
          <div className="stat-label">{t("Open items")}</div>
          <div className="stat-value">{open.length}</div>
        </div>
        <div className={`stat ${overdue.length ? "stat--bad" : ""}`}>
          <div className="stat-label">{t("Overdue items")}</div>
          <div className="stat-value">{overdue.length}</div>
        </div>
        <div className={`stat ${thisWeek.length ? "stat--warn" : ""}`}>
          <div className="stat-label">{t("Due this week")}</div>
          <div className="stat-value">{thisWeek.length}</div>
        </div>
      </div>

      <div className="toolbar" role="search" aria-label={t("Filter your work")}>
        <Field label={t("Search your work")} className="field--grow">
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
        <Field label={t("State")}>
          <Select value={stateName} onChange={(event) => setStateName(event.target.value)}>
            <option value="all">{t("All states")}</option>
            {stateNames.map((name) => (
              <option key={name} value={name}>
                {name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("Priority")}>
          <Select value={priority} onChange={(event) => setPriority(event.target.value)}>
            <option value="all">{t("All priorities")}</option>
            <option value="urgent">{t("Urgent")}</option>
            <option value="high">{t("High")}</option>
            <option value="medium">{t("Medium")}</option>
            <option value="low">{t("Low")}</option>
            <option value="none">{t("None")}</option>
          </Select>
        </Field>
        <Field label={t("Project")}>
          <Select value={projectID} onChange={(event) => setProjectID(event.target.value)}>
            <option value="all">{t("All projects")}</option>
            {projects.map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </Select>
        </Field>
        <Field label={t("Due date")}>
          <Select value={due} onChange={(event) => setDue(event.target.value as DueBucket)}>
            <option value="all">{t("Any")}</option>
            <option value="overdue">{t("Overdue")}</option>
            <option value="today">{t("Due today")}</option>
            <option value="week">{t("Next 7 days")}</option>
            <option value="none">{t("No due date")}</option>
          </Select>
        </Field>
        {hasFilters ? (
          <Button variant="ghost" icon={<X />} onClick={clearFilters}>
            {t("Clear filters")}
          </Button>
        ) : null}
      </div>

      {itemsState.loading ? <Loading /> : null}
      {itemsState.error ? <ErrorState message={itemsState.error} onRetry={itemsState.reload} /> : null}

      {!itemsState.loading && !itemsState.error ? (
        all.length === 0 ? (
          <div className="card">
            <EmptyState icon={<ListChecks />} title={t("No work assigned")}>
              {t("When someone assigns you an item it appears here.")}
            </EmptyState>
          </div>
        ) : filtered.length === 0 ? (
          <div className="card">
            <EmptyState icon={<Search />} title={t("No results")}>
              {t("Try a different word or clear the filters.")}
            </EmptyState>
          </div>
        ) : (
          <ItemTable items={filtered} showProject />
        )
      ) : null}
    </>
  );
}

export function todayISO() {
  const now = new Date();
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}-${String(now.getDate()).padStart(2, "0")}`;
}

export function addDaysISO(date: string, days: number) {
  const value = new Date(`${date}T00:00:00`);
  value.setDate(value.getDate() + days);
  return `${value.getFullYear()}-${String(value.getMonth() + 1).padStart(2, "0")}-${String(value.getDate()).padStart(2, "0")}`;
}
