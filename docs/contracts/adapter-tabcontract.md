# `internal/adapter/tabcontract`

## Contract

The shared contract test for closing an idle tab, run against every implementation of `CloseTab`, whose contract is in [`internal/adapter`](adapter.md).
`CloseFixture` is one prepared tab: `Close` calls the implementation's `CloseTab` on the tab's reference, and `Present` reports whether the tab is still open.
`TestIdleClose` takes a setup that prepares a fixture for a named scenario and runs nine scenarios as subtests.
In `idle` the close succeeds and the tab is gone.
In `busy`, `transient-busy` and `cancelled`, the last with the context already cancelled, the close fails and the tab stays open.
In `session`, `workspace`, `tab` and `pane`, where the reference names another session, workspace, tab or pane than the one created, the close fails and the tab stays open.
In `closed` a first close succeeds, a second close of the same tab fails and the tab is gone.

Rationale: [Director tab replacement](../decisions/director-tab.md).
A test helper with no implementation of its own.

## Tests

It runs twice.
`TestHerdrIdleTabCloseContract` in `internal/adapter/director_tabs_test.go` runs it against `HerdrTabs` over the stub Herdr process.
`TestDirectorIdleTabCloseContract` in `internal/cli/director_tab_test.go` runs it against the CLI's `directorTabs` double.
