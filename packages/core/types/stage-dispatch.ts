/** An epic's stage dispatch: the squad its stages go to (null when off). */
export interface IssueStageDispatch {
  issue_id: string;
  squad_id: string | null;
  dispatch_by: string | null;
}
