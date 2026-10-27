# Microsoft's documented fallback for newer Debian releases without their own suite.
# CLI follows bookworm's current release; DevOps is pinned with its published SHA-256.
COPY base/check-signing-key.sh toolchains/microsoft-signing-key.fingerprint toolchains/azure-devops.sha256 /tmp/azure-install/
ENV AZURE_EXTENSION_SYS_DIR=/opt/azure-cli-extensions \
    AZURE_EXTENSION_USE_DYNAMIC_INSTALL=no \
    AZURE_CORE_COLLECT_TELEMETRY=no
RUN set -eu; \
    curl -fsSL https://packages.microsoft.com/keys/microsoft.asc -o /tmp/azure-install/microsoft.asc; \
    bash /tmp/azure-install/check-signing-key.sh /tmp/azure-install/microsoft.asc /tmp/azure-install/microsoft-signing-key.fingerprint Microsoft; \
    mkdir -p /etc/apt/keyrings; \
    gpg --batch --yes --dearmor --output /etc/apt/keyrings/azure-cli-microsoft.gpg /tmp/azure-install/microsoft.asc; \
    chmod 0644 /etc/apt/keyrings/azure-cli-microsoft.gpg; \
    printf '%s\n' 'deb [arch=amd64 signed-by=/etc/apt/keyrings/azure-cli-microsoft.gpg] https://packages.microsoft.com/repos/azure-cli/ bookworm main' \
        > /etc/apt/sources.list.d/azure-cli.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends azure-cli/bookworm; \
    cd /tmp/azure-install; \
    curl -fsSL https://azcliprod.blob.core.windows.net/cli-extensions/azure_devops-1.0.10-py2.py3-none-any.whl -o azure_devops-1.0.10-py2.py3-none-any.whl; \
    sha256sum -c azure-devops.sha256; \
    PIP_NO_DEPS=1 PIP_NO_INDEX=1 az extension add --system --source /tmp/azure-install/azure_devops-1.0.10-py2.py3-none-any.whl; \
    chmod -R a+rX /opt/azure-cli-extensions; \
    az devops --help >/dev/null; \
    cd /; \
    rm -rf /tmp/azure-install /var/lib/apt/lists/* /root/.azure
