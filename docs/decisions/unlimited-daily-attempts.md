# `max_attempts_per_day = "0"` is no daily cap

The Guildmaster wanted to turn off the daily attempt cap, and was setting `max_attempts_per_day = "10000"` as a stand-in because the [guard rails](0074-guard-rails.md) refused anything below 1.
`"0"` now means no daily cap, managed or local; the default stays 40 when the key is unset.

The store and engine already treated a zero `AttemptCaps.MaxPerDay` as no cap, so only config validation changed.
`Parse` and `GuardRails` share one rule, so they cannot disagree: `max_attempts_per_day` accepts a whole number written plainly, `0` included, and refuses `"-1"`, `"00"`, `"+0"` and `" 0"`, naming the key and that 0 is no daily cap.
`max_running_attempts` still needs at least 1: a running cap of 0 would refuse every attempt, and the Guildmaster asked only for the daily cap to be optional.

With no daily cap, `director status` still counts `attempts_today` and `spend_today`, and `guard_rails.max_attempts_per_day` shows 0.

The config tests were red before the change and green after it ([evidence](../evidence/unlimited-daily-attempts-2026-10-27-red.txt)); the end-to-end interpreter test, more than 40 starts in a day with no refusal, passed before and after, because the engine needed no change.
