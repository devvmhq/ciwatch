// The entry point for bin/squash: squash(upstream) from factory.ts.
import { squash } from "./factory.ts";

try {
  await squash(process.argv[2] ?? "@{upstream}");
} catch (err) {
  console.error(`squash: ${(err as Error).message}`);
  process.exit(1);
}
