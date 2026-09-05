# Mirum Helm chart

The chart runs the single Mirum HTTP service and connects it to an external
PostgreSQL database. It creates a Deployment and Service and can optionally
publish the Service through a Gateway API `HTTPRoute`. The chart does not
install PostgreSQL or put database credentials in Helm values.

## Configuration secret

Create a Secret containing `config.toml`. The listener must bind to the pod
network rather than loopback:

```toml
[server]
addr = "0.0.0.0:8080"

[database]
url = "postgres://mirum:password@postgres.database.svc:5432/mirum"
max_connections = 10
connect_timeout_seconds = 5
```

```console
kubectl create namespace mirum
kubectl -n mirum create secret generic mirum \
  --from-file=config.toml=./config.toml
helm upgrade --install mirum ./charts/mirum --namespace mirum
```

Use `config.existingSecret` and `config.key` when the Secret or key has another
name. The Secret is mounted read-only and the chart never copies its contents
into a generated resource.

## HTTP publication

The chart uses Gateway API instead of creating an Ingress. For example:

```yaml
route:
  enabled: true
  hostnames: [mirum.example.test]
  parentRefs:
    - name: public
      namespace: network
      sectionName: https
```

TLS termination and certificates belong to the selected Gateway. If
`server.hostnames` is enabled in `config.toml`, set `probes.host` to one of the
same authorities so the database readiness probe is accepted:

```yaml
probes:
  host: mirum.example.test
```

## OCI chart

Install a published chart with:

```console
helm upgrade --install mirum \
  oci://ghcr.io/dimidiumlabs/charts/mirum \
  --version VERSION \
  --namespace mirum --create-namespace
```

The workflow in [`.github/workflows/ci.yml`](../../.github/workflows/ci.yml)
validates pull requests and publishes pushes to `main` and `v*` tags. After
authenticating Helm to GHCR, the same package and publish operation is available
locally through the shared platform task used by Gilti and Tesor:

```console
printf '%s' "$GHCR_TOKEN" | helm registry login ghcr.io \
  --username "$GITHUB_ACTOR" --password-stdin
mise run chart -- \
  --chart charts/mirum \
  --version VERSION \
  --app-version VERSION \
  --push oci://ghcr.io/dimidiumlabs/charts
```

The application image must be published with the same version. Setting
`image.tag` overrides that default; `image.digest` takes precedence over both.

## Validation

```console
tests/chart.sh
```
