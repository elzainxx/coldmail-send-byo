# Brain (Go orchestrator) — the ONLY DB client and sole decryptor.
FROM golang:1.23-alpine AS build
WORKDIR /src
# module files first for layer caching
COPY services/brain/go.mod services/brain/go.sum ./
RUN go mod download
COPY services/brain/ ./
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -o /out/brain ./cmd/brain

FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl && adduser -D -u 10001 brain
USER brain
COPY --from=build /out/brain /usr/local/bin/brain
# Health + boxlink ports (bound via env in compose).
EXPOSE 8080 8443
ENTRYPOINT ["/usr/local/bin/brain"]
