// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { StrictMode, type ComponentType } from "react";
import { createRoot } from "react-dom/client";
import type { Messages } from "@/messages/types";

export function getInitialData<T>(): T {
  const el = document.getElementById("__DATA__");
  if (!el?.textContent) {
    throw new Error("missing __DATA__ script tag");
  }

  return JSON.parse(el.textContent) as T;
}

export function mountPage<P extends { m: Messages }>(
  Component: ComponentType<P>,
  m: Messages,
) {
  const data = getInitialData<Omit<P, "m">>();

  const root = document.getElementById("app");
  if (!root) {
    throw new Error("missing #app element");
  }

  createRoot(root).render(
    <StrictMode>
      <Component {...({ ...data, m } as unknown as P)} />
    </StrictMode>,
  );
}
