# Codex Hooks

## Overview

Add an experimental, opt-in Codex hooks integration to Gas Town so Codex sessions can use native `SessionStart` and `Stop` lifecycle hooks when the upstream Codex hooks feature is enabled.

This work exists to improve the Codex startup and shutdown path without regressing current users. Today the built-in `codex` preset is configured as a non-hook runtime, so Gas Town relies on fallback behavior such as wrapper-driven `gt prime`. The first implementation should add a separate hook-capable preset rather than changing the default `codex` preset, because Gas Town does not currently have a reliable runtime check for whether Codex hooks are actually enabled in the user's Codex config.

The intended audience is maintainers and users who explicitly want to try Codex's pre-release hooks integration behind the existing upstream feature flag.

## Design

### Agent preset

Add a new built-in agent preset alongside the existing `codex` preset in `internal/config/agents.go`.

The new preset should:
- Use the same underlying `codex` binary and the same general Codex runtime shape as the current preset.
- Set `SupportsHooks: true`.
- Set `PromptMode: "arg"`.
- Point `HooksProvider` at a new Codex provider.
- Point `HooksDir` at `.codex`.
- Point `HooksSettingsFile` at `hooks.json`.
- Leave the existing `codex` preset unchanged.

Recommended preset name: `codex-hooks`.

This follows Gastown's existing preset model rather than introducing a new product concept. Users opt into the new behavior by selecting the new preset through normal agent selection config or `--agent codex-hooks`.

### Hook installer integration

Reuse the existing generic hook installer in `internal/hooks/installer.go`. No new installer mechanism should be added.

Add provider templates under `internal/hooks/templates/codex/`:
- `hooks-interactive.json`
- `hooks-autonomous.json`

The templates should be intentionally minimal and mirror existing role-aware hook providers:
- Interactive `SessionStart`: run `gt prime --hook`
- Autonomous `SessionStart`: run `gt prime --hook && gt mail check --inject`
- `Stop`: run `gt costs record`

The templates should also preserve the existing PATH bootstrapping pattern used in other hook providers where needed so the `gt` binary is available when hooks fire.

### Runtime behavior

Do not add special-case runtime logic for Codex in the first pass.

The feature should work through the existing runtime and fallback model:
- `internal/runtime/runtime.go` already suppresses startup fallback commands when a preset has a non-informational hooks provider.
- With `PromptMode: "arg"`, the new `codex-hooks` preset follows Gastown's `hooks + prompt` startup path: native `SessionStart` handles priming and the initial beacon/work instructions can be delivered as the startup prompt.
- The existing `codex` preset will continue to use today's non-hook fallback path because it remains hook-disabled.

This keeps the rollout safe: users who select the existing `codex` preset get current behavior, while users who select `codex-hooks` are explicitly choosing the experimental path and are responsible for enabling Codex's upstream hook feature.

### Documentation

Update `docs/agent-provider-integration.md` to document:
- `codex-hooks` is experimental and opt-in.
- Users must enable `[features].codex_hooks = true` in Codex config.
- V1 supports only `SessionStart` and `Stop`.
- The default `codex` preset remains on the fallback path.
- Existing wrapper-based guidance such as `gt-codex` remains relevant for non-hook/default Codex usage.

### Test coverage

Extend `internal/hooks/installer_test.go` with Codex provider coverage that mirrors existing role-aware provider tests.

Tests should verify:
- Interactive roles receive `hooks-interactive.json` content in `.codex/hooks.json`.
- Autonomous roles receive `hooks-autonomous.json` content in `.codex/hooks.json`.
- Installation uses the existing role-aware template resolution path rather than Codex-specific branching.

## Scope

In:
- Add a new built-in `codex-hooks` preset in `internal/config/agents.go`.
- Add Codex hook provider templates for interactive and autonomous roles.
- Add installer tests for the new provider.
- Update docs for configuration, prerequisites, and limitations.

Out:
- Changing the default `codex` preset.
- Automatic detection of whether Codex hooks are enabled in user config.
- Additional hook types such as prompt submission, dangerous-command guards, or pre-compaction flows.
- Full Claude-style hook parity.
- Wrapper-script redesign beyond documenting the relationship between the default fallback path and the new hook-capable preset.

## Non-Negotiables

- [N-1] The existing built-in `codex` preset must remain unchanged in v1.
- [N-2] The feature must ship as an explicit opt-in preset selected through Gastown's normal agent selection flow.
- [N-3] V1 must be limited to `SessionStart` and `Stop`.
- [N-4] The implementation must reuse the existing hook installer and runtime plumbing where possible, including Gastown's normal prompt delivery path.
- [N-5] Docs must state that Codex's upstream `[features].codex_hooks = true` prerequisite is required.

## Forbidden Approaches

- [F-1] Do not flip the existing default `codex` preset to hook-capable without a reliable hook-availability safeguard, because that can suppress fallback priming for users who do not have Codex hooks enabled.
- [F-2] Do not expand the first pass into broader hook parity work, because the intended slice is a small, reviewable lifecycle integration.
- [F-3] Do not add Codex-specific installer or runtime special cases if the existing provider/template abstraction is sufficient.

## Decision Log

| Decision ID | Topic | Chosen Option | Rejected Alternatives | Rationale | Status |
|-------------|-------|---------------|------------------------|-----------|--------|
| D-1 | How users access the feature | Add separate `codex-hooks` preset | Change default `codex`; add detection first then switch default | Separate preset uses Gastown's existing selection model and avoids regressing current Codex users | Resolved |
| D-2 | Initial hook surface | Support only `SessionStart` and `Stop` | Add prompt/mail/tool guard parity in v1 | Smallest useful slice that matches the deferred bead and existing upstream hook availability | Resolved |
| D-3 | Prompt handling for `codex-hooks` | Use `PromptMode: "arg"` | Keep `PromptMode: "none"` on the new preset | If Codex accepts an initial prompt reliably, `hooks + prompt` is Gastown's cleaner startup path and avoids extra startup nudges | Resolved |
| D-4 | Runtime integration strategy | Reuse existing installer and fallback/runtime plumbing | Add Codex-only runtime branches or a new installer path | Lower risk, smaller diff, and already fits Gastown's provider model | Resolved |
| D-5 | Startup behavior for default Codex users | Keep fallback behavior for `codex`; require explicit opt-in for `codex-hooks` | Change wrapper or startup assumptions globally | Preserves today's working path while allowing experimentation behind a clear boundary | Resolved |

## Traceability

| Spec Element | Source | Notes |
|--------------|--------|-------|
| Non-Negotiables N-1 through N-5 | `gas-3ag` + user discussion | Captures the deferred bead's safety constraints plus the explicit agreement that preset selection is opt-in and not auto-detected from the CLI binary |
| Agent preset design | `docs/plans/codex-hooks/codebase-context.tmp` + `internal/config/agents.go` + user discussion | Grounded in the current built-in preset model and updated to use prompt delivery for the new hook-capable preset |
| Hook installer design | `docs/plans/codex-hooks/codebase-context.tmp` + `internal/hooks/installer.go` + `internal/hooks/installer_test.go` | Reuses the generic provider/template path already used by other runtimes |
| Runtime behavior section | `docs/plans/codex-hooks/codebase-context.tmp` + `internal/runtime/runtime.go` + `internal/config/types.go` | Included because fallback suppression is the key reason changing default `codex` is risky, and prompt delivery changes startup orchestration for the new preset |
| Documentation scope | `gas-3ag` + `docs/agent-provider-integration.md` | Ensures the feature flag prerequisite and fallback/default distinction are visible to users |
| Decisions D-1 through D-5 | user discussion + `gas-3ag` | Serializes the main trade-offs discussed before implementation |

## Risks

| Risk | Impact | Mitigation |
|------|--------|------------|
| Codex hook payload or file format differs from expectation | Hook integration may fail at runtime even if the preset installs cleanly | Keep templates minimal, verify against current Codex hooks behavior during implementation, and avoid broader feature assumptions |
| Users select `codex-hooks` without enabling the upstream feature flag | Startup context may not arrive because fallback behavior is intentionally bypassed for the hook-capable preset | Make the prerequisite explicit in docs and keep the default `codex` preset unchanged |
| Future maintainers assume parity with Claude hooks | Scope creep or incorrect expectations for unsupported hook events | Document the limited v1 surface clearly in both the spec and user-facing docs |

## Testing

- Add installer tests covering interactive and autonomous Codex template selection in `internal/hooks/installer_test.go`.
- Verify the new preset points at `.codex/hooks.json` and uses the existing provider installer path.
- Verify `codex-hooks` resolves to `PromptMode: "arg"` so startup follows the `hooks + prompt` path.
- Run targeted Go tests for hook installer coverage.
- Manually inspect generated docs text to confirm the opt-in and feature-flag prerequisites are clear.

## Open Questions

- Whether maintainers prefer the preset name `codex-hooks` or a more explicit experimental variant such as `codex-experimental-hooks`.
- Whether follow-up work should later add hook-availability detection before considering any change to the default `codex` preset.
