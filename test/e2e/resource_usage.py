"""Read-only Kind E2E resource samples; never fall back to an ambient context."""
import argparse
import csv
import json
import math
import os
import platform
import re
import shutil
import signal
import subprocess
import threading
import time
from pathlib import Path

STOP = threading.Event()


def run(command, timeout=5):
    return subprocess.check_output(command, text=True, stderr=subprocess.DEVNULL, timeout=timeout)


def quantity(value):
    match = re.fullmatch(r"([0-9.]+)([a-zA-Z]*)", str(value or 0))
    if not match:
        raise ValueError("unsupported resource quantity")
    number, unit = match.groups()
    return float(number) * {"": 1, "m": .001, "u": 1e-6, "n": 1e-9,
                           "Ki": 1024, "Mi": 1024**2, "Gi": 1024**3,
                           "Ti": 1024**4, "k": 1000, "M": 1e6, "G": 1e9}[unit]


def pod_requests(pod, resource):
    spec = pod["spec"]
    def request(container):
        return quantity(container.get("resources", {}).get("requests", {}).get(resource, 0))
    sidecars, init_peak = 0, 0
    for container in spec.get("initContainers", []):
        if container.get("restartPolicy") == "Always":
            sidecars += request(container)
            init_peak = max(init_peak, sidecars)
        else:
            init_peak = max(init_peak, sidecars + request(container))
    regular = sum(request(c) for c in spec["containers"]) + sidecars
    return max(regular, init_peak) + quantity(spec.get("overhead", {}).get(resource, 0))


def host_sample():
    disk = shutil.disk_usage(Path.cwd())
    result = {"os": platform.system(), "os_version": platform.release(),
              "architecture": platform.machine(), "logical_cpus": os.cpu_count(),
              "load_average": list(os.getloadavg()),
              "filesystem_total_bytes": disk.total, "filesystem_used_bytes": disk.used}
    if platform.system() == "Darwin":
        result["physical_memory_bytes"] = int(run(["sysctl", "-n", "hw.memsize"]).strip())
        vm = run(["vm_stat"])
        page_size = int(re.search(r"page size of (\d+) bytes", vm)[1])
        pages = dict(re.findall(r"^([^:]+):\s+(\d+)\.", vm, re.M))
        result["free_memory_bytes"] = int(pages.get("Pages free", 0)) * page_size
        result["wired_memory_bytes"] = int(pages.get("Pages wired down", 0)) * page_size
        result["compressor_memory_bytes"] = int(pages.get("Pages occupied by compressor", 0)) * page_size
        swap = run(["sysctl", "-n", "vm.swapusage"])
        match = re.search(r"used = ([0-9.]+)M", swap)
        if match:
            result["swap_used_bytes"] = float(match[1]) * 1024**2
        cpu = run(["top", "-l", "2", "-s", "1", "-n", "0"], timeout=5)
        idle = re.findall(r"CPU usage:.*?([0-9.]+)% idle", cpu)
        if idle:
            result["cpu_used_cores"] = (100 - float(idle[-1])) / 100 * os.cpu_count()
    elif Path("/proc/meminfo").exists():
        values = dict(re.findall(r"^(\w+):\s+(\d+) kB", Path("/proc/meminfo").read_text(), re.M))
        result["physical_memory_bytes"] = int(values["MemTotal"]) * 1024
        result["available_memory_bytes"] = int(values["MemAvailable"]) * 1024
        result["swap_used_bytes"] = (int(values["SwapTotal"]) - int(values["SwapFree"])) * 1024
        result["cpu_counters"] = [int(v) for v in Path("/proc/stat").read_text().splitlines()[0].split()[1:9]]
    # The sparse VM disk's allocated blocks differ from its logical capacity.
    colima_disk = Path.home() / ".colima/_lima/colima/diffdisk"
    if colima_disk.exists():
        stat = colima_disk.stat()
        result["colima_disk_logical_bytes"] = stat.st_size
        result["colima_disk_allocated_bytes"] = stat.st_blocks * 512
    return result


class Sampler:
    def __init__(self, kubeconfig, cluster):
        self.kube = ["kubectl", "--kubeconfig", str(kubeconfig), "--request-timeout=4s"]
        self.cluster = cluster
        self.previous_cpu = None
        self.last_disk = 0

    def sample(self, stage):
        result = {"time": time.time(), "stage_last_started": stage, "errors": []}
        def capture(name, action):
            try:
                result[name] = action()
            except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
                result["errors"].append({"source": name, "error": type(error).__name__})
        capture("host", host_sample)
        counters = result.get("host", {}).get("cpu_counters")
        if counters:
            if self.previous_cpu:
                delta = [now - before for now, before in zip(counters, self.previous_cpu)]
                if sum(delta) > 0:
                    result["host"]["cpu_used_cores"] = (1 - (delta[3] + delta[4]) / sum(delta)) * os.cpu_count()
            self.previous_cpu = counters
        containers = []
        try:
            containers = run(["docker", "ps", "--filter", f"label=io.x-k8s.kind.cluster={self.cluster}",
                              "--format", "{{.Names}}"], timeout=4).splitlines()
        except (OSError, subprocess.SubprocessError) as error:
            result["errors"].append({"source": "docker", "error": type(error).__name__})
        if containers:
            capture("docker", lambda: [json.loads(line) for line in run(
                ["docker", "stats", "--no-stream", "--format", "{{json .}}", *containers], timeout=5).splitlines()])
        try:
            context = run(self.kube + ["config", "current-context"], timeout=4).strip()
            if context != "kind-" + self.cluster:
                raise ValueError("resource capture requires the named Kind context")
            nodes = json.loads(run(self.kube + ["get", "nodes", "-o", "json"], timeout=5))["items"]
            pods = json.loads(run(self.kube + ["get", "pods", "-A", "-o", "json"], timeout=5))["items"]
            active = [pod for pod in pods if pod["status"].get("phase") not in ("Succeeded", "Failed")]
            result["requests"] = {"cpu_cores": sum(pod_requests(p, "cpu") for p in active),
                                  "memory_bytes": sum(pod_requests(p, "memory") for p in active),
                                  "active_pods": len(active),
                                  "managed_mcp_pods": sum(p["metadata"].get("labels", {}).get("app.kubernetes.io/managed-by") == "mcp-runtime" for p in active)}
            result["nodes"] = []
            for node in nodes:
                name = node["metadata"]["name"]
                summary = json.loads(run(self.kube + ["get", "--raw", f"/api/v1/nodes/{name}/proxy/stats/summary"], timeout=5))
                result["nodes"].append({"name": name, "architecture": node["status"]["nodeInfo"]["architecture"],
                                        "allocatable": node["status"]["allocatable"], "usage": summary["node"],
                                        "pods": summary["pods"]})
            capture("pvc_reservations", lambda: [{"namespace": p["metadata"]["namespace"], "name": p["metadata"]["name"],
                    "capacity": p.get("status", {}).get("capacity", {}).get("storage"),
                    "requested": p["spec"].get("resources", {}).get("requests", {}).get("storage")}
                for p in json.loads(run(self.kube + ["get", "pvc", "-A", "-o", "json"], timeout=5))["items"]])
            if time.monotonic() - self.last_disk >= 60:
                self.last_disk = time.monotonic()
                def disks():
                    volumes = json.loads(run(self.kube + ["get", "pv", "-o", "json"], timeout=5))["items"]
                    paths = {}
                    for volume in volumes:
                        spec = volume["spec"]
                        claim = spec.get("claimRef", {})
                        path = spec.get("hostPath", spec.get("local", {})).get("path", "")
                        # Only inspect known local Kind data directories, not arbitrary mounts.
                        if claim and path.startswith("/var/") and "\n" not in path:
                            paths[path] = claim["namespace"] + "/" + claim["name"]
                    reports = []
                    for container in containers:
                        command = ["docker", "exec", container, "du", "-sk", "--", "/var/lib/containerd", *paths]
                        completed = subprocess.run(command, text=True, stdout=subprocess.PIPE,
                                                   stderr=subprocess.DEVNULL, timeout=8)
                        report = {"node": container, "data_volumes": {}, "partial": completed.returncode != 0}
                        for line in completed.stdout.splitlines():
                            size, path = line.split("\t", 1)
                            if path in paths:
                                report["data_volumes"][paths[path]] = int(size) * 1024
                            elif path == "/var/lib/containerd":
                                report["containerd_bytes"] = int(size) * 1024
                        reports.append(report)
                    return reports
                capture("node_disks", disks)
            # Metrics Server is optional; retain the kubelet source when absent.
            if any(p["metadata"].get("labels", {}).get("k8s-app") == "metrics-server" for p in active):
                capture("metrics_server", lambda: json.loads(run(self.kube + ["get", "--raw", "/apis/metrics.k8s.io/v1beta1/pods"], timeout=5)))
        except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
            result["errors"].append({"source": "kubernetes", "error": type(error).__name__})
        # These are VM-wide, including BuildKit, other clusters and image caches.
        try:
            if run(["docker", "context", "show"], timeout=3).strip() == "colima":
                capture("colima_memory", lambda: run(["colima", "ssh", "--", "free", "-b"], timeout=5))
        except (OSError, subprocess.SubprocessError):
            pass
        result["collection_seconds"] = time.time() - result["time"]
        return result


def summarize(samples, profile, scenarios, status):
    valid = [sample for sample in samples if sample.get("nodes")]
    def peak(field, getter, source=None):
        values = [getter(s) for s in (valid if source is None else source)]
        values = [v for v in values if v is not None and math.isfinite(v)]
        return {field: max(values) if values else None}
    summary = {"profile": profile, "scenarios": scenarios, "e2e_exit_status": status,
               "samples": len(samples), "kubernetes_samples": len(valid),
               "start": samples[0]["time"] if samples else None,
               "end": samples[-1]["time"] if samples else None,
               "error_samples": sum(bool(s.get("errors")) for s in samples),
               "notes": ["Sampled peaks can miss bursts between samples; E2E traffic is not a fixed-rate load benchmark.",
                         "Node usage includes Kubernetes infrastructure; Colima/host disk includes other clusters and caches. Host CPU includes other applications.",
                         "Stage labels identify the most recently started stage; parallel stages can overlap.",
                         "PVC capacity is a reservation, not bytes consumed. No hard minimum is inferred from idle usage."]}
    summary.update(peak("peak_node_cpu_cores", lambda s: sum(n["usage"].get("cpu", {}).get("usageNanoCores", 0) / 1e9 for n in s["nodes"])))
    summary.update(peak("peak_node_memory_bytes", lambda s: sum(n["usage"].get("memory", {}).get("workingSetBytes", 0) for n in s["nodes"])))
    summary.update(peak("peak_cpu_requests_cores", lambda s: s.get("requests", {}).get("cpu_cores")))
    summary.update(peak("peak_memory_requests_bytes", lambda s: s.get("requests", {}).get("memory_bytes")))
    summary.update(peak("peak_node_filesystem_used_bytes", lambda s: max(n["usage"].get("fs", {}).get("usedBytes", 0) for n in s["nodes"])))
    summary.update(peak("peak_host_cpu_cores", lambda s: s.get("host", {}).get("cpu_used_cores"), samples))
    summary.update(peak("peak_host_swap_bytes", lambda s: s.get("host", {}).get("swap_used_bytes"), samples))
    summary.update(peak("peak_host_filesystem_used_bytes", lambda s: s.get("host", {}).get("filesystem_used_bytes"), samples))
    summary.update(peak("peak_colima_allocated_disk_bytes", lambda s: s.get("host", {}).get("colima_disk_allocated_bytes"), samples))
    volumes = {}
    for sample in valid:
        for node in sample["nodes"]:
            for pod in node["pods"]:
                for volume in pod.get("volume", []):
                    if volume.get("pvcRef") and "usedBytes" in volume:
                        ref = volume["pvcRef"]
                        key = ref["namespace"] + "/" + ref["name"]
                        volumes[key] = max(volumes.get(key, 0), volume["usedBytes"])
    summary["volume_peak_used_bytes"] = volumes
    summary["disk_partial_samples"] = sum(any(n.get("partial") for n in s.get("node_disks", [])) for s in samples)
    for sample in valid:
        for node in sample.get("node_disks", []):
            for name, used in node["data_volumes"].items():
                volumes[name] = max(volumes.get(name, 0), used)
    summary.update(peak("peak_containerd_bytes", lambda s: sum(n.get("containerd_bytes", 0) for n in s.get("node_disks", [])) if s.get("node_disks") else None))
    def vm_used(sample):
        rows = re.findall(r"^Mem:\s+(\d+)\s+(\d+)", sample.get("colima_memory", ""), re.M)
        return int(rows[0][1]) if rows else None
    summary.update(peak("peak_colima_memory_used_bytes", vm_used, samples))
    summary["pvc_reservations"] = valid[-1].get("pvc_reservations", []) if valid else []
    summary["host"] = samples[-1].get("host", {}) if samples else {}
    summary["stages"] = {}
    for sample in valid:
        stage = summary["stages"].setdefault(sample.get("stage_last_started", "unknown"), {"samples": 0, "peak_node_memory_bytes": 0, "peak_node_cpu_cores": 0})
        stage["samples"] += 1
        stage["peak_node_memory_bytes"] = max(stage["peak_node_memory_bytes"], sum(n["usage"].get("memory", {}).get("workingSetBytes", 0) for n in sample["nodes"]))
        stage["peak_node_cpu_cores"] = max(stage["peak_node_cpu_cores"], sum(n["usage"].get("cpu", {}).get("usageNanoCores", 0) / 1e9 for n in sample["nodes"]))
    return summary


def write_summary(output, profile, scenarios, status):
    samples = []
    for line in (output / "samples.jsonl").read_text().splitlines():
        try:
            samples.append(json.loads(line))
        except json.JSONDecodeError:
            pass  # A stopped command may leave an incomplete final line.
    summary = summarize(samples, profile, scenarios, status)
    (output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    with (output / "samples.csv").open("w", newline="") as stream:
        writer = csv.writer(stream)
        writer.writerow(["epoch_seconds", "stage_last_started", "node_cpu_cores", "node_memory_bytes", "cpu_requests_cores", "memory_requests_bytes", "host_cpu_cores", "host_swap_bytes"])
        for sample in samples:
            nodes = sample.get("nodes", [])
            writer.writerow([sample["time"], sample.get("stage_last_started"),
                             sum(n["usage"].get("cpu", {}).get("usageNanoCores", 0) / 1e9 for n in nodes) if nodes else None,
                             sum(n["usage"].get("memory", {}).get("workingSetBytes", 0) for n in nodes) if nodes else None,
                             sample.get("requests", {}).get("cpu_cores"), sample.get("requests", {}).get("memory_bytes"),
                             sample.get("host", {}).get("cpu_used_cores"), sample.get("host", {}).get("swap_used_bytes")])
    lines = ["# Kind E2E resource observations", "", f"Profile: {profile}; scenarios: {scenarios}; exit status: {status}", "",
             "| Quantity | Sampled peak |", "|---|---|"]
    for key in ["peak_node_cpu_cores", "peak_node_memory_bytes", "peak_cpu_requests_cores", "peak_memory_requests_bytes", "peak_node_filesystem_used_bytes"]:
        value = summary[key]
        display = "unavailable" if value is None else (f"{value / 1024**3:.2f} GiB" if key.endswith("bytes") else f"{value:.2f} cores")
        lines.append(f"| {key} | {display} |")
    lines += ["", f"Valid Kubernetes samples: {len([s for s in samples if s.get('nodes')])}/{len(samples)}.", ""] + summary["notes"]
    (output / "summary.md").write_text("\n".join(lines) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", type=Path)
    parser.add_argument("--cluster", default="mcp-runtime")
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--stage-file", type=Path)
    parser.add_argument("--profile", default="single-replica")
    parser.add_argument("--scenarios", default="unspecified")
    parser.add_argument("--interval", type=float, default=15)
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--summarize", action="store_true")
    parser.add_argument("--exit-status", type=int)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    if args.summarize:
        write_summary(args.output, args.profile, args.scenarios, args.exit_status)
        return
    if not args.kubeconfig or args.interval < 5:
        parser.error("provide --kubeconfig and an interval of at least five seconds")
    signal.signal(signal.SIGTERM, lambda *_: STOP.set())
    signal.signal(signal.SIGINT, lambda *_: STOP.set())
    sampler = Sampler(args.kubeconfig, args.cluster)
    while not STOP.is_set():
        started = time.monotonic()
        stage = args.stage_file.read_text().strip() if args.stage_file and args.stage_file.exists() else "preparation"
        sample = sampler.sample(stage)
        with (args.output / "samples.jsonl").open("a") as stream:
            stream.write(json.dumps(sample) + "\n")
        if args.once:
            break
        STOP.wait(max(0, args.interval - (time.monotonic() - started)))
    write_summary(args.output, args.profile, args.scenarios, None)


if __name__ == "__main__":
    main()
