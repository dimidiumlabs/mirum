// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createClient } from "@connectrpc/connect"
import { createConnectTransport } from "@connectrpc/connect-web"
import { Admin } from "@/gen/admin_pb"

export function createAdminClient(csrfToken: string) {
  const transport = createConnectTransport({
    baseUrl: "/api/v1",
    interceptors: [(next) => async (req) => {
      req.header.set("X-CSRF-Token", csrfToken)
      return next(req)
    }],
  })
  return createClient(Admin, transport)
}
