import { useState } from "react";
import { AtSign, Bell, CalendarClock, CheckCheck, Inbox, UserPlus, Zap } from "lucide-react";
import { api, errorMessage } from "../api";
import { useI18n } from "../i18n";
import { useRouter } from "../router";
import { useApp, useLoader } from "../state";
import type { Notification } from "../types";
import { Badge, Button, EmptyState, ErrorState, Loading, PageHead, useToast } from "../components/ui";

function kindIcon(kind: Notification["kind"]) {
  switch (kind) {
    case "assigned":
      return <UserPlus />;
    case "mention":
      return <AtSign />;
    case "due":
      return <CalendarClock />;
    case "automation":
      return <Zap />;
    default:
      return <Bell />;
  }
}

export function NotificationsScreen() {
  const { t, formatDateTime } = useI18n();
  const { navigate } = useRouter();
  const { refreshUnread, notificationsVersion } = useApp();
  const { notify } = useToast();
  const [unreadOnly, setUnreadOnly] = useState(false);
  const state = useLoader(
    () => api<{ notifications: Notification[] }>(`/api/notifications?limit=100${unreadOnly ? "&unread=1" : ""}`),
    [unreadOnly, notificationsVersion],
  );

  const notifications = state.data?.notifications ?? [];

  async function open(notification: Notification) {
    if (!notification.read) {
      try {
        await api(`/api/notifications/${notification.id}/read`, { method: "POST" });
        await refreshUnread();
        state.reload();
      } catch (err) {
        notify(errorMessage(err), "bad");
        return;
      }
    }
    navigate(notification.link);
  }

  async function markAll() {
    try {
      await api("/api/notifications/read-all", { method: "POST" });
      await refreshUnread();
      notify(t("Marked as read."), "good");
      state.reload();
    } catch (err) {
      notify(errorMessage(err), "bad");
    }
  }

  function title(notification: Notification) {
    switch (notification.kind) {
      case "assigned":
        return t("Assigned to you");
      case "mention":
        return t("You were mentioned");
      case "due":
        return t("Due date summary");
      case "automation":
        return t("Automation");
      default:
        return notification.kind;
    }
  }

  return (
    <>
      <PageHead
        title={t("Notifications")}
        lede={t("Assignments, mentions, due summaries and automations.")}
        actions={
          <>
            <Button variant={unreadOnly ? "primary" : "secondary"} onClick={() => setUnreadOnly((value) => !value)}>
              {t("Unread only")}
            </Button>
            <Button variant="secondary" icon={<CheckCheck />} onClick={markAll}>
              {t("Mark all as read")}
            </Button>
          </>
        }
      />

      {state.loading ? <Loading /> : null}
      {state.error ? <ErrorState message={state.error} onRetry={state.reload} /> : null}

      {!state.loading && !state.error ? (
        notifications.length === 0 ? (
          <div className="card">
            <EmptyState icon={<Inbox />} title={t("No notifications yet.")}>
              {t("You are up to date.")}
            </EmptyState>
          </div>
        ) : (
          <div className="card" style={{ overflow: "hidden" }}>
            {notifications.map((notification) => (
              <button
                key={notification.id}
                type="button"
                className={`notification-row ${notification.read ? "" : "notification-row--unread"}`}
                onClick={() => open(notification)}
              >
                <span className="notification-icon" aria-hidden="true">
                  {kindIcon(notification.kind)}
                </span>
                {notification.read ? null : <span className="unread-pip" aria-hidden="true" />}
                <span className="notification-main">
                  <span className="notification-title">{title(notification)}</span>
                  {notification.item_title ? (
                    <span className="notification-sub">
                      {notification.item_title}
                      {notification.project_name ? ` · ${notification.project_name}` : ""}
                    </span>
                  ) : null}
                  {notification.kind === "due" && notification.payload?.count ? (
                    <span className="notification-sub">
                      {t("{count} items are due or overdue.", { count: notification.payload.count })}
                    </span>
                  ) : null}
                  {notification.comment_excerpt ? (
                    <span className="notification-sub">{notification.comment_excerpt}</span>
                  ) : null}
                </span>
                <span className="cluster" style={{ flexDirection: "column", alignItems: "flex-end", gap: 4 }}>
                  <span className="notification-time">{formatDateTime(notification.created_at)}</span>
                  <Badge tone="mono">{notification.kind === "due" ? t("Go to calendar") : t("Open item")}</Badge>
                </span>
              </button>
            ))}
          </div>
        )
      ) : null}
    </>
  );
}
