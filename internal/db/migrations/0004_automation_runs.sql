-- Le azioni add_comment possono citare persone in modo esplicito: gli id
-- separati da virgola vivono accanto al testo, senza interpretare il testo.
ALTER TABLE automation_actions ADD COLUMN mentions TEXT NOT NULL DEFAULT '';

-- Una regola scadenza viene applicata una sola volta per attivita'. La riga
-- funge anche da marcatore per il controllo giornaliero idempotente.
CREATE TABLE automation_rule_runs (
    rule_id INTEGER NOT NULL REFERENCES automation_rules (id),
    item_id INTEGER NOT NULL REFERENCES items (id),
    day TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (rule_id, item_id)
);

CREATE INDEX automation_runs_item ON automation_rule_runs (item_id);
