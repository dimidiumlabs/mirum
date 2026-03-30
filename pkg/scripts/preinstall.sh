#!/bin/sh
# Copyright (c) 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

set -e

PROGRAM=mirum
MIRUM_USER=${MIRUM_USER:-mirum}
MIRUM_GROUP=${MIRUM_GROUP:-${MIRUM_USER}}

if ! getent group $MIRUM_GROUP >/dev/null; then
  groupadd --system $MIRUM_GROUP
fi

if ! getent passwd $MIRUM_USER >/dev/null; then
  useradd --system --gid $MIRUM_GROUP --no-create-home --shell /usr/sbin/nologin $MIRUM_USER
fi
