# Both packages are maintained in the Debian 13 repositories; extensions are system-wide.
RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends azure-cli python3-azext-devops \
    && az devops --help >/dev/null \
    && rm -rf /var/lib/apt/lists/* /root/.azure
