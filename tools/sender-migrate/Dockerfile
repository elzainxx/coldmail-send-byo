# Migration runner image. Built from the repo root (context .) so it can copy the module.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY tools/sender-migrate/go.mod tools/sender-migrate/go.sum ./
RUN go mod download
COPY tools/sender-migrate/ ./
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -o /out/sender-migrate .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/sender-migrate /usr/local/bin/sender-migrate
# db/ (migrations + preflight) is mounted at /db by compose.
ENTRYPOINT ["/usr/local/bin/sender-migrate"]
CMD ["up"]
