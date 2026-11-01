"""Runs impacket's rdp_check against this machine, as a client that shares
no code with ours, so that the Windows RDP workflow can tell the client
from the machine.

rdp_check has no way to read a password from stdin. Given a target that
names none it calls getpass, so this hands it the password from the
environment there, and the password is on no command line.

What it logs is for comparing with the Go client's attempts: when each
call came, what TLS came to, and how much crossed, but nothing of what
crossed. "Access Granted" means only that rdp_check sent the credentials:
what says Windows accepted them is the server's own logon events, at the
times logged here.
"""

import datetime
import getpass
import os
import runpy
import sys
import traceback
from pathlib import Path

from OpenSSL import SSL


def stamp():
    now = datetime.datetime.now(datetime.timezone.utc)
    return now.strftime("%H:%M:%S.") + f"{now.microsecond // 1000:03d}"


def log(message):
    print(f"{stamp()} {message}", flush=True)


def tls_details(connection):
    details = []
    for name in ("get_protocol_version_name", "get_cipher_name", "get_cipher_bits", "get_group_name"):
        call = getattr(connection, name, None)
        if call is None:
            details.append(f"{name}: not in this pyOpenSSL")
            continue
        try:
            details.append(f"{name}: {call()}")
        except Exception as err:
            details.append(f"{name}: {type(err).__name__}: {err}")
    return "; ".join(details)


def traced(name):
    """Logs each call of an SSL.Connection method, without its data."""
    call = getattr(SSL.Connection, name)

    def wrapper(self, *args, **kwargs):
        log(f"{name}: begins")
        try:
            result = call(self, *args, **kwargs)
        except Exception as err:
            log(f"{name}: {type(err).__name__}: {err}")
            if name == "do_handshake":
                log(f"TLS so far: {tls_details(self)}")
            raise
        if name == "do_handshake":
            log(f"TLS: {tls_details(self)}")
        elif name == "send":
            log(f"send: {result} bytes")
        else:
            log(f"{name}: {len(result)} bytes")
        return result

    setattr(SSL.Connection, name, wrapper)


def main():
    log_path = os.environ["OCCAMY_RDP_CHECK_LOG"]
    sys.stdout = sys.stderr = open(log_path, "w", encoding="utf-8", buffering=1)

    password = os.environ["OCCAMY_RDP_WINDOWS_PASSWORD"]
    getpass.getpass = lambda prompt="": password
    for name in ("do_handshake", "send", "recv"):
        traced(name)

    user = os.environ["OCCAMY_RDP_WINDOWS_USER"]
    host = os.environ["OCCAMY_RDP_WINDOWS_ADDR"].rsplit(":", 1)[0]
    script = Path(sys.executable).parent / "rdp_check.py"
    sys.argv = [str(script), f"{user}@{host}"]
    log(f"rdp_check offers PROTOCOL_HYBRID|PROTOCOL_SSL to {host}:3389 as {user}")
    try:
        runpy.run_path(str(script), run_name="__main__")
        log("rdp_check returned")
    except BaseException:
        log("rdp_check ended with:")
        traceback.print_exc()


if __name__ == "__main__":
    main()
