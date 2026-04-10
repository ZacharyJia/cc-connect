package core

import "github.com/ZacharyJia/cx-connect/dashboard"

func (e *Engine) SetDashboardReporter(r *dashboard.Reporter) {
	e.reporter = r
	if r == nil {
		return
	}
	r.SetSnapshotBuilder(e.dashboardSnapshot)
}

func (e *Engine) dashboardSnapshot() []dashboard.SessionGroupReport {
	groups := e.AdminSessionGroups()
	out := make([]dashboard.SessionGroupReport, 0, len(groups))
	for _, group := range groups {
		sessions := make([]dashboard.SessionReport, 0, len(group.Sessions))
		for _, session := range group.Sessions {
			sessions = append(sessions, dashboard.SessionReport{
				ID:             session.ID,
				Name:           session.Name,
				WorkDir:        session.WorkDir,
				AgentSessionID: session.AgentSessionID,
				HistoryCount:   session.HistoryCount,
				CreatedAt:      session.CreatedAt,
				UpdatedAt:      session.UpdatedAt,
				Busy:           session.Busy,
				Active:         session.Active,
			})
		}
		out = append(out, dashboard.SessionGroupReport{
			SessionKey:      group.SessionKey,
			Platform:        group.Platform,
			ActiveSessionID: group.ActiveSessionID,
			Interactive:     group.Interactive,
			Sessions:        sessions,
		})
	}
	return out
}
