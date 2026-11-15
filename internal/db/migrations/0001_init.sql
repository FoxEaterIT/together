CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT NOT NULL COLLATE NOCASE UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('admin', 'manager', 'member', 'guest')),
    locale TEXT NOT NULL DEFAULT 'en' CHECK (locale IN ('en', 'it')),
    email_notifications INTEGER NOT NULL DEFAULT 0 CHECK (email_notifications IN (0, 1)),
    guest_expires_on TEXT,
    invited_by INTEGER REFERENCES users (id),
    created_at TEXT NOT NULL,
    disabled_at TEXT
);

CREATE TABLE sessions (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users (id),
    token_hash TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT
);

CREATE TABLE api_tokens (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users (id),
    name TEXT NOT NULL,
    token TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    revoked_at TEXT
);

CREATE TABLE teams (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    created_at TEXT NOT NULL,
    archived_at TEXT
);

CREATE TABLE team_members (
    team_id INTEGER NOT NULL REFERENCES teams (id),
    user_id INTEGER NOT NULL REFERENCES users (id),
    added_at TEXT NOT NULL,
    PRIMARY KEY (team_id, user_id)
);

CREATE TABLE projects (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    visibility TEXT NOT NULL CHECK (visibility IN ('open', 'private')),
    created_by INTEGER NOT NULL REFERENCES users (id),
    created_at TEXT NOT NULL,
    archived_at TEXT
);

CREATE TABLE project_members (
    project_id INTEGER NOT NULL REFERENCES projects (id),
    user_id INTEGER NOT NULL REFERENCES users (id),
    added_at TEXT NOT NULL,
    PRIMARY KEY (project_id, user_id)
);

CREATE TABLE project_teams (
    project_id INTEGER NOT NULL REFERENCES projects (id),
    team_id INTEGER NOT NULL REFERENCES teams (id),
    added_at TEXT NOT NULL,
    PRIMARY KEY (project_id, team_id)
);

CREATE TABLE workflow_states (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL COLLATE NOCASE UNIQUE,
    category TEXT NOT NULL CHECK (category IN ('open', 'done')),
    position INTEGER NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE workflow_transitions (
    id INTEGER PRIMARY KEY,
    from_state_id INTEGER NOT NULL REFERENCES workflow_states (id),
    to_state_id INTEGER NOT NULL REFERENCES workflow_states (id),
    name TEXT NOT NULL,
    position INTEGER NOT NULL,
    UNIQUE (from_state_id, to_state_id)
);

CREATE TABLE settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    initial_state_id INTEGER REFERENCES workflow_states (id),
    setup_completed INTEGER NOT NULL DEFAULT 0 CHECK (setup_completed IN (0, 1))
);

CREATE TABLE transition_conditions (
    id INTEGER PRIMARY KEY,
    transition_id INTEGER NOT NULL REFERENCES workflow_transitions (id),
    kind TEXT NOT NULL CHECK (kind IN ('role', 'assignee', 'priority', 'item_type')),
    value TEXT NOT NULL
);

CREATE TABLE items (
    id INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects (id),
    type TEXT NOT NULL CHECK (type IN ('observer', 'action', 'micro', 'bug')),
    parent_id INTEGER REFERENCES items (id),
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    priority TEXT NOT NULL DEFAULT 'none' CHECK (priority IN ('none', 'low', 'medium', 'high', 'urgent')),
    state_id INTEGER NOT NULL REFERENCES workflow_states (id),
    due_on TEXT,
    created_by INTEGER NOT NULL REFERENCES users (id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    archived_at TEXT
);

CREATE INDEX items_project_archived ON items (project_id, archived_at);
CREATE INDEX items_parent ON items (parent_id);
CREATE INDEX items_state ON items (state_id);
CREATE INDEX items_due ON items (due_on);

CREATE TABLE item_assignees (
    item_id INTEGER NOT NULL REFERENCES items (id),
    user_id INTEGER NOT NULL REFERENCES users (id),
    assigned_at TEXT NOT NULL,
    PRIMARY KEY (item_id, user_id)
);

CREATE TABLE comments (
    id INTEGER PRIMARY KEY,
    item_id INTEGER NOT NULL REFERENCES items (id),
    author_id INTEGER NOT NULL REFERENCES users (id),
    body TEXT NOT NULL,
    created_at TEXT NOT NULL,
    edited_at TEXT
);

CREATE TABLE comment_mentions (
    comment_id INTEGER NOT NULL REFERENCES comments (id),
    user_id INTEGER NOT NULL REFERENCES users (id),
    PRIMARY KEY (comment_id, user_id)
);

CREATE TABLE item_events (
    id INTEGER PRIMARY KEY,
    item_id INTEGER NOT NULL REFERENCES items (id),
    actor_id INTEGER REFERENCES users (id),
    kind TEXT NOT NULL CHECK (kind IN ('created', 'field_changed', 'state_changed', 'assignees_changed', 'comment_added', 'archived')),
    field TEXT,
    from_value TEXT,
    to_value TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX item_events_item ON item_events (item_id, created_at);

CREATE TABLE automation_rules (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    trigger_kind TEXT NOT NULL CHECK (trigger_kind IN ('state_changed', 'assigned', 'comment_added', 'due_passed')),
    trigger_value TEXT NOT NULL DEFAULT '',
    created_by INTEGER NOT NULL REFERENCES users (id),
    created_at TEXT NOT NULL
);

CREATE TABLE automation_rule_projects (
    rule_id INTEGER NOT NULL REFERENCES automation_rules (id),
    project_id INTEGER NOT NULL REFERENCES projects (id),
    PRIMARY KEY (rule_id, project_id)
);

CREATE TABLE automation_actions (
    id INTEGER PRIMARY KEY,
    rule_id INTEGER NOT NULL REFERENCES automation_rules (id),
    position INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('set_state', 'assign_user', 'add_comment', 'notify')),
    value TEXT NOT NULL
);

CREATE TABLE notifications (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users (id),
    kind TEXT NOT NULL CHECK (kind IN ('assigned', 'mention', 'due', 'automation')),
    item_id INTEGER REFERENCES items (id),
    comment_id INTEGER REFERENCES comments (id),
    payload TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    read_at TEXT,
    emailed_at TEXT
);

CREATE INDEX notifications_user_unread ON notifications (user_id, read_at);

CREATE TABLE invites (
    id INTEGER PRIMARY KEY,
    email TEXT NOT NULL COLLATE NOCASE,
    role TEXT NOT NULL CHECK (role IN ('admin', 'manager', 'member', 'guest')),
    guest_days INTEGER CHECK (guest_days IN (7, 30, 90)),
    token TEXT NOT NULL UNIQUE,
    invited_by INTEGER NOT NULL REFERENCES users (id),
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    accepted_at TEXT,
    revoked_at TEXT
);

INSERT INTO settings (id, initial_state_id, setup_completed) VALUES (1, NULL, 0);
