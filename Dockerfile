ARG appname=fsserve

FROM golang:1.21.7-bookworm as builder
ARG appname
WORKDIR "/go/src/$appname"
RUN [ \( "$(go env GOARCH)" = 'amd64' \) -a \( "$(go env GOOS)" = 'linux' \) ]
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
COPY --link --from=builder "/usr/local/bin/$appname" "/usr/local/bin/$appname"
EXPOSE 8080
WORKDIR "/home/$appname"
ENTRYPOINT ["fsserve", "--config", "./config/.fsserve.json", "-b", "./base"]
CMD ["serve", "-p", "8080"]
