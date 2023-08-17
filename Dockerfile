FROM cgr.dev/chainguard/go:latest as builder
WORKDIR /usr/local/src/go/fsserve
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . ./
RUN CGO_ENABLED=0 go build -v -trimpath -ldflags "-w -s" -o /usr/local/bin/fsserve .

FROM cgr.dev/chainguard/static:latest
MAINTAINER Kevin Wang <kevin@xorkevin.com>
WORKDIR /home/fsserve
COPY --from=builder /usr/local/bin/fsserve /usr/local/bin/
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/fsserve"]
CMD ["serve", "--config", "/home/fsserve/config/.fsserve.json", "-p", "8080", "-b", "/home/fsserve/base"]
