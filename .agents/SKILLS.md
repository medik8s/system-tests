# SKILLS.md

Behavioral guidelines for all coding agents in this repository. Derived from [Andrej Karpathy's observations on LLM coding pitfalls](https://github.com/forrestchang/andrej-karpathy-skills/blob/main/skills/karpathy-guidelines/SKILL.md).

**Tradeoff:** These guidelines bias toward caution over speed. Use judgment on trivial tasks.

## 1. Think Before Coding

Before implementing: state your assumptions explicitly. If multiple interpretations exist, present them — don't pick silently. If a simpler approach exists, say so. If something is unclear, stop, name what's confusing, and ask.

## 2. Simplicity First

Write the minimum code that solves the problem. No features beyond what was asked, no abstractions for single-use code, no "flexibility" that wasn't requested, no error handling for impossible scenarios. If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Surgical Changes

Touch only what the request requires. Don't improve adjacent code, comments, or formatting. Match existing style, even if you'd do it differently. If you notice unrelated dead code, mention it — don't delete it.

When your changes create orphans: remove imports/variables/functions that *your* changes made unused. Don't remove pre-existing dead code unless asked.

Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

Transform tasks into verifiable goals before starting:
- "Fix the bug" → write a test that reproduces it, then make it pass
- "Add validation" → write tests for invalid inputs, then make them pass
- "Refactor X" → ensure tests pass before and after

For multi-step tasks, state a brief plan with verification checkpoints.
