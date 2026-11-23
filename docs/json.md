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
| `followed_from` | array of strings | only with `--follow`, when commits were left behind |
| `runs` | array of objects | always |
| `runs[].id` | integer | always |
| `runs[].workflow` | string | always |
| `runs[].status` | string | always |
| `runs[].conclusion` | string | always, empty while the run is not completed |
| `runs[].url` | string | always |
| `runs[].jobs` | array of objects | always, empty if the jobs could not be fetched |
| `runs[].jobs_error` | string | only when the run's jobs could not be fetched |
| `runs[].superseded_by` | string | only for a run cancelled by a newer push: that commit's sha |
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
