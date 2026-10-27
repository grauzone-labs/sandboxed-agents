#!/bin/sh
set -eu
manifest=/usr/local/share/sandboxed-agents/versions.txt
{
    cat /etc/os-release
    dpkg-query -W -f='${Package}=${Version}\n'
    node --version
    npm --version
    /usr/local/bin/sandboxed-agents-manager version
    if command -v dotnet >/dev/null; then dotnet --list-sdks; fi
    if command -v playwright >/dev/null; then playwright --version; fi
    if command -v az >/dev/null; then
        az version
        az extension list
        cat /usr/local/share/sandboxed-agents/azure-devops-requirements.txt
        rm -rf /root/.azure
    fi
    if command -v cc >/dev/null; then cc --version; fi
} > "$manifest"
