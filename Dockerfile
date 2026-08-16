# The spec is a submodule, so this build needs it checked out:
#   git submodule update --init --recursive
# It is COPYed into the image rather than fetched at build time, which keeps
# the image pinned to exactly the commit the source tree was built from.

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Fail the build rather than ship an image whose --spec default points at
# nothing: an uninitialised submodule would otherwise produce a binary that
# cannot run at all, and the failure would land on the user.
RUN test -f spec/api/openapi.yaml || \
    (echo "spec/api/openapi.yaml is missing — run: git submodule update --init --recursive" && exit 1)
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /dedi-conformance ./cmd/dedi-conformance

FROM alpine:3.20
# Certificates, because the publication profile fetches a publisher's
# well-known over TLS and a scratch image cannot verify one.
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /dedi-conformance /usr/local/bin/dedi-conformance
COPY --from=build /src/spec/api/openapi.yaml /app/spec/api/openapi.yaml
COPY --from=build /src/spec/schemas /app/spec/schemas

# Runs as nobody: the suite only ever issues GETs, so it needs no privilege,
# and a conformance tool people point at production should not ask for any.
USER nobody

ENTRYPOINT ["dedi-conformance"]
CMD ["--manifest", "/work/dedi-conformance.json"]
