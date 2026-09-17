#!/usr/bin/env bash
# Create the realm, publish the service keytab, and run the KDC.
#
# The keytab goes into /krb5-share, which revad also mounts: it needs the key to
# validate tickets, and reva's Kerberos manager refuses to start without it, so
# revad waits on this container's healthcheck.
set -euo pipefail

REALM="${KRB5_REALM:-TEST.CERN.CH}"
SHARE=/krb5-share
KEYTAB="$SHARE/service.keytab"
SERVICE_PRINCIPAL="${KRB5_SERVICE_PRINCIPAL:-HTTP/revad}"
# Other names the same service answers to; see below.
SERVICE_ALIASES="${KRB5_SERVICE_ALIASES:-HTTP/localhost}"

mkdir -p "$SHARE"

cat > /etc/krb5.conf <<EOF
[libdefaults]
    default_realm = $REALM
    dns_lookup_realm = false
    dns_lookup_kdc = false
    # The service is reached by container name, and reverse DNS inside compose
    # would turn it into something the keytab knows nothing about.
    rdns = false
    udp_preference_limit = 1

[realms]
    $REALM = {
        kdc = kdc
        admin_server = kdc
    }

[domain_realm]
    revad = $REALM
    .revad = $REALM
EOF

# Publish the client configuration so revad and the test runner use the same one.
cp /etc/krb5.conf "$SHARE/krb5.conf"

if [ ! -f /var/lib/krb5kdc/principal ]; then
    echo "creating the realm $REALM"
    kdb5_util create -s -P "$(head -c 32 /dev/urandom | base64)" -r "$REALM"

    # The test users, with the same passwords the rest of the dev environment
    # uses, so one account works whichever way you authenticate.
    kadmin.local -q "addprinc -pw relativity einstein@$REALM"
    kadmin.local -q "addprinc -pw radioactivity marie@$REALM"

    kadmin.local -q "addprinc -randkey $SERVICE_PRINCIPAL@$REALM"
    # The same revad is reached as "revad" from inside the compose network and
    # as "localhost" from the host running the tests. A client asks the KDC for
    # a ticket named after the host it dialled, so both names need a principal
    # and both need to be in the one keytab revad loads.
    for alias in $SERVICE_ALIASES; do
        kadmin.local -q "addprinc -randkey $alias@$REALM"
    done
    rm -f "$KEYTAB"
    kadmin.local -q "ktadd -k $KEYTAB $SERVICE_PRINCIPAL@$REALM"
    for alias in $SERVICE_ALIASES; do
        kadmin.local -q "ktadd -k $KEYTAB $alias@$REALM"
    done
    # revad runs as a different user in its own container and only reads it.
    chmod 0644 "$KEYTAB"
fi

echo "KDC ready for $REALM, keytab at $KEYTAB"
exec krb5kdc -n -r "$REALM"
