import usageBackend from "@narumitw/pi-usage/dist/index.ts";
import usageStatus from "../status/usage.ts";

export default function (pi: Parameters<typeof usageBackend>[0]): void {
	usageBackend(pi);
	usageStatus(pi);
}
