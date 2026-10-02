FROM golang:1.25 AS builder

WORKDIR /home/reva

# musl-gcc, for the static cgo build below.
RUN apt-get update \
 && apt-get install -y --no-install-recommends musl-tools \
 && rm -rf /var/lib/apt/lists/*

# A reva integration branch, cernbox-cli-dev, that joins the two pieces this
# environment needs and that are not in master yet: Kerberos authentication
# (cs3org/reva#5827: the auth manager and the SPNEGO credential strategy) and
# the admin HTTP service --as uses (cs3org/reva#5863). It has no pull request of
# its own. gaia resolves a branch name through the GitHub API, so this follows
# its head; point it back at master, or a release, once both are merged.
ARG REVA_VERSION=cernbox-cli-dev

# cgo, rather than CGO_ENABLED=0: the sql share driver runs on sqlite, which
# needs cgo, and it is the only share driver whose "not found" error the
# gateway's probe accepts — the others make every share request fail. Linking
# statically against musl keeps the final image on scratch anyway.
ENV CGO_ENABLED=1
RUN go install github.com/cs3org/gaia@latest \
 && gaia build ${REVA_VERSION} --static-musl -o revad

FROM scratch

COPY --from=builder /home/reva/revad revad

ENTRYPOINT [ "./revad" ]
CMD [ "-c", "/etc/revad/cernbox.toml", "-p", "/var/run/revad.pid" ]
