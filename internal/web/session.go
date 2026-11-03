package web

import (
	"net/http"

	"github.com/Ray0907/spanbox/internal/store"
)

const (
	sessionTurnLimit = 200
	sessionTextChars = 600
)

type sessionTurnView struct {
	store.SessionTurn
	Number int
}

type sessionSummary struct {
	Turns        int
	Traces       int
	InputTokens  *int64
	OutputTokens *int64
	CostUSD      *float64
}

type sessionPage struct {
	layoutData
	SessionID string
	Turns     []sessionTurnView
	Summary   sessionSummary
	Truncated bool
}

// summarize follows the list page rule: a total is unknown (nil) when any trace lacks it.
func summarize(turns []sessionTurnView) sessionSummary {
	summary := sessionSummary{Traces: len(turns)}
	var in, out int64
	var cost float64
	known_in, known_out, known_cost := true, true, true
	for _, turn := range turns {
		if turn.HasRoot {
			summary.Turns++
		}
		if turn.InputTokens == nil {
			known_in = false
		} else {
			in += *turn.InputTokens
		}
		if turn.OutputTokens == nil {
			known_out = false
		} else {
			out += *turn.OutputTokens
		}
		if turn.CostUSD == nil {
			known_cost = false
		} else {
			cost += *turn.CostUSD
		}
	}
	if known_in {
		summary.InputTokens = &in
	}
	if known_out {
		summary.OutputTokens = &out
	}
	if known_cost {
		summary.CostUSD = &cost
	}
	return summary
}

func (deps Deps) sessionDetail(w http.ResponseWriter, r *http.Request, sessionID string) {
	rows, err := deps.Store.ListSessionTurns(r.Context(), sessionID, sessionTextChars, sessionTurnLimit+1)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(rows) == 0 {
		http.NotFound(w, r)
		return
	}
	page := sessionPage{
		layoutData: layoutData{Title: "Session " + sessionID, Section: "sessions", SQLEnabled: deps.Cfg.AuthToken != "", Version: deps.Version},
		SessionID:  sessionID,
	}
	if len(rows) > sessionTurnLimit {
		rows, page.Truncated = rows[:sessionTurnLimit], true
	}
	number := 0
	for _, row := range rows {
		view := sessionTurnView{SessionTurn: row}
		if row.HasRoot {
			number++
			view.Number = number
		}
		page.Turns = append(page.Turns, view)
	}
	page.Summary = summarize(page.Turns)
	deps.render(w, "base", page, "templates/base.html", "templates/session.html")
}
