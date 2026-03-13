---
name: spec
description: Create a new feature or bug spec document in the spec/ directory
argument-hint: "<feature|bug> <short description>"
allowed-tools:
  - read
  - edit
  - grep
  - glob
  - exec
permissions:
  allow:
    - Write(spec/**)
    - Read(spec/**)
    - Exec(ls)
    - Exec(find)
---

Create a new specification document based on the user's request.

## Arguments

The first argument (`$1`) is the spec type: either `feature` or `bug`.
The remaining arguments (`$ARGUMENTS`) contain the type followed by a short description.

## Steps

1. **Determine the spec type** from the first argument. It must be either `feature` or `bug`. If missing or invalid, ask the user to specify.

2. **Find the next sequence number** by listing existing files in the appropriate directory (`spec/features/` or `spec/bugs/`). Extract the highest `NNNN` prefix and increment by 1. If no files exist yet, start at `0001`.

3. **Generate the filename** using the pattern `NNNN-short-description.md` where the description is derived from the user's input, lowercased, with spaces replaced by hyphens and special characters removed.

4. **Write the spec document** using the appropriate template below.

## Feature Spec Template

```markdown
# Feature: <Title>

**Spec ID:** NNNN
**Status:** Draft
**Author:**
**Created:** <today's date YYYY-MM-DD>

## Summary

A brief one-paragraph summary of the feature.

## Motivation

Why is this feature needed? What problem does it solve?

## Detailed Design

### Overview

Describe the high-level approach.

### API / Configuration Changes

Describe any new annotations, CLI flags, Helm values, or API changes.

### Implementation Details

Describe how this will be implemented within the controller reconciliation loop, Pangolin API client, or other components.

### Error Handling

How should errors be handled? What failure modes exist?

## Alternatives Considered

What other approaches were evaluated and why were they rejected?

## Testing Strategy

How will this feature be tested? Include unit tests, integration tests, and manual verification steps.

## Rollout Plan

How will this be rolled out? Any feature flags, migration steps, or backwards compatibility concerns?

## Open Questions

- List any unresolved questions here
```

## Bug Spec Template

```markdown
# Bug: <Title>

**Spec ID:** NNNN
**Status:** Draft
**Author:**
**Created:** <today's date YYYY-MM-DD>
**Severity:** <Critical | High | Medium | Low>

## Summary

A brief one-paragraph summary of the bug.

## Reproduction Steps

1. Step-by-step instructions to reproduce the bug
2. Include relevant configuration, annotations, or manifests

## Expected Behavior

What should happen.

## Actual Behavior

What actually happens. Include error messages, logs, or screenshots if available.

## Root Cause Analysis

Describe the identified or suspected root cause within the codebase.

## Proposed Fix

### Overview

Describe the high-level fix.

### Implementation Details

Describe the specific code changes needed.

### Affected Components

List files and components that need to change.

## Testing Strategy

How will the fix be verified? Include regression test plans.

## Open Questions

- List any unresolved questions here
```

## Important

- Always place feature specs in `spec/features/` and bug specs in `spec/bugs/`.
- Use today's date for the Created field.
- Leave the Author field blank for the user to fill in.
- Set Status to `Draft`.
- Fill in the Title from the user's description.
- Fill in the Summary section with a brief description based on the user's input. Leave all other sections with their placeholder text for the user to complete.
