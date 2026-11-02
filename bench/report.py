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
- comparisons.csv: per comparison, cell and metric, the factor, its 95%
  interval and p-values;
- report.md: the tables, the targets and the figures;
- the figures, as SVG in light and dark.

The analysis was fixed before any data, in
specs/008-native-architecture/comparison-benchmark.md, Analysis.
"""

import argparse
import csv
import itertools
import json
import os
from dataclasses import dataclass
from functools import cache
from pathlib import Path

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib import ticker
import numpy as np
from scipy import stats

SPEC = Path(__file__).resolve().parent.parent / "specs/008-native-architecture/comparison-benchmark.md"


@dataclass(frozen=True)
class Metric:
    label: str
    unit: str
    lower: bool  # whether lower is better


METRICS = {
    "latency_p95": Metric("L p95", "ms", True),
    "latency_p50": Metric("L p50", "ms", True),
    "first_frame_p95": Metric("First frame p95", "ms", True),
    "first_frame_p50": Metric("First frame p50", "ms", True),
    "completion": Metric("Completion", "ms", True),
    "bytes_down": Metric("Bytes down", "KiB", True),
    "rate": Metric("Bytes down per second", "KiB/s", True),
    "fps": Metric("Delivered frames per second", "frames/s", False),
    "bytes_per_frame": Metric("Bytes per frame", "KiB", True),
    "cpu_per_frame": Metric("Server CPU per frame", "ms", True),
    "server_cpu": Metric("Server CPU", "ms", True),
    "server_mem": Metric("Server memory, peak", "MiB", True),
    "client_cpu": Metric("Client CPU", "ms", True),
    "occamyd_alloc": Metric("occamyd allocated", "MiB", True),
    "occamyd_gc": Metric("occamyd GC cycles", "", True),
    "occamyd_heap": Metric("occamyd heap in use", "MiB", True),
    "occamyd_goroutines": Metric("occamyd goroutines", "", True),
}

# Per workload, its primary metric and its secondary ones, as the spec's
# Workloads table has them. What every result costs (COSTS) is secondary
# everywhere.
SCENARIOS = {
    "T1": ("latency_p95", ["latency_p50"]),
    "T2": ("completion", ["bytes_down", "server_cpu"]),
    "T3": ("rate", ["server_cpu"]),
    "T4": ("latency_p95", ["latency_p50", "rate"]),
    "T5": ("rate", ["server_cpu"]),
    "G1": ("rate", ["server_cpu"]),
    "G2": ("latency_p95", ["latency_p50"]),
    "G3": ("bytes_per_frame", ["cpu_per_frame", "fps"]),
    "G4": ("bytes_per_frame", ["cpu_per_frame", "fps"]),
    "G5": ("latency_p95", ["fps", "bytes_per_frame"]),
    "G6": ("bytes_down", ["completion"]),
    "G7": ("first_frame_p95", ["first_frame_p50"]),
    "G8": ("latency_p95", ["latency_p50"]),
    "G9": ("latency_p95", ["bytes_down", "latency_p50"]),
    "G10": ("fps", ["latency_p95", "bytes_per_frame", "cpu_per_frame"]),
    "G11": ("fps", ["latency_p95", "bytes_per_frame", "cpu_per_frame"]),
}
COSTS = ["server_cpu", "server_mem", "client_cpu"]
RUNTIME = ["occamyd_alloc", "occamyd_gc", "occamyd_heap", "occamyd_goroutines"]

# The containers that serve each series: their CPU and memory are its
# server's. The first one named occamy-v1* is the occamyd whose runtime
# the series reports.
SERVERS = {
    "guacd": ["guacd"],
    "occamy": ["occamy", "guacd"],
    "v1-native": ["occamy-v1"],
    "v1-native-nogfx": ["occamy-v1"],
    "v1-guacd": ["occamy-v1", "guacd"],
    "v1-guacd-gfx": ["occamy-v1-gfx", "guacd"],
}

NETS = {
    "N0": "N0: no delay",
    "N1": "N1: 20 ms, 100 Mbit/s",
    "N2": "N2: 50 ms, 20 Mbit/s",
    "N3": "N3: 100 ms, 5 Mbit/s",
    "N4": "N4: 250 ms, 1 Mbit/s",
}
PROTOCOLS = ["ssh", "vnc", "rdp"]


@dataclass(frozen=True)
class Family:
    name: str
    baseline: str
    candidate: str
    correction: str  # "holm" or "bh", over the family's primary metrics


# The comparisons. The essential plan has one family, confirmatory; the
# full plan's (the spec's Analysis, Families) join it when it runs.
FAMILIES = [Family("Guacamole to Occamy", "guacd", "v1-native", "holm")]

ALPHA = 0.05
TARGET_FACTOR = 10
TARGET_MS = 50

# One colour per series wherever it appears, in the validated palette's
# categorical order (light, dark).
COLORS = {
    "guacd": ("#2a78d6", "#3987e5"),
    "v1-native": ("#eb6834", "#d95926"),
    "occamy": ("#1baf7a", "#199e70"),
    "v1-guacd": ("#eda100", "#c98500"),
    "v1-guacd-gfx": ("#e87ba4", "#d55181"),
    "v1-native-nogfx": ("#008300", "#008300"),
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
                                 factor=est, lo=lo, hi=hi, coverage=cov, p=p, p_adjusted=np.nan))
        tested = [r for r in rows if r["primary"] and np.isfinite(r["p"])]
        for r, q in zip(tested, adjust([r["p"] for r in tested], fam.correction)):
            r["p_adjusted"] = q
        out += rows
    return out


def significant(c):
    return np.isfinite(c["p_adjusted"]) and c["p_adjusted"] < ALPHA


def tenfold(c):
    return np.isfinite(c["lo"]) and c["lo"] >= TARGET_FACTOR


def latency_targets(summary, families=FAMILIES):
    """Returns, per candidate and cell at N0 or N1 with a latency, whether
    the upper bound of its L p95 is at most TARGET_MS."""
    out = []
    for fam in families:
        for (series, cell, metric), s in summary.items():
            if series == fam.candidate and metric == "latency_p95" and cell.net in ("N0", "N1"):
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
    again = {(c["family"], c["cell"], c["metric"]): c for c in compare(index(kept)) if c["primary"]}
    changed = []
    for c in comparisons:
        if not c["primary"]:
            continue
        d = again.get((c["family"], c["cell"], c["metric"]))
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
        w = csv.writer(f)
        w.writerow(header)
        w.writerows(rows)


def cell_columns(cell):
    return [cell.study, cell.factor, cell.scenario, cell.protocol, cell.net]


CELL_HEADER = ["study", "factor", "scenario", "protocol", "net"]


def write_tables(out, rows, summary, comparisons):
    write_csv(out / "results.csv", ["series", *CELL_HEADER, "run", "metric", "value", "load_1m", "at"],
              [[r["series"], *cell_columns(r["cell"]), r["run"], r["metric"], r["value"], r["load"], r["at"]]
               for r in sorted(rows, key=lambda r: (r["cell"].order(), r["series"], r["metric"], r["run"]))])
    write_csv(out / "summary.csv", ["series", *CELL_HEADER, "metric", "unit", "n", "median", "lo", "hi", "coverage"],
              [[s, *cell_columns(c), m, METRICS[m].unit, v["n"], v["median"], v["lo"], v["hi"], v["coverage"]]
               for (s, c, m), v in sorted(summary.items(), key=lambda kv: (kv[0][1].order(), kv[0][0], kv[0][2]))])
    write_csv(out / "comparisons.csv",
              ["family", "baseline", "candidate", *CELL_HEADER, "metric", "primary", "n_baseline", "n_candidate",
               "factor", "lo", "hi", "coverage", "p", "p_adjusted"],
              [[c["family"], c["baseline"], c["candidate"], *cell_columns(c["cell"]), c["metric"], c["primary"],
                c["n_baseline"], c["n_candidate"], c["factor"], c["lo"], c["hi"], c["coverage"], c["p"],
                c["p_adjusted"]] for c in comparisons])


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


def bar_figure(path, metric, summary, series, theme):
    """Draws, per network, each cell's median of metric per series as a bar
    with its 95% interval."""
    t, dark = THEMES[theme], theme == "dark"
    cells = sorted({c for (s, c, m) in summary if m == metric and s in series}, key=Cell.order)
    nets = sorted({c.net for c in cells})
    labels = sorted({c.label() for c in cells}, key=lambda l: next(c.order()[2:] for c in cells if c.label() == l))
    fig, axes = plt.subplots(1, len(nets), figsize=(max(3.2, 0.9 * len(labels)) * len(nets) + 1.2, 3.6),
                             sharey=True, squeeze=False, layout="constrained")
    values = []
    for ax, net in zip(axes[0], nets):
        x = np.arange(len(labels))
        width = 0.8 / len(series)
        for i, s in enumerate(series):
            med, lo, hi = [], [], []
            for label in labels:
                v = next((summary[(s, c, metric)] for c in cells
                          if c.net == net and c.label() == label and (s, c, metric) in summary), None)
                med.append(v["median"] if v else np.nan)
                lo.append(v["lo"] if v else np.nan)
                hi.append(v["hi"] if v else np.nan)
            med, lo, hi = map(np.array, (med, lo, hi))
            values += [v for v in np.concatenate([med, lo, hi]) if np.isfinite(v) and v > 0]
            pos = x + (i - (len(series) - 1) / 2) * width
            ax.bar(pos, med, width, color=COLORS.get(s, ("#898781",) * 2)[dark], edgecolor=t["surface"],
                   linewidth=1.5, label=s)
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


def factor_figure(path, fam, comparisons, theme):
    """Draws the family's primary factors with their 95% intervals, on a log
    axis with lines at 1x and 10x."""
    t, dark = THEMES[theme], theme == "dark"
    rows = [c for c in comparisons if c["family"] == fam.name and c["primary"] and np.isfinite(c["factor"])]
    rows.sort(key=lambda c: c["cell"].order())
    fig, ax = plt.subplots(figsize=(6.4, 0.3 * len(rows) + 1.3), layout="constrained")
    y = np.arange(len(rows))[::-1]
    est = np.array([c["factor"] for c in rows])
    lo = np.array([c["lo"] for c in rows])
    hi = np.array([c["hi"] for c in rows])
    ok = np.isfinite(lo)
    color = COLORS.get(fam.candidate, ("#898781",) * 2)[dark]
    ax.errorbar(est[ok], y[ok], xerr=[est[ok] - lo[ok], hi[ok] - est[ok]], fmt="none", ecolor=color,
                elinewidth=2, capsize=0)
    ax.scatter(est, y, s=36, color=color, edgecolors=t["surface"], linewidths=1.5, zorder=3)
    for at, style_ in ((1, "-"), (TARGET_FACTOR, "--")):
        ax.axvline(at, color=t["muted"], linewidth=1, linestyle=style_, zorder=1)
        ax.annotate(f"{at}x", (at, 1.0), xycoords=("data", "axes fraction"), xytext=(3, 2),
                    textcoords="offset points", color=t["text2"], fontsize=8)
    ax.set_xscale("log")
    plain_log_axis(ax.xaxis, "x")
    ax.set_yticks(y, [f"{c['cell'].label()} · {c['cell'].net} · {METRICS[c['metric']].label}" for c in rows])
    ax.tick_params(axis="y", length=0)
    ax.grid(axis="y", visible=False)
    ax.set_xlabel(f"Factor by which {fam.candidate} is better than {fam.baseline} (log scale)")
    fig.suptitle(f"{fam.name}: primary metrics, with 95% intervals", x=0.0, ha="left", color=t["text"],
                 fontsize=11)
    save(fig, path)


def draw(out, summary, comparisons, families=FAMILIES):
    """Draws the figures in both themes and returns their names, without
    the theme's suffix, with a caption."""
    series = [s for s in COLORS if any(k[0] == s for k in summary)]
    figures = []
    for fam in families:
        if any(c["family"] == fam.name and c["primary"] for c in comparisons):
            name = "factors-" + fam.name.lower().replace(" ", "-")
            figures.append((name, f"{fam.name}: factors on the primary metrics"))
            for theme in THEMES:
                with plt.rc_context(style(theme)):
                    factor_figure(out / f"{name}{'-dark' if theme == 'dark' else ''}.svg", fam, comparisons, theme)
    for metric in METRICS:
        if any(m == metric for (_, _, m) in summary):
            figures.append((metric, METRICS[metric].label))
            for theme in THEMES:
                with plt.rc_context(style(theme)):
                    bar_figure(out / f"{metric}{'-dark' if theme == 'dark' else ''}.svg", metric, summary, series,
                               theme)
    return figures


def picture(name, caption):
    return (f'<picture>\n  <source media="(prefers-color-scheme: dark)" srcset="{name}-dark.svg">\n'
            f'  <img alt="{caption}" src="{name}.svg">\n</picture>')


def write_markdown(out, source, rep, rows, failed, summary, comparisons, targets, sens, figures,
                   families=FAMILIES):
    plan = rep.get("plan") or {}
    host = rep.get("host", {})
    gate = plan.get("max_load", 0)
    spec = os.path.relpath(SPEC, out.resolve())
    lines = [
        f"# Comparison results: {source}",
        "",
        "Generated by `bench/report.py`; the analysis is fixed in "
        f"[the comparison benchmark's spec]({spec}), Analysis.",
        "",
        f"- Collected from {rep.get('started', '?')}"
        + (f", resumed at {', '.join(rep['resumed'])}" if rep.get("resumed") else "")
        + f", on {host.get('cpu', '?')} ({host.get('cpus', '?')} CPUs, {host.get('goos', '?')}),"
        f" {host.get('engine', '?')}.",
        f"- {rep.get('runs', '?')} runs, seed {plan.get('seed', '?')}, {plan.get('keys', 'default')} probe keys"
        f" a scenario per run{', quick' if rep.get('quick') else ''}; load gate"
        f" {gate if gate else 'off'}.",
        f"- {len({(r['series'], r['cell'], r['run']) for r in rows})} results analysed, {len(failed)} failed.",
        "",
        "Values are the median of the runs, with an exact distribution-free 95% interval from order statistics."
        " A factor is the Hodges–Lehmann estimate of the ratio of the two series, oriented so that above 1 means"
        " the second is better, with its exact 95% interval from the Mann–Whitney distribution. p is the exact"
        " two-sided permutation test of the rank sum over every split of the runs.",
        "",
    ]
    if failed:
        lines += ["## Failed results", "", "| Series | Cell | Network | Run | Error |", "|---|---|---|---|---|"]
        for r in failed:
            lines.append(f"| {r['series']} | {r['scenario']} {r['protocol']} | {r['net']} | {r['run']} |"
                         f" {r['error'].replace('|', '/')} |")
        lines.append("")

    for fam in families:
        fam_rows = [c for c in comparisons if c["family"] == fam.name]
        primary = [c for c in fam_rows if c["primary"]]
        if not primary:
            continue
        b, a = fam.baseline, fam.candidate
        correction = "Holm" if fam.correction == "holm" else "Benjamini–Hochberg"
        lines += [
            f"## {fam.name}: `{b}` to `{a}`",
            "",
            f"Primary metrics, {len(primary)} tests, {correction}-corrected at {ALPHA}. The {TARGET_FACTOR}x target"
            f" is met where the interval's lower bound is at least {TARGET_FACTOR}.",
            "",
            f"| Cell | Network | Metric | `{b}` | `{a}` | Factor | p | p ({correction}) | {TARGET_FACTOR}x |",
            "|---|---|---|---:|---:|---:|---:|---:|:-:|",
        ]
        for c in primary:
            sb, sa = summary[(b, c["cell"], c["metric"])], summary[(a, c["cell"], c["metric"])]
            unit = METRICS[c["metric"]].unit
            lines.append(
                f"| {c['cell'].label()} | {c['cell'].net} | {METRICS[c['metric']].label} ({unit})"
                f" | {fmt_interval(sb['median'], sb['lo'], sb['hi'])} | {fmt_interval(sa['median'], sa['lo'], sa['hi'])}"
                f" | {fmt_interval(c['factor'], c['lo'], c['hi'], 'x')} | {fmt_p(c['p'])} | {fmt_p(c['p_adjusted'])}"
                f" | {'met' if tenfold(c) else 'no'} |"
            )
        met = sum(tenfold(c) for c in primary)
        sig = sum(significant(c) for c in primary)
        lines += [
            "",
            f"{sig} of {len(primary)} differences are significant after correction; the {TARGET_FACTOR}x target"
            f" is met in {met} of {len(primary)}.",
            "",
        ]
        name = "factors-" + fam.name.lower().replace(" ", "-")
        lines += [picture(name, f"{fam.name}: factors on the primary metrics"), ""]

        secondary = [c for c in fam_rows if not c["primary"] and c["metric"] not in RUNTIME]
        if secondary:
            lines += [
                f"### Secondary metrics: `{b}` to `{a}`",
                "",
                "Exploratory: the p-values are not corrected.",
                "",
                f"| Cell | Network | Metric | `{b}` | `{a}` | Factor | p |",
                "|---|---|---|---:|---:|---:|---:|",
            ]
            for c in secondary:
                sb, sa = summary[(b, c["cell"], c["metric"])], summary[(a, c["cell"], c["metric"])]
                unit = METRICS[c["metric"]].unit
                lines.append(
                    f"| {c['cell'].label()} | {c['cell'].net} | {METRICS[c['metric']].label}"
                    + (f" ({unit})" if unit else "")
                    + f" | {fmt_interval(sb['median'], sb['lo'], sb['hi'])}"
                    f" | {fmt_interval(sa['median'], sa['lo'], sa['hi'])}"
                    f" | {fmt_interval(c['factor'], c['lo'], c['hi'], 'x')} | {fmt_p(c['p'])} |"
                )
            lines.append("")

    if targets:
        lines += [
            f"## The {TARGET_MS} ms target",
            "",
            f"Met where the upper bound of the interval of `L` p95 is at most {TARGET_MS} ms. At N0 that is"
            " the server's own processing; N1 adds a 20 ms round trip.",
            "",
            "| Series | Cell | Network | `L` p95 (ms) | Met |",
            "|---|---|---|---:|:-:|",
        ]
        for t in targets:
            lines.append(f"| `{t['series']}` | {t['cell'].label()} | {t['cell'].net}"
                         f" | {fmt_interval(t['median'], t['lo'], t['hi'])} | {'met' if t['met'] else 'no'} |")
        lines.append("")

    runtime = sorted({(s, c) for (s, c, m) in summary if m in RUNTIME}, key=lambda sc: (sc[0], sc[1].order()))
    if runtime:
        lines += [
            "## occamyd's runtime",
            "",
            "Per result, the median of the runs: bytes allocated and GC cycles during it, and the heap in use and"
            " goroutines at its end.",
            "",
            "| Series | Cell | Network | Allocated (MiB) | GC cycles | Heap (MiB) | Goroutines |",
            "|---|---|---|---:|---:|---:|---:|",
        ]
        for s, c in runtime:
            v = [summary.get((s, c, m), {}).get("median", np.nan) for m in RUNTIME]
            lines.append(f"| `{s}` | {c.label()} | {c.net} | " + " | ".join(fmt(x) for x in v) + " |")
        lines.append("")

    above, changed = sens
    lines += ["## Sensitivity to the host's load", ""]
    if above is None:
        lines += ["The load gate was off; there is no sensitivity analysis.", ""]
    else:
        lines += [f"{above} of the results started with the host's 1-minute load above the gate of {gate}."]
        if not changed:
            lines += ["Without them, no conclusion on a primary metric changes.", ""]
        else:
            lines += [
                "Without them, these conclusions change, and are fragile:",
                "",
                "| Family | Cell | Network | Metric | Before | After |",
                "|---|---|---|---|---|---|",
            ]
            for ch in changed:
                c, d = ch["before"], ch["after"]

                def verdict(x):
                    if x is None:
                        return "not testable"
                    return (f"{fmt(x['factor'])}x, p ({x['family']}) {fmt_p(x['p_adjusted'])},"
                            f" n {x['n_baseline']}/{x['n_candidate']}")

                lines.append(f"| {c['family']} | {c['cell'].label()} | {c['cell'].net} | {METRICS[c['metric']].label}"
                             f" | {verdict(c)} | {verdict(d)} |")
            lines.append("")

    lines += ["## Figures", ""]
    for name, caption in figures:
        if name.startswith("factors-"):
            continue
        lines += [f"### {caption}", "", picture(name, caption), ""]
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
    targets = latency_targets(summary)
    gate = (rep.get("plan") or {}).get("max_load", 0)
    sens = sensitivity(rows, gate, comparisons)

    write_tables(out, rows, summary, comparisons)
    figures = draw(out, summary, comparisons)
    write_markdown(out, source.name, rep, rows, failed, summary, comparisons, targets, sens, figures)

    primary = [c for c in comparisons if c["primary"]]
    print(f"{out / 'report.md'}: {len(primary)} primary comparisons, {sum(significant(c) for c in primary)}"
          f" significant, {sum(tenfold(c) for c in primary)} at {TARGET_FACTOR}x; {len(failed)} failed results")


if __name__ == "__main__":
    main()
