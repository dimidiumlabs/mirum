// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { twMerge } from "tailwind-merge"

export type ClassValue = ClassArray | Record<string, unknown> | string | number | bigint | null | boolean | undefined
export type ClassArray = ClassValue[]

// avoid dependency for 10 lines and this version is stricter in TS
function classList(value: ClassValue): string {
  if (typeof value === "string" || typeof value === "number" || typeof value === "bigint") {
    return String(value)
  }
  if (Array.isArray(value)) {
    return value.map(classList).filter(Boolean).join(" ")
  }
  if (value !== null && typeof value === "object") {
    return Object.keys(value).filter((key) => value[key]).join(" ")
  }
  return ""
}

export function cn(...inputs: ClassValue[]): string {
  return twMerge(classList(inputs))
}
