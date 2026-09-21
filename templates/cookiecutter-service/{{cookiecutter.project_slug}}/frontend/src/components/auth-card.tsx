import { type ReactNode } from "react";
import { Cube } from "@phosphor-icons/react";

/**
 * The chrome for the two signed-out pages. Sign-in and registration render outside the app shell
 * on purpose: a sidebar full of links to pages you are not allowed to open is worse than no
 * sidebar, and it invites a click that ends in a redirect back here.
 */
export function AuthCard({
  title,
  description,
  children,
  footer,
}: {
  title: string;
  description: string;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <div className="flex min-h-dvh items-center justify-center bg-page p-6">
      <div className="w-full max-w-[400px] rounded-2xl border border-hairline bg-surface p-9 shadow-card">
        <div className="mb-5 flex size-[42px] items-center justify-center rounded-[12px] bg-brand">
          <Cube weight="fill" size={22} className="text-white" />
        </div>
        <h1 className="text-[19px] font-bold tracking-[-0.02em] text-ink">{title}</h1>
        <p className="mt-2 text-[13.5px] leading-[1.55] text-subtext">{description}</p>
        <div className="mt-6">{children}</div>
        {footer && <div className="mt-5 text-[12.5px] text-subtext">{footer}</div>}
      </div>
    </div>
  );
}

/** A label + input pairing used by every form on these pages. Errors get `role="alert"`. */
export function Field({
  id,
  label,
  error,
  children,
}: {
  id: string;
  label: string;
  error?: string;
  children: ReactNode;
}) {
  return (
    <div>
      <label htmlFor={id} className="mb-1 block text-[12.5px] font-medium text-body">
        {label}
      </label>
      {children}
      {error && (
        <p role="alert" className="mt-1 text-[12px] text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}

/**
 * The form-level failure: what the server said, not what we guessed it meant. `role="alert"` so
 * a screen reader announces it without the user hunting for red text.
 */
export function FormError({ children }: { children: ReactNode }) {
  return (
    <p
      role="alert"
      className="rounded-[10px] border border-danger-soft bg-danger-soft px-3 py-2 text-[12.5px] text-destructive"
    >
      {children}
    </p>
  );
}
