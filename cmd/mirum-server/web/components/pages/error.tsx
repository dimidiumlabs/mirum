// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { Button } from "@/components/ui/button"

export type ErrorPageProps = {
  status: number
}

const copy: Record<number, { title: string; body: string }> = {
  400: { title: "Bad request", body: "The request couldn't be processed." },
  401: { title: "Not signed in", body: "Please sign in to continue." },
  403: { title: "Forbidden", body: "You don't have permission to access this page." },
  404: { title: "Not found", body: "The page you were looking for doesn't exist." },
  405: { title: "Method not allowed", body: "That action isn't supported here." },
  500: { title: "Something went wrong", body: "An internal error occurred. Please try again." },
  503: { title: "Unavailable", body: "The server is temporarily unable to handle the request." },
}

export function Page({ status }: ErrorPageProps) {
  const text = copy[status] ?? { title: `Error ${status}`, body: "Something went wrong." }

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
        <p className="mt-3 text-sm text-foreground/80">
          {text.body}
        </p>
        <Button asChild variant="outline" size="sm" className="mt-6">
          <a href="/">Back to home</a>
        </Button>
      </div>
    </main>
  )
}
