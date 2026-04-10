// Copyright (c) 2026 Nikolay Govorov
// SPDX-License-Identifier: AGPL-3.0-or-later

import { Code, ConnectError } from "@connectrpc/connect"
import { ErrorInfoSchema, ErrorReason } from "@/gen/api_pb"

// errorReason extracts the ErrorInfo.reason attached by the server.
// Returns null for transport failures or non-mirum responses.
export function errorReason(err: unknown): ErrorReason | null {
  const info = ConnectError.from(err).findDetails(ErrorInfoSchema)[0]
  return info?.reason ?? null
}

// formatError maps any thrown value to user-facing text.
// All .catch() callers should route errors through this function.
export function formatError(err: unknown): string {
  const e = ConnectError.from(err)
  const info = e.findDetails(ErrorInfoSchema)[0]
  if (info) {
    return textForReason(info.reason)
  }
  console.error("api error without ErrorInfo:", e)
  return textForCode(e.code)
}

export function textForReason(reason: ErrorReason): string {
  switch (reason) {
    case ErrorReason.USER_NOT_FOUND:
      return "User not found."
    case ErrorReason.ORG_NOT_FOUND:
      return "Organization not found."
    case ErrorReason.WORKER_NOT_FOUND:
      return "Worker not found."
    case ErrorReason.MEMBER_NOT_FOUND:
      return "Member not found."
    case ErrorReason.EMAIL_TAKEN:
      return "This email is already in use."
    case ErrorReason.SLUG_TAKEN:
      return "This slug is already taken."
    case ErrorReason.ALREADY_MEMBER:
      return "Already a member of this organization."
    case ErrorReason.LAST_OWNER:
      return "An organization must have at least one owner."
    case ErrorReason.SOLE_OWNER:
      return "This user is the sole owner of an organization. Transfer ownership first."
    case ErrorReason.INVALID_SLUG:
      return "Invalid slug. Use lowercase letters, digits, and hyphens."
    case ErrorReason.INVALID_ROLE:
      return "Invalid role."
    case ErrorReason.RESERVED_EMAIL:
      return "This email domain is reserved. Please use a different address."
    case ErrorReason.UNAUTHENTICATED:
      return "Your session has expired. Please sign in again."
    case ErrorReason.PERMISSION_DENIED:
      return "You don't have permission to do that."
    case ErrorReason.INVALID_CREDENTIALS:
      return "Wrong email or password. Please try again."
    case ErrorReason.INVALID_CSRF:
      return "This form expired. Please try again."
    case ErrorReason.RATE_LIMITED:
      return "Too many requests. Please slow down."
    case ErrorReason.UNAVAILABLE:
      return "Service is temporarily unavailable. Please retry."
    case ErrorReason.UNIMPLEMENTED:
      return "This operation is not supported."
    case ErrorReason.INTERNAL:
    case ErrorReason.UNSPECIFIED:
    default:
      return "Something went wrong. Please try again."
  }
}

function textForCode(code: Code): string {
  switch (code) {
    case Code.Canceled:
      return "Request canceled."
    case Code.DeadlineExceeded:
      return "Request timed out."
    case Code.Unavailable:
      return "Service is temporarily unavailable. Please retry."
    default:
      return "Connection failed. Please retry."
  }
}
