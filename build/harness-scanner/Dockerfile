# aiman harness scanner: Trivy, ClamAV, YARA (YARA Forge core rules) and
# osv-scanner for image vetting. Serve builds it on the host
# (aiman-harness-scanner:<hash of these build args>) and runs it with no
# network, a read-only root and the host uid; only the database refresh job
# gives it a network. Every download is checked against a pinned sha256;
# the Debian packages come from apt, which checks the archive signatures.
# harness.images.scanners overrides the versions and checksums through the
# build args.

ARG BASE=debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2

FROM ${BASE} AS fetch
ARG TARGETARCH
ARG TRIVY_VERSION=0.76.2
ARG TRIVY_SHA256_AMD64=bc0fc726e017b8927a62b2d3531d7a84ee01769daf5c99ed866dced54089fdbf
ARG TRIVY_SHA256_ARM64=ab9d51f86e6a5fb936f605faff6b593ac51bc378fff4d3e8b9d58358b9c367c3
ARG OSV_VERSION=2.7.1
ARG OSV_SHA256_AMD64=fb7dab19a8bd9bbe5c3189f09d4b7ee1820c9553a23bac202648a56efd2f6ee8
ARG OSV_SHA256_ARM64=983d22fbad9724514e3fcc6acc83b5fa64c5ffa6b0d9f510028f22c1b223ff81
ARG YARA_FORGE_RELEASE=20261115
ARG YARA_FORGE_SHA256=d8cbfc7940c688bd890f941c323f51940c54b46bb2a78143b55d312bab7581c6
RUN apt-get update -qq \
 && apt-get install -y --no-install-recommends ca-certificates curl unzip \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /fetch
RUN set -eu; \
    case "${TARGETARCH:-amd64}" in \
      amd64) trivy_arch=64bit; trivy_sum=${TRIVY_SHA256_AMD64}; osv_arch=amd64; osv_sum=${OSV_SHA256_AMD64} ;; \
      arm64) trivy_arch=ARM64; trivy_sum=${TRIVY_SHA256_ARM64}; osv_arch=arm64; osv_sum=${OSV_SHA256_ARM64} ;; \
      *) echo "unsupported arch ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSLo trivy.tgz "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-${trivy_arch}.tar.gz"; \
    echo "${trivy_sum}  trivy.tgz" | sha256sum -c -; \
    tar -xzf trivy.tgz trivy; \
    curl -fsSLo osv-scanner "https://github.com/google/osv-scanner/releases/download/v${OSV_VERSION}/osv-scanner_linux_${osv_arch}"; \
    echo "${osv_sum}  osv-scanner" | sha256sum -c -; \
    chmod 0755 trivy osv-scanner; \
    curl -fsSLo yara.zip "https://github.com/YARAHQ/yara-forge/releases/download/${YARA_FORGE_RELEASE}/yara-forge-rules-core.zip"; \
    echo "${YARA_FORGE_SHA256}  yara.zip" | sha256sum -c -; \
    unzip -p yara.zip '*yara-rules-core.yar' > rules.yar; \
    test -s rules.yar

FROM ${BASE}
ARG CLAMAV_VERSION=1.4.3+dfsg-1
ARG YARA_VERSION=4.5.2-1
RUN apt-get update -qq \
 && apt-get install -y --no-install-recommends ca-certificates \
      "clamav=${CLAMAV_VERSION}" "clamav-freshclam=${CLAMAV_VERSION}" "yara=${YARA_VERSION}" \
 && rm -rf /var/lib/apt/lists/* /var/lib/clamav/*
COPY --from=fetch /fetch/trivy /fetch/osv-scanner /usr/local/bin/
COPY --from=fetch /fetch/rules.yar /opt/aiman/yara/rules.yar
RUN printf 'DatabaseMirror database.clamav.net\nForeground yes\nChecks 1\n' > /opt/aiman/freshclam.conf
