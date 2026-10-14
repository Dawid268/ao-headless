import { createFileRoute } from "@tanstack/react-router";
import { loadSessionView } from "../lib/session-view-chunk";

// SessionView renders from LocalSessionHost in the shell so switching between
// project sessions and scratchpads reuses one mounted instance. Awaiting the
// chunk keeps the previous screen up until the session can paint.
export const Route = createFileRoute("/_shell/projects/$projectId_/sessions/$sessionId")({
	loader: async () => {
		await loadSessionView();
	},
});
