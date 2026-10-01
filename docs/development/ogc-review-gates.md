[Documentation index](../README.md) · [Roles](ogc-team-roles.md)

# Feature-service review and quality gates

## Required workflow

1. The owner checks the pinned baseline, applicable project rules, phase acceptance criteria and requirement sources before editing. Requirement conflicts stop dependent work and go to S1/product owner for an ADR and plan amendment.
2. Use one task per branch/PR where practical; documentation Tasks 1–6 form the declared architecture checkpoint. At most four implementation PRs may run concurrently in a dependency layer, with explicit file ownership and no conflicting shared-file edits.
3. The PR names task IDs, authoritative sources, changed paths, exact candidate state, commands, fixtures/artifacts, skips and gate impact. Future tests are requirements, not results.
4. Review in order: independent domain review, then M5 verification/security for relevant changes, then S1 for public contracts and gates. Record actual identities, scopes and verdicts. Authors cannot approve their own work; test authors also need independent review.
5. P0/P1 material findings block merge. After at most two correction rounds for a nontrivial review, unresolved design conflicts go to S1. Re-review the corrected candidate, not the earlier revision.
6. A merged dependency layer gets an integrated verification pass on its exact resulting candidate before the next layer opens. A role name or verbal assertion does not establish PASS.

## Gates
| Gate | Required evidence | Blockers |
|---|---|---|
| G0 Architecture & Governance | spec, roles/RACI, source map, ADR policy, traceability seed, S1 sign-off | unresolved public-contract choice |
| G1 Query Core | interface/contract tests, cancellation/stream semantics, no MVT regression | handler-shaped provider API, duplicated core |
| G2 GPKG Vertical Slice | real feature query from GPKG to domain feature, CRS84 output fixture | fake TileFeatures path |
| G3 Resource Graph | landing/conformance/collections links and content negotiation | broken links/incorrect advertised capabilities |
| G4 Part 1 Vertical Slice | `/items` + `/items/{featureId}` + limit/bbox/datetime + GeoJSON tests | Part 1 required behavior absent |
| G5 Provider Parity | same contract suite across declared providers; capability matrix | silent semantic divergence |
| G6 Filter Safety | typed AST, queryables boundary, compiler contract, injection/fuzz | raw user text in SQL |
| G7 Filtering | Part 3/CQL2 declared subset + parity tests | unsupported behavior advertised |
| G8 CRS | supported-CRS matrix, crs/bbox-crs/Content-Crs, Part 2 evidence | silent CRS fallback/axis error |
| G9 Protocol Candidate | generated OpenAPI matches runtime; HTML/JSON; HEAD/error/proxy checks | advertised/runtime mismatch |
| G10 Production Candidate | official conformance evidence, security, race, load, cancellation, MVT regression | P0/P1, failed required ETS, unstable perf |
| G11 Jivan Retirement | migration matrix closed, Lambda parity/disposition, deployment/rollback docs | reachable Jivan behavior unaccounted |


## Reviewer checklists

S1 checks service/provider boundaries; duplicate query/decoder pipelines; provider-neutral semantics; cancellation; explicit CRS assumptions; bound SQL values/validated identifiers; safe errors; updated documentation/traceability; MVT compatibility evidence; and proven acceptance criteria. Public contract and conformance changes require an ADR and S1 disposition.

M5 checks positive and negative cases; empty/null/malformed geometry; cancellation; provider parity; races/concurrency; injection/fuzz when applicable; HTTP status/content/link behavior; exact candidate revision, dirty files and environment. Every skipped check is explicit. For documentation-only G0, review source manifests, task ownership, traceability, local links, architecture and governance; do not invent runtime test results.

Domain reviewers use the high-risk path map in the role charter. Security-sensitive compilers require parameterization/injection evidence. Rules and maintainability checks apply to the actual changed paths. Required runtime/ETS failures cannot be replaced by code inspection.

## Evidence and transition

M5 owns verification evidence for every gate; affected domains supply inputs and independent verdicts; S1 records acceptance. Gate fields: gate, candidate SHA, UTC timestamp, dirty-state digest, environment/tool versions, fixtures, exact commands, exit/results, artifact links and digests, reviewer identity/verdict, M5 verifier, S1 disposition and bounded waivers with owner/expiry. PASS opens the next dependency layer. WARN opens it only with a recorded bounded non-blocking S1 waiver; FAIL blocks it.

## See Also

- [Team roles and reviewer map](ogc-team-roles.md)
- [Feature-service architecture](../architecture/feature-service.md)
- [Migration traceability](../migration/jivan-feature-matrix.md)
- [Fork verification policy](../../UPSTREAM.md)

## Branch and worktree policy

The feature integration branch starts from reviewed master baseline c65beeb8519f425ff8365c76e54e93baf8e17b07. Compare the actual master and integration baseline at preflight; record drift instead of assuming equality. All working branch checkouts belong under D:/projects/externals/copilot-worktrees/tegola. New task branch names use kkk/ unless an explicit task instruction selects a different name. Existing feature/ogc-features-convergence is the selected integration branch. Use one task PR where practical and explicit ownership for shared files.

Task owners cannot directly merge their own task into integration. An independent authorized reviewer supplies the domain verdict; S1 accepts public-contract changes and advances dependency layers after integrated verification. Merge/release authority must be assigned to an actual actor; a role label does not grant permission.

Agent skills, local plans, research inputs, execution ledgers and agent context remain local. Exclude .ai-factory/, .agents/, .codex/ and AGENTS.md through local Git excludes and inspect the staged file list before any commit. Do not attach these artifacts to a PR or add Co-authored-by trailers. Public architecture, migration and contributor documentation are separate reviewable product deliverables.

## Exact candidate evidence and local fallback

Evidence must identify the commit SHA plus dirty-worktree status and a SHA-256 digest inventory of the changed candidate files. Record UTC timestamp, OS/architecture, Go/compiler/database versions, build tags, suite versions, fixture identity/digest, exact commands, outputs/exit codes, skips, actual reviewer identities, M5 sign-off and S1 disposition. Changing reviewed content invalidates the matching evidence and requires re-verification. A previous SHA or untracked-file-free diff alone does not identify a dirty candidate.

Prefer CI evidence. If CI is unavailable, use the current fork fallback from UPSTREAM.md, with each command and result recorded independently:

```powershell
$env:CGO_ENABLED = '0'
go test -mod vendor -count=1 ./...
$env:CGO_ENABLED = '1'
go test -mod vendor -count=1 ./...
golangci-lint run ./...
```

Restore the caller's CGO_ENABLED setting afterwards. Missing CGO toolchain, database fixtures, linter or official ETS suite is an explicit skip/blocker for affected gates, not a passing result. This fallback is a requirement for future implementation verification; these commands have not been run for the documentation-only G0 candidate. M5 sign-off is required from G1 onward; G0 consumes independent source/architecture/traceability/governance review and records S1 acceptance.

## ADR and change control

After G0, public provider/config/protocol/CRS/filter/capability changes require an ADR, updated task dependencies/acceptance criteria and traceability, independent domain/M5 review and S1 approval before dependent implementation or merge. Implementation details within a phase can be resolved by the owner; intentional provider divergence, compatibility breaks, waivers and retirement require explicit decisions. A normative conflict stops dependent work until S1/product owner resolves it; never invent a compromise inside code.

Store contributor-facing ADRs in docs/architecture/decisions/. Required record:

```markdown
# ADR-NNN: Title
Status: proposed|accepted|superseded|rejected|expired
Date:
Decision owner: S1 (actual actor recorded with verdict)
Affected tasks/gates:
Sources:
## Context
## Constraints / normative requirements
## Considered options
## Decision
## Consequences
## Compatibility and migration
## Verification required
## Expiry/follow-up (for waivers)
```

Waivers name the blocker, risk, bounded scope, owner, expiry, required follow-up and release impact. A failed required security/conformance check cannot become PASS by relabeling it. Superseded ADRs preserve history and point to the replacement.
