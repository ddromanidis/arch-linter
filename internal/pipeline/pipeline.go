package pipeline

import "context"

// Step defines a pure transformation: (Context, In) -> (Out, Error)
type Step[T any] func(context.Context, T) (T, error)

// Run executes steps sequentially passing state by value.
func Run[T any](ctx context.Context, initial T, steps ...Step[T]) (T, error) {
	current := initial
	var err error

	for _, step := range steps {
		if ctx.Err() != nil {
			return current, ctx.Err()
		}

		current, err = step(ctx, current)
		if err != nil {
			return current, err
		}
	}
	return current, nil
}
