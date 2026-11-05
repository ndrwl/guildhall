package cli

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

// This file is the task pages' Answerer for a plan approval's open questions (docs/web.md#plan-open-questions): the
// Guildmaster answers them one at a time inside the approval, as drafts under its decision, and the approval's answer
// writes them into its one answer through answerDecision.

// pageBoxes is the words of a form's question boxes as drafts hold them: each read as an answer file is, and a box
// of only blanks made empty.
func pageBoxes(boxes map[int]string) (map[int]string, error) {
	out := map[int]string{}
	for n, text := range boxes {
		t, err := normaliseText(fmt.Sprintf("your words on Q%d", n), []byte(text), store.MaxTextBytes)
		if err != nil {
			return nil, web.Refuse(http.StatusRequestEntityTooLarge, err.Error())
		}
		if strings.TrimSpace(t) == "" {
			t = ""
		}
		out[n] = t
	}
	return out, nil
}

// planOpen refuses, with the page's reasons, unless decision is still open on an open task and bound to subject, so
// its open questions still take drafts.
func planOpen(s *store.Store, repo, task, decision, subject string) error {
	t, err := s.Task(repo, task)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return web.Refuse(http.StatusNotFound, "there is no task "+repo+"/"+task)
	case err != nil:
		return err
	case t.State != store.TaskOpen:
		return web.Refuse(http.StatusConflict, "this task is closed; its open questions take nothing new")
	}
	dec, err := findDecision(s, repo, task, decision)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return web.Refuse(http.StatusNotFound, "this task has no decision #"+decision)
	case err != nil:
		return err
	case dec.Void:
		return web.Refuse(http.StatusConflict, "changed since you loaded the page: #"+decision+" is void: "+oneLine(dec.VoidReason))
	case dec.Answered:
		return web.Refuse(http.StatusConflict, "already answered: "+webAnswered(dec))
	case dec.Subject == nil || dec.Subject.Digest != subject:
		return web.Refuse(http.StatusConflict, "changed since you loaded the page: #"+decision+" is about another version now; reload and read it again")
	}
	return nil
}

// planNumbers are the numbers of a plan approval's open questions.
func planNumbers(plan []web.PlanQuestion) []int {
	ns := make([]int, len(plan))
	for i, q := range plan {
		ns[i] = q.N
	}
	return ns
}

// pagePlanPress does one press of a plan approval's question controls in s: every box saved as a draft under the
// decision, with the button's mark on its question.
func pagePlanPress(s *store.Store, p web.RoundPost, boxes map[int]string) error {
	if err := planOpen(s, p.Repository, p.TaskID, p.Tag, p.Subject); err != nil {
		return err
	}
	ns := planNumbers(p.Plan)
	if p.Question > 0 && !slices.Contains(ns, p.Question) {
		return web.Refuse(http.StatusBadRequest, fmt.Sprintf("#%s has no open question Q%d", p.Tag, p.Question))
	}
	_, err := savePageDrafts(s, p, web.PlanRound, ns, boxes, nil)
	return err
}

// webPlanAnswer is answer, a plan approval's answer x gives, with its open questions' answers after it
// (web.PlanAnswersText): x's boxes and changed picks, each pick its question's answer, are saved as the decision's
// drafts first, while it is still open, and the answered
// questions read from them, so an answer replayed reads the same drafts. With no question answered the answer is
// unchanged. An answer over the limit is refused and the drafts are kept.
func webPlanAnswer(s *store.Store, x web.Answer, answer string) (string, error) {
	if len(x.Plan) == 0 {
		return answer, nil
	}
	ns := planNumbers(x.Plan)
	if planOpen(s, x.Repository, x.TaskID, x.Decision, x.Subject) == nil {
		boxes, err := pageBoxes(x.Boxes)
		if err != nil {
			return "", err
		}
		p := web.RoundPost{Repository: x.Repository, TaskID: x.TaskID, Tag: x.Decision, Act: web.RoundSave, Login: x.Login, Picks: x.Picks}
		if _, err := savePageDrafts(s, p, web.PlanRound, ns, boxes, nil); err != nil {
			return "", err
		}
	}
	drafts, err := s.Drafts(x.Repository, x.TaskID)
	if err != nil {
		return "", err
	}
	answers := map[int]web.RoundAnswer{}
	for _, d := range drafts {
		if d.Tag != x.Decision || d.Round != web.PlanRound || !slices.Contains(ns, d.Question) {
			continue
		}
		switch d.Mark {
		case store.DraftAccept:
			answers[d.Question] = web.RoundAnswer{Accepted: true, Words: d.Text}
		case store.DraftWords:
			answers[d.Question] = web.RoundAnswer{Words: d.Text}
		case store.DraftPick:
			answers[d.Question] = web.RoundAnswer{Pick: d.Pick, Words: d.Text}
		}
	}
	list := web.PlanAnswersText(x.Plan, answers)
	if list == "" {
		return answer, nil
	}
	answer += "\n\n" + list
	if len(answer) > store.MaxTextBytes {
		return "", web.Refuse(http.StatusRequestEntityTooLarge, fmt.Sprintf("your answer with the open questions' answers is over the limit of %d bytes (64 KiB); shorten some and answer again. Your words are saved", store.MaxTextBytes))
	}
	return answer, nil
}
