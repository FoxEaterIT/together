// Language and theme preferences: first-visit detection, localStorage and
// account persistence, the providers and the picker controls.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { Moon, Sun } from "lucide-react";
import { useI18n } from "./i18n";
import { IconButton, Select } from "./components/ui";
import type { Locale } from "./types";

export type Theme = "light" | "dark";

const THEME_KEY = "together.theme";
const LOCALE_KEY = "together.locale";

export const LOCALES: Locale[] = ["en", "it", "es"];

// Language names stay in their own language so the picker is readable even
// when the interface is currently in a language the reader does not know.
export const LOCALE_NAMES: Record<Locale, string> = {
  en: "English",
  it: "Italiano",
  es: "Español",
};

export function isLocale(value: unknown): value is Locale {
  return value === "en" || value === "it" || value === "es";
}

// First visit: the browser decides. Unknown languages fall back to English.
export function detectLocale(): Locale {
  if (typeof navigator === "undefined") {
    return "en";
  }
  const candidates = navigator.languages?.length ? navigator.languages : [navigator.language];
  for (const tag of candidates) {
    if (!tag) {
      continue;
    }
    const base = tag.toLowerCase().split("-")[0];
    if (isLocale(base)) {
      return base;
    }
  }
  return "en";
}

export function storedLocale(): Locale | null {
  try {
    const value = window.localStorage.getItem(LOCALE_KEY);
    return isLocale(value) ? value : null;
  } catch {
    return null;
  }
}

export function persistLocale(locale: Locale) {
  try {
    window.localStorage.setItem(LOCALE_KEY, locale);
  } catch {
    // Private modes can refuse storage; the in-memory choice still applies.
  }
}

export function storedTheme(): Theme {
  try {
    const value = window.localStorage.getItem(THEME_KEY);
    if (value === "light" || value === "dark") {
      return value;
    }
  } catch {
    // Fall through to the system preference.
  }
  return typeof window !== "undefined" && window.matchMedia?.("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light";
}

export function persistTheme(theme: Theme) {
  try {
    window.localStorage.setItem(THEME_KEY, theme);
  } catch {
    // See persistLocale.
  }
}

export function applyTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
}

type ThemeValue = {
  theme: Theme;
  setTheme: (theme: Theme) => void;
  toggleTheme: () => void;
};

const ThemeContext = createContext<ThemeValue | null>(null);

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(() => storedTheme());

  useEffect(() => {
    applyTheme(theme);
    persistTheme(theme);
  }, [theme]);

  const toggleTheme = useCallback(() => {
    setTheme((current) => (current === "dark" ? "light" : "dark"));
  }, []);

  const value = useMemo<ThemeValue>(() => ({ theme, setTheme, toggleTheme }), [theme, toggleTheme]);

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeValue {
  const value = useContext(ThemeContext);
  if (!value) {
    throw new Error("useTheme must be used inside ThemeProvider");
  }
  return value;
}

export function ThemeToggle() {
  const { theme, toggleTheme } = useTheme();
  const { t } = useI18n();
  const label = theme === "dark" ? t("Switch to light theme") : t("Switch to dark theme");
  return (
    <IconButton label={label} onClick={toggleTheme}>
      {theme === "dark" ? <Sun aria-hidden="true" /> : <Moon aria-hidden="true" />}
    </IconButton>
  );
}

export function LanguageSelect({ className }: { className?: string }) {
  const { locale, setLocale, t } = useI18n();
  return (
    <Select
      className={`lang-select ${className ?? ""}`}
      value={locale}
      aria-label={t("Language")}
      onChange={(event) => setLocale(event.target.value as Locale)}
    >
      {LOCALES.map((value) => (
        <option key={value} value={value}>
          {LOCALE_NAMES[value]}
        </option>
      ))}
    </Select>
  );
}

export function PreferenceControls({ className }: { className?: string }) {
  return (
    <div className={`prefs ${className ?? ""}`}>
      <LanguageSelect />
      <ThemeToggle />
    </div>
  );
}
