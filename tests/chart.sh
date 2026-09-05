#!/bin/sh
# SPDX-FileCopyrightText: 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
chart=$root/charts/mirum

helm() {
    mise x helm@4.1.1 -- helm "$@"
}

mise run chart -- --chart "$chart" --lint-only

rendered=$(mktemp)
routed=$(mktemp)
invalid=$(mktemp)
trap 'rm -f "$rendered" "$routed" "$invalid"' EXIT

helm template mirum "$chart" >"$rendered"
grep -q '^kind: Deployment$' "$rendered"
grep -q '^kind: Service$' "$rendered"
grep -q 'image: "ghcr.io/dimidiumlabs/mirum:0.1.0"' "$rendered"
grep -q 'secretName: "mirum"' "$rendered"
grep -q 'mountPath: /etc/mirum/config.toml' "$rendered"
grep -q 'path: /-/health' "$rendered"
grep -q 'path: /-/ready' "$rendered"
if grep -q '^kind: HTTPRoute$' "$rendered"; then
    echo 'chart rendered HTTPRoute while route.enabled=false' >&2
    exit 1
fi
if grep -q '^kind: Secret$' "$rendered" || grep -q '^kind: ConfigMap$' "$rendered"; then
    echo 'chart rendered configuration or credentials' >&2
    exit 1
fi

helm template mirum "$chart" \
    --set route.enabled=true \
    --set 'route.hostnames[0]=mirum.example.test' \
    --set 'route.parentRefs[0].name=public' \
    --set probes.host=mirum.example.test >"$routed"
grep -q '^kind: HTTPRoute$' "$routed"
grep -q -- '- mirum.example.test' "$routed"
grep -q 'value: "mirum.example.test"' "$routed"

if helm template mirum "$chart" --set config.existingSecret= >"$invalid" 2>&1; then
    echo 'chart accepted an empty configuration Secret name' >&2
    exit 1
fi
if helm template mirum "$chart" --set image.digest=sha256:invalid >"$invalid" 2>&1; then
    echo 'chart accepted an invalid image digest' >&2
    exit 1
fi
if helm template mirum "$chart" \
    --set-string podLabels.app\.kubernetes\.io/name=other >"$invalid" 2>&1; then
    echo 'chart accepted an overridden selector label' >&2
    exit 1
fi
