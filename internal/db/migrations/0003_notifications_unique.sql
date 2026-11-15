-- Deduplica delle notifiche. L'assegnazione non si ripete finche' la
-- precedente resta non letta; la menzione e' unica per commento e utente; il
-- riepilogo scadenze e' unico per utente e giorno UTC.
CREATE UNIQUE INDEX notifications_assigned_unread
    ON notifications (user_id, item_id)
    WHERE kind = 'assigned' AND read_at IS NULL;

CREATE UNIQUE INDEX notifications_mention_once
    ON notifications (user_id, comment_id)
    WHERE kind = 'mention';

CREATE UNIQUE INDEX notifications_due_day
    ON notifications (user_id, substr(created_at, 1, 10))
    WHERE kind = 'due';
