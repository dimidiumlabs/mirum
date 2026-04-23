// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";

export type PluralForms = {
  zero?: string;
  one?: string;
  two?: string;
  few?: string;
  many?: string;
  other: string;
};

// plural selects the correct plural form for n using Intl.PluralRules and
// substitutes "#" with the number. Works for all CLDR locales including
// Russian (4 forms) and Chinese (1 form).
export function plural(locale: string, forms: PluralForms, n: number): string {
  const rule = new Intl.PluralRules(locale).select(n) as keyof PluralForms;
  const template = forms[rule] ?? forms.other;
  return template.replace("#", String(n));
}

// templateParts splits a template string like "Agree to {terms} and {privacy}"
// into an array of strings and ReactNode substitutions, in order. Used to
// build JSX interpolations from translated template strings.
export function templateParts(
  template: string,
  vars: Record<string, ReactNode>,
): ReactNode[] {
  const keys = Object.keys(vars).map((k) => k.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  const pattern = new RegExp(`\\{(${keys.join("|")})\\}`, "g");
  const parts: ReactNode[] = [];
  let last = 0;
  let match: RegExpExecArray | null;

  while ((match = pattern.exec(template)) !== null) {
    if (match.index > last) parts.push(template.slice(last, match.index));
    parts.push(vars[match[1]]);
    last = match.index + match[0].length;
  }
  if (last < template.length) parts.push(template.slice(last));

  return parts;
}
