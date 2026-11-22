-- 0009_project_workflows
-- Workflows become per project. A project owns one or more flows and binds one
-- flow to each item type (Observer, Action, Micro, Bug). The installation-wide
-- workflow that existed before this migration is copied once per existing
-- project, so every project starts with the same "Default" flow and existing
-- items keep pointing at an equivalent state.
--
-- SQLite cannot add a NOT NULL column with a foreign key or change the unique
-- constraint on workflow_states.name in place, so both workflow tables are
-- rebuilt. The migration runner disables foreign key enforcement while pending
-- migrations run and verifies every reference afterwards.
--
-- The legacy rows are snapshotted into temporary tables first: the new state
-- ids are assigned per project and the items, transitions and transition
-- conditions are remapped through the old state names.

CREATE TEMP TABLE legacy_states AS
    SELECT id, name, category, position, created_at FROM workflow_states;
CREATE TEMP TABLE legacy_transitions AS
    SELECT id, from_state_id, to_state_id, name, position FROM workflow_transitions;
CREATE TEMP TABLE legacy_conditions AS
    SELECT transition_id, kind, value FROM transition_conditions;
CREATE TEMP TABLE legacy_settings AS
    SELECT initial_state_id FROM settings WHERE id = 1;

CREATE TABLE workflows (
    id INTEGER PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects (id),
    name TEXT NOT NULL COLLATE NOCASE,
    preset TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (project_id, name)
);

CREATE INDEX workflows_project ON workflows (project_id);

CREATE TABLE project_workflows (
    project_id INTEGER NOT NULL REFERENCES projects (id),
    item_type TEXT NOT NULL CHECK (item_type IN ('observer', 'action', 'micro', 'bug')),
    workflow_id INTEGER NOT NULL REFERENCES workflows (id),
    PRIMARY KEY (project_id, item_type)
);

CREATE INDEX project_workflows_workflow ON project_workflows (workflow_id);

-- One workflow per existing project, with the same name the Go code will use
-- when it seeds a project created after this migration.
INSERT INTO workflows (project_id, name, preset, created_at)
SELECT p.id, 'Default', 'default', strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
FROM projects p;

CREATE TABLE workflow_states_new (
    id INTEGER PRIMARY KEY,
    workflow_id INTEGER NOT NULL REFERENCES workflows (id),
    name TEXT NOT NULL COLLATE NOCASE,
    category TEXT NOT NULL CHECK (category IN ('open', 'done')),
    position INTEGER NOT NULL,
    is_initial INTEGER NOT NULL DEFAULT 0 CHECK (is_initial IN (0, 1)),
    created_at TEXT NOT NULL,
    UNIQUE (workflow_id, name)
);

CREATE UNIQUE INDEX workflow_states_initial
    ON workflow_states_new (workflow_id)
    WHERE is_initial = 1;

CREATE INDEX workflow_states_workflow ON workflow_states_new (workflow_id, position);

-- Every project receives a copy of the legacy states. The legacy initial state
-- becomes the initial state of every copy.
INSERT INTO workflow_states_new (workflow_id, name, category, position, is_initial, created_at)
SELECT w.id, s.name, s.category, s.position,
       CASE WHEN s.id = (SELECT initial_state_id FROM legacy_settings) THEN 1 ELSE 0 END,
       s.created_at
FROM workflows w
CROSS JOIN legacy_states s;

-- A project created before the initial state was chosen (or a corrupted
-- settings row) still needs an initial state: the first one by position.
UPDATE workflow_states_new
SET is_initial = 1
WHERE id IN (
    SELECT (
        SELECT ns.id
        FROM workflow_states_new ns
        WHERE ns.workflow_id = w.id
        ORDER BY ns.position, ns.id
        LIMIT 1
    )
    FROM workflows w
    WHERE NOT EXISTS (
        SELECT 1 FROM workflow_states_new other
        WHERE other.workflow_id = w.id AND other.is_initial = 1
    )
);

CREATE TABLE workflow_transitions_new (
    id INTEGER PRIMARY KEY,
    workflow_id INTEGER NOT NULL REFERENCES workflows (id),
    from_state_id INTEGER NOT NULL REFERENCES workflow_states_new (id),
    to_state_id INTEGER NOT NULL REFERENCES workflow_states_new (id),
    name TEXT NOT NULL,
    position INTEGER NOT NULL,
    UNIQUE (workflow_id, from_state_id, to_state_id)
);

CREATE INDEX workflow_transitions_workflow ON workflow_transitions_new (workflow_id, position);

INSERT INTO workflow_transitions_new (workflow_id, from_state_id, to_state_id, name, position)
SELECT w.id, nf.id, nt.id, t.name, t.position
FROM workflows w
CROSS JOIN legacy_transitions t
JOIN legacy_states ofs ON ofs.id = t.from_state_id
JOIN legacy_states ots ON ots.id = t.to_state_id
JOIN workflow_states_new nf ON nf.workflow_id = w.id AND nf.name = ofs.name
JOIN workflow_states_new nt ON nt.workflow_id = w.id AND nt.name = ots.name;

-- Conditions follow their arrow into every copy. Clearing the old rows first
-- avoids an id overlap between the legacy and the new transitions.
DELETE FROM transition_conditions;

INSERT INTO transition_conditions (transition_id, kind, value)
SELECT nt.id, c.kind, c.value
FROM workflows w
CROSS JOIN legacy_conditions c
JOIN legacy_transitions ot ON ot.id = c.transition_id
JOIN legacy_states ofs ON ofs.id = ot.from_state_id
JOIN legacy_states ots ON ots.id = ot.to_state_id
JOIN workflow_states_new nfs ON nfs.workflow_id = w.id AND nfs.name = ofs.name
JOIN workflow_states_new nts ON nts.workflow_id = w.id AND nts.name = ots.name
JOIN workflow_transitions_new nt
  ON nt.workflow_id = w.id
 AND nt.from_state_id = nfs.id
 AND nt.to_state_id = nts.id;

DROP TABLE workflow_transitions;
DROP TABLE workflow_states;

ALTER TABLE workflow_states_new RENAME TO workflow_states;
ALTER TABLE workflow_transitions_new RENAME TO workflow_transitions;

-- Existing items move to the copy that belongs to their project. Names are
-- unique inside the legacy set, so the join is unambiguous.
UPDATE items
SET state_id = (
    SELECT ns.id
    FROM workflows w
    JOIN workflow_states ns ON ns.workflow_id = w.id
    WHERE w.project_id = items.project_id
      AND ns.name = (SELECT ls.name FROM legacy_states ls WHERE ls.id = items.state_id)
);

-- settings no longer holds the installation initial state: it lives on each
-- workflow, marked by workflow_states.is_initial.
CREATE TABLE settings_new (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    setup_completed INTEGER NOT NULL DEFAULT 0 CHECK (setup_completed IN (0, 1)),
    require_2fa INTEGER NOT NULL DEFAULT 0 CHECK (require_2fa IN (0, 1))
);

INSERT INTO settings_new (id, setup_completed, require_2fa)
SELECT id, setup_completed, require_2fa FROM settings;

DROP TABLE settings;
ALTER TABLE settings_new RENAME TO settings;

-- Every existing project binds its copy to all four item types, which keeps the
-- behaviour of the global workflow: one flow shared by every type.
INSERT INTO project_workflows (project_id, item_type, workflow_id)
SELECT w.project_id, t.item_type, w.id
FROM workflows w
CROSS JOIN (
    SELECT 'observer' AS item_type
    UNION ALL SELECT 'action'
    UNION ALL SELECT 'micro'
    UNION ALL SELECT 'bug'
) t;

DROP TABLE legacy_conditions;
DROP TABLE legacy_transitions;
DROP TABLE legacy_states;
DROP TABLE legacy_settings;
