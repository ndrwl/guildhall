package config

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCheckTaskOverrideMergeRequires(t *testing.T) {
	cfg := overrideConfig(t, "", "")
	for _, r := range mergeRequirements {
		for _, key := range []string{"merge.requires." + r.name, "repository.merge.requires." + r.name} {
			if err := cfg.CheckTaskOverride(key, "true"); err != nil {
				t.Errorf("CheckTaskOverride(%q, true) = %v, want accepted", key, err)
			}
			if err := cfg.CheckTaskOverride(key, "false"); err == nil ||
				!strings.Contains(err.Error(), "`merge.requires."+r.name+"`: a task override may only make merge requirements stricter; it cannot turn one off") {
				t.Errorf("CheckTaskOverride(%q, false) = %v, want the stricter-only refusal", key, err)
			}
		}
	}
	// The test config's site repository has guildmaster_approval off; turning it off for a task is still refused.
	if !slices.ContainsFunc(cfg.Repositories, func(r Repository) bool { return !r.Merge.Requires.Guildmaster() }) {
		t.Fatal("the test config has no repository with guildmaster_approval off")
	}
	for _, tc := range []struct{ key, value, want string }{
		{"merge.requires.guildmaster_approval", "false", "may only make merge requirements stricter"},
		{"merge.requires.ci_green", "yes", "is not a boolean"},
		{"merge.requires.ci_green", "", "is not a boolean"},
		{"merge.requires.bogus", "true", "unknown merge requirement \"bogus\"; want one of guildmaster_approval, artisan_approval, ci_green, nothing_open"},
		{"repository.merge.requires.bogus", "true", "unknown merge requirement"},
		{"merge.reapprove_after_rebase", "never", "`[repository.merge]` is not overridable per task"},
		{"merge.minor_conflict_lines", "7", "`[repository.merge]` is not overridable per task"},
		{"minor_conflict_lines", "7", "`[repository.merge]` is not overridable per task"},
		{"requires", "true", "`[repository.merge]` is not overridable per task"},
		{"merge", "true", "`[repository.merge]` is not overridable per task"},
		{"merge.requires", "true", "`[repository.merge]` is not overridable per task"},
		{"repository.merge", "true", "`[repository.merge]` is not overridable per task"},
	} {
		err := cfg.CheckTaskOverride(tc.key, tc.value)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("CheckTaskOverride(%q, %q) = %v, want %q", tc.key, tc.value, err, tc.want)
		}
	}
}

func TestWithTaskOverridesLeavesMergeRequiresOutOfLocal(t *testing.T) {
	cfg := overrideConfig(t, "", "")
	before := maps.Clone(cfg.Local)
	got, skipped, err := cfg.WithTaskOverrides(map[string]string{
		"merge.requires.guildmaster_approval": "true",
		"repository.merge.requires.ci_green":  "false",
		DefaultEffortKey:                      "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %+v, want none: merge-requires skips are MergeRequires.WithTaskOverrides's", skipped)
	}
	for key := range got.Local {
		if _, merge := mergeRequiresOverride(key); merge {
			t.Errorf("Local has merge-requires key %q", key)
		}
	}
	if problems := got.validate(); len(problems) > 0 {
		t.Errorf("the copy is invalid: %v", problems)
	}
	if got.EffectiveEffort("") != "high" {
		t.Errorf("effective effort = %q, want the override", got.EffectiveEffort(""))
	}
	if !maps.Equal(cfg.Local, before) {
		t.Errorf("receiver Local = %v, want unchanged %v", cfg.Local, before)
	}
}

func TestMergeRequiresWithTaskOverrides(t *testing.T) {
	on, off := true, false
	allOff := MergeRequires{GuildmasterApproval: &off, ArtisanApproval: &off, CIGreen: &off, NothingOpen: &off}
	for _, tc := range []struct {
		name      string
		m         MergeRequires
		overrides map[string]string
		want      MergeRequires
		added     []string
		skipped   []string
	}{
		{name: "none", m: allOff, want: allOff},
		{name: "adds an off requirement", m: allOff,
			overrides: map[string]string{"merge.requires.guildmaster_approval": "true"},
			want:      MergeRequires{GuildmasterApproval: &on, ArtisanApproval: &off, CIGreen: &off, NothingOpen: &off},
			added:     []string{"guildmaster_approval"}},
		{name: "adds all four under either spelling, in documented order", m: allOff,
			overrides: map[string]string{"merge.requires.nothing_open": "true", "repository.merge.requires.ci_green": "true",
				"merge.requires.artisan_approval": "true", "repository.merge.requires.guildmaster_approval": "true",
				"merge.requires.ci_green": "true"},
			want:  MergeRequires{GuildmasterApproval: &on, ArtisanApproval: &on, CIGreen: &on, NothingOpen: &on},
			added: []string{"guildmaster_approval", "artisan_approval", "ci_green", "nothing_open"}},
		{name: "true on a requirement already on is a no-op",
			m:         MergeRequires{GuildmasterApproval: &on, CIGreen: &off},
			overrides: map[string]string{"merge.requires.guildmaster_approval": "true", "merge.requires.artisan_approval": "true"},
			want:      MergeRequires{GuildmasterApproval: &on, ArtisanApproval: &on, CIGreen: &off}},
		{name: "a recorded false or invalid value is skipped",
			m: MergeRequires{GuildmasterApproval: &on, CIGreen: &off},
			overrides: map[string]string{"merge.requires.guildmaster_approval": "false", "merge.requires.ci_green": "yes",
				"repository.merge.requires.bogus": "true"},
			want:    MergeRequires{GuildmasterApproval: &on, CIGreen: &off},
			skipped: []string{"merge.requires.ci_green", "merge.requires.guildmaster_approval", "repository.merge.requires.bogus"}},
		{name: "non-merge keys are ignored", m: allOff,
			overrides: map[string]string{DefaultEffortKey: "max", "merge.reapprove_after_rebase": "never", "step.build.effort": "max"},
			want:      allOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.m.Effective()
			fields := []*bool{tc.m.GuildmasterApproval, tc.m.ArtisanApproval, tc.m.CIGreen, tc.m.NothingOpen}
			got, added, skipped := tc.m.WithTaskOverrides(tc.overrides)
			if !reflect.DeepEqual(got.Effective(), tc.want.Effective()) {
				t.Errorf("requirements = %+v, want %+v", got.Effective(), tc.want.Effective())
			}
			if !slices.Equal(added, tc.added) {
				t.Errorf("added = %v, want %v", added, tc.added)
			}
			var keys []string
			for _, s := range skipped {
				keys = append(keys, s.Key)
				if s.Value != tc.overrides[s.Key] || s.Reason == "" {
					t.Errorf("skip %+v, want its value %q and a reason", s, tc.overrides[s.Key])
				}
			}
			if !slices.Equal(keys, tc.skipped) {
				t.Errorf("skipped = %v, want %v", keys, tc.skipped)
			}
			// The receiver is not mutated: neither its pointers nor what they point at.
			if !reflect.DeepEqual(tc.m.Effective(), before) ||
				!slices.Equal([]*bool{tc.m.GuildmasterApproval, tc.m.ArtisanApproval, tc.m.CIGreen, tc.m.NothingOpen}, fields) {
				t.Errorf("receiver changed to %+v, want %+v", tc.m.Effective(), before)
			}
		})
	}
	if on != true || off != false {
		t.Errorf("a shared bool changed: on %v, off %v", on, off)
	}
}

func TestMergeRequiresWithTaskOverridesReasons(t *testing.T) {
	_, _, skipped := MergeRequires{}.WithTaskOverrides(map[string]string{"merge.requires.nothing_open": "false"})
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "may only make merge requirements stricter") {
		t.Errorf("skipped = %+v, want the stricter-only reason", skipped)
	}
}
