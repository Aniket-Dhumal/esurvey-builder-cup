# Build context is the repository root, not backend/ -- the image carries the
# mock registry files, which live outside this directory.
#
#   docker build -f backend/Dockerfile .

# --- build -------------------------------------------------------------------
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependencies first, so a source-only change does not re-download the module
# cache on every build.
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./

# CGO off gives a static binary, which is what lets the runtime image be
# distroless. -trimpath keeps build paths out of the binary; the ldflags strip
# the symbol table, which is a smaller image and one less thing handed to
# anyone who gets a copy.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/esurvey-server ./cmd/server

# The test suite runs in the image build. A container that cannot pass its own
# authorization tests should never reach a registry.
RUN go vet ./... && go test ./...

# --- runtime -----------------------------------------------------------------
# distroless/static: libc, CA certificates, timezone data, nothing else. No
# shell and no package manager means no interactive foothold in a container
# that gets compromised, and nothing to patch on a CVE treadmill.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=build /out/esurvey-server /app/esurvey-server

# The registry mocks. AgriStack returns no geometry for any farmer we can
# reach, so these are the only parcels the satellite engine can analyse; see
# mock_files/README.md. Compose mounts them read-only over this copy in
# development so edits do not need a rebuild.
COPY --chown=nonroot:nonroot mock_files/ /app/mock_files/

# The LGD hierarchy for Maharashtra. Reference data, not schema -- it is loaded
# by `esurvey-server lgd-load` rather than by a migration, because LGD ships a
# fresh export rather than a diff and reconciliation is the normal operation.
COPY --chown=nonroot:nonroot reference_data/ /app/reference_data/

# The evidence directory, carried empty so that Docker initialises the named
# volume mounted here with nonroot ownership. This image has no shell to mkdir
# with, and a root-owned volume would make every capture fail at the write.
COPY --chown=nonroot:nonroot evidence/ /app/evidence/

ENV ESURVEY_ADDR=:8090 \
    ESURVEY_MOCK_DIR=/app/mock_files \
    ESURVEY_ENV=production

EXPOSE 8090

# nonroot (uid 65532). Declared explicitly rather than inherited, so that a
# base image change cannot quietly promote this container to root.
USER nonroot:nonroot

HEALTHCHECK --interval=15s --timeout=6s --start-period=30s --retries=3 \
    CMD ["/app/esurvey-server", "healthcheck"]

ENTRYPOINT ["/app/esurvey-server"]
