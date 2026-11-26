# Installer image for a DaemonSet: copies the wrapper, the bundle and the k0s drop-in onto the host.
# Build context: the repo root with ./v3compat-runtime (static linux/amd64) and
# ./bundle/out/v3compat (bundle/get.sh) in place; CI builds both in the e2e job.
FROM alpine:3.22 AS prep
COPY --chown=0:0 bundle/out/v3compat /app/bundle/out/v3compat
RUN chmod -R go-w,a+rX /app/bundle/out/v3compat

FROM alpine:3.22
LABEL org.opencontainers.image.source=https://github.com/lhns/v3compat-runtime
COPY --chown=0:0 --chmod=0755 v3compat-runtime /app/v3compat-runtime
COPY --from=prep /app/bundle /app/bundle
COPY --chown=0:0 --chmod=0755 install/install.sh install/entrypoint.sh /app/install/
ENTRYPOINT ["/app/install/entrypoint.sh"]
