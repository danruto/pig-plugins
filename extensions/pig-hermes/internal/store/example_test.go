package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/config"
	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/paths"
	"github.com/danruto/pig-plugins/extensions/pig-hermes/internal/store"
)

func ExampleMemory() {
	root, err := os.MkdirTemp("", "hermes-memory-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)
	ctx := context.Background()
	knowledge := filepath.Join(root, "knowledge")
	db, err := store.Open(ctx, filepath.Join(knowledge, "sessions.db"))
	if err != nil {
		panic(err)
	}
	defer db.Close()
	settings, err := config.Parse([]byte(`{}`))
	if err != nil {
		panic(err)
	}
	memory, err := store.NewMemory(db, settings, paths.Resolved{Knowledge: knowledge, Projects: filepath.Join(root, "projects")})
	if err != nil {
		panic(err)
	}
	scope := store.Scope{Target: "memory"}
	added, err := memory.Add(ctx, scope, store.Mutation{Content: "Prefer 日本語 documentation"})
	if err != nil {
		panic(err)
	}
	fmt.Println(added.Success, added.DocumentSaved, added.SearchReconciled)
	replaced, err := memory.Replace(ctx, scope, store.Mutation{OldText: "日本語", Content: "Prefer concise 日本語 documentation"})
	if err != nil {
		panic(err)
	}
	fmt.Println(replaced.Success)
	planned, err := memory.Apply(ctx, scope, store.MutationPlan{Operations: []store.Mutation{{Action: "add", Content: "Keep examples runnable"}, {Action: "remove", OldText: "concise"}}})
	if err != nil {
		panic(err)
	}
	fmt.Println(planned.Success, len(planned.Entries))
	removed, err := memory.Remove(ctx, scope, store.Mutation{OldText: "runnable"})
	if err != nil {
		panic(err)
	}
	entries, err := memory.Reload(ctx, scope)
	if err != nil {
		panic(err)
	}
	fmt.Println(removed.Success, len(entries))
	// Output:
	// true true true
	// true
	// true 1
	// true 0
}
