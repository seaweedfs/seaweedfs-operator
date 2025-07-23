# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot

# Build arguments for multi-platform support
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT

# Copy the pre-built binary for the target platform
COPY assets/${TARGETOS}/${TARGETARCH}${TARGETVARIANT:+/${TARGETVARIANT}}/manager /manager

WORKDIR /
USER 65532:65532

ENTRYPOINT ["/manager"] 