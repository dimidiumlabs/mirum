#!/bin/sh
# SPDX-FileCopyrightText: 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
chart=$root/deploy/charts/mirum

helm() {
    mise x helm@4.1.1 -- helm "$@"
}

mise run chart -- --chart "$chart" --lint-only

rendered=$(mktemp)
routed=$(mktemp)
invalid=$(mktemp)
customized=$(mktemp)
metadata=$(mktemp -d)
trap 'rm -f "$rendered" "$routed" "$invalid" "$customized"; rm -rf "$metadata"' EXIT

helm template mirum "$chart" >"$rendered"
grep -q '^kind: Deployment$' "$rendered"
grep -q '^kind: Service$' "$rendered"
grep -q 'image: "ghcr.io/dimidiumlabs/mirum:dev"' "$rendered"
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

cat >"$customized" <<'EOF'
serviceAccountName: mirum-runtime
extraVolumes:
  - name: workload-identity
    secret:
      secretName: workload-identity
extraVolumeMounts:
  - name: workload-identity
    mountPath: /var/run/workload-identity
    readOnly: true
EOF
helm template mirum "$chart" --values "$customized" >"$rendered"
grep -q 'serviceAccountName: "mirum-runtime"' "$rendered"
grep -q 'secretName: workload-identity' "$rendered"
grep -q 'mountPath: /var/run/workload-identity' "$rendered"

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
if helm template mirum "$chart" \
    --set-string 'podLabels.app\.kubernetes\.io/name=other' >"$invalid" 2>&1; then
    echo 'chart accepted an overridden selector label' >&2
    exit 1
fi

cp -R "$chart/." "$metadata/"
sed -i 's/^appVersion:.*/appVersion: "1.2.3+metadata"/' "$metadata/Chart.yaml"
helm template mirum "$metadata" >"$rendered"
grep -q 'image: "ghcr.io/dimidiumlabs/mirum:1.2.3_metadata"' "$rendered"
grep -q 'app.kubernetes.io/version: "1.2.3_metadata"' "$rendered"
