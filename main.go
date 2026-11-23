package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/sgtdi/fswatcher/v2"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

// Model to be used
const MODEL_NAME = "gemma-4-31b-it"

// Main Entrypoint
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// 1. Initialize Gemini model (uses GEMINI_API_KEY from environment)
	m, err := gemini.NewModel(ctx, MODEL_NAME, &genai.ClientConfig{
		APIKey: os.Getenv("GOOGLE_API_KEY"),
	})
	if err != nil {
		log.Fatalf("Failed to create Gemini model: %v", err)
	}

	// 2. Build the ADK LoopAgent workflow
	codingWorkflow, err := buildCodingWorkflow(m)
	if err != nil {
		log.Fatalf("Failed to build coding workflow: %v", err)
	}

	// 3. Configure ADK Session Service and Runner
	sessionSvc := session.InMemoryService()
	r, err := runner.New(runner.Config{
		AppName:           "monorepo-coding-agent",
		Agent:             codingWorkflow,
		SessionService:    sessionSvc,
		AutoCreateSession: true,
	})
	if err != nil {
		log.Fatalf("Failed to create ADK runner: %v", err)
	}

	// 4. Start fswatcher with a cooldown to debounce rapid editor saves
	w, err := fswatcher.New(fswatcher.WithCooldown(300 * time.Millisecond))
	if err != nil {
		log.Fatal(err)
	}

	watchErr := make(chan error, 1)
	go func() {
		watchErr <- w.Watch(ctx)
	}()

	fmt.Println("Watching workspace for file changes (Press Ctrl+C to stop)...")

	// Mutex ensures only one agent run processes a file at a time
	var runMu sync.Mutex

	for event := range w.Events() {
		// Skip directories, deletions, or irrelevant workspace paths
		if !isRelevantSourceFile(event.Path) {
			println("not relevant file path:" + event.Path)
			continue
		}

		contentBytes, err := os.ReadFile(event.Path)
		if err != nil {
			// File may have been deleted or temporarily locked during atomic save
			continue
		}

		fileContent := strings.TrimSpace(string(contentBytes))
		if fileContent == "" {
			continue
		}

		if !hasActionableComment(fileContent) {
			println("Does not contain any actionable comment.")
			continue
		}

		fmt.Printf("\n[Watcher] Detected update in %s (%v). Running coding agent...\n", event.Path, event.Types)

		// Run the agent loop synchronously per save so logs stay readable
		runMu.Lock()
		processFileChange(ctx, r, sessionSvc, event.Path, fileContent)
		runMu.Unlock()
	}

	if err := <-watchErr; err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
