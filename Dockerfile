# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/webgate ./cmd/webgate

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/webgate /webgate
ENV WEBGATE_ADDR=:8787
EXPOSE 8787
USER nonroot:nonroot
ENTRYPOINT ["/webgate"]
