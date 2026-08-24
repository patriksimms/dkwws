# Viewer image. Coolify builds this from the repository root.
FROM golang:1.27-alpine AS build

ARG VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/dkwws-viewer ./cmd/dkwws-viewer

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/dkwws-viewer /usr/local/bin/dkwws-viewer

USER nonroot:nonroot
EXPOSE 8080
ENV DKWWS_LISTEN_ADDR=:8080

ENTRYPOINT ["/usr/local/bin/dkwws-viewer"]
