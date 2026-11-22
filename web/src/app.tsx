// Application shell: boot gate, session loading, theme and locale providers,
// and the route table that maps URLs to screens.
import { useCallback, useEffect, useState, type ReactNode } from "react";
import {
  AlertTriangle,
  Compass,
  ShieldAlert,
  WifiOff,
} from "lucide-react";
import { api, ApiError, errorMessage } from "./api";
import { I18nProvider, useI18n } from "./i18n";
import {
  PreferenceControls,
  ThemeProvider,
  detectLocale,
  persistLocale,
  storedLocale,
} from "./preferences";
import { ParamsProvider, RouterProvider, matchPath, useRouter } from "./router";
import { AppProvider } from "./state";
import type { Locale, User } from "./types";
import { Shell } from "./components/shell";
import { Button, Loading, PageHead, ToastProvider } from "./components/ui";
import { LoginScreen, InviteScreen, SetupScreen } from "./screens/auth";
import { MyWorkScreen } from "./screens/my-work";
import { SearchScreen } from "./screens/search";
import { CalendarScreen, ProjectCalendarScreen } from "./screens/calendar";
import { NewProjectScreen, ProjectSettingsScreen, ProjectsScreen } from "./screens/projects";
import { ProjectScreen } from "./screens/project";
import { ItemDetailScreen, NewItemScreen } from "./screens/item";
import { InvitationsScreen, PeopleScreen } from "./screens/people";
import { TeamScreen, TeamsScreen } from "./screens/teams";
import { AutomationsScreen } from "./screens/automations";
import { NotificationsScreen } from "./screens/notifications";
import { ReportsScreen } from "./screens/reports";
import { ProfileScreen } from "./screens/profile";
import { AdminScreen } from "./screens/admin";

type BootState = "loading" | "ready" | "offline";

export function App() {
  const [boot, setBoot] = useState<BootState>("loading");
  const [setupCompleted, setSetupCompleted] = useState(true);
  const [user, setUser] = useState<User | null>(null);
  const [bootError, setBootError] = useState("");
  // Before a session exists the visitor's own choice wins, then the browser
  // language, then English. Once the profile loads it takes over below.
  const [locale, setLocale] = useState<Locale>(() => storedLocale() ?? detectLocale());

  useEffect(() => {
    if (user) {
      setLocale(user.locale);
    }
  }, [user]);

  const changeLocale = useCallback(
    (next: Locale) => {
      setLocale(next);
      persistLocale(next);
      if (user && next !== user.locale) {
        api<User>("/api/me", {
          method: "PUT",
          body: {
            name: user.name,
            locale: next,
            email_notifications: user.email_notifications ? 1 : 0,
          },
        })
          .then((updated) => {
            setUser((current) => (current ? { ...current, ...updated } : current));
          })
          .catch(() => {
            // The interface keeps the choice for this session; the profile
            // screen is the place to retry the server-side preference.
          });
      }
    },
    [user],
  );

  useEffect(() => {
    let cancelled = false;

    async function start() {
      try {
        const setup = await api<{ setup_completed: boolean }>("/api/setup");
        if (cancelled) {
          return;
        }
        setSetupCompleted(setup.setup_completed);
        if (setup.setup_completed) {
          try {
            const me = await api<User>("/api/me");
            if (!cancelled) {
              setUser(me);
            }
          } catch (error) {
            if (!(error instanceof ApiError) || (error.status !== 401 && error.status !== 403)) {
              throw error;
            }
          }
        }
        if (!cancelled) {
          setBoot("ready");
        }
      } catch (error) {
        if (!cancelled) {
          setBootError(errorMessage(error));
          setBoot("offline");
        }
      }
    }

    void start();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    function expired() {
      setUser(null);
    }
    window.addEventListener("together:auth-expired", expired);
    return () => window.removeEventListener("together:auth-expired", expired);
  }, []);

  useEffect(() => {
    if (user) {
      document.title = `${user.name} · Together`;
    } else {
      document.title = "Together";
    }
  }, [user]);

  let content: ReactNode;
  if (boot === "loading") {
    content = <BootGate title="Starting" text="Checking the server." />;
  } else if (boot === "offline") {
    content = (
      <BootGate title="Server unavailable" text="The app could not reach its API.">
        {bootError ? <p className="tiny muted">{bootError}</p> : null}
        <ReloadButton />
      </BootGate>
    );
  } else {
    content = (
      <RouterProvider>
        <ToastProvider>
          <AppRoutes
            user={user}
            setUser={setUser}
            setupCompleted={setupCompleted}
            onSetupDone={() => setSetupCompleted(true)}
          />
        </ToastProvider>
      </RouterProvider>
    );
  }

  return (
    <ThemeProvider>
      <I18nProvider locale={locale} setLocale={changeLocale}>
        {content}
      </I18nProvider>
    </ThemeProvider>
  );
}

function AppRoutes({
  user,
  setUser,
  setupCompleted,
  onSetupDone,
}: {
  user: User | null;
  setUser: (user: User | null) => void;
  setupCompleted: boolean;
  onSetupDone: () => void;
}) {
  const { path, navigate } = useRouter();

  if (!setupCompleted) {
    if (path !== "/setup") {
      return <Redirect to="/setup" />;
    }
    return (
      <SetupScreen
        onDone={(created) => {
          onSetupDone();
          setUser(created);
          navigate("/", { replace: true });
        }}
      />
    );
  }

  if (path === "/setup") {
    return <Redirect to={user ? "/" : "/login"} />;
  }

  const invite = matchPath("/invite/:token", path);
  if (invite) {
    return (
      <InviteScreen
        token={invite.token}
        onDone={(created) => {
          setUser(created);
          navigate("/", { replace: true });
        }}
      />
    );
  }

  if (!user) {
    return (
      <LoginScreen
        onDone={(signedIn) => {
          setUser(signedIn);
          navigate("/", { replace: true });
        }}
      />
    );
  }

  if (path === "/login") {
    return <Redirect to="/" />;
  }

  return (
    <AppProvider user={user} setUser={setUser}>
      <Shell
        onSignOut={() => {
          setUser(null);
          navigate("/login", { replace: true });
        }}
      >
        <RouteContent path={path} user={user} />
      </Shell>
    </AppProvider>
  );
}

function RouteContent({ path, user }: { path: string; user: User }) {
  const { t } = useI18n();
  const isAdmin = user.role === "admin";
  const isManager = user.role === "manager";

  if (isAdminOnly(path) && !isAdmin) {
    return (
      <Forbidden
        title={t("Access denied")}
        body={t("This area is reserved for administrators.")}
      />
    );
  }
  if (isAutomationPath(path) && !isAdmin && !isManager) {
    return (
      <Forbidden
        title={t("Access denied")}
        body={t("Automations are available to administrators and managers.")}
      />
    );
  }

  if (path === "/") {
    return <MyWorkScreen />;
  }
  if (path === "/search") {
    return <SearchScreen />;
  }
  if (path === "/calendar") {
    return <CalendarScreen />;
  }
  if (path === "/notifications") {
    return <NotificationsScreen />;
  }
  if (path === "/reports") {
    return <ReportsScreen />;
  }
  if (path === "/profile") {
    return <ProfileScreen />;
  }
  if (path === "/projects") {
    return <ProjectsScreen />;
  }
  if (path === "/projects/new") {
    return <NewProjectScreen />;
  }
  if (path === "/people") {
    return <PeopleScreen />;
  }
  if (path === "/people/invites") {
    return <InvitationsScreen />;
  }
  if (path === "/teams") {
    return <TeamsScreen />;
  }
  if (path === "/automations") {
    return <AutomationsScreen />;
  }
  if (path === "/admin/backup") {
    return <AdminScreen />;
  }

  const projectItemNew = matchPath("/projects/:id/items/new", path);
  if (projectItemNew) {
    return withParams(projectItemNew, <NewItemScreen />);
  }
  const projectItem = matchPath("/projects/:id/items/:item", path);
  if (projectItem) {
    return withParams(projectItem, <ItemDetailScreen />);
  }
  const projectCalendar = matchPath("/projects/:id/calendar", path);
  if (projectCalendar) {
    return withParams(projectCalendar, <ProjectCalendarScreen />);
  }
  const projectSettings = matchPath("/projects/:id/settings", path);
  if (projectSettings) {
    return withParams(projectSettings, <ProjectSettingsScreen />);
  }
  const project = matchPath("/projects/:id", path);
  if (project) {
    return withParams(project, <ProjectScreen />);
  }
  const team = matchPath("/teams/:id", path);
  if (team) {
    return withParams(team, <TeamScreen />);
  }

  return (
    <Forbidden
      icon={<Compass />}
      title={t("Page not found")}
      body={t("The page you asked for does not exist.")}
    />
  );
}

function withParams(params: Record<string, string>, node: ReactNode) {
  return <ParamsProvider value={params}>{node}</ParamsProvider>;
}

function isAdminOnly(path: string) {
  return (
    path === "/people" ||
    path === "/people/invites" ||
    path === "/teams" ||
    path.startsWith("/teams/") ||
    path === "/admin/backup"
  );
}

function isAutomationPath(path: string) {
  return path === "/automations";
}

function Forbidden({
  title,
  body,
  icon,
}: {
  title: string;
  body: string;
  icon?: ReactNode;
}) {
  const { t } = useI18n();
  return (
    <>
      <PageHead title={title} />
      <div className="card card-pad">
        <div className="empty" style={{ padding: 24 }}>
          <span className="empty-icon" aria-hidden="true">
            {icon ?? <ShieldAlert />}
          </span>
          <h3>{title}</h3>
          <p>{body}</p>
          <a href="/" className="btn btn--secondary">
            {t("My work")}
          </a>
        </div>
      </div>
    </>
  );
}

function ReloadButton() {
  const { t } = useI18n();
  return (
    <Button variant="secondary" icon={<WifiOff />} onClick={() => window.location.reload()}>
      {t("Reload")}
    </Button>
  );
}

function Redirect({ to }: { to: string }) {
  const { navigate } = useRouter();
  useEffect(() => {
    navigate(to, { replace: true });
  }, [navigate, to]);
  return <Loading />;
}

function BootGate({
  title,
  text,
  children,
}: {
  title: string;
  text: string;
  children?: ReactNode;
}) {
  const { t } = useI18n();
  return (
    <main className="gate">
      <PreferenceControls className="gate-prefs" />
      <p className="mark">
        <AlertTriangle /> Together
      </p>
      <h1>{t(title)}</h1>
      <p className="status">{t(text)}</p>
      {children ? <div className="gate-body">{children}</div> : null}
    </main>
  );
}
