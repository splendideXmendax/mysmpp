# syntax=docker/dockerfile:1

FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/mysmpp ./cmd/mysmpp

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/mysmpp /app/mysmpp
COPY configs/example.json /app/configs/example.json

EXPOSE 8080 2775
USER nonroot:nonroot
ENTRYPOINT ["/app/mysmpp"]
CMD ["-config", "/app/configs/example.json"]
