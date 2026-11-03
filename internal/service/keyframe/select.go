package keyframe

import (
	"context"

	"multimo-backend/internal/pkg/media"
)

// Pick is a chosen keyframe time and why it was chosen.
type Pick struct {
	Time   float64 `json:"time"`
	Change float64 `json:"change"` // difference from the previous keyframe
	Reason string  `json:"reason"` // first | change | coverage
}

type Params struct {
	Threshold float64 // content change that starts a new keyframe
	MinGap    float64 // seconds; never closer than this
	MaxGap    float64 // seconds; never further apart than this
	MaxN      int
}

// Select walks the signatures in time order and keeps a frame when it differs
// from the last kept frame by more than Threshold (at least MinGap later), or
// when MaxGap has passed without one. If more than MaxN are kept, they are
// thinned evenly so coverage of the whole video is preserved.
func Select(sigs []media.Signature, p Params) []Pick {
	if len(sigs) == 0 {
		return nil
	}
	picks := []Pick{{Time: sigs[0].Time, Reason: "first"}}
	last := sigs[0]
	for _, s := range sigs[1:] {
		gap := s.Time - last.Time
		if gap < p.MinGap {
			continue
		}
		c := media.Change(s.Pix, last.Pix)
		switch {
		case c > p.Threshold:
			picks = append(picks, Pick{Time: s.Time, Change: c, Reason: "change"})
		case p.MaxGap > 0 && gap >= p.MaxGap:
			picks = append(picks, Pick{Time: s.Time, Change: c, Reason: "coverage"})
		default:
			continue
		}
		last = s
	}
	if p.MaxN > 0 && len(picks) > p.MaxN {
		thinned := make([]Pick, 0, p.MaxN)
		step := float64(len(picks)-1) / float64(p.MaxN-1)
		for i := 0; i < p.MaxN; i++ {
			thinned = append(thinned, picks[int(float64(i)*step+0.5)])
		}
		picks = thinned
	}
	return picks
}

// Signatures extracts change-detection signatures at 2 fps. Long videos are
// decoded key-frames-only; if that leaves too few distinct frames (sparse
// key frames), the full stream is decoded instead.
func Signatures(ctx context.Context, path string, threads int) ([]media.Signature, error) {
	const fps = 2
	p, err := media.Probe(ctx, path)
	if err != nil {
		return nil, err
	}
	if p.Duration > 60 {
		sigs, err := media.FrameSignatures(ctx, path, fps, threads, true)
		if err == nil && float64(media.Distinct(sigs)) >= p.Duration/3 {
			return sigs, nil
		}
	}
	return media.FrameSignatures(ctx, path, fps, threads, false)
}
