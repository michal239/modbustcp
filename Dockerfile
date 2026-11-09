FROM golang:1.25-alpine
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
CMD ["sh", "-c", "go build -buildvcs=false -o /tmp/collector . && exec /tmp/collector"]
