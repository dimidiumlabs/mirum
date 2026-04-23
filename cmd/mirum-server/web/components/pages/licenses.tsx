// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import type { Messages } from "@/messages/types";

interface Dep {
  name: string;
  version?: string;
  spdx: string;
  url?: string;
  count?: number; // >0 means a collapsed scope entry covering N sub-packages
}

function depLabel(d: Dep): string {
  if (d.count && d.count > 0) {
    return `${d.name} (via ${d.count} npm packages)`;
  }
  return d.version ? `${d.name}@${d.version}` : d.name;
}

interface Variant {
  text: string;
  deps: Dep[];
}

interface Group {
  spdx: string;
  total: number;
  variants: Variant[];
}

interface Ecosystem {
  total: number;
  groups: Group[];
}

export interface LicensesPageProps {
  primary: { name: string; spdx: string; text: string };
  manifest: {
    generated_at: string;
    go: Ecosystem;
    npm: Ecosystem;
  };
  m: Messages;
}

function slug(s: string): string {
  return s.replace(/[^A-Za-z0-9.-]/g, "_");
}

/** License renders one variant as an accordion item: the trigger shows the
 *  comma-separated list of packages sharing this exact LICENSE text; the
 *  text itself is hidden inside the collapsed content. */
function License({
  value,
  spdx,
  variant,
}: {
  value: string;
  spdx: string;
  variant: Variant;
}) {
  return (
    <AccordionItem value={value}>
      <AccordionTrigger>
        <span className="text-start">
          {variant.deps.map((d, i) => {
            const outerExpr = d.spdx.replace(/^\((.*)\)$/, "$1");
            return (
              <span key={`${d.name}@${d.version ?? ""}`}>
                {i > 0 && ", "}
                {depLabel(d)}
                {outerExpr !== spdx && ` (${outerExpr})`}
              </span>
            );
          })}
        </span>
      </AccordionTrigger>
      <AccordionContent>
        <pre className="max-h-[280px] overflow-y-scroll border-s-4 border-neutral-800 bg-amber-50 px-4 py-3 text-sm whitespace-pre-wrap dark:border-neutral-500 dark:bg-neutral-800">
          {variant.text}
        </pre>
      </AccordionContent>
    </AccordionItem>
  );
}

function EcosystemSection({
  title,
  data,
  prefix,
}: {
  title: string;
  data: Ecosystem;
  prefix: string;
}) {
  return (
    <>
      <h2 className="mt-6 mb-2 text-2xl font-bold leading-[1.2] text-balance">
        {title} ({data.total})
      </h2>
      <p className="my-4 text-pretty">
        {data.total} runtime {title.toLowerCase()} packages across{" "}
        {data.groups.length} licenses:
      </p>
      <ul className="my-4 list-disc ps-6">
        {data.groups.map((g) => (
          <li key={g.spdx} className="my-1">
            <a
              href={`#${prefix}-${slug(g.spdx)}`}
              className="text-sky-700 hover:underline dark:text-sky-400"
            >
              {g.spdx} ({g.total})
            </a>
          </li>
        ))}
      </ul>

      {data.groups.map((g) => (
        <div key={g.spdx}>
          <h3
            id={`${prefix}-${slug(g.spdx)}`}
            className="mt-6 mb-2 text-[1.15rem] font-bold leading-[1.2] text-balance"
          >
            <a
              href={`#${prefix}-${slug(g.spdx)}`}
              className="text-inherit hover:underline"
            >
              {g.spdx}
            </a>
          </h3>
          <Accordion type="multiple">
            {g.variants.map((v, i) => (
              <License
                key={`${prefix}-${slug(g.spdx)}-${i}`}
                value={`${prefix}-${slug(g.spdx)}-${i}`}
                spdx={g.spdx}
                variant={v}
              />
            ))}
          </Accordion>
        </div>
      ))}
    </>
  );
}

export function Page({ primary, manifest, m: _m }: LicensesPageProps) {
  return (
    <article className="mx-auto max-w-[80ch] px-8 py-6 font-mono leading-[1.4]">
      <h1 className="text-4xl font-bold leading-[1.1] text-balance">
        Licenses
      </h1>

      <h2 className="mt-6 mb-2 text-2xl font-bold leading-[1.2] text-balance">
        Mirum
      </h2>

      <p className="my-4 text-pretty">
        Mirum is licensed under{" "}
        <code className="bg-neutral-200/40 px-[0.4em] py-[0.2em] dark:bg-neutral-700/40">
          {primary.spdx}
        </code>
        . The full license text is included below.
      </p>

      <pre className="my-4 max-h-[280px] overflow-y-scroll border-s-4 border-neutral-800 bg-amber-50 px-4 py-3 text-sm whitespace-pre-wrap dark:border-neutral-500 dark:bg-neutral-800">
        {primary.text}
      </pre>

      <EcosystemSection title="Go" data={manifest.go} prefix="go" />
      <EcosystemSection title="NPM" data={manifest.npm} prefix="npm" />
    </article>
  );
}
