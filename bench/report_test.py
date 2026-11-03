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


def synthetic_window(runs=6, guacd=False):
    """Returns results where v1-native-w2 and v1-native type alike on T1 at
    N0, and on G5 over VNC at N2 v1-native's keys show twice as fast, it
    shows 27 of the scene's 30 frames a second where v1-native-w2 shows
    18, and its frames are 40 ms apart at p95 where v1-native-w2's are 90.
    With guacd, guacd is twenty times slower than v1-native on both."""
    rng = np.random.default_rng(2)
    series = [("v1-native-w2", 2.0, 18, 90.0), ("v1-native", 1.0, 27, 40.0)]
    if guacd:
        series.append(("guacd", 20.0, 15, 200.0))
    results = []
    for run in range(runs):
        for name, slower, fps, p95 in series:
            jitter = 1 + rng.uniform(-0.05, 0.05)
            typed = 2.0 * (20.0 if name == "guacd" else 1.0) * jitter
            results.append(result(name, "T1", "ssh", "N0", run,
                                  latency_ms=dict(n=100, p50=typed * 0.8, p95=typed, p99=typed, max=typed)))
            lat = 60.0 * slower * jitter
            results.append(result(name, "G5", "vnc", "N2", run, scene_fps=30, seconds=10.0,
                                  frames=fps * 10, server_frames=300, bytes_down=4_000_000,
                                  latency_ms=dict(n=100, p50=lat * 0.8, p95=lat, p99=lat, max=lat),
                                  frame_interval_ms=dict(n=fps * 10 - 1, p50=33.3, p95=p95 * jitter, p99=p95 * 2,
                                                         max=p95 * 3)))
    return dict(started="2026-11-02T00:00:00Z", quick=False, runs=runs, host={"cpu": "test"},
                plan=dict(seed=1, max_load=8, keys=100, blocks=[]), results=results)


def synthetic_loss(runs=6):
    """Returns a loss study's results under N1 and N2, where loss adds to
    each key what ADDED says, and to G5's frame interval twice that; guacd
    is ten times slower than v1-native without loss."""
    rng = np.random.default_rng(3)
    results = []
    for run in range(runs):
        for series, base in (("guacd", 200.0), ("v1-native", 20.0)):
            for net in ("N1", "N2"):
                for loss in (0, 0.5, 2):
                    jitter = 1 + rng.uniform(-0.02, 0.02)
                    lat = base * jitter + ADDED[series][loss]
                    for scenario, protocol in (("T1", "ssh"), ("G5", "vnc")):
                        r = result(series, scenario, protocol, net, run,
                                   latency_ms=dict(n=100, p50=lat * 0.8, p95=lat, p99=lat, max=lat))
                        r.update(study="loss", factor=f"loss={loss:g}%")
                        if scenario == "G5":
                            r.update(scene_fps=30, seconds=10.0, frames=270, bytes_down=4_000_000,
                                     frame_interval_ms=dict(n=269, p50=33.3, p95=40 + 2 * ADDED[series][loss],
                                                            p99=90, max=100))
                        results.append(r)
    return dict(started="2026-11-03T00:00:00Z", quick=False, runs=runs, host={"cpu": "test"},
                plan=dict(seed=1, max_load=8, keys=100, blocks=[]), results=results)


ADDED = {"v1-native": {0: 0.0, 0.5: 5.0, 2: 40.0}, "guacd": {0: 0.0, 0.5: 30.0, 2: 120.0}}


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

    def test_shift(self):
        a = np.array([20, 22, 19, 21, 20, 23.0])
        est, lo, hi, cov = report.shift(a, a + 30)
        self.assertAlmostEqual(est, 30)
        self.assertLess(lo, 30)
        self.assertGreater(hi, 30)
        self.assertGreaterEqual(cov, 0.95)
        self.assertAlmostEqual(report.shift(a + 30, a)[0], -30)
        self.assertTrue(math.isnan(report.shift(a[:3], a[:3])[1]))

    def test_out_of_reach(self):
        # At 6 runs a side the smallest p is 2/924: 23 tests can reach
        # 0.05 after Holm's correction, 24 cannot.
        six = dict(n_baseline=6, n_candidate=6)
        self.assertEqual(report.out_of_reach([six] * 23), "")
        self.assertIn("smallest p is 0.0022", report.out_of_reach([six] * 24))
        self.assertIn("over 24 tests none could be", report.out_of_reach([six] * 24))
        # A cell without runs on one side has no test.
        self.assertEqual(report.out_of_reach([dict(n_baseline=0, n_candidate=6)] * 30), "")

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
        self.assertAlmostEqual(float(t1["times_better"]), 20, delta=1.5)
        self.assertAlmostEqual(float(t1["p"]), 2 / 924)
        # Three primary tests in the family: Holm multiplies the smallest by 3.
        self.assertAlmostEqual(float(t1["p_adjusted"]), 3 * 2 / 924)
        self.assertGreaterEqual(float(t1["lo"]), 10)
        g6 = c[("G6", "vnc", "N0", "bytes_down")]
        self.assertAlmostEqual(float(g6["times_better"]), 20, delta=1.5)
        t2 = c[("T2", "ssh", "N0", "completion")]
        self.assertAlmostEqual(float(t2["times_better"]), 1, delta=0.1)
        self.assertGreater(float(t2["p"]), 0.05)
        # Secondary metrics are not corrected.
        self.assertEqual(c[("T1", "ssh", "N0", "latency_p50")]["p_adjusted"], "nan")
        # Server CPU and memory are the series' own containers.
        self.assertAlmostEqual(float(c[("T2", "ssh", "N0", "server_cpu")]["times_better"]), 1)
        self.assertAlmostEqual(float(c[("G6", "vnc", "N0", "server_mem")]["times_better"]), 10)

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

    def family_comparisons(self, out):
        with open(out / "comparisons.csv") as f:
            return {(r["family"], r["scenario"], r["protocol"], r["net"], r["metric"]): r
                    for r in csv.DictReader(f)}

    def test_scene_metrics(self):
        # A scene's share of frames never shown, from the frames it showed
        # in its seconds at its rate, and its cadence at p95; secondary.
        m = report.metrics_of(result("v1-native", "G5", "vnc", "N2", 0, scene_fps=30, seconds=10.0, frames=240,
                                     bytes_down=1024, frame_interval_ms=dict(n=239, p50=33.3, p95=50.0, p99=70.0,
                                                                             max=100.0)))
        self.assertAlmostEqual(m["scene_missed"], 20.0)
        self.assertEqual(m["frame_interval_p95"], 50.0)
        self.assertNotEqual(report.SCENARIOS["G5"][0], "scene_missed")
        for sc in ("G3", "G4", "G5"):
            self.assertTrue(set(report.CADENCE) <= set(report.SCENARIOS[sc][1]), sc)
        # Frames without a scene are no scene's.
        m = report.metrics_of(result("v1-native", "G6", "vnc", "N0", 0, seconds=1.0, frames=2, bytes_down=1024,
                                     completion_ms=100.0))
        self.assertNotIn("scene_missed", m)
        self.assertNotIn("frame_interval_p95", m)

    def test_window_family(self):
        # Without guacd: the frame window's family alone, Holm-corrected
        # over its own two primary tests, without the 10x target.
        out = self.analyse(synthetic_window())
        c = self.family_comparisons(out)
        self.assertFalse(any(k[0] == "Guacamole to Occamy" for k in c))
        g5 = c[("Frame window", "G5", "vnc", "N2", "latency_p95")]
        self.assertEqual((g5["baseline"], g5["candidate"], g5["primary"]), ("v1-native-w2", "v1-native", "True"))
        self.assertAlmostEqual(float(g5["times_better"]), 2, delta=0.15)
        self.assertAlmostEqual(float(g5["p"]), 2 / 924)
        self.assertAlmostEqual(float(g5["p_adjusted"]), 2 * 2 / 924)
        missed = c[("Frame window", "G5", "vnc", "N2", "scene_missed")]
        self.assertAlmostEqual(float(missed["times_better"]), 40 / 10)
        self.assertEqual(missed["p_adjusted"], "nan")
        interval = c[("Frame window", "G5", "vnc", "N2", "frame_interval_p95")]
        self.assertAlmostEqual(float(interval["times_better"]), 90 / 40, delta=0.2)
        self.assertGreater(float(c[("Frame window", "T1", "ssh", "N0", "latency_p95")]["p"]), 0.05)

        md = (out / "report.md").read_text()
        self.assertIn("## Frame window: `v1-native-w2` to `v1-native`", md)
        self.assertIn("1 of 2 differences are significant after correction.", md)
        self.assertNotIn("10x", md)
        self.assertIn("Scene frames never shown (%)", md)
        self.assertIn("Scene frame interval p95 (ms)", md)
        self.assertTrue((out / "factors-frame-window.svg").exists())
        self.assertTrue((out / "factors-frame-window-dark.svg").exists())
        self.assertFalse((out / "factors-guacamole-to-occamy.svg").exists())
        self.assertTrue((out / "scene_missed.svg").exists())

    def test_both_families(self):
        # With guacd too, each family compares its own pair; the targets
        # judge Guacamole's alone, and list v1-native's latency once.
        out = self.analyse(synthetic_window(guacd=True))
        c = self.family_comparisons(out)
        self.assertAlmostEqual(float(c[("Guacamole to Occamy", "T1", "ssh", "N0", "latency_p95")]["times_better"]), 20,
                               delta=2)
        self.assertAlmostEqual(float(c[("Frame window", "T1", "ssh", "N0", "latency_p95")]["times_better"]), 1, delta=0.1)
        md = (out / "report.md").read_text()
        guac, window = md.index("## Guacamole to Occamy"), md.index("## Frame window")
        self.assertIn("the 10x target is met in 2 of 2", md[guac:window])
        self.assertNotIn("10x", md[window:md.index("## The 50 ms target")])
        self.assertEqual(md.count("| `v1-native` | T1 ssh | N0 |"), 1)
        for name in ("factors-guacamole-to-occamy", "factors-frame-window"):
            self.assertTrue((out / f"{name}.svg").exists(), name)

    def test_loss(self):
        # What each loss adds over the same cell without it, Holm-corrected
        # over the 16 primary tests (2 series, 2 networks, 2 losses, T1
        # and G5), beside the round trip; the targets judge no loss cell.
        out = self.analyse(synthetic_loss())
        with open(out / "loss.csv") as f:
            loss = {(r["series"], r["scenario"], r["net"], r["loss"], r["metric"]): r for r in csv.DictReader(f)}
        self.assertEqual(sum(r["primary"] == "True" for r in loss.values()), 16)
        for series, added in ADDED.items():
            for level in (0.5, 2):
                c = loss[(series, "G5", "N2", str(float(level)), "latency_p95")]
                self.assertAlmostEqual(float(c["added"]), added[level], delta=5)
                self.assertEqual((c["primary"], c["rtt_ms"]), ("True", "50"))
            interval = loss[(series, "G5", "N2", "2.0", "frame_interval_p95")]
            self.assertEqual((interval["primary"], interval["p_adjusted"]), ("False", "nan"))
            self.assertAlmostEqual(float(interval["added"]), 2 * added[2])
        two = loss[("guacd", "T1", "N1", "2.0", "latency_p95")]
        self.assertAlmostEqual(float(two["p"]), 2 / 924)
        self.assertAlmostEqual(float(two["p_adjusted"]), 16 * 2 / 924)
        self.assertEqual(two["rtt_ms"], "20")
        self.assertFalse(any(r["loss"] == "0.0" for r in loss.values()))

        md = (out / "report.md").read_text()
        self.assertIn("## Loss: what it adds", md)
        self.assertIn("Primary metrics, 16 tests, Holm-corrected", md)
        self.assertIn("| `guacd` | G5 vnc | N2 | 2% | L p95 (ms) |", md)
        self.assertIn("| 50 ms |", md)
        self.assertNotIn("## The 50 ms target", md)
        self.assertNotIn("10x target is met", md)
        for name in ("loss-latency_p95", "loss-fps", "loss-frame_interval_p95"):
            self.assertTrue((out / f"{name}.svg").exists(), name)
            self.assertTrue((out / f"{name}-dark.svg").exists(), name)
            self.assertEqual(md.count(f'srcset="{name}-dark.svg"'), 1, name)
        # guacd against v1-native in each of the 12 cells, with or without
        # loss.
        with open(out / "comparisons.csv") as f:
            primary = [r for r in csv.DictReader(f) if r["family"] == "Guacamole to Occamy" and r["primary"] == "True"]
        self.assertEqual(len(primary), 12)

    def test_loss_all_failed(self):
        # A loss where every run of a series failed is marked on the
        # figure, and the series' other losses are still drawn.
        rep = synthetic_loss()
        for r in rep["results"]:
            if (r["series"], r["scenario"], r["net"], r["factor"]) == ("guacd", "G5", "N2", "loss=2%"):
                r.pop("latency_ms")
                r["error"] = "probe key 2: context deadline exceeded"
        out = self.analyse(rep)
        svg = (out / "loss-latency_p95.svg").read_text()
        self.assertEqual(svg.count("no run succeeded"), 1)
        with open(out / "loss.csv") as f:
            rows = {(r["series"], r["scenario"], r["net"], r["loss"]) for r in csv.DictReader(f)
                    if r["metric"] == "latency_p95"}
        self.assertIn(("guacd", "G5", "N2", "0.5"), rows)
        self.assertNotIn(("guacd", "G5", "N2", "2.0"), rows)

    def test_svg_is_deterministic(self):
        a = self.analyse(synthetic())
        b = self.analyse(synthetic())
        self.assertEqual((a / "latency_p95.svg").read_bytes(), (b / "latency_p95.svg").read_bytes())


if __name__ == "__main__":
    unittest.main()
