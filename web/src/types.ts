// Wire types shared by the API client and the screens. They mirror the JSON
// produced by internal/httpapi; change both sides together.
export type Role = "admin" | "manager" | "member" | "guest";
export type Locale = "en" | "it" | "es";
export type Visibility = "open" | "private";
export type ItemType = "observer" | "action" | "micro" | "bug";
export type Priority = "none" | "low" | "medium" | "high" | "urgent";
export type StateCategory = "open" | "done";

export type User = {
  id: number;
  name: string;
  email: string;
  role: Role;
  locale: Locale;
  email_notifications: boolean;
  guest_expires_on: string;
  disabled: boolean;
  created_at: string;
  smtp_configured?: boolean;
  two_factor_enabled: boolean;
};

export type TwoFactorStatus = {
  available: boolean;
  enabled: boolean;
  pending: boolean;
  recovery_codes_remaining: number;
};

export type TwoFactorSetup = {
  secret: string;
  otpauth_uri: string;
};

export type LoginChallenge = {
  two_factor_required?: boolean;
  two_factor_setup_required?: boolean;
  challenge_token: string;
  expires_at: string;
};

export type EnrollResult = {
  user: User;
  recovery_codes: string[];
};

export type Person = {
  id: number;
  name: string;
  role: Role;
  guest_expires_on: string;
  disabled: boolean;
};

export type Project = {
  id: number;
  name: string;
  description: string;
  visibility: Visibility;
  created_by: number;
  created_by_name: string;
  created_at: string;
  archived_at?: string;
  member_count: number;
  team_count: number;
};

export type Assignee = {
  user_id: number;
  name: string;
  email: string;
  role: Role;
  assigned_at: string;
};

export type Item = {
  id: number;
  project_id: number;
  project_name: string;
  type: ItemType;
  parent_id: number | null;
  title: string;
  description: string;
  priority: Priority;
  state_id: number;
  state_name: string;
  state_category: StateCategory;
  due_on: string;
  created_by: number;
  created_by_name: string;
  created_at: string;
  updated_at: string;
  archived_at?: string;
  assignees: Assignee[];
  snippet?: string;
  match?: "title" | "description" | "comments";
};

export type ItemRef = {
  id: number;
  type: ItemType;
  title: string;
  state_id: number;
  state_name: string;
};

export type WorkflowCondition = {
  id?: number;
  transition_id?: number;
  kind: "role" | "assignee" | "priority" | "item_type";
  value: string;
};

export type WorkflowState = {
  id: number;
  workflow_id: number;
  name: string;
  category: StateCategory;
  position: number;
  created_at: string;
  initial: boolean;
  item_count: number;
  active_item_count: number;
};

export type WorkflowTransition = {
  id: number;
  workflow_id: number;
  from_state_id: number;
  from_state_name: string;
  to_state_id: number;
  to_state_name: string;
  name: string;
  position: number;
  conditions: WorkflowCondition[];
};

export type Workflow = {
  id: number;
  project_id: number;
  name: string;
  preset: string;
  created_at: string;
  states: WorkflowState[];
  transitions: WorkflowTransition[];
  initial_state_id: number;
  item_types: string[];
};

export type WorkflowList = {
  workflows: Workflow[];
  bindings: Record<string, number>;
};

export type WorkflowStateRef = {
  id: number;
  name: string;
  category: StateCategory;
  workflow_id: number;
  workflow_name: string;
  project_id: number;
  project_name: string;
};

export type WorkflowPreset = {
  slug: string;
  name: string;
  team_kind: string;
  description: string;
  states: { key: string; name: string; category: StateCategory }[];
  transitions: { from: string; to: string; name: string; conditions?: WorkflowCondition[] }[];
};

export type ItemDetail = {
  item: Item;
  parent: ItemRef | null;
  children: ItemRef[];
  transitions: WorkflowTransition[];
};

export type Comment = {
  id: number;
  item_id: number;
  author_id: number;
  author_name: string;
  body: string;
  created_at: string;
  edited_at?: string;
  mentions: { user_id: number; name: string }[];
};

export type ItemEvent = {
  id: number;
  kind: string;
  field?: string;
  from_value?: string;
  to_value?: string;
  actor_id?: number;
  actor_name?: string;
  created_at: string;
};

export type Notification = {
  id: number;
  user_id?: number;
  kind: "assigned" | "mention" | "due" | "automation";
  item_id: number | null;
  comment_id: number | null;
  payload?: { day?: string; count?: number };
  created_at: string;
  read_at?: string;
  emailed_at?: string;
  read: boolean;
  item_title?: string;
  project_id?: number;
  project_name?: string;
  comment_excerpt?: string;
  link: string;
};

export type AutomationAction = {
  id?: number;
  position: number;
  kind: "set_state" | "assign_user" | "add_comment" | "notify";
  value: string;
  state_id?: number;
  user_id?: number;
  body?: string;
  mention_ids: number[];
};

export type AutomationRule = {
  id: number;
  name: string;
  enabled: boolean;
  trigger_kind: "state_changed" | "assigned" | "comment_added" | "due_passed";
  trigger_value: string;
  trigger_state_id?: number;
  trigger_state_name?: string;
  project_ids: number[];
  projects: { id: number; name: string }[];
  actions: AutomationAction[];
  created_by: number;
  created_at: string;
};

export type Token = {
  id: number;
  name: string;
  prefix: string;
  /** Present only in the response that creates the token. */
  token?: string;
  created_at: string;
};

export type Invite = {
  id: number;
  email: string;
  role: Role;
  guest_days: number | null;
  created_at: string;
  expires_at: string;
};

export type Team = {
  id: number;
  name: string;
  created_at: string;
  archived_at?: string;
};

export type ProjectMember = {
  user_id: number;
  name: string;
  email: string;
  role: Role;
  added_at: string;
};

export type TeamMember = {
  user_id: number;
  name: string;
  email: string;
  role: Role;
  added_at: string;
};

export type ProjectTeamLink = {
  team_id: number;
  name: string;
  added_at: string;
};

export type SmtpSettings = {
  host: string;
  port: number;
  user: string;
  from: string;
  tls: boolean;
  configured: boolean;
  has_password: boolean;
};

export type SecuritySettings = {
  require_2fa: boolean;
};

export type ReportStateCount = {
  state_id: number;
  state_name: string;
  state_category: StateCategory;
  count: number;
};

export type ReportWorkload = {
  user_id: number;
  name: string;
  role: Role;
  guest_expires_on: string;
  disabled: boolean;
  expired: boolean;
  open_count: number;
};

export type Report = {
  states: ReportStateCount[];
  workload: ReportWorkload[];
  totals: { open: number; done: number; all: number };
};

export type ListResponse<T> = { [key: string]: T[] };
