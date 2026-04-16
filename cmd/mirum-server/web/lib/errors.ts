// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { Code, ConnectError } from "@connectrpc/connect";
import { ErrorInfoSchema, ErrorReason } from "@/gen/api_pb";
import type { Messages } from "@/messages/types";

// errorReason extracts the ErrorInfo.reason attached by the server.
// Returns null for transport failures or non-mirum responses.
export function errorReason(err: unknown): ErrorReason | null {
  const info = ConnectError.from(err).findDetails(ErrorInfoSchema)[0];
  return info?.reason ?? null;
}

// formatError maps any thrown value to user-facing text.
// All .catch() callers should route errors through this function.
export function formatError(err: unknown, m: Messages): string {
  const e = ConnectError.from(err);
  const info = e.findDetails(ErrorInfoSchema)[0];
  if (info) {
    return textForReason(info.reason, m);
  }
  console.error("api error without ErrorInfo:", e);
  return textForCode(e.code, m);
}

export function textForReason(reason: ErrorReason, m: Messages): string {
  switch (reason) {
    case ErrorReason.USER_NOT_FOUND:
      return m.err_user_not_found;
    case ErrorReason.ORG_NOT_FOUND:
      return m.err_org_not_found;
    case ErrorReason.WORKER_NOT_FOUND:
      return m.err_worker_not_found;
    case ErrorReason.MEMBER_NOT_FOUND:
      return m.err_member_not_found;
    case ErrorReason.EMAIL_TAKEN:
      return m.err_email_taken;
    case ErrorReason.SLUG_TAKEN:
      return m.err_slug_taken;
    case ErrorReason.ALREADY_MEMBER:
      return m.err_already_member;
    case ErrorReason.LAST_OWNER:
      return m.err_last_owner;
    case ErrorReason.SOLE_OWNER:
      return m.err_sole_owner;
    case ErrorReason.INVALID_SLUG:
      return m.err_invalid_slug;
    case ErrorReason.INVALID_ROLE:
      return m.err_invalid_role;
    case ErrorReason.RESERVED_EMAIL:
      return m.err_reserved_email;
    case ErrorReason.UNAUTHENTICATED:
      return m.err_unauthenticated;
    case ErrorReason.PERMISSION_DENIED:
      return m.err_permission_denied;
    case ErrorReason.INVALID_CREDENTIALS:
      return m.err_invalid_credentials;
    case ErrorReason.INVALID_CSRF:
      return m.err_invalid_csrf;
    case ErrorReason.RATE_LIMITED:
      return m.err_rate_limited;
    case ErrorReason.UNAVAILABLE:
      return m.err_unavailable;
    case ErrorReason.UNIMPLEMENTED:
      return m.err_unimplemented;
    case ErrorReason.INTERNAL:
    case ErrorReason.UNSPECIFIED:
    default:
      return m.err_unknown;
  }
}

function textForCode(code: Code, m: Messages): string {
  switch (code) {
    case Code.Canceled:
      return m.err_code_canceled;
    case Code.DeadlineExceeded:
      return m.err_code_timeout;
    case Code.Unavailable:
      return m.err_code_unavailable;
    default:
      return m.err_code_unknown;
  }
}
