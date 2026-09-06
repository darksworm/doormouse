# Compile on the builder's native architecture, even for cross-platform images.
ARG BUILDPLATFORM
FROM --platform=$BUILDPLATFORM golang:1.24.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/doormouse .

FROM alpine:3.22

# Everything that does not need the binary happens before it is copied in, so a
# new release busts as little cache as possible. Only setcap has to come after.
#
# The account is dedicated and unprivileged. UID and GID are pinned to 1000, the
# first user on most Linux hosts, so an SSH key that is 0600 and owned by the
# host user stays readable through a bind mount with no chown.
#
# libcap-setcap rather than libcap: it is the only piece needed here and pulls
# two packages instead of five. It stays in the image, which costs about 60 kB.
# Removing it after the COPY would save nothing, since the files would still sit
# in this layer with only a whiteout on top, and it would put an apk fetch back
# on the path every release rebuilds.
RUN addgroup -g 1000 doormouse \
    && adduser -D -u 1000 -G doormouse doormouse \
    && apk add --no-cache libcap-setcap

WORKDIR /app

# The binary lives outside /app so that /app can be bind-mounted as a whole
# writable config directory without handing the runtime user its own binary.
COPY --from=build /out/doormouse /usr/local/bin/doormouse

# doormouse runs as a non-root user, and the kernel would otherwise stop it from
# binding ports below 1024. This file capability grants that one bind permission,
# so `port = ":443"` works with neither root nor a host sysctl.
# CAP_NET_BIND_SERVICE is in Docker's default set, so no cap_add is needed. Drop
# it and the container will not start at all: the kernel refuses to exec a file
# whose capability it cannot grant.
#
# The one step that cannot move above the COPY: setcap stamps the binary, so the
# binary has to be there. It is a single local syscall, with nothing to fetch.
RUN setcap cap_net_bind_service=+ep /usr/local/bin/doormouse

# Numeric, not the name: Kubernetes cannot verify runAsNonRoot against a
# username and refuses to start the pod, so the number has to be on the image.
USER 1000:1000

# Same contract as before: the default port, and a config mounted at
# /app/config.toml. TCP routes listen on their own ports; with network_mode:
# host they are reachable directly, otherwise publish each one.
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/doormouse", "/app/config.toml"]
