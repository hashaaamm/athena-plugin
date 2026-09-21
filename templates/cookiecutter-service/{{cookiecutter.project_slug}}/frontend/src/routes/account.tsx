import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { useNavigate } from "@tanstack/react-router";
import { SignOut } from "@phosphor-icons/react";
import { toast } from "sonner";
import { z } from "zod";

import { Field, FormError } from "@/components/auth-card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PageSpinner } from "@/components/ui/spinner";
import { ApiError } from "@/lib/api/client";
import { useChangePassword, useMe, useSignOut, type User } from "@/lib/api/auth";
import { useAccessToken } from "@/lib/use-session";

/**
 * The signed-in user, and the one thing they can do to their account here.
 *
 * `GET /auth/me` reads the row rather than trusting the token's claims, so this page shows what
 * is currently true about the account — a user deactivated four minutes ago is not "fine" here.
 * It is also why there is nothing worth caching for long: the request is the point.
 */

// Mirrors app/schemas/auth.py, like the registration form does. The current password is not
// bounded here for the same reason it is not bounded there: it is checked against a stored
// digest, and a bound would tell the caller how long the real password is not.
const MIN_PASSWORD_LENGTH = 12;
const MAX_PASSWORD_LENGTH = 1024;

const schema = z.object({
  current_password: z.string().min(1, "Enter your current password"),
  new_password: z
    .string()
    .min(MIN_PASSWORD_LENGTH, `At least ${MIN_PASSWORD_LENGTH} characters`)
    .max(MAX_PASSWORD_LENGTH, `At most ${MAX_PASSWORD_LENGTH} characters`),
});

type Values = z.infer<typeof schema>;

const EMPTY: Values = { current_password: "", new_password: "" };

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-4 border-b border-hairline py-[10px] last:border-b-0">
      <span className="text-[12.5px] text-subtext">{label}</span>
      <span className="truncate font-mono text-[12.5px] text-ink">{value}</span>
    </div>
  );
}

function ChangePasswordForm() {
  const change = useChangePassword();
  const {
    register,
    handleSubmit,
    reset,
    setError,
    formState: { errors, isSubmitting },
  } = useForm<Values>({ resolver: zodResolver(schema), defaultValues: EMPTY });

  const submit = handleSubmit(async (values) => {
    try {
      await change.mutateAsync(values);
      reset(EMPTY);
      toast.success("Password changed.");
    } catch (err) {
      // 403, not 401, and the distinction is the backend's whole point: the session is valid and
      // only this operation was refused. A 401 here would have logged the user out through the
      // middleware in `lib/api/client.ts` and thrown away the form they were filling in.
      if (err instanceof ApiError && err.status === 403) {
        setError("current_password", { message: err.message });
        return;
      }
      setError("root", {
        message: err instanceof ApiError ? err.message : "Could not change the password.",
      });
    }
  });

  return (
    <form onSubmit={submit} className="mt-4 flex flex-col gap-4" noValidate>
      {errors.root && <FormError>{errors.root.message}</FormError>}

      <Field
        id="current_password"
        label="Current password"
        error={errors.current_password?.message}
      >
        <Input
          id="current_password"
          type="password"
          autoComplete="current-password"
          aria-invalid={Boolean(errors.current_password)}
          {...register("current_password")}
        />
      </Field>

      <Field id="new_password" label="New password" error={errors.new_password?.message}>
        <Input
          id="new_password"
          type="password"
          autoComplete="new-password"
          aria-invalid={Boolean(errors.new_password)}
          {...register("new_password")}
        />
      </Field>

      <div>
        <Button type="submit" disabled={isSubmitting}>
          {isSubmitting ? "Changing…" : "Change password"}
        </Button>
      </div>

      <p className="text-[12px] leading-relaxed text-faint">
        Your current sign-in keeps working. This backend issues no refresh tokens and stores no
        sessions, so a password change cannot end one that is already open — ask Athena for the
        JWT authentication guide, where revocation is a step with a table behind it.
      </p>
    </form>
  );
}

function AccountDetails({ user }: { user: User }) {
  return (
    <div className="mt-4">
      <Detail label="Email" value={user.email} />
      <Detail label="User ID" value={user.id} />
      <Detail label="Status" value={user.is_active ? "active" : "deactivated"} />
      <Detail label="Registered" value={new Date(user.created_at).toLocaleString("en-GB")} />
    </div>
  );
}

export function AccountPage() {
  const token = useAccessToken();
  const me = useMe(token);
  const signOut = useSignOut();
  const navigate = useNavigate();

  async function onSignOut() {
    signOut();
    await navigate({ to: "/login" });
  }

  return (
    <div className="mx-auto max-w-[680px] px-10 pb-[60px] pt-8">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-[26px] font-bold tracking-[-0.03em] text-ink">Account</h1>
          <p className="mt-1 text-[13.5px] text-subtext">
            Read from <code className="font-mono text-[12.5px]">GET /api/v1/auth/me</code> on every
            visit, not decoded from the token.
          </p>
        </div>
        <Button variant="outline" onClick={onSignOut}>
          <SignOut weight="bold" />
          Sign out
        </Button>
      </div>

      <section className="mt-7 rounded-2xl border border-hairline bg-surface p-[26px]">
        <h2 className="text-[15px] font-semibold text-ink">Who you are signed in as</h2>
        {me.isPending && <PageSpinner className="min-h-[120px]" />}
        {me.isError && (
          <FormError>
            {me.error instanceof ApiError
              ? me.error.message
              : "Could not load your account."}
          </FormError>
        )}
        {me.data && <AccountDetails user={me.data} />}
      </section>

      <section className="mt-6 rounded-2xl border border-hairline bg-surface p-[26px]">
        <h2 className="text-[15px] font-semibold text-ink">Change your password</h2>
        <p className="mt-1 text-[13px] text-subtext">
          A valid session is not enough. Prove you know the current one.
        </p>
        <ChangePasswordForm />
      </section>
    </div>
  );
}
