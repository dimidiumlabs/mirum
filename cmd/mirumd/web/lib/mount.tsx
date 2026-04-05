// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { StrictMode, type ComponentType } from "react"
import { createRoot } from "react-dom/client"

export function getInitialData<T>(): T {
  const el = document.getElementById("__DATA__")
  if (!el?.textContent) {
    throw new Error("missing __DATA__ script tag")
  }

  return JSON.parse(el.textContent) as T
}

export function mountPage<P extends object>(Component: ComponentType<P>) {
  const props = getInitialData<P>()

  createRoot(document.getElementById("app")!).render(
    <StrictMode>
      <Component {...props} />
    </StrictMode>,
  )
}
