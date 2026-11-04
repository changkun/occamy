# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "numpy>=2.0",
#   "scipy>=1.13",
#   "matplotlib>=3.9",
# ]
# ///
"""Analyses the comparison benchmark's results into tables, tests and figures.

    uv run bench/report.py bench-essential.json -o docs/benchmarks/essential

reads the results the harness collected with -plan and writes into the
output directory:

- results.csv: one row per run, cell and metric;
- summary.csv: per series, cell and metric, the median of the runs and its
  95% interval;
- comparisons.csv: per comparison, cell and metric, the candidate's
  improvement over the baseline, its 95% interval and p-values;
- loss.csv: in a loss study, per series, cell and metric, what the loss
  added, its 95% interval and p-values;
- report.md: the tables, the targets and the figures;
- the figures, as SVG in light and dark.

The analysis was fixed before any data, in
specs/008-native-architecture/comparison-benchmark.md, Analysis.
"""

import argparse
import csv
import itertools
import json
import math
import os
import textwrap
from dataclasses import dataclass, field
from functools import cache
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib import ticker
import numpy as np
from scipy import stats

# The page that explains the scenarios, networks, metrics and analysis
# to a reader; each report links its sections.
DOCS = Path(__file__).resolve().parent.parent / "docs/benchmarks.md"


@dataclass(frozen=True)
class Metric:
    label: str
    unit: str
    lower: bool  # whether lower is better
    short: str = ""  # the label beside a cell's name, where the label is long

    def brief(self):
        return self.short or self.label


# Each metric as a reader names it. Latency is from a key, a click or a
# move to the first frame that shows its effect.
METRICS = {
    "latency_p95": Metric("Latency, 95th percentile", "ms", True, "latency p95"),
    "latency_p50": Metric("Latency, median", "ms", True, "latency p50"),
    "first_frame_p95": Metric("Time to the first frame, 95th percentile", "ms", True, "first frame p95"),
    "first_frame_p50": Metric("Time to the first frame, median", "ms", True, "first frame p50"),
    "completion": Metric("Time to complete", "ms", True, "time to complete"),
    "bytes_down": Metric("Data to the client", "KiB", True, "data"),
    "rate": Metric("Data to the client per second", "KiB/s", True, "data per second"),
    "fps": Metric("Frames shown a second", "frames/s", False, "frames a second"),
    "scene_missed": Metric("Scene frames never shown", "%", True),
    "frame_interval_p95": Metric("Gap between frames, 95th percentile", "ms", True, "frame gap p95"),
    "bytes_per_frame": Metric("Data per frame", "KiB", True),
    "cpu_per_frame": Metric("Server CPU per frame", "ms", True),
    "server_cpu": Metric("Server CPU time", "ms", True),
    "server_mem": Metric("Server memory, peak", "MiB", True),
    "client_cpu": Metric("Client CPU time", "ms", True),
    "occamyd_alloc": Metric("Memory occamyd allocated", "MiB", True),
    "occamyd_gc": Metric("occamyd's garbage collection cycles", "", True),
    "occamyd_heap": Metric("occamyd's heap in use", "MiB", True),
    "occamyd_goroutines": Metric("occamyd's goroutines", "", True),
}

# Per workload, its primary metric and its secondary ones, as the spec's
# Workloads table has them. What every result costs (COSTS) is secondary
# everywhere. A scene that stamps its frames also has the share of them
# never shown and its cadence (CADENCE), which
# specs/008-native-architecture/realtime-transport.md, step 1, adds
# beside the frame rate.
CADENCE = ["scene_missed", "frame_interval_p95"]
SCENARIOS = {
    "T1": ("latency_p95", ["latency_p50"]),
    "T2": ("completion", ["bytes_down", "server_cpu"]),
    "T3": ("rate", ["server_cpu"]),
    "T4": ("latency_p95", ["latency_p50", "rate"]),
    "T5": ("rate", ["server_cpu"]),
    "G1": ("rate", ["server_cpu"]),
    "G2": ("latency_p95", ["latency_p50"]),
    "G3": ("bytes_per_frame", ["cpu_per_frame", "fps", *CADENCE]),
    "G4": ("bytes_per_frame", ["cpu_per_frame", "fps", *CADENCE]),
    "G5": ("latency_p95", ["fps", *CADENCE, "bytes_per_frame", "cpu_per_frame"]),
    "G6": ("bytes_down", ["completion"]),
    "G7": ("first_frame_p95", ["first_frame_p50"]),
    "G8": ("latency_p95", ["latency_p50"]),
    "G9": ("latency_p95", ["bytes_down", "latency_p50"]),
    "G10": ("fps", ["latency_p95", *CADENCE, "bytes_per_frame", "cpu_per_frame"]),
    "G11": ("fps", ["latency_p95", *CADENCE, "bytes_per_frame", "cpu_per_frame"]),
}
# Each scenario by what it does, as the figures and tables name it, and
# as docs/benchmarks.md does beside its code; the CSV files keep the code.
SCENARIO_NAMES = {
    "T1": "Typing",
    "T2": "Long output",
    "T3": "top",
    "T4": "Paging in less",
    "T5": "Coloured log",
    "G1": "Still desktop",
    "G2": "Key press",
    "G3": "Scrolling text",
    "G4": "Dragging a window",
    "G5": "Key during motion",
    "G6": "Full-screen photo",
    "G7": "Connecting",
    "G8": "Click",
    "G9": "Drawing",
    "G10": "Video in a window",
    "G11": "Full-screen video",
    "G12": "Resizing",
}
PROTOCOL_NAMES = {"ssh": "SSH", "vnc": "VNC", "rdp": "RDP"}
COSTS = ["server_cpu", "server_mem", "client_cpu"]
RUNTIME = ["occamyd_alloc", "occamyd_gc", "occamyd_heap", "occamyd_goroutines"]

# The containers that serve each series: their CPU and memory are its
# server's. The first one named occamy-v1* is the occamyd whose runtime
# the series reports.
SERVERS = {
    "guacd": ["guacd"],
    "occamy": ["occamy", "guacd"],
    "v1-native": ["occamy-v1"],
    "v1-native-w2": ["occamy-v1"],
    "v1-native-cubic": ["occamy-v1-cubic"],
    "v1-native-bbr": ["occamy-v1-bbr"],
    "v1-native-nogfx": ["occamy-v1"],
    "v1-guacd": ["occamy-v1", "guacd"],
    "v1-guacd-gfx": ["occamy-v1-gfx", "guacd"],
}

# Each network by its round trip and bandwidth.
NETS = {
    "N0": "No delay",
    "N1": "20 ms, 100 Mbit/s",
    "N2": "50 ms, 20 Mbit/s",
    "N3": "100 ms, 5 Mbit/s",
    "N4": "250 ms, 1 Mbit/s",
}
RTT_MS = {"N0": 0, "N1": 20, "N2": 50, "N3": 100, "N4": 250}
PROTOCOLS = ["ssh", "vnc", "rdp"]

# The study that drops packets, in the kernel, each block under one loss,
# its factor (specs/008-native-architecture/realtime-transport.md, step
# 2). Its cells without loss are its control, and the targets judge none
# of its cells: they are set without loss.
LOSS = "loss"
# The metrics drawn against the loss: a key's latency, and a scene's rate
# and cadence.
LOSS_FIGURES = ["latency_p95", "fps", "frame_interval_p95"]


def loss_of(cell):
    """Returns the percentage of packets lost in a loss study's cell, from
    its factor, such as loss=0.5%."""
    return float(cell.factor.removeprefix("loss=").removesuffix("%"))


def factor_name(factor):
    """Names a cell's factor as a reader would: a loss study's loss=2% as
    2% lost."""
    if factor.startswith("loss="):
        return f"{factor.removeprefix('loss=')} lost"
    return factor


def loss_order(cell):
    """Orders a loss study's cells as Cell.order does, by the loss as a
    number rather than its factor's text."""
    return (*cell.order()[:-1], loss_of(cell))


@dataclass(frozen=True)
class Family:
    name: str
    baseline: str
    candidate: str
    correction: str  # "holm" or "bh", over the family's primary metrics
    # targets says whether the 10x and 50 ms targets judge the family: they
    # set Occamy against Guacamole, not one of Occamy's settings against
    # another.
    targets: bool = True
    # names, (series, name) pairs, names a series in this family where
    # SERIES_NAMES would mislead, as v1-native in results from before
    # occamyd asked for BBR, when it ran CUBIC.
    names: tuple = ()

    def has(self, c):
        """Says whether the comparison c is one of the family's: of its name
        and its two series, since two pairs may share a name."""
        return (c["family"], c["baseline"], c["candidate"]) == (self.name, self.baseline, self.candidate)


# The comparisons, each made where both of its series are in the results.
# The essential plan has one family, confirmatory; the full plan's (the
# spec's Analysis, Families) join it when it runs. The frame window
# compares the window of two frames before step 1 of
# specs/008-native-architecture/realtime-transport.md with the window
# from the path after it, and the congestion control CUBIC with BBR
# (step 3a), each on the same image in the same run. Step 3a ran before
# occamyd asked for BBR itself, with v1-native on the kernel's default,
# CUBIC, beside v1-native-bbr, whose network namespace took BBR; since,
# v1-native-cubic, whose occamyd asks for CUBIC, is the one beside
# v1-native.
FAMILIES = [
    Family("Guacamole to Occamy", "guacd", "v1-native", "holm"),
    Family("Frame window", "v1-native-w2", "v1-native", "holm", targets=False),
    Family("Congestion control", "v1-native-cubic", "v1-native", "holm", targets=False,
           names=(("v1-native", "Occamy with BBR"),)),
    Family("Congestion control", "v1-native", "v1-native-bbr", "holm", targets=False,
           names=(("v1-native", "Occamy with CUBIC"),)),
]

# Each series as a reader names it: Guacamole, the baseline, and Occamy
# with the setting a real-time step varies.
SERIES_NAMES = {
    "guacd": "Guacamole",
    "v1-native": "Occamy",
    "occamy": "Occamy 0.x",
    "v1-guacd": "Occamy through guacd",
    "v1-guacd-gfx": "Occamy through guacd, GFX",
    "v1-native-nogfx": "Occamy without GFX",
    "v1-native-w2": "Occamy with a window of two",
    "v1-native-cubic": "Occamy with CUBIC",
    "v1-native-bbr": "Occamy with BBR",
}


def net_entry(net):
    """Names a network in a table by its round trip and bandwidth."""
    return NETS.get(net, net)


def series_names(comparisons, families=FAMILIES):
    """Returns each series' name, as the families in comparisons name it."""
    names = dict(SERIES_NAMES)
    for fam in families:
        if any(fam.has(c) for c in comparisons):
            names.update(fam.names)
    return names

ALPHA = 0.05
TARGET_FACTOR = 10
TARGET_MS = 50

# One colour per series wherever it appears, in the validated palette's
# categorical order (light, dark). v1-native's other setting in a
# real-time step is violet, the window of two in step 1 and the other
# congestion control in step 3a, which never share a figure: red, the
# last slot, is too close to v1-native's orange to tell apart.
COLORS = {
    "guacd": ("#2a78d6", "#3987e5"),
    "v1-native": ("#eb6834", "#d95926"),
    "occamy": ("#1baf7a", "#199e70"),
    "v1-guacd": ("#eda100", "#c98500"),
    "v1-guacd-gfx": ("#e87ba4", "#d55181"),
    "v1-native-nogfx": ("#008300", "#008300"),
    "v1-native-w2": ("#6250d6", "#9085e9"),
    "v1-native-cubic": ("#6250d6", "#9085e9"),
    "v1-native-bbr": ("#6250d6", "#9085e9"),
}
THEMES = {
    "light": dict(surface="#fcfcfb", text="#0b0b0b", text2="#52514e", muted="#898781", grid="#e1e0d9", axis="#c3c2b7"),
    "dark": dict(surface="#1a1a19", text="#f0efec", text2="#c3c2b7", muted="#898781", grid="#2c2c2a", axis="#383835"),
}


# Reading.


def server_services(series):
    return SERVERS.get(series, [])


def metrics_of(r):
    """Returns the metrics of one result by name: the ones its workload
    reports, and what it cost."""
    m = {}
    if lat := r.get("latency_ms"):
        m["latency_p95"], m["latency_p50"] = lat["p95"], lat["p50"]
    if ff := r.get("first_frame_ms"):
        m["first_frame_p95"], m["first_frame_p50"] = ff["p95"], ff["p50"]
    if c := r.get("completion_ms"):
        m["completion"] = c
    secs, frames = r.get("seconds", 0), r.get("frames", 0)
    if secs > 0 and "bytes_down" in r:
        m["bytes_down"] = r["bytes_down"] / 1024
        m["rate"] = m["bytes_down"] / secs
        if frames > 0:
            m["fps"] = frames / secs
            m["bytes_per_frame"] = m["bytes_down"] / frames
    # A scene's frames are those it showed, each counted once by its stamp,
    # so the share it never showed is what the scene drew in the time less
    # that; its cadence is the time between the frames it showed.
    if (scene_fps := r.get("scene_fps", 0)) > 0 and secs > 0:
        m["scene_missed"] = 100 * (1 - frames / (scene_fps * secs))
    if fi := r.get("frame_interval_ms"):
        m["frame_interval_p95"] = fi["p95"]
    services = server_services(r["series"])
    if cpu := r.get("cpu_ms"):
        m["server_cpu"] = sum(cpu.get(s, 0) for s in services)
        if frames > 0:
            m["cpu_per_frame"] = m["server_cpu"] / frames
    if mem := r.get("mem_mib"):
        peaks = [mem[s]["peak"] for s in services if s in mem]
        if peaks:
            m["server_mem"] = sum(peaks)
    if c := r.get("client_cpu_ms"):
        m["client_cpu"] = c
    occamyd = next((s for s in services if s.startswith("occamy-v1")), None)
    if occamyd and (rt := r.get("runtime", {}).get(occamyd)):
        m["occamyd_alloc"] = rt["alloc_mib"]
        m["occamyd_gc"] = rt["gc_cycles"]
        m["occamyd_heap"] = rt["heap_inuse_mib"]
        m["occamyd_goroutines"] = rt["goroutines"]

    if r["scenario"] not in SCENARIOS:
        return m
    primary, secondary = SCENARIOS[r["scenario"]]
    keep = {primary, *secondary, *COSTS, *RUNTIME}
    return {k: v for k, v in m.items() if k in keep}


@dataclass(frozen=True)
class Cell:
    study: str
    factor: str
    scenario: str
    protocol: str
    net: str

    def label(self):
        s = f"{self.scenario} {self.protocol}"
        if self.factor:
            s += f" ({self.factor})"
        return s

    def title(self, sep=", ", factor=True, wrap=0):
        """Names the cell as a reader would: its scenario and protocol, and
        its factor, such as 2% lost; the scenario wrapped at wrap
        characters if wrap is set."""
        scenario = SCENARIO_NAMES.get(self.scenario, self.scenario)
        if wrap:
            scenario = textwrap.fill(scenario, wrap)
        s = f"{scenario}{sep}{PROTOCOL_NAMES.get(self.protocol, self.protocol)}"
        if factor and self.factor:
            s += f", {factor_name(self.factor)}"
        return s

    def order(self):
        sc = list(SCENARIOS).index(self.scenario) if self.scenario in SCENARIOS else len(SCENARIOS)
        pr = PROTOCOLS.index(self.protocol) if self.protocol in PROTOCOLS else len(PROTOCOLS)
        return (self.study, self.net, sc, self.scenario, pr, self.protocol, self.factor)


def read_rows(rep):
    """Returns the report's results as tidy rows, one per run, cell and
    metric, and the results that failed."""
    rows, failed = [], []
    for r in rep["results"]:
        if r.get("error"):
            failed.append(r)
            continue
        cell = Cell(r.get("study", ""), r.get("factor", ""), r["scenario"], r["protocol"], r["net"])
        for name, value in metrics_of(r).items():
            rows.append(
                dict(series=r["series"], cell=cell, run=r["run"], metric=name, value=float(value),
                     load=r.get("load_1m", 0.0), at=r.get("at", ""))
            )
    return rows, failed


def index(rows):
    """Groups the rows' values by series, cell and metric, in run order."""
    out = {}
    for row in sorted(rows, key=lambda x: x["run"]):
        out.setdefault((row["series"], row["cell"], row["metric"]), []).append(row["value"])
    return {k: np.array(v) for k, v in out.items()}


# Statistics.


def median_interval(x, level=0.95):
    """Returns the median of x, and the exact distribution-free interval for
    it from order statistics, [x(k), x(n+1-k)] with the largest k whose
    coverage is at least level, and that coverage. The interval is nan if
    no k reaches the level."""
    x = np.sort(np.asarray(x, float))
    n = len(x)
    if n == 0:
        return np.nan, np.nan, np.nan, np.nan
    k = 0
    while k + 1 <= n // 2 and 1 - 2 * stats.binom.cdf(k, n, 0.5) >= level:
        k += 1
    if k == 0:
        return float(np.median(x)), np.nan, np.nan, np.nan
    return float(np.median(x)), x[k - 1], x[n - k], 1 - 2 * stats.binom.cdf(k - 1, n, 0.5)


@cache
def splits(total, n):
    """Every way to choose which n of total pooled runs are the first
    series', as index rows."""
    return np.array(list(itertools.combinations(range(total), n)))


@cache
def mann_whitney_rank(n, m, alpha=ALPHA):
    """Returns k such that [d(k), d(nm+1-k)], of the nm ordered pairwise
    differences, is the exact 1-alpha interval for a shift between samples
    of n and m, from the Mann–Whitney distribution, and its coverage; k is
    0 when no interval reaches it."""
    u = splits(n + m, n).sum(axis=1) + n - n * (n + 1) // 2  # ranks from 1
    total = len(u)
    k = 0
    while k + 1 <= n * m // 2 and np.count_nonzero(u <= k) / total <= alpha / 2:
        k += 1
    if k == 0:
        return 0, np.nan
    return k, 1 - 2 * np.count_nonzero(u <= k - 1) / total


def rank_sum_p(a, b):
    """Returns the exact two-sided permutation p-value of the rank sum of a
    against b, over every split of the pooled runs, with midranks for
    ties."""
    n, total = len(a), len(a) + len(b)
    ranks = stats.rankdata(np.concatenate([a, b]))
    w = ranks[splits(total, n)].sum(axis=1)
    mean = n * (total + 1) / 2
    observed = abs(ranks[:n].sum() - mean)
    return float(np.mean(np.abs(w - mean) >= observed - 1e-9))


def factor(a, b, lower=True):
    """Returns the factor by which b is better than a, the Hodges–Lehmann
    estimate of the ratio a/b (b/a where higher is better), with its exact
    95% interval and the interval's coverage."""
    a, b = np.asarray(a, float), np.asarray(b, float)
    if len(a) == 0 or len(b) == 0 or (a <= 0).any() or (b <= 0).any():
        return np.nan, np.nan, np.nan, np.nan
    d = np.log(a)[:, None] - np.log(b)[None, :]
    d = np.sort((d if lower else -d).ravel())
    est = float(np.exp(np.median(d)))
    k, coverage = mann_whitney_rank(len(a), len(b))
    if k == 0:
        return est, np.nan, np.nan, np.nan
    return est, float(np.exp(d[k - 1])), float(np.exp(d[len(d) - k])), coverage


def shift(a, b):
    """Returns what b adds to a, the Hodges–Lehmann estimate of the shift
    b - a, with its exact 95% interval from the Mann–Whitney distribution
    and the interval's coverage."""
    a, b = np.asarray(a, float), np.asarray(b, float)
    if len(a) == 0 or len(b) == 0:
        return np.nan, np.nan, np.nan, np.nan
    d = np.sort((b[None, :] - a[:, None]).ravel())
    est = float(np.median(d))
    k, coverage = mann_whitney_rank(len(a), len(b))
    if k == 0:
        return est, np.nan, np.nan, np.nan
    return est, float(d[k - 1]), float(d[len(d) - k]), coverage


def holm(p):
    """Returns Holm's adjustment of p."""
    p = np.asarray(p, float)
    adj, running = np.empty(len(p)), 0.0
    for i, k in enumerate(np.argsort(p)):
        running = max(running, (len(p) - i) * p[k])
        adj[k] = min(1.0, running)
    return adj


def adjust(p, how):
    p = np.asarray(p, float)
    if len(p) == 0:
        return p
    return holm(p) if how == "holm" else stats.false_discovery_control(p, method="bh")


# Analysis.


def summarise(values):
    out = {}
    for (series, cell, metric), x in values.items():
        med, lo, hi, cov = median_interval(x)
        out[(series, cell, metric)] = dict(n=len(x), median=med, lo=lo, hi=hi, coverage=cov)
    return out


def compare(values, families=FAMILIES):
    """Returns the comparisons of each family, per cell and metric, with the
    family's correction over its primary metrics."""
    out = []
    cells = sorted({k[1] for k in values}, key=Cell.order)
    for fam in families:
        rows = []
        for cell in cells:
            primary = SCENARIOS.get(cell.scenario, (None, []))[0]
            for metric in METRICS:
                a = values.get((fam.baseline, cell, metric))
                b = values.get((fam.candidate, cell, metric))
                if a is None or b is None:
                    continue
                est, lo, hi, cov = factor(a, b, METRICS[metric].lower)
                p = rank_sum_p(a, b) if len(a) > 1 and len(b) > 1 else np.nan
                rows.append(dict(family=fam.name, baseline=fam.baseline, candidate=fam.candidate, cell=cell,
                                 metric=metric, primary=metric == primary, n_baseline=len(a), n_candidate=len(b),
                                 factor=est, lo=lo, hi=hi, coverage=cov, p=p, p_adjusted=np.nan,
                                 targets=fam.targets and cell.study != LOSS))
        tested = [r for r in rows if r["primary"] and np.isfinite(r["p"])]
        for r, q in zip(tested, adjust([r["p"] for r in tested], fam.correction)):
            r["p_adjusted"] = q
        out += rows
    return out


def compare_loss(values):
    """Returns, per series and cell of the loss study with loss, what the
    loss added to each metric over the same cell without it, with Holm's
    correction over the primary metrics."""
    rows = []
    for (series, cell, metric), b in values.items():
        if cell.study != LOSS or loss_of(cell) == 0:
            continue
        base = next((c for (s, c, m) in values if s == series and m == metric and c.study == LOSS
                     and loss_of(c) == 0 and (c.scenario, c.protocol, c.net) == (cell.scenario, cell.protocol, cell.net)),
                    None)
        if base is None:
            continue
        a = values[(series, base, metric)]
        est, lo, hi, cov = shift(a, b)
        p = rank_sum_p(a, b) if len(a) > 1 and len(b) > 1 else np.nan
        primary = SCENARIOS.get(cell.scenario, (None, []))[0]
        rows.append(dict(series=series, cell=cell, metric=metric, primary=metric == primary, n_baseline=len(a),
                         n_candidate=len(b), shift=est, lo=lo, hi=hi, coverage=cov, p=p, p_adjusted=np.nan))
    rows.sort(key=lambda r: (*loss_order(r["cell"]), r["series"], list(METRICS).index(r["metric"])))
    tested = [r for r in rows if r["primary"] and np.isfinite(r["p"])]
    for r, q in zip(tested, adjust([r["p"] for r in tested], "holm")):
        r["p_adjusted"] = q
    return rows


def significant(c):
    return np.isfinite(c["p_adjusted"]) and c["p_adjusted"] < ALPHA


def outcome(primary):
    """Says how many of the primary differences are significant after the
    correction, without the sentence's end."""
    return (f"{sum(significant(c) for c in primary)} of the {len(primary)} differences are significant after the"
            " correction")


def out_of_reach(primary):
    """Returns why no difference among primary could be significant after
    Holm's correction, when none could: an exact test's smallest p, two
    splits of all of them, times the number of tests reaches ALPHA."""
    least = min((2 / math.comb(c["n_baseline"] + c["n_candidate"], c["n_baseline"])
                 for c in primary if c["n_baseline"] and c["n_candidate"]), default=None)
    if least is None or len(primary) * least < ALPHA:
        return ""
    return (f" With this many runs, the exact test's smallest p is {fmt_p(least)}, so after the correction over"
            f" {len(primary)} tests no difference can be significant, and the intervals carry the evidence.")


def tenfold(c):
    """Reports whether a comparison of a family that the targets judge
    meets the 10x target."""
    return c.get("targets", True) and np.isfinite(c["lo"]) and c["lo"] >= TARGET_FACTOR


def latency_targets(summary, families=FAMILIES):
    """Returns, per candidate of a family that the targets judge and cell at
    N0 or N1 with a latency, whether the upper bound of its L p95 is at
    most TARGET_MS."""
    out, seen = [], set()
    for fam in families:
        if not fam.targets:
            continue
        for (series, cell, metric), s in summary.items():
            if (series == fam.candidate and metric == "latency_p95" and cell.net in ("N0", "N1")
                    and cell.study != LOSS and (series, cell) not in seen):
                seen.add((series, cell))
                out.append(dict(series=series, cell=cell, met=np.isfinite(s["hi"]) and s["hi"] <= TARGET_MS, **s))
    return sorted(out, key=lambda t: t["cell"].order())


def sensitivity(rows, gate, comparisons):
    """Returns the primary comparisons whose conclusion, significance or the
    tenfold target, changes without the results measured above the load
    gate, and how many results were."""
    if not gate:
        return None, []
    above = {(r["series"], r["cell"], r["run"]) for r in rows if r["load"] > gate}
    kept = [r for r in rows if (r["series"], r["cell"], r["run"]) not in above]

    def key(c):
        return c["family"], c["baseline"], c["candidate"], c["cell"], c["metric"]

    again = {key(c): c for c in compare(index(kept)) if c["primary"]}
    changed = []
    for c in comparisons:
        if not c["primary"]:
            continue
        d = again.get(key(c))
        if d is None or significant(d) != significant(c) or tenfold(d) != tenfold(c):
            changed.append(dict(before=c, after=d))
    return len(above), changed


# Output.


def fmt(x):
    if x is None or not np.isfinite(x):
        return "–"
    a = abs(x)
    if a >= 100:
        return f"{x:,.0f}"
    if a >= 10:
        return f"{x:.1f}"
    if a >= 1:
        return f"{x:.2f}"
    return f"{x:.3g}"


def fmt_p(p):
    if not np.isfinite(p):
        return "–"
    return "<0.0001" if p < 0.0001 else f"{p:.4f}"


def fmt_interval(v, lo, hi, suffix=""):
    if not np.isfinite(lo):
        return f"{fmt(v)}{suffix}"
    return f"{fmt(v)}{suffix} [{fmt(lo)}, {fmt(hi)}]"


def write_csv(path, header, rows):
    with open(path, "w", newline="") as f:
        w = csv.writer(f, lineterminator="\n")
        w.writerow(header)
        w.writerows(rows)


def cell_columns(cell):
    return [cell.study, cell.factor, cell.scenario, cell.protocol, cell.net]


CELL_HEADER = ["study", "factor", "scenario", "protocol", "net"]


def write_tables(out, rows, summary, comparisons, losses=()):
    write_csv(out / "results.csv", ["series", *CELL_HEADER, "run", "metric", "value", "load_1m", "at"],
              [[r["series"], *cell_columns(r["cell"]), r["run"], r["metric"], r["value"], r["load"], r["at"]]
               for r in sorted(rows, key=lambda r: (r["cell"].order(), r["series"], r["metric"], r["run"]))])
    write_csv(out / "summary.csv", ["series", *CELL_HEADER, "metric", "unit", "n", "median", "lo", "hi", "coverage"],
              [[s, *cell_columns(c), m, METRICS[m].unit, v["n"], v["median"], v["lo"], v["hi"], v["coverage"]]
               for (s, c, m), v in sorted(summary.items(), key=lambda kv: (kv[0][1].order(), kv[0][0], kv[0][2]))])
    write_csv(out / "comparisons.csv",
              ["family", "baseline", "candidate", *CELL_HEADER, "metric", "primary", "n_baseline", "n_candidate",
               "improvement", "lo", "hi", "coverage", "p", "p_adjusted"],
              [[c["family"], c["baseline"], c["candidate"], *cell_columns(c["cell"]), c["metric"], c["primary"],
                c["n_baseline"], c["n_candidate"], c["factor"], c["lo"], c["hi"], c["coverage"], c["p"],
                c["p_adjusted"]] for c in comparisons])
    if losses:
        write_csv(out / "loss.csv",
                  ["series", *CELL_HEADER, "loss", "metric", "primary", "n_baseline", "n_candidate", "added", "lo",
                   "hi", "coverage", "p", "p_adjusted", "rtt_ms"],
                  [[c["series"], *cell_columns(c["cell"]), loss_of(c["cell"]), c["metric"], c["primary"],
                    c["n_baseline"], c["n_candidate"], c["shift"], c["lo"], c["hi"], c["coverage"], c["p"],
                    c["p_adjusted"], RTT_MS.get(c["cell"].net, np.nan)] for c in losses])


def style(theme):
    t = THEMES[theme]
    return {
        "figure.facecolor": t["surface"], "axes.facecolor": t["surface"], "savefig.facecolor": t["surface"],
        "axes.edgecolor": t["axis"], "axes.labelcolor": t["text2"], "text.color": t["text"],
        "xtick.color": t["axis"], "ytick.color": t["axis"],
        "xtick.labelcolor": t["text2"], "ytick.labelcolor": t["text2"],
        "axes.grid": True, "grid.color": t["grid"], "grid.linewidth": 0.8, "axes.axisbelow": True,
        "axes.spines.top": False, "axes.spines.right": False, "axes.spines.left": False,
        "legend.frameon": False, "font.size": 9, "axes.titlesize": 10, "axes.titlelocation": "left",
        "font.family": "sans-serif", "font.sans-serif": ["Inter", "Helvetica Neue", "Arial", "DejaVu Sans"],
        "svg.fonttype": "none", "svg.hashsalt": "occamy",
    }


def save(fig, path):
    """Saves fig in the format of path's extension, SVG without a date so
    that the same results draw the same file."""
    path = Path(path)
    fig.savefig(path, bbox_inches="tight", **({"metadata": {"Date": None}} if path.suffix == ".svg" else {}))
    plt.close(fig)


def plain_log_axis(axis, suffix=""):
    """Labels a log axis with plain numbers at 1, 2 and 5 of each decade."""
    axis.set_major_locator(ticker.LogLocator(subs=(1, 2, 5)))
    axis.set_major_formatter(ticker.FuncFormatter(lambda v, _: f"{v:g}{suffix}"))
    axis.set_minor_formatter(ticker.NullFormatter())


def bar_figure(path, metric, summary, series, names, theme):
    """Draws, per network, each cell's median of metric per series as a bar
    with its 95% interval."""
    t, dark = THEMES[theme], theme == "dark"
    cells = sorted({c for (s, c, m) in summary if m == metric and s in series}, key=Cell.order)
    nets = sorted({c.net for c in cells})
    labels = sorted({c.title("\n", wrap=10) for c in cells},
                    key=lambda l: next(c.order()[2:] for c in cells if c.title("\n", wrap=10) == l))
    fig, axes = plt.subplots(1, len(nets), figsize=(max(3.2, 0.95 * len(labels)) * len(nets) + 1.2, 3.8),
                             sharey=True, squeeze=False, layout="constrained")
    values = []
    for ax, net in zip(axes[0], nets):
        x = np.arange(len(labels))
        width = 0.8 / len(series)
        for i, s in enumerate(series):
            med, lo, hi = [], [], []
            for label in labels:
                v = next((summary[(s, c, metric)] for c in cells
                          if c.net == net and c.title("\n", wrap=10) == label and (s, c, metric) in summary), None)
                med.append(v["median"] if v else np.nan)
                lo.append(v["lo"] if v else np.nan)
                hi.append(v["hi"] if v else np.nan)
            med, lo, hi = map(np.array, (med, lo, hi))
            values += [v for v in np.concatenate([med, lo, hi]) if np.isfinite(v) and v > 0]
            pos = x + (i - (len(series) - 1) / 2) * width
            ax.bar(pos, med, width, color=COLORS.get(s, ("#898781",) * 2)[dark], edgecolor=t["surface"],
                   linewidth=1.5, label=names.get(s, s))
            ok = np.isfinite(lo)
            ax.errorbar(pos[ok], med[ok], yerr=[med[ok] - lo[ok], hi[ok] - med[ok]], fmt="none",
                        ecolor=t["text"], elinewidth=1, capsize=2.5)
        ax.set_xticks(x, labels)
        ax.set_title(NETS.get(net, net), color=t["text2"])
        ax.tick_params(axis="x", length=0)
        ax.grid(axis="x", visible=False)
    if values and max(values) / min(values) > 10:
        axes[0][0].set_yscale("log")
        plain_log_axis(axes[0][0].yaxis)
    unit = METRICS[metric].unit
    axes[0][0].set_ylabel(f"{METRICS[metric].label}" + (f" ({unit})" if unit else ""))
    fig.legend(*axes[0][0].get_legend_handles_labels(), loc="outside right upper")
    fig.suptitle(f"{METRICS[metric].label}: median of the runs, with 95% intervals", x=0.0, ha="left",
                 color=t["text"], fontsize=11)
    save(fig, path)


def factor_title(fam, names):
    """Names what a family compares: its candidate against its baseline."""
    return f"{names.get(fam.candidate, fam.candidate)} against {names.get(fam.baseline, fam.baseline)}"


def factor_figure(path, fam, comparisons, names, theme):
    """Draws the family's primary factors with their 95% intervals, a panel
    per network, on a log axis with a line at 1x, and at 10x where the
    targets judge it."""
    t, dark = THEMES[theme], theme == "dark"
    rows = [c for c in comparisons if fam.has(c) and c["primary"] and np.isfinite(c["factor"])]
    rows.sort(key=lambda c: c["cell"].order())
    nets = sorted({c["cell"].net for c in rows})
    counts = [sum(c["cell"].net == net for c in rows) for net in nets]
    fig, axes = plt.subplots(len(nets), 1, figsize=(6.4, 0.3 * len(rows) + 0.45 * len(nets) + 1.0),
                             sharex=True, squeeze=False, layout="constrained", gridspec_kw={"height_ratios": counts})
    color = COLORS.get(fam.candidate, ("#898781",) * 2)[dark]
    lines = ((1, "-"), (TARGET_FACTOR, "--")) if any(c["targets"] for c in rows) else ((1, "-"),)
    for i, (ax, net) in enumerate(zip(axes[:, 0], nets)):
        sub = [c for c in rows if c["cell"].net == net]
        y = np.arange(len(sub))[::-1]
        est = np.array([c["factor"] for c in sub])
        lo = np.array([c["lo"] for c in sub])
        hi = np.array([c["hi"] for c in sub])
        ok = np.isfinite(lo)
        ax.errorbar(est[ok], y[ok], xerr=[est[ok] - lo[ok], hi[ok] - est[ok]], fmt="none", ecolor=color,
                    elinewidth=2, capsize=0)
        ax.scatter(est, y, s=36, color=color, edgecolors=t["surface"], linewidths=1.5, zorder=3)
        for at, style_ in lines:
            ax.axvline(at, color=t["muted"], linewidth=1, linestyle=style_, zorder=1)
            if i == 0:
                ax.annotate(f"{at}×" + (", the target" if at == TARGET_FACTOR else ""), (at, 1.0),
                            xycoords=("data", "axes fraction"), xytext=(3, 2), textcoords="offset points",
                            color=t["text2"], fontsize=8)
        ax.set_yticks(y, [f"{c['cell'].title()} · {METRICS[c['metric']].brief()}" for c in sub])
        ax.set_ylim(-0.6, len(sub) - 0.4)
        # The first panel's title clears the 1x and 10x marks above it.
        ax.set_title(NETS.get(net, net), color=t["text2"], pad=16 if i == 0 else 6)
        ax.tick_params(axis="y", length=0)
        ax.grid(axis="y", visible=False)
    axes[0][0].set_xscale("log")
    plain_log_axis(axes[0][0].xaxis, "×")
    axes[-1][0].set_xlabel(f"Improvement of {names.get(fam.candidate, fam.candidate)} over"
                           f" {names.get(fam.baseline, fam.baseline)} (log scale)")
    fig.suptitle(f"{factor_title(fam, names)}: improvement on each primary metric, with 95% intervals", x=0.0,
                 ha="left",
                 color=t["text"], fontsize=11)
    save(fig, path)


def loss_figure(path, metric, summary, names, theme):
    """Draws, per cell of the loss study, each series' median of metric
    against the loss, with its 95% interval, and marks a loss where none
    of a series' runs succeeded at the top of the panel."""
    t, dark = THEMES[theme], theme == "dark"
    cells = [c for (_, c, m) in summary if m == metric and c.study == LOSS]
    panels = sorted({(c.net, c.scenario, c.protocol) for c in cells},
                    key=lambda k: next(c.order() for c in cells if (c.net, c.scenario, c.protocol) == k))
    levels = sorted({loss_of(c) for c in cells})
    series = [s for s in COLORS if any(k[0] == s and k[1].study == LOSS and k[2] == metric for k in summary)]
    cols = len({(sc, pr) for _, sc, pr in panels})
    rows = -(-len(panels) // cols)
    fig, axes = plt.subplots(rows, cols, figsize=(2.6 * cols + 1.2, 2.4 * rows + 0.6), squeeze=False,
                             layout="constrained")
    for ax in axes.flat[len(panels):]:
        ax.set_visible(False)
    x = np.arange(len(levels))
    for ax, (net, sc, pr) in zip(axes.flat, panels):
        failed = []
        for s in series:
            v = [summary.get((s, c, metric)) for level in levels
                 for c in [next((c for c in cells if (c.net, c.scenario, c.protocol) == (net, sc, pr)
                                 and loss_of(c) == level), None)] if c is not None]
            if len(v) != len(levels) or all(e is None for e in v):
                continue
            med = np.array([e["median"] if e else np.nan for e in v])
            lo = np.array([e["lo"] if e else np.nan for e in v])
            hi = np.array([e["hi"] if e else np.nan for e in v])
            color = COLORS[s][dark]
            failed += [(x[i], color) for i, e in enumerate(v) if e is None]
            ax.plot(x, med, color=color, linewidth=2, marker="o", markersize=6, markeredgecolor=t["surface"],
                    markeredgewidth=1.5, label=names.get(s, s), zorder=3)
            ok = np.isfinite(lo)
            ax.errorbar(x[ok], med[ok], yerr=[med[ok] - lo[ok], hi[ok] - med[ok]], fmt="none", ecolor=color,
                        elinewidth=1, capsize=2.5, zorder=2)
        ax.set_xticks(x, [f"{level:g}%" for level in levels])
        ax.set_title(f"{SCENARIO_NAMES.get(sc, sc)}, {PROTOCOL_NAMES.get(pr, pr)} · {NETS.get(net, net)}",
                     color=t["text2"], fontsize=9)
        ax.set_ylim(bottom=0)
        top = ax.get_ylim()[1]
        for at, color in failed:
            ax.plot([at], [top], marker="x", markersize=7, markeredgewidth=2, color=color, clip_on=False, zorder=3)
            ax.annotate("no run succeeded", (at, top), xytext=(0, -12), textcoords="offset points", ha="right" if at else "left",
                        color=t["text2"], fontsize=7)
        ax.grid(axis="x", visible=False)
    unit = METRICS[metric].unit
    for ax in axes[:, 0]:
        ax.set_ylabel(f"{METRICS[metric].label}" + (f" ({unit})" if unit else ""))
    for ax in axes[-1]:
        ax.set_xlabel("Packets to the client lost")
    # Centred, so that a narrow figure's title does not run into it.
    fig.legend(*axes[0][0].get_legend_handles_labels(), loc="outside right center")
    fig.suptitle(f"{METRICS[metric].label} against packets lost: median of the runs, with 95% intervals", x=0.0,
                 ha="left", color=t["text"], fontsize=11)
    save(fig, path)


def factors_name(fam):
    """Names the file of the family's figure of improvements, from the
    family's name, without the theme's suffix."""
    return "factors-" + fam.name.lower().replace(" ", "-")


def draw(out, summary, comparisons, families=FAMILIES):
    """Draws the figures in both themes and returns their names, without
    the theme's suffix, with the text that describes each."""
    series = [s for s in COLORS if any(k[0] == s for k in summary)]
    names = series_names(comparisons, families)
    figures = []
    for fam in families:
        rows = [c for c in comparisons if fam.has(c) and c["primary"]]
        if rows:
            name = factors_name(fam)
            figures.append((name, f"The improvement of {names.get(fam.candidate, fam.candidate)} over"
                                  f" {names.get(fam.baseline, fam.baseline)} on each scenario's primary metric,"
                                  " with its 95% interval, on a log axis, in a panel for each network"
                                  + (f"; a dashed line marks the target of {TARGET_FACTOR}×"
                                     if any(c["targets"] for c in rows) else "")))
            for theme in THEMES:
                with plt.rc_context(style(theme)):
                    factor_figure(out / f"{name}{'-dark' if theme == 'dark' else ''}.svg", fam, comparisons, names,
                                  theme)
    for metric in LOSS_FIGURES:
        if any(m == metric and c.study == LOSS for (_, c, m) in summary):
            name = f"loss-{metric}"
            figures.append((name, f"{METRICS[metric].label} against the share of packets lost: each series'"
                                  " median of the runs, with its 95% interval, in a panel for each scenario"
                                  " and network"))
            for theme in THEMES:
                with plt.rc_context(style(theme)):
                    loss_figure(out / f"{name}{'-dark' if theme == 'dark' else ''}.svg", metric, summary, names,
                                theme)
    for metric in METRICS:
        if any(m == metric for (_, _, m) in summary):
            figures.append((metric, f"{METRICS[metric].label}: each series' median of the runs in each scenario,"
                                    " with its 95% interval, in a panel for each network"))
            for theme in THEMES:
                with plt.rc_context(style(theme)):
                    bar_figure(out / f"{metric}{'-dark' if theme == 'dark' else ''}.svg", metric, summary, series,
                               names, theme)
    return figures


def picture(name, caption):
    return (f'<picture>\n  <source media="(prefers-color-scheme: dark)" srcset="{name}-dark.svg">\n'
            f'  <img alt="{caption}" src="{name}.svg">\n</picture>')


def loss_lines(losses, summary, figures, names=SERIES_NAMES):
    """Returns the report's section on what the loss added, if the results
    have a loss study."""
    if not losses:
        return []
    primary = [c for c in losses if c["primary"]]
    lines = [
        "## What packet loss adds to the primary metrics",
        "",
        "For each series, what losing a share of the packets to the client added to a metric, over the same"
        " scenario and network without loss. Both are measured on a link emulated in the kernel. *Added* is the"
        " Hodges–Lehmann estimate of the difference, in the metric's unit, with its exact 95% interval from the"
        " Mann–Whitney distribution; the network's round trip is beside it, for scale. The table has each"
        f" scenario's primary metric, and Holm's correction is over its {len(primary)} tests.",
        "",
        "| Series | Scenario | Network | Loss | Metric | Without loss | With loss | Added | Round trip | p | p (Holm) |",
        "|---|---|---|---:|---|---:|---:|---:|---:|---:|---:|",
    ]

    def row(c, corrected=True):
        base = next(k[1] for k in summary if k[0] == c["series"] and k[2] == c["metric"] and k[1].study == LOSS
                    and loss_of(k[1]) == 0
                    and (k[1].scenario, k[1].protocol, k[1].net) == (c["cell"].scenario, c["cell"].protocol,
                                                                       c["cell"].net))
        sb, sa = summary[(c["series"], base, c["metric"])], summary[(c["series"], c["cell"], c["metric"])]
        unit = METRICS[c["metric"]].unit
        cell = c["cell"]
        out = (f"| {names.get(c['series'], c['series'])} | {cell.title(factor=False)}"
               f" | {net_entry(cell.net)}"
               f" | {loss_of(cell):g}%"
               f" | {METRICS[c['metric']].label}" + (f" ({unit})" if unit else "")
               + f" | {fmt_interval(sb['median'], sb['lo'], sb['hi'])}"
               f" | {fmt_interval(sa['median'], sa['lo'], sa['hi'])}"
               f" | {fmt_interval(c['shift'], c['lo'], c['hi'])} | {RTT_MS.get(cell.net, '–')} ms | {fmt_p(c['p'])} |")
        return out + (f" {fmt_p(c['p_adjusted'])} |" if corrected else "")

    lines += [row(c) for c in primary]
    lines += ["", f"{outcome(primary)}.{out_of_reach(primary)}", ""]
    for name, caption in figures:
        if name.startswith("loss-"):
            lines += [picture(name, caption), ""]
    secondary = [c for c in losses if not c["primary"] and c["metric"] not in RUNTIME]
    if secondary:
        lines += [
            "## What packet loss adds to the secondary metrics",
            "",
            "The other metrics that each scenario records. They're exploratory, and their p-values aren't"
            " corrected.",
            "",
            "| Series | Scenario | Network | Loss | Metric | Without loss | With loss | Added | Round trip | p |",
            "|---|---|---|---:|---|---:|---:|---:|---:|---:|",
        ]
        lines += [row(c, corrected=False) for c in secondary]
        lines.append("")
    return lines


def and_list(items):
    """Joins items as a sentence lists them: a, b and c."""
    items = list(items)
    return items[0] if len(items) == 1 else ", ".join(items[:-1]) + " and " + items[-1]


def write_markdown(out, source, rep, rows, failed, summary, comparisons, targets, sens, figures,
                   families=FAMILIES, losses=()):
    plan = rep.get("plan") or {}
    host = rep.get("host", {})
    gate = plan.get("max_load", 0)
    docs = os.path.relpath(DOCS, out.resolve())
    names = series_names(comparisons, families)
    present = [fam for fam in families if any(fam.has(c) and c["primary"] for c in comparisons)]
    title = "; ".join(dict.fromkeys(factor_title(fam, names) for fam in present)) or "Benchmark results"
    results = len({(r["series"], r["cell"], r["run"]) for r in rows})
    series = list(dict.fromkeys([s for fam in present for s in (fam.baseline, fam.candidate)]
                                + sorted({r["series"] for r in rows})))
    keys = plan.get("keys")
    lines = [
        f"# {title}",
        "",
        f"The comparison benchmark's results in `{source}`, as `bench/report.py` analyses them: each series'"
        " median of every metric in each scenario and network, the improvement of one series over the other, and"
        " whether the difference is significant.",
        f"[How Occamy is measured]({docs}#how-occamy-is-measured) describes the scenarios, the networks and the"
        f" metrics, and [Analysis]({docs}#analysis) the statistics, which were fixed before any data was"
        " collected.",
        "",
        "## The run",
        "",
        f"- Started at {rep.get('started', '?')}"
        + (f", resumed at {', '.join(rep['resumed'])}" if rep.get("resumed") else "")
        + f", on {host.get('cpu', '?')} ({host.get('cpus', '?')} CPUs, {host.get('goos', '?')}), with"
        f" {host.get('engine', '?')}.",
        f"- {rep.get('runs', '?')} runs with seed {plan.get('seed', '?')}"
        + (", in quick mode" if rep.get("quick") else "")
        + f"; each scenario that measures a key's latency timed"
        f" {f'{keys} keys' if keys else 'the default number of keys'} a run.",
        (f"- Before each result, the harness waited until the host's 1-minute load average was below {gate}."
         if gate else "- The harness didn't wait for the host's load before each result."),
        f"- {results} results analysed, and {len(failed)} failed.",
        f"- The series are {and_list(names.get(s, s) for s in series)};"
        f" [Run a comparison]({docs}#run-a-comparison) gives each one's name in the results files.",
        "",
        "## How to read this report",
        "",
        "Each value is the median of the runs, followed by its 95% interval in brackets: an exact interval from"
        " order statistics, which assumes nothing about how the values are distributed.",
        "",
        "*Improvement* is the baseline's value divided by the candidate's, or the candidate's divided by the"
        " baseline's for a metric where higher is better, such as frames shown a second. Above 1×, the candidate"
        " is better: 3× is a third of the baseline's latency, or three times its frames. It's the Hodges–Lehmann"
        " estimate of that ratio, with its exact 95% interval from the Mann–Whitney distribution.",
        "",
        "*p* is the exact two-sided permutation test of the rank sum, over every split of the runs. Each"
        " scenario's *primary metric* is the one its comparison is judged on. *p (Holm)* corrects the primary"
        f" metrics' p-values with Holm's method, and a difference is significant where it's below {ALPHA}. The"
        " other metrics, the *secondary* ones, are exploratory, and their p-values aren't corrected.",
        "",
    ]
    if failed:
        lines += ["## Failed results", "", "These results failed, and the analysis leaves them out.", "",
                  "| Series | Scenario | Network | Run | Error |", "|---|---|---|---:|---|"]
        for r in failed:
            cell = Cell(r.get("study", ""), r.get("factor", ""), r["scenario"], r["protocol"], r["net"])
            lines.append(f"| {names.get(r['series'], r['series'])} | {cell.title()} | {net_entry(r['net'])}"
                         f" | {r['run']} | {r['error'].replace('|', '/')} |")
        lines.append("")

    for fam in families:
        fam_rows = [c for c in comparisons if fam.has(c)]
        primary = [c for c in fam_rows if c["primary"]]
        if not primary:
            continue
        b, a = fam.baseline, fam.candidate
        nb, na = names.get(b, b), names.get(a, a)

        # One family's sections are the report's own; several name theirs.
        def heading(text):
            return f"## {factor_title(fam, names)}: {text.lower()}" if len(present) > 1 else f"## {text}"

        # The targets judge the family's cells without a loss study's.
        judged = any(c["targets"] for c in primary)
        correction = "Holm" if fam.correction == "holm" else "Benjamini–Hochberg"
        intro = (f"Each scenario's primary metric for {nb} and {na}, and the improvement of {na} over {nb}: above"
                 f" 1×, {na} is better. {correction}'s correction is over these {len(primary)} tests.")
        header = f"| Scenario | Network | Metric | {nb} | {na} | Improvement | p | p ({correction}) |"
        rule = "|---|---|---|---:|---:|---:|---:|---:|"
        if judged:
            intro += (f" *Target* says whether the improvement meets the target of {TARGET_FACTOR}×, which it does"
                      f" where its interval's lower bound is at least {TARGET_FACTOR}×.")
            header += " Target |"
            rule += ":-:|"
        lines += [heading("Primary metrics"), "", intro, "", header, rule]
        for c in primary:
            sb, sa = summary[(b, c["cell"], c["metric"])], summary[(a, c["cell"], c["metric"])]
            unit = METRICS[c["metric"]].unit
            lines.append(
                f"| {c['cell'].title()} | {net_entry(c['cell'].net)} | {METRICS[c['metric']].label} ({unit})"
                f" | {fmt_interval(sb['median'], sb['lo'], sb['hi'])} | {fmt_interval(sa['median'], sa['lo'], sa['hi'])}"
                f" | {fmt_interval(c['factor'], c['lo'], c['hi'], '×')} | {fmt_p(c['p'])} | {fmt_p(c['p_adjusted'])} |"
                + (f" {('met' if tenfold(c) else 'no') if c['targets'] else '–'} |" if judged else "")
            )
        result = outcome(primary)
        if judged:
            result += (f", and {sum(tenfold(c) for c in primary)} of the {sum(c['targets'] for c in primary)}"
                       f" improvements meet the target of {TARGET_FACTOR}×")
        lines += ["", result + "." + (out_of_reach(primary) if fam.correction == "holm" else ""), ""]
        name = factors_name(fam)
        lines += [picture(name, next(caption for n, caption in figures if n == name)), ""]

        secondary = [c for c in fam_rows if not c["primary"] and c["metric"] not in RUNTIME]
        if secondary:
            lines += [
                heading("Secondary metrics"),
                "",
                f"The other metrics that each scenario records, for {nb} and {na}. They're exploratory, and their"
                " p-values aren't corrected.",
                "",
                f"| Scenario | Network | Metric | {nb} | {na} | Improvement | p |",
                "|---|---|---|---:|---:|---:|---:|",
            ]
            for c in secondary:
                sb, sa = summary[(b, c["cell"], c["metric"])], summary[(a, c["cell"], c["metric"])]
                unit = METRICS[c["metric"]].unit
                lines.append(
                    f"| {c['cell'].title()} | {net_entry(c['cell'].net)} | {METRICS[c['metric']].label}"
                    + (f" ({unit})" if unit else "")
                    + f" | {fmt_interval(sb['median'], sb['lo'], sb['hi'])}"
                    f" | {fmt_interval(sa['median'], sa['lo'], sa['hi'])}"
                    f" | {fmt_interval(c['factor'], c['lo'], c['hi'], '×')} | {fmt_p(c['p'])} |"
                )
            lines.append("")

    lines += loss_lines(losses, summary, figures, names)

    if targets:
        lines += [
            f"## The {TARGET_MS} ms target",
            "",
            f"A key's effect should show within {TARGET_MS} ms on the network with no delay and on the one with a"
            " 20 ms round trip. The target is met where the upper bound of the 95% interval of the latency's 95th"
            f" percentile is at most {TARGET_MS} ms. With no delay, the latency is the server's own processing;"
            " the other network adds its round trip.",
            "",
            "| Series | Scenario | Network | Latency, 95th percentile (ms) | Met |",
            "|---|---|---|---:|:-:|",
        ]
        for t in targets:
            lines.append(f"| {names.get(t['series'], t['series'])} | {t['cell'].title()} | {net_entry(t['cell'].net)}"
                         f" | {fmt_interval(t['median'], t['lo'], t['hi'])} | {'met' if t['met'] else 'no'} |")
        lines.append("")

    runtime = sorted({(s, c) for (s, c, m) in summary if m in RUNTIME}, key=lambda sc: (sc[0], sc[1].order()))
    if runtime:
        lines += [
            "## occamyd's runtime",
            "",
            "For each scenario and network, the median of the runs: the memory `occamyd` allocated and the"
            " garbage collection cycles it ran during the scenario, and its heap in use and its goroutines at the"
            " end.",
            "",
            "| Series | Scenario | Network | Allocated (MiB) | GC cycles | Heap (MiB) | Goroutines |",
            "|---|---|---|---:|---:|---:|---:|",
        ]
        for s, c in runtime:
            v = [summary.get((s, c, m), {}).get("median", np.nan) for m in RUNTIME]
            lines.append(f"| {names.get(s, s)} | {c.title()} | {net_entry(c.net)} | "
                         + " | ".join(fmt(x) for x in v) + " |")
        lines.append("")

    above, changed = sens
    lines += ["## Sensitivity to the host's load", ""]
    if above is None:
        lines += ["The harness didn't wait for the host's load, so there's no gate to test the conclusions"
                  " against.", ""]
    elif not above:
        lines += [f"None of the {results} results started while the host's 1-minute load average was above the"
                  f" gate of {gate}, so no conclusion on a primary metric depends on the host's load.", ""]
    else:
        lines += [f"{above} of the {results} results started while the host's 1-minute load average was above the"
                  f" gate of {gate}."]
        if not changed:
            lines += ["Without them, no conclusion on a primary metric changes.", ""]
        else:
            lines += [
                "Without them, these conclusions on primary metrics change, so they're fragile:",
                "",
                "| Comparison | Scenario | Network | Metric | With them | Without them |",
                "|---|---|---|---|---|---|",
            ]
            for ch in changed:
                c, d = ch["before"], ch["after"]
                fam = next(f for f in families if f.has(c))
                correction = "Holm" if fam.correction == "holm" else "Benjamini–Hochberg"

                def verdict(x):
                    if x is None:
                        return "not testable"
                    return (f"{fmt(x['factor'])}×, p ({correction}) {fmt_p(x['p_adjusted'])},"
                            f" {x['n_baseline']} and {x['n_candidate']} runs")

                lines.append(f"| {factor_title(fam, names)} | {c['cell'].title()} | {net_entry(c['cell'].net)}"
                             f" | {METRICS[c['metric']].label}"
                             f" | {verdict(c)} | {verdict(d)} |")
            lines.append("")

    lines += ["## Figures", "",
              "Each figure shows a metric's median for each series in each scenario, with its 95% interval, in a"
              " panel for each network. Where the values span more than a factor of ten, the axis is"
              " logarithmic.", ""]
    for name, caption in figures:
        if name.startswith(("factors-", "loss-")):
            continue
        lines += [f"### {METRICS[name].label}", "", picture(name, caption), ""]
    (out / "report.md").write_text("\n".join(lines))


def main(argv=None):
    ap = argparse.ArgumentParser(description="Analyse the comparison benchmark's results.")
    ap.add_argument("results", help="the harness's results, such as bench-essential.json")
    ap.add_argument("-o", "--out", default="bench-report", help="the directory to write into")
    args = ap.parse_args(argv)

    source = Path(args.results)
    rep = json.loads(source.read_text())
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)

    rows, failed = read_rows(rep)
    values = index(rows)
    summary = summarise(values)
    comparisons = compare(values)
    losses = compare_loss(values)
    targets = latency_targets(summary)
    gate = (rep.get("plan") or {}).get("max_load", 0)
    sens = sensitivity(rows, gate, comparisons)

    write_tables(out, rows, summary, comparisons, losses)
    figures = draw(out, summary, comparisons)
    write_markdown(out, source.name, rep, rows, failed, summary, comparisons, targets, sens, figures, losses=losses)

    primary = [c for c in comparisons if c["primary"]]
    print(f"{out / 'report.md'}: {len(primary)} primary comparisons, {sum(significant(c) for c in primary)}"
          f" significant, {sum(tenfold(c) for c in primary)} meet the {TARGET_FACTOR}× target; {len(failed)}"
          " failed results")


if __name__ == "__main__":
    main()
