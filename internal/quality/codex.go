package quality

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ndrwl/guildhall/internal/adapter/codex"
	"github.com/ndrwl/guildhall/internal/render"
)

// answerSchema is the judge's answer as a strict JSON Schema, the form Codex's --output-schema takes: every field
// required and none other, a criterion's score or pass null where its kind has none. ParseAnswer still decides.
const answerSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["merge_as_is", "criteria"],
  "properties": {
    "merge_as_is": {"type": "string", "enum": ["yes", "no"]},
    "criteria": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "score", "pass", "evidence"],
        "properties": {
          "id": {"type": "string"},
          "score": {"type": ["integer", "null"]},
          "pass": {"type": ["boolean", "null"]},
          "evidence": {"type": "string"}
        }
      }
    }
  }
}`

// CodexAnswer is the judge step's Codex answer contract (codex.Answer): the read-only judge returns its
// judgement.json in its final message, and the wrapper writes it as AnswerFile once CheckAnswer accepts it.
var CodexAnswer = codex.Answer{File: AnswerFile, Schema: json.RawMessage(answerSchema), Check: CheckAnswer}

// CheckAnswer checks a judge's answer, data, with ParseAnswer against the pending criteria of the self-review the
// judge was given: inputs/self-review.json beside its prompt, in promptDir.
func CheckAnswer(promptDir string, data []byte) error {
	given, err := os.ReadFile(filepath.Join(promptDir, render.InputsDir, "self-review.json"))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSelfReview, err)
	}
	sr, err := ReadSelfReview(given)
	if err != nil {
		return err
	}
	_, err = ParseAnswer(data, sr.Pending)
	return err
}
