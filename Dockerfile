FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tenzo ./cmd/tenzo

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=build /out/tenzo /app/tenzo

ENV TENZO_HTTP__ADDR=:8170

USER nonroot:nonroot
EXPOSE 8170

ENTRYPOINT ["/app/tenzo"]
CMD ["serve"]
