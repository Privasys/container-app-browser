# The Privasys renderer: a headless browser as an attested service.
#
# The image is deliberately the heavy half of the pair. Chromium and the
# text recogniser live here, in an enclave with no volume and no
# credentials of its own, so the service that holds the availability
# record and the customer's account can stay small and stay away from
# whatever a watched page decides to return.
#
# Single-arch and provenance-free on purpose: an OCI attestation index
# would change the manifest digest the enclave pins at OID
# 1.3.6.1.4.1.65230.3.2, which is the value a caller checks before
# trusting this service with a credential.

FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/browser ./cmd/browser

FROM alpine:3.21

# Chromium, the fonts a page needs to render as its authors intended,
# and the text recogniser. Everything is pinned by the base image
# digest, so the measurement covers the exact renderer that produced a
# screenshot: a recogniser or a browser fetched at runtime would make
# yesterday's reading unreproducible.
RUN apk add --no-cache \
        ca-certificates \
        chromium \
        nss \
        freetype \
        harfbuzz \
        font-noto \
        font-noto-emoji \
        ttf-freefont \
        tesseract-ocr \
        tesseract-ocr-data-eng \
        tzdata

COPY --from=builder /out/browser /usr/local/bin/browser
COPY privasys.json /privasys.json

ENV BROWSER_CHROMIUM=/usr/bin/chromium

# No fixed port and no EXPOSE: the platform runs containers on the host
# network and injects a unique $PORT per app.
ENTRYPOINT ["/usr/local/bin/browser"]
