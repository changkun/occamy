# Occamy against Guacamole

The results in `bench-essential.json`, analysed by `bench/report.py` as [the comparison benchmark's spec](../../../specs/008-native-architecture/comparison-benchmark.md) fixed before any data.

- Collected from 2026-11-03T20:47:56.02838Z, on Apple M5 Pro (18 CPUs, darwin), podman version 6.0.2.
- 6 runs, seed 20261102, 100 probe keys a scenario per run; load gate 8.
- 192 results analysed, 0 failed.

Values are the median of the runs, with an exact distribution-free 95% interval from order statistics. An improvement is the baseline's value divided by the candidate's, or the candidate's divided by the baseline's for a metric where higher is better, such as frames shown a second, so that above 1× the candidate is better: 3× is a third of the latency, or three times the frames. It is the Hodges–Lehmann estimate of that ratio, with its exact 95% interval from the Mann–Whitney distribution. p is the exact two-sided permutation test of the rank sum over every split of the runs.

## Guacamole to Occamy

The improvement of Occamy (`v1-native`) over Guacamole (`guacd`): above 1×, Occamy is better. Primary metrics, 16 tests, Holm-corrected at 0.05. The target is an improvement of 10×, met where the interval's lower bound is at least 10×.

| Scenario | Network | Metric | Guacamole | Occamy | Improvement | p | p (Holm) | Target |
|---|---|---|---:|---:|---:|---:|---:|:-:|
| Typing, SSH (T1) | No delay (N0) | Latency, 95th percentile (ms) | 2.48 [2.20, 5.39] | 1.73 [1.41, 2.63] | 1.49× [1.18, 2.21] | 0.0260 | 0.0519 | no |
| Long output, SSH (T2) | No delay (N0) | Time to complete (ms) | 930 [864, 978] | 19.7 [16.6, 48.6] | 46.8× [20.1, 55.7] | 0.0022 | 0.0346 | met |
| Key press, VNC (G2) | No delay (N0) | Latency, 95th percentile (ms) | 16.4 [15.7, 17.0] | 4.64 [4.28, 5.31] | 3.49× [3.21, 3.73] | 0.0022 | 0.0346 | no |
| Key press, RDP (G2) | No delay (N0) | Latency, 95th percentile (ms) | 20.6 [19.3, 22.4] | 8.10 [6.92, 12.3] | 2.55× [1.82, 2.98] | 0.0022 | 0.0346 | no |
| Key during motion, VNC (G5) | No delay (N0) | Latency, 95th percentile (ms) | 155 [132, 165] | 49.0 [39.6, 54.3] | 3.13× [2.71, 3.87] | 0.0022 | 0.0346 | no |
| Key during motion, RDP (G5) | No delay (N0) | Latency, 95th percentile (ms) | 150 [135, 191] | 66.8 [57.9, 92.3] | 2.27× [1.83, 2.76] | 0.0022 | 0.0346 | no |
| Full-screen photo, VNC (G6) | No delay (N0) | Data to the client (KiB) | 2,235 [1,497, 2,235] | 122 [122, 122] | 18.3× [12.3, 18.3] | 0.0022 | 0.0346 | met |
| Full-screen photo, RDP (G6) | No delay (N0) | Data to the client (KiB) | 2,175 [1,379, 2,175] | 108 [108, 115] | 20.1× [12.7, 20.1] | 0.0022 | 0.0346 | met |
| Typing, SSH (T1) | 50 ms, 20 Mbit/s (N2) | Latency, 95th percentile (ms) | 55.4 [54.3, 55.8] | 54.5 [53.4, 55.1] | 1.02× [0.999, 1.03] | 0.0649 | 0.0649 | no |
| Long output, SSH (T2) | 50 ms, 20 Mbit/s (N2) | Time to complete (ms) | 951 [917, 1,316] | 107 [105, 109] | 8.89× [8.54, 12.1] | 0.0022 | 0.0346 | no |
| Key press, VNC (G2) | 50 ms, 20 Mbit/s (N2) | Latency, 95th percentile (ms) | 69.4 [67.5, 73.3] | 57.5 [56.4, 59.0] | 1.21× [1.18, 1.24] | 0.0022 | 0.0346 | no |
| Key press, RDP (G2) | 50 ms, 20 Mbit/s (N2) | Latency, 95th percentile (ms) | 71.4 [70.2, 72.6] | 60.0 [59.3, 60.7] | 1.19× [1.18, 1.21] | 0.0022 | 0.0346 | no |
| Key during motion, VNC (G5) | 50 ms, 20 Mbit/s (N2) | Latency, 95th percentile (ms) | 214 [182, 229] | 93.2 [91.1, 113] | 2.19× [1.97, 2.47] | 0.0022 | 0.0346 | no |
| Key during motion, RDP (G5) | 50 ms, 20 Mbit/s (N2) | Latency, 95th percentile (ms) | 226 [201, 240] | 117 [111, 126] | 1.91× [1.70, 2.05] | 0.0022 | 0.0346 | no |
| Full-screen photo, VNC (G6) | 50 ms, 20 Mbit/s (N2) | Data to the client (KiB) | 2,235 [1,441, 2,235] | 122 [122, 122] | 18.3× [11.8, 18.3] | 0.0022 | 0.0346 | met |
| Full-screen photo, RDP (G6) | 50 ms, 20 Mbit/s (N2) | Data to the client (KiB) | 2,175 [1,169, 2,175] | 108 [108, 111] | 20.1× [10.8, 20.1] | 0.0022 | 0.0346 | met |

14 of 16 differences are significant after correction; the target of 10× is met in 5 of 16.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="factors-guacamole-to-occamy-dark.svg">
  <img alt="The improvement of Occamy over Guacamole on each scenario's primary metric, with 95% intervals" src="factors-guacamole-to-occamy.svg">
</picture>

### Guacamole to Occamy: secondary metrics

Exploratory: the p-values are not corrected.

| Scenario | Network | Metric | Guacamole | Occamy | Improvement | p |
|---|---|---|---:|---:|---:|---:|
| Typing, SSH (T1) | No delay (N0) | Latency, median (ms) | 1.49 [1.28, 1.67] | 0.96 [0.77, 1.18] | 1.54× [1.22, 1.98] | 0.0022 |
| Typing, SSH (T1) | No delay (N0) | Server memory, peak (MiB) | 62.7 [43.6, 78.9] | 32.9 [6.32, 65.6] | 2.31× [0.787, 6.99] | 0.1797 |
| Typing, SSH (T1) | No delay (N0) | Client CPU time (ms) | 63.3 [51.6, 67.9] | 60.7 [53.9, 66.2] | 1.02× [0.852, 1.17] | 0.6991 |
| Long output, SSH (T2) | No delay (N0) | Data to the client (KiB) | 101 [60.4, 113] | 8.95 [5.64, 9.67] | 11.4× [7.60, 14.3] | 0.0022 |
| Long output, SSH (T2) | No delay (N0) | Server CPU time (ms) | 892 [838, 926] | 20.2 [15.7, 24.6] | 43.8× [37.6, 55.2] | 0.0022 |
| Long output, SSH (T2) | No delay (N0) | Server memory, peak (MiB) | 68.0 [63.2, 93.4] | 32.3 [8.30, 61.8] | 3.02× [1.21, 7.61] | 0.0022 |
| Long output, SSH (T2) | No delay (N0) | Client CPU time (ms) | 47.5 [34.1, 53.1] | 22.7 [20.3, 26.9] | 1.97× [1.45, 2.32] | 0.0022 |
| Key press, VNC (G2) | No delay (N0) | Latency, median (ms) | 14.6 [14.3, 14.6] | 3.47 [3.37, 3.66] | 4.18× [4.00, 4.24] | 0.0022 |
| Key press, VNC (G2) | No delay (N0) | Server memory, peak (MiB) | 75.8 [73.5, 76.8] | 34.6 [13.7, 62.1] | 2.49× [1.24, 5.35] | 0.0022 |
| Key press, VNC (G2) | No delay (N0) | Client CPU time (ms) | 67.2 [54.8, 70.6] | 76.4 [67.1, 90.7] | 0.833× [0.695, 1.01] | 0.0931 |
| Key press, RDP (G2) | No delay (N0) | Latency, median (ms) | 17.1 [16.7, 17.8] | 5.85 [5.46, 6.59] | 2.90× [2.70, 3.13] | 0.0022 |
| Key press, RDP (G2) | No delay (N0) | Server memory, peak (MiB) | 92.4 [58.3, 93.5] | 44.7 [39.7, 51.4] | 2.06× [1.47, 2.32] | 0.0022 |
| Key press, RDP (G2) | No delay (N0) | Client CPU time (ms) | 72.8 [63.6, 85.3] | 99.2 [82.1, 114] | 0.728× [0.62, 0.854] | 0.0043 |
| Key during motion, VNC (G5) | No delay (N0) | Frames shown per second (frames/s) | 25.2 [24.0, 26.6] | 29.1 [28.4, 29.7] | 1.15× [1.11, 1.22] | 0.0022 |
| Key during motion, VNC (G5) | No delay (N0) | Scene frames never shown (%) | 16.0 [11.5, 20.1] | 2.89 [1.03, 5.17] | 5.61× [2.95, 16.8] | 0.0022 |
| Key during motion, VNC (G5) | No delay (N0) | Gap between frames, 95th percentile (ms) | 70.3 [61.9, 83.6] | 50.6 [45.9, 60.5] | 1.40× [1.22, 1.64] | 0.0022 |
| Key during motion, VNC (G5) | No delay (N0) | Data per frame (KiB) | 50.4 [49.8, 51.9] | 8.02 [7.97, 8.10] | 6.27× [6.21, 6.45] | 0.0022 |
| Key during motion, VNC (G5) | No delay (N0) | Server CPU per frame (ms) | 49.3 [45.8, 54.3] | 12.9 [11.5, 13.3] | 3.89× [3.50, 4.32] | 0.0022 |
| Key during motion, VNC (G5) | No delay (N0) | Server CPU time (ms) | 12,471 [11,923, 13,194] | 3,706 [3,407, 3,810] | 3.39× [3.24, 3.60] | 0.0022 |
| Key during motion, VNC (G5) | No delay (N0) | Server memory, peak (MiB) | 56.5 [54.7, 57.3] | 39.3 [36.4, 41.5] | 1.44× [1.36, 1.50] | 0.0022 |
| Key during motion, VNC (G5) | No delay (N0) | Client CPU time (ms) | 4,908 [4,644, 5,302] | 2,159 [2,095, 2,242] | 2.26× [2.16, 2.38] | 0.0022 |
| Key during motion, RDP (G5) | No delay (N0) | Frames shown per second (frames/s) | 23.3 [21.5, 23.7] | 24.3 [24.1, 24.6] | 1.04× [1.03, 1.12] | 0.0022 |
| Key during motion, RDP (G5) | No delay (N0) | Scene frames never shown (%) | 22.2 [20.9, 28.2] | 19.0 [18.1, 19.8] | 1.17× [1.10, 1.42] | 0.0022 |
| Key during motion, RDP (G5) | No delay (N0) | Gap between frames, 95th percentile (ms) | 74.2 [52.9, 89.4] | 50.8 [50.0, 57.1] | 1.38× [1.06, 1.68] | 0.0043 |
| Key during motion, RDP (G5) | No delay (N0) | Data per frame (KiB) | 66.0 [64.9, 66.7] | 7.73 [7.71, 7.84] | 8.53× [8.41, 8.63] | 0.0022 |
| Key during motion, RDP (G5) | No delay (N0) | Server CPU per frame (ms) | 58.9 [55.3, 71.2] | 15.8 [14.2, 20.2] | 3.83× [3.31, 4.48] | 0.0022 |
| Key during motion, RDP (G5) | No delay (N0) | Server CPU time (ms) | 13,938 [13,059, 15,869] | 3,855 [3,414, 4,855] | 3.70× [3.20, 4.13] | 0.0022 |
| Key during motion, RDP (G5) | No delay (N0) | Server memory, peak (MiB) | 77.9 [67.3, 90.1] | 68.4 [64.9, 74.2] | 1.15× [1.04, 1.31] | 0.0411 |
| Key during motion, RDP (G5) | No delay (N0) | Client CPU time (ms) | 5,808 [5,334, 6,410] | 1,897 [1,680, 2,138] | 3.13× [2.74, 3.49] | 0.0022 |
| Full-screen photo, VNC (G6) | No delay (N0) | Time to complete (ms) | 186 [176, 218] | 86.8 [73.4, 135] | 2.23× [1.61, 2.51] | 0.0022 |
| Full-screen photo, VNC (G6) | No delay (N0) | Server CPU time (ms) | 328 [200, 371] | 102 [92.9, 119] | 3.23× [2.15, 3.55] | 0.0022 |
| Full-screen photo, VNC (G6) | No delay (N0) | Server memory, peak (MiB) | 61.5 [37.2, 63.0] | 41.8 [38.4, 46.6] | 1.46× [0.968, 1.57] | 0.0649 |
| Full-screen photo, VNC (G6) | No delay (N0) | Client CPU time (ms) | 85.3 [81.2, 94.2] | 51.2 [47.3, 55.2] | 1.68× [1.57, 1.82] | 0.0022 |
| Full-screen photo, RDP (G6) | No delay (N0) | Time to complete (ms) | 177 [131, 266] | 158 [79.4, 188] | 1.39× [0.815, 2.23] | 0.3939 |
| Full-screen photo, RDP (G6) | No delay (N0) | Server CPU time (ms) | 296 [239, 326] | 139 [117, 163] | 2.06× [1.76, 2.40] | 0.0022 |
| Full-screen photo, RDP (G6) | No delay (N0) | Server memory, peak (MiB) | 70.4 [57.8, 70.9] | 67.5 [62.6, 71.4] | 1.03× [0.924, 1.09] | 0.3095 |
| Full-screen photo, RDP (G6) | No delay (N0) | Client CPU time (ms) | 93.8 [89.4, 105] | 67.5 [53.5, 71.6] | 1.47× [1.29, 1.69] | 0.0022 |
| Typing, SSH (T1) | 50 ms, 20 Mbit/s (N2) | Latency, median (ms) | 54.1 [53.6, 55.0] | 53.5 [53.0, 54.2] | 1.01× [0.994, 1.03] | 0.0931 |
| Typing, SSH (T1) | 50 ms, 20 Mbit/s (N2) | Server memory, peak (MiB) | 47.3 [46.7, 78.5] | 56.6 [11.3, 63.5] | 0.845× [0.749, 4.13] | 0.3095 |
| Typing, SSH (T1) | 50 ms, 20 Mbit/s (N2) | Client CPU time (ms) | 79.5 [63.6, 95.5] | 80.4 [61.4, 97.1] | 1.00× [0.76, 1.36] | 1.0000 |
| Long output, SSH (T2) | 50 ms, 20 Mbit/s (N2) | Data to the client (KiB) | 82.7 [59.6, 148] | 0.849 [0.699, 1.04] | 96.1× [66.3, 182] | 0.0022 |
| Long output, SSH (T2) | 50 ms, 20 Mbit/s (N2) | Server CPU time (ms) | 870 [836, 1,216] | 17.7 [14.3, 21.2] | 52.5× [43.5, 66.9] | 0.0022 |
| Long output, SSH (T2) | 50 ms, 20 Mbit/s (N2) | Server memory, peak (MiB) | 67.1 [66.4, 68.0] | 56.7 [12.4, 64.8] | 1.18× [1.04, 5.37] | 0.0022 |
| Long output, SSH (T2) | 50 ms, 20 Mbit/s (N2) | Client CPU time (ms) | 45.2 [35.2, 75.4] | 16.5 [12.7, 20.7] | 2.83× [1.94, 4.27] | 0.0022 |
| Key press, VNC (G2) | 50 ms, 20 Mbit/s (N2) | Latency, median (ms) | 67.3 [66.3, 67.6] | 55.9 [55.2, 56.7] | 1.20× [1.18, 1.22] | 0.0022 |
| Key press, VNC (G2) | 50 ms, 20 Mbit/s (N2) | Server memory, peak (MiB) | 75.3 [74.5, 76.7] | 61.4 [18.9, 70.5] | 1.23× [1.09, 3.95] | 0.0022 |
| Key press, VNC (G2) | 50 ms, 20 Mbit/s (N2) | Client CPU time (ms) | 86.4 [69.0, 109] | 105 [74.9, 123] | 0.839× [0.666, 1.11] | 0.2403 |
| Key press, RDP (G2) | 50 ms, 20 Mbit/s (N2) | Latency, median (ms) | 69.5 [68.7, 70.0] | 58.3 [57.9, 59.1] | 1.19× [1.18, 1.20] | 0.0022 |
| Key press, RDP (G2) | 50 ms, 20 Mbit/s (N2) | Server memory, peak (MiB) | 92.5 [67.0, 92.6] | 46.7 [34.1, 57.2] | 1.88× [1.61, 2.15] | 0.0022 |
| Key press, RDP (G2) | 50 ms, 20 Mbit/s (N2) | Client CPU time (ms) | 82.7 [76.8, 104] | 117 [93.6, 142] | 0.761× [0.593, 0.918] | 0.0260 |
| Key during motion, VNC (G5) | 50 ms, 20 Mbit/s (N2) | Frames shown per second (frames/s) | 25.3 [24.5, 26.0] | 29.2 [28.5, 29.6] | 1.15× [1.12, 1.19] | 0.0022 |
| Key during motion, VNC (G5) | 50 ms, 20 Mbit/s (N2) | Scene frames never shown (%) | 15.8 [13.5, 18.4] | 2.68 [1.19, 5.04] | 5.95× [3.65, 11.3] | 0.0022 |
| Key during motion, VNC (G5) | 50 ms, 20 Mbit/s (N2) | Gap between frames, 95th percentile (ms) | 69.0 [64.6, 72.0] | 49.8 [47.5, 51.5] | 1.37× [1.30, 1.45] | 0.0022 |
| Key during motion, VNC (G5) | 50 ms, 20 Mbit/s (N2) | Data per frame (KiB) | 46.3 [44.9, 46.9] | 7.90 [7.87, 7.97] | 5.86× [5.70, 5.93] | 0.0022 |
| Key during motion, VNC (G5) | 50 ms, 20 Mbit/s (N2) | Server CPU per frame (ms) | 46.7 [44.7, 49.2] | 12.4 [11.3, 13.8] | 3.77× [3.32, 4.10] | 0.0022 |
| Key during motion, VNC (G5) | 50 ms, 20 Mbit/s (N2) | Server CPU time (ms) | 17,164 [16,097, 18,395] | 3,603 [3,353, 3,999] | 4.76× [4.23, 5.24] | 0.0022 |
| Key during motion, VNC (G5) | 50 ms, 20 Mbit/s (N2) | Server memory, peak (MiB) | 56.3 [55.6, 57.1] | 60.2 [38.2, 70.9] | 0.936× [0.806, 1.47] | 0.3939 |
| Key during motion, VNC (G5) | 50 ms, 20 Mbit/s (N2) | Client CPU time (ms) | 6,644 [6,179, 7,121] | 2,206 [2,041, 2,355] | 3.01× [2.75, 3.26] | 0.0022 |
| Key during motion, RDP (G5) | 50 ms, 20 Mbit/s (N2) | Frames shown per second (frames/s) | 23.3 [22.4, 23.7] | 24.0 [23.7, 24.7] | 1.04× [1.01, 1.06] | 0.0022 |
| Key during motion, RDP (G5) | 50 ms, 20 Mbit/s (N2) | Scene frames never shown (%) | 22.4 [21.0, 25.2] | 20.0 [17.6, 20.8] | 1.14× [1.04, 1.27] | 0.0022 |
| Key during motion, RDP (G5) | 50 ms, 20 Mbit/s (N2) | Gap between frames, 95th percentile (ms) | 71.4 [60.0, 84.2] | 55.4 [44.5, 65.3] | 1.30× [1.09, 1.53] | 0.0043 |
| Key during motion, RDP (G5) | 50 ms, 20 Mbit/s (N2) | Data per frame (KiB) | 53.7 [48.8, 56.1] | 7.79 [7.64, 7.87] | 6.93× [6.39, 7.17] | 0.0022 |
| Key during motion, RDP (G5) | 50 ms, 20 Mbit/s (N2) | Server CPU per frame (ms) | 54.3 [52.1, 57.5] | 15.3 [14.3, 18.5] | 3.62× [3.10, 3.86] | 0.0022 |
| Key during motion, RDP (G5) | 50 ms, 20 Mbit/s (N2) | Server CPU time (ms) | 19,641 [18,438, 20,991] | 3,668 [3,422, 4,428] | 5.28× [4.74, 5.78] | 0.0022 |
| Key during motion, RDP (G5) | 50 ms, 20 Mbit/s (N2) | Server memory, peak (MiB) | 73.1 [67.9, 80.4] | 65.3 [63.2, 71.8] | 1.09× [1.03, 1.23] | 0.0152 |
| Key during motion, RDP (G5) | 50 ms, 20 Mbit/s (N2) | Client CPU time (ms) | 8,207 [7,608, 8,758] | 1,854 [1,713, 2,147] | 4.33× [3.98, 4.73] | 0.0022 |
| Full-screen photo, VNC (G6) | 50 ms, 20 Mbit/s (N2) | Time to complete (ms) | 1,064 [702, 1,127] | 208 [160, 230] | 5.09× [4.38, 5.76] | 0.0022 |
| Full-screen photo, VNC (G6) | 50 ms, 20 Mbit/s (N2) | Server CPU time (ms) | 320 [184, 334] | 121 [107, 136] | 2.54× [1.72, 2.91] | 0.0022 |
| Full-screen photo, VNC (G6) | 50 ms, 20 Mbit/s (N2) | Server memory, peak (MiB) | 61.8 [36.6, 62.8] | 46.0 [42.6, 51.3] | 1.32× [0.86, 1.44] | 0.0649 |
| Full-screen photo, VNC (G6) | 50 ms, 20 Mbit/s (N2) | Client CPU time (ms) | 87.3 [84.7, 93.3] | 50.4 [41.9, 64.2] | 1.74× [1.45, 2.02] | 0.0022 |
| Full-screen photo, RDP (G6) | 50 ms, 20 Mbit/s (N2) | Time to complete (ms) | 991 [606, 1,044] | 151 [145, 164] | 6.37× [4.17, 6.95] | 0.0022 |
| Full-screen photo, RDP (G6) | 50 ms, 20 Mbit/s (N2) | Server CPU time (ms) | 289 [183, 423] | 124 [121, 145] | 2.31× [1.49, 2.92] | 0.0022 |
| Full-screen photo, RDP (G6) | 50 ms, 20 Mbit/s (N2) | Server memory, peak (MiB) | 62.0 [61.6, 62.5] | 64.3 [62.9, 68.7] | 0.964× [0.91, 0.983] | 0.0022 |
| Full-screen photo, RDP (G6) | 50 ms, 20 Mbit/s (N2) | Client CPU time (ms) | 89.6 [69.3, 116] | 56.7 [49.1, 61.6] | 1.63× [1.41, 1.90] | 0.0022 |

## The 50 ms target

Met where the upper bound of the interval of the latency's 95th percentile is at most 50 ms. With no delay that is the server's own processing; 20 ms, 100 Mbit/s adds a 20 ms round trip.

| Series | Scenario | Network | Latency p95 (ms) | Met |
|---|---|---|---:|:-:|
| Occamy | Typing, SSH (T1) | No delay (N0) | 1.73 [1.41, 2.63] | met |
| Occamy | Key press, VNC (G2) | No delay (N0) | 4.64 [4.28, 5.31] | met |
| Occamy | Key press, RDP (G2) | No delay (N0) | 8.10 [6.92, 12.3] | met |
| Occamy | Key during motion, VNC (G5) | No delay (N0) | 49.0 [39.6, 54.3] | no |
| Occamy | Key during motion, RDP (G5) | No delay (N0) | 66.8 [57.9, 92.3] | no |

## occamyd's runtime

Per result, the median of the runs: bytes allocated and GC cycles during it, and the heap in use and goroutines at its end.

| Series | Scenario | Network | Allocated (MiB) | GC cycles | Heap (MiB) | Goroutines |
|---|---|---|---:|---:|---:|---:|
| Occamy | Typing, SSH (T1) | No delay (N0) | 1.31 | 0 | 26.1 | 8.00 |
| Occamy | Long output, SSH (T2) | No delay (N0) | 4.28 | 1.50 | 8.45 | 8.00 |
| Occamy | Key press, VNC (G2) | No delay (N0) | 9.59 | 2.00 | 12.2 | 8.00 |
| Occamy | Key press, RDP (G2) | No delay (N0) | 22.6 | 2.00 | 24.5 | 8.00 |
| Occamy | Key during motion, VNC (G5) | No delay (N0) | 159 | 13.0 | 27.3 | 8.00 |
| Occamy | Key during motion, RDP (G5) | No delay (N0) | 87.7 | 5.00 | 37.8 | 8.00 |
| Occamy | Full-screen photo, VNC (G6) | No delay (N0) | 35.9 | 3.00 | 30.3 | 8.00 |
| Occamy | Full-screen photo, RDP (G6) | No delay (N0) | 57.4 | 2.00 | 51.2 | 8.00 |
| Occamy | Typing, SSH (T1) | 50 ms, 20 Mbit/s (N2) | 1.30 | 0 | 45.5 | 8.50 |
| Occamy | Long output, SSH (T2) | 50 ms, 20 Mbit/s (N2) | 1.68 | 0 | 47.5 | 8.50 |
| Occamy | Key press, VNC (G2) | 50 ms, 20 Mbit/s (N2) | 10.6 | 0.5 | 37.8 | 8.50 |
| Occamy | Key press, RDP (G2) | 50 ms, 20 Mbit/s (N2) | 22.4 | 2.00 | 25.4 | 8.00 |
| Occamy | Key during motion, VNC (G5) | 50 ms, 20 Mbit/s (N2) | 106 | 9.00 | 25.8 | 8.50 |
| Occamy | Key during motion, RDP (G5) | 50 ms, 20 Mbit/s (N2) | 93.2 | 5.00 | 53.3 | 8.00 |
| Occamy | Full-screen photo, VNC (G6) | 50 ms, 20 Mbit/s (N2) | 40.9 | 3.00 | 35.7 | 8.50 |
| Occamy | Full-screen photo, RDP (G6) | 50 ms, 20 Mbit/s (N2) | 54.5 | 3.50 | 46.7 | 8.00 |

## Sensitivity to the host's load

0 of the results started with the host's 1-minute load above the gate of 8.
Without them, no conclusion on a primary metric changes.

## Figures

### Latency, 95th percentile

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="latency_p95-dark.svg">
  <img alt="Latency, 95th percentile" src="latency_p95.svg">
</picture>

### Latency, median

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="latency_p50-dark.svg">
  <img alt="Latency, median" src="latency_p50.svg">
</picture>

### Time to complete

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="completion-dark.svg">
  <img alt="Time to complete" src="completion.svg">
</picture>

### Data to the client

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="bytes_down-dark.svg">
  <img alt="Data to the client" src="bytes_down.svg">
</picture>

### Frames shown per second

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="fps-dark.svg">
  <img alt="Frames shown per second" src="fps.svg">
</picture>

### Scene frames never shown

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="scene_missed-dark.svg">
  <img alt="Scene frames never shown" src="scene_missed.svg">
</picture>

### Gap between frames, 95th percentile

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="frame_interval_p95-dark.svg">
  <img alt="Gap between frames, 95th percentile" src="frame_interval_p95.svg">
</picture>

### Data per frame

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="bytes_per_frame-dark.svg">
  <img alt="Data per frame" src="bytes_per_frame.svg">
</picture>

### Server CPU per frame

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="cpu_per_frame-dark.svg">
  <img alt="Server CPU per frame" src="cpu_per_frame.svg">
</picture>

### Server CPU time

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="server_cpu-dark.svg">
  <img alt="Server CPU time" src="server_cpu.svg">
</picture>

### Server memory, peak

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="server_mem-dark.svg">
  <img alt="Server memory, peak" src="server_mem.svg">
</picture>

### Client CPU time

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="client_cpu-dark.svg">
  <img alt="Client CPU time" src="client_cpu.svg">
</picture>

### occamyd allocated

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="occamyd_alloc-dark.svg">
  <img alt="occamyd allocated" src="occamyd_alloc.svg">
</picture>

### occamyd GC cycles

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="occamyd_gc-dark.svg">
  <img alt="occamyd GC cycles" src="occamyd_gc.svg">
</picture>

### occamyd heap in use

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="occamyd_heap-dark.svg">
  <img alt="occamyd heap in use" src="occamyd_heap.svg">
</picture>

### occamyd goroutines

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="occamyd_goroutines-dark.svg">
  <img alt="occamyd goroutines" src="occamyd_goroutines.svg">
</picture>
