FROM golang:1.26-alpine AS builder

RUN apk update \
    && apk upgrade && apk add git

WORKDIR /go/src/flouret.io/update-cloudflare

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -a -installsuffix cgo -ldflags "-s -w" -o update-cloudflare .

FROM alpine:3.23.3 AS alpine
RUN apk update && apk upgrade && apk add --no-cache ca-certificates
RUN update-ca-certificates
RUN adduser -D -h / -H -s /sbin/nologin -u 10001 -g "" update-cloudflare

FROM scratch
COPY --from=alpine /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=alpine /etc/passwd /etc/group /etc/
COPY --from=builder /go/src/flouret.io/update-cloudflare/update-cloudflare /
USER update-cloudflare:update-cloudflare
CMD ["/update-cloudflare"]
