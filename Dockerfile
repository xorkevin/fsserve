FROM golang:alpine3.16 as builder
RUN apk add --no-cache ca-certificates tzdata mailcap

FROM scratch
MAINTAINER xorkevin <kevin@xorkevin.com>
WORKDIR /home/fsserve
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/
COPY --from=builder /etc/mime.types /etc/
COPY bin/fsserve .
VOLUME /home/fsserve/config
VOLUME /home/fsserve/base
EXPOSE 8080
ENTRYPOINT ["/home/fsserve/fsserve"]
CMD ["serve", "-p", "8080", "-b", "/home/fsserve/base"]
