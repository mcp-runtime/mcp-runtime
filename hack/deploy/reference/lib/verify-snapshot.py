#!/usr/bin/env python3
"""Verify a protected recovery snapshot before any cluster mutation."""
import argparse
import hashlib
import json
from pathlib import Path


def verify(root, cluster_uid, full=False):
    root = Path(root).resolve(strict=True)
    if not (root / "BACKUP_COMPLETE").is_file():
        raise ValueError("snapshot is incomplete")
    if full and not (root / "FULL_BACKUP_COMPLETE").is_file():
        raise ValueError("reset requires a completed full backup, not a TLS/setup snapshot")
    sums = root / "SHA256SUMS"
    entries = {}
    for line in sums.read_text().splitlines():
        digest, name = line.split("  ", 1)
        path = root / name
        if path.is_symlink() or not path.resolve().is_relative_to(root):
            raise ValueError("unsafe snapshot member")
        if name in entries or len(digest) != 64:
            raise ValueError("invalid checksum entry")
        with path.open("rb") as source:
            digest_hash = hashlib.sha256()
            for block in iter(lambda: source.read(1024 * 1024), b""):
                digest_hash.update(block)
            actual = digest_hash.hexdigest()
        if actual != digest:
            raise ValueError(f"checksum mismatch: {name}")
        entries[name] = digest
    expected = {str(p.relative_to(root)) for p in root.rglob("*") if p.is_file()
                and p.name not in {"SHA256SUMS", "BACKUP_COMPLETE", "FULL_BACKUP_COMPLETE"}}
    if set(entries) != expected:
        raise ValueError("snapshot has unverified or missing files")
    if "cluster-identity.json" not in entries:
        raise ValueError("snapshot has no verified cluster identity")
    identity = json.loads((root / "cluster-identity.json").read_text())
    if identity["clusterUID"] != cluster_uid:
        raise ValueError("snapshot belongs to another cluster")
    if full and not {"k3s-state.db", "k3s-host-and-pv-data.tar.gz"} <= set(entries):
        raise ValueError("full recovery data is missing")
    return root


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("snapshot")
    parser.add_argument("cluster_uid")
    parser.add_argument("--full", action="store_true")
    args = parser.parse_args()
    try:
        print(verify(args.snapshot, args.cluster_uid, args.full))
    except (ValueError, OSError, KeyError) as error:
        parser.exit(1, f"Invalid recovery snapshot: {error}\n")
