package search

import (
	"context"
	"fmt"
	"sync"

	"multimo-backend/internal/service/keyframe"
	"multimo-backend/internal/service/pipeline"
)

// IndexKind embeds document chunks for vector search.
const IndexKind = "index"

const indexBatch = 50 // chunks per goroutine; Embed splits further into API batches

// HandleIndex computes text embeddings for every chunk of a document.
func (s *Service) HandleIndex(ctx context.Context, t *pipeline.Task, report pipeline.Reporter) error {
	rows, err := s.DB.GetAll(ctx, `SELECT id, text FROM doc_chunks WHERE asset_id = ? ORDER BY seq`, t.AssetID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	var mu sync.Mutex
	var firstErr error
	done := 0
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for start := 0; start < len(rows); start += indexBatch {
		part := rows[start:min(start+indexBatch, len(rows))]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			texts := make([]string, len(part))
			for i, r := range part {
				texts[i] = r["text"].String()
			}
			res, err := s.AI.Embed(ctx, "chunk-embedding", t.AssetID, texts)
			if err == nil {
				for i, r := range part {
					if _, err = s.DB.Exec(ctx, `UPDATE doc_chunks SET embedding = ?::vector, embedding_model = ? WHERE id = ?`,
						keyframe.VectorLiteral(res.Vectors[i]), res.Model, r["id"].Int64()); err != nil {
						break
					}
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			done += len(part)
			report(float64(done)/float64(len(rows)), fmt.Sprintf("embedded %d/%d chunks", done, len(rows)))
		}()
	}
	wg.Wait()
	return firstErr
}
