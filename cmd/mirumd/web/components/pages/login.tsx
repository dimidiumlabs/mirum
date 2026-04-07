// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { GalleryVerticalEnd, AlertCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from "@/components/ui/field";
import { ErrorReason } from "@/gen/admin_pb";
import { textForReason } from "@/lib/errors";
import * as m from "@/paraglide/messages.js";

export type LoginFormProps = {
  csrf: string;
  errorReason?: ErrorReason;
};

export function Page({ csrf, errorReason }: LoginFormProps) {
  const [error, setError] = useState(
    errorReason !== undefined ? textForReason(errorReason) : undefined,
  );
  const dismissError = () => setError(undefined);

  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-6 bg-muted p-6 md:p-10">
      <div className="flex w-full max-w-sm flex-col gap-6">
        <div className="flex items-center gap-2 self-center font-medium">
          <div className="flex size-6 items-center justify-center rounded-md bg-primary text-primary-foreground">
            <GalleryVerticalEnd className="size-4" />
          </div>
          {m.login_brand()}
        </div>

        <div className={"flex flex-col gap-6"}>
          <Card>
            <CardHeader className="text-center">
              <CardTitle className="text-xl">{m.login_title()}</CardTitle>
            </CardHeader>

            <CardContent>
              <form method="POST" action="/auth/login">
                <input type="hidden" name="csrf" value={csrf} />

                <FieldGroup>
                  {error && (
                    <Field>
                      <div
                        role="alert"
                        className="flex items-center gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive"
                      >
                        <AlertCircle className="size-4 shrink-0" aria-hidden />
                        <span>{error}</span>
                      </div>
                    </Field>
                  )}

                  <Field>
                    <FieldLabel htmlFor="email">{m.login_email_label()}</FieldLabel>
                    <Input
                      id="email"
                      name="email"
                      type="email"
                      placeholder={m.login_email_placeholder()}
                      required
                      autoFocus
                      onInput={dismissError}
                    />
                  </Field>

                  <Field>
                    <FieldLabel htmlFor="password">{m.login_password_label()}</FieldLabel>
                    <Input
                      id="password"
                      name="password"
                      type="password"
                      autoComplete="current-password"
                      required
                      onInput={dismissError}
                    />
                  </Field>

                  <Field>
                    <Button type="submit">{m.login_submit()}</Button>

                    <FieldDescription className="text-center">
                      {m.login_contact_admin()}
                    </FieldDescription>
                  </Field>
                </FieldGroup>
              </form>
            </CardContent>
          </Card>

          <FieldDescription className="px-6 text-center">
            {m.login_terms_prefix()}{" "}
            <a href="#">{m.login_terms_of_service()}</a>{" "}
            {m.login_terms_and()}{" "}
            <a href="#">{m.login_privacy_policy()}</a>.
          </FieldDescription>
        </div>
      </div>
    </div>
  );
}
