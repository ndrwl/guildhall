# Windows CI speed timings, 2026-10-22

Seconds from `go test -json` on instrumented workflow_dispatch runs. main a/b: main 098b9af (runs 39833908526, 39834988861). branch a: 371fc61, every change but the shell batching (run 39834985109). branch b: the branch head (run 39836095590). Every test passed in all four runs.

**windows-latest**

| package | main a | main b | branch a | branch b |
|---|---:|---:|---:|---:|
| internal/interp | 186.9 | 299.2 | 57.3 | 115.4 |
| internal/git | 96.4 | 86.7 | 45.2 | 86.9 |
| internal/launch | 82.9 | 93.9 | 40.3 | 62.5 |
| internal/cli | 82.6 | 141.9 | 26.4 | 46.8 |
| internal/store | 51.3 | 77.1 | 4.6 | 16.0 |
| cmd/guildhall | 46.2 | 62.1 | 30.9 | 57.4 |
| internal/forge | 42.6 | 42.4 | 11.2 | 19.7 |
| internal/steward | 36.2 | 26.1 | 16.7 | 41.9 |
| internal/herdr | 33.0 | 29.0 | 25.4 | 18.9 |
| internal/artisan | 19.8 | 30.9 | 18.4 | 20.3 |
| internal/adapter/claude | 11.2 | 7.8 | 5.4 | 9.3 |
| whole go test (wall) | **249** | **365** | **91** | **176** |

**macos-latest**

| package | main a | main b | branch a | branch b |
|---|---:|---:|---:|---:|
| internal/interp | 15.9 | 28.5 | 13.6 | 15.0 |
| internal/git | 18.5 | 24.6 | 21.3 | 13.3 |
| internal/launch | 33.9 | 34.5 | 29.1 | 30.1 |
| internal/cli | 91.0 | 93.7 | 96.9 | 88.6 |
| internal/store | 2.6 | 4.2 | 1.7 | 2.5 |
| cmd/guildhall | 12.0 | 15.8 | 17.9 | 14.1 |
| internal/forge | 8.4 | 9.6 | 12.2 | 3.9 |
| internal/steward | 4.3 | 4.8 | 3.2 | 4.0 |
| internal/herdr | 13.1 | 19.4 | 15.1 | 3.6 |
| internal/artisan | 14.8 | 15.0 | 22.4 | 15.0 |
| internal/adapter/claude | 2.4 | 2.5 | 2.5 | 2.4 |
| whole go test (wall) | **101** | **105** | **100** | **99** |

**ubuntu-latest**

| package | main a | main b | branch a | branch b |
|---|---:|---:|---:|---:|
| internal/interp | 8.3 | 8.2 | 7.3 | 6.1 |
| internal/git | 5.5 | 5.5 | 3.1 | 2.4 |
| internal/launch | 30.5 | 30.7 | 28.3 | 28.3 |
| internal/cli | 26.8 | 26.6 | 24.6 | 26.4 |
| internal/store | 1.3 | 1.1 | 0.6 | 0.7 |
| cmd/guildhall | 10.0 | 10.3 | 9.4 | 9.3 |
| internal/forge | 2.9 | 3.0 | 1.6 | 1.4 |
| internal/steward | 1.9 | 2.0 | 1.7 | 1.5 |
| internal/herdr | 7.7 | 7.8 | 10.7 | 0.7 |
| internal/artisan | 16.6 | 17.0 | 16.4 | 19.0 |
| internal/adapter/claude | 2.6 | 2.7 | 2.5 | 2.2 |
| whole go test (wall) | **52** | **52** | **50** | **42** |

Hosted runners vary by up to about 2x between runs of the same commit, as the two main runs and the two branch runs show.
