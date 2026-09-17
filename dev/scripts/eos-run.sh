#!/bin/bash

# Inject a config entry into the [mgm:xrootd:mgm] section of the mgm config file.
# The first space-delimited token is the key; the rest is the value.
# Usage: inject_mgm_config "key value..."
inject_mgm_config() {
    local entry="$1"
    local key="${entry%% *}"
    local value="${entry#* }"
    local config_file="/etc/eos/config/mgm/mgm"

    if grep -qP "^\s*#*\s*${key}(\s|$)" "$config_file" 2>/dev/null; then
        sed -i "/^\[mgm:xrootd:mgm\]/,/^\[/{s|^\s*#*\s*${key}.*|${entry}|}" "$config_file"
    else
        sed -i "/^\[mgm:xrootd:mgm\]/a ${entry}" "$config_file"
    fi
}

# Inject a config entry into the [fst:xrootd:fst] section of the fst config file.
# The first space-delimited token is the key; the rest is the value.
# Usage: inject_fst_config "key value..."
inject_fst_config() {
    local entry="$1"
    local key="${entry%% *}"
    local value="${entry#* }"
    local config_file="/etc/eos/config/fst/fst"

    if grep -qP "^\s*#*\s*${key}(\s|$)" "$config_file" 2>/dev/null; then
        sed -i "/^\[fst:xrootd:fst\]/,/^\[/{s|^\s*#*\s*${key}.*|${entry}|}" "$config_file"
    else
        sed -i "/^\[fst:xrootd:fst\]/a ${entry}" "$config_file"
    fi
}

# Inject an env variable into the [sysconfig] section of the mgm config file.
# Format: KEY=value
# Usage: inject_mgm_sysconfig "KEY=value"
inject_mgm_sysconfig() {
    local entry="$1"
    local key="${entry%%=*}"
    local config_file="/etc/eos/config/mgm/mgm"

    if grep -qP "^\s*#*\s*${key}=" "$config_file" 2>/dev/null; then
        sed -i "/^\[sysconfig\]/,/^\[/{s|^\s*#*\s*${key}=.*|${entry}|}" "$config_file"
    else
        sed -i "/^\[sysconfig\]/a ${entry}" "$config_file"
    fi
}

# Inject an env variable into the [sysconfig] section of the fst config file.
# Format: KEY=value
# Usage: inject_fst_sysconfig "KEY=value"
inject_fst_sysconfig() {
    local entry="$1"
    local key="${entry%%=*}"
    local config_file="/etc/eos/config/fst/fst"

    if grep -qP "^\s*#*\s*${key}=" "$config_file" 2>/dev/null; then
        sed -i "/^\[sysconfig\]/,/^\[/{s|^\s*#*\s*${key}=.*|${entry}|}" "$config_file"
    else
        sed -i "/^\[sysconfig\]/a ${entry}" "$config_file"
    fi
}

eos daemon sss recreate

inject_mgm_config "xrd.protocol XrdHttp:8443 libXrdHttp.so"
inject_mgm_config "http.trace all"
inject_mgm_config "http.exthandler EosMgmHttp /usr/lib64/libEosMgmHttp.so eos::mgm::http::redirect-to-https=0"
inject_mgm_config "http.cadir /etc/grid-security/certificates/"
inject_mgm_config "http.cert /etc/grid-security/eos.crt"
inject_mgm_config "http.key /etc/grid-security/eos.key"
# inject_mgm_sysconfig "EOS_MGM_GRPC_SSL_CERT=/etc/grid-security/eos.crt"
# inject_mgm_sysconfig "EOS_MGM_GRPC_SSL_KEY=/etc/grid-security/eos.key"
# inject_mgm_sysconfig "EOS_MGM_GRPC_SSL_CA=/etc/grid-security/certificates/ca.crt"

inject_fst_config "xrd.protocol XrdHttp:8001 libXrdHttp.so"
inject_fst_config "http.exthandler EosFstHttp /usr/lib64/libEosFstHttp.so none"
inject_fst_config "http.cadir /etc/grid-security/certificates/"
inject_fst_config "http.cert /etc/grid-security/eos.crt"
inject_fst_config "http.key /etc/grid-security/eos.key"

eos daemon run qdb &
eos daemon run mgm &
eos daemon run fst &
sssd

# wait namespace to boot
sleep 5

for name in 01; do
  mkdir -p /data/fst/$name;
  chown daemon:daemon /data/fst/$name
done
eos space define default

eosfstregister -r localhost /data/fst/ default:1

eos space set default on

eos mkdir -p /eos/user

# create cbox sudoer user
adduser cbox -u 58679
eos vid set membership 0 +sudo
eos vid set membership cbox +sudo
eos vid set membership daemon +sudo
eos vid set membership cbox -uids $(id adm -u)
eos vid set membership daemon -uids $(id adm -u)

# enable and configure grpc and http
eos vid set map -https key:eos_auth_key vuid:58679 vgid:58679
eos vid set map -grpc key:eos_auth_key vuid:58679 vgid:58679

eos vid add gateway '*' grpc
eos vid add gateway '*' https

# Create directories for users
# Each entry is "username:uid"
for entry in "einstein:10000" "marie:10001" "richard:10002"; do
  user="${entry%%:*}"
  uid="${entry#*:}"
  adduser "$user" -u "$uid"
  eospath="/eos/user/${user:0:1}/${user}"
  eos mkdir -p "$eospath"
  eos chown -R "${uid}" "$eospath"
  # Set 100GB "project" quota
  eos quota set -g 99 -v 100000000000 -p "$eospath"
done

tail -f /var/log/eos/fst/xrdlog.fst /var/log/eos/mgm/xrdlog.mgm