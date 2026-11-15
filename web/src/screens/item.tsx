import { useEffect, useState, type FormEvent } from "react";
import {
  Archive,
  ArrowLeft,
  CornerDownRight,
  Eye,
  Loader2,
  MessageSquare,
  Pencil,
  Plus,
  Send,
  UserPlus,
} from "lucide-react";
import { api, ApiError, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { Link, useParams, useRouter } from "../router";
import { canWrite, useApp, useLoader } from "../state";
import type {
  Comment,
  Item,
  ItemDetail,
  ItemEvent,
  ItemType,
  Person,
  Priority,
  Project,
  Workflow,
} from "../types";
import { itemHref, PriorityBadge, StateBadge, TypeIcon, TypeTag, DueLabel } from "../components/items";
import {
  Avatar,
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  ErrorState,
  Field,
  InlineError,
  Input,
  Loading,
  Modal,
  PageHead,
  Select,
  Textarea,
  useToast,
} from "../components/ui";

type ItemFieldsProps = {
  type: ItemType;
  onTypeChange: (type: ItemType) => void;
  parentID: string;
  onParentChange: (id: string) => void;
  title: string;
  onTitleChange: (value: string) => void;
  description: string;
  onDescriptionChange: (value: string) => void;
  priority: Priority;
  onPriorityChange: (value: Priority) => void;
  dueOn: string;
  onDueChange: (value: string) => void;
  assignees: number[];
  onAssigneesChange: (ids: number[]) => void;
  people: Person[];
  candidates: Item[];
  prefix: string;
};

function ItemFields({
  type,
  onTypeChange,
  parentID,
  onParentChange,
  title,
  onTitleChange,
  description,
  onDescriptionChange,
  priority,
  onPriorityChange,
  dueOn,
  onDueChange,
  assignees,
  onAssigneesChange,
  people,
  candidates,
  prefix,
}: ItemFieldsProps) {
  const { t } = useI18n();
  const actions = candidates.filter((candidate) => candidate.type === "action");
  const observers = candidates.filter((candidate) => candidate.type === "observer");
  const parentOptions = type === "micro" ? actions : type === "action" || type === "bug" ? observers : [];

  return (
    <>
      <div className="form-grid">
        <Field label={t("Item type")} htmlFor={`${prefix}-type`}>
          <Select id={`${prefix}-type`} value={type} onChange={(event) => onTypeChange(event.target.value as ItemType)}>
            <option value="observer">{t("Observer")}</option>
            <option value="action">{t("Action")}</option>
            <option value="micro">{t("Micro")}</option>
            <option value="bug">{t("Bug")}</option>
          </Select>
        </Field>
        {type !== "observer" ? (
          <Field
            label={type === "micro" ? t("Required action") : t("Optional observer")}
            htmlFor={`${prefix}-parent`}
            required={type === "micro"}
          >
            <Select
              id={`${prefix}-parent`}
              value={parentID}
              onChange={(event) => onParentChange(event.target.value)}
              disabled={parentOptions.length === 0}
            >
              <option value="">{parentOptions.length === 0 ? t("No parent") : t("No parent")}</option>
              {parentOptions.map((candidate) => (
                <option key={candidate.id} value={candidate.id}>
                  #{candidate.id} {candidate.title}
                </option>
              ))}
            </Select>
          </Field>
        ) : null}
      </div>

      {type === "micro" && parentOptions.length === 0 ? (
        <div className="banner banner--warn">
          <CornerDownRight />
          <span>{t("Micro items must belong to an action in the same project.")}</span>
        </div>
      ) : null}

      <Field label={t("Title")} required htmlFor={`${prefix}-title`}>
        <Input
          id={`${prefix}-title`}
          value={title}
          maxLength={200}
          autoFocus
          onChange={(event) => onTitleChange(event.target.value)}
        />
      </Field>

      <Field label={t("Description")} htmlFor={`${prefix}-description`}>
        <Textarea
          id={`${prefix}-description`}
          value={description}
          onChange={(event) => onDescriptionChange(event.target.value)}
        />
      </Field>

      <div className="form-grid">
        <Field label={t("Priority")} htmlFor={`${prefix}-priority`}>
          <Select
            id={`${prefix}-priority`}
            value={priority}
            onChange={(event) => onPriorityChange(event.target.value as Priority)}
          >
            <option value="none">{t("None")}</option>
            <option value="low">{t("Low")}</option>
            <option value="medium">{t("Medium")}</option>
            <option value="high">{t("High")}</option>
            <option value="urgent">{t("Urgent")}</option>
          </Select>
        </Field>
        <Field label={t("Due date")} htmlFor={`${prefix}-due`}>
          <Input
            id={`${prefix}-due`}
            type="date"
            value={dueOn}
            onChange={(event) => onDueChange(event.target.value)}
          />
        </Field>
      </div>

      <Field label={t("Assignees")}>
        {people.length === 0 ? (
          <p className="small muted">{t("No people available")}</p>
        ) : (
          <div className="assignee-picker">
            {people.map((person) => (
              <Checkbox
                key={person.id}
                label={person.name}
                checked={assignees.includes(person.id)}
                onChange={(event) => {
                  if (event.target.checked) {
                    onAssigneesChange([...assignees, person.id]);
                  } else {
                    onAssigneesChange(assignees.filter((id) => id !== person.id));
                  }
                }}
              />
            ))}
          </div>
        )}
      </Field>
    </>
  );
}

export function NewItemScreen() {
  const { t } = useI18n();
  const { id } = useParams();
  const projectID = id;
  const { user } = useApp();
  const { navigate } = useRouter();
  const { notify } = useToast();

  const [type, setType] = useState<ItemType>("action");
  const [parentID, setParentID] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [priority, setPriority] = useState<Priority>("none");
  const [dueOn, setDueOn] = useState("");
  const [assignees, setAssignees] = useState<number[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const projectState = useLoader(() => api<Project>(`/api/projects/${projectID}`), [projectID]);
  const workflowState = useLoader(() => api<Workflow>("/api/workflow"), []);
  const peopleState = useLoader(
    () => api<{ people: Person[] }>(`/api/projects/${projectID}/people`),
    [projectID],
  );
  const itemsState = useLoader(
    () => api<{ items: Item[] }>(`/api/items?project_id=${projectID}&limit=500`),
    [projectID],
  );

  const initial = workflowState.data?.states.find((state) => state.id === workflowState.data?.initial_state_id);

  if (projectState.loading) {
    return <Loading />;
  }
  if (projectState.error || !projectState.data) {
    return (
      <>
        <PageHead title={t("Project not found")} />
        <ErrorState message={projectState.error || t("Project not found")} onRetry={projectState.reload} />
      </>
    );
  }

  const project = projectState.data;
  if (!canWrite(user) || project.archived_at) {
    return (
      <>
        <PageHead title={t("New item")} lede={project.name} />
        <div className="banner banner--warn">
          <Eye />
          <span>{project.archived_at ? t("This project is archived. Work items are read-only.") : t("Read-only access")}</span>
        </div>
        <Link to={`/projects/${project.id}`} className="btn btn--secondary" style={{ marginTop: 16 }}>
          {t("Back to project")}
        </Link>
      </>
    );
  }

  function changeType(next: ItemType) {
    setType(next);
    if (next === "observer") {
      setParentID("");
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (title.trim() === "") {
      setError(t("Please fill in the required fields."));
      return;
    }
    if (type === "micro" && !parentID) {
      setError(t("Micro items require an action parent."));
      return;
    }
    setBusy(true);
    try {
      const item = await api<Item>(`/api/projects/${project.id}/items`, {
        method: "POST",
        body: {
          type,
          parent_id: parentID ? Number(parentID) : null,
          title: title.trim(),
          description,
          priority,
          due_on: dueOn,
          assignee_ids: assignees,
        },
      });
      notify(t("Item created."), "good");
      navigate(itemHref(item));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <PageHead
        title={t("New item")}
        lede={project.name}
        actions={
          <Link to={`/projects/${project.id}`} className="btn btn--ghost">
            <ArrowLeft /> {t("Back to project")}
          </Link>
        }
      />

      <form className="card card-pad form" style={{ maxWidth: 820 }} onSubmit={submit}>
        {error ? <InlineError message={error} /> : null}
        <ItemFields
          type={type}
          onTypeChange={changeType}
          parentID={parentID}
          onParentChange={setParentID}
          title={title}
          onTitleChange={setTitle}
          description={description}
          onDescriptionChange={setDescription}
          priority={priority}
          onPriorityChange={setPriority}
          dueOn={dueOn}
          onDueChange={setDueOn}
          assignees={assignees}
          onAssigneesChange={setAssignees}
          people={peopleState.data?.people ?? []}
          candidates={itemsState.data?.items ?? []}
          prefix="new"
        />

        <div className="field">
          <span className="field-label">{t("Initial state")}</span>
          {initial ? (
            <div className="cluster">
              <Badge tone={initial.category === "done" ? "good" : "accent"}>{initial.name}</Badge>
              <span className="small muted">{t("The initial state comes from the workflow and cannot be changed here.")}</span>
            </div>
          ) : (
            <div className="banner banner--warn">
              <Eye />
              <span>{t("No initial state is configured. Ask an admin to set one.")}</span>
            </div>
          )}
        </div>

        <div className="cluster">
          <Button type="submit" variant="primary" disabled={busy || !initial} icon={busy ? <Loader2 className="spinner" /> : <Plus />}>
            {busy ? t("Loading…") : t("Create item")}
          </Button>
          <Link to={`/projects/${project.id}`} className="btn btn--ghost">
            {t("Cancel")}
          </Link>
        </div>
      </form>
    </>
  );
}

export function ItemDetailScreen() {
  const { t } = useI18n();
  // The route is /projects/:id/items/:item: the project id must not be used as
  // the item id, or every deep link opens the item whose id matches the project.
  const { item: itemParam } = useParams();
  const itemID = itemParam;
  const { user } = useApp();
  const { navigate } = useRouter();
  const { notify } = useToast();

  const detailState = useLoader(() => api<ItemDetail>(`/api/items/${itemID}`), [itemID]);
  const commentsState = useLoader(() => api<{ comments: Comment[] }>(`/api/items/${itemID}/comments`), [itemID]);
  const eventsState = useLoader(() => api<{ events: ItemEvent[] }>(`/api/items/${itemID}/events`), [itemID]);
  const workflowState = useLoader(() => api<Workflow>("/api/workflow"), []);

  const detail = detailState.data;
  const projectID = detail?.item.project_id;
  const peopleState = useLoader(
    () =>
      projectID
        ? api<{ people: Person[] }>(`/api/projects/${projectID}/people`)
        : Promise.resolve({ people: [] }),
    [projectID],
  );
  const itemsState = useLoader(
    () =>
      projectID
        ? api<{ items: Item[] }>(`/api/items?project_id=${projectID}&limit=500`)
        : Promise.resolve({ items: [] }),
    [projectID],
  );

  const [commentBody, setCommentBody] = useState("");
  const [commentMentions, setCommentMentions] = useState<number[]>([]);
  const [commentBusy, setCommentBusy] = useState(false);
  const [showMentions, setShowMentions] = useState(false);
  const [commentError, setCommentError] = useState("");

  const [editingComment, setEditingComment] = useState<number | null>(null);
  const [editBody, setEditBody] = useState("");
  const [editMentions, setEditMentions] = useState<number[]>([]);
  const [editBusy, setEditBusy] = useState(false);

  const [editOpen, setEditOpen] = useState(false);
  const [archiveOpen, setArchiveOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  const [type, setType] = useState<ItemType>("action");
  const [parentID, setParentID] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [priority, setPriority] = useState<Priority>("none");
  const [dueOn, setDueOn] = useState("");
  const [assignees, setAssignees] = useState<number[]>([]);
  const [editError, setEditError] = useState("");

  useEffect(() => {
    if (!detail) {
      return;
    }
    setType(detail.item.type);
    setParentID(detail.item.parent_id ? String(detail.item.parent_id) : "");
    setTitle(detail.item.title);
    setDescription(detail.item.description);
    setPriority(detail.item.priority);
    setDueOn(detail.item.due_on);
    setAssignees(detail.item.assignees.map((assignee) => assignee.user_id));
  }, [detail]);

  const people = peopleState.data?.people ?? [];

  if (detailState.loading) {
    return <Loading />;
  }
  if (detailState.error || !detail) {
    return (
      <>
        <PageHead title={t("Item not found")} />
        <ErrorState message={detailState.error || t("Item not found")} onRetry={detailState.reload} />
        <Link to="/" className="btn btn--secondary" style={{ marginTop: 16 }}>
          {t("My work")}
        </Link>
      </>
    );
  }

  const item = detail.item;
  const archived = Boolean(item.archived_at);
  const writable = canWrite(user) && !archived;

  async function refreshAll() {
    detailState.reload();
    commentsState.reload();
    eventsState.reload();
  }

  async function transition(transitionID: number) {
    setBusy(true);
    try {
      await api<Item>(`/api/items/${item.id}/transitions/${transitionID}`, { method: "POST" });
      notify(t("State changed."), "good");
      await refreshAll();
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        notify(t("The item changed in the meantime. Reload and try again."), "bad");
      } else {
        notify(errorMessage(err), "bad");
      }
    } finally {
      setBusy(false);
    }
  }

  async function saveItem(event?: FormEvent) {
    event?.preventDefault();
    setEditError("");
    if (title.trim() === "") {
      setEditError(t("Please fill in the required fields."));
      return;
    }
    if (type === "micro" && !parentID) {
      setEditError(t("Micro items require an action parent."));
      return;
    }
    setBusy(true);
    try {
      await api<Item>(`/api/items/${item.id}`, {
        method: "PUT",
        body: {
          type,
          parent_id: parentID ? Number(parentID) : null,
          title: title.trim(),
          description,
          priority,
          due_on: dueOn,
          assignee_ids: assignees,
        },
      });
      notify(t("Item saved."), "good");
      setEditOpen(false);
      await refreshAll();
    } catch (err) {
      setEditError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function archiveItem() {
    setBusy(true);
    try {
      await api(`/api/items/${item.id}`, { method: "DELETE" });
      notify(t("Item archived."), "good");
      navigate(`/projects/${item.project_id}`);
    } catch (err) {
      notify(errorMessage(err), "bad");
      setBusy(false);
      setArchiveOpen(false);
    }
  }

  async function addComment(event: FormEvent) {
    event.preventDefault();
    setCommentError("");
    if (commentBody.trim() === "") {
      setCommentError(t("Please fill in the required fields."));
      return;
    }
    setCommentBusy(true);
    try {
      await api<Comment>(`/api/items/${item.id}/comments`, {
        method: "POST",
        body: { body: commentBody, mentions: commentMentions },
      });
      setCommentBody("");
      setCommentMentions([]);
      setShowMentions(false);
      notify(t("Comment added."), "good");
      commentsState.reload();
      eventsState.reload();
    } catch (err) {
      setCommentError(errorMessage(err));
    } finally {
      setCommentBusy(false);
    }
  }

  function insertMention(person: Person) {
    const token = `@${person.name}`;
    setCommentBody((current) => `${current}${current && !current.endsWith(" ") ? " " : ""}${token} `);
    setCommentMentions((current) => (current.includes(person.id) ? current : [...current, person.id]));
    setShowMentions(false);
  }

  function startEditComment(comment: Comment) {
    setEditingComment(comment.id);
    setEditBody(comment.body);
    setEditMentions(comment.mentions.map((mention) => mention.user_id));
  }

  async function saveComment(commentID: number) {
    setEditBusy(true);
    try {
      await api<Comment>(`/api/comments/${commentID}`, {
        method: "PUT",
        body: { body: editBody, mentions: editMentions },
      });
      setEditingComment(null);
      notify(t("Comment saved."), "good");
      commentsState.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    } finally {
      setEditBusy(false);
    }
  }

  const states = workflowState.data?.states ?? [];

  return (
    <>
      <div className="cluster" style={{ marginBottom: 16 }}>
        <Link to={`/projects/${item.project_id}`} className="btn btn--ghost btn--sm">
          <ArrowLeft /> {item.project_name}
        </Link>
        <span className="tag">#{item.id}</span>
        {archived ? <Badge tone="warn">{t("Archived")}</Badge> : null}
      </div>

      {archived ? (
        <div className="banner banner--warn" style={{ marginBottom: 20 }}>
          <Archive />
          <span>{t("Item is archived.")}</span>
        </div>
      ) : null}

      <div className="detail-grid">
        <div className="detail-main">
          <div className="item-head">
            <div className="item-kicker">
              <TypeTag type={item.type} />
              <span>·</span>
              <span>{t("Created by {name}", { name: item.created_by_name })}</span>
            </div>
            <h1 className="item-title">{item.title}</h1>
            <div className="cluster">
              <StateBadge name={item.state_name} category={item.state_category} />
              <PriorityBadge priority={item.priority} />
              {item.due_on ? <DueLabel date={item.due_on} category={item.state_category} /> : null}
            </div>
          </div>

          <section className="card card-pad">
            <h2 className="card-title" style={{ marginBottom: 10 }}>
              {t("Description")}
            </h2>
            <p className="description">{item.description || t("No description.")}</p>
          </section>

          <section className="card card-pad">
            <div className="spread" style={{ marginBottom: 8 }}>
              <h2 className="card-title">{t("Comments")}</h2>
              <span className="tag">
                <MessageSquare /> {commentsState.data?.comments.length ?? 0}
              </span>
            </div>

            {commentsState.loading ? <Loading /> : null}
            {commentsState.data?.comments.length === 0 ? (
              <p className="small muted">{t("No comments yet.")}</p>
            ) : (
              <div>
                {(commentsState.data?.comments ?? []).map((comment) => (
                  <article key={comment.id} className="comment">
                    <Avatar name={comment.author_name} />
                    <div className="comment-body">
                      <div className="comment-head">
                        <span className="comment-author">{comment.author_name}</span>
                        <span className="comment-time">{new Date(comment.created_at).toLocaleString()}</span>
                        {comment.edited_at ? <span className="tiny muted">{t("Edited {date}", { date: new Date(comment.edited_at).toLocaleString() })}</span> : null}
                      </div>
                      {editingComment === comment.id ? (
                        <div className="comment-editor stack-sm">
                          <Textarea value={editBody} onChange={(event) => setEditBody(event.target.value)} rows={4} />
                          <div className="cluster">
                            <Button size="sm" variant="primary" disabled={editBusy} onClick={() => saveComment(comment.id)}>
                              {t("Save comment")}
                            </Button>
                            <Button size="sm" variant="ghost" onClick={() => setEditingComment(null)}>
                              {t("Cancel")}
                            </Button>
                          </div>
                        </div>
                      ) : (
                        <>
                          <p className="comment-text">{comment.body}</p>
                          {comment.mentions.length > 0 ? (
                            <div className="cluster" style={{ marginTop: 6 }}>
                              {comment.mentions.map((mention) => (
                                <Badge key={mention.user_id} tone="accent">
                                  @{mention.name}
                                </Badge>
                              ))}
                            </div>
                          ) : null}
                          {writable && comment.author_id === user.id ? (
                            <div className="comment-actions">
                              <Button size="sm" variant="ghost" icon={<Pencil />} onClick={() => startEditComment(comment)}>
                                {t("Edit comment")}
                              </Button>
                            </div>
                          ) : null}
                        </>
                      )}
                    </div>
                  </article>
                ))}
              </div>
            )}

            {writable ? (
              <form className="stack-sm" style={{ marginTop: 16 }} onSubmit={addComment}>
                {commentError ? <InlineError message={commentError} /> : null}
                <div style={{ position: "relative" }}>
                  <Textarea
                    value={commentBody}
                    placeholder={t("Write a comment…")}
                    rows={3}
                    onChange={(event) => {
                      setCommentBody(event.target.value);
                      if (event.target.value.endsWith("@")) {
                        setShowMentions(true);
                      }
                    }}
                  />
                  {showMentions ? (
                    <div className="mention-popover" role="listbox" aria-label={t("Mention people")}>
                      {people.length === 0 ? (
                        <p className="small muted" style={{ padding: 8 }}>
                          {t("No people available")}
                        </p>
                      ) : (
                        people.map((person) => (
                          <button
                            key={person.id}
                            type="button"
                            className="mention-option"
                            role="option"
                            aria-selected={false}
                            onClick={() => insertMention(person)}
                          >
                            <Avatar name={person.name} size="sm" /> {person.name}
                          </button>
                        ))
                      )}
                    </div>
                  ) : null}
                </div>
                {commentMentions.length > 0 ? (
                  <div className="cluster">
                    {commentMentions.map((mentionID) => {
                      const person = people.find((entry) => entry.id === mentionID);
                      if (!person) {
                        return null;
                      }
                      return (
                        <span key={mentionID} className="chip">
                          @{person.name}
                          <button
                            type="button"
                            aria-label={t("Remove {name}", { name: person.name })}
                            onClick={() => setCommentMentions((current) => current.filter((id) => id !== mentionID))}
                          >
                            ×
                          </button>
                        </span>
                      );
                    })}
                  </div>
                ) : null}
                <div className="cluster">
                  <Button type="submit" variant="primary" icon={<Send />} disabled={commentBusy}>
                    {t("Add a comment")}
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    icon={<UserPlus />}
                    onClick={() => setShowMentions((current) => !current)}
                  >
                    {t("Mention people")}
                  </Button>
                </div>
              </form>
            ) : null}
          </section>

          <section className="card card-pad">
            <h2 className="card-title" style={{ marginBottom: 10 }}>
              {t("History")}
            </h2>
            {eventsState.data?.events.length === 0 ? (
              <p className="small muted">{t("No history yet.")}</p>
            ) : (
              <div className="timeline">
                {(eventsState.data?.events ?? []).map((event) => (
                  <div key={event.id} className="timeline-item">
                    <span className="timeline-dot" aria-hidden="true" />
                    <div className="timeline-text">
                      {describeEvent(event, states, people, t)}
                      <span className="timeline-time">
                        {event.actor_name ? `${event.actor_name} · ` : ""}
                        {new Date(event.created_at).toLocaleString()}
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </section>
        </div>

        <aside className="detail-side">
          <div className="card card-pad stack-sm">
            <h2 className="card-title">{t("Actions")}</h2>
            {writable && detail.transitions.length > 0 ? (
              detail.transitions.map((transition) => (
                <Button
                  key={transition.id}
                  variant="secondary"
                  block
                  disabled={busy}
                  onClick={() => transitionApply(transition.id)}
                >
                  {t("Move to {state}", { state: transition.to_state_name })}
                </Button>
              ))
            ) : (
              <p className="small muted">
                {writable ? t("No transitions are available for you on this item.") : t("Read-only access")}
              </p>
            )}
            {writable ? (
              <>
                <div className="divider" />
                <Button variant="ghost" block icon={<Pencil />} onClick={() => setEditOpen(true)}>
                  {t("Edit item")}
                </Button>
                <Button variant="danger-ghost" block icon={<Archive />} onClick={() => setArchiveOpen(true)}>
                  {t("Archive item")}
                </Button>
              </>
            ) : null}
          </div>

          <div className="card card-pad">
            <h2 className="card-title" style={{ marginBottom: 8 }}>
              {t("Detail")}
            </h2>
            <div className="meta-list">
              <div className="meta-row">
                <span className="meta-label">{t("Type")}</span>
                <span className="meta-value">
                  <TypeTag type={item.type} />
                </span>
              </div>
              <div className="meta-row">
                <span className="meta-label">{t("State")}</span>
                <span className="meta-value">{item.state_name}</span>
              </div>
              <div className="meta-row">
                <span className="meta-label">{t("Priority")}</span>
                <span className="meta-value">
                  <PriorityBadge priority={item.priority} />
                </span>
              </div>
              <div className="meta-row">
                <span className="meta-label">{t("Due date")}</span>
                <span className="meta-value">{item.due_on ? <DueLabel date={item.due_on} category={item.state_category} /> : "—"}</span>
              </div>
              <div className="meta-row">
                <span className="meta-label">{t("Project")}</span>
                <span className="meta-value">
                  <Link to={`/projects/${item.project_id}`}>{item.project_name}</Link>
                </span>
              </div>
              <div className="meta-row">
                <span className="meta-label">{t("Created")}</span>
                <span className="meta-value tiny">{new Date(item.created_at).toLocaleString()}</span>
              </div>
            </div>
          </div>

          <div className="card card-pad">
            <h2 className="card-title" style={{ marginBottom: 8 }}>
              {t("Assignees")}
            </h2>
            {item.assignees.length === 0 ? (
              <p className="small muted">{t("Unassigned")}</p>
            ) : (
              <div className="stack-sm">
                {item.assignees.map((assignee) => (
                  <div key={assignee.user_id} className="cluster">
                    <Avatar name={assignee.name} size="sm" />
                    <span className="small">{assignee.name}</span>
                  </div>
                ))}
              </div>
            )}
          </div>

          {detail.parent || detail.children.length > 0 ? (
            <div className="card card-pad">
              <h2 className="card-title" style={{ marginBottom: 8 }}>
                {t("Parent and children")}
              </h2>
              <div className="relation-list">
                {detail.parent ? (
                  <Link to={`/projects/${item.project_id}/items/${detail.parent.id}`} className="relation-link">
                    <TypeIcon type={detail.parent.type} />
                    <span className="grow">{detail.parent.title}</span>
                    <span className="tag">{t("Parent")}</span>
                  </Link>
                ) : null}
                {detail.children.map((child) => (
                  <Link key={child.id} to={`/projects/${item.project_id}/items/${child.id}`} className="relation-link">
                    <TypeIcon type={child.type} />
                    <span className="grow">{child.title}</span>
                    <span className="tag">{child.state_name}</span>
                  </Link>
                ))}
              </div>
            </div>
          ) : null}
        </aside>
      </div>

      <Modal
        open={editOpen}
        title={t("Edit item")}
        onClose={() => setEditOpen(false)}
        wide
        footer={
          <>
            <Button variant="ghost" onClick={() => setEditOpen(false)} disabled={busy}>
              {t("Cancel")}
            </Button>
            <Button variant="primary" onClick={() => void saveItem()} disabled={busy} icon={busy ? <Loader2 className="spinner" /> : <Pencil />}>
              {t("Save item")}
            </Button>
          </>
        }
      >
        <form className="form" onSubmit={saveItem}>
          {editError ? <InlineError message={editError} /> : null}
          <ItemFields
            type={type}
            onTypeChange={setType}
            parentID={parentID}
            onParentChange={setParentID}
            title={title}
            onTitleChange={setTitle}
            description={description}
            onDescriptionChange={setDescription}
            priority={priority}
            onPriorityChange={setPriority}
            dueOn={dueOn}
            onDueChange={setDueOn}
            assignees={assignees}
            onAssigneesChange={setAssignees}
            people={people}
            candidates={(itemsState.data?.items ?? []).filter((candidate) => candidate.id !== item.id)}
            prefix="edit"
          />
        </form>
      </Modal>

      <ConfirmDialog
        open={archiveOpen}
        title={t("Archive this item?")}
        body={t("Archiving an action also archives its micro items. Nothing is deleted from the database.")}
        confirmLabel={t("Archive item")}
        busy={busy}
        onConfirm={archiveItem}
        onClose={() => setArchiveOpen(false)}
      />
    </>
  );

  function transitionApply(transitionID: number) {
    void transition(transitionID);
  }
}

function describeEvent(
  event: ItemEvent,
  states: { id: number; name: string }[],
  people: Person[],
  t: (text: string, vars?: Record<string, string | number>) => string,
) {
  const stateName = (value: string) => states.find((state) => String(state.id) === value)?.name ?? value;
  const names = (value: string) =>
    value
      .split(",")
      .filter(Boolean)
      .map((id) => people.find((person) => String(person.id) === id)?.name ?? `#${id}`)
      .join(", ") || "—";

  switch (event.kind) {
    case "created":
      return t("Created");
    case "archived":
      return t("Archived");
    case "comment_added":
      return t("Comment added.");
    case "state_changed":
      return `${t("State")}: ${stateName(event.from_value ?? "")} → ${stateName(event.to_value ?? "")}`;
    case "assignees_changed":
      return `${t("Assignees")}: ${names(event.from_value ?? "")} → ${names(event.to_value ?? "")}`;
    case "field_changed":
      return `${event.field ?? t("Detail")}: ${event.from_value || "—"} → ${event.to_value || "—"}`;
    default:
      return event.kind;
  }
}
