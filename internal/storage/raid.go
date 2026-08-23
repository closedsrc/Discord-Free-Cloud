package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"discord-free-cloud/internal/discord"
)

type MultiGuildRouter struct {
	mu    sync.RWMutex
	nodes []BotNode
}

func NewMultiGuildRouter(nodes []BotNode) *MultiGuildRouter {
	return &MultiGuildRouter{
		nodes: nodes,
	}
}

func (r *MultiGuildRouter) UpdateNodes(nodes []BotNode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nodes = nodes
}

func (r *MultiGuildRouter) RoutePartToNode(partIndex int) (*BotNode, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.nodes) == 0 {
		return nil, errors.New("no active bot nodes configured in cluster")
	}

	target := r.nodes[partIndex%len(r.nodes)]
	return &target, nil
}

type StripeTask struct {
	PartIndex int
	Data      []byte
	Node      BotNode
}

type StripeResult struct {
	PartIndex int
	ChannelID string
	MessageID string
	URL       string
	Error     error
}

func (r *MultiGuildRouter) ParallelStripe(ctx context.Context, tasks []StripeTask, uploadFn func(ctx context.Context, task StripeTask) (*discord.UploadChunkResult, error)) ([]StripeResult, error) {
	results := make([]StripeResult, len(tasks))
	var wg sync.WaitGroup
	var hasError int32 = 0

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t StripeTask) {
			defer wg.Done()

			res, err := uploadFn(ctx, t)
			if err != nil {
				atomic.StoreInt32(&hasError, 1)
				results[idx] = StripeResult{
					PartIndex: t.PartIndex,
					Error:     err,
				}
				return
			}

			results[idx] = StripeResult{
				PartIndex: t.PartIndex,
				ChannelID: res.ChannelID,
				MessageID: res.MessageID,
				URL:       res.AttachmentURL,
			}
		}(i, task)
	}

	wg.Wait()

	if atomic.LoadInt32(&hasError) == 1 {
		return results, fmt.Errorf("one or more stripe uploads failed")
	}

	return results, nil
}
