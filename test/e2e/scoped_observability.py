"""Real platform-session scoped Grafana and isolated resource-history acceptance."""
import http.client
import http.cookiejar
import json
import math
import os
import re
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path


def ui_origin():
    # Browser Origin follows the configured public UI, independently of the
    # IPv4 loopback address used to reach the disposable port-forward.
    return os.environ.get("SCOPED_UI_ORIGIN", os.environ["SCOPED_UI_BASE"]).rstrip("/")


def query_has_series(status, body):
    if status != 200:
        raise AssertionError(f"scoped Grafana query failed with HTTP {status}")
    result = json.loads(body)
    for ref, value in result.get("results", {}).items():
        if value.get("error") or value.get("status", 200) >= 400:
            # Do not dump backend responses, which can contain query payloads.
            raise AssertionError(f"scoped Grafana datasource query {ref} failed")
    return frames_present(body)


def mcp_origin():
    # Adapter-certificate IngressRoutes listen on Traefik websecure. The UI
    # stays on the plain HTTP forward, so MCP must use SCOPED_MCP_BASE when
    # the scenario points it at the TLS port.
    configured = os.environ.get("SCOPED_MCP_BASE", "").strip()
    if configured:
        return configured.rstrip("/")
    return os.environ["SCOPED_UI_BASE"].rstrip("/")


def mcp_opener():
    origin = urllib.parse.urlsplit(mcp_origin())
    # Kind Traefik's default certificate SAN is not 127.0.0.1. The rest of the
    # E2E harness uses the same loopback exception (qa-e2e.sh --tls-insecure-skip-verify).
    if origin.scheme == "https" and origin.hostname in {"127.0.0.1", "localhost"}:
        return urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl._create_unverified_context()))
    return urllib.request.build_opener()


def check(condition, message):
    if not condition:
        raise AssertionError(message)
    print("[pass] " + message, flush=True)


# A dropped connection (the Traefik port-forward being recycled right after a
# server deploy, for example) is retried a few times. Each retry is logged so a
# real crash that closes every connection still fails the run and shows up.
CONNECTION_ATTEMPTS = 4
CONNECTION_BACKOFF_SECONDS = 2
CONNECTION_ERRORS = (http.client.RemoteDisconnected, ConnectionResetError, ConnectionRefusedError, BrokenPipeError)


def request(client, url, method="GET", body=None, headers=None, retry_connection=None):
    headers = dict(headers or {})
    if body is not None:
        body = json.dumps(body).encode()
        headers.setdefault("Content-Type", "application/json")
    # Retrying could double-submit a write, so only safe methods retry unless the
    # caller says the request is idempotent (the login call does).
    retry = method in ("GET", "HEAD") if retry_connection is None else retry_connection
    attempts = CONNECTION_ATTEMPTS if retry else 1
    for attempt in range(1, attempts + 1):
        req = urllib.request.Request(url, data=body, method=method, headers=headers)
        try:
            with client.open(req, timeout=15) as response:
                return response.status, response.headers, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.headers, error.read()
        except (*CONNECTION_ERRORS, urllib.error.URLError) as error:
            # urllib wraps a refused or reset connection as URLError(reason=...), so
            # look at the reason too. Other URLErrors (DNS, TLS) are not retried.
            reason = getattr(error, "reason", error)
            if not isinstance(error, CONNECTION_ERRORS) and not isinstance(reason, CONNECTION_ERRORS):
                raise
            if attempt == attempts:
                raise
            print(f"[retry] {method} {urllib.parse.urlsplit(url).path} dropped ({type(error).__name__}), attempt {attempt}/{attempts}", file=sys.stderr, flush=True)
            time.sleep(CONNECTION_BACKOFF_SECONDS)


def session(base, email=None, key=None):
    cookies = http.cookiejar.CookieJar()
    client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cookies))
    body = {"api_key": key} if key else {"email": email, "password": os.environ["SCOPED_PASSWORD"]}
    status, _, response_body = request(client, base + "/auth/login", "POST", body, retry_connection=True)
    check(status == 200, "platform session login succeeds")
    client.scoped_csrf_token = json.loads(response_body)["csrf_token"]
    return client, cookies


def frame_numbers(body):
    result = json.loads(body)
    numbers = []
    for value in result.get("results", {}).values():
        for frame in value.get("frames", []):
            for field, column in zip(frame.get("schema", {}).get("fields", []), frame.get("data", {}).get("values", [])):
                if field.get("type") == "number":
                    numbers.extend(number for number in column if isinstance(number, (int, float)) and not isinstance(number, bool) and math.isfinite(number))
    return numbers


def frames_present(body):
    return bool(frame_numbers(body))


def query(client, expr, forged=False):
    base = os.environ["SCOPED_UI_BASE"]
    now = int(time.time() * 1000)
    headers = {"Origin": ui_origin()}
    if forged:
        headers.update({"X-WEBAUTH-USER": "forged-admin", "X-WEBAUTH-ROLE": "Admin", "X-Grafana-Org-Id": "2"})
    return request(client, base + "/grafana/api/ds/query", "POST", {
        "from": str(now - 3600000), "to": str(now), "queries": [{"refId": "A", "expr": expr,
        "datasource": {"uid": "prometheus", "type": "prometheus"}, "range": True, "intervalMs": 15000, "maxDataPoints": 2000}]}, headers)


def functional():
    base = os.environ["SCOPED_UI_BASE"]
    target = json.loads(Path(os.environ["SCOPED_TARGET"]).read_text())
    namespace, server, uid = (target["metadata"][key] for key in ("namespace", "name", "uid"))
    owner, cookies = session(base, os.environ["SCOPED_OWNER"])
    member, _ = session(base, os.environ["SCOPED_MEMBER"])
    foreign, _ = session(base, os.environ["SCOPED_FOREIGN"])
    admin, _ = session(base, key=os.environ["SCOPED_API_KEY"])
    anonymous = urllib.request.build_opener()
    status, _, _ = request(anonymous, base + "/grafana/api/health")
    check(status == 401, "Grafana requires the platform session")
    params = urllib.parse.urlencode({"namespace": namespace, "server": server})
    status, _, body = request(owner, base + "/api/ui/v1/runtime/observability/links?" + params)
    check(status == 200, "owner receives current server observability links")
    links = json.loads(body)
    expression = next(query["query"] for query in links["prometheus"]["queries"] if query["id"] == "request_rate")
    check(uid in expression and links["grafana"]["available"], "links include current UID and scoped Grafana")
    dashboard = links["grafana"]["url"]
    status, headers, body = request(owner, urllib.parse.urljoin(base, dashboard))
    check(status == 200 and b"grafanaBootData" in body and headers.get("Content-Security-Policy"), "native Grafana dashboard boot is protected by its nonce CSP")
    check(not any(cookie.name == "grafana_session" for cookie in cookies), "platform session does not issue a Grafana login cookie")

    # A bounded functional conversation creates real metrics; this is not a
    # staging load test or a throughput/latency benchmark.
    protocol = os.environ["SCOPED_PROTOCOL"]
    mcp_headers = {"Accept": "application/json, text/event-stream", "Mcp-Protocol-Version": protocol}
    endpoint = mcp_origin() + "/" + server + "/mcp"
    mcp = mcp_opener()
    deadline = time.monotonic() + 90
    while True:
        try:
            status, response_headers, _ = request(mcp, endpoint, "POST", {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": protocol, "capabilities": {}, "clientInfo": {"name": "scoped-history-qa", "version": "1"}}}, mcp_headers)
        except urllib.error.URLError as error:
            reason = getattr(error, "reason", error)
            if time.monotonic() >= deadline or not isinstance(reason, CONNECTION_ERRORS):
                raise
            print("[retry] POST MCP initialize dropped (%s)" % type(reason).__name__, file=sys.stderr, flush=True)
            time.sleep(2)
            continue
        if status == 200 or time.monotonic() >= deadline:
            break
        time.sleep(2)
    check(status == 200, "tenant fixture MCP initialize succeeds (status %s)" % status)
    if response_headers.get("Mcp-Session-Id"):
        mcp_headers["Mcp-Session-Id"] = response_headers["Mcp-Session-Id"]
    status, _, _ = request(mcp, endpoint, "POST", {"jsonrpc": "2.0", "method": "notifications/initialized"}, mcp_headers)
    check(status in (200, 202), "tenant fixture MCP initialized notification succeeds")
    for index in range(5):
        status, _, result = request(mcp, endpoint, "POST", {"jsonrpc": "2.0", "id": index + 2, "method": "tools/call", "params": {"name": "aaa-ping", "arguments": {}}}, mcp_headers)
        check(status == 200 and b"pong" in result, "tenant fixture tools/call succeeds")
    deadline = time.monotonic() + 180
    while True:
        status, _, body = query(owner, expression)
        if query_has_series(status, body):
            break
        if time.monotonic() >= deadline:
            raise AssertionError("scoped Grafana did not receive real gateway series")
        time.sleep(3)
    check(True, "owner Grafana query returns actual current-UID series")
    check(query(member, expression)[0] == 200, "same-team member can query the server")
    check(query(foreign, expression, forged=True)[0] == 403, "foreign team remains denied with forged Grafana identity")
    check(query(owner, "up")[0] == 403, "tenant arbitrary infrastructure PromQL is denied")
    check(query(admin, "up")[0] == 200, "admin retains unfiltered Grafana queries")
    check(request(owner, base + "/grafana/explore")[0] == 200, "bounded Explore shell uses the same platform session")
    for suffix in ("/api/datasources/proxy/uid/prometheus/api/v1/query?query=up", "/api/datasources/uid/prometheus/resources/api/v1/query?query=up", "/api/plugins/prometheus/resources/api/v1/query?query=up", "/api/tsdb/query", "/api/live/ws", "/api/dashboards/uid/scrape-coverage"):
        check(request(owner, base + "/grafana" + suffix)[0] == 403, "tenant alternate/global Grafana route is denied")
    check(request(owner, base + "/internal/observability/resources/metrics")[0] in (401, 404), "tenant cannot call the infrastructure collector directly")
    queries = {"collection_success": "mcp_resource_collection_success", "scrape_source": 'up{job="mcp-resource-history"}', "node_cpu": "mcp_resource_node_cpu_cores",
               "container_memory": "mcp_resource_container_working_set_bytes", "node_filesystem": "mcp_resource_node_filesystem_used_bytes",
               "node_network": "mcp_resource_node_network_receive_bytes_total", "requests": "mcp_resource_pod_cpu_requests_cores",
               "pvc_reservations": "mcp_resource_pvc_requested_bytes", "volume_used": "mcp_resource_volume_used_bytes",
               "collection_overhead": "mcp_resource_collection_duration_seconds"}
    resource = {}
    for name, expr in queries.items():
        status, _, body = query(admin, expr)
        resource[name] = {"status": status, "series_present": status == 200 and frames_present(body)}
        if name != "volume_used":
            check(resource[name]["series_present"], "resource history has " + name + " series")
        if name in ("collection_success", "scrape_source"):
            # Historical successes alone cannot hide a currently failing source.
            instant_status, _, instant_body = request(admin, base + "/grafana/api/ds/query", "POST", {
                "from": str(int(time.time() * 1000) - 60000), "to": str(int(time.time() * 1000)),
                "queries": [{"refId": "A", "expr": expr, "datasource": {"uid": "prometheus", "type": "prometheus"}, "instant": True}]}, {"Origin": ui_origin()})
            check(instant_status == 200 and frame_numbers(instant_body) and all(number == 1 for number in frame_numbers(instant_body)), "current authenticated " + name + " is successful")
    check(request(owner, base + "/auth/logout", "POST", {}, {"Origin": ui_origin(), "X-CSRF-Token": owner.scoped_csrf_token})[0] == 200, "platform session logout succeeds")
    check(request(owner, base + "/grafana/api/health")[0] == 401, "platform logout revokes Grafana access")
    Path(os.environ["SCOPED_REPORT"]).write_text(json.dumps({"functional": "passed", "resources": resource,
        "notes": ["Absent driver volume history remains missing; reservations are not consumption.", "See grafana.json for same-window kubelet comparisons and overhead."]}, indent=2) + "\n")


def network():
    kube = ["kubectl", "--kubeconfig", os.environ["SCOPED_KUBECONFIG"]]
    namespace, pod = os.environ["SCOPED_NAMESPACE"], os.environ["SCOPED_PROBE"]
    def get(*args):
        return json.loads(subprocess.check_output(kube + ["get", *args, "-o", "json"], text=True))

    probe = get("pod", pod, "-n", namespace)
    check(probe["status"]["phase"] == "Running" and probe["spec"].get("automountServiceAccountToken") is False, "network evidence uses the current uncredentialed tenant probe")
    agents = get("daemonset", "cilium", "-n", "kube-system")
    config = get("configmap", "cilium-config", "-n", "kube-system")
    check(agents["status"]["observedGeneration"] >= agents["metadata"]["generation"] and agents["status"]["numberReady"] == agents["status"]["desiredNumberScheduled"] > 0,
          "current Cilium generation is fully ready")
    check(config["data"].get("enable-policy") == "default" and config["data"].get("enable-k8s-networkpolicy") == "true", "running Cilium config explicitly enables Kubernetes policy")
    policy = get("networkpolicy", "mcp-scoped-observability-ingress", "-n", "mcp-observability")
    canonical = json.loads(subprocess.check_output(kube + ["create", "--dry-run=client", "-f", str(Path(__file__).resolve().parents[2] / "k8s/23-scoped-observability-networkpolicy.yaml"), "-o", "json"], text=True))["items"][0]
    check(policy["spec"] == canonical["spec"], "live telemetry rule exactly matches the release security boundary")
    services = {name: get("svc", name, "-n", "mcp-observability")["spec"]["clusterIP"]
                for name in ("grafana", "prometheus", "loki", "tempo")}
    ui = json.loads(subprocess.check_output(kube + ["get", "svc", "mcp-ui", "-n", "mcp-platform", "-o", "json"], text=True))["spec"]["clusterIP"]
    command = kube + ["exec", "-n", namespace, pod, "--", "wget", "-T", "3", "-S", "-O", "/dev/null", "--header=X-WEBAUTH-USER: forged-admin", "--header=X-WEBAUTH-ROLE: Admin"]
    control = subprocess.run(command + [f"http://{ui}:8082/health"], capture_output=True, text=True, timeout=10)
    check(control.returncode == 0 and "200" in control.stderr, "tenant probe can reach a healthy allowed UI peer")
    denied = []
    for name, port, path in (("grafana", 3000, "/grafana/api/health"), ("prometheus", 9090, "/prometheus/api/v1/query?query=up"), ("loki", 3100, "/ready"), ("tempo", 3200, "/ready")):
        pods = get("pods", "-n", "mcp-observability", "-l", "app=" + name)["items"]
        addresses = [services[name]] + [item["status"]["podIP"] for item in pods if item["status"].get("phase") == "Running"]
        check(len(addresses) > 1, "backend has a running direct endpoint: " + name)
        for address in addresses:
            result = subprocess.run(command + [f"http://{address}:{port}{path}"], capture_output=True, text=True, timeout=10)
            check(result.returncode != 0 and not re.search(r"HTTP/\d", result.stdout + result.stderr), "Cilium blocks tenant service/direct-pod " + name + " including forged auth-proxy identity")
        denied.append(name)
    evidence = {"probe_uid": probe["metadata"]["uid"], "probe_namespace": namespace, "cilium_generation": agents["metadata"]["generation"], "policy_uid": policy["metadata"]["uid"], "policy_resource_version": policy["metadata"]["resourceVersion"], "denied_backends": denied, "allowed_control": "UI health"}
    Path(os.environ["SCOPED_REPORT"]).with_name("scoped-network-evidence.json").write_text(json.dumps(evidence, indent=2) + "\n")
    path = Path(os.environ["SCOPED_REPORT"])
    report = json.loads(path.read_text())
    report["network_policy"] = "passed"
    path.write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    network() if "--network" in sys.argv else functional()
