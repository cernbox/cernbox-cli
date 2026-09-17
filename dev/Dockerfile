FROM golang:1.25 AS builder

WORKDIR /home/reva
ENV CGO_ENABLED 0
RUN go install github.com/cs3org/gaia@latest && gaia build -o revad

FROM scratch

# COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /home/reva/revad revad

ENTRYPOINT [ "./revad" ]
CMD [ "-c", "/etc/revad/cernbox.toml", "-p", "/var/run/revad.pid" ]