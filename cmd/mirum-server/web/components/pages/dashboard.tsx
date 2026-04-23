// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useState } from "react";
import { createConsoleClient } from "@/api/client";
import { formatError } from "@/lib/errors";
import type { Org } from "@/gen/api_pb";
import type { Messages } from "@/messages/types";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export interface DashboardProps {
  user: { email: string };
  csrf: string;
  m: Messages;
}

export function Page({ user, csrf, m }: DashboardProps) {
  const client = useMemo(() => createConsoleClient(csrf), [csrf]);
  const [orgs, setOrgs] = useState<Org[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const ac = new AbortController();
    client
      .orgList({}, { signal: ac.signal })
      .then((res) => setOrgs(res.organizations))
      .catch((err: unknown) => {
        if (ac.signal.aborted) return;
        setError(formatError(err, m));
      });
    return () => ac.abort();
  }, [client, m]);

  return (
    <div className="mx-auto max-w-3xl p-8 space-y-6">
      <header className="flex items-center justify-between">
        <h1 className="text-2xl font-semibold">{m.app_name}</h1>
        <div className="flex items-center gap-3">
          <span className="text-sm text-muted-foreground">{user.email}</span>
          <form method="POST" action="/auth/logout">
            <input type="hidden" name="csrf" value={csrf} />
            <Button type="submit" variant="outline" size="sm">
              {m.sign_out}
            </Button>
          </form>
        </div>
      </header>

      <Card>
        <CardHeader>
          <CardTitle>{m.dashboard_organizations}</CardTitle>
        </CardHeader>
        <CardContent>
          {error && <p className="text-destructive">{error}</p>}
          {orgs === null && !error && (
            <p className="text-muted-foreground">{m.loading}</p>
          )}
          {orgs?.length === 0 && (
            <p className="text-muted-foreground">{m.dashboard_no_organizations}</p>
          )}
          {orgs && orgs.length > 0 && (
            <ul className="divide-y">
              {orgs.map((org) => (
                <li key={org.slug} className="py-2">
                  <span className="font-medium">{org.name}</span>{" "}
                  <span className="text-muted-foreground">({org.slug})</span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
