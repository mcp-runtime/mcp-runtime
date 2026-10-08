"""Check the manifest and executable architecture of a built platform image."""

import argparse
import json
from pathlib import Path
import struct
import subprocess
import tempfile


MACHINES = {"amd64": 62, "arm64": 183}


def validate_elf(data: bytes, architecture: str) -> None:
    if architecture not in MACHINES:
        raise ValueError(f"unsupported architecture: {architecture}")
    if len(data) < 64 or data[:4] != b"\x7fELF" or data[4] != 2:
        raise ValueError("platform executable is not a 64-bit ELF binary")
    if data[5] not in (1, 2):
        raise ValueError("invalid ELF byte order")
    machine = struct.unpack("<H" if data[5] == 1 else ">H", data[18:20])[0]
    if machine != MACHINES[architecture]:
        raise ValueError(f"ELF machine {machine} does not match {architecture}")


def validate_image(image: str, expected: str | None = None) -> None:
    metadata = json.loads(subprocess.check_output(["docker", "image", "inspect", image]))[0]
    architecture = expected or metadata["Architecture"]
    if metadata["Os"] != "linux" or metadata["Architecture"] != architecture:
        raise ValueError(f"image manifest does not match linux/{architecture}")
    entrypoint = metadata["Config"].get("Entrypoint") or []
    if not entrypoint or not entrypoint[0].startswith("/"):
        raise ValueError("platform image needs an absolute executable entrypoint")
    container = subprocess.check_output(["docker", "create", image], text=True).strip()
    try:
        with tempfile.TemporaryDirectory(prefix="mcp-image-architecture-") as directory:
            binary = Path(directory) / "executable"
            subprocess.run(["docker", "cp", f"{container}:{entrypoint[0]}", str(binary)], check=True)
            with binary.open("rb") as stream:
                validate_elf(stream.read(64), architecture)
    finally:
        subprocess.run(["docker", "rm", container], check=True, stdout=subprocess.DEVNULL)
    print(f"{image}: manifest and ELF executable match linux/{architecture}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image")
    parser.add_argument("--expected-arch", choices=MACHINES)
    args = parser.parse_args()
    validate_image(args.image, args.expected_arch)
