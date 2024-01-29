FROM cgr.dev/chainguard/go:latest as builder
WORKDIR /usr/local/src/go/fsserve
COPY --link go.mod /usr/local/src/go/fsserve/go.mod
COPY --link go.sum /usr/local/src/go/fsserve/go.sum
RUN \
  --mount=type=cache,id=gomodcache,sharing=locked,target=/root/go/pkg/mod \
  go mod download -json && go mod verify
COPY --link . /usr/local/src/go/fsserve
RUN \
  --mount=type=cache,id=gomodcache,sharing=locked,target=/root/go/pkg/mod \
  --mount=type=cache,id=gobuildcache,sharing=locked,target=/root/.cache/go-build \
  GOPROXY=off go build -v -trimpath -ldflags "-w -s" -o /usr/local/bin/fsserve .

FROM cgr.dev/chainguard/glibc-dynamic:latest
LABEL org.opencontainers.image.authors="Kevin Wang <kevin@xorkevin.com>"
COPY --link --from=builder /usr/local/bin/fsserve /usr/local/bin/fsserve
EXPOSE 8080
WORKDIR /home/fsserve
ENTRYPOINT ["/usr/local/bin/fsserve", "--config", "/home/fsserve/config/.fsserve.json", "-b", "/home/fsserve/base"]
CMD ["serve", "-p", "8080"]
