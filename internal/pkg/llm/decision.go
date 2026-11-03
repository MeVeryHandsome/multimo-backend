package llm

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// DecisionQuestion is one question for the StartLux decision model.
//
//   - Type "choice": Criteria is map[string]string (option id -> description)
//     or []string; the answer is the most likely option with probabilities.
//   - Type "score": Criteria is []string with 2..10 levels, lowest first; the
//     answer is the probability-weighted level.
type DecisionQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria"`
}

type DecisionAnswer struct {
	Type            string             `json:"type"`
	Choice          string             `json:"choice,omitempty"`
	Confidence      float64            `json:"confidence"`
	Probabilities   map[string]float64 `json:"probabilities"`
	NoneProbability float64            `json:"none_probability,omitempty"`
	Score           float64            `json:"score,omitempty"`
	Legend          map[string]string  `json:"legend,omitempty"`
}

type DecisionRequest struct {
	Purpose   string
	AssetID   int64
	State     string
	Questions map[string]DecisionQuestion
	// Fallback overrides the default keyword-overlap rules. Optional.
	Fallback func() (map[string]DecisionAnswer, error)
}

type DecisionResponse struct {
	Answers  map[string]DecisionAnswer
	Model    string
	Usage    Usage
	Fallback bool
}

type decisionWire struct {
	Answers map[string]DecisionAnswer `json:"answers"`
	Model   string                    `json:"model"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error string `json:"error"`
}

// Decide asks the decision model. All questions share the same state.
func (g *Gateway) Decide(ctx context.Context, req DecisionRequest) (*DecisionResponse, error) {
	if len(req.Questions) == 0 {
		return nil, fmt.Errorf("decide: no questions")
	}
	body := map[string]any{"model": g.dec.model, "state": req.State, "questions": req.Questions}
	fallback := req.Fallback
	if fallback == nil {
		fallback = func() (map[string]DecisionAnswer, error) { return RuleDecide(req.State, req.Questions), nil }
	}
	resp, _, err := run(ctx, g, g.dec, callMeta{purpose: req.Purpose, assetID: req.AssetID, input: summarizeDecision(req)},
		func(ctx context.Context) (attemptResult[*DecisionResponse], error) {
			var w decisionWire
			if err := g.postJSON(ctx, g.dec, body, &w); err != nil {
				return attemptResult[*DecisionResponse]{}, err
			}
			for id := range req.Questions {
				if _, ok := w.Answers[id]; !ok {
					return attemptResult[*DecisionResponse]{}, fmt.Errorf("%w: missing answer %q", errBadResponse, id)
				}
			}
			u := Usage{PromptTokens: w.Usage.InputTokens, CompletionTokens: w.Usage.OutputTokens,
				TotalTokens: w.Usage.InputTokens + w.Usage.OutputTokens}
			return attemptResult[*DecisionResponse]{value: &DecisionResponse{Answers: w.Answers, Model: w.Model, Usage: u},
				usage: u, output: summarizeAnswers(w.Answers)}, nil
		},
		func() (*DecisionResponse, string, error) {
			a, err := fallback()
			if err != nil {
				return nil, "", err
			}
			return &DecisionResponse{Answers: a, Model: "local-rule", Fallback: true}, summarizeAnswers(a), nil
		})
	return resp, err
}

// RuleDecide is the local fallback: each option or score is judged by how many
// of its tokens appear in the state. Crude, but deterministic and offline.
func RuleDecide(state string, qs map[string]DecisionQuestion) map[string]DecisionAnswer {
	stateToks := map[string]bool{}
	for _, t := range Tokenize(state) {
		stateToks[t] = true
	}
	overlap := func(text string) float64 {
		toks := Tokenize(text)
		if len(toks) == 0 {
			return 0
		}
		hit := 0
		for _, t := range toks {
			if stateToks[t] {
				hit++
			}
		}
		return float64(hit) / float64(len(toks))
	}

	out := make(map[string]DecisionAnswer, len(qs))
	for id, q := range qs {
		switch q.Type {
		case "choice":
			opts := criteriaOptions(q.Criteria)
			scores := map[string]float64{}
			for _, o := range opts {
				scores[o.id] = overlap(o.text + " " + q.Instructions)
			}
			probs := softmax(scores, 4)
			best := ""
			for _, o := range opts {
				if best == "" || probs[o.id] > probs[best] {
					best = o.id
				}
			}
			out[id] = DecisionAnswer{Type: "choice", Choice: best, Confidence: probs[best], Probabilities: probs}
		case "score":
			levels := criteriaOptions(q.Criteria)
			o := overlap(q.Instructions)
			lvl := o * float64(len(levels)-1)
			probs := map[string]float64{}
			legend := map[string]string{}
			for i, l := range levels {
				d := math.Abs(float64(i) - lvl)
				probs[strconv.Itoa(i)] = math.Max(0, 1-d)
				legend[strconv.Itoa(i)] = l.text
			}
			out[id] = DecisionAnswer{Type: "score", Score: lvl, Probabilities: normalize(probs), Legend: legend}
		}
	}
	return out
}

type option struct{ id, text string }

func criteriaOptions(c any) []option {
	var opts []option
	switch v := c.(type) {
	case map[string]string:
		for k, t := range v {
			opts = append(opts, option{k, t})
		}
		sort.Slice(opts, func(i, j int) bool { return opts[i].id < opts[j].id })
	case []string:
		for i, t := range v {
			opts = append(opts, option{strconv.Itoa(i), t})
		}
	}
	return opts
}

func softmax(scores map[string]float64, temp float64) map[string]float64 {
	out := map[string]float64{}
	var sum float64
	for k, s := range scores {
		out[k] = math.Exp(s * temp)
		sum += out[k]
	}
	for k := range out {
		out[k] /= sum
	}
	return out
}

func normalize(p map[string]float64) map[string]float64 {
	var sum float64
	for _, v := range p {
		sum += v
	}
	if sum == 0 {
		return p
	}
	for k := range p {
		p[k] /= sum
	}
	return p
}

func summarizeDecision(req DecisionRequest) string {
	ids := make([]string, 0, len(req.Questions))
	for id, q := range req.Questions {
		ids = append(ids, id+":"+q.Type)
	}
	sort.Strings(ids)
	return fmt.Sprintf("[questions %s] %s", strings.Join(ids, ","), req.State)
}

func summarizeAnswers(a map[string]DecisionAnswer) string {
	ids := make([]string, 0, len(a))
	for id := range a {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		x := a[id]
		if x.Type == "score" {
			parts = append(parts, fmt.Sprintf("%s=score %.2f", id, x.Score))
		} else {
			parts = append(parts, fmt.Sprintf("%s=%s (%.2f)", id, x.Choice, x.Confidence))
		}
	}
	return strings.Join(parts, "; ")
}
