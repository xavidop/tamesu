<!-- Thanks for contributing to tamesu! Please fill this out so reviewers have context. -->

## Summary

<!-- What does this change do, and why is it needed? Link any related issue. -->

Closes #

## Type of change

- [ ] feat (non-breaking)
- [ ] fix (non-breaking)
- [ ] Breaking change
- [ ] New or changed target
- [ ] docs
- [ ] ci / release / tooling

## Checklist

- [ ] `make all` passes
- [ ] New or changed behaviour is covered by tests
- [ ] README, `site/src/pages/docs/` and `skills/tamesu/SKILL.md` are updated (the claim checks fail otherwise)
- [ ] No secret, API key or token appears in code, fixtures, logs or artifacts
- [ ] Every model call goes through the `llm` module (Genkit), every secret through mamori

## New or changed target

- [ ] The target passes the `targettest` conformance suite
- [ ] It lives in its own module with a dependabot entry and a `go.work` entry
- [ ] Its docs page, the targets table and the sidebar are updated

## Notes for reviewers

<!-- Anything else worth calling out. -->
