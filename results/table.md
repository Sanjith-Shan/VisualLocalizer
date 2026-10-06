Median translation and rotation error per scene (lower is better).

| Scene | PoseNet | DSAC* (RGB + 3D model) | This work |
|---|---|---|---|
| chess | 32 cm, 8.12° | 1.8 cm, 1.1° | 2.7 cm, 0.97° |
| fire | 47 cm, 14.4° | 1.9 cm, 1.24° | 2.2 cm, 0.96° |
| heads | 29 cm, 12° | 1.1 cm, 1.82° | 1.0 cm, 0.79° |
| office | 48 cm, 7.68° | 2.5 cm, 1.15° | 3.2 cm, 0.95° |
| pumpkin | 47 cm, 8.42° | 3.9 cm, 1.34° | 5.5 cm, 1.38° |
| redkitchen | 59 cm, 8.64° | 3.8 cm, 1.68° | 4.6 cm, 1.49° |
| stairs | 47 cm, 13.8° | 2.9 cm, 1.16° | 2.7 cm, 0.79° |

This work, full detail (raw pose.txt GT). Throughput is the multi-thread eval run.

| Scene | Test frames | Localized | Median | Within 5 cm, 5° | Within 2 cm, 2° | DSAC* RGB+3D within 5 cm, 5° | Eval threads | Throughput fps | Map points |
|---|---|---|---|---|---|---|---|---|---|
| chess | 2000 | 100.0% | 2.67 cm, 0.968° | 88.6% | 33.2% | 97.5% | 10 | 48.6 | 488339 |
| fire | 2000 | 100.0% | 2.24 cm, 0.964° | 90.0% | 43.4% | 93.5% | 10 | 25.8 | 564586 |
| heads | 1000 | 100.0% | 1.03 cm, 0.787° | 100.0% | 87.5% | 99.8% | 10 | 51.0 | 116008 |
| office | 4000 | 100.0% | 3.19 cm, 0.952° | 76.5% | 24.0% | 90.0% | 10 | 52.9 | 736953 |
| pumpkin | 2000 | 99.8% | 5.51 cm, 1.375° | 42.9% | 6.5% | 62.4% | 10 | 44.9 | 708929 |
| redkitchen | 5000 | 100.0% | 4.60 cm, 1.493° | 55.9% | 11.2% | 65.3% | 10 | 42.6 | 1542391 |
| stairs | 1000 | 100.0% | 2.73 cm, 0.790° | 83.1% | 37.0% | 87.5% | 10 | 49.6 | 298514 |

Sources:
- PoseNet: Kendall, Grimes, Cipolla. PoseNet: A Convolutional Network for Real-Time 6-DOF Camera Relocalization. ICCV 2015. Numbers from arXiv:1505.07427, Figure 6 table, PoseNet column (meters converted to cm).
- DSAC* (RGB + 3D model): Brachmann and Rother. Visual Camera Re-Localization from RGB and RGB-D Images Using DSAC. TPAMI 2021. Numbers from arXiv:2002.12324, Figure 6, row 'RGB + 3D model' (depth used for training only, as here).
