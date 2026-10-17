"""Stream private disposable browser fixtures to stdin; never write artifacts."""
import json
import os
from pathlib import Path
import sys


def main():
    run = os.environ["STAGING_MT_RUN_ID"]
    source = Path(os.environ["STAGING_MT_CONFIG_DIR"]) / f"demo-credentials-{run}.json"
    passwords = json.loads(source.read_text())
    fixture = {
        "baseURL": os.environ["PLATFORM_URL"],
        "candidate": os.environ["WORKSPACE_BROWSER_CANDIDATE"],
        "roles": {
            "admin": {"email": os.environ["MCP_PLATFORM_ADMIN_EMAIL"], "password": os.environ["MCP_PLATFORM_ADMIN_PASSWORD"]},
            "owner": {"email": os.environ.get("ACME_EMAIL", f"acme-owner-{run}@example.com"), "password": passwords["acme"], "teamSlug": os.environ.get("ACME_SLUG", f"acme-{run}")},
            "member": {"email": os.environ.get("GLOBEX_EMAIL", f"globex-user-{run}@example.com"), "password": passwords["globex"], "teamSlug": os.environ.get("GLOBEX_SLUG", f"globex-{run}")},
        },
    }
    ca_file = os.environ.get("SSL_CERT_FILE") or os.environ.get("CURL_CA_BUNDLE")
    if ca_file:
        fixture["caPEM"] = Path(ca_file).read_text()
    if not all(role["email"] and role["password"] for role in fixture["roles"].values()):
        raise ValueError("missing disposable browser role fixture")
    json.dump(fixture, sys.stdout)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Do not echo file contents, password-bearing dictionaries or exception
        # context into the stage log when fixture configuration is incomplete.
        raise SystemExit("could not load complete disposable browser fixtures") from None
