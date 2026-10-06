# syntax=docker/dockerfile:1
# One image for every role: serve, worker, migrate, rotate-keys.

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/haalchaal ./cmd/haalchaal

# Static binary on a minimal base: no shell, no package manager, CA
# certificates for vendor HTTPS, timezone data embedded in the binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/haalchaal /haalchaal
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/haalchaal"]
CMD ["serve"]
