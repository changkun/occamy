# /// script
# requires-python = ">=3.11"
# dependencies = [
#   "numpy>=2.0",
#   "scipy>=1.13",
#   "matplotlib>=3.9",
# ]
# ///
"""Tests of report.py, on synthetic results with known answers:

    uv run bench/report_test.py
"""

import csv
import json
import math
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import numpy as np

import report


def result(series, scenario, protocol, net, run, load=1.0, **fields):
    r = dict(series=series, scenario=scenario, protocol=protocol, net=net, run=run, study="matrix", load_1m=load)
    r.update(fields)
    return r


def synthetic(runs=6, ratio=20.0, load=None):
    """Returns results where v1-native is ratio times better than guacd on
    T1's latency and G6's bytes, and the same on T2's completion."""
    rng = np.random.default_rng(1)
    results = []
    for run in range(runs):
        for series, scale in (("guacd", ratio), ("v1-native", 1.0)):
            jitter = 1 + rng.uniform(-0.05, 0.05)
            lat = 2.0 * scale * jitter
            results.append(result(series, "T1", "ssh", "N0", run, load=load(run) if load else 1.0,
                                  latency_ms=dict(n=100, p50=lat * 0.8, p95=lat, p99=lat, max=lat),
                                  client_cpu_ms=10 * jitter))
            results.append(result(series, "T2", "ssh", "N0", run,
                                  completion_ms=100 * jitter, seconds=0.1, bytes_down=30000, frames=8,
                                  cpu_ms={"guacd": 50.0, "occamy-v1": 50.0}))
            results.append(result(series, "G6", "vnc", "N0", run,
                                  completion_ms=150.0, seconds=0.15, bytes_down=int(100_000 * scale * jitter),
                                  frames=2, mem_mib={"guacd": {"end": 100, "peak": 120},
                                                     "occamy-v1": {"end": 10, "peak": 12}},
                                  runtime={"occamy-v1": dict(goroutines=7, fds=9, heap_inuse_mib=2.0, gc_cycles=1,
                                                             gc_pause_ms=0.1, alloc_mib=3.0)}))
    return dict(started="2026-11-02T00:00:00Z", quick=False, runs=runs, host={"cpu": "test"},
                plan=dict(seed=1, max_load=8, keys=100, blocks=[]), results=results)


class Statistics(unittest.TestCase):
    def test_median_interval(self):
        med, lo, hi, cov = report.median_interval([5, 1, 4, 2, 6, 3])
        self.assertEqual((med, lo, hi), (3.5, 1, 6))
        self.assertAlmostEqual(cov, 1 - 2 / 64)
        _, lo, hi, cov = report.median_interval(range(1, 8))
        self.assertEqual((lo, hi), (1, 7))
        self.assertAlmostEqual(cov, 1 - 2 / 128)
        _, lo, hi, cov = report.median_interval(range(1, 12))
        self.assertEqual((lo, hi), (2, 10))  # 1 - 2 * 12/2048 = 98.8%; k = 3 covers 93.5%
        self.assertTrue(math.isnan(report.median_interval([1, 2, 3, 4, 5])[1]))

    def test_mann_whitney_rank(self):
        # The two-sided 5% critical values of U are 5 at 6 and 6 runs, 8 at
        # 7 and 7; the interval is [d(U+1), d(nm-U)].
        self.assertEqual(report.mann_whitney_rank(6, 6)[0], 6)
        self.assertEqual(report.mann_whitney_rank(7, 7)[0], 9)
        self.assertEqual(report.mann_whitney_rank(3, 3)[0], 0)
        self.assertGreaterEqual(report.mann_whitney_rank(6, 6)[1], 0.95)

    def test_rank_sum_p(self):
        self.assertAlmostEqual(report.rank_sum_p(np.arange(6) + 10, np.arange(6)), 2 / 924)
        self.assertAlmostEqual(report.rank_sum_p(np.arange(7) + 10, np.arange(7)), 2 / 3432)
        self.assertEqual(report.rank_sum_p(np.array([1, 1, 1, 2, 2, 2.0]), np.array([1, 1, 1, 2, 2, 2.0])), 1.0)
        # Ties across the series: midranks, and still exact.
        p = report.rank_sum_p(np.array([2, 2, 3, 3, 3, 3.0]), np.array([1, 1, 1, 1, 2, 2.0]))
        self.assertGreater(p, 2 / 924)
        self.assertLess(p, 0.05)

    def test_factor(self):
        a = np.array([20, 22, 19, 21, 20, 23.0])
        est, lo, hi, cov = report.factor(a, a / 10)
        self.assertAlmostEqual(est, 10, places=6)
        self.assertLess(lo, 10)
        self.assertGreater(hi, 10)
        self.assertGreaterEqual(cov, 0.95)
        # Higher is better: the factor is still above 1 where b is better.
        self.assertAlmostEqual(report.factor(a / 10, a, lower=False)[0], 10, places=6)
        self.assertTrue(math.isnan(report.factor(np.array([0, 1.0]), np.array([1, 1.0]))[0]))

    def test_holm(self):
        np.testing.assert_allclose(report.holm([0.01, 0.04, 0.03, 0.005]), [0.03, 0.06, 0.06, 0.02])
        np.testing.assert_allclose(report.holm([0.5, 0.6]), [1.0, 1.0])


class Report(unittest.TestCase):
    def analyse(self, rep):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        out = Path(tmp.name)
        src = out / "results.json"
        src.write_text(json.dumps(rep))
        report.main([str(src), "-o", str(out / "report")])
        return out / "report"

    def comparisons(self, out):
        with open(out / "comparisons.csv") as f:
            return {(r["scenario"], r["protocol"], r["net"], r["metric"]): r for r in csv.DictReader(f)}

    def test_known_answers(self):
        out = self.analyse(synthetic())
        c = self.comparisons(out)
        t1 = c[("T1", "ssh", "N0", "latency_p95")]
        self.assertEqual(t1["primary"], "True")
        self.assertAlmostEqual(float(t1["factor"]), 20, delta=1.5)
        self.assertAlmostEqual(float(t1["p"]), 2 / 924)
        # Three primary tests in the family: Holm multiplies the smallest by 3.
        self.assertAlmostEqual(float(t1["p_adjusted"]), 3 * 2 / 924)
        self.assertGreaterEqual(float(t1["lo"]), 10)
        g6 = c[("G6", "vnc", "N0", "bytes_down")]
        self.assertAlmostEqual(float(g6["factor"]), 20, delta=1.5)
        t2 = c[("T2", "ssh", "N0", "completion")]
        self.assertAlmostEqual(float(t2["factor"]), 1, delta=0.1)
        self.assertGreater(float(t2["p"]), 0.05)
        # Secondary metrics are not corrected.
        self.assertEqual(c[("T1", "ssh", "N0", "latency_p50")]["p_adjusted"], "nan")
        # Server CPU and memory are the series' own containers.
        self.assertAlmostEqual(float(c[("T2", "ssh", "N0", "server_cpu")]["factor"]), 1)
        self.assertAlmostEqual(float(c[("G6", "vnc", "N0", "server_mem")]["factor"]), 10)

        md = (out / "report.md").read_text()
        self.assertIn("2 of 3 differences are significant after correction", md)
        self.assertIn("the 10x target is met in 2 of 3", md)
        self.assertIn("no conclusion on a primary metric changes", md)
        for name in ("factors-guacamole-to-occamy", "latency_p95", "bytes_down", "completion"):
            self.assertTrue((out / f"{name}.svg").exists(), name)
            self.assertTrue((out / f"{name}-dark.svg").exists(), name)
            self.assertIn(f'srcset="{name}-dark.svg"', md)

    def test_failed_results(self):
        rep = synthetic()
        rep["results"].append(result("guacd", "T1", "ssh", "N0", 6, error="connection refused"))
        out = self.analyse(rep)
        with open(out / "results.csv") as f:
            runs = {r["run"] for r in csv.DictReader(f)}
        self.assertNotIn("6", runs)
        self.assertIn("connection refused", (out / "report.md").read_text())

    def test_sensitivity(self):
        # Two of T1's runs above the gate leave 4 runs a series, too few
        # for a significant difference after correction: fragile.
        out = self.analyse(synthetic(load=lambda run: 9.0 if run < 2 else 1.0))
        md = (out / "report.md").read_text()
        self.assertIn("4 of the results started with the host's 1-minute load above the gate of 8", md)
        self.assertIn("these conclusions change", md)
        self.assertIn("n 4/4", md)

    def test_svg_is_deterministic(self):
        a = self.analyse(synthetic())
        b = self.analyse(synthetic())
        self.assertEqual((a / "latency_p95.svg").read_bytes(), (b / "latency_p95.svg").read_bytes())


if __name__ == "__main__":
    unittest.main()
