// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Messages } from "./types";
import { templateParts } from "./utils";
import raw from "./en-GB.json";

export default {
  ...raw,
  login_terms: (terms, privacy) => (
    <>{templateParts(raw.login_terms, { terms, privacy })}</>
  ),
} satisfies Messages;
