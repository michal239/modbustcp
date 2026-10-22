FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /app .

FROM alpine:3.20
COPY --from=build /app /app
ENTRYPOINT ["/app"]
