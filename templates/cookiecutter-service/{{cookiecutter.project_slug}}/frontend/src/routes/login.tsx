import { useState } from "react";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { Link, useNavigate } from "@tanstack/react-router";
import { z } from "zod";

import { AuthCard, Field, FormError } from "@/components/auth-card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ApiError } from "@/lib/api/client";
import { useSignIn } from "@/lib/api/auth";

/**
 * Sign in.
 *
 * The one rule this page exists to keep: **it never says whether the address is registered.**
 * The backend answers a wrong password and an unknown email with the same status, the same body
 * and the same timing, deliberately — see `app/services/auth_service.py`. A form that improves
 * on that with "no account found" hands back the user directory the backend just refused to
 * publish, and it is the easiest thing in the world to add by accident.
 */

const schema = z.object({
  email: z.string().min(1, "Enter your email address").email("That does not look like an email"),
  password: z.string().min(1, "Enter your password"),
});

type Values = z.infer<typeof schema>;

const EMPTY: Values = { email: "", password: "" };

/**
 * One message for every way signing in can fail on the credentials. 401 is the backend's
 * deliberate ambiguity and 422 is "no password of ours is that short" — telling those two apart
 * on screen is an oracle, so they are one string here.
 */
const SIGN_IN_FAILED = "Invalid email or password.";

function describe(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401 || err.status === 422) return SIGN_IN_FAILED;
    return err.message;
  }
  return "Could not reach the server. Check that the backend is running.";
}

export function LoginPage() {
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
      await signIn.mutateAsync(values);
      await navigate({ to: "/" });
    } catch (err) {
      setFailure(describe(err));
    }
  });

  const registerLink = (
    <>
      No account yet?{" "}
      <Link to="/register" className="font-semibold text-brand hover:underline">
        Create one
      </Link>
    </>
  );

  return (
    <AuthCard
      title="Sign in"
      description="Your session lasts fifteen minutes and ends when you close this tab."
      footer={registerLink}
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

        <Field id="password" label="Password" error={errors.password?.message}>
          <Input
            id="password"
            type="password"
            autoComplete="current-password"
            aria-invalid={Boolean(errors.password)}
            {...register("password")}
          />
        </Field>

        <Button type="submit" disabled={isSubmitting}>
          {isSubmitting ? "Signing in…" : "Sign in"}
        </Button>
      </form>
    </AuthCard>
  );
}
