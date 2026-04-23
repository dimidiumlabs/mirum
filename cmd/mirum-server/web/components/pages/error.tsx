// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Messages } from "@/messages/types";
import { Button } from "@/components/ui/button";

export interface ErrorPageProps {
  status: number;
  m: Messages;
}

function errorCopy(
  status: number,
  m: Messages,
): { title: string; body: string } {
  switch (status) {
    case 400:
      return { title: m.error_400_title, body: m.error_400_body };
    case 401:
      return { title: m.error_401_title, body: m.error_401_body };
    case 403:
      return { title: m.error_403_title, body: m.error_403_body };
    case 404:
      return { title: m.error_404_title, body: m.error_404_body };
    case 405:
      return { title: m.error_405_title, body: m.error_405_body };
    case 500:
      return { title: m.error_500_title, body: m.error_500_body };
    case 503:
      return { title: m.error_503_title, body: m.error_503_body };
    default:
      return { title: `Error ${status}`, body: m.error_unknown_body };
  }
}

export function Page({ status, m }: ErrorPageProps) {
  const text = errorCopy(status, m);

  return (
    <main className="relative flex min-h-svh items-center justify-center overflow-hidden p-6">
      <p
        aria-hidden
        className="pointer-events-none absolute inset-x-0 top-1/2 hidden -translate-y-1/2 select-none text-center font-bold leading-none tracking-tighter text-foreground/[0.04] text-[clamp(12rem,32vw,24rem)] md:block"
      >
        {status}
      </p>
      <div className="relative w-full max-w-sm">
        <p className="text-xs font-medium uppercase tracking-wider text-foreground/60">
          Error {status}
        </p>
        <h1 className="mt-2 text-2xl font-semibold tracking-tight">
          {text.title}
        </h1>
        <p className="mt-3 text-sm text-foreground/80">{text.body}</p>
        <Button asChild variant="outline" size="sm" className="mt-6">
          <a href="/">{m.back_to_home}</a>
        </Button>
      </div>
    </main>
  );
}
