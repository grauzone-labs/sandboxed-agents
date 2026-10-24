---
status: accepted
---

# The readiness key exchange runs ssh with a private known_hosts file

The SSH check of `update`'s readiness wait (ADR-0007) runs the host's `ssh -v` with `-F none`, `-E ssh.log`, `BatchMode=yes`, `StrictHostKeyChecking=accept-new`, `IdentityAgent=none`, and `UserKnownHostsFile=known_hosts`, with every authentication method off, in a new temporary directory that is its working directory. The sandbox counts as reachable over SSH when `ssh` writes `debug1: SSH2_MSG_NEWKEYS received` to its log file `ssh.log` and the known_hosts file holds an Ed25519 host key for the loopback port. Authentication then fails, so the exit status of `ssh` is not used. The temporary directory is removed after each attempt.

## Reasons

- **It completes a real key exchange on every supported host.** The sandbox's sshd offers the hybrid post-quantum method `sntrup761x25519-sha512@openssh.com`. `ssh` proposes only the key exchange methods its build supports, so it negotiates a method it can compute, also with the inbox Windows OpenSSH Client 9.5, which is a supported prerequisite ([Win32-OpenSSH #2140](https://github.com/PowerShell/Win32-OpenSSH/issues/2140), [openssh-portable #756](https://github.com/PowerShell/openssh-portable/pull/756)).
- **It needs no further prerequisite.** `ssh` is one of the OpenSSH prerequisites `ssh` and `ssh-keygen` (ADR-0002).
- **The NEWKEYS line shows a verified exchange.** `ssh` records the host key as soon as it arrives, before it verifies the server's signature over the exchange; it sends its NEWKEYS and accepts the server's only after that verification (`input_kex_gen_reply` in `kexgen.c` and `kex_send_newkeys` in `kex.c`, in OpenSSH 9.5 and in current releases). The NEWKEYS line therefore shows the completed key exchange, and the recorded key shows that it came with an Ed25519 host key for that port.
- **It leaves the user's SSH files alone.** The check writes no file in the user's SSH directory or in host state, and with `-F none` it reads no SSH configuration. Because it names no identity file, `ssh` looks up its default identity files in the user's SSH directory and may read them, a private key file included, to load their public keys; this follows from the OpenSSH 10.0 source (`load_public_identity_files` in `ssh.c`, `sshkey_load_public` in `authfile.c`). With every authentication method off and `IdentityAgent=none`, `ssh` never uses these keys to authenticate.

## Considered options

- **A separate key-scanning program of the OpenSSH client.** Rejected: it is not one of the OpenSSH prerequisites, and the scanner of the inbox Windows OpenSSH Client 9.5 proposes `sntrup761x25519-sha512@openssh.com`, which its Windows build cannot compute ([Win32-OpenSSH #2140](https://github.com/PowerShell/Win32-OpenSSH/issues/2140)), so it cannot complete the key exchange with the sandbox's sshd.
- **Removing `sntrup761x25519` from the image's sshd configuration.** Rejected: it would take the hybrid post-quantum key exchange away from every client to work around one client tool.
- **An SSH library such as `golang.org/x/crypto/ssh`.** Rejected: the module's dependencies are limited to the standard library and `golang.org/x/sys`.
- **A key exchange implemented in the executable.** Rejected: protocol and cryptography code to maintain for a readiness check.
- **A TCP connect or SSH banner check.** Rejected for the reason in ADR-0007: port forwarding can accept a connection before sshd listens, and the check is meant to show a completed key exchange.
- **Requiring a newer Windows OpenSSH Client.** Rejected: the inbox Windows OpenSSH Client is a supported prerequisite.
- **The recorded host key alone.** Rejected: `ssh` records it before it verifies the exchange signature, so a server that sends a valid host key and a wrong signature, or none, would count as ready.
- **The authentication refusal of `ssh`, such as `Permission denied (publickey)`.** Rejected: it depends on the server's authentication settings, and a server that accepts a connection without authentication never sends it.

## Consequences

- The probe also sets `GlobalKnownHostsFile=none`, `HostKeyAlgorithms=ssh-ed25519`, and `HashKnownHosts=no`: the temporary file is the only host-key store, the negotiated host key is Ed25519, and the loopback entry can be checked directly.
- Each readiness attempt makes one unauthenticated SSH connection that ends before sign-in.
- The readiness wait uses no OpenSSH program beyond the prerequisites `ssh` and `ssh-keygen`, so the preflight checks nothing further for it.
- `ssh` writes its log to the file `ssh.log` that `-E` names, and the check reads that file; what `ssh` writes to its standard output or standard error is not used. The inbox Windows OpenSSH Client 9.5 writes its log and its channel output through its own POSIX layer, which waits for each write to complete asynchronously; on a pipe or file handle that the calling program created, as Go's `exec` and .NET's `Process` do, that completion never arrives, so `ssh` blocks in its first log line before it connects (`fileio_write` in `contrib/win32/win32compat/fileio.c` of [openssh-portable v9.5.0.0](https://github.com/PowerShell/openssh-portable/tree/v9.5.0.0)). A file that `ssh` opens itself, as `-E` does, is opened for asynchronous completion and completes. Output that the client prints through the C runtime, such as `ssh -V` and `ssh -G`, is not affected.
- The known_hosts and log files are named relative to the working directory of `ssh`, so its command line holds no path. A path would need quoting for `ssh`'s option parser, and Windows programs split a quoted argument in different ways: the inbox Windows OpenSSH Client reads a quoted path as one argument, while Git for Windows' `ssh`, which runs on the MSYS2 runtime, can stop with `invalid quotes` on a quoted path that holds no space. The inbox Windows OpenSSH Client stays the supported Windows client.
- The check depends on a debug line of OpenSSH. A test drives the host's real `ssh` against a test SSH server whose exchange is valid, has a corrupted signature, or has no signature, and expects only the first to count as ready; it runs wherever `ssh` is on `PATH`.
