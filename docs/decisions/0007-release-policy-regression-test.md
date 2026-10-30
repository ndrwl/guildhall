# D7. Release policy regression test

The focused release policy test uses Python 3 and PyYAML 6.0.1 to parse the workflow into a semantic model rather than matching YAML source text.
Run it with `python3 .github/tests/test_release.py` in an environment with PyYAML available.
CI runs it in the `test (ubuntu-latest)` leg on Python 3.12 with PyYAML 6.0.1 ([CI action pins and race](ci-action-pins-and-race.md)).
It evaluates the publication condition's conjunction of equality predicates for pushed tags, manual tags, manual branches and pushed branches, and checks that manual builds retain their artifact-upload path.
These are development-only test dependencies; the binary remains standard-library-only.
