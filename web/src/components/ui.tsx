import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  CircleAlert,
  Copy,
  Inbox,
  Loader2,
  X,
} from "lucide-react";
import { useI18n } from "../i18n";

/* ------------------------------------------------------------------ */
/* Buttons                                                             */
/* ------------------------------------------------------------------ */

type ButtonVariant = "primary" | "secondary" | "ghost" | "danger" | "danger-ghost";

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
  size?: "sm";
  icon?: ReactNode;
  block?: boolean;
};

export function Button({
  variant = "secondary",
  size,
  icon,
  block,
  className,
  children,
  type = "button",
  ...rest
}: ButtonProps) {
  const classes = [
    "btn",
    `btn--${variant}`,
    size === "sm" ? "btn--sm" : "",
    block ? "btn-block" : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button type={type} className={classes} {...rest}>
      {icon}
      {children}
    </button>
  );
}

type IconButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  label: string;
  children: ReactNode;
};

export function IconButton({ label, className, children, type = "button", ...rest }: IconButtonProps) {
  return (
    <button
      type={type}
      className={`icon-button ${className ?? ""}`}
      aria-label={label}
      title={label}
      {...rest}
    >
      {children}
    </button>
  );
}

/* ------------------------------------------------------------------ */
/* Form fields                                                         */
/* ------------------------------------------------------------------ */

export function Field({
  label,
  hint,
  error,
  required,
  htmlFor,
  children,
  className,
}: {
  label?: string;
  hint?: string;
  error?: string;
  required?: boolean;
  htmlFor?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={`field ${className ?? ""}`}>
      {label ? (
        <label className="field-label" htmlFor={htmlFor}>
          {label}
          {required ? <span className="req">*</span> : null}
        </label>
      ) : null}
      {children}
      {hint ? <span className="field-hint">{hint}</span> : null}
      {error ? <span className="field-error">{error}</span> : null}
    </div>
  );
}

export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  const { className, ...rest } = props;
  return <input className={`input ${className ?? ""}`} {...rest} />;
}

export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  const { className, children, ...rest } = props;
  return (
    <select className={`select ${className ?? ""}`} {...rest}>
      {children}
    </select>
  );
}

export function Textarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  const { className, ...rest } = props;
  return <textarea className={`textarea ${className ?? ""}`} {...rest} />;
}

export function Checkbox({
  label,
  ...rest
}: InputHTMLAttributes<HTMLInputElement> & { label: ReactNode }) {
  return (
    <label className="checkbox">
      <input type="checkbox" {...rest} />
      <span>{label}</span>
    </label>
  );
}

export function Switch({
  checked,
  onChange,
  disabled,
  label,
  id,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  label: string;
  id?: string;
}) {
  return (
    <label className="switch">
      <input
        id={id}
        type="checkbox"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span className="switch-track" aria-hidden="true" />
      <span className="small">{label}</span>
    </label>
  );
}

/* ------------------------------------------------------------------ */
/* Badges, avatars                                                     */
/* ------------------------------------------------------------------ */

type BadgeTone = "neutral" | "accent" | "good" | "warn" | "bad" | "violet" | "teal" | "mono";

export function Badge({
  tone = "neutral",
  children,
  className,
}: {
  tone?: BadgeTone;
  children: ReactNode;
  className?: string;
}) {
  return <span className={`badge badge--${tone} ${className ?? ""}`}>{children}</span>;
}

export function initials(name: string) {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) {
    return "?";
  }
  if (parts.length === 1) {
    return parts[0].slice(0, 2);
  }
  return `${parts[0][0]}${parts[parts.length - 1][0]}`;
}

export function Avatar({
  name,
  size,
  title,
}: {
  name: string;
  size?: "sm" | "lg";
  title?: string;
}) {
  return (
    <span
      className={`avatar ${size ? `avatar--${size}` : ""}`}
      title={title ?? name}
      aria-hidden={title ? undefined : true}
    >
      {initials(name)}
    </span>
  );
}

export function AvatarStack({
  people,
  max = 4,
}: {
  people: { id: number; name: string }[];
  max?: number;
}) {
  if (people.length === 0) {
    return <span className="tiny muted">—</span>;
  }
  const shown = people.slice(0, max);
  const rest = people.length - shown.length;
  return (
    <span className="avatar-stack" title={people.map((person) => person.name).join(", ")}>
      {shown.map((person) => (
        <Avatar key={person.id} name={person.name} size="sm" />
      ))}
      {rest > 0 ? <span className="tag">+{rest}</span> : null}
    </span>
  );
}

/* ------------------------------------------------------------------ */
/* States                                                              */
/* ------------------------------------------------------------------ */

export function Loading({ label }: { label?: string }) {
  const { t } = useI18n();
  return (
    <div className="loading-block" role="status">
      <Loader2 className="spinner" aria-hidden="true" />
      <span>{label ?? t("Loading…")}</span>
    </div>
  );
}

export function EmptyState({
  icon,
  title,
  children,
  action,
}: {
  icon?: ReactNode;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="empty">
      <span className="empty-icon" aria-hidden="true">
        {icon ?? <Inbox />}
      </span>
      <h3>{title}</h3>
      {children ? <p>{children}</p> : null}
      {action}
    </div>
  );
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  const { t } = useI18n();
  return (
    <div className="banner banner--bad" role="alert">
      <CircleAlert />
      <div className="grow">
        <strong>{t("Something went wrong")}</strong>
        <div className="small">{message}</div>
      </div>
      {onRetry ? (
        <Button variant="secondary" size="sm" onClick={onRetry}>
          {t("Retry")}
        </Button>
      ) : null}
    </div>
  );
}

export function InlineError({ message }: { message: string }) {
  if (!message) {
    return null;
  }
  return (
    <div className="banner banner--bad" role="alert">
      <AlertTriangle />
      <span>{message}</span>
    </div>
  );
}

export function PageHead({
  title,
  lede,
  actions,
}: {
  title: ReactNode;
  lede?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="page-head">
      <div className="grow">
        <h1 className="page-title">{title}</h1>
        {lede ? <p className="page-lede">{lede}</p> : null}
      </div>
      {actions ? <div className="page-actions">{actions}</div> : null}
    </header>
  );
}

export function SectionHead({
  title,
  lede,
  actions,
}: {
  title: string;
  lede?: string;
  actions?: ReactNode;
}) {
  return (
    <div className="section-head">
      <div className="grow">
        <h2 className="section-title">{title}</h2>
        {lede ? <p className="section-lede">{lede}</p> : null}
      </div>
      {actions}
    </div>
  );
}

/* ------------------------------------------------------------------ */
/* Modal                                                               */
/* ------------------------------------------------------------------ */

export function Modal({
  open,
  title,
  onClose,
  children,
  footer,
  wide,
}: {
  open: boolean;
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) {
      return;
    }
    if (open && !dialog.open) {
      dialog.showModal();
    } else if (!open && dialog.open) {
      dialog.close();
    }
  }, [open]);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) {
      return;
    }
    const handleCancel = (event: Event) => {
      event.preventDefault();
      onClose();
    };
    dialog.addEventListener("cancel", handleCancel);
    return () => dialog.removeEventListener("cancel", handleCancel);
  }, [onClose]);

  return (
    <dialog
      ref={ref}
      className={`modal ${wide ? " modal--wide" : ""}`}
      onClick={(event) => {
        if (event.target === ref.current) {
          onClose();
        }
      }}
    >
      <div className="modal-head">
        <h2 className="modal-title">{title}</h2>
        <IconButton label="Close" onClick={onClose}>
          <X />
        </IconButton>
      </div>
      <div className="modal-body">{children}</div>
      {footer ? <div className="modal-foot">{footer}</div> : null}
    </dialog>
  );
}

export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel,
  tone = "danger",
  busy,
  onConfirm,
  onClose,
}: {
  open: boolean;
  title: string;
  body: string;
  confirmLabel: string;
  tone?: "danger" | "primary";
  busy?: boolean;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  return (
    <Modal
      open={open}
      title={title}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            {t("Cancel")}
          </Button>
          <Button variant={tone} onClick={onConfirm} disabled={busy}>
            {busy ? <Loader2 className="spinner" aria-hidden="true" /> : null}
            {confirmLabel}
          </Button>
        </>
      }
    >
      <p className="small muted">{body}</p>
    </Modal>
  );
}

/* ------------------------------------------------------------------ */
/* Toasts                                                              */
/* ------------------------------------------------------------------ */

type ToastTone = "default" | "good" | "bad";
type ToastItem = { id: number; message: string; tone: ToastTone };

type ToastValue = {
  notify: (message: string, tone?: ToastTone) => void;
};

const ToastContext = createContext<ToastValue | null>(null);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastItem[]>([]);
  const timers = useRef<number[]>([]);

  useEffect(() => {
    const pending = timers.current;
    return () => {
      pending.forEach((timer) => window.clearTimeout(timer));
    };
  }, []);

  const notify = useCallback((message: string, tone: ToastTone = "default") => {
    const id = Date.now() + Math.random();
    setToasts((current) => [...current, { id, message, tone }]);
    const timer = window.setTimeout(() => {
      setToasts((current) => current.filter((toast) => toast.id !== id));
    }, 4200);
    timers.current.push(timer);
  }, []);

  const value = useMemo<ToastValue>(() => ({ notify }), [notify]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="toast-region" aria-live="polite" aria-atomic="false">
        {toasts.map((toast) => (
          <div key={toast.id} className={`toast toast--${toast.tone}`}>
            {toast.tone === "good" ? <CheckCircle2 /> : toast.tone === "bad" ? <AlertTriangle /> : <Check />}
            <span className="grow">{toast.message}</span>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}

export function useToast(): ToastValue {
  const value = useContext(ToastContext);
  if (!value) {
    throw new Error("useToast must be used inside ToastProvider");
  }
  return value;
}

/* ------------------------------------------------------------------ */
/* Copy field                                                          */
/* ------------------------------------------------------------------ */

export function CopyField({ value, label }: { value: string; label?: string }) {
  const { t } = useI18n();
  const { notify } = useToast();

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      notify(t("Copied"), "good");
    } catch {
      const area = document.createElement("textarea");
      area.value = value;
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.appendChild(area);
      area.select();
      document.execCommand("copy");
      document.body.removeChild(area);
      notify(t("Copied"), "good");
    }
  }

  return (
    <div className="copy-field">
      <input className="input" value={value} readOnly aria-label={label ?? value} onFocus={(event) => event.target.select()} />
      <Button variant="secondary" icon={<Copy />} onClick={copy}>
        {t("Copy")}
      </Button>
    </div>
  );
}
