"""Unauthenticated, read-only MCP server for inspecting real tool traffic."""

import os
from datetime import datetime
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from mcp.server.fastmcp import FastMCP
from mcp.server.transport_security import TransportSecuritySettings
from mcp.types import ToolAnnotations


mcp = FastMCP(
    "test-mcpsnoop",
    instructions=(
        "Use echo to inspect arguments and todays_info to inspect structured results."
    ),
    host="0.0.0.0",
    port=int(os.environ.get("PORT", "8088")),
    streamable_http_path=os.environ.get("MCP_PATH", "/mcp"),
    stateless_http=True,
    json_response=True,
    transport_security=TransportSecuritySettings(
        allowed_hosts=["mcp.mcpruntime.org", "localhost:*", "127.0.0.1:*"],
        allowed_origins=[
            "https://mcp.mcpruntime.org",
            "http://localhost:*",
            "http://127.0.0.1:*",
        ],
    ),
)


@mcp.tool(
    annotations=ToolAnnotations(
        readOnlyHint=True,
        destructiveHint=False,
        idempotentHint=True,
        openWorldHint=False,
    )
)
def echo(message: str) -> str:
    """Return the supplied message unchanged."""
    return message


@mcp.tool(
    annotations=ToolAnnotations(
        readOnlyHint=True,
        destructiveHint=False,
        idempotentHint=False,
        openWorldHint=False,
    )
)
def todays_info(timezone_name: str = "UTC") -> dict[str, str]:
    """Return today's date, weekday, and current timestamp in an IANA timezone."""
    try:
        tz = ZoneInfo(timezone_name)
    except (ZoneInfoNotFoundError, ValueError) as exc:
        raise ValueError("Use an IANA timezone such as UTC or Asia/Kolkata.") from exc
    now = datetime.now(tz)
    return {
        "date": now.date().isoformat(),
        "weekday": now.strftime("%A"),
        "timezone": timezone_name,
        "timestamp": now.isoformat(timespec="seconds"),
    }


if __name__ == "__main__":
    mcp.run(transport="streamable-http")
