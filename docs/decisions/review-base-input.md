# Expose the recorded review base in the prompt

Code-review preparation passes the recorded binding's range base to `render.Stage.ReviewBase`, and the renderer names it in the prompt alongside the attempt identity.
This uses the existing immutable binding rather than introducing a workflow port or a second document containing the same commit.
It covers full, requested-full and rebase-only rounds without changing review outcome schemas.
A missing range is rendered explicitly as unavailable, and the review skill reports a blocker instead of guessing from the default branch.
Published preparations remain unchanged on replay, including preparations made before this field existed.
