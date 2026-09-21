import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Link, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { z } from "zod";

import { AuthCard, Field, FormError } from "@/components/auth-card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiError } from "@/lib/api/client";
import { useRegister, useSignIn } from "@/lib/api/auth";

/**
 * Create an account, then sign in with it.
 *
 * Two requests, because `POST /auth/register` returns the user and no session — the backend will
 * not hand out a token it was not asked for. Doing the second leg here rather than making the
 * user retype the password they just chose is the whole reason this page is not two pages.
 *
 * Registration **does** disclose that an address is taken, and that is the backend's decision,
 * not an oversight here: answering 201 to a duplicate would leave the real owner without an
 * account while the caller believed they had one. Sign-in, where it matters, discloses nothing.
 */

// Mirrors app/schemas/auth.py. Duplicated deliberately: the server validates regardless, and the
// client validates so nobody waits for a round trip to be told a password is too short.
const MIN_PASSWORD_LENGTH = 12;
const MAX_PASSWORD_LENGTH = 1024;

const schema = z.object({
  email: z.string().min(1, "Enter your email address").email("That does not look like an email"),
  password: z
    .string()
    .min(MIN_PASSWORD_LENGTH, `At least ${MIN_PASSWORD_LENGTH} characters`)
    .max(MAX_PASSWORD_LENGTH, `At most ${MAX_PASSWORD_LENGTH} characters`),
});

type Values = z.infer<typeof schema>;

const EMPTY: Values = { email: "", password: "" };

function describe(err: unknown, fallback: string): string {
  if (err instanceof ApiError) return err.message;
  return fallback;
}

export function RegisterPage() {
  const createAccount = useRegister();
  const signIn = useSignIn();
  const navigate = useNavigate();
  const [failure, setFailure] = useState<string | null>(null);

  const {
    register,
    handleSubmit,
    formState: { errors, isSubmitting },
  } = useForm<Values>({ resolver: zodResolver(schema), defaultValues: EMPTY });

  const submit = handleSubmit(async (values) => {
    setFailure(null);
    try {
      await createAccount.mutateAsync(values);
    } catch (err) {
      setFailure(describe(err, "Could not create the account."));
      return;
    }
    try {
      await signIn.mutateAsync(values);
      await navigate({ to: "/account" });
    } catch {
      // The account exists; only the second leg failed. Saying so is the difference between a
      // user who signs in and one who registers again and hits "already exists".
      toast.success("Account created. Sign in to continue.");
      await navigate({ to: "/login" });
    }
  });

  const loginLink = (
    <>
      Already have an account?{" "}
      <Link to="/login" className="font-semibold text-brand hover:underline">
        Sign in
      </Link>
    </>
  );

  return (
    <AuthCard
      title="Create an account"
      description="An email address and a password. There is no verification step and no reset flow — this backend sends no mail."
      footer={loginLink}
    >
      <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
        {failure && <FormError>{failure}</FormError>}

        <Field id="email" label="Email" error={errors.email?.message}>
          <Input
            id="email"
            type="email"
            autoComplete="username"
            placeholder="ada@example.com"
            aria-invalid={Boolean(errors.email)}
            {...register("email")}
          />
        </Field>

        <Field
          id="password"
          label="Password"
          error={errors.password?.message}
        >
          <Input
            id="password"
            type="password"
            autoComplete="new-password"
            aria-invalid={Boolean(errors.password)}
            {...register("password")}
          />
          <p className="mt-1 text-[12px] text-faint">
            At least {MIN_PASSWORD_LENGTH} characters. Length is the only rule — a passphrase
            beats a symbol.
          </p>
        </Field>

        <Button type="submit" disabled={isSubmitting}>
          {isSubmitting ? "Creating…" : "Create account"}
        </Button>
      </form>
    </AuthCard>
  );
}
