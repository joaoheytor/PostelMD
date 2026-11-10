# The release image (ADR-0009): the same static binary as the release archives, on distroless.
# No RUN steps, so every platform builds without emulation. `make image` cross-compiles
# bin/linux-<arch>/postelmd first; the build context holds nothing else (.dockerignore).
FROM gcr.io/distroless/static:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329

ARG TARGETARCH
COPY --chmod=0555 bin/linux-${TARGETARCH}/postelmd /postelmd
# /data belongs to the image's user, so a named volume works without --user (ADR-0009).
COPY --chown=65532:65532 --chmod=0700 bin/empty/ /data/

LABEL org.opencontainers.image.title="PostelMD" \
      org.opencontainers.image.description="Self-hosted, git-native Markdown workspace" \
      org.opencontainers.image.source="https://github.com/joaoheytor/PostelMD" \
      org.opencontainers.image.licenses="Apache-2.0"

USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --start-interval=1s \
  CMD ["/postelmd", "healthcheck"]
ENTRYPOINT ["/postelmd"]
CMD ["serve"]
