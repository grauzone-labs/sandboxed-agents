#!/opt/az/bin/python3 -I
"""Exercise the real DevOps credential store without credentials or downloads."""

import os
from pathlib import Path
import sys
import tempfile


def check_credentials():
    """Round-trip a dummy PAT as agent in disposable, private configuration."""
    if (os.getuid(), os.getgid()) != (1000, 1000):
        raise RuntimeError("credential check must run as agent (1000:1000)")
    with tempfile.TemporaryDirectory(prefix="azure-credentials-") as directory:
        os.environ["HOME"] = directory
        os.environ["AZURE_CONFIG_DIR"] = str(Path(directory) / ".azure")
        os.environ["XDG_CONFIG_HOME"] = directory
        os.environ.pop("AZURE_DEVOPS_CONFIG_DIR", None)
        os.environ.pop("DBUS_SESSION_BUS_ADDRESS", None)
        os.environ.pop("PYTHON_KEYRING_BACKEND", None)
        os.environ["AZURE_CORE_COLLECT_TELEMETRY"] = "0"
        os.environ["PIP_NO_INDEX"] = "1"
        from azure.cli.core.extension import get_extension_path

        # An explicit path permits the same check against a local wheel install.
        extension_path = sys.argv[1] if len(sys.argv) == 2 else get_extension_path("azure-devops")
        if os.access(extension_path, os.W_OK):
            raise RuntimeError("credential check requires an extension directory agent cannot write")
        sys.path.insert(0, extension_path)
        attempts = []

        def forbid_downloads(event, args):
            """Refuse network connections and child processes, including pip."""
            if event in ("socket.connect", "socket.getaddrinfo", "subprocess.Popen", "os.system", "os.exec", "os.posix_spawn"):
                attempts.append(event)
                raise RuntimeError("credential check forbids network and child processes")

        sys.addaudithook(forbid_downloads)
        from azext_devops.dev.common.credential_store import CredentialStore

        store = CredentialStore()
        key = "https://dev.azure.com/sandboxed-agents-NONSECRET-test"
        for token in ("NONSECRET-dummy-token", "NONSECRET-replacement-token"):
            store.set_password(key, token)
            if store.get_password(key) != token:
                raise RuntimeError("credential read did not match dummy write")
        credential_file = Path(store._PAT_FILE)
        if not credential_file.is_relative_to(directory):
            raise RuntimeError("credential escaped disposable configuration")
        stat = credential_file.stat()
        if stat.st_mode & 0o777 != 0o600 or stat.st_uid != 1000:
            raise RuntimeError("credential file must be private and owned by agent")
        store.clear_password(key)
        if store.get_password(key) is not None:
            raise RuntimeError("credential survived deletion")
        if attempts:
            raise RuntimeError("credential operation attempted network or a child process")
        print("Azure DevOps credential write/read/replace/delete passed as agent; downloads forbidden")


if __name__ == "__main__":
    check_credentials()
