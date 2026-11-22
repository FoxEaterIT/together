package workflow

import "strings"

// Built-in flows. They are starting points, not shared objects: creating a flow
// from a preset copies every state and arrow into the project, so edits never
// leak between projects. Labels are data in English, like the seeded states of
// the default flow; an administrator can rename them.

var presets = []Preset{
	{
		Slug:        "default",
		Name:        "Default",
		TeamKind:    "generic",
		Description: "Three states and no arrows: the smallest valid workflow.",
		States: []PresetState{
			{Key: "todo", Name: "To do", Category: "open"},
			{Key: "doing", Name: "In progress", Category: "open"},
			{Key: "done", Name: "Done", Category: "done"},
		},
	},
	{
		Slug:        "pm",
		Name:        "Project Manager",
		TeamKind:    "pm",
		Description: "Backlog, planning, review and a blocked branch, with a reopen arrow.",
		States: []PresetState{
			{Key: "backlog", Name: "Backlog", Category: "open"},
			{Key: "planned", Name: "Planned", Category: "open"},
			{Key: "doing", Name: "In progress", Category: "open"},
			{Key: "blocked", Name: "Blocked", Category: "open"},
			{Key: "review", Name: "Review", Category: "open"},
			{Key: "done", Name: "Done", Category: "done"},
		},
		Transitions: []PresetTransition{
			{From: "backlog", To: "planned", Name: "Plan"},
			{From: "planned", To: "doing", Name: "Start"},
			{From: "doing", To: "review", Name: "Send to review"},
			{From: "review", To: "doing", Name: "Changes requested"},
			{From: "review", To: "done", Name: "Approve"},
			{From: "doing", To: "blocked", Name: "Block"},
			{From: "blocked", To: "doing", Name: "Unblock"},
			{From: "done", To: "doing", Name: "Reopen"},
		},
	},
	{
		Slug:        "pm-light",
		Name:        "Project Manager (light)",
		TeamKind:    "pm",
		Description: "Short flow for small activities and Micro items.",
		States: []PresetState{
			{Key: "todo", Name: "To do", Category: "open"},
			{Key: "doing", Name: "In progress", Category: "open"},
			{Key: "done", Name: "Done", Category: "done"},
		},
		Transitions: []PresetTransition{
			{From: "todo", To: "doing", Name: "Start"},
			{From: "doing", To: "done", Name: "Complete"},
			{From: "done", To: "doing", Name: "Reopen"},
		},
	},
	{
		Slug:        "dev",
		Name:        "Dev",
		TeamKind:    "dev",
		Description: "Implementation, code review, testing and a blocked branch.",
		States: []PresetState{
			{Key: "todo", Name: "To do", Category: "open"},
			{Key: "doing", Name: "In progress", Category: "open"},
			{Key: "review", Name: "Code review", Category: "open"},
			{Key: "testing", Name: "Testing", Category: "open"},
			{Key: "blocked", Name: "Blocked", Category: "open"},
			{Key: "done", Name: "Done", Category: "done"},
		},
		Transitions: []PresetTransition{
			{From: "todo", To: "doing", Name: "Start"},
			{From: "doing", To: "review", Name: "Open pull request"},
			{From: "review", To: "doing", Name: "Changes requested"},
			{From: "review", To: "testing", Name: "Approve"},
			{From: "testing", To: "doing", Name: "Tests failed"},
			{From: "testing", To: "done", Name: "Merge"},
			{From: "doing", To: "blocked", Name: "Block"},
			{From: "blocked", To: "doing", Name: "Unblock"},
			{From: "done", To: "doing", Name: "Reopen"},
		},
	},
	{
		Slug:        "dev-light",
		Name:        "Dev (light)",
		TeamKind:    "dev",
		Description: "Short flow for Micro items and small fixes.",
		States: []PresetState{
			{Key: "todo", Name: "To do", Category: "open"},
			{Key: "doing", Name: "In progress", Category: "open"},
			{Key: "done", Name: "Done", Category: "done"},
		},
		Transitions: []PresetTransition{
			{From: "todo", To: "doing", Name: "Start"},
			{From: "doing", To: "done", Name: "Complete"},
			{From: "done", To: "doing", Name: "Reopen"},
		},
	},
	{
		Slug:        "design",
		Name:        "Design",
		TeamKind:    "design",
		Description: "Brief, wireframes, design, review and revisions.",
		States: []PresetState{
			{Key: "brief", Name: "Brief", Category: "open"},
			{Key: "wireframes", Name: "Wireframes", Category: "open"},
			{Key: "design", Name: "Design", Category: "open"},
			{Key: "review", Name: "Review", Category: "open"},
			{Key: "revisions", Name: "Revisions", Category: "open"},
			{Key: "approved", Name: "Approved", Category: "done"},
		},
		Transitions: []PresetTransition{
			{From: "brief", To: "wireframes", Name: "Start wireframes"},
			{From: "wireframes", To: "design", Name: "Approve structure"},
			{From: "design", To: "review", Name: "Send to review"},
			{From: "review", To: "revisions", Name: "Request changes"},
			{From: "revisions", To: "review", Name: "Resubmit"},
			{From: "review", To: "approved", Name: "Approve"},
			{From: "approved", To: "design", Name: "Reopen"},
		},
	},
	{
		Slug:        "design-light",
		Name:        "Design (light)",
		TeamKind:    "design",
		Description: "Short flow for small assets.",
		States: []PresetState{
			{Key: "brief", Name: "Brief", Category: "open"},
			{Key: "design", Name: "Design", Category: "open"},
			{Key: "approved", Name: "Approved", Category: "done"},
		},
		Transitions: []PresetTransition{
			{From: "brief", To: "design", Name: "Start"},
			{From: "design", To: "approved", Name: "Approve"},
			{From: "approved", To: "design", Name: "Reopen"},
		},
	},
	{
		Slug:        "support",
		Name:        "Customer Service",
		TeamKind:    "support",
		Description: "Triage, customer waiting and resolution, with a closed state.",
		States: []PresetState{
			{Key: "new", Name: "New", Category: "open"},
			{Key: "triaged", Name: "Triaged", Category: "open"},
			{Key: "doing", Name: "In progress", Category: "open"},
			{Key: "waiting", Name: "Waiting for customer", Category: "open"},
			{Key: "resolved", Name: "Resolved", Category: "done"},
			{Key: "closed", Name: "Closed", Category: "done"},
		},
		Transitions: []PresetTransition{
			{From: "new", To: "triaged", Name: "Triage"},
			{From: "triaged", To: "doing", Name: "Take"},
			{From: "doing", To: "waiting", Name: "Ask customer"},
			{From: "waiting", To: "doing", Name: "Customer replied"},
			{From: "doing", To: "resolved", Name: "Resolve"},
			{From: "resolved", To: "doing", Name: "Reopen"},
			{From: "resolved", To: "closed", Name: "Close"},
		},
	},
	{
		Slug:        "support-light",
		Name:        "Customer Service (light)",
		TeamKind:    "support",
		Description: "Short flow for quick requests.",
		States: []PresetState{
			{Key: "new", Name: "New", Category: "open"},
			{Key: "doing", Name: "In progress", Category: "open"},
			{Key: "resolved", Name: "Resolved", Category: "done"},
		},
		Transitions: []PresetTransition{
			{From: "new", To: "doing", Name: "Take"},
			{From: "doing", To: "resolved", Name: "Resolve"},
			{From: "resolved", To: "doing", Name: "Reopen"},
		},
	},
	{
		Slug:        "sales",
		Name:        "Sales",
		TeamKind:    "sales",
		Description: "Lead, qualification, proposal, negotiation, won or lost.",
		States: []PresetState{
			{Key: "lead", Name: "Lead", Category: "open"},
			{Key: "qualified", Name: "Qualified", Category: "open"},
			{Key: "proposal", Name: "Proposal sent", Category: "open"},
			{Key: "negotiation", Name: "Negotiation", Category: "open"},
			{Key: "won", Name: "Won", Category: "done"},
			{Key: "lost", Name: "Lost", Category: "done"},
		},
		Transitions: []PresetTransition{
			{From: "lead", To: "qualified", Name: "Qualify"},
			{From: "qualified", To: "proposal", Name: "Send proposal"},
			{From: "proposal", To: "negotiation", Name: "Negotiate"},
			{From: "proposal", To: "lost", Name: "Lost"},
			{From: "negotiation", To: "won", Name: "Win"},
			{From: "negotiation", To: "lost", Name: "Lost"},
			{From: "lost", To: "lead", Name: "Reopen"},
		},
	},
	{
		Slug:        "sales-light",
		Name:        "Sales (light)",
		TeamKind:    "sales",
		Description: "Short pipeline for small opportunities.",
		States: []PresetState{
			{Key: "lead", Name: "Lead", Category: "open"},
			{Key: "negotiation", Name: "Negotiation", Category: "open"},
			{Key: "won", Name: "Won", Category: "done"},
			{Key: "lost", Name: "Lost", Category: "done"},
		},
		Transitions: []PresetTransition{
			{From: "lead", To: "negotiation", Name: "Negotiate"},
			{From: "negotiation", To: "won", Name: "Win"},
			{From: "negotiation", To: "lost", Name: "Lost"},
			{From: "lost", To: "lead", Name: "Reopen"},
		},
	},
}

// Presets returns the catalog in declaration order.
func Presets() []Preset {
	out := make([]Preset, len(presets))
	copy(out, presets)
	return out
}

// PresetBySlug looks up one preset.
func PresetBySlug(slug string) (Preset, bool) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	for _, preset := range presets {
		if preset.Slug == slug {
			return preset, true
		}
	}
	return Preset{}, false
}
