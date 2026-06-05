# syntax=docker/dockerfile:1

FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/mysmpp ./cmd/mysmpp
RUN mkdir -p /out/data && touch /out/data/.keep

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/mysmpp /app/mysmpp
COPY --from=build --chown=nonroot:nonroot /out/data /app/data
COPY --chown=nonroot:nonroot configs/docker.json /app/configs/docker.json

EXPOSE 19087 29175
ENV MYSMPP_CONFIG_SEED=/app/configs/docker.json
USER nonroot:nonroot
ENTRYPOINT ["/app/mysmpp"]
CMD ["-config", "/app/data/config.json"]
