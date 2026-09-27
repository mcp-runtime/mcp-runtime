import os
import re
from datetime import datetime, timezone

from mcp.server.fastmcp import FastMCP
from mcp.server.transport_security import TransportSecuritySettings


mcp = FastMCP(
    "example-python-2025-11-25",
    instructions=(
        "Data utility MCP server for text cleanup, lightweight math, "
        "timestamping, task summaries, prompts, and reference resources."
    ),
    transport_security=TransportSecuritySettings(enable_dns_rebinding_protection=False),
)


_NON_SLUG = re.compile(r"[^a-z0-9]+")
_WORDS = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]*")
_NUMBERS = re.compile(r"[-+]?(?:\d*\.\d+|\d+)")


def _normalize_priority(priority: str) -> str:
    value = (priority or "").strip().lower()
    if value in {"low", "medium", "high"}:
        return value
    return "medium"


@mcp.tool()
def ping(note: str = "") -> str:
    """Return a simple pong response."""
    return f"pong:{note}" if note else "pong"


@mcp.tool()
def echo(message: str) -> str:
    """Echo the provided message."""
    return message


@mcp.tool()
def reverse(message: str) -> str:
    """Reverse the provided text."""
    return message[::-1]


@mcp.tool()
def add(a: float, b: float) -> str:
    """Add two numeric values."""
    result = a + b
    return f"{result:g}"


@mcp.tool()
def multiply(a: float, b: float) -> str:
    """Multiply two numeric values."""
    result = a * b
    return f"{result:g}"


@mcp.tool()
def upper(message: str) -> str:
    """Uppercase the provided message."""
    return message.upper()


@mcp.tool()
def lower(message: str) -> str:
    """Lowercase the provided message."""
    return message.lower()


@mcp.tool()
def slugify(message: str) -> str:
    """Convert the provided message into a URL slug."""
    slug = _NON_SLUG.sub("-", (message or "").strip().lower()).strip("-")
    return slug


@mcp.tool()
def word_count(message: str) -> str:
    """Count words in the provided message."""
    return str(len((message or "").split()))


@mcp.tool()
def extract_keywords(message: str, limit: int = 5) -> str:
    """Extract stable lowercase keywords from a short text sample."""
    limit = max(1, min(limit, 20))
    seen: list[str] = []
    for match in _WORDS.findall(message or ""):
        word = match.lower()
        if len(word) < 3 or word in seen:
            continue
        seen.append(word)
        if len(seen) >= limit:
            break
    return ", ".join(seen)


@mcp.tool()
def summarize_numbers(values: str) -> str:
    """Summarize numbers found in comma, space, or prose separated text."""
    count = 0
    total = 0.0
    minimum = 0.0
    maximum = 0.0
    for match in _NUMBERS.findall(values or ""):
        number = float(match)
        if count == 0:
            minimum = number
            maximum = number
        else:
            minimum = min(minimum, number)
            maximum = max(maximum, number)
        total += number
        count += 1
    if count == 0:
        return "count: 0"
    average = total / count
    return (
        f"count: {count}\n"
        f"min: {minimum:g}\n"
        f"max: {maximum:g}\n"
        f"sum: {total:g}\n"
        f"average: {average:g}"
    )


@mcp.tool()
def now() -> str:
    """Return the current UTC timestamp in RFC 3339 form."""
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


@mcp.tool()
def create_task(title: str, priority: str = "medium", owner: str = "") -> str:
    """Create a deterministic task summary for IDE and adapter smoke tests."""
    title = (title or "").strip() or "Untitled task"
    owner = (owner or "").strip() or "unassigned"
    return (
        f"task: {title}\n"
        f"priority: {_normalize_priority(priority)}\n"
        f"owner: {owner}\n"
        "status: open"
    )


@mcp.resource("embedded:readme")
def readme_resource() -> str:
    """Data utility overview and supported workflow notes."""
    return (
        "Data utility MCP exposes text cleanup, keyword extraction, simple "
        "numeric summaries, timestamps, task records, and prompt templates."
    )


@mcp.resource("embedded:task-guide")
def task_guide_resource() -> str:
    """Task workflow guidance for data utility smoke tests."""
    return (
        "Use create_task with title, priority (low|medium|high), and owner to "
        "produce a deterministic task record for adapter smoke tests."
    )


@mcp.resource("embedded:data-cleaning-checklist")
def data_cleaning_checklist_resource() -> str:
    """Checklist for lightweight data-cleaning demos."""
    return (
        "Checklist: trim whitespace, normalize case, extract keywords, "
        "summarize numeric ranges, record assumptions, and note follow-up checks."
    )


@mcp.prompt()
def hello() -> str:
    """Return a simple greeting prompt."""
    return "Hello from the Data utility MCP server."


@mcp.prompt()
def summarize(text: str) -> str:
    """Summarize a short text input."""
    text = (text or "").strip() or "No text provided."
    return f"Summarize this briefly: {text}"


@mcp.prompt()
def task_brief(goal: str) -> str:
    """Draft a concise task brief from a goal."""
    goal = (goal or "").strip() or "No goal provided."
    return f"Turn this goal into a concise task brief with acceptance criteria: {goal}"


@mcp.prompt()
def data_quality_review(dataset: str, concern: str = "") -> str:
    """Draft a concise data quality review prompt."""
    dataset = (dataset or "").strip() or "the dataset"
    concern = (concern or "").strip() or "freshness, completeness, and outliers"
    return (
        f"Review {dataset} for {concern}. Call out assumptions, likely failure "
        "modes, and the next validation query to run."
    )


if __name__ == "__main__":
    mcp.settings.host = "0.0.0.0"
    mcp.settings.port = int(os.environ.get("PORT", "8088"))
    mcp.settings.streamable_http_path = os.environ.get("MCP_PATH", "/mcp")
    mcp.run(transport="streamable-http")
