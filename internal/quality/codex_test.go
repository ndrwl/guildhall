package quality_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/quality"
	"github.com/ndrwl/guildhall/internal/render"
)

// TestCheckAnswer: the judge's Codex answer is checked against the self-review beside its prompt, a null score or
// pass, as the strict schema has a criterion carry, reading as absent; a refused answer, a missing self-review and a
// schema that is not JSON are each refused.
func TestCheckAnswer(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, render.InputsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, render.InputsDir, "self-review.json"), []byte(selfReview), 0o600); err != nil {
		t.Fatal(err)
	}
	good := `{"merge_as_is": "no", "criteria": [
		{"id": "B2", "score": 2, "pass": null, "evidence": "a.go:1"},
		{"id": "D1", "score": 1, "pass": null, "evidence": "b.go:2"},
		{"id": "G2", "score": null, "pass": true, "evidence": "no assertion removed"}]}`
	if err := quality.CheckAnswer(dir, []byte(good)); err != nil {
		t.Errorf("CheckAnswer(good) = %v", err)
	}
	if err := quality.CheckAnswer(dir, []byte(`{"merge_as_is": "no", "criteria": []}`)); !errors.Is(err, quality.ErrAnswer) {
		t.Errorf("CheckAnswer(nothing judged) = %v, want ErrAnswer", err)
	}
	if err := quality.CheckAnswer(t.TempDir(), []byte(good)); !errors.Is(err, quality.ErrSelfReview) {
		t.Errorf("CheckAnswer without a self-review = %v, want ErrSelfReview", err)
	}
	if !json.Valid(quality.CodexAnswer.Schema) || quality.CodexAnswer.File != quality.AnswerFile || quality.CodexAnswer.Check == nil {
		t.Errorf("CodexAnswer = %+v", quality.CodexAnswer)
	}
}
