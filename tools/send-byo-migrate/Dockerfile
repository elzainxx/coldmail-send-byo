# Migration runner image. Built from the repo root (context .) so it can copy the module.
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY tools/send-byo-migrate/go.mod tools/send-byo-migrate/go.sum ./
RUN go mod download
COPY tools/send-byo-migrate/ ./
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -o /out/send-byo-migrate .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/send-byo-migrate /usr/local/bin/send-byo-migrate
# db/ (migrations + preflight) is mounted at /db by compose.
ENTRYPOINT ["/usr/local/bin/send-byo-migrate"]
CMD ["up"]
