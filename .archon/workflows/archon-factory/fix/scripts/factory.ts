// Runs one of the pack's non-AI steps (.shared/factory.ts). Every node that uses
// this script binds step, task and lane, since Archon checks that each variable
// a script reads is bound. Each workflow's folder holds this same file, since a
// packaged workflow resolves scripts only from its own scripts/ (bin/check).
import { main } from "../../.shared/factory.ts";

await main(process.env.INPUTS_STEP ?? "", {
  task: process.env.INPUTS_TASK ?? "",
  lane: process.env.INPUTS_LANE ?? "",
  run: process.env.WORKFLOW_ID ?? "",
  artifacts: process.env.ARTIFACTS_DIR ?? "",
});
