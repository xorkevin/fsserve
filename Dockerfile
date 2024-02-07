FROM golang:1.21.7-bookworm as builder
WORKDIR /go/src/fsserve
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
  GOPROXY=off go build -v -trimpath -ldflags "-w -s" -o /usr/local/bin/fsserve .

FROM gcr.io/distroless/base-debian12:latest
LABEL org.opencontainers.image.authors="Kevin Wang <kevin@xorkevin.com>"
COPY --link --from=builder /usr/local/bin/fsserve /usr/local/bin/fsserve
EXPOSE 8080
WORKDIR /home/fsserve
ENTRYPOINT ["fsserve", "--config", "/home/fsserve/config/.fsserve.json", "-b", "/home/fsserve/base"]
CMD ["serve", "-p", "8080"]
