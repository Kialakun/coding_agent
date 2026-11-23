package main

import (
	"iter"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/agent/workflowagents/loopagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

func hasActionableComment(content string) bool {
	triggers := []string{
		"// TODO",
		"// FIX",
		"// DEBUG",
		"// GENERATE",
		"// @ai",
		"<!-- TODO",
		"<!-- FIX",
		"<!-- @ai",
		"<!-- GENERATE",
	}
	upper := strings.ToUpper(content)
	for _, t := range triggers {
		if strings.Contains(upper, strings.ToUpper(t)) {
			return true
		}
	}
	return false
}

func buildCodingWorkflow(m model.LLM) (agent.Agent, error) {
	codeRefiner, err := llmagent.New(llmagent.Config{
		Name: "CodeRefiner",
		Instruction: `You are an expert full-stack developer working in a pnpm workspace monorepo (Go backend + SvelteKit 5 + shadcn-svelte frontend).

Target File: {file_path}

Task:
{requirements}

Current File Content:
{current_code}

Review Feedback from Previous Iteration (if any):
{review_feedback?}

Strict Stack & Architecture Rules:
1. Comment-Driven Action: Read inline comments (// TODO, // FIX, // DEBUG, // GENERATE, <!-- ... -->) inside the file to determine whether to generate new code, fix bugs, or refactor. Remove or update resolved instruction comments in your final code.
2. Monorepo Layout: Respect 'apps/' and 'packages/' boundaries (e.g., shared UI components in 'packages/ui', shared types in 'packages/types').
3. Package Management: Use 'pnpm' and 'workspace:*' protocol for internal workspace packages. Never use npm or yarn.
4. Frontend (Svelte 5 & shadcn-svelte): Use Svelte 5 runes ($state, $derived, $effect, $props) exclusively. Import shadcn-svelte components from the shared UI workspace package rather than duplicating them.
5. Backend (Go): Write idiomatic Go with explicit error handling.
6. Output the complete, updated file content cleanly so it is ready to replace the file.`,
		OutputKey: "current_code",
		Model:     m,
	})
	if err != nil {
		return nil, err
	}

	qualityChecker, err := llmagent.New(llmagent.Config{
		Name: "QualityChecker",
		Instruction: `Audit the updated code for {file_path} against the comments and stack rules.

Updated Code:
{current_code}

Checklist:
- Were all inline comment instructions (TODO, FIX, DEBUG, GENERATE) addressed?
- Are pnpm workspace conventions ('workspace:*', 'apps/' vs 'packages/') respected?
- Does Svelte code strictly use Svelte 5 runes ($state, $derived, $props) without Svelte 4 syntax?
- Is Go code idiomatic and free of unhandled errors?

Set status to "pass" ONLY if all checks succeed. Otherwise set status to "fail" and explain exact fixes in feedback.`,
		OutputKey: "quality_review",
		OutputSchema: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"status": {
					Type:        genai.TypeString,
					Enum:        []string{"pass", "fail"},
					Description: "Whether the code passes all checks.",
				},
				"feedback": {
					Type:        genai.TypeString,
					Description: "Actionable fixes required if status is fail.",
				},
			},
			Required: []string{"status", "feedback"},
		},
		Model: m,
	})
	if err != nil {
		return nil, err
	}

	checkStatusAndEscalate, err := agent.New(agent.Config{
		Name: "StopChecker",
		Run: func(ctx agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(yield func(*session.Event, error) bool) {
				rawReview, err := ctx.Session().State().Get("quality_review")
				shouldStop := false

				if err == nil {
					if reviewMap, ok := rawReview.(map[string]any); ok {
						if status, ok := reviewMap["status"].(string); ok {
							shouldStop = strings.EqualFold(strings.TrimSpace(status), "pass")
						}
						if fb, ok := reviewMap["feedback"].(string); ok {
							_ = ctx.Session().State().Set("review_feedback", fb)
						}
					}
				}

				ev := session.NewEvent(ctx, ctx.InvocationID())
				ev.Author = "StopChecker"
				ev.Actions = session.EventActions{
					Escalate: shouldStop,
				}
				yield(ev, nil)
			}
		},
	})
	if err != nil {
		return nil, err
	}

	return loopagent.New(loopagent.Config{
		MaxIterations: 5,
		AgentConfig: agent.Config{
			Name: "MonorepoCodingLoop",
			SubAgents: []agent.Agent{
				codeRefiner,
				qualityChecker,
				checkStatusAndEscalate,
			},
		},
	})
}
