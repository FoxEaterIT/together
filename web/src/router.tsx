// Minimal history router: path matching with parameters, typed links and
// programmatic navigation. The Go server serves index.html for every non-API
// path, so deep links survive a reload.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type AnchorHTMLAttributes,
  type MouseEvent,
  type ReactNode,
} from "react";

type Location = {
  path: string;
  search: string;
  full: string;
};

type RouterValue = Location & {
  navigate: (to: string, options?: { replace?: boolean }) => void;
};

const RouterContext = createContext<RouterValue | null>(null);

function readLocation(): Location {
  return {
    path: window.location.pathname || "/",
    search: window.location.search,
    full: `${window.location.pathname || "/"}${window.location.search}`,
  };
}

export function RouterProvider({ children }: { children: ReactNode }) {
  const [location, setLocation] = useState<Location>(readLocation);

  useEffect(() => {
    const onPop = () => setLocation(readLocation());
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const navigate = useCallback((to: string, options?: { replace?: boolean }) => {
    const target = to.startsWith("/") ? to : `/${to}`;
    if (target === `${window.location.pathname}${window.location.search}`) {
      return;
    }
    window.history[options?.replace ? "replaceState" : "pushState"](null, "", target);
    setLocation(readLocation());
    window.scrollTo({ top: 0, behavior: "auto" });
  }, []);

  const value = useMemo<RouterValue>(() => ({ ...location, navigate }), [location, navigate]);

  return <RouterContext.Provider value={value}>{children}</RouterContext.Provider>;
}

export function useRouter(): RouterValue {
  const value = useContext(RouterContext);
  if (!value) {
    throw new Error("useRouter must be used inside RouterProvider");
  }
  return value;
}

type LinkProps = AnchorHTMLAttributes<HTMLAnchorElement> & { to: string };

export function Link({ to, onClick, children, ...rest }: LinkProps) {
  const { navigate } = useRouter();
  function handleClick(event: MouseEvent<HTMLAnchorElement>) {
    onClick?.(event);
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return;
    }
    event.preventDefault();
    navigate(to);
  }
  return (
    <a href={to} onClick={handleClick} {...rest}>
      {children}
    </a>
  );
}

export function useQueryState() {
  const { search } = useRouter();
  return useMemo(() => new URLSearchParams(search), [search]);
}

const ParamsContext = createContext<Record<string, string>>({});

export function ParamsProvider({
  value,
  children,
}: {
  value: Record<string, string>;
  children: ReactNode;
}) {
  return <ParamsContext.Provider value={value}>{children}</ParamsContext.Provider>;
}

export function useParams(): Record<string, string> {
  return useContext(ParamsContext);
}

export function matchPath(pattern: string, path: string): Record<string, string> | null {
  const patternParts = pattern.split("/").filter(Boolean);
  const pathParts = path.replace(/\/+$/, "").split("/").filter(Boolean);
  if (patternParts.length !== pathParts.length) {
    return null;
  }
  const params: Record<string, string> = {};
  for (let index = 0; index < patternParts.length; index += 1) {
    const expected = patternParts[index];
    const actual = pathParts[index];
    if (expected.startsWith(":")) {
      params[expected.slice(1)] = decodeURIComponent(actual);
    } else if (expected !== actual) {
      return null;
    }
  }
  return params;
}
