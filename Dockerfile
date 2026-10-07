FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /sessions ./cmd/sessions

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /sessions /usr/local/bin/sessions
USER 65532:65532
ENV PORT=8080 BIND_HOST=0.0.0.0 GOMEMLIMIT=384MiB
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["sessions", "mcp"]
