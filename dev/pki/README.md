# Dev PKI

Certificates for the development environment, signed by the CA in this
directory. Nothing here is secret and nothing here should ever be trusted
outside `docker compose`: the private keys are committed so the environment
comes up with no setup step.

The reva instances serve TLS because OCM requires it. reva's discovery client
forces `https` for any peer that is not localhost, so two plain-HTTP instances
can never federate with each other, however they are configured.

Each certificate carries its compose service name, plus `localhost` and
`127.0.0.1` so the same certificate works from inside the network and from the
host running the tests.

To regenerate one (they last ten years):

```bash
name=revad   # or revad-partner, or eos-storage
openssl req -new -newkey rsa:2048 -nodes \
    -keyout "$name.key" -out "/tmp/$name.csr" -subj "/CN=$name"
printf 'subjectAltName = DNS:%s, DNS:localhost, IP:127.0.0.1\n' "$name" > "/tmp/$name.ext"
openssl x509 -req -in "/tmp/$name.csr" -CA ca.crt -CAkey ca.key -CAcreateserial \
    -out "$name.crt" -days 3650 -sha256 -extfile "/tmp/$name.ext"
```

`ca.srl`, openssl's serial bookkeeping, is not kept: `-CAcreateserial` writes it
again whenever it is missing.
