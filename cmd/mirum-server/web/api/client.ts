// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createClient, type Interceptor } from "@connectrpc/connect"
import { createConnectTransport } from "@connectrpc/connect-web"
import { Console, ErrorReason } from "@/gen/api_pb"
import { errorReason } from "@/lib/errors"

const csrfInterceptor = (csrfToken: string): Interceptor =>
  (next) => async (req) => {
    req.header.set("X-CSRF-Token", csrfToken)
    return next(req)
  }

// authInterceptor redirects to /auth/login on Unauthenticated so individual
// callers don't need to handle session expiry explicitly.
const authInterceptor: Interceptor = (next) => async (req) => {
  try {
    return await next(req)
  } catch (err) {
    if (errorReason(err) === ErrorReason.UNAUTHENTICATED) {
      window.location.assign("/auth/login")
    }
    throw err
  }
}

export function createConsoleClient(csrfToken: string) {
  const transport = createConnectTransport({
    baseUrl: "/api/v1",
    interceptors: [csrfInterceptor(csrfToken), authInterceptor],
  })
  return createClient(Console, transport)
}
