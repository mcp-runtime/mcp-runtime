# example-python-2025-11-25

Python server using the official MCP Python SDK 1.x (`mcp[cli]<2`). The SDK's
highest supported MCP protocol revision is `2025-11-25`; the server uses the
legacy initialize/session flow.

Build and run locally:

```bash
python app.py
```

The Dockerfile installs the pinned SDK major range and builds the server image.
Its checked-in Runtime server identity ends in `-gateway` because the metadata
enables the Runtime gateway for policy, sessions, and audit.
