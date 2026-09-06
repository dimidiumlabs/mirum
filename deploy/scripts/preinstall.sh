#!/bin/sh
# SPDX-FileCopyrightText: 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

set -e

nologin=/usr/sbin/nologin
[ -x "$nologin" ] || nologin=/sbin/nologin
[ -x "$nologin" ] || nologin=/bin/false

if ! getent group mirum >/dev/null; then
  if command -v groupadd >/dev/null; then
    groupadd --system mirum
  else
    addgroup -S mirum
  fi
fi

if ! getent passwd mirum >/dev/null; then
  if command -v useradd >/dev/null; then
    useradd --system --gid mirum --no-create-home --shell "$nologin" mirum
  else
    adduser -S -H -G mirum -s "$nologin" mirum
  fi
fi
