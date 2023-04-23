FROM golang:1.20.3-alpine3.17 as builder
WORKDIR /usr/local/src/go/fsserve
RUN adduser -u 10001 -D fsserve
RUN apk add --no-cache ca-certificates tzdata mailcap
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . ./
RUN go build -v -trimpath -ldflags "-w -s" -o /usr/local/bin/fsserve .

FROM scratch
MAINTAINER xorkevin <kevin@xorkevin.com>
WORKDIR /home/fsserve
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/
COPY --from=builder /etc/mime.types /etc/
COPY --from=builder /etc/passwd /etc/
COPY --from=builder /usr/local/bin/fsserve /usr/local/bin/
VOLUME /home/fsserve/config
VOLUME /home/fsserve/base
EXPOSE 8080
USER fsserve
ENTRYPOINT ["/usr/local/bin/fsserve"]
CMD ["serve", "--config", "/home/fsserve/config/.fsserve.json", "-p", "8080", "-b", "/home/fsserve/base"]
