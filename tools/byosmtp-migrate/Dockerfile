# Migration runner image. Built from the repo root (context .) so it can copy the module.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY tools/byosmtp-migrate/go.mod tools/byosmtp-migrate/go.sum ./
RUN go mod download
COPY tools/byosmtp-migrate/ ./
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -o /out/byosmtp-migrate .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/byosmtp-migrate /usr/local/bin/byosmtp-migrate
# db/ (migrations + preflight) is mounted at /db by compose.
ENTRYPOINT ["/usr/local/bin/byosmtp-migrate"]
CMD ["up"]
