// Entry point: imports the token layer and styles, applies the stored theme
// before the first paint, then mounts the application.
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/inter";
import "@fontsource-variable/space-grotesk";
import "@fontsource-variable/jetbrains-mono";
import "./tokens.css";
import "./styles.css";
import { App } from "./app";
import { applyTheme, storedTheme } from "./preferences";

const root = document.getElementById("root");
if (!root) {
  throw new Error("missing root");
}

// Apply the stored theme before the first paint so a reload does not flash
// the light palette over a dark one.
applyTheme(storedTheme());

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
