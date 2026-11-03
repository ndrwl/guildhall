package web

import (
	"html/template"
	"strconv"
	"strings"
)

// listGroups are the task list's open groups in the order it shows them, each with its heading.
var listGroups = []struct {
	Group Group
	Title string
}{
	{GroupYou, "Waiting for you"},
	{GroupMoving, "Moving"},
	{GroupOther, "Waiting on something else"},
}

// listView is the task list's model, every value formatted for the templates.
type listView struct {
	Style template.CSS
	// Count is the band's count line, such as "3 tasks wait for you · 5 are moving · 3 wait on something else".
	Count string
	// You says a task waits on the Guildmaster, so the count line is amber.
	You bool
	// Groups are the open groups that hold a task, and Finished the tasks finished in the last 7 days.
	Groups   []listGroup
	Finished []listRow
	ReadAt   string
}

type listGroup struct {
	Class, Title string
	Rows         []listRow
}

// listRow is one task's row: its name, linked to its page, its title, where it stands and since when.
type listRow struct {
	Name, Href, Title string
	Where             whereView
	Since             string
}

func (s *Server) listView(l TaskList) listView {
	v := listView{Style: style, ReadAt: s.clock(l.ReadAt)}
	multi := false
	for _, t := range l.Tasks {
		multi = multi || t.Repository != l.Tasks[0].Repository
	}
	byGroup := map[Group][]listRow{}
	for _, t := range l.Tasks {
		row := listRow{Name: t.ID, Href: taskHref(t.Repository, t.ID), Title: t.Title, Where: whereView{Lead: t.Where.Lead, Rest: t.Where.Rest, You: t.Where.You}}
		if multi {
			row.Name = t.Repository + "/" + t.ID
		}
		if row.Where.Lead == "" && t.Stands != "" {
			row.Where.Lead = upperFirst(t.Stands) + "."
		}
		if t.Since != nil {
			word := "since "
			if t.Group == GroupFinished {
				word = "closed "
			}
			row.Since = word + s.when(*t.Since)
		}
		g := t.Group
		if g != GroupYou && g != GroupMoving && g != GroupFinished {
			g = GroupOther
		}
		byGroup[g] = append(byGroup[g], row)
	}
	counts := make([]int, len(listGroups))
	for i, g := range listGroups {
		counts[i] = len(byGroup[g.Group])
		if counts[i] > 0 {
			v.Groups = append(v.Groups, listGroup{Class: string(g.Group), Title: g.Title, Rows: byGroup[g.Group]})
		}
	}
	v.Finished = byGroup[GroupFinished]
	v.Count, v.You = countLine(counts[0], counts[1], counts[2]), counts[0] > 0
	return v
}

// countLine is the task list's count line: how many tasks wait for the Guildmaster, move and wait on something
// else, the word "task" said once, on the first count; "No open tasks." when there are none.
func countLine(you, moving, other int) string {
	if you+moving+other == 0 {
		return "No open tasks."
	}
	var parts []string
	named := false
	add := func(n int, one, many string) {
		noun := ""
		if !named {
			noun, named = " tasks", true
			if n == 1 {
				noun = " task"
			}
		}
		verb := many
		if n == 1 {
			verb = one
		}
		parts = append(parts, strconv.Itoa(n)+noun+" "+verb)
	}
	if you > 0 {
		add(you, "waits for you", "wait for you")
	} else {
		parts = append(parts, "Nothing waits for you")
	}
	if moving > 0 {
		add(moving, "is moving", "are moving")
	}
	if other > 0 {
		add(other, "waits on something else", "wait on something else")
	}
	return strings.Join(parts, " · ")
}
