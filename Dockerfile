FROM scratch
MAINTAINER xorkevin <kevin@xorkevin.com>
WORKDIR /home/fsserve
COPY bin/fsserve .
VOLUME /home/fsserve/config
VOLUME /home/fsserve/base
EXPOSE 8080
ENTRYPOINT ["/home/fsserve/fsserve"]
CMD ["serve", "-p", "8080", "-b", "/home/fsserve/base"]
