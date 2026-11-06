#!/usr/bin/env python3
"""Run the native Copilot contract test with loopback as its only network."""

import os
from pathlib import Path
import socket
import struct
import sys


def main():
    """Enter a fresh Linux network namespace, enable loopback, and run the test."""
    if sys.platform != "linux":
        raise SystemExit("the native Copilot contract test requires Linux")
    import fcntl
    binary = os.environ.get("SANDBOXED_AGENTS_TEST_COPILOT", "")
    if not binary or not Path(binary).is_absolute():
        raise SystemExit("set SANDBOXED_AGENTS_TEST_COPILOT to an absolute Copilot 1.0.106 path")
    namespace = os.readlink("/proc/self/ns/net")
    if len(sys.argv) == 1:
        os.execvp("unshare", ["unshare", "--user", "--map-root-user", "--net",
                             sys.executable, str(Path(__file__).resolve()), namespace])
    if len(sys.argv) != 2 or namespace == sys.argv[1]:
        raise SystemExit("a fresh network namespace is required")
    if [name for _, name in socket.if_nameindex()] != ["lo"]:
        raise SystemExit("refusing a namespace with non-loopback interfaces")
    with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as control:
        flags = fcntl.ioctl(control, 0x8913, struct.pack("16sH14x", b"lo", 0))  # SIOCGIFFLAGS
        current = struct.unpack("16sH14x", flags)[1]
        fcntl.ioctl(control, 0x8914, struct.pack("16sH14x", b"lo", current | 1))  # SIOCSIFFLAGS, IFF_UP
    print(f"Copilot contract test: {namespace}, loopback only", flush=True)
    os.chdir(Path(__file__).resolve().parent.parent)
    os.execvp("go", ["go", "test", "./internal/manager", "-run",
                     "^TestCopilotProxyNativeClient$", "-count=1", "-v"])


if __name__ == "__main__":
    main()
