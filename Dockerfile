# syntax=docker/dockerfile:1
FROM golang:1.27.4-alpine3.24 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -ldflags="-s -w" -o /out/aperture ./cmd/aperture \
 && go build -trimpath -ldflags="-s -w" -o /out/tracegen ./cmd/tracegen

FROM alpine:3.24
RUN addgroup -S aperture && adduser -S -G aperture -H aperture
COPY --from=build /out/aperture /out/tracegen /usr/local/bin/
USER aperture
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/aperture"]
CMD ["serve"]
