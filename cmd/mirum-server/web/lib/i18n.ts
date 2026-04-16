// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

function pageLocale(): string {
  const el = document.getElementById("__DATA__");
  if (!el?.textContent) return "en-GB";
  try {
    return (JSON.parse(el.textContent) as { locale?: string }).locale ?? "en-GB";
  } catch {
    return "en-GB";
  }
}

const locale = pageLocale();

export const dateFormat = new Intl.DateTimeFormat(locale);
export const dateTimeFormat = new Intl.DateTimeFormat(locale, {
  dateStyle: "medium",
  timeStyle: "short",
});
export const relativeFormat = new Intl.RelativeTimeFormat(locale, {
  numeric: "auto",
});
export const numberFormat = new Intl.NumberFormat(locale);
