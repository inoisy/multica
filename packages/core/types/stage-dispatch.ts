import type { Issue } from "./issue";

/** An epic's stage dispatch: the squad its stages go to (null when off). */
export interface IssueStageDispatch {
  issue_id: string;
  squad_id: string | null;
  dispatch_by: string | null;
}

/** One epic on the conveyor board: the issue and the squad its stages go to. */
export interface StageDispatchEpic {
  issue: Issue;
  squad_id: string;
  dispatch_by: string | null;
}
