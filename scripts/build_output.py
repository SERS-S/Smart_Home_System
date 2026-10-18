#!/usr/bin/env python3
"""Run a build step with a transient terminal panel (standard library only)."""

import argparse
import errno
import fcntl
import os
import pty
import re
import select
import shutil
import signal
import subprocess
import sys
import tempfile
import termios
import time
import unicodedata
from collections import deque


ANSI = re.compile(r"\x1b\[[0-?]*[ -/]*[@-~]")


def clipped(text, width, color):
    """Keep SGR colors while clipping visible characters to one terminal row."""
    result = []
    used = 0
    pos = 0
    while pos < len(text):
        escape = ANSI.match(text, pos)
        if escape:
            if color and escape.group().endswith("m"):
                result.append(escape.group())
            pos = escape.end()
            continue
        char = text[pos]
        pos += 1
        if unicodedata.category(char).startswith("C"):
            continue
        size = 0 if unicodedata.combining(char) else (
            2 if unicodedata.east_asian_width(char) in "WF" else 1
        )
        if used + size > width:
            break
        result.append(char)
        used += size
    return "".join(result) + ("\033[0m" if color else "")


def run(label, command):
    if not sys.stdout.isatty() or os.environ.get("TERM", "dumb") == "dumb":
        try:
            status = subprocess.call(command)
        except OSError as error:
            print(f"{label}: {error}", file=sys.stderr)
            return 127 if isinstance(error, FileNotFoundError) else 126
        except KeyboardInterrupt:
            return 130
        return status if status >= 0 else 128 - status

    color = "NO_COLOR" not in os.environ
    cyan, green, red, reset = (
        ("\033[36m", "\033[32m", "\033[31m", "\033[0m")
        if color else ("", "", "", "")
    )
    env = os.environ.copy()
    if color:
        env["CLICOLOR_FORCE"] = "1"
    else:
        env.pop("CLICOLOR_FORCE", None)
        env["CLICOLOR"] = "0"

    lines = deque(maxlen=4)
    pending = b""
    drawn = 0
    last_render = 0.0
    started = time.monotonic()
    master, slave = pty.openpty()
    process = None
    status = 1
    interrupted_status = 130

    def clear():
        nonlocal drawn
        if drawn:
            sys.stdout.write(f"\033[{drawn}A\r\033[J")
            drawn = 0

    def render():
        nonlocal drawn
        terminal = shutil.get_terminal_size()
        width = max(1, terminal.columns - 1)
        row_count = max(2, min(5, terminal.lines - 1))
        elapsed = time.monotonic() - started
        spinner = "|/-\\"[int(elapsed * 8) % 4]
        visible = list(lines)
        if pending:
            visible.append(pending.decode("utf-8", "replace"))
        visible = visible[-(row_count - 1):]
        rows = [f"{cyan}  {spinner} {label} · {elapsed:.1f} с{reset}"]
        rows += ["    " + line for line in visible]
        rows += [""] * (row_count - len(rows))
        if drawn:
            sys.stdout.write(f"\033[{drawn}A\r")
            if drawn != len(rows):
                sys.stdout.write("\033[J")
        sys.stdout.write("".join(
            "\r\033[2K" + clipped(row, width, color) + "\n" for row in rows
        ))
        sys.stdout.flush()
        drawn = len(rows)

    def interrupted(signum, _frame):
        nonlocal interrupted_status
        interrupted_status = 128 + signum
        raise KeyboardInterrupt

    previous_term = signal.signal(signal.SIGTERM, interrupted)

    def attach_terminal():
        # scp shows its progress meter only when it owns a foreground terminal.
        fcntl.ioctl(slave, termios.TIOCSCTTY, 0)

    with tempfile.TemporaryFile() as log:
        try:
            process = subprocess.Popen(
                command, stdin=subprocess.DEVNULL, stdout=slave, stderr=slave,
                env=env, start_new_session=True, preexec_fn=attach_terminal,
            )
            os.close(slave)
            slave = None
            sys.stdout.write("\033[?25l")
            while True:
                ready, _, _ = select.select([master], [], [], 0.12)
                if ready:
                    try:
                        chunk = os.read(master, 65536)
                    except OSError as error:
                        if error.errno != errno.EIO:
                            raise
                        chunk = b""
                    if not chunk:
                        break
                    log.write(chunk)
                    parts = (pending + chunk).replace(b"\r", b"\n").split(b"\n")
                    pending = parts.pop()
                    for part in parts:
                        if part:
                            lines.append(part.decode("utf-8", "replace"))
                if time.monotonic() - last_render >= 0.12:
                    render()
                    last_render = time.monotonic()
            status = process.wait()
        except KeyboardInterrupt:
            status = interrupted_status
        except OSError as error:
            log.write(f"{label}: {error}\n".encode("utf-8"))
            status = 127 if isinstance(error, FileNotFoundError) else 126
        finally:
            if process is not None and process.poll() is None:
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                try:
                    process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    process.wait()
            os.close(master)
            if slave is not None:
                os.close(slave)
            clear()
            sys.stdout.write("\033[?25h")
            sys.stdout.flush()
            signal.signal(signal.SIGTERM, previous_term)

        elapsed = time.monotonic() - started
        if status == 0:
            print(f"{green}  ✓ {label} · {elapsed:.1f} с{reset}")
        else:
            print(f"{red}  ✗ {label} · код {status}{reset}", flush=True)
            log.seek(0)
            shutil.copyfileobj(log, sys.stdout.buffer)
            sys.stdout.buffer.flush()
        return status if status >= 0 else 128 - status


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("label")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if not args.command:
        parser.error("a command is required")
    sys.exit(run(args.label, args.command))
