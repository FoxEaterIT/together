import { useState } from "react";
import { BarChart3, Users } from "lucide-react";
import { api, queryString } from "../api";
import { useI18n } from "../i18n";
import { useApp, useLoader } from "../state";
import type { Report } from "../types";
import { Badge, EmptyState, ErrorState, Field, Loading, PageHead, Select } from "../components/ui";

export function ReportsScreen() {
  const { t } = useI18n();
  const { projects } = useApp();
  const [projectID, setProjectID] = useState("");

  const state = useLoader(
    () => api<Report>(`/api/reports${queryString({ project_id: projectID })}`),
    [projectID],
  );

  const states = state.data?.states ?? [];
  const workload = state.data?.workload ?? [];
  const openTotal = state.data?.totals.open ?? 0;
  const allTotal = state.data?.totals.all ?? 0;
  const maxState = Math.max(1, ...states.map((entry) => entry.count));
  const maxWorkload = Math.max(1, ...workload.map((entry) => entry.open_count));

  return (
    <>
      <PageHead title={t("Reports")} lede={t("Counts by state and open workload by person.")} />

      <div className="toolbar">
        <Field label={t("Filter by project")} className="field--grow">
          <Select value={projectID} onChange={(event) => setProjectID(event.target.value)}>
            <option value="">{t("All projects")}</option>
            {projects.map((project) => (
              <option key={project.id} value={project.id}>
                {project.name}
              </option>
            ))}
          </Select>
        </Field>
        <Badge tone="accent">
          {t("Total open")}: {openTotal}
        </Badge>
      </div>

      {state.loading ? <Loading /> : null}
      {state.error ? <ErrorState message={state.error} onRetry={state.reload} /> : null}

      {!state.loading && !state.error ? (
        allTotal === 0 ? (
          <div className="card">
            <EmptyState icon={<BarChart3 />} title={t("No data to report.")} />
          </div>
        ) : (
          <div className="report-grid">
            <div className="card">
              <div className="card-head">
                <div>
                  <h2 className="card-title">{t("State counts")}</h2>
                  <p className="card-sub">{t("Counts by state")}</p>
                </div>
              </div>
              <div className="card-body">
                <div className="bar-list">
                  {states.map((entry) => (
                    <div key={entry.state_id} className="bar-row">
                      <span className="bar-label">
                        <span className="grow" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                          {entry.state_name}
                        </span>
                        <Badge tone={entry.state_category === "done" ? "good" : "neutral"}>
                          {entry.state_category === "done" ? t("Done") : t("Open")}
                        </Badge>
                      </span>
                      <span className="bar-track">
                        <span
                          className={`bar-fill ${entry.state_category === "done" ? "bar-fill--done" : ""}`}
                          style={{ width: `${Math.round((entry.count / maxState) * 100)}%` }}
                        />
                      </span>
                      <span className="bar-value">{entry.count}</span>
                    </div>
                  ))}
                </div>
              </div>
            </div>

            <div className="card">
              <div className="card-head">
                <div>
                  <h2 className="card-title">{t("Workload")}</h2>
                  <p className="card-sub">{t("Open items per person")}</p>
                </div>
                <Users />
              </div>
              <div className="card-body stack-sm">
                {workload.length === 0 ? (
                  <p className="small muted">{t("No data to report.")}</p>
                ) : (
                  workload.map((entry) => (
                    <div key={entry.user_id} className="workload-row">
                      <span className="workload-name">
                        {entry.name}
                        {entry.expired ? (
                          <Badge tone="warn" className="badge--mono">
                            {t("Expired")}
                          </Badge>
                        ) : null}
                      </span>
                      <span className="bar-track grow">
                        <span
                          className="bar-fill"
                          style={{ width: `${Math.round((entry.open_count / maxWorkload) * 100)}%` }}
                        />
                      </span>
                      <span className="workload-count">{entry.open_count}</span>
                    </div>
                  ))
                )}
                <div className="divider" />
                <p className="tiny muted">{t("This is a workload view, not a performance measure.")}</p>
                <p className="tiny muted">{t("An item with several assignees counts for each person.")}</p>
              </div>
            </div>
          </div>
        )
      ) : null}
    </>
  );
}
