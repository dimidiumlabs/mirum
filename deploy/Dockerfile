# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
# SPDX-FileCopyrightText: 2026 Nikolay Govorov
# SPDX-License-Identifier: AGPL-3.0-or-later

ARG DISTROLESS_DIGEST=sha256:a77defd6fedbb3392b175ba8ea3d1c22be963c1597c248c3ba987ddd80bfb512

FROM gcr.io/distroless/cc-debian13:nonroot@${DISTROLESS_DIGEST}

ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown

LABEL org.opencontainers.image.title="Mirum" \
      org.opencontainers.image.description="Mirum web service" \
      org.opencontainers.image.source="https://git.dimidiumlabs.io/mirum" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later"

COPY --chown=root:root --chmod=0755 .container/binary-${TARGETARCH}/mirum /usr/local/bin/mirum
COPY --chown=root:root deploy/mirum.toml /etc/mirum/config.toml
COPY --chown=root:root LICENSE README.md LICENSES/ /usr/share/doc/mirum/

USER 10000:10000
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/mirum"]
CMD ["--config", "/etc/mirum/config.toml"]
