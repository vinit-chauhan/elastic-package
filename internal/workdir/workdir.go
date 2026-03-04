// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

package workdir

import (
	"context"
	"os"
)

type workDirKey struct{}

// WithDir returns a new context with the given working directory stored in it.
// This allows goroutines to operate with a virtual working directory instead
// of relying on the process-global os.Chdir.
func WithDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, workDirKey{}, dir)
}

// DirFromContext returns the working directory stored in the context, if any.
func DirFromContext(ctx context.Context) (string, bool) {
	dir, ok := ctx.Value(workDirKey{}).(string)
	return dir, ok && dir != ""
}

// Dir returns the working directory from the context if set, otherwise
// falls back to os.Getwd().
func Dir(ctx context.Context) (string, error) {
	if dir, ok := DirFromContext(ctx); ok {
		return dir, nil
	}
	return os.Getwd()
}
