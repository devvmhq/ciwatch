// Creates the dex tasks the human picked from the triaged backlog at the final
// review, with a `tasks: 1,3` line in the approval comment; the triage step
// wrote the numbered list to triage.json (.shared/factory.ts backlogTasks).
// Each workflow's folder holds this same file (bin/check).
import { backlogTasks } from "../../.shared/factory.ts";

try {
  console.log(await backlogTasks(process.env.INPUTS_COMMENT ?? "", process.env.ARTIFACTS_DIR ?? ""));
} catch (err) {
  console.error(`factory backlog: ${(err as Error).message}`);
  process.exit(1);
}
