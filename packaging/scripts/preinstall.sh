#!/bin/sh
# Copyright (c) 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

set -e

nologin=/usr/sbin/nologin
[ -x "$nologin" ] || nologin=/sbin/nologin
[ -x "$nologin" ] || nologin=/bin/false

for svc in mirum-server mirum-worker; do
  if ! getent group "$svc" >/dev/null; then
    if command -v groupadd >/dev/null; then
      groupadd --system "$svc"
    else
      addgroup -S "$svc"
    fi
  fi
  if ! getent passwd "$svc" >/dev/null; then
    if command -v useradd >/dev/null; then
      useradd --system --gid "$svc" --no-create-home --shell "$nologin" "$svc"
    else
      adduser -S -H -G "$svc" -s "$nologin" "$svc"
    fi
  fi
done
