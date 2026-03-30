#!/bin/sh
# Copyright (c) 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

set -e

if [ -x "/bin/systemctl" ] && [ -d /run/systemd/system ] && [ -f /usr/lib/systemd/system/mirumd.service ]; then
  /bin/systemctl stop mirumd.service || true
  /bin/systemctl disable mirumd.service || true
fi
