# The `--json` output

`ciwatch --json <commit>` prints one compact JSON document on stdout, and the table is drawn from the same data.

## Versioning

The top-level `schema` field is the version of this document, currently `1`. Adding a field keeps the version. Removing a field or changing its meaning bumps it.

Schema 1 is not frozen yet: it freezes at ciwatch's first release, and until then fields may still be removed without a bump.

## Fields

| Field | Type | Present |
| --- | --- | --- |
| `schema` | integer | always |
| `sha` | string | always: the commit the runs are for |
| `runs` | array of objects | always |
| `runs[].id` | integer | always |
| `runs[].attempt` | integer | always: 1 for a run's first attempt, higher after a rerun (`--rerun-failed`), 0 when gh doesn't report it; the report is of this attempt |
| `runs[].workflow` | string | always |
| `runs[].status` | string | always |
| `runs[].conclusion` | string | always, empty while the run is not completed |
| `runs[].url` | string | always |
| `runs[].jobs` | array of objects | always, empty if the jobs could not be fetched |
| `runs[].jobs_error` | string | only when the run's jobs could not be fetched |
| `runs[].fuzz_crashes` | array of objects | only for a failed run (`failure` or `timed_out`) with `crash-Fuzz<Name>` artifacts, in the order the API lists them |
| `runs[].fuzz_crashes[].target` | string | always, with `fuzz_crashes`: the fuzz target, from the artifact name |
| `runs[].fuzz_crashes[].artifact` | string | always, with `fuzz_crashes`: the artifact's name, `crash-<target>` |
| `runs[].fuzz_crashes[].run_id` | integer | always, with `fuzz_crashes`: the run the artifact belongs to |
| `runs[].fuzz_crashes[].fetched_to` | string | only with `--fetch-crashes`, once downloaded: the directory, relative to the checkout |
| `runs[].fuzz_crashes[].fetch_error` | string | only with `--fetch-crashes`, when the download was skipped or failed |
| `runs[].artifacts_error` | string | only when the run's artifacts could not be listed |
| `runs[].newer_run` | object | only for a cancelled run whose workflow has a first run after it on the branch, for another commit |
| `runs[].newer_run.id` | integer | always, with `newer_run`: the newer run's ID |
| `runs[].newer_run.sha` | string | always, with `newer_run`: the newer run's commit |
| `runs[].jobs[].id` | integer | always |
| `runs[].jobs[].name` | string | always |
| `runs[].jobs[].status` | string | always |
| `runs[].jobs[].conclusion` | string | always, empty while the job is not completed |
| `runs[].jobs[].url` | string | always |
| `runs[].jobs[].steps` | array of objects | only for a failed job whose log was read, empty if no failed step had a line in the log |
| `runs[].jobs[].steps[].name` | string | always: the failed step's name, or `(job log)` when the job has no failed step |
| `runs[].jobs[].steps[].lines` | array of strings | always: the last 40 lines of the step's log, without gh's prefix and the timestamp |
| `runs[].jobs[].steps[].omitted` | integer | always: how many earlier lines were left out, 0 if none |
| `runs[].jobs[].log_error` | string | only for a failed job whose log could not be read |
| `runs[].jobs[].tests` | array of objects | only with `--go-tests`, for a failed job whose log was read, empty if no failing test was found |
| `runs[].jobs[].tests[].package` | string | always, empty if the log names no package |
| `runs[].jobs[].tests[].name` | string | always |
| `runs[].jobs[].tests[].lines` | array of strings | always: the log lines around the failure |
| `runs[].jobs[].tests[].running_test` | string | only for a crash: the test that was running |

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | every run passed |
| 1 | a run failed |
| 2 | usage error |
| 3 | a run was cancelled |
| 4 | ciwatch could not check |
