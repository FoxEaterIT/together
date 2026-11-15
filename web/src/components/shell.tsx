import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import {
  Bell,
  CalendarDays,
  ChevronDown,
  FolderKanban,
  ListChecks,
  LogOut,
  Mail,
  Menu,
  Search,
  ShieldCheck,
  SlidersHorizontal,
  UserRound,
  Users,
  Workflow as WorkflowIcon,
  X,
  Zap,
  BarChart3,
  Database,
} from "lucide-react";
import { api, errorMessage } from "../api";
import { roleLabel, useI18n } from "../i18n";
import { LanguageSelect, ThemeToggle } from "../preferences";
import { Link, useRouter } from "../router";
import { canCreateProjects, useApp } from "../state";
import type { Notification } from "../types";
import { Avatar, Badge, IconButton } from "./ui";

function NavLink({
  to,
  icon,
  label,
  end,
  count,
  onNavigate,
}: {
  to: string;
  icon: ReactNode;
  label: string;
  end?: boolean;
  count?: number;
  onNavigate?: () => void;
}) {
  const { path } = useRouter();
  const active = end ? path === to : path === to || path.startsWith(`${to}/`);
  return (
    <Link
      to={to}
      className="nav-link"
      aria-current={active ? "page" : undefined}
      onClick={onNavigate}
    >
      {icon}
      <span>{label}</span>
      {count ? <span className="nav-count">{count}</span> : null}
    </Link>
  );
}

export function Shell({ children, onSignOut }: { children: ReactNode; onSignOut: () => void }) {
  const { t } = useI18n();
  const { user, projects, unread, refreshUnread } = useApp();
  const { path, navigate } = useRouter();
  const [drawer, setDrawer] = useState(false);
  const [menu, setMenu] = useState<"none" | "bell" | "user">("none");
  const [notifications, setNotifications] = useState<Notification[]>([]);
  const [notificationsLoading, setNotificationsLoading] = useState(false);
  const [query, setQuery] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);
  const actionsRef = useRef<HTMLDivElement>(null);

  const isAdmin = user.role === "admin";
  const isManager = user.role === "manager";
  const showAdmin = isAdmin || isManager;

  useEffect(() => {
    setDrawer(false);
    setMenu("none");
  }, [path]);

  useEffect(() => {
    if (!drawer) {
      return;
    }
    function onKey(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setDrawer(false);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [drawer]);

  useEffect(() => {
    function onKey(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        searchRef.current?.focus();
        searchRef.current?.select();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    if (menu === "none") {
      return;
    }
    function onPointer(event: PointerEvent) {
      if (actionsRef.current && !actionsRef.current.contains(event.target as Node)) {
        setMenu("none");
      }
    }
    window.addEventListener("pointerdown", onPointer);
    return () => window.removeEventListener("pointerdown", onPointer);
  }, [menu]);

  useEffect(() => {
    if (menu !== "bell") {
      return;
    }
    let cancelled = false;
    setNotificationsLoading(true);
    api<{ notifications: Notification[] }>("/api/notifications?limit=8")
      .then((body) => {
        if (!cancelled) {
          setNotifications(body.notifications);
        }
      })
      .catch(() => undefined)
      .finally(() => {
        if (!cancelled) {
          setNotificationsLoading(false);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [menu, unread]);

  useEffect(() => {
    if (path === "/search") {
      setQuery(new URLSearchParams(window.location.search).get("q") ?? "");
    }
  }, [path]);

  function submitSearch(event: FormEvent) {
    event.preventDefault();
    const trimmed = query.trim();
    navigate(trimmed ? `/search?q=${encodeURIComponent(trimmed)}` : "/search");
  }

  async function markAllRead() {
    try {
      await api("/api/notifications/read-all", { method: "POST" });
      setNotifications((current) => current.map((notification) => ({ ...notification, read: true })));
      await refreshUnread();
    } catch (error) {
      console.warn(errorMessage(error));
    }
  }

  async function openNotification(notification: Notification) {
    if (!notification.read) {
      try {
        await api(`/api/notifications/${notification.id}/read`, { method: "POST" });
        setNotifications((current) =>
          current.map((entry) => (entry.id === notification.id ? { ...entry, read: true } : entry)),
        );
        await refreshUnread();
      } catch {
        // Navigation still proceeds; the server stays the source of truth.
      }
    }
    navigate(notification.link);
  }

  async function signOut() {
    try {
      await api("/api/logout", { method: "POST" });
    } catch {
      // A failed logout still clears the local view.
    }
    onSignOut();
  }

  const quickProjects = projects.slice(0, 6);

  return (
    <div className="app-shell">
      {drawer ? (
        <button className="sidebar-scrim" aria-label={t("Close navigation")} onClick={() => setDrawer(false)} />
      ) : null}
      <aside className={`sidebar ${drawer ? "sidebar--open" : ""}`} aria-label={t("Main navigation")}>
        <div className="brand">
          <img className="brand-mark" src="/assets/logo-256.png" alt="" width={34} height={34} aria-hidden="true" />
          <div>
            <div className="brand-word">Together</div>
            <span className="brand-sub">self-hosted</span>
          </div>
          <IconButton className="sidebar-close" label={t("Close navigation")} onClick={() => setDrawer(false)}>
            <X />
          </IconButton>
        </div>

        <nav className="nav-group" aria-label={t("Main navigation")}>
          <span className="nav-label">{t("Work")}</span>
          <NavLink to="/" end icon={<ListChecks />} label={t("My work")} />
          <NavLink to="/search" icon={<Search />} label={t("Search")} />
          <NavLink to="/calendar" icon={<CalendarDays />} label={t("Calendar")} />
          <NavLink to="/reports" icon={<BarChart3 />} label={t("Reports")} />
        </nav>

        <div className="nav-group">
          <span className="nav-label">{t("Projects")}</span>
          <NavLink to="/projects" end icon={<FolderKanban />} label={t("All projects")} />
          <div className="quick-projects">
            {quickProjects.map((project) => (
              <Link key={project.id} to={`/projects/${project.id}`} className="quick-link">
                <span className="quick-dot" aria-hidden="true" />
                <span className="grow" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {project.name}
                </span>
              </Link>
            ))}
          </div>
        </div>

        {isAdmin ? (
          <div className="nav-group">
            <span className="nav-label">{t("Organization")}</span>
            <NavLink to="/people" end icon={<Users />} label={t("People")} />
            <NavLink to="/people/invites" icon={<Mail />} label={t("Invitations")} />
            <NavLink to="/teams" icon={<ShieldCheck />} label={t("Teams")} />
          </div>
        ) : null}

        {showAdmin ? (
          <div className="nav-group">
            <span className="nav-label">{t("Administration")}</span>
            <NavLink to="/automations" icon={<Zap />} label={t("Automations")} />
            {isAdmin ? <NavLink to="/workflow" icon={<WorkflowIcon />} label={t("Workflow")} /> : null}
            {isAdmin ? <NavLink to="/admin/backup" icon={<Database />} label={t("Backup")} /> : null}
          </div>
        ) : null}

        <div className="sidebar-foot">
          <strong>{user.name}</strong>
          <span>{roleLabel(user.role, t)}</span>
          {canCreateProjects(user) ? <div className="tiny">{t("Can create projects")}</div> : null}
        </div>
      </aside>

      <div className="shell-main">
        <header className="topbar">
          <IconButton className="menu-button" label={t("Open navigation")} onClick={() => setDrawer(true)}>
            <Menu />
          </IconButton>

          <form className="top-search" role="search" onSubmit={submitSearch}>
            <Search aria-hidden="true" />
            <input
              ref={searchRef}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t("Search everything")}
              aria-label={t("Global search")}
              type="search"
            />
            <span className="kbd" aria-hidden="true">
              ⌘K
            </span>
          </form>

          <div className="topbar-actions" ref={actionsRef}>
            <LanguageSelect className="lang-select--top" />
            <ThemeToggle />
            <IconButton
              label={t("Unread notifications")}
              aria-expanded={menu === "bell"}
              aria-haspopup="dialog"
              onClick={() => setMenu(menu === "bell" ? "none" : "bell")}
            >
              <Bell />
              {unread > 0 ? (
                <span className="badge-dot" aria-live="polite" aria-atomic="true">
                  {unread > 99 ? "99+" : unread}
                </span>
              ) : null}
            </IconButton>

            <button
              type="button"
              className="user-button"
              aria-label={t("Account menu")}
              aria-expanded={menu === "user"}
              aria-haspopup="menu"
              onClick={() => setMenu(menu === "user" ? "none" : "user")}
            >
              <Avatar name={user.name} />
              <ChevronDown aria-hidden="true" style={{ width: 15, height: 15 }} />
            </button>

            {menu === "bell" ? (
              <div className="popover" role="dialog" aria-label={t("Notifications")}>
                <div className="popover-head">
                  <span className="popover-title">{t("Notifications")}</span>
                  <button type="button" className="btn btn--ghost btn--sm" onClick={markAllRead}>
                    {t("Mark all as read")}
                  </button>
                </div>
                <div className="popover-list">
                  {notificationsLoading && notifications.length === 0 ? (
                    <div className="loading-block small">{t("Loading…")}</div>
                  ) : notifications.length === 0 ? (
                    <div className="empty" style={{ padding: 28 }}>
                      <h3 className="small">{t("No unread notifications")}</h3>
                      <p className="tiny">{t("You are up to date.")}</p>
                    </div>
                  ) : (
                    notifications.map((notification) => (
                      <button
                        key={notification.id}
                        type="button"
                        className={`notification-row ${notification.read ? "" : "notification-row--unread"}`}
                        onClick={() => openNotification(notification)}
                      >
                        {notification.read ? null : <span className="unread-pip" aria-hidden="true" />}
                        <span className="notification-main">
                          <span className="notification-title">{notificationTitle(notification, t)}</span>
                          <span className="notification-sub">
                            {notification.item_title || notification.comment_excerpt || notification.project_name || ""}
                          </span>
                        </span>
                        <span className="notification-time">{formatNotificationTime(notification.created_at)}</span>
                      </button>
                    ))
                  )}
                </div>
                <div className="popover-foot">
                  <Link to="/notifications" className="btn btn--ghost btn--sm btn-block">
                    {t("View all notifications")}
                  </Link>
                </div>
              </div>
            ) : null}

            {menu === "user" ? (
              <div className="popover popover-menu" role="menu" aria-label={t("Account menu")}>
                <div style={{ padding: "8px 10px 10px" }}>
                  <div className="small strong">{user.name}</div>
                  <div className="tiny muted">{user.email}</div>
                  <Badge tone="mono">{roleLabel(user.role, t)}</Badge>
                </div>
                <div className="menu-sep" />
                <Link to="/profile" className="menu-item" role="menuitem">
                  <UserRound /> {t("Profile")}
                </Link>
                <Link to="/notifications" className="menu-item" role="menuitem">
                  <Bell /> {t("Notifications")}
                </Link>
                {isAdmin ? (
                  <Link to="/admin/backup" className="menu-item" role="menuitem">
                    <SlidersHorizontal /> {t("Admin")}
                  </Link>
                ) : null}
                <div className="menu-sep" />
                <button type="button" className="menu-item" role="menuitem" onClick={signOut}>
                  <LogOut /> {t("Sign out")}
                </button>
              </div>
            ) : null}
          </div>
        </header>

        <main id="main" className="content">
          {children}
        </main>
      </div>
    </div>
  );
}

function notificationTitle(notification: Notification, t: (text: string, vars?: Record<string, string | number>) => string) {
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

function formatNotificationTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  return new Intl.DateTimeFormat(undefined, { day: "2-digit", month: "short" }).format(date);
}
