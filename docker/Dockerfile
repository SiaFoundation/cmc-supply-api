FROM docker.io/library/golang:1.20 AS builder

WORKDIR /coinbase

COPY . .
# build
# RUN go generate ./...
RUN go build -o bin/ -tags='netgo timetzdata' -trimpath -a -ldflags '-s -w -linkmode external -extldflags "-static"'  ./cmd/cmcd

FROM docker.io/library/alpine:3
LABEL maintainer="The Sia Foundation <info@sia.tech>" \
      org.opencontainers.image.description.vendor="The Sia Foundation" \
      org.opencontainers.image.description="A Sia supply API - provides the Siacoin supply in the format specified by  statistics for the Sia network" \
      org.opencontainers.image.source="https://github.com/SiaFoundation/cmc-supply-api" \
      org.opencontainers.image.licenses=MIT

ENV PUID=0
ENV PGID=0

# copy binary and prepare data dir.
COPY --from=builder /coinbase/bin/* /usr/bin/
VOLUME [ "/data" ]

# API port
EXPOSE 9980/tcp
# RPC port
EXPOSE 9981/tcp

USER ${PUID}:${PGID}

ENTRYPOINT [ "cmcd", "-log.stdout", "-dir", "/data", "-api", ":9980" ]