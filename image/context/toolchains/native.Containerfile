RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends build-essential cmake ninja-build pkg-config gdb \
    && rm -rf /var/lib/apt/lists/*
