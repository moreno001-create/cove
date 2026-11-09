# Cove's image: the program, with Tesseract and Poppler for reading scanned pages. Build with: docker build -t cove .
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /cove ./cmd/cove

# Alpine, for Tesseract (reading the words in scans and photos of papers) and Poppler (PDF text).
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tesseract-ocr tesseract-ocr-data-eng poppler-utils
COPY --from=build /cove /cove
ENV COVE_DATA=/data COVE_ADDR=:8080
VOLUME /data
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/cove"]
