#!/bin/sh
# SPDX-FileCopyrightText: 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

set -e

if [ -x /bin/systemctl ] && [ -d /run/systemd/system ] && [ -f /usr/lib/systemd/system/mirum.service ]; then
  /bin/systemctl daemon-reload
fi
