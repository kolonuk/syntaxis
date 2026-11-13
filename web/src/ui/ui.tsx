// SPDX-License-Identifier: GPL-2.0-or-later
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type SelectHTMLAttributes,
  type TextareaHTMLAttributes,
} from "react";
import "./theme.css";

type Variant = "primary" | "secondary" | "danger" | "ghost" | "icon";

export function Button({
  variant = "secondary",
  loading = false,
  children,
  disabled,
  className = "",
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; loading?: boolean }) {
  const cls = `btn btn-${variant === "icon" ? "ghost btn-icon" : variant} ${className}`;
  return (
    <button
      {...rest}
      className={cls}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
    >
      {loading && <span className="spinner" aria-hidden="true" />}
      {children}
    </button>
  );
}

export function Field({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: (id: string) => ReactNode;
}) {
  const id = useId();
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {children(id)}
      {hint && <span className="hint">{hint}</span>}
    </div>
  );
}

export function Input({
  label,
  hint,
  ...p
}: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string }) {
  return (
    <Field label={label} hint={hint}>
      {(id) => <input id={id} className="input" {...p} />}
    </Field>
  );
}

export function Select({
  label,
  options,
  ...p
}: SelectHTMLAttributes<HTMLSelectElement> & {
  label: string;
  options: { value: string; label: string }[];
}) {
  return (
    <Field label={label}>
      {(id) => (
        <select id={id} className="select" {...p}>
          {options.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      )}
    </Field>
  );
}

export function TextArea({
  label,
  ...p
}: TextareaHTMLAttributes<HTMLTextAreaElement> & { label: string }) {
  return <Field label={label}>{(id) => <textarea id={id} className="textarea" {...p} />}</Field>;
}

export function Toggle({
  label,
  checked,
  onChange,
}: {
  label: string;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <label className="toggle">
      <input
        type="checkbox"
        role="switch"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="track" aria-hidden="true" />
      <span>{label}</span>
    </label>
  );
}

export const Card = ({ title, children }: { title?: string; children: ReactNode }) => (
  <section className="card">
    {title && <h2>{title}</h2>}
    {children}
  </section>
);

export type Tone = "neutral" | "success" | "warn" | "danger" | "info";
export const Badge = ({ tone = "neutral", children }: { tone?: Tone; children: ReactNode }) => (
  <span className={`badge ${tone === "neutral" ? "" : "badge-" + tone}`}>{children}</span>
);

export function StatusPill({ state }: { state: string }) {
  const tone: Tone =
    state === "running"
      ? "info"
      : state === "error"
        ? "danger"
        : state === "needs_input"
          ? "warn"
          : "success";
  const label = state === "needs_input" ? "needs input" : state;
  return <Badge tone={tone}>{label}</Badge>;
}

export function Tabs({
  tabs,
  value,
  onChange,
}: {
  tabs: { id: string; label: string }[];
  value: string;
  onChange: (id: string) => void;
}) {
  return (
    <div role="tablist" className="tabs">
      {tabs.map((t) => (
        <button
          key={t.id}
          role="tab"
          className="tab"
          aria-selected={t.id === value}
          onClick={() => onChange(t.id)}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}

export function Modal({
  title,
  onClose,
  children,
  actions,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  actions?: ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null;
    ref.current?.querySelector<HTMLElement>("input,select,textarea,button")?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      prev?.focus();
    };
  }, [onClose]);
  return (
    <div className="modal-backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-modal="true" aria-label={title} ref={ref}>
        <h2>{title}</h2>
        {children}
        {actions && <div className="actions">{actions}</div>}
      </div>
    </div>
  );
}

export function Tooltip({ text, children }: { text: string; children: ReactNode }) {
  return (
    <span className="tooltip">
      {children}
      <span className="tip" role="tooltip">
        {text}
      </span>
    </span>
  );
}

export function Meter({ label, used, limit }: { label: string; used: number; limit: number }) {
  const unlimited = !limit || limit <= 0;
  const p = unlimited ? 0 : Math.min(100, Math.round((used / limit) * 100));
  const tone = p >= 95 ? "danger" : p >= 80 ? "warn" : "";
  return (
    <div>
      <div className="row">
        <span className="grow">{label}</span>
        <span className="muted">
          {unlimited ? `${used} used (no limit)` : `${used} / ${limit}`}
        </span>
      </div>
      <div
        className={`meter ${tone}`}
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={p}
      >
        <span style={{ width: `${p}%` }} />
      </div>
    </div>
  );
}

export const EmptyState = ({
  title,
  hint,
  action,
}: {
  title: string;
  hint?: string;
  action?: ReactNode;
}) => (
  <div className="empty">
    <h3>{title}</h3>
    {hint && <p>{hint}</p>}
    {action}
  </div>
);

export const Skeleton = ({ lines = 3 }: { lines?: number }) => (
  <div role="status" aria-busy="true" aria-label="Loading">
    {Array.from({ length: lines }).map((_, i) => (
      <div
        key={i}
        className="skeleton"
        style={{ height: 14, margin: "10px 0", width: `${90 - i * 12}%` }}
      />
    ))}
  </div>
);

export function Banner({
  tone = "info",
  children,
}: {
  tone?: "info" | "warn" | "danger";
  children: ReactNode;
}) {
  return (
    <div className={`banner banner-${tone}`} role={tone === "info" ? "status" : "alert"}>
      {children}
    </div>
  );
}

// ---- toasts ----
interface ToastItem {
  id: number;
  text: string;
  tone: "info" | "error" | "success";
}
const ToastCtx = createContext<(text: string, tone?: ToastItem["tone"]) => void>(() => undefined);
export const useToast = () => useContext(ToastCtx);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const push = useCallback((text: string, tone: ToastItem["tone"] = "info") => {
    const id = Date.now() + Math.random();
    setItems((x) => [...x, { id, text, tone }]);
    setTimeout(() => setItems((x) => x.filter((i) => i.id !== id)), 5000);
  }, []);
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toasts" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className={`toast toast-${t.tone}`} role="status">
            {t.text}
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}

/** Registers keyboard shortcuts; ignored while typing in a field. */
export function useShortcuts(map: Record<string, () => void>) {
  const ref = useRef(map);
  ref.current = map;
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (el && /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) return;
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      ref.current[e.key]?.();
    };
    document.addEventListener("keydown", on);
    return () => document.removeEventListener("keydown", on);
  }, []);
}
