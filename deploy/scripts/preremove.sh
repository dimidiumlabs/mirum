#!/bin/sh
# SPDX-FileCopyrightText: 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

set -e

if [ -x /bin/systemctl ] && [ -d /run/systemd/system ] && [ -f /usr/lib/systemd/system/mirum.service ]; then
  /bin/systemctl stop mirum.service || true
  /bin/systemctl disable mirum.service || true
fi

if command -v rc-service >/dev/null && [ -f /etc/init.d/mirum ]; then
  rc-service mirum stop || true
  rc-update del mirum || true
fi
