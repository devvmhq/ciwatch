// The review's verdict with churn decided by code (.shared/factory.ts
// churnCheck): a blocker or major on lines the previous round's fix changed.
// Each workflow's folder holds this same file (bin/check).
import { churnCheck } from "../../.shared/factory.ts";

try {
  console.log(await churnCheck(process.env.ARTIFACTS_DIR ?? "", process.env.INPUTS_REVIEW ?? "{}"));
} catch (err) {
  console.error(`factory churn: ${(err as Error).message}`);
  process.exit(1);
}
