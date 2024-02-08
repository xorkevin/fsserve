ARG appname=fsserve

FROM golang:1.22.0-bookworm as builder
ARG appname
WORKDIR "/go/src/$appname"
RUN [ \( "$(go env GOARCH)" = 'amd64' \) -a \( "$(go env GOOS)" = 'linux' \) -a \( "$(dpkg --print-architecture)" = 'amd64' \) ]
RUN \
  --mount=type=cache,id=gomodcache,sharing=locked,target=/go/pkg/mod \
  --mount=type=bind,source=go.mod,target=go.mod \
  --mount=type=bind,source=go.sum,target=go.sum \
  go mod download -json && go mod verify
RUN \
  --mount=type=cache,id=gomodcache,sharing=locked,readonly,target=/go/pkg/mod \
  --mount=type=cache,id=gobuildcache,sharing=locked,target=/root/.cache/go-build \
  --mount=type=bind,source=.,target=. \
  GOPROXY=off go build -v -trimpath -ldflags "-w -s" -o "/usr/local/bin/$appname" .

FROM debian:12.4-slim
ARG appname
LABEL org.opencontainers.image.authors="Kevin Wang <kevin@xorkevin.com>"
RUN \
  --mount=type=cache,id=deb12aptpkgcache,sharing=locked,target=/var/cache/apt \
  --mount=type=cache,id=deb12aptlistscache,sharing=locked,target=/var/lib/apt/lists \
  rm /etc/apt/apt.conf.d/docker-clean && \
  apt-get update && \
  apt-get install -y ca-certificates
COPY --link --from=builder "/usr/local/bin/$appname" "/usr/local/bin/$appname"
EXPOSE 8080
WORKDIR "/home/$appname"
ENTRYPOINT ["fsserve", "--config", "./config/.fsserve.json", "-b", "./base"]
CMD ["serve", "-p", "8080"]
