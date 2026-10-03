import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";

type Variant = "act" | "outline" | "ghost" | "danger" | "agent";
type BtnSize = "sm" | "md" | "lg";

const variants: Record<Variant, string> = {
  // The one saturated yellow: things you can act on.
  act: "bg-act text-act-ink stroke shadow-pop press hover:brightness-[1.03]",
  outline: "bg-surface text-ink stroke shadow-pop-sm press hover:bg-sunken",
  ghost: "bg-transparent text-ink hover:bg-sunken",
  danger: "bg-surface text-danger stroke press hover:bg-danger-bg",
  agent: "bg-agent-fill text-ink stroke shadow-pop-sm press",
};
const sizes: Record<BtnSize, string> = {
  sm: "min-h-11 px-3 text-sm gap-1.5 rounded-full",        // 44px: never smaller
  md: "min-h-11 px-4 text-[15px] gap-2 rounded-full",
  lg: "min-h-13 px-5 text-base gap-2 rounded-2xl w-full",
};

export const Button = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: BtnSize; icon?: ReactNode }>(
  function Button({ variant = "outline", size = "md", icon, className = "", children, type = "button", ...rest }, ref) {
    return (
      <button ref={ref} type={type} {...rest}
        className={"inline-flex items-center justify-center font-semibold select-none disabled:pointer-events-none disabled:bg-sunken disabled:text-muted disabled:shadow-none " + variants[variant] + " " + sizes[size] + " " + className}>
        {icon}{children}
      </button>
    );
  });

/** IconButton: a 44px round target with a visible label for screen readers. */
export const IconButton = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement> & { label: string; badge?: number }>(
  function IconButton({ label, badge, className = "", children, type = "button", ...rest }, ref) {
    return (
      <button ref={ref} type={type} aria-label={label} title={label} {...rest}
        className={"relative inline-grid place-items-center size-11 rounded-full text-ink hover:bg-sunken disabled:opacity-40 " + className}>
        {children}
        {!!badge && <span className="absolute -top-0.5 -right-0.5 min-w-5 h-5 px-1 grid place-items-center rounded-full bg-danger text-white text-[11px] font-bold tnum">{badge > 99 ? "99+" : badge}</span>}
      </button>
    );
  });
