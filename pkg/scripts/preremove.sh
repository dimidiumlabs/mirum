#!/bin/sh
# Copyright (c) 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

set -e

if [ -x "/bin/systemctl" ] && [ -d /run/systemd/system ]; then
  /bin/systemctl stop mirum-server.service || true
  /bin/systemctl disable mirum-server.service || true

  /bin/systemctl stop 'mirum-worker@*' || true
  /bin/systemctl disable mirum-worker@.service || true
fi
