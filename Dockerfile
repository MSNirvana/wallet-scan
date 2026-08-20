FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/wallet-scan ./cmd/wallet-scan

FROM golang:1.24-alpine
RUN addgroup -S scanner && adduser -S -G scanner scanner
COPY --from=build /out/wallet-scan /usr/local/bin/wallet-scan
USER scanner
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/wallet-scan"]
