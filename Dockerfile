FROM golang:1.23 AS builder

WORKDIR /app

COPY . .
# build
# RUN go generate ./...
RUN CGO_ENABLED=1 go build -o bin/ -tags='netgo timetzdata' -trimpath -a -ldflags '-s -w -linkmode external -extldflags "-static"'  ./cmd/cmcd

FROM debian:bookworm-slim
LABEL maintainer="The Sia Foundation <info@sia.tech>" \
    org.opencontainers.image.description.vendor="The Sia Foundation" \
    org.opencontainers.image.description="A Sia supply API - provides the supply of Siacoin in the format specified by CoinMarketCap" \
    org.opencontainers.image.source="https://github.com/SiaFoundation/cmc-supply-api" \
    org.opencontainers.image.licenses=MIT

ENV PUID=0
ENV PGID=0

# copy binary and prepare data dir.
COPY --from=builder /app/bin/* /usr/bin/
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
VOLUME [ "/data" ]

# API port
EXPOSE 8080/tcp

USER ${PUID}:${PGID}

ENTRYPOINT [ "cmcd", "-dir", "/data" ]
