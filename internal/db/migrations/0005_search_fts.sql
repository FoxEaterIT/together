-- Ricerca full-text su attivita'. Una riga per item: il titolo e la
-- descrizione arrivano dalla riga items, i commenti sono aggregati. L'indice
-- non contiene progetto ne' stato: la visibilita' resta un join su items e
-- projects, come per ogni altra query. item_id non e' indicizzato perche' serve
-- solo a collegare la riga FTS all'item.
CREATE VIRTUAL TABLE items_fts USING fts5(
    title,
    description,
    comments,
    item_id UNINDEXED,
    tokenize = 'unicode61 remove_diacritics 2'
);

-- Backfill degli item gia' presenti. group_concat separa i commenti con uno
-- spazio: per la ricerca conta l'insieme dei token, non il confine tra commenti.
INSERT INTO items_fts (title, description, comments, item_id)
SELECT i.title,
       i.description,
       COALESCE((SELECT group_concat(c.body, ' ') FROM comments c WHERE c.item_id = i.id), ''),
       i.id
FROM items i;

-- Ogni scrittura ricostruisce la singola riga. La DELETE esplicita evita di
-- lasciare una riga vecchia quando il testo cambia o un commento sparisce.
CREATE TRIGGER items_fts_item_insert
AFTER INSERT ON items
BEGIN
    INSERT INTO items_fts (title, description, comments, item_id)
    VALUES (NEW.title, NEW.description,
            COALESCE((SELECT group_concat(body, ' ') FROM comments WHERE item_id = NEW.id), ''),
            NEW.id);
END;

CREATE TRIGGER items_fts_item_update
AFTER UPDATE OF title, description ON items
BEGIN
    DELETE FROM items_fts WHERE item_id = OLD.id;
    INSERT INTO items_fts (title, description, comments, item_id)
    VALUES (NEW.title, NEW.description,
            COALESCE((SELECT group_concat(body, ' ') FROM comments WHERE item_id = NEW.id), ''),
            NEW.id);
END;

CREATE TRIGGER items_fts_item_delete
AFTER DELETE ON items
BEGIN
    DELETE FROM items_fts WHERE item_id = OLD.id;
END;

CREATE TRIGGER items_fts_comment_insert
AFTER INSERT ON comments
BEGIN
    DELETE FROM items_fts WHERE item_id = NEW.item_id;
    INSERT INTO items_fts (title, description, comments, item_id)
    SELECT i.title, i.description,
           COALESCE((SELECT group_concat(c.body, ' ') FROM comments c WHERE c.item_id = i.id), ''),
           i.id
    FROM items i
    WHERE i.id = NEW.item_id;
END;

CREATE TRIGGER items_fts_comment_update
AFTER UPDATE OF body, item_id ON comments
BEGIN
    DELETE FROM items_fts WHERE item_id = OLD.item_id;
    INSERT INTO items_fts (title, description, comments, item_id)
    SELECT i.title, i.description,
           COALESCE((SELECT group_concat(c.body, ' ') FROM comments c WHERE c.item_id = i.id), ''),
           i.id
    FROM items i
    WHERE i.id = NEW.item_id;
END;

CREATE TRIGGER items_fts_comment_delete
AFTER DELETE ON comments
BEGIN
    DELETE FROM items_fts WHERE item_id = OLD.item_id;
    INSERT INTO items_fts (title, description, comments, item_id)
    SELECT i.title, i.description,
           COALESCE((SELECT group_concat(c.body, ' ') FROM comments c WHERE c.item_id = i.id), ''),
           i.id
    FROM items i
    WHERE i.id = OLD.item_id;
END;
