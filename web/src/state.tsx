// Shared signed-in state: the current user, the cached project list, the unread
// count and the SSE connection with its polling fallback.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type DependencyList,
  type ReactNode,
} from "react";
import { api, errorMessage } from "./api";
import type { Project, User } from "./types";

type AppValue = {
  user: User;
  setUser: (user: User) => void;
  projects: Project[];
  refreshProjects: () => Promise<void>;
  unread: number;
  refreshUnread: () => Promise<void>;
  /** Increments when a notification arrives or the unread state changes. */
  notificationsVersion: number;
};

const AppContext = createContext<AppValue | null>(null);

export function AppProvider({
  user,
  setUser,
  children,
}: {
  user: User;
  setUser: (user: User) => void;
  children: ReactNode;
}) {
  const [projects, setProjects] = useState<Project[]>([]);
  const [unread, setUnread] = useState(0);
  const [notificationsVersion, setNotificationsVersion] = useState(0);

  const refreshProjects = useCallback(async () => {
    const body = await api<{ projects: Project[] }>("/api/projects");
    setProjects(body.projects);
  }, []);

  const refreshUnread = useCallback(async () => {
    const body = await api<{ count: number }>("/api/notifications/unread-count");
    setUnread(body.count);
  }, []);

  useEffect(() => {
    refreshProjects().catch(() => undefined);
  }, [refreshProjects, user.id]);

  useEffect(() => {
    refreshUnread().catch(() => undefined);
    let closed = false;
    let source: EventSource | null = null;
    let failures = 0;
    let pollTimer: number | null = null;
    const bump = () => setNotificationsVersion((value) => value + 1);

    function stopPolling() {
      if (pollTimer !== null) {
        window.clearInterval(pollTimer);
        pollTimer = null;
      }
    }

    function startPolling() {
      if (pollTimer !== null) {
        return;
      }
      pollTimer = window.setInterval(() => {
        refreshUnread().catch(() => undefined);
        bump();
      }, 60000);
    }

    function applyCount(raw: string) {
      try {
        const payload = JSON.parse(raw) as { count?: unknown };
        if (typeof payload.count === "number") {
          setUnread(payload.count);
        }
      } catch {
        // A malformed frame is ignored: the next full state comes from the API.
      }
    }

    function connect() {
      if (closed) {
        return;
      }
      source = new EventSource("/api/notifications/stream", { withCredentials: true });
      source.addEventListener("ready", (event) => {
        failures = 0;
        stopPolling();
        applyCount((event as MessageEvent<string>).data);
      });
      source.addEventListener("unread", (event) => {
        applyCount((event as MessageEvent<string>).data);
        bump();
      });
      source.addEventListener("notification", (event) => {
        applyCount((event as MessageEvent<string>).data);
        bump();
      });
      source.addEventListener("resync", () => {
        refreshUnread().catch(() => undefined);
        bump();
      });
      source.onerror = () => {
        failures += 1;
        if (failures >= 3) {
          source?.close();
          source = null;
          startPolling();
        }
      };
    }

    function onVisibility() {
      if (document.visibilityState === "visible") {
        refreshUnread().catch(() => undefined);
        bump();
      }
    }

    connect();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      closed = true;
      source?.close();
      stopPolling();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [refreshUnread, user.id]);

  const value = useMemo<AppValue>(
    () => ({ user, setUser, projects, refreshProjects, unread, refreshUnread, notificationsVersion }),
    [user, setUser, projects, refreshProjects, unread, refreshUnread, notificationsVersion],
  );

  return <AppContext.Provider value={value}>{children}</AppContext.Provider>;
}

export function useApp(): AppValue {
  const value = useContext(AppContext);
  if (!value) {
    throw new Error("useApp must be used inside AppProvider");
  }
  return value;
}

export function canWrite(user: User) {
  return user.role !== "guest";
}

export function canCreateProjects(user: User) {
  return user.role === "admin" || user.role === "manager";
}

type LoaderState<T> = {
  data: T | null;
  error: string;
  loading: boolean;
};

/**
 * Loads data and exposes a reload function. The loader is intentionally kept
 * out of the dependency list: callers pass the values that should trigger a
 * reload, and the latest closure is always used.
 */
export function useLoader<T>(loader: () => Promise<T>, deps: DependencyList) {
  const [state, setState] = useState<LoaderState<T>>({ data: null, error: "", loading: true });
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setState((current) => ({ ...current, loading: true, error: "" }));
    loader()
      .then((data) => {
        if (!cancelled) {
          setState({ data, error: "", loading: false });
        }
      })
      .catch((error: unknown) => {
        if (!cancelled) {
          setState({ data: null, error: errorMessage(error), loading: false });
        }
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce]);

  const reload = useCallback(() => setNonce((value) => value + 1), []);
  return { ...state, reload };
}
