package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func processFileChange(
	ctx context.Context,
	r *runner.Runner,
	sessionSvc session.Service,
	filePath string,
	fileContent string,
) {
	// Use a fresh session per file save so old iterations don't pollute new edits
	sessionID := fmt.Sprintf("edit-%d", time.Now().UnixNano())
	userID := "local-dev"

	_, err := sessionSvc.Create(ctx, &session.CreateRequest{
		AppName:   "monorepo-coding-agent",
		UserID:    userID,
		SessionID: sessionID,
		State: map[string]any{
			"file_path":    filePath,
			"current_code": fileContent,
			"requirements": "Inspect the comments (e.g. // TODO, // FIX, // GENERATE, <!-- ... -->) and existing code in the file. Generate, fix, or debug the implementation accordingly.",
		},
	})
	if err != nil {
		log.Printf("Error creating session: %v", err)
		return
	}

	prompt := genai.NewContentFromText(
		fmt.Sprintf("File `%s` was just saved. Fulfill any instructions or fixes indicated by the comments in the code.", filePath),
		genai.RoleUser,
	)

	// Stream events from the LoopAgent
	for ev, err := range r.Run(ctx, userID, sessionID, prompt, agent.RunConfig{}) {
		if err != nil {
			log.Printf("[Agent Error] %v\n", err)
			break
		}
		if ev == nil {
			continue
		}

		// Print progress from each sub-agent
		if ev.Content != nil {
			for _, part := range ev.Content.Parts {
				if part.Text != "" {
					fmt.Printf("\n--- [%s] ---\n%s\n", ev.Author, part.Text)
				}
			}
		}
	}
}

func isRelevantSourceFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}

	// Ignore build artifacts, dependencies, and hidden git folders
	ignoredDirs := []string{"node_modules", ".git", ".svelte-kit", "dist", "build", ".pnpm-store"}
	for _, dir := range ignoredDirs {
		if strings.Contains(path, string(filepath.Separator)+dir+string(filepath.Separator)) {
			return false
		}
	}

	// Only trigger on Go, Svelte, TypeScript, JavaScript, or CSS files
	switch filepath.Ext(path) {
	case ".go", ".svelte", ".ts", ".js", ".css":
		return true
	default:
		return false
	}
}
