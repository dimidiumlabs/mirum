#!/bin/sh
# Copyright (c) 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

set -e

if [ -x "/bin/systemctl" ] && [ -d /run/systemd/system ] && [ -f /usr/lib/systemd/system/mirumd.service ]; then
  /bin/systemctl daemon-reload

  # Don't enable by default, don't know in advance whether it's a daemon or a worker
  # /bin/systemctl enable mirumd
  # /bin/systemctl enable mirumw
fi
