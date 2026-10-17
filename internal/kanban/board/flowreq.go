package board

// FlowRequest is one flow command over the wire: the command's name, the actor
// it runs for and the flags the cli's own command of that name takes. The cli
// builds it to hand its write to the huddle hub, and the hub's kanban tab
// decodes it to run the same board call the cli's direct path runs.
type FlowRequest struct {
	Command  string   `json:"command"`
	Actor    string   `json:"actor"`
	ID       string   `json:"id"`
	Note     string   `json:"note"`
	Branch   string   `json:"branch"`
	Worktree string   `json:"worktree"`
	Until    string   `json:"until"`
	Column   string   `json:"column"`
	Checks   string   `json:"checks"`
	Merge    string   `json:"merge"`
	On       []string `json:"on"`
	Pairs    []string `json:"pairs"`
	Force    bool     `json:"force"`
}
