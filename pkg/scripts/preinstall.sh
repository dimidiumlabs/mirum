#!/bin/sh
# Copyright (c) 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

set -e

for svc in mirumd mirumw; do
  if ! getent group $svc >/dev/null; then
    groupadd --system $svc
  fi
  if ! getent passwd $svc >/dev/null; then
    useradd --system --gid $svc --no-create-home --shell /usr/sbin/nologin $svc
  fi
done
